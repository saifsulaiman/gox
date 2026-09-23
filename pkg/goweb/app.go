// Package goweb provides GoxWeb: A High-Performance Web Framework for Go / GOX.
// It combines standard Go compatibility with zero-GC Request Arenas and built-in
// enterprise support for SQLite, PostgreSQL, Redis, RabbitMQ, and Elasticsearch.
//
// DISCLAIMER: This software is provided "AS IS", without warranty of any kind.
// In no event shall the authors or contributors be held liable for any damages
// or claims arising from its use. See the root LICENSE file for full terms.
package goweb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goxlang/gox/pkg/goxrt"
)

// Config configures the GoxWeb Engine and its integrated services.
type Config struct {
	Port            int           `json:"port"`
	Host            string        `json:"host"`
	ReadTimeout     time.Duration `json:"read_timeout"`
	WriteTimeout    time.Duration `json:"write_timeout"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`
	DisableArena    bool          `json:"disable_arena"`
	Logger          *slog.Logger  `json:"-"`

	// Multi-Service Declarative Configuration
	SQLite           string   `json:"sqlite"`            // SQLite file path or ":memory:"
	SQLiteReplicas   []string `json:"sqlite_replicas"`   // SQLite read replicas (slaves)
	Postgres         string   `json:"postgres"`          // PostgreSQL DSN / URL
	PostgresReplicas []string `json:"postgres_replicas"` // PostgreSQL read replicas (slaves)
	PgBouncer        bool     `json:"pgbouncer"`         // Enable PgBouncer-specific connection optimization
	Redis            string   `json:"redis"`             // Redis server address (e.g. "localhost:6379" or "memory")
	RabbitMQ         string   `json:"rabbitmq"`          // RabbitMQ AMQP URL (e.g. "amqp://..." or "memory")
	Elastic          string   `json:"elastic"`           // Elasticsearch URL (e.g. "http://localhost:9200")
}

// Engine is the central framework instance managing routing, middleware,
// service lifecycles, and HTTP execution.
type Engine struct {
	cfg          Config
	logger       *slog.Logger
	router       *Router
	middlewares  []HandlerFunc
	server       *http.Server
	sqlite       *Database
	postgres     *Database
	redis        *RedisClient
	queue        *QueueClient
	search       *SearchClient
	bloom        *BloomFilter
	singleflight *SingleflightGroup
	pool         sync.Pool
}

// New creates and initializes a production-ready GoxWeb application.
func New(cfg ...Config) *Engine {
	c := Config{
		Port:            8080,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		ShutdownTimeout: 5 * time.Second,
	}
	if len(cfg) > 0 {
		userCfg := cfg[0]
		c.Port = cmp.Or(userCfg.Port, 8080)
		c.Host = userCfg.Host
		c.ReadTimeout = cmp.Or(userCfg.ReadTimeout, 10*time.Second)
		c.WriteTimeout = cmp.Or(userCfg.WriteTimeout, 10*time.Second)
		c.ShutdownTimeout = cmp.Or(userCfg.ShutdownTimeout, 5*time.Second)
		c.DisableArena = userCfg.DisableArena
		c.Logger = userCfg.Logger
		c.SQLite = userCfg.SQLite
		c.SQLiteReplicas = userCfg.SQLiteReplicas
		c.Postgres = userCfg.Postgres
		c.PostgresReplicas = userCfg.PostgresReplicas
		c.PgBouncer = userCfg.PgBouncer
		c.Redis = userCfg.Redis
		c.RabbitMQ = userCfg.RabbitMQ
		c.Elastic = userCfg.Elastic
	}

	logger := cmp.Or(c.Logger, slog.Default())

	e := &Engine{
		cfg:          c,
		logger:       logger,
		router:       NewRouter(),
		bloom:        NewBloomFilter(100000, 0.01),
		singleflight: NewSingleflightGroup(),
	}
	e.pool.New = func() any {
		return &Context{
			engine: e,
			index:  -1,
			status: http.StatusOK,
			Params: make(Params, 0, 8),
		}
	}

	// Initialize SQLite if configured or default to in-memory SQLite
	if c.SQLite != "" {
		db, err := NewDatabase(DBConfig{
			Driver:   DBSQLite,
			DSN:      c.SQLite,
			Replicas: c.SQLiteReplicas,
		})
		if err != nil {
			logger.Warn("SQLite init failed, activating in-memory fallback", slog.Any("error", err))
		} else {
			e.sqlite = db
		}
	}
	if e.sqlite == nil {
		db, _ := NewDatabase(DBConfig{
			Driver:   DBSQLite,
			DSN:      "file::memory:?cache=shared&mode=rwc",
			Replicas: c.SQLiteReplicas,
		})
		e.sqlite = db
	}

	// Initialize PostgreSQL if configured
	if c.Postgres != "" {
		db, err := NewDatabase(DBConfig{
			Driver:    DBPostgres,
			DSN:       c.Postgres,
			Replicas:  c.PostgresReplicas,
			PgBouncer: c.PgBouncer,
		})
		if err != nil {
			logger.Warn("Postgres init failed", slog.Any("error", err))
		} else {
			e.postgres = db
		}
	}

	// Initialize Redis client
	if c.Redis != "" {
		e.redis = NewRedisClient(RedisConfig{Addr: c.Redis})
	} else {
		e.redis = NewRedisClient(RedisConfig{Addr: "memory"})
	}

	// Initialize RabbitMQ client
	if c.RabbitMQ != "" {
		e.queue = NewQueueClient(RabbitMQConfig{URL: c.RabbitMQ})
	} else {
		e.queue = NewQueueClient(RabbitMQConfig{URL: "memory"})
	}

	// Initialize Elasticsearch client
	if c.Elastic != "" {
		addrs := []string{c.Elastic}
		if strings.Contains(c.Elastic, ",") {
			addrs = strings.Split(c.Elastic, ",")
		}
		e.search = NewSearchClient(ElasticsearchConfig{Addresses: addrs})
	} else {
		e.search = NewSearchClient(ElasticsearchConfig{})
	}

	// Register automated /health, /ready, and /metrics endpoints
	e.registerHealthEndpoints()

	return e
}

// Use adds global middlewares to the application execution stack.
func (e *Engine) Use(middlewares ...HandlerFunc) {
	e.middlewares = append(e.middlewares, middlewares...)
}

// GET registers a GET route handler.
func (e *Engine) GET(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodGet, pattern, handlers...)
}

// POST registers a POST route handler.
func (e *Engine) POST(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPost, pattern, handlers...)
}

// PUT registers a PUT route handler.
func (e *Engine) PUT(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPut, pattern, handlers...)
}

// DELETE registers a DELETE route handler.
func (e *Engine) DELETE(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodDelete, pattern, handlers...)
}

// PATCH registers a PATCH route handler.
func (e *Engine) PATCH(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodPatch, pattern, handlers...)
}

// HEAD registers a HEAD route handler.
func (e *Engine) HEAD(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodHead, pattern, handlers...)
}

// OPTIONS registers an OPTIONS route handler.
func (e *Engine) OPTIONS(pattern string, handlers ...HandlerFunc) {
	e.Handle(http.MethodOptions, pattern, handlers...)
}

// Any registers a route matching all standard HTTP methods.
func (e *Engine) Any(pattern string, handlers ...HandlerFunc) {
	methods := []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions,
	}
	for _, m := range methods {
		e.Handle(m, pattern, handlers...)
	}
}

// Handle registers a route for the given HTTP method and pattern.
func (e *Engine) Handle(method, pattern string, handlers ...HandlerFunc) {
	combined := make([]HandlerFunc, 0, len(e.middlewares)+len(handlers))
	combined = append(combined, e.middlewares...)
	combined = append(combined, handlers...)
	e.router.AddRoute(method, pattern, combined)
}

// RouteGroup represents a prefix-grouped subset of routes with shared middlewares.
type RouteGroup struct {
	prefix      string
	engine      *Engine
	middlewares []HandlerFunc
}

// Group creates a new route group with a common URL prefix and group middlewares.
func (e *Engine) Group(prefix string, middlewares ...HandlerFunc) *RouteGroup {
	return &RouteGroup{
		prefix:      prefix,
		engine:      e,
		middlewares: middlewares,
	}
}

// Group creates a nested route group.
func (g *RouteGroup) Group(prefix string, middlewares ...HandlerFunc) *RouteGroup {
	combined := make([]HandlerFunc, 0, len(g.middlewares)+len(middlewares))
	combined = append(combined, g.middlewares...)
	combined = append(combined, middlewares...)
	return &RouteGroup{
		prefix:      g.prefix + prefix,
		engine:      g.engine,
		middlewares: combined,
	}
}

// Handle registers a route within the group.
func (g *RouteGroup) Handle(method, pattern string, handlers ...HandlerFunc) {
	fullPath := g.prefix + pattern
	combined := make([]HandlerFunc, 0, len(g.middlewares)+len(handlers))
	combined = append(combined, g.middlewares...)
	combined = append(combined, handlers...)
	g.engine.Handle(method, fullPath, combined...)
}

// GET registers a GET route within the group.
func (g *RouteGroup) GET(pattern string, handlers ...HandlerFunc) {
	g.Handle(http.MethodGet, pattern, handlers...)
}

// POST registers a POST route within the group.
func (g *RouteGroup) POST(pattern string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPost, pattern, handlers...)
}

// PUT registers a PUT route within the group.
func (g *RouteGroup) PUT(pattern string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPut, pattern, handlers...)
}

// DELETE registers a DELETE route within the group.
func (g *RouteGroup) DELETE(pattern string, handlers ...HandlerFunc) {
	g.Handle(http.MethodDelete, pattern, handlers...)
}

// PATCH registers a PATCH route within the group.
func (g *RouteGroup) PATCH(pattern string, handlers ...HandlerFunc) {
	g.Handle(http.MethodPatch, pattern, handlers...)
}

// SQLite returns the SQLite database instance.
func (e *Engine) SQLite() *Database {
	return e.sqlite
}

// Postgres returns the PostgreSQL database instance.
func (e *Engine) Postgres() *Database {
	return e.postgres
}

// DB returns primary database (PostgreSQL if present, else SQLite).
func (e *Engine) DB() *Database {
	if e.postgres != nil {
		return e.postgres
	}
	return e.sqlite
}

// Redis returns the Redis client instance.
func (e *Engine) Redis() *RedisClient {
	return e.redis
}

// Queue returns the RabbitMQ / message queue client.
func (e *Engine) Queue() *QueueClient {
	return e.queue
}

// Search returns the Elasticsearch / full-text search client.
func (e *Engine) Search() *SearchClient {
	return e.search
}

// Bloom returns the integrated high-speed Bloom Filter.
func (e *Engine) Bloom() *BloomFilter {
	return e.bloom
}

// Singleflight returns the integrated singleflight deduplication engine.
func (e *Engine) Singleflight() *SingleflightGroup {
	return e.singleflight
}

// ServeHTTP implements http.Handler with GOX Request Arena zero-GC acceleration.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var arena *goxrt.Arena
	if !e.cfg.DisableArena {
		arena = goxrt.GetArena()
		defer goxrt.PutArena(arena)
	}

	c := e.pool.Get().(*Context)
	c.reset(w, r, e, arena)
	defer e.pool.Put(c)

	var params Params
	handlers := e.router.Match(r.Method, r.URL.Path, &params)
	if handlers == nil {
		_ = c.Error(http.StatusNotFound, "route not found")
		return
	}

	c.Params = params
	c.handlers = handlers
	_ = c.Next()
}

// Run starts the HTTP server on the configured port and listens for graceful shutdown.
func (e *Engine) Run(addr ...string) error {
	listenAddr := fmt.Sprintf("%s:%d", e.cfg.Host, e.cfg.Port)
	if len(addr) > 0 && addr[0] != "" {
		listenAddr = addr[0]
	}

	e.server = &http.Server{
		Addr:         listenAddr,
		Handler:      e,
		ReadTimeout:  e.cfg.ReadTimeout,
		WriteTimeout: e.cfg.WriteTimeout,
	}

	// Server shutdown channel
	shutdownErr := make(chan error, 1)
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		e.logger.Info("Graceful shutdown initiated")
		ctx, cancel := context.WithTimeout(context.Background(), e.cfg.ShutdownTimeout)
		defer cancel()

		var errs []error
		// Close server and active connections
		if err := e.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}

		// Close databases and service pools
		if e.sqlite != nil {
			if err := e.sqlite.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if e.postgres != nil {
			if err := e.postgres.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if e.redis != nil {
			if err := e.redis.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if e.queue != nil {
			if err := e.queue.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if e.search != nil {
			if err := e.search.Close(); err != nil {
				errs = append(errs, err)
			}
		}

		shutdownErr <- errors.Join(errs...)
	}()

	e.logger.Info("Starting GoxWeb server",
		slog.String("addr", listenAddr),
		slog.Bool("arena_enabled", !e.cfg.DisableArena),
	)
	if err := e.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return <-shutdownErr
}
