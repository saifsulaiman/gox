package goweb

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/goxlang/gox/pkg/goxrt"
)

// H is a convenient shortcut for map[string]any used in JSON payloads.
type H map[string]any

// HandlerFunc defines the request handler signature in GoxWeb.
type HandlerFunc func(c *Context) error

// Param represents a single URL route parameter.
type Param struct {
	Key   string
	Value string
}

// Params is a list of parameters.
type Params []Param

// Get returns the value of the first Param which key matches the given name.
func (ps Params) Get(name string) string {
	idx := slices.IndexFunc(ps, func(p Param) bool {
		return p.Key == name
	})
	if idx != -1 {
		return ps[idx].Value
	}
	return ""
}

// All yields an iterator over all parameter key-value pairs (Go 1.23+).
func (ps Params) All() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for _, p := range ps {
			if !yield(p.Key, p.Value) {
				return
			}
		}
	}
}

// Context represents the context of the current HTTP request.
// It holds request/response state, route parameters, and provides
// access to the GOX Request Arena and integrated service clients.
type Context struct {
	Request  *http.Request
	Writer   http.ResponseWriter
	Params   Params
	handlers []HandlerFunc
	index    int
	status   int
	written  bool
	engine   *Engine
	arena    *goxrt.Arena
	keys     map[string]any
	keysMu   sync.RWMutex
	logger   *slog.Logger
}

// newContext returns a freshly initialized context.
func newContext(w http.ResponseWriter, r *http.Request, engine *Engine, arena *goxrt.Arena) *Context {
	return &Context{
		Request:  r,
		Writer:   w,
		engine:   engine,
		arena:    arena,
		index:    -1,
		status:   http.StatusOK,
		Params:   make(Params, 0, 8),
	}
}

// Reset resets the context for reuse from a pool.
func (c *Context) Reset(w http.ResponseWriter, r *http.Request) {
	c.Request = r
	c.Writer = w
	c.handlers = nil
	c.index = -1
	c.status = http.StatusOK
	c.written = false
	clear(c.Params)
	c.Params = c.Params[:0]
	c.keysMu.Lock()
	if c.keys != nil {
		clear(c.keys)
	}
	c.keysMu.Unlock()
	c.logger = nil
}

// reset resets the context with engine and arena for internal pool recycling.
func (c *Context) reset(w http.ResponseWriter, r *http.Request, engine *Engine, arena *goxrt.Arena) {
	c.Reset(w, r)
	c.engine = engine
	c.arena = arena
}

// Next executes the pending handlers in the chain inside the calling handler.
func (c *Context) Next() error {
	c.index++
	for c.index < len(c.handlers) {
		if err := c.handlers[c.index](c); err != nil {
			return err
		}
		c.index++
	}
	return nil
}

// Abort prevents pending handlers from being called.
func (c *Context) Abort() {
	c.index = len(c.handlers)
}

// AbortWithStatus calls Abort and writes the HTTP status code.
func (c *Context) AbortWithStatus(code int) {
	c.Status(code)
	if !c.written {
		c.Writer.WriteHeader(code)
		c.written = true
	}
	c.Abort()
}

// AbortWithError calls Abort, sets status code, and writes a structured JSON error.
func (c *Context) AbortWithError(code int, err error) error {
	c.Abort()
	return c.JSON(code, H{"error": err.Error(), "status": code})
}

// AbortWithJSON aborts pending handlers and writes a JSON response payload.
func (c *Context) AbortWithJSON(code int, obj any) error {
	c.Abort()
	return c.JSON(code, obj)
}

// AbortWithBlob aborts pending handlers and writes a raw byte payload with content-type.
func (c *Context) AbortWithBlob(code int, contentType string, b []byte) error {
	c.Abort()
	return c.Blob(code, contentType, b)
}

// IsAborted returns true if the context handler chain was aborted.
func (c *Context) IsAborted() bool {
	return c.index >= len(c.handlers)
}

// Method returns the HTTP request method (e.g. GET, POST).
func (c *Context) Method() string {
	if c.Request != nil {
		return c.Request.Method
	}
	return ""
}

// Context returns the standard Go context.Context of the underlying request.
func (c *Context) Context() context.Context {
	return c.Request.Context()
}

// Arena returns the active GOX Request Arena, or nil if arena is disabled.
func (c *Context) Arena() *goxrt.Arena {
	if c.arena != nil {
		return c.arena
	}
	if c.Request != nil {
		return goxrt.GetRequestArena(c.Request.Context())
	}
	return nil
}

// Param returns the value of a URL parameter.
func (c *Context) Param(key string) string {
	return c.Params.Get(key)
}

// ParamInt parses a URL parameter as an integer.
func (c *Context) ParamInt(key string) (int, error) {
	val := c.Param(key)
	if val == "" {
		return 0, fmt.Errorf("param %q is missing", key)
	}
	return strconv.Atoi(val)
}

// Query returns the keyed url query value if it exists, otherwise empty string.
func (c *Context) Query(key string) string {
	return c.Request.URL.Query().Get(key)
}

// QueryDefault returns the keyed url query value or defaultValue if empty.
func (c *Context) QueryDefault(key, defaultValue string) string {
	return cmp.Or(c.Query(key), defaultValue)
}

// Logger returns the contextual structured logger for this request.
func (c *Context) Logger() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	if c.engine != nil && c.engine.logger != nil {
		return c.engine.logger
	}
	return slog.Default()
}

// SetLogger sets a custom contextual structured logger for this request.
func (c *Context) SetLogger(l *slog.Logger) {
	c.logger = l
}

// Header returns the request header value for the given key.
func (c *Context) Header(key string) string {
	return c.Request.Header.Get(key)
}

// SetHeader sets a response header.
func (c *Context) SetHeader(key, val string) {
	c.Writer.Header().Set(key, val)
}

// Status sets the HTTP response status code.
func (c *Context) Status(code int) {
	c.status = code
}

// StatusCode returns the current HTTP response status code.
func (c *Context) StatusCode() int {
	return c.status
}

// Set stores a key/value pair exclusively for this context.
func (c *Context) Set(key string, val any) {
	c.keysMu.Lock()
	if c.keys == nil {
		c.keys = make(map[string]any)
	}
	c.keys[key] = val
	c.keysMu.Unlock()
}

// Get returns the value for the given key.
func (c *Context) Get(key string) (any, bool) {
	c.keysMu.RLock()
	defer c.keysMu.RUnlock()
	if c.keys == nil {
		return nil, false
	}
	val, ok := c.keys[key]
	return val, ok
}

// BindJSON reads the request body and unmarshals JSON into the given pointer.
func (c *Context) BindJSON(target any) error {
	defer c.Request.Body.Close()
	decoder := json.NewDecoder(c.Request.Body)
	return decoder.Decode(target)
}

// JSON serializes the given struct as JSON into the response body.
func (c *Context) JSON(code int, obj any) error {
	c.SetHeader("Content-Type", "application/json; charset=utf-8")
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}

	encoder := json.NewEncoder(c.Writer)
	return encoder.Encode(obj)
}

// JSONFast writes raw pre-formatted JSON bytes directly.
func (c *Context) JSONFast(code int, jsonBytes []byte) error {
	c.SetHeader("Content-Type", "application/json; charset=utf-8")
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}
	_, err := c.Writer.Write(jsonBytes)
	return err
}

// String writes a formatted string to the response body.
func (c *Context) String(code int, format string, values ...any) error {
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}
	_, err := fmt.Fprintf(c.Writer, format, values...)
	return err
}

// HTML writes an HTML string to the response body.
func (c *Context) HTML(code int, html string) error {
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}
	_, err := io.WriteString(c.Writer, html)
	return err
}

// Bytes writes raw bytes with the specified content type.
func (c *Context) Bytes(code int, contentType string, data []byte) error {
	c.SetHeader("Content-Type", contentType)
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}
	_, err := c.Writer.Write(data)
	return err
}

// Blob writes raw bytes with the specified content type (alias for Bytes).
func (c *Context) Blob(code int, contentType string, data []byte) error {
	return c.Bytes(code, contentType, data)
}

// NoContent returns an empty response with the specified status code.
func (c *Context) NoContent(code int) error {
	if !c.written {
		c.Writer.WriteHeader(code)
		c.status = code
		c.written = true
	}
	return nil
}

// Error writes a structured JSON error response.
func (c *Context) Error(code int, message string) error {
	return c.JSON(code, H{
		"error":  message,
		"status": code,
	})
}

// SQLite returns the configured SQLite database instance.
func (c *Context) SQLite() *Database {
	if c.engine == nil {
		return nil
	}
	return c.engine.sqlite
}

// Postgres returns the configured PostgreSQL database instance.
func (c *Context) Postgres() *Database {
	if c.engine == nil {
		return nil
	}
	return c.engine.postgres
}

// DB returns the primary configured database (Postgres if present, else SQLite).
func (c *Context) DB() *Database {
	if c.engine == nil {
		return nil
	}
	if c.engine.postgres != nil {
		return c.engine.postgres
	}
	return c.engine.sqlite
}

// UsePrimaryDB forces all subsequent database read queries in the current HTTP request
// to execute against the Primary/Master database rather than read replicas (slaves).
// This guarantees read-your-own-writes consistency after an INSERT, UPDATE, or DELETE.
func (c *Context) UsePrimaryDB() {
	if c.Request != nil {
		c.Request = c.Request.WithContext(WithPrimary(c.Request.Context()))
	}
}

// ReadDB returns a read replica using round-robin (or Primary if forced by UsePrimaryDB or no replicas).
func (c *Context) ReadDB() *sql.DB {
	db := c.DB()
	if db == nil {
		return nil
	}
	return db.ReadDBContext(c.Context())
}

// WriteDB returns the primary writer database.
func (c *Context) WriteDB() *sql.DB {
	db := c.DB()
	if db == nil {
		return nil
	}
	return db.WriteDB()
}

// Redis returns the integrated Redis client.
func (c *Context) Redis() *RedisClient {
	if c.engine == nil {
		return nil
	}
	return c.engine.redis
}

// Queue returns the integrated RabbitMQ / message queue client.
func (c *Context) Queue() *QueueClient {
	if c.engine == nil {
		return nil
	}
	return c.engine.queue
}

// Search returns the integrated Elasticsearch / search client.
func (c *Context) Search() *SearchClient {
	if c.engine == nil {
		return nil
	}
	return c.engine.search
}

// Bloom returns the integrated high-speed Bloom Filter.
func (c *Context) Bloom() *BloomFilter {
	if c.engine == nil {
		return nil
	}
	return c.engine.bloom
}

// RealIP attempts to extract the client IP address from standard headers.
func (c *Context) RealIP() string {
	if xff := c.Request.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}
	if xri := c.Request.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return strings.Split(c.Request.RemoteAddr, ":")[0]
}

// IP returns the client IP address (convenience alias for RealIP).
func (c *Context) IP() string {
	return c.RealIP()
}

// RequestID returns the request ID from context or standard header.
func (c *Context) RequestID() string {
	if val, ok := c.Get("RequestID"); ok {
		if s, ok := val.(string); ok && s != "" {
			return s
		}
	}
	if c.Request != nil {
		return c.Header("X-Request-ID")
	}
	return ""
}

