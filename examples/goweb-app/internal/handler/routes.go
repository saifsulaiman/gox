package handler

import (
	"time"

	"example.com/gox-web-app/internal/config"
	"github.com/goxlang/gox/pkg/goweb"
)

// RegisterRoutes binds all REST endpoints and route groups onto the GoxWeb Engine.
func RegisterRoutes(app *goweb.Engine, cfg *config.AppConfig) {
	// Root Welcome & System Overview
	app.GET("/", RootOverview)

	// Auth Token Generation
	app.POST("/api/auth/token", IssueTokenHandler(cfg.JWTSecret))

	// API Route Group
	api := app.Group("/api")
	{
		// Product catalog
		api.GET("/products", ListProducts)
		api.GET("/products/:id", GetProduct)
		api.POST("/products", goweb.JWTAuth(cfg.JWTSecret), goweb.RequireRoles("admin"), CreateProduct)
		api.DELETE("/products/:id", goweb.JWTAuth(cfg.JWTSecret), goweb.RequireRoles("admin"), DeleteProduct)
		api.GET("/products/search", SearchProducts)

		// Order transactions
		api.POST("/orders", goweb.Idempotency(goweb.IdempotencyOptions{
			HeaderKey: "Idempotency-Key",
			LockTTL:   10 * time.Second,
		}), CreateOrder)
		api.GET("/orders", ListOrders)

		// Analytics, Leaderboard & Trending
		api.GET("/leaderboard", GetLeaderboard)
		api.GET("/leaderboard/around/:id", GetLeaderboardAround)
		api.GET("/trending", GetTrending)
		api.GET("/stats/visitors", GetVisitorStats)
	}
}
