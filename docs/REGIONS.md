# GOX Region & Arena Inference Specification

## 1. Executive Summary

GOX v0.4 introduces **Region and Arena Inference**, enabling automatic, deterministic memory management for allocations whose lifetime exceeds an individual function frame but remains bounded by an identifiable program lifecycle (e.g. caller scope, loop body, request handler, parser unit, or batch transaction).

Traditional Go forces all such objects onto the runtime heap, causing GC write barriers, memory fragmentation, and periodic mark-sweep pause overhead. GOX v0.4 proves the lifetime boundaries of these allocations and assigns them to scoped arenas that are allocated via fast bump allocation and reclaimed or reset in bulk ($O(1)$) with zero garbage collector involvement.

---

## 2. Theoretical Framework & Invariants

### 2.1 The Outlive-Ordering Theorem
Let $\mathcal{R}_A$ and $\mathcal{R}_B$ be two memory regions. We define the partial order $\mathcal{R}_A \succeq \mathcal{R}_B$ ($\mathcal{R}_A$ *outlives* $\mathcal{R}_B$) if the lifetime of $\mathcal{R}_A$ encompasses the lifetime of $\mathcal{R}_B$.

**Fundamental Theorem of Region Safety**:
> A reference from region $\mathcal{R}_1$ to an object in region $\mathcal{R}_2$ is sound if and only if $\mathcal{R}_2 \succeq \mathcal{R}_1$.
>
> That is, references may point from shorter-lived regions to longer-lived regions (or global state), but **never** from longer-lived regions to shorter-lived regions.

If a longer-lived region held a pointer into a shorter-lived region, deallocating the shorter-lived region would leave a dangling pointer in the longer-lived region, causing use-after-free bugs.

### 2.2 Cyclic Objects within Regions
A major weakness of Automatic Reference Counting (ARC) is its inability to collect cyclic data structures ($A \to B \to A$) without complex cycle detection algorithms or backup tracing GC passes.

**Theorem on Region Cycles**:
> Any object graph, regardless of internal reference topology (including arbitrary directed cycles), can be safely allocated in a single region $\mathcal{R}$ provided the entire graph's lifetime is bounded by $\mathcal{R}$.
>
> Bulk reclamation of $\mathcal{R}$ deallocates the entire cycle instantaneously ($O(1)$) without traversing edges or detecting cycles.

---

## 3. Supported Region Scopes

| Scope | Inferred Lifetime | Typical Use Cases | Deallocation Point |
| :--- | :--- | :--- | :--- |
| `ScopeCaller` | Bounded by caller function frame | Constructors, factory functions, returned DTOs | Caller function return |
| `ScopeFunction` | Bounded by callee function frame | Temporary cyclic graphs, batch processing, AST units | Function return |
| `ScopeLexical` | Bounded by loop iteration or block | Loop-local buffers, row parsing, batch stream transforms | Loop iteration end |
| `ScopeRequest` | Bounded by request/transaction lifecycle | HTTP/gRPC handlers, database transactions | Request completion |
| `ScopeNested` | Bounded by sub-computation within parent arena | Intermediate calculations inside a long-lived request | Child arena free |

---

## 4. Internal Region IR

Region operations are internal compiler abstractions, not Go source syntax. The developer writes ordinary Go code without annotations:

```text
region.create(scope=CALLER, id=reg:1)
ptr := region.alloc(reg:1, type=*User, size=32)
region.borrow(ptr, reg:1)
// ... use ptr ...
region.destroy(reg:1) // or region.reset(reg:1)
```

### Operation Semantics
- `region.create(scope, id)`: Instantiates or resets an arena descriptor at the scope entry point.
- `region.alloc(reg, type, size)`: Sub-allocates from the arena buffer using pointer-aligned bump allocation ($O(1)$).
- `region.borrow(ptr, reg)`: Establishes a local alias within the region lifetime.
- `region.escape(ptr, target)`: Verifies whether an alias crosses a region boundary.
- `region.reset(reg)`: Resets chunk allocation offset to 0, recycling the memory buffer for the next iteration without GC allocation.
- `region.destroy(reg)`: Releases arena chunks back to the OS or allocator pool.

---

## 5. Runtime Architecture (`internal/runtime/arena.go`)

The GOX runtime implementation provides a zero-lock, goroutine-confined chunked bump allocator:
- **Chunk Size**: Default 64 KB contiguous buffers.
- **Allocation Cost**: 2 CPU instructions (offset addition and pointer comparison).
- **Reset Cost**: $O(1)$ pointer reset across active chunks.
- **Fragmentation**: Zero per-object header overhead; external fragmentation bounded by chunk size.

---

## 6. Verification and Boundary Hazards

An allocation candidate is disqualified from region placement if it exhibits any of the following boundary hazards:
1. **Global Escapes**: Storing references into package-level or global variables.
2. **Concurrency Escapes**: Transmitting references across un-joined goroutine boundaries or channels.
3. **Unsafe / Reflection**: Converting references to `unsafe.Pointer` or passing to `reflect.ValueOf`.
4. **Open-World Calls**: Passing references to external assembly, Cgo, or open-world dynamic dispatch without closed-world guarantees.
