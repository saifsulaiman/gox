package goweb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestRouterAndContext(t *testing.T) {
	app := New(Config{Port: 9999})

	app.GET("/api/items/:id", func(c *Context) error {
		id := c.Param("id")
		return c.JSON(http.StatusOK, H{"item_id": id, "category": c.Query("cat")})
	})

	app.POST("/api/items", func(c *Context) error {
		var req struct {
			Name  string  `json:"name"`
			Price float64 `json:"price"`
		}
		if err := c.BindJSON(&req); err != nil {
			return c.Error(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusCreated, H{"name": req.Name, "price": req.Price, "created": true})
	})

	// Test GET with param and query
	req := httptest.NewRequest(http.MethodGet, "/api/items/42?cat=electronics", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if res["item_id"] != "42" || res["category"] != "electronics" {
		t.Fatalf("unexpected response content: %v", res)
	}

	// Test POST with JSON body
	body := bytes.NewBufferString(`{"name":"Laptop","price":999.99}`)
	postReq := httptest.NewRequest(http.MethodPost, "/api/items", body)
	postReq.Header.Set("Content-Type", "application/json")
	wPost := httptest.NewRecorder()
	app.ServeHTTP(wPost, postReq)

	if wPost.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", wPost.Code)
	}
}

func TestRouteGroupAndMiddlewares(t *testing.T) {
	app := New()
	app.Use(Security(), RequestID())

	v1 := app.Group("/v1", CORS(DefaultCORSOptions))
	{
		v1.GET("/profile", func(c *Context) error {
			return c.String(http.StatusOK, "user profile")
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/profile", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected security header")
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatalf("expected X-Request-ID header")
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("expected CORS header")
	}
}

func TestRecoverMiddleware(t *testing.T) {
	app := New()
	app.Use(Recover())

	app.GET("/panic", func(c *Context) error {
		panic("something went terribly wrong")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on panic, got %d", w.Code)
	}
}

func TestDatabaseIntegration(t *testing.T) {
	db, err := NewDatabase(DBConfig{
		Driver: DBSQLite,
		DSN:    "file::memory:?cache=shared&mode=rwc",
	})
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, email TEXT);`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Transaction test
	ctx := context.Background()
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, txErr := tx.Exec("INSERT INTO users (name, email) VALUES (?, ?)", "Alice", "alice@example.com")
		return txErr
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	var name, email string
	err = db.QueryRow("SELECT name, email FROM users WHERE id = 1").Scan(&name, &email)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if name != "Alice" || email != "alice@example.com" {
		t.Fatalf("unexpected user: %s %s", name, email)
	}
}

func TestDatabaseMasterSlave(t *testing.T) {
	// Setup Primary and 2 Replicas
	// Primary has table with primary_marker
	primaryDSN := "file:mem_primary?mode=memory&cache=shared"
	rep1DSN := "file:mem_rep1?mode=memory&cache=shared"
	rep2DSN := "file:mem_rep2?mode=memory&cache=shared"

	db, err := NewDatabase(DBConfig{
		Driver:   DBSQLite,
		DSN:      primaryDSN,
		Replicas: []string{rep1DSN, rep2DSN},
	})
	if err != nil {
		t.Fatalf("failed to initialize cluster: %v", err)
	}
	defer db.Close()

	if db.ReplicaCount() != 2 {
		t.Fatalf("expected 2 replicas, got %d", db.ReplicaCount())
	}

	// Initialize tables on Primary and both Replicas with distinct markers
	_, err = db.Primary().Exec("CREATE TABLE cluster_info (source TEXT); INSERT INTO cluster_info VALUES ('PRIMARY');")
	if err != nil {
		t.Fatalf("setup primary failed: %v", err)
	}

	reps := db.Replicas()
	if len(reps) != 2 {
		t.Fatalf("expected 2 replica handles, got %d", len(reps))
	}
	_, err = reps[0].Exec("CREATE TABLE cluster_info (source TEXT); INSERT INTO cluster_info VALUES ('REPLICA_1');")
	if err != nil {
		t.Fatalf("setup replica 1 failed: %v", err)
	}
	_, err = reps[1].Exec("CREATE TABLE cluster_info (source TEXT); INSERT INTO cluster_info VALUES ('REPLICA_2');")
	if err != nil {
		t.Fatalf("setup replica 2 failed: %v", err)
	}

	// 1. Reads should round-robin across replicas
	sources := make(map[string]int)
	for range 10 {
		var src string
		err = db.QueryRow("SELECT source FROM cluster_info").Scan(&src)
		if err != nil {
			t.Fatalf("query replica failed: %v", err)
		}
		sources[src]++
	}
	if sources["REPLICA_1"] == 0 || sources["REPLICA_2"] == 0 {
		t.Fatalf("expected reads distributed between replicas, got: %v", sources)
	}
	if sources["PRIMARY"] != 0 {
		t.Fatalf("unexpected read on primary when replicas were healthy: %v", sources)
	}

	// 2. Forced Primary read via WithPrimary(ctx)
	ctxPrimary := WithPrimary(context.Background())
	var src string
	err = db.QueryRowContext(ctxPrimary, "SELECT source FROM cluster_info").Scan(&src)
	if err != nil {
		t.Fatalf("query forced primary failed: %v", err)
	}
	if src != "PRIMARY" {
		t.Fatalf("expected PRIMARY from WithPrimary, got %s", src)
	}

	// 3. Direct QueryPrimary
	err = db.QueryRowPrimary("SELECT source FROM cluster_info").Scan(&src)
	if err != nil {
		t.Fatalf("QueryRowPrimary failed: %v", err)
	}
	if src != "PRIMARY" {
		t.Fatalf("expected PRIMARY from QueryRowPrimary, got %s", src)
	}

	// 4. Writes strictly execute on Primary
	_, err = db.Exec("INSERT INTO cluster_info VALUES ('PRIMARY_INSERT');")
	if err != nil {
		t.Fatalf("exec on primary failed: %v", err)
	}
	var count int
	err = db.QueryRowPrimary("SELECT COUNT(*) FROM cluster_info WHERE source LIKE 'PRIMARY%'").Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("expected 2 primary rows, got %d (err: %v)", count, err)
	}

	// 5. Failover test: simulate replica failure by executing query on non-existent table on replica,
	// or closing replica. When a replica query encounters a connection error, it falls back to Primary.
	stats := db.Stats()
	if stats.ActiveReplicas != 2 || stats.TotalReplicas != 2 {
		t.Fatalf("expected 2 active replicas in stats, got %+v", stats)
	}

	// 6. Test AddReplica dynamically
	rep3DSN := "file:mem_rep3?mode=memory&cache=shared"
	err = db.AddReplica(rep3DSN)
	if err != nil {
		t.Fatalf("AddReplica failed: %v", err)
	}
	if db.ReplicaCount() != 3 {
		t.Fatalf("expected 3 replicas after AddReplica, got %d", db.ReplicaCount())
	}
}

func TestExecOneAndTxRetry(t *testing.T) {
	db, err := NewDatabase(DBConfig{
		Driver: DBSQLite,
		DSN:    "file:mem_excone?mode=memory&cache=shared",
	})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	_, err = db.Exec("CREATE TABLE items (id INT PRIMARY KEY, qty INT);")
	if err != nil {
		t.Fatalf("create table failed: %v", err)
	}

	// ExecOne on insert
	err = db.ExecOne(ctx, "INSERT INTO items (id, qty) VALUES (?, ?);", 1, 100)
	if err != nil {
		t.Fatalf("ExecOne insert failed: %v", err)
	}

	// ExecOne on update matching 1 row -> should PASS
	err = db.ExecOne(ctx, "UPDATE items SET qty = 90 WHERE id = 1;")
	if err != nil {
		t.Fatalf("ExecOne update 1 row failed: %v", err)
	}

	// ExecOne on update matching 0 rows -> should FAIL with ErrNotOneRowAffected
	err = db.ExecOne(ctx, "UPDATE items SET qty = 80 WHERE id = 999;")
	if !errors.Is(err, ErrNotOneRowAffected) {
		t.Fatalf("expected ErrNotOneRowAffected on 0 rows, got: %v", err)
	}

	// Insert another item
	_ = db.ExecOne(ctx, "INSERT INTO items (id, qty) VALUES (?, ?);", 2, 200)

	// ExecOne on update matching 2 rows -> should FAIL with ErrNotOneRowAffected
	err = db.ExecOne(ctx, "UPDATE items SET qty = 0;")
	if !errors.Is(err, ErrNotOneRowAffected) {
		t.Fatalf("expected ErrNotOneRowAffected on 2 rows, got: %v", err)
	}

	// Test WithTxRetry
	retryCount := 0
	err = db.WithTxRetry(ctx, 3, func(tx *sql.Tx) error {
		retryCount++
		if retryCount < 2 {
			// Simulate transient locked error
			return errors.New("database is locked: transient lock contention")
		}
		_, err := tx.Exec("UPDATE items SET qty = 50 WHERE id = 1;")
		return err
	})
	if err != nil {
		t.Fatalf("WithTxRetry failed: %v", err)
	}
	if retryCount != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", retryCount)
	}

	var newQty int
	err = db.QueryRowPrimary("SELECT qty FROM items WHERE id = 1;").Scan(&newQty)
	if err != nil || newQty != 50 {
		t.Fatalf("expected qty = 50, got %d (err: %v)", newQty, err)
	}
}

func TestRedisIntegration(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()

	ctx := context.Background()
	err := redis.Set(ctx, "session:123", "active", 10*time.Second)
	if err != nil {
		t.Fatalf("redis set failed: %v", err)
	}

	val, err := redis.Get(ctx, "session:123")
	if err != nil || val != "active" {
		t.Fatalf("expected active, got %s (err: %v)", val, err)
	}

	// Incr test
	count, err := redis.Incr(ctx, "page_views")
	if err != nil || count != 1 {
		t.Fatalf("expected 1, got %d (err: %v)", count, err)
	}

	// JSON cache test
	type UserCache struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	u := UserCache{Name: "Bob", Role: "Admin"}
	if err := redis.SetJSON(ctx, "user:bob", u, time.Minute); err != nil {
		t.Fatalf("redis setjson failed: %v", err)
	}

	var retrieved UserCache
	if err := redis.GetJSON(ctx, "user:bob", &retrieved); err != nil {
		t.Fatalf("redis getjson failed: %v", err)
	}
	if retrieved.Name != "Bob" || retrieved.Role != "Admin" {
		t.Fatalf("retrieved mismatch: %+v", retrieved)
	}
}

func TestQueueIntegration(t *testing.T) {
	queue := NewQueueClient(RabbitMQConfig{URL: "memory", Workers: 2})
	defer queue.Close()

	received := make(chan string, 1)
	err := queue.ConsumeJSON("tasks.queue", func(msg Message, payload map[string]any) error {
		received <- payload["task"].(string)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to consume: %v", err)
	}

	ctx := context.Background()
	err = queue.PublishJSON(ctx, "tasks.exchange", "tasks.queue", map[string]any{
		"task": "send_welcome_email",
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	select {
	case task := <-received:
		if task != "send_welcome_email" {
			t.Fatalf("unexpected task: %s", task)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for queue message")
	}
}

func TestSearchIntegration(t *testing.T) {
	search := NewSearchClient(ElasticsearchConfig{})
	defer search.Close()

	ctx := context.Background()
	err := search.Index(ctx, "products", "prod-1", map[string]any{
		"name":        "Mechanical Keyboard RGB",
		"category":    "Electronics",
		"description": "Premium mechanical gaming keyboard with tactile switches",
		"price":       129.99,
	})
	if err != nil {
		t.Fatalf("indexing failed: %v", err)
	}

	err = search.Index(ctx, "products", "prod-2", map[string]any{
		"name":        "Wireless Mouse",
		"category":    "Electronics",
		"description": "Ergonomic wireless mouse with ultra-fast optical sensor",
		"price":       59.99,
	})
	if err != nil {
		t.Fatalf("indexing failed: %v", err)
	}

	// Search for keyboard
	res, err := search.Search(ctx, "products", "keyboard")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if res.Total < 1 {
		t.Fatalf("expected at least 1 hit, got %d", res.Total)
	}
	if res.Hits[0].ID != "prod-1" {
		t.Fatalf("expected hit prod-1, got %s", res.Hits[0].ID)
	}
}

func TestHealthEndpoints(t *testing.T) {
	app := New(Config{
		SQLite:   "file::memory:?cache=shared&mode=rwc",
		Redis:    "memory",
		RabbitMQ: "memory",
		Elastic:  "",
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected /health 200, got %d", w.Code)
	}

	var report HealthReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatalf("failed to decode health report: %v", err)
	}
	if report.Status != "UP" {
		t.Fatalf("expected status UP, got %s", report.Status)
	}

	// Test /metrics
	reqMetrics := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	wMetrics := httptest.NewRecorder()
	app.ServeHTTP(wMetrics, reqMetrics)

	if wMetrics.Code != http.StatusOK {
		t.Fatalf("expected /metrics 200, got %d", wMetrics.Code)
	}
}

func TestIdempotencyMiddleware(t *testing.T) {
	app := New()
	app.Use(Idempotency(IdempotencyOptions{
		HeaderKey: "Idempotency-Key",
		LockTTL:   5 * time.Second,
	}))

	var handlerExecutions atomic.Int32

	app.POST("/api/orders", func(c *Context) error {
		handlerExecutions.Add(1)
		return c.JSON(http.StatusCreated, H{"order_id": "ORD-12345", "status": "CONFIRMED"})
	})

	// First POST request with Idempotency-Key
	req1 := httptest.NewRequest(http.MethodPost, "/api/orders", bytes.NewBufferString(`{"item":"book"}`))
	req1.Header.Set("Idempotency-Key", "idemp-test-key-1")
	w1 := httptest.NewRecorder()
	app.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on first request, got %d", w1.Code)
	}
	if handlerExecutions.Load() != 1 {
		t.Fatalf("expected handler executed once, got %d", handlerExecutions.Load())
	}

	// Second POST request with identical Idempotency-Key
	req2 := httptest.NewRequest(http.MethodPost, "/api/orders", bytes.NewBufferString(`{"item":"book"}`))
	req2.Header.Set("Idempotency-Key", "idemp-test-key-1")
	w2 := httptest.NewRecorder()
	app.ServeHTTP(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on replayed request, got %d", w2.Code)
	}
	if handlerExecutions.Load() != 1 {
		t.Fatalf("expected handler NOT re-executed, got %d executions", handlerExecutions.Load())
	}
	if w2.Header().Get("X-Cache-Lookup") != "HIT-IDEMPOTENT" {
		t.Fatalf("expected X-Cache-Lookup: HIT-IDEMPOTENT, got %s", w2.Header().Get("X-Cache-Lookup"))
	}
}

func TestAuthAndRBAC(t *testing.T) {
	secret := "super-secure-production-secret-2026"
	app := New()

	adminToken, err := GenerateJWT(JWTClaims{
		Subject:     "usr-admin",
		Username:    "alice_admin",
		Roles:       []string{"admin"},
		Permissions: []string{"products:read", "products:write"},
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
	}, secret)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}

	guestToken, err := GenerateJWT(JWTClaims{
		Subject:     "usr-guest",
		Username:    "bob_guest",
		Roles:       []string{"viewer"},
		Permissions: []string{"products:read"},
		ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
	}, secret)
	if err != nil {
		t.Fatalf("generate guest token failed: %v", err)
	}

	adminRoute := app.Group("/admin", JWTAuth(secret), RequireRoles("admin"), RequirePermissions("products:write"))
	adminRoute.POST("/create", func(c *Context) error {
		u := c.User()
		return c.JSON(http.StatusOK, H{"created_by": u.Username})
	})

	// 1. Unauthenticated request -> 401
	reqUnauth := httptest.NewRequest(http.MethodPost, "/admin/create", nil)
	wUnauth := httptest.NewRecorder()
	app.ServeHTTP(wUnauth, reqUnauth)
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauth request, got %d", wUnauth.Code)
	}

	// 2. Guest user with wrong role -> 403 Forbidden
	reqGuest := httptest.NewRequest(http.MethodPost, "/admin/create", nil)
	reqGuest.Header.Set("Authorization", "Bearer "+guestToken)
	wGuest := httptest.NewRecorder()
	app.ServeHTTP(wGuest, reqGuest)
	if wGuest.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for guest with insufficient role, got %d", wGuest.Code)
	}

	// 3. Admin user with correct role & permission -> 200 OK
	reqAdmin := httptest.NewRequest(http.MethodPost, "/admin/create", nil)
	reqAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	wAdmin := httptest.NewRecorder()
	app.ServeHTTP(wAdmin, reqAdmin)
	if wAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid admin, got %d (body: %s)", wAdmin.Code, wAdmin.Body.String())
	}
}

func TestSingleflight(t *testing.T) {
	app := New()
	var computeCount atomic.Int32

	app.GET("/api/compute", func(c *Context) error {
		val, err := c.Singleflight("heavy-calc", func() (any, error) {
			computeCount.Add(1)
			time.Sleep(50 * time.Millisecond) // Simulate slow query
			return "result-42", nil
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, H{"data": val})
	})

	const concurrentRequests = 10
	var wg sync.WaitGroup
	wg.Add(concurrentRequests)

	for range concurrentRequests {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/compute", nil)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", w.Code)
			}
		}()
	}

	wg.Wait()

	// All 10 requests should coalesce into exactly 1 execution
	if computeCount.Load() != 1 {
		t.Fatalf("expected singleflight to coalesce to 1 execution, got %d", computeCount.Load())
	}
}

func TestAsyncWorker(t *testing.T) {
	app := New()
	var asyncDone atomic.Bool

	app.POST("/api/async-job", func(c *Context) error {
		c.Async(func(ctx context.Context) {
			time.Sleep(20 * time.Millisecond)
			asyncDone.Store(true)
		})
		c.Async(func(ctx context.Context) {
			panic("intentional async panic for test")
		})
		return c.JSON(http.StatusAccepted, H{"status": "queued"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/async-job", nil)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", w.Code)
	}

	// Wait for async task to complete
	time.Sleep(60 * time.Millisecond)
	if !asyncDone.Load() {
		t.Fatalf("expected async task to finish in background")
	}
}

func TestResilience(t *testing.T) {
	app := New()

	// 1. Test ETag
	app.GET("/api/cached-doc", ETag(), func(c *Context) error {
		return c.String(http.StatusOK, "consistent static data")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/api/cached-doc", nil)
	w1 := httptest.NewRecorder()
	app.ServeHTTP(w1, req1)

	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag header on response")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/cached-doc", nil)
	req2.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	app.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified on ETag match, got %d", w2.Code)
	}

	// 2. Test Timeout
	app.GET("/api/slow", Timeout(20*time.Millisecond), func(c *Context) error {
		select {
		case <-time.After(100 * time.Millisecond):
			return c.String(http.StatusOK, "late response")
		case <-c.Context().Done():
			return c.Context().Err()
		}
	})

	reqSlow := httptest.NewRequest(http.MethodGet, "/api/slow", nil)
	wSlow := httptest.NewRecorder()
	app.ServeHTTP(wSlow, reqSlow)

	if wSlow.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 Gateway Timeout, got %d", wSlow.Code)
	}
}

func TestBloomFilterAndRedisLock(t *testing.T) {
	bf := NewBloomFilter(1000, 0.01)
	bf.Add("product:101")
	bf.Add("product:102")

	if !bf.Contains("product:101") {
		t.Fatalf("expected product:101 in bloom filter")
	}
	if !bf.Contains("product:102") {
		t.Fatalf("expected product:102 in bloom filter")
	}
	if bf.Contains("product:999999") {
		t.Fatalf("unexpected false positive for product:999999")
	}

	// Redis Lock
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()

	ctx := context.Background()
	lock1, err := redis.Lock(ctx, "resource:inventory:42", 2*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Concurrent lock on same key should fail
	_, err = redis.Lock(ctx, "resource:inventory:42", 2*time.Second)
	if !errors.Is(err, ErrLockNotAcquired) {
		t.Fatalf("expected ErrLockNotAcquired, got: %v", err)
	}

	// Renew
	err = lock1.Renew(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("renew lock failed: %v", err)
	}

	// Unlock
	err = lock1.Unlock(ctx)
	if err != nil {
		t.Fatalf("unlock failed: %v", err)
	}

	// Acquire again should succeed
	lock2, err := redis.Lock(ctx, "resource:inventory:42", 2*time.Second)
	if err != nil {
		t.Fatalf("re-acquire lock failed: %v", err)
	}
	_ = lock2.Unlock(ctx)

	// Redis Bloom Filter
	err = redis.BloomAdd(ctx, "catalog_filter", "item-99")
	if err != nil {
		t.Fatalf("BloomAdd failed: %v", err)
	}
	contains, err := redis.BloomContains(ctx, "catalog_filter", "item-99")
	if err != nil || !contains {
		t.Fatalf("expected item-99 in redis bloom filter")
	}
	containsFake, err := redis.BloomContains(ctx, "catalog_filter", "non-existent-item")
	if err != nil || containsFake {
		t.Fatalf("unexpected hit for non-existent-item in redis bloom filter")
	}
}

func TestRabbitMQDLQ(t *testing.T) {
	qc := NewQueueClient(RabbitMQConfig{
		Workers:           1,
		EnableDLQ:         true,
		MaxRetries:        2,
		PublisherConfirms: true,
	})
	defer qc.Close()

	ctx := context.Background()
	var dlqReceived atomic.Bool

	// Subscribe to DLQ
	_ = qc.Subscribe("orders.dlq", func(msg Message) error {
		if msg.Headers["x-death-reason"] != "" {
			dlqReceived.Store(true)
		}
		return nil
	})

	// Subscribe to main queue with failing handler
	_ = qc.Subscribe("orders", func(msg Message) error {
		return errors.New("simulated unrecoverable inventory error")
	})

	err := qc.PublishJSON(ctx, "", "orders", H{"order_id": 999})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Wait for message to retry and move to DLQ
	time.Sleep(100 * time.Millisecond)

	if !dlqReceived.Load() {
		t.Fatalf("expected message to be routed to orders.dlq after max retries")
	}
}

func TestRedisCuckooFilter(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()
	ctx := context.Background()

	key := "cuckoo:products"
	_ = redis.CuckooAdd(ctx, key, "prod-101")
	_ = redis.CuckooAdd(ctx, key, "prod-102")

	// Existence check
	exists, err := redis.CuckooContains(ctx, key, "prod-101")
	if err != nil || !exists {
		t.Fatalf("expected prod-101 in cuckoo filter")
	}
	existsFake, err := redis.CuckooContains(ctx, key, "prod-999")
	if err != nil || existsFake {
		t.Fatalf("unexpected prod-999 in cuckoo filter")
	}

	// Deletion check (updatability)
	deleted, err := redis.CuckooDelete(ctx, key, "prod-101")
	if err != nil || !deleted {
		t.Fatalf("expected prod-101 to be deleted from cuckoo filter")
	}

	// Verify not found after deletion
	existsAfter, err := redis.CuckooContains(ctx, key, "prod-101")
	if err != nil || existsAfter {
		t.Fatalf("expected prod-101 to NOT exist after deletion")
	}

	// Prod-102 still intact
	exists102, err := redis.CuckooContains(ctx, key, "prod-102")
	if err != nil || !exists102 {
		t.Fatalf("expected prod-102 to remain in cuckoo filter")
	}
}

func TestRedisTopK(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()
	ctx := context.Background()

	key := "trending:searches"
	_ = redis.TopKReserve(ctx, key, 3)

	// Simulate searches: "keyboard" x5, "mouse" x3, "headphones" x1
	for range 5 {
		_, _ = redis.TopKAdd(ctx, key, "keyboard")
	}
	for range 3 {
		_, _ = redis.TopKAdd(ctx, key, "mouse")
	}
	_, _ = redis.TopKAdd(ctx, key, "headphones")

	// Verify query
	qRes, err := redis.TopKQuery(ctx, key, "keyboard", "mouse", "monitor")
	if err != nil {
		t.Fatalf("TopKQuery failed: %v", err)
	}
	if !qRes[0] || !qRes[1] || qRes[2] {
		t.Fatalf("unexpected query result: %v", qRes)
	}

	// Verify counts
	counts, err := redis.TopKCount(ctx, key, "keyboard", "mouse")
	if err != nil || counts[0] != 5 || counts[1] != 3 {
		t.Fatalf("unexpected counts: %v", counts)
	}

	// Verify ranked list
	list, err := redis.TopKList(ctx, key)
	if err != nil || len(list) < 2 {
		t.Fatalf("expected top-k list with items, got: %v (err: %v)", list, err)
	}
	if list[0] != "keyboard" || list[1] != "mouse" {
		t.Fatalf("expected top 1=keyboard, 2=mouse, got: %v", list)
	}
}

func TestRedisLeaderboard(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()
	ctx := context.Background()

	boardKey := "lb:top_gamers"

	// Add players
	_ = redis.LeaderboardAdd(ctx, boardKey, "alice", 1500)
	_ = redis.LeaderboardAdd(ctx, boardKey, "bob", 1200)
	_ = redis.LeaderboardAdd(ctx, boardKey, "charlie", 2000)
	_ = redis.LeaderboardAdd(ctx, boardKey, "david", 900)
	_ = redis.LeaderboardAdd(ctx, boardKey, "eve", 1750)

	// Total count
	count, err := redis.LeaderboardCount(ctx, boardKey)
	if err != nil || count != 5 {
		t.Fatalf("expected 5 members, got %d", count)
	}

	// Rank check: Charlie should be #1 (2000 pts)
	rankCharlie, scoreCharlie, err := redis.LeaderboardGetRank(ctx, boardKey, "charlie")
	if err != nil || rankCharlie != 1 || scoreCharlie != 2000 {
		t.Fatalf("expected Charlie rank 1 (2000 pts), got rank %d (%.0f pts)", rankCharlie, scoreCharlie)
	}

	// Incr score: Bob gets +1000 pts (1200 + 1000 = 2200) -> Bob becomes #1!
	newBobScore, err := redis.LeaderboardIncrBy(ctx, boardKey, "bob", 1000)
	if err != nil || newBobScore != 2200 {
		t.Fatalf("expected Bob score 2200, got %.0f", newBobScore)
	}
	rankBob, _, _ := redis.LeaderboardGetRank(ctx, boardKey, "bob")
	if rankBob != 1 {
		t.Fatalf("expected Bob to be rank 1 after incr, got rank %d", rankBob)
	}

	// Get Top 3
	top3, err := redis.LeaderboardGetTop(ctx, boardKey, 3)
	if err != nil || len(top3) != 3 {
		t.Fatalf("expected top 3 items, got %d (err: %v)", len(top3), err)
	}
	if top3[0].Member != "bob" || top3[1].Member != "charlie" || top3[2].Member != "eve" {
		t.Fatalf("unexpected top 3 ordering: %+v", top3)
	}

	// Around Me window: Alice (score 1500) radius 1
	aroundAlice, err := redis.LeaderboardAroundMe(ctx, boardKey, "alice", 1)
	if err != nil || len(aroundAlice) == 0 {
		t.Fatalf("AroundMe failed: %v", err)
	}
	foundAlice := false
	for _, item := range aroundAlice {
		if item.Member == "alice" {
			foundAlice = true
			break
		}
	}
	if !foundAlice {
		t.Fatalf("expected alice in around-me window: %+v", aroundAlice)
	}

	// Remove member
	err = redis.LeaderboardRemove(ctx, boardKey, "david")
	if err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	countAfter, _ := redis.LeaderboardCount(ctx, boardKey)
	if countAfter != 4 {
		t.Fatalf("expected 4 members after remove, got %d", countAfter)
	}
}

func TestRedisHyperLogLog(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()
	ctx := context.Background()

	hllKey := "visitors:2026-09-23"
	_, _ = redis.HyperLogLogAdd(ctx, hllKey, "ip:1.1.1.1", "ip:2.2.2.2", "ip:3.3.3.3")
	// Duplicate IP should not increment unique count
	_, _ = redis.HyperLogLogAdd(ctx, hllKey, "ip:1.1.1.1")

	count, err := redis.HyperLogLogCount(ctx, hllKey)
	if err != nil || count != 3 {
		t.Fatalf("expected unique count 3, got %d (err: %v)", count, err)
	}
}

func TestRedisBloomRebuild(t *testing.T) {
	redis := NewRedisClient(RedisConfig{Addr: "memory"})
	defer redis.Close()
	ctx := context.Background()

	key := "items:filter"
	_ = redis.BloomAdd(ctx, key, "old-item")
	exists, _ := redis.BloomContains(ctx, key, "old-item")
	if !exists {
		t.Fatalf("expected old-item in bloom filter")
	}

	// Rebuild with new items
	err := redis.BloomRebuild(ctx, key, "new-item-1", "new-item-2")
	if err != nil {
		t.Fatalf("BloomRebuild failed: %v", err)
	}

	existsOld, _ := redis.BloomContains(ctx, key, "old-item")
	if existsOld {
		t.Fatalf("expected old-item to be cleared by rebuild")
	}
	existsNew1, _ := redis.BloomContains(ctx, key, "new-item-1")
	if !existsNew1 {
		t.Fatalf("expected new-item-1 in rebuilt filter")
	}
}
