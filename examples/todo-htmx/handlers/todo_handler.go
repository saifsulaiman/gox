package handlers

import (
	"html/template"
	"net/http"
	"strconv"

	"example.com/gox-todo-htmx/models"
)

type ListViewData struct {
	Todos          []models.Todo
	ActiveCount    int64
	CompletedCount int64
	TotalCount     int64
	CurrentFilter  string
}

type TodoHandler struct {
	store *models.TodoStore
	tmpl  *template.Template
}

func NewTodoHandler(store *models.TodoStore, tmpl *template.Template) *TodoHandler {
	return &TodoHandler{
		store: store,
		tmpl:  tmpl,
	}
}

// RegisterRoutes registers all REST and HTMX routes onto the given mux.
func (h *TodoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.handleIndex)
	mux.HandleFunc("GET /todos", h.handleListTodos)
	mux.HandleFunc("POST /todos", h.handleCreateTodo)
	mux.HandleFunc("GET /todos/{id}", h.handleGetTodo)
	mux.HandleFunc("GET /todos/{id}/edit", h.handleEditTodoForm)
	mux.HandleFunc("PUT /todos/{id}", h.handleUpdateTodo)
	mux.HandleFunc("PUT /todos/{id}/toggle", h.handleToggleTodo)
	mux.HandleFunc("DELETE /todos/{id}", h.handleDeleteTodo)
	mux.HandleFunc("POST /todos/clear-completed", h.handleClearCompleted)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

func (h *TodoHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := h.getListViewData("all")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *TodoHandler) handleListTodos(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}

	data, err := h.getListViewData(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "todo_list.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *TodoHandler) handleCreateTodo(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	priority := models.Priority(r.FormValue("priority"))

	_, err := h.store.Create(title, priority)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Return updated list
	data, err := h.getListViewData("all")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_list.html", data)
}

func (h *TodoHandler) handleGetTodo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	todo, err := h.store.GetByID(id)
	if err != nil {
		http.Error(w, "todo not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_item.html", todo)
}

func (h *TodoHandler) handleEditTodoForm(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	todo, err := h.store.GetByID(id)
	if err != nil {
		http.Error(w, "todo not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_edit.html", todo)
}

func (h *TodoHandler) handleUpdateTodo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	priority := models.Priority(r.FormValue("priority"))

	todo, err := h.store.Update(id, title, priority)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_item.html", todo)
}

func (h *TodoHandler) handleToggleTodo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	todo, err := h.store.Toggle(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_item.html", todo)
}

func (h *TodoHandler) handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.store.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Returning empty response tells HTMX to remove the target element
	w.WriteHeader(http.StatusOK)
}

func (h *TodoHandler) handleClearCompleted(w http.ResponseWriter, r *http.Request) {
	_, err := h.store.ClearCompleted()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data, err := h.getListViewData("all")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.tmpl.ExecuteTemplate(w, "todo_list.html", data)
}

func (h *TodoHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","app":"gox-todo-htmx"}`))
}

func (h *TodoHandler) getListViewData(filter string) (*ListViewData, error) {
	todos, err := h.store.List(filter)
	if err != nil {
		return nil, err
	}

	total, active, completed, err := h.store.Stats()
	if err != nil {
		return nil, err
	}

	return &ListViewData{
		Todos:          todos,
		ActiveCount:    active,
		CompletedCount: completed,
		TotalCount:     total,
		CurrentFilter:  filter,
	}, nil
}

func parseID(param string) (uint, error) {
	id, err := strconv.ParseUint(param, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
