package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"example.com/gox-todo-sqlc/db"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestMux(t *testing.T) (*http.ServeMux, *db.Queries) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	queries, err := db.NewQueries(sqlDB)
	if err != nil {
		t.Fatalf("failed to init queries: %v", err)
	}

	handler := NewTodoHandler(queries)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux, queries
}

func TestHandlerWithArena(t *testing.T) {
	mux, queries := setupTestMux(t)
	defer queries.Close()

	handler := goxrt.WithRequestArena(mux)

	// 1. Create a todo
	form := url.Values{}
	form.Set("title", "Learn GOX Arenas")
	form.Set("priority", "high")

	req := httptest.NewRequest("POST", "/todos", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	// 2. List todos
	req = httptest.NewRequest("GET", "/todos?filter=all", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Learn GOX Arenas") {
		t.Fatalf("expected body to contain task title, got %s", body)
	}

	// 3. Toggle todo 1
	req = httptest.NewRequest("PUT", "/todos/1/toggle", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on toggle, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"completed":true`) {
		t.Fatalf("expected completed=true, got %s", rec.Body.String())
	}

	// 4. Stats
	req = httptest.NewRequest("GET", "/stats", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on stats, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("expected total 1, got %s", rec.Body.String())
	}
}
