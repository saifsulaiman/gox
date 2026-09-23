# Running Go Applications with GOX (`gox run` & `gox build`)

This guide explains how to run, test, and deploy ordinary Go applications using the **GOX compiler and runtime toolchain**.

GOX is **100% compatible with standard Go 1.24+ source code**. You do not need to rewrite your code, add annotations, or modify existing dependencies.

---

## 1. Quick Start: `gox run` vs. `gox build`

GOX provides two primary ways to run Go applications:

| Command | Best Used For | What It Does |
| :--- | :--- | :--- |
| **`gox run [flags] [pkg] [-- args...]`** | **Local Development & Fast Testing** | Analyzes, rewrites, compiles to an ephemeral binary in a temporary sandbox, executes immediately, streams I/O, and automatically deletes the temporary binary upon exit. Equivalent to `go run`. |
| **`gox build [flags] [pkg]`** | **Production Releases & Distribution** | Analyzes, rewrites, and produces a standalone, optimized native binary ready for server deployment or container packaging. Equivalent to `go build`. |

---

## 2. Running Applications with `gox run`

### 2.1 Basic Execution

In your Go project directory (where `go.mod` or `main.go` resides), run:

```bash
gox run .
```

GOX will:
1. Perform whole-program escape and lifetime analysis across 7 memory tiers.
2. Transform proven allocation sites (arenas, unique slab pools, immortal segments, ARC) in an ephemeral directory.
3. Compile and execute the native binary.
4. Clean up all temporary build files when the application terminates.

### 2.2 Forwarding Application Arguments and Flags

If your Go application accepts command-line flags (such as `-port`, `-db`, `-v`, or positional arguments), separate GOX toolchain flags from your application flags using **`--`**:

```bash
# General syntax:
gox run [gox-flags] [package] -- [application-arguments]

# Examples:
gox run . -- -port 8080 -db app.db
gox run . -- -env production --workers=8
gox run ./cmd/server -- --config=/etc/app/config.yaml
```

### 2.3 Running from Anywhere with `-dir`

You do not need to change directories (`cd`) into your project root. Use the `-dir` flag to target any directory:

```bash
# Run the basic example
gox run -dir ./examples/basic .

# Run the concurrent worker pipeline example
gox run -dir ./examples/pipeline .

# Run the GORM + SQLite + HTMX Todo application
gox run -dir ./examples/todo-htmx . -- -port 8080 -in-memory
```

### 2.4 Useful Flags for `gox run`

- **`-v` (Verbose)**: Prints discovered allocation sites, strategy breakdowns, and rewritten files:
  ```bash
  gox run -v .
  ```
- **`-keep-transformed`**: Preserves the transformed AST source files and embedded runtime in `.gox-cache/staging` so you can inspect the exact code being executed:
  ```bash
  gox run -keep-transformed .
  ```
- **`-no-cache`**: Forces GOX to bypass the incremental decision cache and re-analyze all packages from scratch:
  ```bash
  gox run -no-cache .
  ```

---

## 3. Production Deployment with `gox build`

For staging, production environments, and Docker containers, use `gox build` to generate standalone native binaries.

### 3.1 Standard Native Build

```bash
# Compile current package into 'myapp' binary
gox build -o myapp .

# Run the compiled binary directly
./myapp -port 8080
```

### 3.2 Production Cross-Compilation

GOX supports full cross-compilation without external C toolchains:

```bash
# Compile for Linux x86_64 from macOS:
gox build -os linux -arch amd64 -ldflags="-s -w" -o bin/server-linux .

# Compile for Linux ARM64 (AWS Graviton / Raspberry Pi):
gox build -os linux -arch arm64 -ldflags="-s -w" -o bin/server-arm64 .

# Compile for Windows x86_64:
gox build -os windows -arch amd64 -o bin/server.exe .
```

---

## 4. Real-World Application Recipes

### Recipe 1: Web APIs & Microservices (`net/http`, GORM, Gin, Echo)

In request-heavy web services, standard Go creates millions of short-lived heap allocations per second (contexts, request/response structs, JSON buffers), stressing the GC.

When running with GOX:
```bash
cd examples/todo-htmx
gox run . -- -port 8080 -db todos.db
```
- Request-scoped data structures in pure Go handlers are proven to have bounded lifecycles and are allocated into scoped Arenas or Unique-owner free-list pools.
- When interacting with reflection-heavy ORMs (like GORM) or CGO drivers (like SQLite), GOX's safety engine automatically applies conservative fallback (`TRACING_FALLBACK`) to ensure 100% crash-free execution.
- See [examples/todo-htmx/BENCHMARK.md](./examples/todo-htmx/BENCHMARK.md) for full benchmark methodology.

### Recipe 2: Streaming Data Pipelines & Worker Pools

For streaming worker pipelines processing events through channels:
```bash
gox run -dir ./examples/pipeline .
```
- Batch allocations inside workers are recycled at the point of last use.
- Configuration structs initialized at startup are placed into static read-only memory, excluded from GC root scanning.

### Recipe 3: Live Reloading during Development

You can pair `gox run` with popular Go live-reload tools like [Air](https://github.com/air-verse/air):

In your `.air.toml`:
```toml
[build]
  cmd = "gox build -o ./tmp/main ."
  bin = "./tmp/main"
  full_bin = "./tmp/main -- -port 8080"
  include_ext = ["go", "html", "css"]
  exclude_dir = ["assets", "tmp", "vendor", ".gox-cache"]
```
Every file save will trigger GOX's sub-millisecond incremental cache, immediately reloading your application with zero GC memory optimizations.

### Recipe 4: Production Multi-Stage Dockerfile

Deploy GOX-optimized applications to lightweight alpine/scratch containers:

```dockerfile
# Stage 1: Build using GOX
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install GOX
RUN go install github.com/goxlang/gox/cmd/gox@latest

# Copy dependency definitions and source
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Compile with GOX for Linux target
RUN gox build -os linux -arch amd64 -ldflags="-s -w" -o /app/server .

# Stage 2: Minimal runtime image
FROM alpine:3.20

WORKDIR /app
COPY --from=builder /app/server /app/server

EXPOSE 8080
ENTRYPOINT ["/app/server"]
```

---

## 5. Verifying Memory Behavior with `pprof`

Because GOX produces 100% standard Go binaries, all official Go profiling and diagnostic tools continue to work seamlessly:

```bash
# Profile heap allocations in your running GOX application
go tool pprof http://localhost:8080/debug/pprof/heap

# Profile execution latency and goroutines
go tool pprof http://localhost:8080/debug/pprof/profile?seconds=30
```

Notice that allocation sites transformed to `goxrt` Arenas and Unique Pools will no longer show up under Go `mheap` allocation profiles, confirming the elimination of GC overhead.

---

## 6. Summary of Running Options

| Need | Recommended Command |
| :--- | :--- |
| Quick test run | `gox run .` |
| Test with arguments | `gox run . -- -port 9000 -v` |
| Inspect generated code | `gox run -keep-transformed .` |
| Inspect transformation steps | `gox run -v .` |
| Build for current machine | `gox build -o myapp .` |
| Build for Linux server | `gox build -os linux -arch amd64 -o myapp-linux .` |
| Static analysis report | `gox analyze -memory-strategy ./...` |
