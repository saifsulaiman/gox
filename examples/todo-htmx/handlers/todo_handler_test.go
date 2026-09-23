package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"example.com/gox-todo-htmx/models"
)

func setupTestHandler(t *testing.T) (*TodoHandler, *http.ServeMux) {
	store, err := models.NewTodoStore("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	tmpl, err := template.ParseGlob("../templates/*.html")
	if err != nil {
		// Fallback for tests running from different directories
		tmpl, err = template.ParseGlob("templates/*.html")
		if err != nil {
			t.Fatalf("failed to parse templates: %v", err)
		}
	}

	handler := NewTodoHandler(store, tmpl)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return handler, mux
}

func TestIndexAndCRUD(t *testing.T) {
	_, mux := setupTestHandler(t)

	// 1. GET /
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for GET /, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Task Master") {
		t.Errorf("expected response to contain 'Task Master'")
	}

	// 2. POST /todos (Create)
	form := url.Values{}
	form.Set("title", "Integrate GORM and HTMX")
	form.Set("priority", "high")
	req = httptest.NewRequest("POST", "/todos", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for POST /todos, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Integrate GORM and HTMX") {
		t.Errorf("expected response to contain created todo")
	}

	// 3. PUT /todos/1/toggle
	req = httptest.NewRequest("PUT", "/todos/1/toggle", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for toggle, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "completed") {
		t.Errorf("expected todo item to have completed class")
	}

	// 4. GET /todos/1/edit
	req = httptest.NewRequest("GET", "/todos/1/edit", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for edit form, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "edit-form") {
		t.Errorf("expected edit form")
	}

	// 5. PUT /todos/1 (Update)
	form = url.Values{}
	form.Set("title", "Integrate GORM, SQLite and HTMX")
	form.Set("priority", "medium")
	req = httptest.NewRequest("PUT", "/todos/1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for update, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Integrate GORM, SQLite and HTMX") {
		t.Errorf("expected updated title")
	}

	// 6. DELETE /todos/1
	req = httptest.NewRequest("DELETE", "/todos/1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete, got %d", rec.Code)
	}

	// 7. GET /healthz
	req = httptest.NewRequest("GET", "/healthz", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for healthz, got %d", rec.Code)
	}
}
