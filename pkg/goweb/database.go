package goweb

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// DBType represents the supported database types.
type DBType string

const (
	DBSQLite   DBType = "sqlite3"
	DBPostgres DBType = "postgres"
)

type dbContextKey struct{}

var primaryDBKey = dbContextKey{}

// WithPrimary returns a child context that forces all database queries (both reads and writes)
// to execute against the Primary/Master database, bypassing read replicas (slaves).
// This guarantees "read-your-own-writes" consistency immediately following an insert or update.
func WithPrimary(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, primaryDBKey, true)
}

// IsPrimaryRequired returns true if the context mandates reading from the Primary database.
func IsPrimaryRequired(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	val, _ := ctx.Value(primaryDBKey).(bool)
	return val
}

type replicaNode struct {
	db        *sql.DB
	dsn       string
	downUntil atomic.Int64 // Unix nanoseconds until which replica is considered down
	failures  atomic.Uint64
}

// Database wraps standard *sql.DB instances with Master/Slave Read-Write splitting,
// resilient connection pooling, automatic replica failover, transaction helpers, and PgBouncer tuning.
type Database struct {
	primary     *sql.DB
	replicas    []*replicaNode
	nextReplica atomic.Uint64
	driver      DBType
	dsn         string
	cfg         DBConfig
}

// DBConfig configures a database connection with optional Read Replicas (slaves).
type DBConfig struct {
	Driver          DBType
	DSN             string   // Primary / Master (Writer) connection string
	Replicas        []string // Read Replicas / Slaves connection strings
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PgBouncer       bool // Enable PgBouncer-specific transaction pool optimization
}

// DBClusterStats captures connection pool statistics across primary and read replicas.
type DBClusterStats struct {
	Primary        sql.DBStats
	Replicas       []sql.DBStats
	ActiveReplicas int
	TotalReplicas  int
}

// ErrNotOneRowAffected indicates that an update or delete did not affect exactly one row.
var ErrNotOneRowAffected = errors.New("database: expected exactly 1 row affected")

// NewDatabase initializes primary and read-replica database connections with production pooling.
func NewDatabase(cfg DBConfig) (*Database, error) {
	driverName := string(cmp.Or(cfg.Driver, DBSQLite))
	cfg.Driver = DBType(driverName)

	dsn := cfg.DSN
	if dsn == "" && cfg.Driver == DBSQLite {
		dsn = "file::memory:?cache=shared&mode=rwc"
	}

	primaryDB, err := openDBPool(driverName, dsn, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to open primary %s database: %w", cfg.Driver, err)
	}

	wrapper := &Database{
		primary: primaryDB,
		driver:  cfg.Driver,
		dsn:     dsn,
		cfg:     cfg,
	}

	// Open read replicas if configured
	for i, replicaDSN := range cfg.Replicas {
		if strings.TrimSpace(replicaDSN) == "" {
			continue
		}
		repDB, repErr := openDBPool(driverName, replicaDSN, cfg)
		if repErr != nil {
			slog.Warn("Read replica connection failed during init, marking offline",
				slog.Int("replica_index", i),
				slog.String("driver", driverName),
				slog.Any("error", repErr),
			)
			node := &replicaNode{db: repDB, dsn: replicaDSN}
			node.downUntil.Store(time.Now().Add(10 * time.Second).UnixNano())
			node.failures.Add(1)
			wrapper.replicas = append(wrapper.replicas, node)
			continue
		}
		wrapper.replicas = append(wrapper.replicas, &replicaNode{
			db:  repDB,
			dsn: replicaDSN,
		})
	}

	// Verify primary connectivity with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := primaryDB.PingContext(ctx); err != nil {
		slog.Warn("Primary database initial ping failed, registered for lazy reconnection",
			slog.String("driver", string(cfg.Driver)),
			slog.Bool("pgbouncer", cfg.PgBouncer),
			slog.Any("error", err),
		)
	}

	return wrapper, nil
}

func openDBPool(driverName, dsn string, cfg DBConfig) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}

	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		if cfg.Driver == DBSQLite {
			maxOpen = 10
			if strings.Contains(dsn, ":memory:") {
				maxOpen = 1
			}
		} else {
			maxOpen = 25
		}
	}
	db.SetMaxOpenConns(maxOpen)

	maxIdle := cmp.Or(cfg.MaxIdleConns, maxOpen/2)
	if maxIdle < 1 {
		maxIdle = 1
	}
	db.SetMaxIdleConns(maxIdle)

	lifetime := cmp.Or(cfg.ConnMaxLifetime, 5*time.Minute)
	idleTimeout := cfg.ConnMaxIdleTime
	if cfg.PgBouncer {
		lifetime = cmp.Or(cfg.ConnMaxLifetime, 1*time.Minute)
		idleTimeout = cmp.Or(cfg.ConnMaxIdleTime, 30*time.Second)
	}
	db.SetConnMaxLifetime(lifetime)
	if idleTimeout > 0 {
		db.SetConnMaxIdleTime(idleTimeout)
	}

	if cfg.Driver == DBSQLite {
		_, _ = db.Exec("PRAGMA journal_mode = WAL;")
		_, _ = db.Exec("PRAGMA busy_timeout = 5000;")
		_, _ = db.Exec("PRAGMA synchronous = NORMAL;")
		_, _ = db.Exec("PRAGMA cache_size = -64000;")
		_, _ = db.Exec("PRAGMA foreign_keys = ON;")
	}

	return db, nil
}

// Raw returns the primary underlying *sql.DB instance.
func (d *Database) Raw() *sql.DB {
	return d.primary
}

// Primary returns the primary writer database.
func (d *Database) Primary() *sql.DB {
	return d.primary
}

// WriteDB returns the primary writer database.
func (d *Database) WriteDB() *sql.DB {
	return d.primary
}

// ReplicaCount returns the total number of configured read replicas.
func (d *Database) ReplicaCount() int {
	return len(d.replicas)
}

// Replicas returns all active read replica instances.
func (d *Database) Replicas() []*sql.DB {
	out := make([]*sql.DB, 0, len(d.replicas))
	for _, r := range d.replicas {
		if r.db != nil {
			out = append(out, r.db)
		}
	}
	return out
}

// AddReplica dynamically connects a new read replica at runtime.
func (d *Database) AddReplica(dsn string) error {
	repDB, err := openDBPool(string(d.driver), dsn, d.cfg)
	if err != nil {
		return fmt.Errorf("add replica failed: %w", err)
	}
	d.replicas = append(d.replicas, &replicaNode{
		db:  repDB,
		dsn: dsn,
	})
	return nil
}

// ReadDB returns a read replica using round-robin load balancing over healthy replicas.
// If all replicas are down, in cooldown, or none are configured, it transparently falls back to Primary.
func (d *Database) ReadDB() *sql.DB {
	return d.ReadDBContext(context.Background())
}

// ReadDBContext returns a database instance for read operations.
// If the context has WithPrimary(ctx) set, it returns Primary.
// Otherwise, it selects a healthy read replica using round-robin, falling back to Primary if needed.
func (d *Database) ReadDBContext(ctx context.Context) *sql.DB {
	if IsPrimaryRequired(ctx) || len(d.replicas) == 0 {
		return d.primary
	}

	n := len(d.replicas)
	now := time.Now().UnixNano()
	startIdx := int(d.nextReplica.Add(1) % uint64(n))

	// Search for the first healthy replica (not in cooldown)
	for i := range n {
		idx := (startIdx + i) % n
		node := d.replicas[idx]
		if node.db != nil && now >= node.downUntil.Load() {
			return node.db
		}
	}

	// All replicas currently in cooldown; fallback to primary to keep service alive
	return d.primary
}

func (d *Database) markReplicaFailed(db *sql.DB, err error) {
	if db == d.primary || db == nil {
		return
	}
	for _, node := range d.replicas {
		if node.db == db {
			node.failures.Add(1)
			// Put replica in 5-second cooldown
			node.downUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
			slog.Warn("Read replica marked unhealthy, falling back to primary",
				slog.String("dsn", node.dsn),
				slog.Uint64("failures", node.failures.Load()),
				slog.Any("error", err),
			)
			break
		}
	}
}

// Replica returns a read replica using round-robin load balancing.
func (d *Database) Replica() *sql.DB {
	return d.ReadDB()
}

// Driver returns the database driver type.
func (d *Database) Driver() DBType {
	return d.driver
}

// Exec executes a mutating query strictly against the Primary/Master database.
func (d *Database) Exec(query string, args ...any) (sql.Result, error) {
	return d.primary.Exec(query, args...)
}

// ExecContext executes a mutating query strictly against the Primary/Master database.
func (d *Database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.primary.ExecContext(ctx, query, args...)
}

// ExecOne executes a mutating query against the Primary database and enforces that
// exactly 1 row was affected. If 0 or >1 rows are affected, it returns ErrNotOneRowAffected.
func (d *Database) ExecOne(ctx context.Context, query string, args ...any) error {
	res, err := d.primary.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: expected 1, affected %d", ErrNotOneRowAffected, n)
	}
	return nil
}

// Query executes a read query load-balanced across Read Replicas (slaves).
// Automatically falls back to primary if no replicas are configured or if the replica fails.
func (d *Database) Query(query string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), query, args...)
}

// QueryContext executes a read query load-balanced across Read Replicas.
// Respects WithPrimary(ctx) for read-your-own-writes consistency.
// Automatically falls back to Primary if a read replica returns a network/connection error.
func (d *Database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	target := d.ReadDBContext(ctx)
	rows, err := target.QueryContext(ctx, query, args...)
	if err != nil && target != d.primary && isConnectionError(err) {
		d.markReplicaFailed(target, err)
		return d.primary.QueryContext(ctx, query, args...)
	}
	return rows, err
}

// QueryRow executes a single-row read query load-balanced across Read Replicas.
func (d *Database) QueryRow(query string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), query, args...)
}

// QueryRowContext executes a single-row read query load-balanced across Read Replicas.
// Respects WithPrimary(ctx) for read-your-own-writes consistency.
func (d *Database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	target := d.ReadDBContext(ctx)
	return target.QueryRowContext(ctx, query, args...)
}

// QueryPrimary executes a read query explicitly on the Primary/Master database.
func (d *Database) QueryPrimary(query string, args ...any) (*sql.Rows, error) {
	return d.primary.Query(query, args...)
}

// QueryPrimaryContext executes a read query explicitly on the Primary/Master database.
func (d *Database) QueryPrimaryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.primary.QueryContext(ctx, query, args...)
}

// QueryRowPrimary executes a single-row read query explicitly on the Primary/Master database.
func (d *Database) QueryRowPrimary(query string, args ...any) *sql.Row {
	return d.primary.QueryRow(query, args...)
}

// QueryRowPrimaryContext executes a single-row read query explicitly on the Primary/Master database.
func (d *Database) QueryRowPrimaryContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.primary.QueryRowContext(ctx, query, args...)
}

// Prepare creates a prepared statement strictly on the Primary/Master database.
func (d *Database) Prepare(query string) (*sql.Stmt, error) {
	return d.primary.Prepare(query)
}

// PrepareContext creates a prepared statement strictly on the Primary/Master database.
func (d *Database) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return d.primary.PrepareContext(ctx, query)
}

// Begin starts a transaction strictly on the Primary/Master database.
func (d *Database) Begin() (*sql.Tx, error) {
	return d.primary.Begin()
}

// BeginTx starts a transaction strictly on the Primary/Master database with transaction options.
func (d *Database) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.primary.BeginTx(ctx, opts)
}

// WithTx executes the given function inside a database transaction strictly
// on the Primary/Master database. Automatically rolls back on error or panic, and commits on success.
func (d *Database) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.primary.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// WithTxRetry executes a transaction strictly on the Primary/Master database with
// safe exponential backoff retries for transient errors (e.g. Postgres 40001 serialization failures, locks).
func (d *Database) WithTxRetry(ctx context.Context, maxRetries int, fn func(tx *sql.Tx) error) error {
	maxRetries = cmp.Or(maxRetries, 3)
	var err error

	for attempt := range maxRetries {
		err = d.WithTx(ctx, fn)
		if err == nil {
			return nil
		}

		if !isRetryableDBError(err) {
			return err
		}

		// Exponential backoff with jitter
		backoff := time.Duration(10*(1<<attempt))*time.Millisecond + time.Duration(attempt*5)*time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}

	return fmt.Errorf("transaction failed after %d retries: %w", maxRetries, err)
}

func isRetryableDBError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadlock detected") ||
		strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "40001") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "busy") ||
		isConnectionError(err)
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "bad connection") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "server closed the connection")
}

// Stats returns connection pool stats across primary and all read replicas.
func (d *Database) Stats() DBClusterStats {
	var repStats []sql.DBStats
	active := 0
	now := time.Now().UnixNano()

	for _, r := range d.replicas {
		if r.db != nil {
			repStats = append(repStats, r.db.Stats())
			if now >= r.downUntil.Load() {
				active++
			}
		}
	}

	return DBClusterStats{
		Primary:        d.primary.Stats(),
		Replicas:       repStats,
		ActiveReplicas: active,
		TotalReplicas:  len(d.replicas),
	}
}

// Ping verifies connections to primary and replica databases are alive.
func (d *Database) Ping(ctx context.Context) error {
	if d.primary == nil {
		return fmt.Errorf("database connection not initialized")
	}
	if err := d.primary.PingContext(ctx); err != nil {
		return fmt.Errorf("primary database ping failed: %w", err)
	}
	for i, rep := range d.replicas {
		if rep.db != nil {
			if err := rep.db.PingContext(ctx); err != nil {
				return fmt.Errorf("replica %d ping failed: %w", i, err)
			}
		}
	}
	return nil
}

// Close closes all primary and replica database connections.
func (d *Database) Close() error {
	var errs []error
	if d.primary != nil {
		if err := d.primary.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, rep := range d.replicas {
		if rep.db != nil {
			if err := rep.db.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
