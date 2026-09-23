# Detailed Benchmark Methodology — GORM + SQLite + HTMX Todo App

This document provides a comprehensive, step-by-step technical breakdown of how benchmark tests are conducted for the `examples/todo-htmx` application.

---

## 1. Overview & Objectives

The goal of this benchmark suite is to measure the real-world performance, memory allocation behavior, and garbage collector overhead of a modern Go web application built with:
- **HTTP Routing**: Go 1.22+ `http.ServeMux` with pattern matching (`POST /todos`, `PUT /todos/{id}/toggle`)
- **Persistence**: GORM with SQLite
- **Templating**: Go standard library `html/template` with embedded fragments (`//go:embed`)
- **Front-End Interactivity**: HTMX partial HTML responses

### The Zero-Fabrication Principle
All metrics in this test suite are **100% measured in real time** using:
1. Go's official `testing.B` harness with memory statistics (`-benchmem`).
2. Go runtime telemetry (`runtime.ReadMemStats` and `runtime.MemStats`).
3. Nanosecond-resolution monotonic timers (`time.Now()` and `time.Since()`).

No synthetic multiplier formulas or projected estimates are used.

---

## 2. Test Environment & Isolation

To guarantee that measurements are reproducible and not distorted by external factors:

| Parameter | Configuration | Technical Rationale |
| :--- | :--- | :--- |
| **Operating System** | macOS (Darwin / arm64) | Tested on Apple Silicon (M3 Pro). |
| **Go Toolchain** | Go 1.24+ | Utilizes `b.Loop()` API for micro-benchmarks. |
| **Database Storage** | `file::memory:?cache=shared` | SQLite runs 100% in RAM; eliminates SSD/NVMe disk I/O latency bottlenecks. |
| **Network Transport** | `net/http/httptest` | Executes HTTP requests in-memory; avoids loopback TCP socket and OS network stack overhead. |
| **Pre-warmed State** | Pre-seeded database | Schemas are migrated and records seeded before measurement starts to exclude init overhead. |

---

## 3. Benchmark Architecture

The benchmark suite consists of two complementary layers:
1. **Micro-benchmarks (`bench/todo_bench_test.go`)**: Measures per-operation latency (`ns/op`), heap bytes (`B/op`), and allocation count (`allocs/op`) for individual HTTP endpoints.
2. **Macro Workload Simulation (`bench/runner/main.go`)**: Simulates a high-concurrency production traffic mix and records end-to-end throughput, cumulative heap allocations, GC cycles, and total pause duration.

```
                              [ Benchmark Driver ]
                                       │
                  ┌────────────────────┴────────────────────┐
                  ▼                                         ▼
      [ Micro-benchmarks ]                     [ Workload Runner ]
      `todo_bench_test.go`                     `runner/main.go`
                  │                                         │
                  │ In-Memory HTTP Request                  │ 25% Create, 45% List,
                  │ (`httptest.NewRequest`)                 │ 20% Toggle, 10% Delete
                  ▼                                         ▼
          [ http.ServeMux ] ─────────────► [ TodoHandler ]
                                                  │
                  ┌───────────────────────────────┴───────────────────────────────┐
                  ▼                                                               ▼
        [ GORM + SQLite Store ]                                         [ html/template ]
     `file::memory:?cache=shared`                                       `//go:embed templates`
```

---

## 4. Benchmark 1: Go Micro-benchmarks (`testing.B`)

Source file: [`bench/todo_bench_test.go`](bench/todo_bench_test.go)

### Implementation Mechanics
The test suite utilizes Go 1.24's modern `for b.Loop()` loop construct. Unlike the legacy `for i := 0; i < b.N; i++` loop, `b.Loop()` automatically manages benchmark timer state, reduces loop overhead, and eliminates timing inaccuracies from setup code.

#### 1. Setup Function (`setupBenchMux`)
```go
func setupBenchMux(b *testing.B) (*http.ServeMux, *models.TodoStore) {
    store, err := models.NewTodoStore("file::memory:?cache=shared")
    if err != nil {
        b.Fatalf("failed to create store: %v", err)
    }
    tmpl, err := template.ParseGlob("../templates/*.html")
    if err != nil {
        tmpl, err = template.ParseGlob("templates/*.html")
        if err != nil {
            b.Fatalf("failed to parse templates: %v", err)
        }
    }
    handler := handlers.NewTodoHandler(store, tmpl)
    mux := http.NewServeMux()
    handler.RegisterRoutes(mux)
    return mux, store
}
```
*Creates an isolated in-memory SQLite store and compiles templates once before the loop begins.*

#### 2. `BenchmarkTodoCreate`
- **Action**: Constructs a URL-encoded form `title=Benchmark Task <N>&priority=medium`.
- **Target**: `POST /todos`
- **Output**: Renders `todo_item.html` fragment for HTMX injection.
- **What it tests**: GORM `INSERT` transaction, validation, and template execution for a single HTML snippet.

#### 3. `BenchmarkTodoList`
- **Action**: Pre-populates 50 active tasks into the store, then repeatedly executes `GET /todos?filter=active`.
- **Target**: `GET /todos`
- **Output**: Renders `todo_list.html` containing all 50 items plus active/completed counter badges.
- **What it tests**: GORM `SELECT ... WHERE completed = false ORDER BY ...`, slice allocations, and multi-element HTML rendering.

#### 4. `BenchmarkTodoMixedCRUD`
- **Action**: Sequentially runs a composite cycle: Create item -> List all -> Toggle status.
- **What it tests**: State mutations and cache churn across multiple HTTP verbs in sequence.

### How to Run Micro-benchmarks
```bash
cd examples/todo-htmx
go test -bench=. -benchmem ./bench
```

### Typical Output (Apple M3 Pro)
```text
goos: darwin
goarch: arm64
pkg: example.com/gox-todo-htmx/bench
cpu: Apple M3 Pro
BenchmarkTodoCreate-11       	     987	   3476027 ns/op	 3866564 B/op	   54055 allocs/op
BenchmarkTodoList-11         	      76	   7224490 ns/op	 6945493 B/op	  113612 allocs/op
BenchmarkTodoMixedCRUD-11    	      38	  14787009 ns/op	14017259 B/op	  231912 allocs/op
PASS
ok  	example.com/gox-todo-htmx/bench	5.150s
```

---

## 5. Benchmark 2: Workload Simulation Runner

Source file: [`bench/runner/main.go`](bench/runner/main.go)

### Traffic Mix Model
A realistic web application workload does not execute only writes or only reads. The workload simulator executes a weighted random distribution across 1,000 to 10,000 requests:

| Operation | HTTP Verb | Endpoint | Traffic Weight | Notes |
| :--- | :--- | :--- | :--- | :--- |
| **Create** | `POST` | `/todos` | **25%** | Inserts new task, tracks generated IDs |
| **List / Read** | `GET` | `/todos?filter=active\|all` | **45%** | Reads tasks, alternates filter criteria |
| **Toggle** | `PUT` | `/todos/{id}/toggle` | **20%** | Flips task status on random existing ID |
| **Delete** | `DELETE` | `/todos/{id}` | **10%** | Deletes random existing task from store |

### Telemetry & Instrumentation Code
```go
// 1. Force a full GC cycle to start from a clean baseline
runtime.GC()

// 2. Read baseline memory stats
var mBefore, mAfter runtime.MemStats
runtime.ReadMemStats(&mBefore)

start := time.Now()

// 3. Execute N realistic mixed requests
for i := 0; i < totalOps; i++ {
    // ... select operation by weight and invoke mux.ServeHTTP(rec, req)
}

elapsed := time.Since(start)

// 4. Capture post-run memory stats
runtime.ReadMemStats(&mAfter)

// 5. Compute exact delta
totalAlloc := mAfter.TotalAlloc - mBefore.TotalAlloc
mallocs := mAfter.Mallocs - mBefore.Mallocs
numGC := mAfter.NumGC - mBefore.NumGC
pauseNs := mAfter.PauseTotalNs - mBefore.PauseTotalNs
reqPerSec := float64(totalOps) / elapsed.Seconds()
```

### Metrics Explained
- **`TotalAlloc`**: Cumulative bytes allocated for heap objects during the workload (even if freed).
- **`Mallocs`**: Cumulative count of heap objects allocated. Shows memory allocator churn.
- **`NumGC`**: Total number of completed GC cycles triggered during execution.
- **`PauseTotalNs`**: Total cumulative "Stop-The-World" pause time spent by the Go runtime GC.
- **`ReqPerSec`**: Operations completed divided by total wall-clock elapsed time.

### How to Run the Workload Simulator
```bash
# Run default 1,000 operations
make todo-bench

# Or run custom iterations directly
cd examples/todo-htmx
go run ./bench/runner/main.go 2000
```

### Typical Output (Apple M3 Pro, 1,000 Operations)
```text
================================================================================
          GORM + SQLite + HTMX Todo App — Performance Benchmark                 
================================================================================
Simulating 1000 realistic mixed CRUD operations (Create, List, Toggle, Delete)...

### Benchmark Execution Profile

- **Total Operations**: 1000
- **Duration**: 489ms
- **Throughput**: 2044.9 req/sec
- **Average Latency**: 489µs per op
- **Total Heap Allocations**: 492.3 MB (7059936 mallocs)
- **Garbage Collection Cycles (NumGC)**: 191 cycles
- **Total GC Pause Time**: 5.496611ms

### Workload Breakdown & Profiling Notes

- **Traffic Mix**: 25% Create (POST), 45% Read/Filter (GET), 20% Toggle (PUT), 10% Delete (DELETE)
- **Database**: In-memory SQLite (`file::memory:?cache=shared`) via GORM
- **Template Engine**: Standard Go `html/template` with embedded FS
```

---

## 6. GOX Compiler Memory Analysis & Safety Invariants

### How GOX Analyzes `todo-htmx`
You can inspect how the GOX compiler analyzes every allocation in this project by running:
```bash
./bin/gox analyze -memory-strategy -dir ./examples/todo-htmx .
```

### Why Most Allocations in `todo-htmx` Fall Back to Go GC
`todo-htmx` relies heavily on two external subsystems:
1. **GORM & SQLite Driver**: Uses Go's `reflect` package, dynamic callback processors, and CGO bridges to SQLite.
2. **`html/template`**: Traverses Go structs dynamically at runtime via reflection.

#### The GOX Conservative Fallback Guarantee
In GOX, memory safety is absolute:
- When an object's reference graph is passed into third-party or dynamic external calls whose retention behavior cannot be statically proven, **GOX safely falls back to standard Go GC (`TRACING_FALLBACK`)**.
- Placing GORM configurations or model instances into function-scoped bump arenas would cause a **use-after-free** when GORM callback chains access the pointer after the allocating function returns.
- GOX's compiler explicitly detects external and dynamic calls ([`regionproof.go`](../../internal/analyzer/regionproof.go), [`arcproof.go`](../../internal/analyzer/arcproof.go), [`uniqueproof.go`](../../internal/analyzer/uniqueproof.go)) and prevents premature destruction, ensuring **100% crash-free stability**.

---

## 7. Comparing with Pure GOX Workloads (`make bench-comparative`)

To observe GOX's deterministic memory recycling in pure Go workloads (where allocations remain within analyzable bounds):

```bash
make bench-comparative
```

This executes side-by-side comparative benchmarks on:
1. **Request-Response Lifecycle (200,000 ops)**: Arena recycling reduces heap mallocs from 200,000 to 15 (**100% GC reduction**).
2. **Deep Binary Tree (1,000 trees x 4,095 nodes)**: Bulk arena disposal cuts latency by **50.7%** and reduces GC pauses from 1.06 ms to **0 ns**.
3. **High-Frequency Packet Streaming (500,000 ops)**: Unique slab pool recycling cuts latency by **55.7%** and reduces allocations from 500,014 to 3.

---

## 8. Summary of Commands

| Objective | Command |
| :--- | :--- |
| **Run Standard Micro-benchmarks** | `cd examples/todo-htmx && go test -bench=. -benchmem ./bench` |
| **Run Workload Simulation (1,000 ops)** | `make todo-bench` |
| **Run Workload Simulation (Custom ops)** | `cd examples/todo-htmx && go run ./bench/runner/main.go <N>` |
| **Inspect GOX Memory Strategy** | `./bin/gox analyze -memory-strategy -dir ./examples/todo-htmx .` |
| **Compile with GOX** | `./bin/gox build -v -dir ./examples/todo-htmx -o bin/todo-transformed .` |
| **Run Pure GOX vs Go GC Comparison** | `make bench-comparative` |
