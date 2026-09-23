# GOX v0.2 ownership and lifetime engine

## Safety contract

GOX v0.2 observes programs but never changes their memory behavior. Its output is evidence for future compiler work, not a deallocation proof. A later code generator must independently validate every proposed stack, region, or ARC transformation. Missing evidence always moves an allocation toward `UNKNOWN` or `RUNTIME_FALLBACK`.

## Pipeline

```text
Go modules and packages
        ↓
Typed syntax and instantiated Go SSA
        ↓
CHA whole-program call graph
        ↓
Allocation identity and alias graph
        ↓
Interprocedural parameter/return propagation
        ↓
Container, closure, concurrency, and boundary facts
        ↓
Safety-ordered ownership/lifetime classification
        ↓
Text, JSON, trace, and DOT/JSON graph reports
```

Allocation IDs hash package path, function identity, package-relative source position, allocation kind, and function-local ordinal. They remain stable across checkout locations when source layout is unchanged.

## Call and lifetime propagation

Class-hierarchy analysis provides a sound but deliberately imprecise call graph over the SSA program. Static calls map arguments directly to parameters. Interface and function-value calls retain all CHA targets. In open-world mode, dynamic dispatch still requires fallback because another implementation may exist outside the loaded program.

When a tracked identity reaches a return instruction, GOX finds every incoming call-graph edge, maps the relevant result or tuple extraction into the caller, and continues reference traversal. This repeats until the identity is consumed, retained, escapes, or reaches a previously visited SSA value. The reported caller depth describes how many return boundaries were crossed.

The analysis is context-insensitive. Passing different allocations through the same parameter or return can merge facts and cause conservative false fallbacks. It must not cause an unsafe optimistic classification.

## Alias and retention model

Identity-preserving SSA operations include field and index addresses, slicing, type/interface changes, pointer conversions, phi nodes, assertions, and tuple extraction. Loads do not make the containing allocation an alias of a loaded field.

Stores and container operations create retention edges:

- struct, array, and slice-address stores use `container_store`;
- map keys and values use `map_entry`;
- appended elements use `append_element`;
- captured values use `closure_capture`.

The child inherits hazards discovered while walking the container. GOX runs Tarjan strongly connected component analysis over allocation-retention edges; self cycles and multi-allocation cycles fall back. Field sensitivity and path-sensitive mutation histories remain future work.

## Boundary rules

The following facts force runtime fallback:

- process-global retention;
- goroutine transfer or channel communication;
- reflection or unsafe-pointer provenance loss;
- cgo, plugins, low-level syscall trampolines, or recognized runtime FFI;
- external functions without SSA bodies;
- unresolved calls and open-world dynamic dispatch;
- exported library returns whose callers are not closed;
- recovered panic values;
- detected self cycles.

Channels themselves always fall back in v0.2. Defers are function-bounded unless their callee introduces another hazard.

## Classification order

1. Channels and FFI.
2. Unsafe, reflection, globals, goroutines, channel transfer, and cycles.
3. Exported open-world returns, dynamic calls, and external calls.
4. Unmodeled SSA operations.
5. Returned retained object graphs as region candidates.
6. Other retained objects or closures as ARC candidates.
7. Returned unique values as region candidates.
8. Frame-local values as stack-safe.
9. Locally bounded SSA heap values as potentially stack-safe.

This ordering ensures a known hazard dominates a weaker local-lifetime observation.

## Research gates before code generation

Do not begin allocation rewriting until representative application corpora demonstrate:

1. zero manually confirmed false-safe results;
2. stable allocation identities and classifications across repeated builds;
3. understood reasons for dominant fallback and unknown categories;
4. allocation-frequency and byte-weighted coverage, not merely site coverage;
5. acceptable cold and warm analysis latency and memory;
6. a field-sensitive heap graph with general cycle detection;
7. explicit summaries for audited standard-library, assembly, runtime, and FFI calls;
8. path-sensitive proof obligations that a code generator can revalidate.

Phase 3 should remain hybrid. Stack, region, and ARC strategies must coexist with tracing runtime fallback, and any failed proof must preserve ordinary Go semantics.
