package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"

	"example.com/gox-todo-sqlc/db"
	"example.com/gox-todo-sqlc/handlers"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	port := flag.Int("port", 8082, "HTTP server listening port")
	dbPath := flag.String("db", "file:todo_sqlc.db?cache=shared&mode=rwc", "SQLite database connection string")
	flag.Parse()

	sqlDB, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("failed to open sqlite database: %v", err)
	}
	defer sqlDB.Close()

	// SQLite connection tuning
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	queries, err := db.NewQueries(sqlDB)
	if err != nil {
		log.Fatalf("failed to initialize queries: %v", err)
	}
	defer queries.Close()

	todoHandler := handlers.NewTodoHandler(queries)

	// Wrap handler with zero-alloc GOX Request Arena
	serverHandler := goxrt.AdaptArenaHandler(todoHandler)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[GOX-Todo-SQLC] Server started on http://localhost%s (Zero-GC Request Arena Enabled)", addr)
	if err := http.ListenAndServe(addr, serverHandler); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
