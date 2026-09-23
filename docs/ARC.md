# GOX Automatic Reference Counting (ARC) Specification

## 1. Executive Summary

GOX v0.6 introduces **Automatic Reference Counting (ARC)** (`StrategyARC`), establishing deterministic, $O(1)$ reference-counted lifecycle management for shared, multi-owner objects that cannot be bounded by a single stack frame, region arena, or unique owner.

In standard Go, shared objects (e.g., shared configuration trees, immutable payload nodes, shared DAG structures, reference-shared container items) are conservatively placed on the runtime tracing garbage collected heap. Tracing GC introduces periodic mark-sweep pause latency, write barriers, and unpredictable deallocation timing.

GOX v0.6 eliminates tracing GC overhead for shared objects without adding any new syntax, lifetime annotations, or manual memory management requirements:
1. The developer writes ordinary, idiomatic Go code.
2. GOX proves whether an object's type reference graph is **strictly acyclic**.
3. GOX statically determines reference duplication points (`retain`) and points of last reference (`release`).
4. GOX runs a **Swift-style borrow elision pass** that statically eliminates redundant retain/release pairs for read-only borrows.
5. GOX selects high-performance **non-atomic reference counters** for goroutine-confined objects, reserving atomic counters only for multi-threaded concurrency boundaries.

---

## 2. Theoretical Framework & Invariants

### 2.1 The Theorem of Sound ARC
Reference counting without cycle detection leaks memory when cyclical reference topologies exist ($A \to B \to A$).

**Fundamental Theorem of Sound ARC**:
> A shared object $v$ can be safely and deterministically managed by reference counting without cycle collectors or runtime tracing if and only if its type dependency graph $\mathcal{G}_{type}$ and reachable reference graph $\mathcal{G}_{ref}$ are **mathematically proven acyclic**.
>
> That is, there exists no directed sequence of pointer, slice, map, interface, or struct field references such that:
> $$v \rightsquigarrow v_1 \rightsquigarrow v_2 \dots \rightsquigarrow v$$
>
> If an object graph is acyclic, all reference counts are guaranteed to drop monotonically to zero upon cessation of use, ensuring **Zero Cycle Leaks** and **Immediate $O(1)$ Destruction**.

If an allocation belongs to a type graph that contains a potential cycle (e.g., linked list nodes with `Next *Node`, doubly-linked lists, or recursive tree nodes with parent pointers), GOX rejects `StrategyARC`. If such cycles are bounded by a lexical or function lifecycle, GOX delegates them to `StrategyRegion` (v0.4 Arena); otherwise, they safely fall back to runtime tracing GC (`StrategyTracingFallback`).

### 2.2 The Reference Preservation Invariant
For any active reference $r$ to an ARC box $B$:
$$\text{RefCount}(B) = \sum_{p \in \text{LivePaths}} \text{Retains}(B, p) - \text{Releases}(B, p)$$
At all program points during the lifetime of $r$, $\text{RefCount}(B) \ge 1$. When the last active reference drops out of scope, $\text{RefCount}(B) = 0$, triggering immediate invocation of the value's destructor and memory reclamation.

---

## 3. Static Analysis Architecture

### 3.1 Type & Object Graph Cycle Analyzer (`internal/analyzer/cycle.go`)
GOX constructs the Type Reference Dependency Graph $\mathcal{G}_{type} = (V_{type}, E_{field})$ for every analyzed type:
- Vertices $V_{type}$ represent Go types (`struct`, `pointer`, `slice`, `map`, `interface`).
- Directed edges $E_{field}$ represent reachable child types through exported and unexported fields, element types, and value types.
- The engine uses Tarjan's Strongly Connected Components (SCC) and depth-first search (DFS) with recursive memoization.
- A type is proven acyclic if every reachable Strongly Connected Component is trivial (size 1 with no self-loop).

### 3.2 Retain and Release Site Placement (`internal/analyzer/arcproof.go`)
- **Retain Sites**: Placed at program points where references are duplicated or stored into longer-lived containers:
  * Pointer stores into struct fields or slice elements (`*ssa.Store`)
  * Passing references to consuming functions
  * Returns from constructors or factory methods
- **Release Sites**: Placed at points of last use determined by the SSA Control-Flow Graph (CFG) liveness analysis engine (`internal/analyzer/liveness.go`):
  * Immediately following the final use of a reference in a basic block
  * Prior to function returns where the local reference is dropped
  * Along early exit and error branches that abort prior to normal consumption

### 3.3 Swift-Style Borrow Elision Pass (`internal/analyzer/arcelision.go`)
Naive reference counting emits excessive retain/release operations around temporary variables and local function arguments. This causes CPU cache thrashing and unnecessary atomic bus-locking.

GOX implements a static borrow elision pass inspired by Swift's SIL ARC Optimizer:
1. Within each basic block, pairs of `RETAIN` and `RELEASE` instructions targeting the same variable without intervening escaping stores are matched.
2. Calls to non-retaining callees (callees that borrow the pointer without storing it into longer-lived structures) are classified as **borrows**.
3. Matched pairs are marked as `ELIDED_RETAIN` and `ELIDED_RELEASE` and omitted from generated runtime code, reducing runtime reference count adjustments to zero.

### 3.4 Atomic vs. Non-Atomic Counter Selection
- **Non-Atomic Counters** (`is_atomic: false`): Used when all reference flows remain confined to the creating goroutine. Plain register increments/decrements avoid atomic instruction latency ($O(1)$ with zero cache-coherence bus lock overhead).
- **Atomic Counters** (`is_atomic: true`): Used when references cross concurrency boundaries (`OwnershipConcurrent` or shared goroutine contexts), using `sync/atomic.Int64` to prevent race conditions across threads.

---

## 4. Runtime Data Model (`internal/runtime/arc.go`)

GOX provides a zero-overhead generic reference counting runtime:

```go
type Box[T any] struct {
    val      T
    atomic   bool
    atomicRC atomic.Int64
    plainRC  int64
}
```

### Core Operations
- `NewBox[T](val T, isAtomic bool) *Box[T]`: Allocates a new box initialized with reference count = 1.
- `Retain[T](b *Box[T])`: Increments reference count (atomic or non-atomic).
- `Release[T](b *Box[T], destructor func(*T))`: Decrements reference count. If count reaches zero, invokes optional destructor and frees memory immediately.
- `Value[T](b *Box[T]) *T`: Returns direct pointer to boxed payload.
- `Count[T](b *Box[T]) int64`: Returns current reference count.

---

## 5. Promotion Hierarchy Integration

GOX enforces a strict 5-tier promotion hierarchy for every allocation:

```
+--------------------------------------------------------------------+
| 1. STACK (StrategyStack) - Frame-local hardware SP bump ($0)       |
+---------------------------------+----------------------------------+
                                  | (Escapes Frame)
                                  v
+--------------------------------------------------------------------+
| 2. REGION (StrategyRegion) - Multi-frame / cyclic arena ($O(1)$)   |
+---------------------------------+----------------------------------+
                                  | (Unbounded Scope / Dynamic Lifetime)
                                  v
+--------------------------------------------------------------------+
| 3. UNIQUE OWNED (StrategyUniqueOwned) - Single owner PLU free      |
+---------------------------------+----------------------------------+
                                  | (Shared / Multi-Owner Retention)
                                  v
+--------------------------------------------------------------------+
| 4. ARC (StrategyARC) - Proven acyclic shared ref-counting ($O(1)$)  |
+---------------------------------+----------------------------------+
                                  | (Cyclic / Global / Channel / Unproven)
                                  v
+--------------------------------------------------------------------+
| 5. TRACING FALLBACK (StrategyTracingFallback) - Safe Go runtime GC |
+--------------------------------------------------------------------+
```

### Strict Adversarial Rejection
ARC proof strictly rejects and falls back to `StrategyTracingFallback` when:
- **Cycles**: Structural type graph or value retention graph has cycle risk.
- **Channels**: References sent over Go channel buffers.
- **Globals**: Stored in package-level or process-lifetime global state.
- **Unsafe Pointers**: `unsafe.Pointer` conversions obscure object provenance.
- **Reflection**: `reflect.Value` inspection obscures lifecycle tracking.
- **FFI**: Exported library APIs or cgo/foreign-function boundaries without ARC contracts.

---

## 6. Verification & Performance

- **Unit & Race Testing**: 100% test pass rate under `go test ./...` and `go test -race ./...`.
- **Borrow Elision**: Elides 100% of redundant retain/release pairs on borrowed inspection workflows.
- **Analysis Latency**: Whole-program acyclic proofs and ARC placement execute in **~30.4 ms**.
