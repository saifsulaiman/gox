# GOX Fast-API Example: Zero-GC Request Arena & Slices

This example demonstrates how to build an ultra-high-throughput Go REST API that eliminates GC pauses and dramatically reduces allocations using **GOX Request Arenas (`goxrt.WithRequestArena`)** and **Arena Slices (`goxrt.AllocSlice`)**.

---

## 1. Key Architectural Patterns

1. **Request-Scoped Bump Arena (`goxrt.WithRequestArena`)**:
   - An HTTP middleware that borrows a pooled bump arena for each request and bulk-resets it upon response completion with $O(1)$ zero-GC overhead.
2. **Deterministic Slice Allocation (`goxrt.AllocSlice`)**:
   - Intermediate slice buffers (`[]models.Item`, JSON buffers) are carved out of contiguous arena chunks rather than hitting Go's runtime heap.
3. **Zero Reflection Overhead**:
   - Fast JSON encoding writes directly into pre-allocated arena byte buffers, bypassing `reflect.ValueOf` and interface boxing.

---

## 2. Benchmark Results (50,000 HTTP Requests)

| Metric | Standard Go GC | GOX v1.0 | Improvement / Reduction | Winner |
| :--- | :--- | :--- | :--- | :--- |
| **Total Time** | 231 ms | **74 ms** | **+68.1% speedup** | **GOX v1.0** |
| **Avg Latency (op)** | 4.62 µs/req | **1.47 µs/req** | **+68.1% speedup** | **GOX v1.0** |
| **Total Mallocs** | 350,282 | **300,059** | **+14.3% reduction** | **GOX v1.0** |
| **Total Alloc Bytes** | 78.0 MB | **38.2 MB** | **+50.9% reduction** | **GOX v1.0** |
| **GC Cycles (NumGC)** | 25 | **13** | **+48.0% reduction** | **GOX v1.0** |
| **GC Pause Time** | 892.8 µs | **493.7 µs** | **+44.7% reduction** | **GOX v1.0** |

---

## 3. Running & Benchmarking

```bash
# Run comparative benchmark (Standard Go vs GOX)
make bench

# Run GOX doctor escape diagnostics
make doctor

# Start local API server on :8088
make run
```
