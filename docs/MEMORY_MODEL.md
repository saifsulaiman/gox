# GOX Memory Model & Strategy Taxonomy

## Philosophy: Unmodified Go, Proven Memory Strategies

GOX is built on a fundamental premise:
**The input language is ordinary Go. No new syntax. No lifetime annotations. No borrow-checker boilerplate. No manual free.**

Eliminating or drastically reducing dependence on a tracing garbage collector without language modifications requires the compiler to prove memory lifetimes statically and assign the most efficient memory strategy supported by sound evidence. Where a deterministic lifetime can be proven, memory can be allocated on the call stack or in deterministic arenas/regions. Where proof cannot be established, the compiler safely falls back to conventional tracing garbage collection.

---

## Three-Tier Confidence Model

To ensure absolute safety, GOX v0.3 introduces a formal three-tier confidence model for all memory strategy decisions:

```
+-------------------------------------------------------------------------+
|                               CONFIDENCE                                |
+------------------+---------------------------+--------------------------+
|      PROVEN      |         PROBABLE          |         UNPROVEN         |
|                  |                           |                          |
| Sound invariant  | Suggestive signals exist, | Hazard or unmodeled      |
| proof complete;  | but proof obligations     | construct present;       |
| verified against | remain unfulfilled;       | cannot bound lifetime;   |
| all constructs.  | NOT safe to transform.    | Tracing GC required.     |
|                  | (Falls back in v0.5)      |                          |
+------------------+---------------------------+--------------------------+
```

### 1. `PROVEN`
A formal, defensible proof has been constructed. Every reference path, alias, and escape construct has been verified to be bounded by the target lifetime. Only `PROVEN` decisions may be used by subsequent compiler transformation passes to alter allocation placement.

### 2. `PROBABLE`
Static analysis signals indicate the allocation is likely bounded (e.g. classified as `POTENTIALLY_STACK_SAFE` in v0.2, or identified as a candidate region in caller scope), but one or more proof obligations cannot be closed. In v0.3, all `PROBABLE` candidates are treated with conservative safety: they default to `TRACING_FALLBACK`.

### 3. `UNPROVEN`
A known escape hazard, runtime boundary, or unmodeled SSA construct is present (e.g., global retention, channel transmission, goroutine boundary, reflection, `unsafe.Pointer`, or open-world dynamic dispatch). The allocation must remain under the tracing garbage collector.

---

## Memory Strategy Taxonomy

GOX defines eight discrete memory strategies covering the full lifecycle spectrum:

| Strategy | Description | Active Status |
| :--- | :--- | :--- |
| `STACK` | Allocation placed directly in the current function's stack frame. Deallocated automatically upon function return with zero GC overhead. | **Active** (v0.3+, when `PROVEN`) |
| `CALLER_STACK` | Allocation placed in the caller's stack frame via hidden return pointer slot. | Planned (v0.5.x) |
| `REGION` | Allocation placed within a scoped arena or region tied to a caller, lexical, or dynamic lifecycle. Reset/freed in bulk ($O(1)$). | **Active** (v0.4+, when `PROVEN`) |
| `UNIQUE_OWNED` | Allocation has exactly one owner at all times; deterministic point-of-last-use deallocation. | **Active** (v0.5+, when `PROVEN`) |
| `ARC` | Shared ownership with deterministic reference counting, supported by acyclic proofs. | **Active** (v0.6+, when `PROVEN`) |
| `WEAK` | Asymmetric cyclic structures broken into acyclic strong ownership via weak back-pointers (Parent, Prev, Owner). | **Active** (v0.7+, when `PROVEN`) |
| `IMMORTAL` | Static/global allocation never freed; allocated once and preserved until process exit. | **Active** (v0.8+, when `PROVEN`) |
| `TRACING_FALLBACK` | Standard Go runtime garbage-collected heap allocation. Safe fallback for all unproven or hazardous sites. | **Active** (Default fallback) |

---

## Language Constructs Analyzed for Stack Proof

The GOX Stack Proof Engine verifies allocations against all 18+ Go language constructs that can invalidate stack placement:

1. **Returned Pointers**: Direct returns, tuple returns, and returned struct fields.
2. **Interface Boxing**: Boxing values into `interface{}`/`any`, which creates runtime type headers and can escape depending on method calls and sinks.
3. **Slices & Backing Arrays**: Slices whose backing store outlives the local frame, slice headers escaped via append or reslicing.
4. **Maps**: `make(map[...])` is inherently runtime heap-allocated in Go.
5. **Channels**: `make(chan ...)` is inherently runtime heap-allocated in Go.
6. **Closures & Variable Capture**: Captured variables residing in shared closure contexts; closures escaped via call or return.
7. **`defer` Statements**: Deferred calls bounded by function exit vs. deferred closures that capture and retain references across returns.
8. **`panic` / `recover`**: Exceptional control flow paths where normal stack unwind could lead to dangling references if caught.
9. **Method Values**: `obj.Method` synthesizes a closure object capturing `obj`.
10. **Variadic Calls**: `args ...T` synthesizes a slice whose lifetime depends on callee retention.
11. **Nested Structures & Deep Indirection**: Pointer fields inside nested structs (e.g., `A.B.C.D.Ptr = p`) followed across任意 levels of indirection.
12. **Generics**: Generic functions instantiated across multiple concrete types whose type parameters may contain pointer/interface types.
13. **Dynamic Calls**: Interface method calls where CHA cannot guarantee closed-world completeness.
14. **Recursion**: Stack growth and unbounded stack frames preventing deterministic frame bounds.
15. **Unknown / External Callees**: Functions without SSA bodies (assembly, cgo, external packages).
16. **Function Pointers**: Indirect function calls whose call targets cannot be resolved to a finite set.
17. **`unsafe.Pointer`**: Arbitrary pointer arithmetic and conversions circumventing type and lifetime safety.
18. **Reflection**: `reflect.ValueOf`, `reflect.MakeSlice`, and dynamic reflection inspections.
19. **Sync Primitives**: `sync.Map`, `sync.Pool`, and atomic operations storing references globally or concurrently.
20. **Concurrency Boundaries**: `go func(...)` launches a concurrent goroutine whose execution outlives the parent frame.
21. **Global Variables**: Storing references into package-level or global variables.

---

## Cross-Validation with Go Escape Analysis

To provide defense-in-depth, GOX v0.3 couples its internal SSA-level proof engine with the Go compiler's escape analysis:

- When analyzing a package, GOX invokes `go build -gcflags='-m -m'` in the target package environment.
- The output is indexed by source position (`file:line`).
- If the Go compiler determines that an allocation site escapes to the heap, GOX will **never** mark it as `PROVEN STACK`, even if SSA heuristic signals are favorable.
- If the Go compiler reports that an allocation does not escape, GOX uses this as a corroborating invariant alongside its own construct checks.
- If compiler escape analysis is unavailable (e.g., offline or partial builds), GOX degrades gracefully by relying strictly on its own SSA invariants.

---

## Invariant-Driven Proof Output

Every `PROVEN` decision carries a machine-checkable list of invariants that collectively justify stack allocation:

```json
{
  "allocation_id": "alloc:fc93596a0dc55c2b",
  "source_position": "/path/to/main.go:13:15",
  "function": "main.localGreeting",
  "strategy": "STACK",
  "confidence": "PROVEN",
  "proof": [
    "All SSA referrers of this allocation remain within the allocating function's scope.",
    "The allocation is SSA-heap but all modeled uses are function-bounded.",
    "No construct (return, goroutine, channel, closure-escape, global, reflection, unsafe, FFI) invalidates stack placement.",
    "The Go compiler's escape analysis confirms this allocation does not escape."
  ]
}
```

If an allocation cannot be proven, explicit `blocking_reasons` document the exact constructs and positions that prevented proof.
