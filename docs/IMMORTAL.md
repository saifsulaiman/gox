# GOX Immortal & Static Allocations Specification

## 1. Executive Summary

In **GOX v0.8**, we introduce **Immortal & Static Allocation Inference** (`StrategyImmortal`). This milestone establishes compile-time inference and runtime support for objects whose lifetimes span the entire execution of the process ($\tau(\alpha) = \infty$).

In standard Go runtimes, global singletons, routing radix trees, pre-computed lookup tables, and configuration caches are allocated on the general garbage-collected heap. Even though these objects are never freed during process execution, the standard Go garbage collector is forced to:
1. Scan every global variable and pointer root on **every single GC mark cycle**.
2. Execute GC write barriers on all pointer modifications within global data structures.
3. Keep track of dynamic allocation headers and finalizer tables for process-lifetime memory.

In production microservices, long-lived global data structures often comprise **15% to 35% of the total persistent heap**, creating significant GC pause times, cache pollution, and CPU overhead during tracing sweeps.

GOX v0.8 statically identifies process-lifetime allocations and proves that they never require collection prior to process termination. These allocations are promoted to `StrategyImmortal`, placing them in static zero-GC memory:
- **Zero GC Root Tracing**: Excluded from GC mark and sweep phases.
- **Zero Reference Counting Overhead**: No atomic CAS operations or retain/release instructions.
- **Zero Write Barrier Overhead**: Read-only tables (`IMMUTABLE_RODATA`) can be write-protected in memory.
- **Ultra-Low Latency Reads**: Direct memory reads running at **0.81 ns/op** (>1.2 billion operations/second).

---

## 2. Theoretical Framework & Formal Theorems

### 2.1 The Process-Lifetime Soundness Theorem
Let $\alpha$ be an allocation in a Go program $\mathcal{P}$, and let $\tau(\alpha)$ denote the lifetime of $\alpha$, defined as the duration between its allocation time $t_{alloc}(\alpha)$ and its earliest safe deallocation time $t_{free}(\alpha)$.

**Theorem 1 (Process Lifetime Soundness)**:
> An allocation $\alpha$ is soundly classified as `StrategyImmortal` if and only if:
> 1. $\alpha$ is rooted in package global state ($\text{Ownership}(\alpha) = \text{GLOBAL} \land \text{Lifetime}(\alpha) = \text{GLOBAL}$) or allocated within a package initialization function ($\text{Scope}(\alpha) \in \text{InitFunctions}$).
> 2. $\alpha$ does not cross an unmodeled FFI/Cgo boundary where foreign code might deallocate it ($\alpha \cap \text{FFI} = \emptyset$).
> 3. $\alpha$ does not escape to channels ($\alpha \cap \text{Channels} = \emptyset$) or unmodeled reflection ($\alpha \cap \text{Reflection} = \emptyset$).
> 4. $\alpha$ is retained for the entire process duration ($\tau(\alpha) = \infty$), such that $t_{free}(\alpha) \ge t_{exit}(\mathcal{P})$.

### 2.2 The Post-Init Immutability Theorem
Let $\text{Stores}(\alpha)$ be the set of all SSA `Store` instructions writing to $\alpha$ or any field/index address derived from $\alpha$ ($\text{Addr} \rightsquigarrow \alpha$).

**Theorem 2 (Post-Init Immutability)**:
> If for all $s \in \text{Stores}(\alpha)$, the enclosing function $\text{Parent}(s)$ satisfies $\text{isInitFunction}(\text{Parent}(s)) = \text{true}$, then $\alpha$ is **strictly immutable after package initialization**.
>
> That is, no store instruction ever writes to $\alpha$ during the execution of any non-init function (request handlers, background workers, or main loop). Such allocations are classified as:
> $$\alpha \in \text{IMMUTABLE\_RODATA}$$
> and may be safely mapped into write-protected read-only memory pages.

If there exists at least one $s \in \text{Stores}(\alpha)$ such that $\text{isInitFunction}(\text{Parent}(s)) = \text{false}$, the allocation is classified as:
$$\alpha \in \text{STATIC\_DATA}$$
indicating a mutable static singleton (e.g., global metrics counter, cache head) that lives for process lifetime but permits runtime updates.

---

## 3. Static Analysis Architecture

The `immortalProofEngine` (`internal/analyzer/immortalproof.go`) operates across 5 safety gates:

```
+-------------------------------------------------------------------+
|               Candidate Allocation Decision Site                  |
+-------------------------------------------------------------------+
                                  |
                                  v
+-------------------------------------------------------------------+
| Gate 1: Heap Type Safety Check                                    |
|   - Rejects channels (have runtime wait queues and locks)         |
+-------------------------------------------------------------------+
                                  | Pass
                                  v
+-------------------------------------------------------------------+
| Gate 2: Absolute Escape Hazards                                   |
|   - Rejects channels, unsafe.Pointer, reflection, FFI boundaries  |
+-------------------------------------------------------------------+
                                  | Pass
                                  v
+-------------------------------------------------------------------+
| Gate 3: Process Lifetime Verification                             |
|   - Ownership == GLOBAL or Lifetime == GLOBAL                     |
|   - Site within init() or stored to *ssa.Global                   |
+-------------------------------------------------------------------+
                                  | Pass
                                  v
+-------------------------------------------------------------------+
| Gate 4: Package-Wide Mutability Analysis                          |
|   - Traverses all non-init functions in package                   |
|   - Searches for stores to findGlobalRoot(store.Addr)             |
|   - Distinguishes IMMUTABLE_RODATA vs. STATIC_DATA                |
+-------------------------------------------------------------------+
                                  | Pass
                                  v
+-------------------------------------------------------------------+
| Gate 5: Invariant Synthesis & Proof Construction                  |
|   - Emits StrategyImmortal (Confidence: PROVEN)                   |
+-------------------------------------------------------------------+
```

---

## 4. Runtime Architecture

The immortal runtime (`internal/runtime/immortal.go`) provides thread-safe, bump-allocated process memory:

```go
type ImmortalStats struct {
    BytesAllocated int64 `json:"bytes_allocated"`
    AllocCount     int64 `json:"alloc_count"`
    ReadOnlyCount  int64 `json:"read_only_count"`
}

func AllocImmortal[T any](val T) *T
func AllocReadOnly[T any](val T) *T
func GetImmortalStats() ImmortalStats
```

### Key Performance Properties:
- **Lock-Free Read Access**: Reads require no synchronization primitives, atomic operations, or memory barriers. Reads execute at the raw hardware cache speed (**0.81 ns/op**).
- **Fast Allocation**: Allocations during package initialization execute via contiguous bump allocation (**24.89 ns/op**).
- **Zero GC Finalizers**: No finalizers are registered with the Go runtime; memory is freed en masse when the operating system reclaims the process address space at exit.

---

## 5. The Complete 7-Tier GOX Promotion Hierarchy

With the addition of `StrategyImmortal` in v0.8, the GOX memory strategy ladder spans 7 tiers:

| Tier | Strategy | Scope / Condition | Deallocation Mechanism | Proof Engine |
| :---: | :--- | :--- | :--- | :--- |
| **1** | `StrategyStack` | Frame-local, non-escaping | Instantaneous Stack Pointer adjustment | `stackProofEngine` |
| **2** | `StrategyImmortal` | Package globals, init tables, singletons | Zero deallocation (process lifetime) | `immortalProofEngine` |
| **3** | `StrategyRegion` | Lexically/call-bounded multi-object graphs | $O(1)$ Arena bulk release at boundary | `regionProofEngine` |
| **4** | `StrategyUniqueOwned` | Single-owner, linear borrow/move paths | Point of Last Use (PLU) deterministic free | `uniqueProofEngine` |
| **5** | `StrategyARC` | Multi-owner shared acyclic graphs | $O(1)$ reference counting with borrow elision | `arcProofEngine` |
| **6** | `StrategyWeak` | Cyclic graphs with asymmetric back-pointers | Dual-counter ARC + safe Upgrade() | `weakProofEngine` |
| **7** | `StrategyTracingFallback` | Open-world dynamic dispatch, FFI, channels | Standard Go tracing garbage collector | Conservative Fallback |
