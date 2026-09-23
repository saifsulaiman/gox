package bench

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"example.com/gox-todo-htmx/handlers"
	"example.com/gox-todo-htmx/models"
)

func setupBenchMux(b *testing.B) (*http.ServeMux, *models.TodoStore) {
	store, err := models.NewTodoStore("file::memory:?cache=shared")
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}

	tmpl, err := template.ParseGlob("../templates/*.html")
	if err != nil {
		tmpl, err = template.ParseGlob("templates/*.html")
		if err != nil {
			b.Fatalf("failed to parse templates: %v", err)
		}
	}

	handler := handlers.NewTodoHandler(store, tmpl)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return mux, store
}

func BenchmarkTodoCreate(b *testing.B) {
	mux, _ := setupBenchMux(b)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		form := url.Values{}
		form.Set("title", "Benchmark Task "+strconv.Itoa(i))
		form.Set("priority", "medium")

		req := httptest.NewRequest("POST", "/todos", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
		i++
	}
}

func BenchmarkTodoList(b *testing.B) {
	mux, store := setupBenchMux(b)

	// Pre-populate 50 items
	for i := 0; i < 50; i++ {
		_, _ = store.Create("Prepopulated item "+strconv.Itoa(i), models.PriorityMedium)
	}

	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest("GET", "/todos?filter=active", nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("expected 200, got %d", rec.Code)
		}
	}
}

func BenchmarkTodoMixedCRUD(b *testing.B) {
	mux, _ := setupBenchMux(b)

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// 1. Create
		form := url.Values{}
		form.Set("title", "CRUD Task "+strconv.Itoa(i))
		form.Set("priority", "high")
		req := httptest.NewRequest("POST", "/todos", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// 2. List
		req = httptest.NewRequest("GET", "/todos?filter=all", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// 3. Toggle
		req = httptest.NewRequest("PUT", "/todos/1/toggle", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		i++
	}
}
