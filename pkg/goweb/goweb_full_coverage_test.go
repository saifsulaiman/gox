package goweb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type dummyPgDriver struct{}

func (d *dummyPgDriver) Open(name string) (driver.Conn, error) {
	return &dummyPgConn{}, nil
}

type dummyPgConn struct{}

func (c *dummyPgConn) Prepare(query string) (driver.Stmt, error) { return &dummyPgStmt{}, nil }
func (c *dummyPgConn) Close() error                              { return nil }
func (c *dummyPgConn) Begin() (driver.Tx, error)                 { return &dummyPgTx{}, nil }
func (c *dummyPgConn) Ping(ctx context.Context) error            { return nil }

type dummyPgStmt struct{}

func (s *dummyPgStmt) Close() error                                    { return nil }
func (s *dummyPgStmt) NumInput() int                                   { return -1 }
func (s *dummyPgStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, nil }
func (s *dummyPgStmt) Query(args []driver.Value) (driver.Rows, error)  { return nil, nil }

type dummyPgTx struct{}

func (t *dummyPgTx) Commit() error   { return nil }
func (t *dummyPgTx) Rollback() error { return nil }

func init() {
	for _, d := range sql.Drivers() {
		if d == "postgres" {
			return
		}
	}
	sql.Register("postgres", &dummyPgDriver{})
}


// TestEngineRoutingAndMethods tests PUT, PATCH, HEAD, OPTIONS, and Any across Engine and RouteGroup.
func TestEngineRoutingAndMethods(t *testing.T) {
	app := New(Config{})

	// Test Engine methods
	app.PUT("/put", func(c *Context) error { return c.String(http.StatusOK, "put") })
	app.PATCH("/patch", func(c *Context) error { return c.String(http.StatusOK, "patch") })
	app.HEAD("/head", func(c *Context) error { return c.NoContent(http.StatusOK) })
	app.OPTIONS("/options", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	app.Any("/any", func(c *Context) error { return c.String(http.StatusOK, "any:%s", c.Method()) })

	// Test RouteGroup methods
	g := app.Group("/api/v1")
	g.PUT("/put", func(c *Context) error { return c.String(http.StatusOK, "group-put") })
	g.PATCH("/patch", func(c *Context) error { return c.String(http.StatusOK, "group-patch") })
	g.DELETE("/del", func(c *Context) error { return c.String(http.StatusOK, "group-del") })
	g.HEAD("/head", func(c *Context) error { return c.NoContent(http.StatusOK) })
	g.OPTIONS("/options", func(c *Context) error { return c.NoContent(http.StatusNoContent) })
	g.Any("/any", func(c *Context) error { return c.String(http.StatusOK, "group-any") })

	methods := []struct {
		method string
		path   string
		expect int
	}{
		{http.MethodPut, "/put", http.StatusOK},
		{http.MethodPatch, "/patch", http.StatusOK},
		{http.MethodHead, "/head", http.StatusOK},
		{http.MethodOptions, "/options", http.StatusNoContent},
		{http.MethodGet, "/any", http.StatusOK},
		{http.MethodPost, "/any", http.StatusOK},
		{http.MethodPut, "/api/v1/put", http.StatusOK},
		{http.MethodPatch, "/api/v1/patch", http.StatusOK},
		{http.MethodDelete, "/api/v1/del", http.StatusOK},
		{http.MethodHead, "/api/v1/head", http.StatusOK},
		{http.MethodOptions, "/api/v1/options", http.StatusNoContent},
		{http.MethodGet, "/api/v1/any", http.StatusOK},
	}

	for _, tc := range methods {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, tc.path, nil)
		app.ServeHTTP(w, r)
		if w.Code != tc.expect {
			t.Errorf("%s %s: expected %d, got %d", tc.method, tc.path, tc.expect, w.Code)
		}
	}
}

// TestContextAbortVariations tests all Abort methods and IsAborted.
func TestContextAbortVariations(t *testing.T) {
	app := New(Config{})

	app.GET("/abort-status", func(c *Context) error {
		c.AbortWithStatus(http.StatusPaymentRequired)
		if !c.IsAborted() {
			t.Errorf("expected IsAborted to be true")
		}
		return nil
	})

	app.GET("/abort-error", func(c *Context) error {
		return c.AbortWithError(http.StatusConflict, errors.New("conflict error"))
	})

	app.GET("/abort-json", func(c *Context) error {
		return c.AbortWithJSON(http.StatusUnprocessableEntity, H{"err": "validation"})
	})

	app.GET("/abort-blob", func(c *Context) error {
		return c.AbortWithBlob(http.StatusTeapot, "application/custom", []byte("blob-abort"))
	})

	tests := []struct {
		path   string
		expect int
	}{
		{"/abort-status", http.StatusPaymentRequired},
		{"/abort-error", http.StatusConflict},
		{"/abort-json", http.StatusUnprocessableEntity},
		{"/abort-blob", http.StatusTeapot},
	}

	for _, tc := range tests {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		app.ServeHTTP(w, r)
		if w.Code != tc.expect {
			t.Errorf("%s: expected %d, got %d", tc.path, tc.expect, w.Code)
		}
	}
}

// TestContextBytesBlobAndResponses tests Context Bytes, Blob, Error, and Logger.
func TestContextBytesBlobAndResponses(t *testing.T) {
	app := New(Config{})

	app.GET("/bytes", func(c *Context) error {
		return c.Bytes(http.StatusOK, "text/plain", []byte("raw-bytes"))
	})

	app.GET("/blob", func(c *Context) error {
		return c.Blob(http.StatusOK, "application/octet-stream", []byte{0x01, 0x02, 0x03})
	})

	app.GET("/error", func(c *Context) error {
		return c.Error(http.StatusBadRequest, "custom error message")
	})

	app.GET("/logger-test", func(c *Context) error {
		l := c.Logger()
		if l == nil {
			t.Errorf("expected non-nil logger")
		}
		customLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
		c.SetLogger(customLogger)
		if c.Logger() != customLogger {
			t.Errorf("custom logger not set")
		}
		return c.NoContent(http.StatusOK)
	})

	wBytes := httptest.NewRecorder()
	app.ServeHTTP(wBytes, httptest.NewRequest(http.MethodGet, "/bytes", nil))
	if wBytes.Body.String() != "raw-bytes" {
		t.Errorf("expected raw-bytes, got %s", wBytes.Body.String())
	}

	wBlob := httptest.NewRecorder()
	app.ServeHTTP(wBlob, httptest.NewRequest(http.MethodGet, "/blob", nil))
	if len(wBlob.Body.Bytes()) != 3 {
		t.Errorf("expected 3 bytes blob")
	}

	wErr := httptest.NewRecorder()
	app.ServeHTTP(wErr, httptest.NewRequest(http.MethodGet, "/error", nil))
	if wErr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400 for error helper")
	}

	wLog := httptest.NewRecorder()
	app.ServeHTTP(wLog, httptest.NewRequest(http.MethodGet, "/logger-test", nil))
	if wLog.Code != http.StatusOK {
		t.Errorf("expected 200 for logger test")
	}
}

// TestEngineServicesAndDBAccess tests accessing services on Engine and Context.
func TestEngineServicesAndDBAccess(t *testing.T) {
	app := New(Config{
		SQLite:   "file::memory:?cache=shared&mode=rwc",
		Postgres: "file::memory:?cache=shared&mode=rwc",
	})

	// Engine services
	if app.SQLite() == nil {
		t.Errorf("expected non-nil SQLite")
	}
	if app.Postgres() == nil {
		t.Errorf("expected non-nil Postgres")
	}
	if app.DB() == nil {
		t.Errorf("expected non-nil DB")
	}
	if app.Redis() == nil {
		t.Errorf("expected non-nil Redis")
	}
	if app.Queue() == nil {
		t.Errorf("expected non-nil Queue")
	}
	if app.Search() == nil {
		t.Errorf("expected non-nil Search")
	}
	if app.Bloom() == nil {
		t.Errorf("expected non-nil Bloom")
	}
	if app.Singleflight() == nil {
		t.Errorf("expected non-nil Singleflight")
	}

	app.GET("/services", func(c *Context) error {
		if c.SQLite() == nil || c.Postgres() == nil || c.DB() == nil {
			t.Errorf("context DB accessors returned nil")
		}
		if c.ReadDB() == nil || c.WriteDB() == nil {
			t.Errorf("context ReadDB/WriteDB returned nil")
		}
		if c.Arena() == nil {
			// May be nil if disabled or dummy
		}
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/services", nil))
	if w.Code != http.StatusOK {
		t.Errorf("services endpoint failed")
	}
}

// TestDetachedAsyncContextMethods tests Deadline, Done, Err, and Value on detachedContext.
func TestDetachedAsyncContextMethods(t *testing.T) {
	parentCtx := context.WithValue(context.Background(), "trace_id", "trace-abc-123")
	c := &Context{
		Request: httptest.NewRequest(http.MethodGet, "/", nil).WithContext(parentCtx),
	}

	executed := make(chan bool, 1)
	c.Async(func(asyncCtx context.Context) {
		// Test Deadline
		_, ok := asyncCtx.Deadline()
		if ok {
			t.Errorf("expected no deadline on detached context")
		}

		// Test Err and Done
		if asyncCtx.Err() != nil {
			t.Errorf("expected nil Err initially")
		}
		select {
		case <-asyncCtx.Done():
			t.Errorf("expected Done channel not closed")
		default:
		}

		// Test Value inheritance
		val := asyncCtx.Value("trace_id")
		if val != "trace-abc-123" {
			t.Errorf("expected trace-abc-123, got %v", val)
		}

		executed <- true
	})

	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatalf("async worker did not execute")
	}
}

// TestBloomFilterCountAndReset tests Bloom Filter Count and Reset.
func TestBloomFilterCountAndReset(t *testing.T) {
	bf := NewBloomFilter(1000, 0.01)

	if bf.Count() != 0 {
		t.Errorf("expected count 0, got %d", bf.Count())
	}

	bf.Add("item-1")
	bf.Add("item-2")
	if bf.Count() != 2 {
		t.Errorf("expected count 2, got %d", bf.Count())
	}

	bf.Reset()
	if bf.Count() != 0 {
		t.Errorf("expected count 0 after reset, got %d", bf.Count())
	}
	if bf.Contains("item-1") {
		t.Errorf("item-1 should not be present after reset")
	}
}

// TestStaticFileServing tests app.Static file serving.
func TestStaticFileServing(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "hello.txt")
	_ = os.WriteFile(testFile, []byte("static file contents"), 0644)

	app := New(Config{})
	app.Static("/static", tmpDir)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/hello.txt", nil)
	app.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for static file, got %d", w.Code)
	}
	if w.Body.String() != "static file contents" {
		t.Fatalf("expected 'static file contents', got '%s'", w.Body.String())
	}
}

// TestDatabasePrimaryAndReplicaQueries tests QueryPrimary, QueryPrimaryContext, QueryRowPrimary, and AddReplica.
func TestDatabasePrimaryAndReplicaQueries(t *testing.T) {
	db, err := NewDatabase(DBConfig{
		Driver: DBSQLite,
		DSN:    "file::memory:?cache=shared&mode=rwc",
	})
	if err != nil {
		t.Fatalf("NewDatabase failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	_, _ = db.Exec("CREATE TABLE IF NOT EXISTS test_prim (id INTEGER PRIMARY KEY, msg TEXT)")
	_, _ = db.Exec("INSERT INTO test_prim (msg) VALUES ('hello-primary')")

	// QueryPrimary
	rows, err := db.QueryPrimary("SELECT msg FROM test_prim")
	if err != nil {
		t.Fatalf("QueryPrimary failed: %v", err)
	}
	rows.Close()

	// QueryPrimaryContext
	rowsCtx, err := db.QueryPrimaryContext(ctx, "SELECT msg FROM test_prim")
	if err != nil {
		t.Fatalf("QueryPrimaryContext failed: %v", err)
	}
	rowsCtx.Close()

	// QueryRowPrimary
	var msg string
	if err := db.QueryRowPrimary("SELECT msg FROM test_prim WHERE id = 1").Scan(&msg); err != nil {
		t.Fatalf("QueryRowPrimary failed: %v", err)
	}
	if msg != "hello-primary" {
		t.Errorf("expected hello-primary, got %s", msg)
	}

	// QueryRowPrimaryContext
	var msgCtx string
	if err := db.QueryRowPrimaryContext(ctx, "SELECT msg FROM test_prim WHERE id = 1").Scan(&msgCtx); err != nil {
		t.Fatalf("QueryRowPrimaryContext failed: %v", err)
	}
	if msgCtx != "hello-primary" {
		t.Errorf("expected hello-primary, got %s", msgCtx)
	}

	// AddReplica
	err = db.AddReplica("file::memory:?cache=shared&mode=rwc")
	if err != nil {
		t.Fatalf("AddReplica failed: %v", err)
	}
	if db.ReplicaCount() < 1 {
		t.Errorf("expected at least 1 replica")
	}

	// PrepareContext
	stmt, err := db.PrepareContext(ctx, "SELECT msg FROM test_prim WHERE id = ?")
	if err != nil {
		t.Fatalf("PrepareContext failed: %v", err)
	}
	defer stmt.Close()
}

// TestRedisComprehensiveOperations tests Incr, Del, JSON, Leaderboard count/remove, and FlushAll.
func TestRedisComprehensiveOperations(t *testing.T) {
	app := New(Config{})
	r := app.Redis()
	ctx := context.Background()

	// Ping
	if err := r.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// Incr
	count, err := r.Incr(ctx, "page_views")
	if err != nil || count != 1 {
		t.Fatalf("Incr failed, expected 1, got %d (err: %v)", count, err)
	}
	count, _ = r.Incr(ctx, "page_views")
	if count != 2 {
		t.Fatalf("Incr failed on second increment")
	}

	// SetJSON and GetJSON
	type Profile struct {
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	}
	if err := r.SetJSON(ctx, "prof:1", Profile{Name: "Bob", Admin: true}, time.Minute); err != nil {
		t.Fatalf("SetJSON failed: %v", err)
	}
	var p Profile
	if err := r.GetJSON(ctx, "prof:1", &p); err != nil || p.Name != "Bob" || !p.Admin {
		t.Fatalf("GetJSON failed: %+v (err: %v)", p, err)
	}

	// Del
	if err := r.Del(ctx, "prof:1", "page_views"); err != nil {
		t.Fatalf("Del failed: %v", err)
	}

	// LeaderboardCount and LeaderboardRemove
	_ = r.LeaderboardAdd(ctx, "lb_ops", "u1", 50)
	_ = r.LeaderboardAdd(ctx, "lb_ops", "u2", 100)
	c, err := r.LeaderboardCount(ctx, "lb_ops")
	if err != nil || c != 2 {
		t.Fatalf("expected count 2, got %d (err: %v)", c, err)
	}
	if err := r.LeaderboardRemove(ctx, "lb_ops", "u1"); err != nil {
		t.Fatalf("LeaderboardRemove failed: %v", err)
	}
	c, _ = r.LeaderboardCount(ctx, "lb_ops")
	if c != 1 {
		t.Fatalf("expected count 1 after remove, got %d", c)
	}

	// Close
	_ = r.Close()
}

// TestRabbitMQComprehensiveQueue tests ConsumeJSON and Close.
func TestRabbitMQComprehensiveQueue(t *testing.T) {
	app := New(Config{})
	q := app.Queue()

	received := make(chan string, 1)
	err := q.ConsumeJSON("tasks", func(msg Message, payload map[string]any) error {
		received <- payload["task"].(string)
		return nil
	})
	if err != nil {
		t.Fatalf("ConsumeJSON failed: %v", err)
	}

	_ = q.PublishJSON(context.Background(), "", "tasks", map[string]any{"task": "encode_video"})

	select {
	case val := <-received:
		if val != "encode_video" {
			t.Errorf("expected encode_video, got %s", val)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for ConsumeJSON")
	}

	_ = q.Close()
}

// TestSearchComprehensive tests Search Close and empty index operations.
func TestSearchComprehensive(t *testing.T) {
	app := New(Config{})
	s := app.Search()
	ctx := context.Background()

	// Search on empty index
	res, err := s.Search(ctx, "empty_idx", "nonexistent")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.Total != 0 {
		t.Errorf("expected 0 hits")
	}

	_ = s.Close()
}

// TestResilienceAndLimits tests BodyLimit, RateLimiter, Logger, and RequestArenaMiddleware.
func TestResilienceAndLimits(t *testing.T) {
	app := New(Config{})

	// 1. BodyLimit
	app.POST("/body-limit", BodyLimit(10), func(c *Context) error {
		var body map[string]string
		if err := c.BindJSON(&body); err != nil {
			return c.AbortWithError(http.StatusRequestEntityTooLarge, err)
		}
		return c.String(http.StatusOK, "ok")
	})

	// Valid payload
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/body-limit", strings.NewReader(`{"a":"b"}`))
	app.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", w1.Code)
	}

	// Oversized payload
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/body-limit", strings.NewReader(`{"large":"this payload definitely exceeds ten bytes"}`))
	app.ServeHTTP(w2, req2)
	if w2.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", w2.Code)
	}

	// 2. RateLimiter
	rateApp := New(Config{})
	rateApp.Use(RateLimiter(1.0, 2))
	rateApp.GET("/limited", func(c *Context) error { return c.String(http.StatusOK, "passed") })

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/limited", nil)
		r.RemoteAddr = "192.168.1.100:1234"
		rateApp.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200 OK, got %d", i, w.Code)
		}
	}
	// 3rd immediate request must be rejected with 429
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "/limited", nil)
	r3.RemoteAddr = "192.168.1.100:1234"
	rateApp.ServeHTTP(w3, r3)
	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 Too Many Requests, got %d", w3.Code)
	}

	// 3. Logger & RequestArenaMiddleware
	logApp := New(Config{})
	logApp.Use(Logger())
	logApp.Use(RequestArenaMiddleware())
	logApp.GET("/log-200", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	logApp.GET("/log-400", func(c *Context) error { return c.String(http.StatusBadRequest, "bad") })
	logApp.GET("/log-500", func(c *Context) error { return c.String(http.StatusInternalServerError, "err") })

	for _, path := range []string{"/log-200?foo=bar", "/log-400", "/log-500"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Request-ID", "req-12345")
		logApp.ServeHTTP(w, r)
	}
}

// TestRouterDirectServe tests router.ServeHTTP and Params.All iterator.
func TestRouterDirectServe(t *testing.T) {
	r := NewRouter()
	r.AddRoute(http.MethodGet, "/items/:id", []HandlerFunc{
		func(c *Context) error {
			allParams := make(map[string]string)
			for k, v := range c.Params.All() {
				allParams[k] = v
			}
			if allParams["id"] != "42" {
				t.Errorf("expected param id=42, got %s", allParams["id"])
			}
			return c.String(http.StatusOK, "item:%s", c.Params.Get("id"))
		},
	})

	// Match route
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", w.Code)
	}

	// 404 with custom notFound handler
	r.notFound = []HandlerFunc{func(c *Context) error {
		return c.String(http.StatusNotFound, "custom 404")
	}}
	w404 := httptest.NewRecorder()
	req404 := httptest.NewRequest(http.MethodGet, "/nowhere", nil)
	r.ServeHTTP(w404, req404)
	if w404.Code != http.StatusNotFound || !strings.Contains(w404.Body.String(), "custom 404") {
		t.Errorf("expected custom 404, got code=%d body=%s", w404.Code, w404.Body.String())
	}

	// Default 404 when notFound is nil
	r2 := NewRouter()
	wDef := httptest.NewRecorder()
	r2.ServeHTTP(wDef, req404)
	if wDef.Code != http.StatusNotFound {
		t.Errorf("expected default 404, got %d", wDef.Code)
	}
}

// TestDatabaseFullLifecycle tests all Database query, exec, tx, and replica helper methods.
func TestDatabaseFullLifecycle(t *testing.T) {
	db, err := NewDatabase(DBConfig{
		Driver:   DBSQLite,
		DSN:      "file::memory:?cache=shared&mode=rwc",
		Replicas: []string{"file::memory:?cache=shared&mode=rwc"},
	})
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Accessors
	if db.Raw() == nil || db.Primary() == nil || db.WriteDB() == nil {
		t.Errorf("expected non-nil raw/primary/write DB")
	}
	if db.ReplicaCount() != 1 {
		t.Errorf("expected 1 replica, got %d", db.ReplicaCount())
	}
	if len(db.Replicas()) != 1 {
		t.Errorf("expected 1 replica DB")
	}
	if db.ReadDB() == nil || db.Replica() == nil {
		t.Errorf("expected non-nil read replica")
	}

	// Add dynamic replica
	if err := db.AddReplica("file::memory:?cache=shared&mode=rwc"); err != nil {
		t.Errorf("AddReplica failed: %v", err)
	}

	// DDL & Exec
	_, err = db.Exec("CREATE TABLE full_lifecycle_users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE failed: %v", err)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO full_lifecycle_users (id, name) VALUES (1, 'Alice')")
	if err != nil {
		t.Fatalf("ExecContext failed: %v", err)
	}

	err = db.ExecOne(ctx, "INSERT INTO full_lifecycle_users (id, name) VALUES (2, 'Bob')")
	if err != nil {
		t.Fatalf("ExecOne failed: %v", err)
	}

	// Query & QueryContext
	rows, err := db.Query("SELECT name FROM full_lifecycle_users ORDER BY id ASC")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	_ = rows.Close()

	rowsCtx, err := db.QueryContext(ctx, "SELECT name FROM full_lifecycle_users ORDER BY id ASC")
	if err != nil {
		t.Fatalf("QueryContext failed: %v", err)
	}
	_ = rowsCtx.Close()

	// QueryRow & QueryRowContext
	var name string
	err = db.QueryRow("SELECT name FROM full_lifecycle_users WHERE id = 1").Scan(&name)
	if err != nil || name != "Alice" {
		t.Errorf("QueryRow failed: %v, name=%s", err, name)
	}

	var name2 string
	err = db.QueryRowContext(ctx, "SELECT name FROM full_lifecycle_users WHERE id = 2").Scan(&name2)
	if err != nil || name2 != "Bob" {
		t.Errorf("QueryRowContext failed: %v, name=%s", err, name2)
	}

	// QueryPrimary & QueryPrimaryContext
	pRows, err := db.QueryPrimary("SELECT name FROM full_lifecycle_users WHERE id = 1")
	if err != nil {
		t.Fatalf("QueryPrimary failed: %v", err)
	}
	_ = pRows.Close()

	pRowsCtx, err := db.QueryPrimaryContext(ctx, "SELECT name FROM full_lifecycle_users WHERE id = 1")
	if err != nil {
		t.Fatalf("QueryPrimaryContext failed: %v", err)
	}
	_ = pRowsCtx.Close()

	var nameP1 string
	err = db.QueryRowPrimary("SELECT name FROM full_lifecycle_users WHERE id = 1").Scan(&nameP1)
	if err != nil || nameP1 != "Alice" {
		t.Errorf("QueryRowPrimary failed: %v", err)
	}

	var nameP2 string
	err = db.QueryRowPrimaryContext(ctx, "SELECT name FROM full_lifecycle_users WHERE id = 2").Scan(&nameP2)
	if err != nil || nameP2 != "Bob" {
		t.Errorf("QueryRowPrimaryContext failed: %v", err)
	}

	// Prepare & PrepareContext
	stmt, err := db.Prepare("SELECT name FROM full_lifecycle_users WHERE id = ?")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	_ = stmt.Close()

	stmtCtx, err := db.PrepareContext(ctx, "SELECT name FROM full_lifecycle_users WHERE id = ?")
	if err != nil {
		t.Fatalf("PrepareContext failed: %v", err)
	}
	_ = stmtCtx.Close()

	// Begin & BeginTx
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	_ = tx.Rollback()

	txCtx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	_ = txCtx.Rollback()

	// Replica failure tracking & fallback
	db.markReplicaFailed(nil, nil)
	db.markReplicaFailed(db.primary, nil)
	if len(db.replicas) > 0 {
		db.markReplicaFailed(db.replicas[0].db, errors.New("simulated read replica failure"))
	}
}

// TestContextExtraMethods tests newContext, service getters, RealIP, and RequestID.
func TestContextExtraMethods(t *testing.T) {
	app := New(Config{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	req.Header.Set("X-Request-ID", "custom-header-id")

	c := newContext(w, req, app, nil)

	// Context services
	if c.Queue() == nil {
		t.Errorf("expected non-nil Queue")
	}
	if c.Search() == nil {
		t.Errorf("expected non-nil Search")
	}
	if c.Bloom() == nil {
		t.Errorf("expected non-nil Bloom")
	}

	// RealIP / IP
	if c.RealIP() != "203.0.113.195" {
		t.Errorf("expected RealIP from X-Forwarded-For, got %s", c.RealIP())
	}
	if c.IP() != "203.0.113.195" {
		t.Errorf("expected IP alias, got %s", c.IP())
	}

	// X-Real-IP fallback
	req.Header.Del("X-Forwarded-For")
	req.Header.Set("X-Real-IP", "198.51.100.1")
	if c.RealIP() != "198.51.100.1" {
		t.Errorf("expected RealIP from X-Real-IP, got %s", c.RealIP())
	}

	// RemoteAddr fallback
	req.Header.Del("X-Real-IP")
	if c.RealIP() != "10.0.0.1" {
		t.Errorf("expected RealIP from RemoteAddr, got %s", c.RealIP())
	}

	// RequestID from header
	if c.RequestID() != "custom-header-id" {
		t.Errorf("expected RequestID from header, got %s", c.RequestID())
	}

	// RequestID from context Set
	c.Set("RequestID", "context-stored-id")
	if c.RequestID() != "context-stored-id" {
		t.Errorf("expected RequestID from context, got %s", c.RequestID())
	}
}

// TestRedisPubSubAndHLL tests Redis Publish, Subscribe, HyperLogLog, and Lock helpers.
func TestRedisPubSubAndHLL(t *testing.T) {
	app := New(Config{})
	r := app.Redis()
	ctx := context.Background()

	// 1. Pub/Sub
	received := make(chan string, 1)
	r.Subscribe("notifications", func(msg string) {
		received <- msg
	})

	err := r.Publish(ctx, "notifications", "hello-world")
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case msg := <-received:
		if msg != "hello-world" {
			t.Errorf("expected hello-world, got %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for Redis pub/sub message")
	}

	// 2. HyperLogLog
	added, err := r.HyperLogLogAdd(ctx, "unique_visitors", "user1", "user2", "user3", "user1")
	if err != nil || !added {
		t.Errorf("HyperLogLogAdd failed: %v, added=%v", err, added)
	}

	cardinality, err := r.HyperLogLogCount(ctx, "unique_visitors")
	if err != nil || cardinality != 3 {
		t.Errorf("expected cardinality 3, got %d (err=%v)", cardinality, err)
	}

	// 3. Lock methods
	lock, err := r.Lock(ctx, "order:123", 5*time.Second)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	if lock.Key() != "order:123" {
		t.Errorf("expected key order:123, got %s", lock.Key())
	}
	if lock.Token() == "" {
		t.Errorf("expected non-empty token")
	}
	if err := lock.Renew(ctx, 10*time.Second); err != nil {
		t.Errorf("Renew failed: %v", err)
	}
	if err := lock.Unlock(ctx); err != nil {
		t.Errorf("Unlock failed: %v", err)
	}
}

// TestAuthPermissionsAndUser tests User, HasPermission, and RequirePermissions middleware.
func TestAuthPermissionsAndUser(t *testing.T) {
	u := &User{
		ID:          "usr-99",
		Roles:       []string{"editor"},
		Permissions: []string{"posts:read", "posts:write"},
	}

	if !u.HasRole("editor") || u.HasRole("admin") {
		t.Errorf("HasRole assertion failed")
	}
	if !u.HasPermission("posts:read") || u.HasPermission("posts:delete") {
		t.Errorf("HasPermission assertion failed")
	}

	// Nil receiver safety
	var nilUser *User
	if nilUser.HasRole("admin") || nilUser.HasPermission("posts:read") {
		t.Errorf("nil User should return false for all checks")
	}

	// RequirePermissions middleware
	app := New(Config{})
	app.GET("/protected-perm", func(c *Context) error {
		// Attach user
		c.Set(userContextKey, u)
		return c.Next()
	}, RequirePermissions("posts:read", "posts:write"), func(c *Context) error {
		return c.String(http.StatusOK, "authorized")
	})

	wAuth := httptest.NewRecorder()
	app.ServeHTTP(wAuth, httptest.NewRequest(http.MethodGet, "/protected-perm", nil))
	if wAuth.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", wAuth.Code)
	}

	// Missing permission
	app.GET("/forbidden-perm", func(c *Context) error {
		c.Set(userContextKey, u)
		return c.Next()
	}, RequirePermissions("posts:delete"), func(c *Context) error {
		return c.String(http.StatusOK, "unreachable")
	})

	wForb := httptest.NewRecorder()
	app.ServeHTTP(wForb, httptest.NewRequest(http.MethodGet, "/forbidden-perm", nil))
	if wForb.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", wForb.Code)
	}

	// Unauthenticated
	app.GET("/no-auth", RequirePermissions("posts:read"), func(c *Context) error {
		return c.String(http.StatusOK, "unreachable")
	})
	wNoAuth := httptest.NewRecorder()
	app.ServeHTTP(wNoAuth, httptest.NewRequest(http.MethodGet, "/no-auth", nil))
	if wNoAuth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", wNoAuth.Code)
	}
}

// TestRouteGroupStatic tests Static serving mounted under RouteGroup.
func TestRouteGroupStatic(t *testing.T) {
	app := New(Config{})
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "docs.txt")
	_ = os.WriteFile(testFile, []byte("documentation text"), 0644)

	grp := app.Group("/v1")
	grp.Static("/static", tmpDir)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/static/docs.txt", nil)
	app.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	if w.Body.String() != "documentation text" {
		t.Errorf("expected 'documentation text', got '%s'", w.Body.String())
	}
}

// TestNestedRouteGroup tests Group.Group chaining.
func TestNestedRouteGroup(t *testing.T) {
	app := New(Config{})
	v1 := app.Group("/api").Group("/v1")
	v1.GET("/users", func(c *Context) error {
		return c.String(http.StatusOK, "v1 users")
	})

	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if w.Code != http.StatusOK || w.Body.String() != "v1 users" {
		t.Errorf("nested route group failed: code=%d body=%s", w.Code, w.Body.String())
	}
}

// TestIsConnectionError checks all database connection error recognition patterns.
func TestIsConnectionError(t *testing.T) {
	if isConnectionError(nil) {
		t.Errorf("expected false for nil error")
	}

	knownErrors := []string{
		"read: connection reset by peer",
		"write: broken pipe",
		"driver: bad connection",
		"dial tcp 127.0.0.1: connection refused",
		"net/http: i/o timeout",
		"server closed the connection unexpectedly",
	}

	for _, msg := range knownErrors {
		if !isConnectionError(errors.New(msg)) {
			t.Errorf("expected isConnectionError to be true for: %s", msg)
		}
	}

	if isConnectionError(errors.New("unique constraint violation")) {
		t.Errorf("expected false for logic error")
	}
}

// TestSearchHealth tests in-memory and HTTP search health check.
func TestSearchHealth(t *testing.T) {
	app := New(Config{})
	s := app.Search()
	ctx := context.Background()

	// In-memory fallback health
	err := s.Health(ctx)
	if err != nil {
		t.Errorf("expected nil error for in-memory search health, got %v", err)
	}
}

// TestRedisNetworkMock tests RedisClient over mock in-memory net.Pipe full-duplex socket.
func TestRedisNetworkMock(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := serverConn.Read(buf)
			if err != nil {
				return
			}
			req := string(buf[:n])
			if strings.Contains(req, "PING") {
				_, _ = serverConn.Write([]byte("+PONG\r\n"))
			} else if strings.Contains(req, "GET") {
				_, _ = serverConn.Write([]byte("$5\r\nhello\r\n"))
			} else if strings.Contains(req, "DEL") || strings.Contains(req, "EVAL") || strings.Contains(req, "PF") || strings.Contains(req, "Z") || strings.Contains(req, "PUBLISH") || strings.Contains(req, "BF") || strings.Contains(req, "CF") {
				_, _ = serverConn.Write([]byte(":1\r\n"))
			} else if strings.Contains(req, "INCR") {
				_, _ = serverConn.Write([]byte(":2\r\n"))
			} else {
				_, _ = serverConn.Write([]byte("+OK\r\n"))
			}
		}
	}()

	client := NewRedisClient(RedisConfig{
		Addr:        "memory",
		DialTimeout: 2 * time.Second,
		PoolSize:    1,
	})
	client.isMemory = false
	client.connPool = make(chan net.Conn, 1)
	client.connPool <- clientConn

	ctx := context.Background()

	// 1. Ping
	if err := client.Ping(ctx); err != nil {
		t.Errorf("mock Redis Ping failed: %v", err)
	}

	// 2. Set
	if err := client.Set(ctx, "k1", "val", 0); err != nil {
		t.Errorf("mock Redis Set failed: %v", err)
	}

	// 3. Get
	val, err := client.Get(ctx, "k1")
	if err != nil || val != "hello" {
		t.Errorf("mock Redis Get failed: %v, val=%s", err, val)
	}

	// 4. Incr
	num, err := client.Incr(ctx, "counter")
	if err != nil || num != 2 {
		t.Errorf("mock Redis Incr failed: %v, num=%d", err, num)
	}

	// 5. Del
	if err := client.Del(ctx, "k1"); err != nil {
		t.Errorf("mock Redis Del failed: %v", err)
	}

	// 6. Lock, Renew, Unlock
	lock, err := client.Lock(ctx, "network_lock", 5*time.Second)
	if err != nil {
		t.Errorf("network Lock failed: %v", err)
	} else {
		_ = lock.Renew(ctx, 10*time.Second)
		_ = lock.Unlock(ctx)
	}

	// 7. PubSub
	_ = client.Publish(ctx, "chan", "msg")

	// 8. HyperLogLog
	_, _ = client.HyperLogLogAdd(ctx, "hll", "a", "b")
	_, _ = client.HyperLogLogCount(ctx, "hll")

	// 9. Bloom & Cuckoo
	_ = client.BloomAdd(ctx, "bf", "item")
	_, _ = client.BloomContains(ctx, "bf", "item")
	_ = client.CuckooAdd(ctx, "cf", "item")
	_, _ = client.CuckooContains(ctx, "cf", "item")
	_, _ = client.CuckooDelete(ctx, "cf", "item")

	// 10. TopK & Leaderboard
	_ = client.TopKReserve(ctx, "topk", 5)
	_, _ = client.TopKAdd(ctx, "topk", "item")
	_ = client.LeaderboardAdd(ctx, "lb", "usr", 100)
	_, _ = client.LeaderboardIncrBy(ctx, "lb", "usr", 10)
	_ = client.LeaderboardRemove(ctx, "lb", "usr")

	_ = client.Close()
}

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func TestSearchHTTPMode(t *testing.T) {
	client := NewSearchClient(ElasticsearchConfig{
		Addresses: []string{"http://127.0.0.1:9200"},
		Username:  "elastic",
		Password:  "secret",
	})

	client.httpClient.Transport = mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		if strings.Contains(path, "_cluster/health") {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"status":"green"}`)),
				Header:     make(http.Header),
			}, nil
		}
		if strings.Contains(path, "_search") {
			body := `{"took":2,"hits":{"total":{"value":1},"hits":[{"_id":"1","_score":1.0,"_source":{"name":"gox"}}]}}`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}
		// Index
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(`{"result":"created"}`)),
			Header:     make(http.Header),
		}, nil
	})

	ctx := context.Background()

	// 1. Health
	if err := client.Health(ctx); err != nil {
		t.Errorf("Health failed: %v", err)
	}

	// 2. Index
	if err := client.Index(ctx, "items", "1", map[string]string{"name": "gox"}); err != nil {
		t.Errorf("Index failed: %v", err)
	}

	// 3. Search
	res, err := client.Search(ctx, "items", "gox")
	if err != nil || res.Total != 1 {
		t.Errorf("Search failed: %v, total=%d", err, res.Total)
	}
}

func TestContextGettersComprehensive(t *testing.T) {
	// Nil engine context
	cNil := &Context{}
	if cNil.SQLite() != nil || cNil.Postgres() != nil || cNil.DB() != nil {
		t.Error("nil engine should return nil DBs")
	}
	if cNil.ReadDB() != nil || cNil.WriteDB() != nil {
		t.Error("nil engine should return nil SQL DBs")
	}
	if cNil.Redis() != nil || cNil.Queue() != nil || cNil.Search() != nil || cNil.Bloom() != nil {
		t.Error("nil engine should return nil client integrations")
	}
	if cNil.Method() != "" {
		t.Error("nil request method should be empty")
	}
	if cNil.Arena() != nil {
		t.Error("nil arena should return nil")
	}
	if val, err := cNil.ParamInt("id"); err == nil || val != 0 {
		t.Error("empty params should error on ParamInt")
	}

	// Active engine context
	app := New(Config{})
	db, err := NewDatabase(DBConfig{Driver: DBSQLite, DSN: "file::memory:?cache=shared&mode=rwc"})
	if err == nil {
		app.sqlite = db
	}
	req, _ := http.NewRequest("POST", "/test/123", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	w := httptest.NewRecorder()
	c := &Context{
		Writer:  w,
		Request: req,
		Params:  Params{Param{Key: "id", Value: "123"}, Param{Key: "bad", Value: "abc"}},
		engine:  app,
		keys:    map[string]any{"key1": "val1"},
	}

	if c.Method() != "POST" {
		t.Errorf("expected POST, got %s", c.Method())
	}
	idVal, err := c.ParamInt("id")
	if err != nil || idVal != 123 {
		t.Errorf("expected 123, got %d (%v)", idVal, err)
	}
	if _, err := c.ParamInt("bad"); err == nil {
		t.Error("expected error for non-integer paramInt")
	}
	if c.Params.Get("id") != "123" {
		t.Errorf("expected 123 from Params.Get, got %s", c.Params.Get("id"))
	}
	var countParams int
	for k, v := range c.Params.All() {
		if k != "" && v != "" {
			countParams++
		}
	}
	if countParams != 2 {
		t.Errorf("expected 2 params from Params.All(), got %d", countParams)
	}
	if c.SQLite() == nil || c.DB() == nil {
		t.Error("expected valid sqlite DB instance")
	}
	c.UsePrimaryDB()
	if c.WriteDB() == nil || c.ReadDB() == nil {
		t.Error("expected valid read/write sql.DB")
	}
	if c.Redis() == nil || c.Queue() == nil || c.Search() == nil || c.Bloom() == nil {
		t.Error("expected valid default clients on app context")
	}
	v, ok := c.Get("key1")
	if !ok || v != "val1" {
		t.Error("expected key1 from c.Get")
	}
	if c.RealIP() != "198.51.100.1" {
		t.Errorf("expected 198.51.100.1, got %s", c.RealIP())
	}
	if err := c.Error(http.StatusBadRequest, "invalid request"); err != nil {
		t.Errorf("Error() returned error: %v", err)
	}
}

func TestDatabaseEdgeComprehensive(t *testing.T) {
	db, err := NewDatabase(DBConfig{Driver: DBSQLite, DSN: "file::memory:?cache=shared&mode=rwc"})
	if err != nil {
		t.Fatalf("failed to init in-memory sqlite: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}

	// Add replica
	if err := db.AddReplica("file::memory:?cache=shared&mode=rwc"); err != nil {
		t.Fatalf("failed to add replica: %v", err)
	}

	// Test retryable error detection
	if !isRetryableDBError(fmt.Errorf("database is locked")) {
		t.Error("expected locked to be retryable")
	}
	if isRetryableDBError(fmt.Errorf("syntax error in SQL")) {
		t.Error("expected syntax error not to be retryable")
	}

	// Test WithTxRetry
	attempts := 0
	err = db.WithTxRetry(ctx, 3, func(tx *sql.Tx) error {
		attempts++
		if attempts < 2 {
			return fmt.Errorf("database is locked")
		}
		_, err := tx.Exec("CREATE TABLE IF NOT EXISTS items (id INT);")
		return err
	})
	if err != nil {
		t.Errorf("WithTxRetry failed: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}



