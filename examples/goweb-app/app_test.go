package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"example.com/gox-web-app/internal/bootstrap"
	"example.com/gox-web-app/internal/config"
	"example.com/gox-web-app/internal/domain"
	"example.com/gox-web-app/internal/handler"
	"example.com/gox-web-app/internal/worker"
	"github.com/goxlang/gox/pkg/goweb"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestApp(t *testing.T) (*goweb.Engine, *config.AppConfig) {
	dbFile := t.Name() + "_test.db"
	t.Cleanup(func() {
		_ = os.Remove(dbFile)
		_ = os.Remove(dbFile + "-journal")
		_ = os.Remove(dbFile + "-wal")
		_ = os.Remove(dbFile + "-shm")
	})

	cfg := &config.AppConfig{
		Port:      8099,
		JWTSecret: "super-secret-test-key",
		SQLite:    "file:" + dbFile + "?cache=shared&mode=rwc",
		Postgres:  "",
		Redis:     "memory",
		RabbitMQ:  "memory",
		Elastic:   "",
	}

	app := goweb.New(goweb.Config{
		Port:     cfg.Port,
		SQLite:   cfg.SQLite,
		Postgres: cfg.Postgres,
		Redis:    cfg.Redis,
		RabbitMQ: cfg.RabbitMQ,
		Elastic:  cfg.Elastic,
	})

	handler.RegisterMiddleware(app)
	bootstrap.InitDatabases(app)
	worker.RegisterWorkers(app)
	handler.RegisterRoutes(app, cfg)

	return app, cfg
}

func TestAppRootAndHealth(t *testing.T) {
	app, _ := setupTestApp(t)

	// 1. Root Overview
	wRoot := httptest.NewRecorder()
	app.ServeHTTP(wRoot, httptest.NewRequest(http.MethodGet, "/", nil))
	if wRoot.Code != http.StatusOK {
		t.Fatalf("expected 200 for /, got %d", wRoot.Code)
	}
	var rootData map[string]any
	_ = json.Unmarshal(wRoot.Body.Bytes(), &rootData)
	if rootData["app"] != "Nexus Commerce Microservice" {
		t.Errorf("expected app name Nexus Commerce Microservice, got %v", rootData["app"])
	}

	// 2. Health check
	wHealth := httptest.NewRecorder()
	app.ServeHTTP(wHealth, httptest.NewRequest(http.MethodGet, "/health", nil))
	if wHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 for /health, got %d", wHealth.Code)
	}

	// 3. Metrics
	wMetrics := httptest.NewRecorder()
	app.ServeHTTP(wMetrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if wMetrics.Code != http.StatusOK {
		t.Fatalf("expected 200 for /metrics, got %d", wMetrics.Code)
	}
}

func TestAppProductCatalogAndBloomDefense(t *testing.T) {
	app, _ := setupTestApp(t)

	// 1. List products (initially seeded with 3 items)
	wList := httptest.NewRecorder()
	app.ServeHTTP(wList, httptest.NewRequest(http.MethodGet, "/api/products", nil))
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/products, got %d", wList.Code)
	}
	var products []domain.Product
	if err := json.Unmarshal(wList.Body.Bytes(), &products); err != nil {
		t.Fatalf("failed to unmarshal products: %v (body: %s)", err, wList.Body.String())
	}
	if len(products) < 3 {
		t.Fatalf("expected at least 3 seeded products, got %d", len(products))
	}

	// 2. Get existing product #1
	wGet1 := httptest.NewRecorder()
	app.ServeHTTP(wGet1, httptest.NewRequest(http.MethodGet, "/api/products/1", nil))
	if wGet1.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/products/1, got %d", wGet1.Code)
	}

	// 3. Get non-existing product #99999 (blocked by Bloom/Cuckoo without DB access)
	wGetNone := httptest.NewRecorder()
	app.ServeHTTP(wGetNone, httptest.NewRequest(http.MethodGet, "/api/products/99999", nil))
	if wGetNone.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for /api/products/99999, got %d", wGetNone.Code)
	}
}

func TestAppAuthAndAdminMutations(t *testing.T) {
	app, _ := setupTestApp(t)

	// 1. Request admin token
	tokenBody := bytes.NewBufferString(`{"role":"admin"}`)
	wToken := httptest.NewRecorder()
	reqToken := httptest.NewRequest(http.MethodPost, "/api/auth/token", tokenBody)
	reqToken.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(wToken, reqToken)
	if wToken.Code != http.StatusOK {
		t.Fatalf("expected 200 for token issue, got %d", wToken.Code)
	}
	var tokenRes struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(wToken.Body.Bytes(), &tokenRes)
	if tokenRes.Token == "" {
		t.Fatalf("expected non-empty JWT token")
	}

	// 2. Create product with Admin token
	newProd := `{"sku":"SKU-TEST-99","name":"Mechanical Keyboard","price":129.99,"stock":50}`
	wCreate := httptest.NewRecorder()
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/products", bytes.NewBufferString(newProd))
	reqCreate.Header.Set("Content-Type", "application/json")
	reqCreate.Header.Set("Authorization", "Bearer "+tokenRes.Token)
	app.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for product creation, got %d (body: %s)", wCreate.Code, wCreate.Body.String())
	}

	// 3. Attempt creation without token (should be 401 Unauthorized)
	wNoAuth := httptest.NewRecorder()
	reqNoAuth := httptest.NewRequest(http.MethodPost, "/api/products", bytes.NewBufferString(newProd))
	reqNoAuth.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(wNoAuth, reqNoAuth)
	if wNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", wNoAuth.Code)
	}
}

func TestAppOrderCreationWithIdempotency(t *testing.T) {
	app, _ := setupTestApp(t)

	orderJSON := `{"product_id":1,"customer_id":"cust_alice","quantity":2,"unit_price":29.99}`
	idempotencyKey := "idem-test-uuid-10101"

	// 1. Initial Order Placement
	wOrder1 := httptest.NewRecorder()
	reqOrder1 := httptest.NewRequest(http.MethodPost, "/api/orders", bytes.NewBufferString(orderJSON))
	reqOrder1.Header.Set("Content-Type", "application/json")
	reqOrder1.Header.Set("Idempotency-Key", idempotencyKey)
	app.ServeHTTP(wOrder1, reqOrder1)
	if wOrder1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for order placement, got %d (body: %s)", wOrder1.Code, wOrder1.Body.String())
	}

	// 2. Idempotent Replay (same key, must return same response or cached replay)
	wOrder2 := httptest.NewRecorder()
	reqOrder2 := httptest.NewRequest(http.MethodPost, "/api/orders", bytes.NewBufferString(orderJSON))
	reqOrder2.Header.Set("Content-Type", "application/json")
	reqOrder2.Header.Set("Idempotency-Key", idempotencyKey)
	app.ServeHTTP(wOrder2, reqOrder2)
	if wOrder2.Code != http.StatusCreated && wOrder2.Code != http.StatusOK {
		t.Fatalf("expected 200 or 201 for idempotent replay, got %d", wOrder2.Code)
	}
}

func TestAppAnalyticsEndpoints(t *testing.T) {
	app, _ := setupTestApp(t)

	// 1. Search products (records into Top-K)
	wSearch := httptest.NewRecorder()
	app.ServeHTTP(wSearch, httptest.NewRequest(http.MethodGet, "/api/products/search?q=keyboard", nil))
	if wSearch.Code != http.StatusOK {
		t.Fatalf("expected 200 for search, got %d", wSearch.Code)
	}

	// 2. Get trending queries (Top-K)
	wTrending := httptest.NewRecorder()
	app.ServeHTTP(wTrending, httptest.NewRequest(http.MethodGet, "/api/trending", nil))
	if wTrending.Code != http.StatusOK {
		t.Fatalf("expected 200 for trending, got %d", wTrending.Code)
	}

	// 3. Get leaderboard
	wLeader := httptest.NewRecorder()
	app.ServeHTTP(wLeader, httptest.NewRequest(http.MethodGet, "/api/leaderboard", nil))
	if wLeader.Code != http.StatusOK {
		t.Fatalf("expected 200 for leaderboard, got %d", wLeader.Code)
	}

	// 4. Unique visitors (HyperLogLog)
	wVis := httptest.NewRecorder()
	app.ServeHTTP(wVis, httptest.NewRequest(http.MethodGet, "/api/stats/visitors", nil))
	if wVis.Code != http.StatusOK {
		t.Fatalf("expected 200 for visitor stats, got %d", wVis.Code)
	}
}
