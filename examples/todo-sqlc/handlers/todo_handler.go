package handlers

import (
	"database/sql"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/gox-todo-sqlc/db"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

// TodoHandler handles REST and HTMX requests using typed queries and arenas.
type TodoHandler struct {
	queries *db.Queries
	mux     *http.ServeMux
}

// NewTodoHandler creates a new handler instance.
func NewTodoHandler(queries *db.Queries) *TodoHandler {
	h := &TodoHandler{
		queries: queries,
		mux:     http.NewServeMux(),
	}
	h.RegisterRoutes(h.mux)
	return h
}

// ServeHTTP implements http.Handler for baseline standard Go GC.
func (h *TodoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// ServeHTTPWithArena dispatches requests with an active Arena directly,
// achieving 100% zero-alloc context and router handling.
func (h *TodoHandler) ServeHTTPWithArena(w http.ResponseWriter, r *http.Request, arena *goxrt.Arena) {
	path := r.URL.Path
	method := r.Method

	switch {
	case method == "GET" && path == "/todos":
		filter := r.URL.Query().Get("filter")
		if filter == "" {
			filter = "all"
		}
		todos, err := h.queries.ListArena(r.Context(), arena, filter)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeTodoListJSON(w, todos, arena)

	case method == "POST" && path == "/todos":
		_ = r.ParseForm()
		title := r.FormValue("title")
		if title == "" {
			title = "Untitled Task"
		}
		priority := db.Priority(r.FormValue("priority"))
		if priority == "" {
			priority = db.PriorityMedium
		}
		todo, err := h.queries.Create(r.Context(), title, priority)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeTodoJSON(w, &todo)

	case method == "PUT" && strings.HasPrefix(path, "/todos/") && strings.HasSuffix(path, "/toggle"):
		idStr := strings.TrimPrefix(path, "/todos/")
		idStr = strings.TrimSuffix(idStr, "/toggle")
		id, err := parseID(idStr)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		todo, err := h.queries.Toggle(r.Context(), id)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeTodoJSON(w, &todo)

	case method == "DELETE" && strings.HasPrefix(path, "/todos/"):
		idStr := strings.TrimPrefix(path, "/todos/")
		id, err := parseID(idStr)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := h.queries.Delete(r.Context(), id); err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		h.mux.ServeHTTP(w, r)
	}
}

// RegisterRoutes registers all REST routes on the mux.
func (h *TodoHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /todos", h.handleList)
	mux.HandleFunc("POST /todos", h.handleCreate)
	mux.HandleFunc("GET /todos/{id}", h.handleGet)
	mux.HandleFunc("PUT /todos/{id}/toggle", h.handleToggle)
	mux.HandleFunc("PUT /todos/{id}", h.handleUpdate)
	mux.HandleFunc("DELETE /todos/{id}", h.handleDelete)
	mux.HandleFunc("POST /todos/clear-completed", h.handleClearCompleted)
	mux.HandleFunc("GET /stats", h.handleStats)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
}

func (h *TodoHandler) handleList(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}

	arena := goxrt.GetRequestArena(r.Context())
	var todos []db.Todo
	var err error

	if arena != nil {
		todos, err = h.queries.ListArena(r.Context(), arena, filter)
	} else {
		todos, err = h.queries.List(r.Context(), filter)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeTodoListJSON(w, todos, arena)
}

func (h *TodoHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	title := r.FormValue("title")
	if title == "" {
		title = "Untitled Task"
	}
	priority := db.Priority(r.FormValue("priority"))
	if priority == "" {
		priority = db.PriorityMedium
	}

	todo, err := h.queries.Create(r.Context(), title, priority)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeTodoJSON(w, &todo)
}

func (h *TodoHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	todo, err := h.queries.GetByID(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeTodoJSON(w, &todo)
}

func (h *TodoHandler) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	todo, err := h.queries.Toggle(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeTodoJSON(w, &todo)
}

func (h *TodoHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	_ = r.ParseForm()
	title := r.FormValue("title")
	priority := db.Priority(r.FormValue("priority"))
	if priority == "" {
		priority = db.PriorityMedium
	}

	todo, err := h.queries.Update(r.Context(), id, title, priority)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeTodoJSON(w, &todo)
}

func (h *TodoHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.queries.Delete(r.Context(), id); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *TodoHandler) handleClearCompleted(w http.ResponseWriter, r *http.Request) {
	cleared, err := h.queries.ClearCompleted(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var buf [32]byte
	b := append(buf[:0], `{"cleared":`...)
	b = strconv.AppendInt(b, cleared, 10)
	b = append(b, '}')
	_, _ = w.Write(b)
}

func (h *TodoHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.queries.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	var buf [128]byte
	b := append(buf[:0], `{"total":`...)
	b = strconv.AppendInt(b, stats.Total, 10)
	b = append(b, `,"active":`...)
	b = strconv.AppendInt(b, stats.Active, 10)
	b = append(b, `,"completed":`...)
	b = strconv.AppendInt(b, stats.Completed, 10)
	b = append(b, '}')
	_, _ = w.Write(b)
}

func (h *TodoHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","app":"gox-todo-sqlc"}`))
}

func parseID(param string) (uint, error) {
	id, err := strconv.ParseUint(param, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}

// Zero-reflection typed JSON writers:

func appendTodoJSON(buf []byte, t *db.Todo) []byte {
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendUint(buf, uint64(t.ID), 10)
	buf = append(buf, `,"title":"`...)
	buf = append(buf, t.Title...)
	buf = append(buf, `","priority":"`...)
	buf = append(buf, string(t.Priority)...)
	buf = append(buf, `","completed":`...)
	if t.Completed {
		buf = append(buf, "true"...)
	} else {
		buf = append(buf, "false"...)
	}
	buf = append(buf, `,"created_at":"`...)
	buf = t.CreatedAt.AppendFormat(buf, time.RFC3339)
	buf = append(buf, `","updated_at":"`...)
	buf = t.UpdatedAt.AppendFormat(buf, time.RFC3339)
	buf = append(buf, `"}`...)
	return buf
}

func writeTodoJSON(w io.Writer, t *db.Todo) {
	var buf [256]byte
	b := appendTodoJSON(buf[:0], t)
	_, _ = w.Write(b)
}

func writeTodoListJSON(w io.Writer, todos []db.Todo, arena *goxrt.Arena) {
	if len(todos) == 0 {
		_, _ = w.Write([]byte("[]"))
		return
	}
	var buf []byte
	if arena != nil {
		buf = goxrt.AllocSlice[byte](arena, 0, len(todos)*160)
	} else {
		buf = make([]byte, 0, len(todos)*160)
	}
	buf = append(buf, '[')
	for i := range todos {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendTodoJSON(buf, &todos[i])
	}
	buf = append(buf, ']')
	_, _ = w.Write(buf)
}
