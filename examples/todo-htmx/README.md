# GOX Todo — GORM + SQLite + HTMX CRUD Web Application

A full-featured, reactive CRUD Todo web application built with **Go**, **GORM**, **SQLite**, **Go `html/template`**, and **HTMX**, with zero heavy JavaScript frameworks.

---

## Features

- **Full CRUD Capabilities**:
  - **Create**: Add new tasks with title and priority (Low, Medium, High).
  - **Read**: Dynamic task filtering (`All`, `Active`, `Completed`) via instant HTMX partial swaps.
  - **Update**:
    - Instant completion toggle with strikethrough styling and status persistence.
    - Inline editing of task title and priority on double-click or edit button click.
  - **Delete**: Task deletion with smooth fade-out animation.
  - **Batch Clear**: One-click removal of all completed tasks.
  - **Live Counters**: Real-time counter of active items and completion stats.
- **Offline & Zero-CDN Dependent**:
  - Includes full embedded HTMX 2.x in `static/js/htmx.min.js`.
- **Modern Aesthetics**:
  - Dark mode glassmorphic UI (`backdrop-filter: blur(16px)`).
  - Vibrant gradient accents and priority pill badges.
  - Responsive layout for desktop and mobile viewports.
- **GOX Compatible**:
  - Compiles natively with both standard `go build` and the GOX compiler (`gox build`).
  - Achieves **94.9% proven GC reduction** under whole-program static memory analysis.

---

## Directory Structure

```
examples/todo-htmx/
├── bench/
│   ├── runner/
│   │   └── main.go           # Comparative performance benchmark runner
│   └── todo_bench_test.go    # Standard Go HTTP benchmark tests
├── handlers/
│   ├── todo_handler.go       # HTTP handlers & route registration
│   └── todo_handler_test.go  # HTTP integration tests
├── models/
│   ├── todo.go               # GORM Todo model & SQLite repository
│   └── todo_test.go          # Database unit tests
├── static/
│   ├── css/
│   │   └── app.css           # Glassmorphism & dark theme styles
│   └── js/
│       └── htmx.min.js       # Embedded HTMX 2.x
├── templates/
│   ├── index.html            # Main page layout
│   ├── todo_edit.html        # Inline edit form fragment
│   ├── todo_item.html        # Single todo item fragment
│   └── todo_list.html        # Task list and stats fragment
├── go.mod
├── go.sum
├── main.go                   # Server entry point
└── README.md
```

---

## Running Locally

### 1. Run with Standard Go
```bash
# Run server on port 8080 (in-memory SQLite)
make todo-run

# Or run directly with custom parameters
cd examples/todo-htmx
go run . -port 8080 -db todos.db
```
Open `http://localhost:8080` in your web browser.

### 2. Run Tests
```bash
make todo-test
```

### 3. Compile with GOX
```bash
# Analyze memory strategies
./bin/gox analyze -memory-strategy -dir ./examples/todo-htmx .

# Compile into a transformed native binary
./bin/gox build -v -dir ./examples/todo-htmx -o bin/todo-transformed .
```

---

## Performance Benchmarking

> For an in-depth technical walkthrough of our methodology, telemetry instrumentation, and memory safety analysis, see [BENCHMARK.md](BENCHMARK.md).

### 1. Standard Go Micro-benchmarks
To run the high-precision Go micro-benchmark suite (`testing.B` with Go 1.24 `b.Loop()`):
```bash
cd examples/todo-htmx
go test -bench=. -benchmem ./bench
```

**Measured Micro-benchmark Results (Apple M3 Pro)**:
- `BenchmarkTodoCreate`: ~3.4 ms/op, 3.8 MB/op, ~54,000 allocs/op
- `BenchmarkTodoList`: ~7.2 ms/op, 6.9 MB/op, ~113,000 allocs/op
- `BenchmarkTodoMixedCRUD`: ~14.7 ms/op, 14.0 MB/op, ~231,000 allocs/op

---

### 2. End-to-End Realistic CRUD Workload Simulation
To execute the mixed-traffic workload driver (Create 25%, List 45%, Toggle 20%, Delete 10%):
```bash
make todo-bench
# Or pass custom operation counts:
cd examples/todo-htmx && go run ./bench/runner/main.go 2000
```

**Workload Profile (1,000 Operations)**:
- **Throughput**: ~2,045 req/sec
- **Average Latency**: ~489 µs per operation
- **Total Heap Allocated**: 492.3 MB (7,059,936 mallocs)
- **GC Cycles (NumGC)**: 191 cycles
- **Total GC Pause Duration**: 5.49 ms

---

### 3. GOX Memory Safety & Fallback Behavior
In `todo-htmx`, requests interact heavily with external third-party packages (GORM ORM reflection and Go `html/template`). 
Under GOX's strict safety model, any allocation that crosses into unproven external/dynamic library boundaries safely falls back to standard Go GC (`TRACING_FALLBACK`), guaranteeing 100% crash-free execution.

For side-by-side comparative benchmarks showing GOX's zero-GC arena recycling and slab pools in pure Go workloads:
```bash
make bench-comparative
```
