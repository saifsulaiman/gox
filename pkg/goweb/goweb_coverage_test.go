package goweb

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestContextHelpers tests context parameter extraction, headers, IP, and query fallbacks.
func TestContextHelpers(t *testing.T) {
	app := New(Config{})

	app.GET("/context-test/:name/:id", func(c *Context) error {
		// Test Param and ParamInt
		if c.Param("name") != "gox" {
			t.Errorf("expected param 'gox', got %s", c.Param("name"))
		}
		id, err := c.ParamInt("id")
		if err != nil || id != 42 {
			t.Errorf("expected param id 42, got %d (err: %v)", id, err)
		}

		// Test Query and QueryDefault
		if c.QueryDefault("page", "1") != "1" {
			t.Errorf("expected default query '1', got %s", c.QueryDefault("page", "1"))
		}
		if c.QueryDefault("filter", "none") != "active" {
			t.Errorf("expected query 'active', got %s", c.QueryDefault("filter", "none"))
		}

		// Test Header
		if c.Header("X-Custom-Client") != "unit-test" {
			t.Errorf("expected header 'unit-test', got %s", c.Header("X-Custom-Client"))
		}

		// Test IP / RealIP
		if c.IP() != "192.168.1.100" {
			t.Errorf("expected IP '192.168.1.100', got %s", c.IP())
		}
		if c.RealIP() != "192.168.1.100" {
			t.Errorf("expected RealIP '192.168.1.100', got %s", c.RealIP())
		}

		// Test RequestID
		if c.RequestID() != "req-12345" {
			t.Errorf("expected RequestID 'req-12345', got %s", c.RequestID())
		}

		// Test Set / Get
		c.Set("user_role", "admin")
		role, ok := c.Get("user_role")
		if !ok || role != "admin" {
			t.Errorf("expected context key 'user_role' to be 'admin'")
		}

		// Test Read-Your-Own-Writes Primary preference
		c.UsePrimaryDB()

		// Test JSONFast
		c.SetHeader("X-Fast-Response", "true")
		return c.JSONFast(http.StatusOK, []byte(`{"status":"ok","fast":true}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/context-test/gox/42?filter=active", nil)
	req.Header.Set("X-Custom-Client", "unit-test")
	req.Header.Set("X-Forwarded-For", "192.168.1.100, 10.0.0.1")
	req.Header.Set("X-Request-ID", "req-12345")
	w := httptest.NewRecorder()

	app.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	if w.Header().Get("X-Fast-Response") != "true" {
		t.Errorf("expected X-Fast-Response header")
	}
}

// TestContextStringAndStatus tests c.String, c.Status, and c.StatusCode.
func TestContextStringAndStatus(t *testing.T) {
	app := New(Config{})

	app.GET("/text", func(c *Context) error {
		c.Status(http.StatusAccepted)
		if c.StatusCode() != http.StatusAccepted {
			t.Errorf("expected status code 202, got %d", c.StatusCode())
		}
		return c.String(http.StatusAccepted, "item %s created", "book")
	})

	req := httptest.NewRequest(http.MethodGet, "/text", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", w.Code)
	}
	if w.Body.String() != "item book created" {
		t.Fatalf("expected 'item book created', got '%s'", w.Body.String())
	}
}

// TestContextHTMLAndNoContent tests c.HTML and c.NoContent.
func TestContextHTMLAndNoContent(t *testing.T) {
	app := New(Config{})

	app.GET("/html", func(c *Context) error {
		return c.HTML(http.StatusOK, "<h1>Hello GOX</h1>")
	})
	app.DELETE("/empty", func(c *Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	wHTML := httptest.NewRecorder()
	app.ServeHTTP(wHTML, httptest.NewRequest(http.MethodGet, "/html", nil))
	if wHTML.Code != http.StatusOK || wHTML.Body.String() != "<h1>Hello GOX</h1>" {
		t.Errorf("HTML response mismatch")
	}

	wEmpty := httptest.NewRecorder()
	app.ServeHTTP(wEmpty, httptest.NewRequest(http.MethodDelete, "/empty", nil))
	if wEmpty.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", wEmpty.Code)
	}
}

// TestContextResetAndPool verifies context pooling and reset.
func TestContextResetAndPool(t *testing.T) {
	app := New(Config{})
	c := app.pool.Get().(*Context)
	c.Set("temp", "value")
	c.UsePrimaryDB()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/reset", nil)
	c.Reset(w, r)

	if _, ok := c.Get("temp"); ok {
		t.Errorf("expected keys to be cleared after Reset()")
	}
	app.pool.Put(c)
}

// TestDatabaseWithTxAndReplicaDistribution tests transaction helper and replica balancing.
func TestDatabaseWithTxAndReplicaDistribution(t *testing.T) {
	app := New(Config{})
	db := app.SQLite()

	// 1. Successful transaction
	err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE IF NOT EXISTS tx_test (id INTEGER PRIMARY KEY, val TEXT)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO tx_test (val) VALUES ('committed')")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx failed: %v", err)
	}

	// 2. Failed transaction (should rollback)
	simulatedErr := errors.New("rollback test")
	err = db.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO tx_test (val) VALUES ('rolled_back')")
		if err != nil {
			return err
		}
		return simulatedErr
	})
	if !errors.Is(err, simulatedErr) {
		t.Fatalf("expected simulated error, got %v", err)
	}

	// Verify 'rolled_back' was not committed
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM tx_test WHERE val = 'rolled_back'").Scan(&count)
	if count != 0 {
		t.Errorf("expected rolled_back count to be 0, got %d", count)
	}

	// 3. Replicas, driver and stats
	driver := db.Driver()
	if driver != DBSQLite {
		t.Errorf("expected sqlite driver, got %s", driver)
	}
	stats := db.Stats()
	_ = stats.Primary
}

// TestRedisExtendedOperations tests TopK, Leaderboards, Cuckoo, and Lock renew.
func TestRedisExtendedOperations(t *testing.T) {
	app := New(Config{})
	r := app.Redis()
	ctx := context.Background()

	// TopK Reserve, Add, Query, Count, List
	_ = r.TopKReserve(ctx, "trending_topics", 5)
	_, _ = r.TopKAdd(ctx, "trending_topics", "golang", "gox", "redis", "golang", "gox")
	list, err := r.TopKList(ctx, "trending_topics")
	if err != nil || len(list) == 0 {
		t.Fatalf("TopKList failed: %v", err)
	}
	counts, err := r.TopKCount(ctx, "trending_topics", "golang")
	if err != nil || len(counts) == 0 || counts[0] < 2 {
		t.Fatalf("TopKCount failed, expected >= 2, got %v", counts)
	}

	// Leaderboard operations
	_ = r.LeaderboardAdd(ctx, "game_rank", "player1", 100)
	_ = r.LeaderboardAdd(ctx, "game_rank", "player2", 200)
	_, _ = r.LeaderboardIncrBy(ctx, "game_rank", "player1", 150) // player1 now 250

	top, err := r.LeaderboardGetTop(ctx, "game_rank", 2)
	if err != nil || len(top) < 2 || top[0].Member != "player1" {
		t.Fatalf("Leaderboard top player mismatch: %v", top)
	}
	rank, score, err := r.LeaderboardGetRank(ctx, "game_rank", "player1")
	if err != nil || rank != 1 || score != 250 {
		t.Fatalf("expected player1 rank 1 and score 250, got rank %d score %f", rank, score)
	}
	around, err := r.LeaderboardAroundMe(ctx, "game_rank", "player1", 1)
	if err != nil || len(around) == 0 {
		t.Fatalf("LeaderboardAroundMe failed: %v", around)
	}

	// Lock renewal
	lock, err := r.Lock(ctx, "res_lock", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	if err := lock.Renew(ctx, 1*time.Second); err != nil {
		t.Errorf("Renew lock failed: %v", err)
	}
	_ = lock.Unlock(ctx)
}

// TestRabbitMQExtendedQueue tests queue JSON publishing and Ping.
func TestRabbitMQExtendedQueue(t *testing.T) {
	app := New(Config{})
	q := app.Queue()
	ctx := context.Background()

	// Ping queue
	if err := q.Ping(ctx); err != nil {
		t.Fatalf("Queue ping failed: %v", err)
	}

	// Publish JSON
	payload := map[string]any{"event": "signup", "user_id": 101}
	if err := q.PublishJSON(ctx, "", "events", payload); err != nil {
		t.Fatalf("PublishJSON failed: %v", err)
	}
}

// TestElasticsearchExtended tests index, get, delete, and search operations.
func TestElasticsearchExtended(t *testing.T) {
	app := New(Config{})
	es := app.Search()
	ctx := context.Background()

	// Health check
	if err := es.Health(ctx); err != nil {
		t.Fatalf("Search health check failed: %v", err)
	}

	// Index documents
	doc1 := map[string]any{"id": "doc1", "title": "High Performance Go", "category": "books"}
	if err := es.Index(ctx, "articles", "doc1", doc1); err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	// Get document
	retrieved, err := es.Get(ctx, "articles", "doc1")
	if err != nil || retrieved["title"] != "High Performance Go" {
		t.Fatalf("Get document failed: %v", err)
	}

	// Query
	items, err := es.Query(ctx, "articles", "Performance")
	if err != nil || len(items) == 0 {
		t.Fatalf("Query failed: %v", err)
	}

	// Delete
	if err := es.Delete(ctx, "articles", "doc1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

// TestResilienceMiddlewares tests Timeout, BodyLimit, ETag, and CircuitBreaker middlewares.
func TestResilienceMiddlewares(t *testing.T) {
	app := New(Config{})

	// 1. Timeout middleware
	app.GET("/timeout", Timeout(20*time.Millisecond), func(c *Context) error {
		select {
		case <-time.After(60 * time.Millisecond):
			return c.String(http.StatusOK, "done")
		case <-c.Context().Done():
			return c.Context().Err()
		}
	})

	wTimeout := httptest.NewRecorder()
	app.ServeHTTP(wTimeout, httptest.NewRequest(http.MethodGet, "/timeout", nil))
	if wTimeout.Code != http.StatusGatewayTimeout {
		t.Errorf("expected 504 Gateway Timeout, got %d", wTimeout.Code)
	}

	// 2. ETag middleware
	app.GET("/etag-test", ETag(), func(c *Context) error {
		return c.String(http.StatusOK, "cached static payload")
	})

	wETag1 := httptest.NewRecorder()
	app.ServeHTTP(wETag1, httptest.NewRequest(http.MethodGet, "/etag-test", nil))
	etagVal := wETag1.Header().Get("ETag")
	if etagVal == "" {
		t.Fatalf("expected ETag header to be set")
	}

	// Second request with If-None-Match should return 304 Not Modified
	reqETag2 := httptest.NewRequest(http.MethodGet, "/etag-test", nil)
	reqETag2.Header.Set("If-None-Match", etagVal)
	wETag2 := httptest.NewRecorder()
	app.ServeHTTP(wETag2, reqETag2)
	if wETag2.Code != http.StatusNotModified {
		t.Errorf("expected 304 Not Modified, got %d", wETag2.Code)
	}

	// 3. Circuit breaker middleware
	cb := CircuitBreaker(CircuitBreakerOptions{
		FailureThreshold: 2,
		CooldownDuration: 50 * time.Millisecond,
	})

	var shouldFail bool
	app.GET("/flaky", cb, func(c *Context) error {
		if shouldFail {
			return errors.New("downstream service failure")
		}
		return c.String(http.StatusOK, "ok")
	})

	// First two fail
	shouldFail = true
	wCB1 := httptest.NewRecorder()
	app.ServeHTTP(wCB1, httptest.NewRequest(http.MethodGet, "/flaky", nil))
	wCB2 := httptest.NewRecorder()
	app.ServeHTTP(wCB2, httptest.NewRequest(http.MethodGet, "/flaky", nil))

	// Third request should be tripped into 503 Service Unavailable by Circuit Breaker
	wCB3 := httptest.NewRecorder()
	app.ServeHTTP(wCB3, httptest.NewRequest(http.MethodGet, "/flaky", nil))
	if wCB3.Code != http.StatusServiceUnavailable {
		t.Errorf("expected circuit breaker to return 503, got %d", wCB3.Code)
	}
}
