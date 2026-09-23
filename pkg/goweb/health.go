package goweb

import (
	"context"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goxlang/gox/pkg/goxrt"
)

// HealthReport holds the aggregated status of the application and its dependencies.
type HealthReport struct {
	Status    string            `json:"status"`
	Timestamp string            `json:"timestamp"`
	Uptime    string            `json:"uptime"`
	Services  map[string]string `json:"services"`
	Metrics   map[string]any    `json:"metrics,omitempty"`
}

// RegisterHealthEndpoints attaches /health, /ready, and /metrics routes to the engine.
func (e *Engine) registerHealthEndpoints() {
	startTime := time.Now()
	var totalRequests atomic.Uint64

	// Global request counter middleware
	e.Use(func(c *Context) error {
		totalRequests.Add(1)
		return c.Next()
	})

	healthHandler := func(c *Context) error {
		ctx, cancel := context.WithTimeout(c.Context(), 2*time.Second)
		defer cancel()

		services := make(map[string]string)
		var mu sync.Mutex
		var wg sync.WaitGroup

		checkService := func(name string, checkFn func(context.Context) error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status := "UP"
				if err := checkFn(ctx); err != nil {
					status = "DOWN: " + err.Error()
				}
				mu.Lock()
				services[name] = status
				mu.Unlock()
			}()
		}

		if e.sqlite != nil {
			checkService("sqlite", e.sqlite.Ping)
		}
		if e.postgres != nil {
			checkService("postgres", e.postgres.Ping)
		}
		if e.redis != nil {
			checkService("redis", e.redis.Ping)
		}
		if e.queue != nil {
			checkService("rabbitmq", e.queue.Ping)
		}
		if e.search != nil {
			checkService("elasticsearch", e.search.Health)
		}

		wg.Wait()

		overall := "UP"
		for _, s := range services {
			if len(s) >= 4 && s[:4] == "DOWN" {
				overall = "DEGRADED"
				break
			}
		}

		statusCode := http.StatusOK
		if overall == "DEGRADED" {
			statusCode = http.StatusServiceUnavailable
		}

		return c.JSON(statusCode, HealthReport{
			Status:    overall,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Uptime:    time.Since(startTime).Truncate(time.Second).String(),
			Services:  services,
		})
	}

	e.GET("/health", healthHandler)
	e.GET("/ready", healthHandler)

	e.GET("/metrics", func(c *Context) error {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		uniqueStats := goxrt.GetUniqueStats()

		metrics := H{
			"uptime":            time.Since(startTime).Truncate(time.Second).String(),
			"total_requests":    totalRequests.Load(),
			"goroutines":        runtime.NumGoroutine(),
			"heap_alloc_bytes":  m.Alloc,
			"heap_sys_bytes":    m.Sys,
			"gc_cycles":         m.NumGC,
			"gox_unique_allocs": uniqueStats.AllocCount,
			"gox_unique_freed":  uniqueStats.FreeCount,
			"gox_active_unique": uniqueStats.ActiveObjects,
		}

		return c.JSON(http.StatusOK, metrics)
	})
}
