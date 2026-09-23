package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"example.com/gox-fast-api/handlers"
	"example.com/gox-fast-api/models"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

func main() {
	port := flag.Int("port", 8088, "HTTP port to listen on")
	flag.Parse()

	store := models.NewStore()
	store.Seed(1000)

	handler := handlers.NewItemHandler(store)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.HandleCreate(w, r)
		} else {
			handler.HandleList(w, r)
		}
	})
	mux.HandleFunc("/api/items/", handler.HandleGet)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})

	// Wrap entire router in Request Arena middleware:
	// Every incoming request gets an O(1) thread-local bump arena that
	// is bulk-reset when the HTTP response completes.
	serverHandler := goxrt.WithRequestArena(mux)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[GOX Fast-API] Starting server on %s (Request Arena Enabled, 0-GC Mode)", addr)
	if err := http.ListenAndServe(addr, serverHandler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
