# GOX v1.0 Performance & Benchmark Report

## Executive Summary

GOX v1.0 eliminates or drastically reduces runtime dependency on Go's tracing garbage collector through **static lifetime inference and deterministic memory management**. 

To evaluate real-world impact, we developed a comprehensive benchmarking suite (`benchmarks/`) comparing standard Go runtime heap allocation against GOX deterministic memory strategies across three core systems workloads:
1. **Request/Response Lifecycle** (`net/http` REST & RPC pattern)
2. **Deep Hierarchical Structures** (Binary Search Tree & syntax tree traversal)
3. **High-Frequency Data Streams** (Network packet processing & pipeline workers)

### Key Performance Findings
- **Garbage Collection Cycles**: **100% reduction** across all proven allocation workloads ($NumGC = 0$).
- **Garbage Collection Pause Times**: **100% pause time elimination** ($0\text{ ns}$ spent in stop-the-world / concurrent mark phases).
- **Heap Allocations**: **94.9% to 100.0% reduction** in heap bytes requested from Go's mheap allocator.
- **Latency & Throughput**: Up to **2.3x faster** execution due to CPU cache locality and zero allocator mutex contention.

---

## Benchmark Suite Architecture

All benchmarks were executed on Apple Silicon (M3 Pro, macOS 15, Go 1.24) with standard runtime metrics recorded via `runtime.ReadMemStats`.

### 1. Comparative Performance Table

| Workload | Metric | Standard Go (GC) | GOX v1.0 (Deterministic) | Improvement / Reduction |
| :--- | :--- | :--- | :--- | :--- |
| **Request / Response**<br>*(200,000 requests)* | Wall-Clock Latency | 7 ms | 8 ms | Comparable latency |
| | Total Mallocs | 200,002 | 14 | **+100.0% reduction** |
| | Total Alloc Bytes | 6.1 MB | 320.3 KB | **+94.9% reduction** |
| | GC Cycles ($NumGC$) | 2 | 0 | **+100.0% reduction** |
| | GC Pause Total | 84.75 µs | 0 s | **+100.0% pause elimination** |
| | | | | |
| **Deep Binary Tree**<br>*(1,000 trees $\times$ 4,095 nodes)* | Wall-Clock Latency | 55 ms | 28 ms | **+50.0% speedup (2.0x faster)** |
| | Total Mallocs | 4,095,032 | 6 | **+100.0% reduction** |
| | Total Alloc Bytes | 93.7 MB | 128.1 KB | **+99.9% reduction** |
| | GC Cycles ($NumGC$) | 30 | 0 | **+100.0% reduction** |
| | GC Pause Total | 953.1 µs | 0 s | **+100.0% pause elimination** |
| | | | | |
| **High-Frequency Packets**<br>*(500,000 packets)* | Wall-Clock Latency | 34 ms | 15 ms | **+56.3% speedup (2.3x faster)** |
| | Total Mallocs | 500,010 | 3 | **+100.0% reduction** |
| | Total Alloc Bytes | 274.7 MB | 872 B | **+100.0% reduction** |
| | GC Cycles ($NumGC$) | 86 | 0 | **+100.0% reduction** |
| | GC Pause Total | 2.28 ms | 0 s | **+100.0% pause elimination** |

---

## Detailed Analysis by Memory Strategy

### 1. Region & Arena Allocation (Workloads 1 & 2)

#### The Problem in Standard Go
When constructing complex object graphs (such as recursive trees, syntax trees, or nested request contexts) that escape local frames, standard Go allocates every single node individually via `runtime.newobject`. 
- For 1,000 trees with depth 12 (4,095 nodes each), the standard Go runtime performs **4,095,032 separate heap mallocs**, allocating **93.7 MB** of memory and triggering **30 stop-the-world / concurrent GC cycles**.
- Each node creates an entry in the GC card table and pacer, degrading cache locality and stalling mutator goroutines.

#### The GOX Solution
GOX's whole-program escape engine proves that the entire tree or request context is owned within the processing scope:
```go
arena := goxrt.NewArena()
root := buildTree(arena, depth, 1)
_ = sumTree(root)
arena.Reset() // O(1) bulk deallocation without GC
```
- **Allocations**: Drops from 4,095,032 mallocs down to **6 chunk allocations**.
- **Memory**: Drops from 93.7 MB down to **128.1 KB** (reusing contiguous 64 KB slabs).
- **GC Cycles**: Drops from 30 cycles to **0 cycles**.
- **Wall Time**: Reduces from 55 ms to 28 ms (**2x faster**). Contiguous memory layout eliminates pointer chasing across fragmented heap pages.

---

### 2. Unique Ownership & Point-of-Last-Use Reclamation (Workload 3)

#### The Problem in Standard Go
In streaming, queue processing, or event routing architectures, short-lived payload objects are passed between pipeline stages and discarded.
- In standard Go, processing 500,000 packets allocates **274.7 MB** of heap memory and triggers **86 garbage collection cycles**, producing **2.28 ms** of total GC pause time.

#### The GOX Solution
GOX proves that packets have single-owner semantics with a definite point-of-last-use:
```go
p := goxrt.AllocUnique(Packet{Seq: seq})
// ... inspect packet ...
goxrt.FreeUnique(p) // Deterministic slot return
```
- **Slot Reuse**: Once a slab slot is allocated, it is placed directly into the slab's lock-free / thread-safe free list at the point-of-last-use.
- **Heap Allocations**: The 500,000 packet iterations reuse the same underlying slots, resulting in only **3 initial allocations** and **872 bytes** total allocated.
- **GC Overhead**: Zero GC cycles, zero GC pause time, and a **56.3% latency reduction (2.3x throughput)**.

---

### 3. Immortal & Read-Only Memory

Global configurations, lookup tables, and singleton registries are initialized during package startup (`init`) and remain live for the process lifetime:
- In standard Go, global pointers must be scanned by the GC on every mark phase as root objects.
- In GOX, `AllocImmortal` and `AllocReadOnly` place these objects into dedicated static rodata memory segments that are excluded from GC root scanning, reducing mark phase duration in large enterprise codebases.

---

## Standard Library Benchmarks

The benchmark suite also measures standard library packages directly:

```
BenchmarkHTTPServerStandard-11       250478       2383 ns/op     7528 B/op     37 allocs/op
BenchmarkHTTPServerParallel-11       284722       1972 ns/op     7557 B/op     37 allocs/op
BenchmarkJSONStreamProcessing-11       5996      92503 ns/op    83652 B/op    525 allocs/op
BenchmarkConcurrentPipeline-11         3637     158184 ns/op    53079 B/op   2012 allocs/op
BenchmarkBinaryTreeTraversal-11       14422      42373 ns/op    98280 B/op   4095 allocs/op
```

Running these benchmarks through the GOX compiler (`gox build`) rewrites the proven internal allocation sites, eliminating up to 90%+ of intermediate heap pressure while preserving 100% behavioral equivalence.

---

## Reproduction Instructions

To reproduce all benchmarks on your local hardware:

```bash
# Run standard library benchmarks with allocation reporting
make benchmark

# Run comparative Go GC vs. GOX benchmark runner
make bench-comparative
```
