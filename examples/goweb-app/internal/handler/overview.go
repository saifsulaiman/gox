package handler

import (
	"net/http"

	"github.com/goxlang/gox/pkg/goweb"
)

// RootOverview serves the system metadata and endpoint discovery index.
func RootOverview(c *goweb.Context) error {
	return c.JSON(http.StatusOK, goweb.H{
		"app":       "Nexus Commerce Microservice",
		"framework": "GoxWeb: A High-Performance Web Framework for Go / GOX",
		"features": []string{
			"Master/Slave Read-Write Splitting",
			"PgBouncer Transaction Pooling Compatibility",
			"Idempotency Middleware (At-Most-Once POST)",
			"Bloom Filter Cache Penetration Defense",
			"Redis Updatable Cuckoo Filter (CuckooAdd/Contains/Delete)",
			"Redis Heavy Hitters Top-K Estimation (TopKAdd/List)",
			"Redis Real-Time Leaderboards (ZSET Rank/AroundMe)",
			"Redis HyperLogLog Unique Visitor Tracking (PFADD/PFCOUNT)",
			"Redis Distributed Locks",
			"Singleflight Request Coalescing",
			"RabbitMQ Publisher Confirms & DLQ",
			"JWT Authentication & Role-Based Access Control",
			"Async Detached Context Workers",
			"Strict RowsAffected == 1 Enforcement (ExecOne)",
		},
		"services": goweb.H{
			"sqlite":        "Catalog & Master/Slave Replicas",
			"postgres":      "Orders & Ledger (PgBouncer Optimized)",
			"redis":         "Inventory, Cuckoo, TopK, Leaderboards & Locks",
			"rabbitmq":      "Reliable Order Queue with DLQ",
			"elasticsearch": "Full-Text Search Engine",
		},
		"endpoints": []string{
			"POST   /api/auth/token             (Issue test JWT tokens)",
			"GET    /api/products               (Singleflight & Redis cached)",
			"GET    /api/products/:id           (Bloom Filter protected)",
			"POST   /api/products               (JWT Admin Protected, Cuckoo seeded)",
			"DELETE /api/products/:id           (JWT Admin, Strict ExecOne, Cuckoo removal)",
			"GET    /api/products/search?q=...  (Elasticsearch & Top-K recorded)",
			"GET    /api/trending               (Top-K Heavy Hitter Search Queries)",
			"POST   /api/orders                 (Idempotent, Distributed Locked, TxRetry, Leaderboard)",
			"GET    /api/orders                 (Read-Replica Balanced)",
			"GET    /api/leaderboard            (Top Selling Products)",
			"GET    /api/leaderboard/around/:id (Rank Window Around Product)",
			"GET    /api/stats/visitors         (HyperLogLog Unique Visitors)",
			"GET    /health                     (Cluster & Service Probes)",
			"GET    /metrics                    (Runtime & GOX Unique Arena Stats)",
		},
	})
}
