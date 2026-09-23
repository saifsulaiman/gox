# GoxWeb: A High-Performance Web Framework for Go / GOX

We have implemented **`GoxWeb`** (`pkg/goweb`) and a production showcase application ([`examples/goweb-app/main.go`](./examples/goweb-app/main.go)), supporting **SQLite**, **PostgreSQL**, **Redis**, **RabbitMQ**, and **Elasticsearch** with minimal boilerplate.

The framework adheres strictly to **Modern 2026 Go Guidelines**, delivers enterprise reliability (Master/Slave read-write splitting, PgBouncer compatibility, safe retries, idempotent mutations, Cuckoo/Bloom filters, Top-K heavy hitters, real-time leaderboards, HyperLogLog visitor counting, distributed locks, DLQ, RBAC), and maximizes throughput under the **GOX zero-GC compiler**.

---

## 1. Modular Package Architecture (`examples/goweb-app`)

The monolithic `main.go` file (~540 lines) has been refactored into an idiomatic, production-grade 2026 Go directory layout:

```
examples/goweb-app/
├── main.go                                  # Minimal composition root & server runner (46 lines)
└── internal/
    ├── config/
    │   └── config.go                        # Typed AppConfig, flag parsing & env var extraction
    ├── domain/
    │   ├── product.go                       # Product catalog model
    │   └── order.go                         # Order transaction model & request DTO
    ├── bootstrap/
    │   └── database.go                      # DB schemas, seeds, Bloom, Cuckoo, TopK & ES indexing
    ├── worker/
    │   └── order_worker.go                  # RabbitMQ consumer for asynchronous order fulfillment
    └── handler/
        ├── middleware.go                    # Global middleware stack (Logger, Recover, CORS, HLL)
        ├── routes.go                        # Central router wiring on goweb.Engine
        ├── overview.go                      # Root GET / system overview
        ├── auth.go                          # POST /api/auth/token (JWT generator)
        ├── product.go                       # Product CRUD, singleflight, Bloom/Cuckoo protection
        ├── order.go                         # Order checkout, idempotency, distributed lock, Tx retry
        └── analytics.go                     # Leaderboard, Top-K trending searches, visitor HLL
```

### Composition Root ([`main.go`](./examples/goweb-app/main.go))
```go
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
	cfg := config.LoadConfig()
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

	handler.RegisterMiddleware(app)
	bootstrap.InitDatabases(app)
	worker.RegisterWorkers(app)
	handler.RegisterRoutes(app, cfg)

	if err := app.Run(); err != nil {
		slog.Error("Server startup failed", slog.Any("error", err))
		os.Exit(1)
	}
}
```

---

## 2. Advanced Redis Capabilities

Located in [`pkg/goweb/redis.go`](./pkg/goweb/redis.go):

| Redis Feature | Methods | Implementation Highlights |
| :--- | :--- | :--- |
| **Updatable Cuckoo Filter** | `CuckooAdd`, `CuckooContains`, `CuckooDelete` | Unlike Bloom filters where items cannot be removed without a full rebuild, Cuckoo filters support item deletion (`CuckooDelete`). Uses Redis RESP `CF.*` commands with memory fallback. |
| **Heavy Hitters / Top-K** | `TopKReserve`, `TopKAdd`, `TopKQuery`, `TopKCount`, `TopKList` | Tracks trending keywords and heavy hitters in streaming data. Uses Redis RESP `TOPK.*` commands with in-memory Min-Heap fallback. |
| **Real-Time Leaderboard** | `LeaderboardAdd`, `LeaderboardIncrBy`, `LeaderboardGetRank`, `LeaderboardGetTop`, `LeaderboardGetRange`, `LeaderboardAroundMe`, `LeaderboardRemove` | Tracks product sales rankings, user scores, and range queries. Uses Redis RESP `ZSET` commands (`ZADD`, `ZINCRBY`, `ZRANK`, `ZREVRANGE`) with sorted slice fallback. |
| **HyperLogLog (HLL)** | `HyperLogLogAdd`, `HyperLogLogCount` | Probabilistic cardinality estimation for unique visitors with constant ~12 KB memory footprint per key. Uses Redis RESP `PFADD` and `PFCOUNT`. |
| **Bloom Filter Rebuild** | `BloomReset`, `BloomRebuild` | Allows resetting and atomic bulk rebuilding of Bloom filters from primary database records. |

---

## 3. Master/Slave Read-Write Splitting & Database Safety

Located in [`pkg/goweb/database.go`](./pkg/goweb/database.go):

| Capability | Behavior & Implementation Details |
| :--- | :--- |
| **Strict Writes to Primary** | `Exec`, `ExecContext`, `ExecOne`, `Begin`, `BeginTx`, `WithTx`, `WithTxRetry` strictly execute against the **Primary/Master** database. |
| **Load-Balanced Reads** | `Query`, `QueryContext`, `QueryRow`, `QueryRowContext` automatically load-balance across **Read Replicas (Slaves)** using round-robin. |
| **Automatic Failover** | If a read replica encounters a connection or network error, it enters a 5-second cooldown and the query is transparently retried on the **Primary** database without failing the request. |
| **Read-Your-Own-Writes Consistency** | Calling `c.UsePrimaryDB()` or wrapping contexts with `WithPrimary(ctx)` forces subsequent reads to bypass read replicas and query the Primary database, eliminating replication lag staleness immediately after mutations. |
| **PgBouncer Pooling** | Setting `PgBouncer: true` optimizes connection pools for transaction pooling mode (short connection lifetimes, aggressive idle reaping, no conflicting prepared statements). |
| **Strict Single-Row Modification** | `ExecOne(ctx, query, args...)` enforces that `RowsAffected() == 1`. If 0 or >1 rows are affected, it returns `ErrNotOneRowAffected` to prevent accidental bulk modifications. |
| **Safe Transaction Retries** | `WithTxRetry(ctx, maxRetries, fn)` retries transient PostgreSQL serialization failures (`40001`), deadlocks, and connection dropouts with exponential backoff and jitter. |

---

## 4. Endpoints in Modular Showcase Microservice

```
POST   /api/auth/token             (Issue test JWT tokens with RBAC claims)
GET    /api/products               (Singleflight & Redis cached)
GET    /api/products/:id           (Bloom Filter cache penetration defense)
POST   /api/products               (JWT Admin Protected, Primary DB, Bloom + Cuckoo seeded)
DELETE /api/products/:id           (JWT Admin, Strict ExecOne, CuckooDelete & LeaderboardRemove)
GET    /api/products/search?q=...  (Elasticsearch & Heavy Hitter Top-K recorded)
GET    /api/trending               (Top-K Heavy Hitter Search Queries)
POST   /api/orders                 (Idempotent POST, Distributed Lock, Safe TxRetry, Sales Leaderboard)
GET    /api/orders                 (Read-Replica Balanced)
GET    /api/leaderboard            (Top Selling Products)
GET    /api/leaderboard/around/:id (Rank Window Around Product)
GET    /api/stats/visitors         (HyperLogLog Unique Visitors)
GET    /health                     (Cluster & Service Probes)
GET    /metrics                    (Runtime & GOX Unique Arena Stats)
```

---

## 5. Automated Verification

### Go Test Suite (`go test -count=1 -v -race ./pkg/goweb/...`)
```
=== RUN   TestRouterAndContext               --- PASS (0.00s)
=== RUN   TestRouteGroupAndMiddlewares       --- PASS (0.00s)
=== RUN   TestRecoverMiddleware              --- PASS (0.00s)
=== RUN   TestDatabaseIntegration            --- PASS (0.00s)
=== RUN   TestDatabaseMasterSlave            --- PASS (0.00s)
=== RUN   TestExecOneAndTxRetry              --- PASS (0.01s)
=== RUN   TestRedisIntegration               --- PASS (0.00s)
=== RUN   TestQueueIntegration               --- PASS (0.00s)
=== RUN   TestSearchIntegration              --- PASS (0.00s)
=== RUN   TestHealthEndpoints                --- PASS (0.00s)
=== RUN   TestIdempotencyMiddleware          --- PASS (0.00s)
=== RUN   TestAuthAndRBAC                    --- PASS (0.00s)
=== RUN   TestSingleflight                   --- PASS (0.05s)
=== RUN   TestAsyncWorker                    --- PASS (0.06s)
=== RUN   TestResilience                     --- PASS (0.02s)
=== RUN   TestBloomFilterAndRedisLock        --- PASS (0.00s)
=== RUN   TestRabbitMQDLQ                    --- PASS (0.10s)
=== RUN   TestRedisCuckooFilter              --- PASS (0.00s)
=== RUN   TestRedisTopK                      --- PASS (0.00s)
=== RUN   TestRedisLeaderboard               --- PASS (0.00s)
=== RUN   TestRedisHyperLogLog               --- PASS (0.00s)
=== RUN   TestRedisBloomRebuild              --- PASS (0.00s)
PASS (All 22 tests passed with zero data races)
```

### Static Analysis (`go vet ./...`)
- Both `pkg/goweb` and `examples/goweb-app` pass with **0 warnings and 0 errors**.

### GOX Compilation & Benchmark
- Compiled with custom compiler:
  ```bash
  ./bin/gox build -v -dir ./examples/goweb-app -o bin/nexus-api-gox .
  # Output: gox: 151 allocations, 23.2% proven GC reduction
  # Binary written to bin/nexus-api-gox
  ```
- Performance Benchmark (`go run ./examples/goweb-app/bench/runner/main.go`):
  - **72,215 requests/sec** (GOX Request Arena)
  - **109.5 µs average latency**
  - **10,000 slab reclaims** under Request Arena execution.
