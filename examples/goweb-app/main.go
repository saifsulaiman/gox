package main

import (
	"log/slog"
	"os"

	"example.com/gox-web-app/internal/bootstrap"
	"example.com/gox-web-app/internal/config"
	"example.com/gox-web-app/internal/handler"
	"example.com/gox-web-app/internal/worker"
	"github.com/goxlang/gox/pkg/goweb"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	// 1. Load typed configuration from flags and environment
	cfg := config.LoadConfig()

	// 2. Initialize GoxWeb Application with Master/Slave Read-Write Splitting, PgBouncer, Redis, RabbitMQ & Elasticsearch
	app := goweb.New(goweb.Config{
		Port:             cfg.Port,
		SQLite:           cfg.SQLite,
		SQLiteReplicas:   cfg.SQLiteReplicas,
		Postgres:         cfg.Postgres,
		PostgresReplicas: cfg.PostgresReplicas,
		PgBouncer:        cfg.PgBouncer,
		Redis:            cfg.Redis,
		RabbitMQ:         cfg.RabbitMQ,
		Elastic:          cfg.Elastic,
	})

	// 3. Attach production middleware stack
	handler.RegisterMiddleware(app)

	// 4. Bootstrap database schemas & seed initial catalog and filter state
	bootstrap.InitDatabases(app)

	// 5. Register background queue workers
	worker.RegisterWorkers(app)

	// 6. Register all REST API endpoints
	handler.RegisterRoutes(app, cfg)

	// 7. Run server with automated Graceful Shutdown & Zero-GC Request Arena
	if err := app.Run(); err != nil {
		slog.Error("Server startup failed", slog.Any("error", err))
		os.Exit(1)
	}
}
