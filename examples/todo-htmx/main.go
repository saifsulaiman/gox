package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"

	"example.com/gox-todo-htmx/handlers"
	"example.com/gox-todo-htmx/models"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	dbPath := flag.String("db", "todos.db", "SQLite database file path")
	inMemory := flag.Bool("in-memory", false, "Use in-memory SQLite database")
	flag.Parse()

	activeDBPath := *dbPath
	if *inMemory {
		activeDBPath = "file::memory:?cache=shared"
	}

	log.Printf("[GOX-Todo] Initializing SQLite database at %s...", activeDBPath)
	store, err := models.NewTodoStore(activeDBPath)
	if err != nil {
		log.Fatalf("failed to initialize store: %v", err)
	}

	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		log.Fatalf("failed to parse embedded templates: %v", err)
	}

	mux := http.NewServeMux()

	// Static file handler from embedded files
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("failed to open embedded static sub-filesystem: %v", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Register todo routes
	todoHandler := handlers.NewTodoHandler(store, tmpl)
	todoHandler.RegisterRoutes(mux)

	serverHandler := goxrt.WithRequestArena(mux)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[GOX-Todo] Server started successfully on http://localhost%s (Request Arena Enabled)", addr)
	if err := http.ListenAndServe(addr, serverHandler); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failure: %v", err)
	}
}
