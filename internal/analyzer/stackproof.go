package analyzer

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// stackProofResult holds the outcome of attempting to prove an allocation
// is safely placeable on the stack.
type stackProofResult struct {
	proven          bool
	confidence      Confidence
	proof           []string         // invariants that collectively prove stack safety
	blockingReasons []BlockingReason // constructs that prevented proof
}

// stackProofEngine attempts to prove that an allocation's lifetime is
// completely bounded by its enclosing function frame. It checks every Go
// construct that could invalidate stack placement and produces either a
// proof (a list of verified invariants) or blocking reasons explaining
// why the proof failed.
//
// The engine is deliberately conservative: any construct it cannot fully
// model results in an UNPROVEN decision with an explicit blocking reason.
type stackProofEngine struct {
	engine      *ownershipEngine
	escapeIndex *escapeIndex
}

func newStackProofEngine(engine *ownershipEngine, escapeIdx *escapeIndex) *stackProofEngine {
	return &stackProofEngine{engine: engine, escapeIndex: escapeIdx}
}

// prove attempts to prove that the given allocation can safely live on the stack.
func (sp *stackProofEngine) prove(site *allocationSite, alloc Allocation) stackProofResult {
	result := stackProofResult{confidence: ConfidenceUnproven}

	// Gate 1: If the Go SSA builder already places it on the stack (ssaHeap==false),
	// the Go compiler itself considers this frame-local. This is a strong signal.
	if !site.ssaHeap {
		result.proven = true
		result.confidence = ConfidenceProven
		result.proof = append(result.proof,
			"The Go SSA builder represents this allocation as frame-local storage (Alloc.Heap=false).",
			"All SSA referrers remain within the enclosing function frame.",
		)
		// Even for SSA-stack allocations, verify no construct invalidates this.
		blockers := sp.checkConstructs(site, alloc)
		if len(blockers) > 0 {
			// The Go compiler said stack but our analysis found concerns.
			// Be conservative: downgrade to PROBABLE.
			result.proven = false
			result.confidence = ConfidenceProbable
			result.blockingReasons = blockers
			result.proof = append(result.proof,
				"WARNING: SSA says stack-local but GOX found potential escape constructs. Downgrading to PROBABLE.",
			)
		}
		// Also perform direct SSA escape check: the SSA stack flag only means
		// the Alloc itself is frame-local, but the VALUE stored through it
		// (for captured variables, address-taken locals) can still escape via
		// return, goroutine, channel, or global store.
		if result.proven && site.value != nil {
			seen := make(map[ssa.Value]bool)
			if sp.valueEscapesFrame(site.value, site.fn, seen) {
				result.proven = false
				result.confidence = ConfidenceProbable
				result.blockingReasons = append(result.blockingReasons, BlockingReason{
					Kind:        "ssa_stack_value_escapes",
					Description: "SSA marks the allocation as stack-local, but references stored through it escape the function frame.",
					Position:    alloc.Position,
					Construct:   "ssa_referrer",
				})
				result.proof = append(result.proof,
					"WARNING: SSA says stack-local but stored references escape the frame. Downgrading to PROBABLE.",
				)
			}
		}
		return result
	}

	// Gate 2: Maps and channels are always heap-allocated in Go.
	if site.kind == "map" || site.kind == "channel" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_heap",
			Description: fmt.Sprintf("%s allocations are always heap-resident in the Go runtime.", site.kind),
			Construct:   site.kind,
		})
		return result
	}

	// Gate 3: Check the v0.2 classification. If it found fallback-level hazards,
	// we cannot override that with a stack proof.
	if alloc.Classification == RuntimeFallback || alloc.Classification == Unknown {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "v02_fallback",
			Description: fmt.Sprintf("v0.2 classified this as %s; stack proof requires no fallback-level hazards.", alloc.Classification),
		})
		for _, reason := range alloc.Reasons {
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "v02_reason",
				Description: reason,
			})
		}
		return result
	}

	// Gate 4: Cross-validate with Go compiler escape analysis.
	if escInfo := sp.escapeIndex.lookup(alloc.Position); escInfo != nil {
		if escInfo.escapes {
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "compiler_escape",
				Description: "The Go compiler's escape analysis reports this allocation escapes to heap.",
				Position:    alloc.Position,
			})
			for _, reason := range escInfo.reasons {
				if strings.Contains(strings.ToLower(reason), "escap") || strings.Contains(strings.ToLower(reason), "leak") || strings.Contains(strings.ToLower(reason), "moved") {
					result.blockingReasons = append(result.blockingReasons, BlockingReason{
						Kind:        "compiler_detail",
						Description: reason,
						Position:    alloc.Position,
					})
				}
			}
			return result
		}
	}

	// Gate 5: Check all 18+ constructs that can invalidate stack placement.
	blockers := sp.checkConstructs(site, alloc)
	if len(blockers) > 0 {
		result.blockingReasons = blockers
		return result
	}

	// Gate 5.5: Direct SSA-level escape verification.
	// The construct checker above inspects v0.2 reason text, but some escape
	// patterns (e.g., a pointer that is both deferred and returned) may not
	// surface in reason text. Perform a direct SSA referrer walk to verify
	// the allocation truly does not escape its frame.
	if site.value != nil {
		seen := make(map[ssa.Value]bool)
		if sp.valueEscapesFrame(site.value, site.fn, seen) {
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ssa_escape_detected",
				Description: "Direct SSA referrer analysis detected that a reference escapes the function frame.",
				Position:    alloc.Position,
				Construct:   "ssa_referrer",
			})
			return result
		}
	}


	// Gate 6: If the v0.2 classification is POTENTIALLY_STACK_SAFE or STACK_SAFE,
	// and all construct checks pass, and the compiler agrees (or has no opinion),
	// we can prove stack safety.
	if alloc.Classification == StackSafe || alloc.Classification == PotentialStack {
		result.proven = true
		result.confidence = ConfidenceProven
		result.proof = append(result.proof,
			"All SSA referrers of this allocation remain within the allocating function's scope.",
		)
		if alloc.Classification == PotentialStack {
			result.proof = append(result.proof,
				"The allocation is SSA-heap but all modeled uses are function-bounded.",
				"No construct (return, goroutine, channel, closure-escape, global, reflection, unsafe, FFI) invalidates stack placement.",
			)
		}
		if escInfo := sp.escapeIndex.lookup(alloc.Position); escInfo != nil && !escInfo.escapes {
			result.proof = append(result.proof,
				"The Go compiler's escape analysis confirms this allocation does not escape.",
			)
		}
		if alloc.Ownership == OwnershipBorrowed {
			result.proof = append(result.proof,
				"References are borrowed by callees that do not retain them beyond their own frame.",
			)
		}
		return result
	}

	// For REGION_CANDIDATE and ARC_CANDIDATE in v0.3: not stack-provable.
	if alloc.Classification == RegionCandidate {
		result.confidence = ConfidenceProbable
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "region_candidate",
			Description: "The allocation crosses return boundaries and may require a caller-owned region (v0.4).",
			Construct:   "return",
		})
		return result
	}
	if alloc.Classification == ARCCandidate {
		result.confidence = ConfidenceProbable
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "arc_candidate",
			Description: "The allocation has shared ownership and may require reference counting (v0.6).",
			Construct:   "shared_ownership",
		})
		return result
	}

	result.blockingReasons = append(result.blockingReasons, BlockingReason{
		Kind:        "unclassified",
		Description: fmt.Sprintf("Allocation classification %q does not match any stack-proof path.", alloc.Classification),
	})
	return result
}

// checkConstructs examines every Go construct that can invalidate stack placement.
// It returns a slice of blocking reasons; an empty slice means no blocking constructs.
func (sp *stackProofEngine) checkConstructs(site *allocationSite, alloc Allocation) []BlockingReason {
	var blockers []BlockingReason

	// Collect flow facts about this allocation by examining the v0.2 analysis results.
	// We reconstruct the key facts from the Allocation's fields and reasons.
	reasonText := strings.Join(alloc.Reasons, " ")
	limitText := strings.Join(alloc.Limitations, " ")

	// 1. Returned pointers
	if alloc.CallerDepth > 0 {
		blockers = append(blockers, BlockingReason{
			Kind:        "returned_pointer",
			Description: fmt.Sprintf("Allocation escapes through %d return boundary(ies).", alloc.CallerDepth),
			Construct:   "return",
			Position:    alloc.Position,
		})
	}

	// 2. Interface boxing (check SSA referrers for MakeInterface)
	if sp.hasSSAUse(site, isMakeInterface) {
		if sp.interfaceEscapes(site) {
			blockers = append(blockers, BlockingReason{
				Kind:        "interface_escape",
				Description: "The allocation is boxed in an interface that may escape the function frame.",
				Construct:   "interface",
				Position:    alloc.Position,
			})
		}
	}

	// 3. Closure capture
	if strings.Contains(reasonText, "closure") || strings.Contains(reasonText, "captured") {
		if alloc.Classification != StackSafe && alloc.Classification != PotentialStack {
			blockers = append(blockers, BlockingReason{
				Kind:        "closure_capture",
				Description: "The allocation is captured by a closure whose lifetime may exceed the function frame.",
				Construct:   "closure",
				Position:    alloc.Position,
			})
		}
	}

	// 4. Goroutine use
	if strings.Contains(reasonText, "goroutine") {
		blockers = append(blockers, BlockingReason{
			Kind:        "goroutine_escape",
			Description: "The allocation crosses a goroutine boundary with an independently scheduled lifetime.",
			Construct:   "goroutine",
			Position:    alloc.Position,
		})
	}

	// 5. Channel use
	if strings.Contains(reasonText, "channel") && alloc.Lifetime == LifetimeChannel {
		blockers = append(blockers, BlockingReason{
			Kind:        "channel_escape",
			Description: "The allocation is sent through a channel and may outlive the sender's frame.",
			Construct:   "channel",
			Position:    alloc.Position,
		})
	}

	// 6. Global store
	if alloc.Ownership == OwnershipGlobal || alloc.Lifetime == LifetimeGlobal {
		blockers = append(blockers, BlockingReason{
			Kind:        "global_store",
			Description: "The allocation is stored in global state and has process lifetime.",
			Construct:   "global",
			Position:    alloc.Position,
		})
	}

	// 7. Reflection
	if strings.Contains(reasonText, "reflect") || strings.Contains(reasonText, "Reflection") {
		blockers = append(blockers, BlockingReason{
			Kind:        "reflection_use",
			Description: "Reflection can retain or reproduce references outside the visible SSA flow.",
			Construct:   "reflection",
			Position:    alloc.Position,
		})
	}

	// 8. unsafe.Pointer
	if strings.Contains(reasonText, "unsafe") || strings.Contains(reasonText, "Unsafe") {
		blockers = append(blockers, BlockingReason{
			Kind:        "unsafe_use",
			Description: "unsafe.Pointer conversion obscures aliasing and object provenance.",
			Construct:   "unsafe",
			Position:    alloc.Position,
		})
	}

	// 9. FFI / cgo
	if strings.Contains(reasonText, "cgo") || strings.Contains(reasonText, "FFI") || strings.Contains(reasonText, "foreign") {
		blockers = append(blockers, BlockingReason{
			Kind:        "ffi_boundary",
			Description: "The allocation crosses a foreign-function interface boundary.",
			Construct:   "cgo",
			Position:    alloc.Position,
		})
	}

	// 10. Defer (defer is frame-bounded but can complicate stack placement if
	//     the deferred function retains the pointer beyond its invocation)
	if strings.Contains(reasonText, "Deferred") || strings.Contains(reasonText, "deferred") {
		// Defer is bounded by function exit, so it does NOT block stack placement
		// unless it captures into an escaping closure. The v0.2 analysis already
		// handles this case — if defer introduces an escape, it shows up in
		// other categories. So this is informational, not blocking.
	}

	// 11. Panic/recover
	if strings.Contains(reasonText, "panic") || strings.Contains(reasonText, "recover") {
		blockers = append(blockers, BlockingReason{
			Kind:        "panic_recover",
			Description: "Panic/recover creates an implicit retention path that may escape the frame.",
			Construct:   "panic",
			Position:    alloc.Position,
		})
	}

	// 12. Dynamic dispatch
	if strings.Contains(reasonText, "dynamic") || strings.Contains(reasonText, "dispatch") {
		if alloc.Classification == RuntimeFallback {
			blockers = append(blockers, BlockingReason{
				Kind:        "dynamic_dispatch",
				Description: "Dynamic dispatch may reach implementations outside the analyzed call graph.",
				Construct:   "interface_call",
				Position:    alloc.Position,
			})
		}
	}

	// 13. External calls without SSA bodies
	if strings.Contains(reasonText, "external call") || strings.Contains(reasonText, "no analyzable") {
		blockers = append(blockers, BlockingReason{
			Kind:        "external_call",
			Description: "The allocation is passed to an external function without an analyzable SSA body.",
			Construct:   "external_call",
			Position:    alloc.Position,
		})
	}

	// 14. Recursion (check if function appears in its own call graph)
	if sp.isSelfRecursive(site) {
		// Recursion doesn't inherently prevent stack placement unless the allocation
		// escapes through the recursive call. If v0.2 didn't flag it, it's okay.
		// But we note it for transparency.
	}

	// 15. Heap storage (retained by other heap objects)
	if len(alloc.RetainedBy) > 0 {
		blockers = append(blockers, BlockingReason{
			Kind:        "heap_retained",
			Description: fmt.Sprintf("The allocation is retained by %d other heap object(s).", len(alloc.RetainedBy)),
			Construct:   "container",
			Position:    alloc.Position,
		})
	}

	// 16. Cycle risk
	if strings.Contains(reasonText, "cycle") || strings.Contains(limitText, "cycle") {
		blockers = append(blockers, BlockingReason{
			Kind:        "cycle_risk",
			Description: "The allocation participates in a retention cycle.",
			Construct:   "cycle",
			Position:    alloc.Position,
		})
	}

	// 17. Exported open-world return
	if strings.Contains(reasonText, "exported") || strings.Contains(reasonText, "library API") {
		blockers = append(blockers, BlockingReason{
			Kind:        "exported_return",
			Description: "The allocation may leave the analyzed program through an exported API.",
			Construct:   "export",
			Position:    alloc.Position,
		})
	}

	return blockers
}

// hasSSAUse checks whether the allocation site's SSA value has any referrer
// matching the given predicate.
func (sp *stackProofEngine) hasSSAUse(site *allocationSite, pred func(ssa.Instruction) bool) bool {
	if site == nil || site.value == nil {
		return false
	}
	refs := site.value.Referrers()
	if refs == nil {
		return false
	}
	seen := make(map[ssa.Value]bool)
	return sp.hasSSAUseRecursive(site.value, pred, seen)
}

func (sp *stackProofEngine) hasSSAUseRecursive(value ssa.Value, pred func(ssa.Instruction) bool, seen map[ssa.Value]bool) bool {
	if value == nil || seen[value] {
		return false
	}
	seen[value] = true
	refs := value.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		if pred(ref) {
			return true
		}
		if derived, ok := ref.(ssa.Value); ok {
			if sp.hasSSAUseRecursive(derived, pred, seen) {
				return true
			}
		}
	}
	return false
}

// interfaceEscapes checks if an interface-boxed value escapes the function frame.
func (sp *stackProofEngine) interfaceEscapes(site *allocationSite) bool {
	if site == nil || site.value == nil {
		return true // conservative
	}
	seen := make(map[ssa.Value]bool)
	return sp.valueEscapesFrame(site.value, site.fn, seen)
}

// valueEscapesFrame returns true if the value may escape the given function's frame.
func (sp *stackProofEngine) valueEscapesFrame(value ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) bool {
	if value == nil || seen[value] {
		return false
	}
	seen[value] = true
	refs := value.Referrers()
	if refs == nil {
		return true // conservative: no referrer info means we can't prove containment
	}
	for _, ref := range *refs {
		switch use := ref.(type) {
		case *ssa.Return:
			return true
		case *ssa.Store:
			if use.Val == value {
				if derivedFromGlobal(use.Addr, make(map[ssa.Value]bool)) {
					return true
				}
			}
		case *ssa.Send:
			return true
		case *ssa.Go:
			return true
		case *ssa.Panic:
			return true
		case *ssa.MakeClosure:
			// If the closure itself escapes, the captured value escapes.
			if sp.valueEscapesFrame(use, fn, seen) {
				return true
			}
		case *ssa.MakeInterface:
			if sp.valueEscapesFrame(use, fn, seen) {
				return true
			}
		case *ssa.Phi:
			if sp.valueEscapesFrame(use, fn, seen) {
				return true
			}
		case *ssa.ChangeType, *ssa.Convert, *ssa.ChangeInterface:
			if derived, ok := ref.(ssa.Value); ok {
				if sp.valueEscapesFrame(derived, fn, seen) {
					return true
				}
			}
		case *ssa.TypeAssert, *ssa.Extract, *ssa.FieldAddr, *ssa.IndexAddr, *ssa.Slice:
			if derived, ok := ref.(ssa.Value); ok {
				if sp.valueEscapesFrame(derived, fn, seen) {
					return true
				}
			}
		case *ssa.Call:
			// If passed to a call, check if the callee might retain it.
			// Conservative: if it's passed as an argument and the call result
			// escapes, consider it escaping.
			common := use.Common()
			if common != nil {
				for _, arg := range common.Args {
					if arg == value || derivedFrom(arg, value, make(map[ssa.Value]bool)) {
						// The value is passed as an argument. If the call is to
						// a function we've already analysed and it didn't cause
						// fallback, it's okay. Otherwise, be conservative.
						if common.StaticCallee() == nil {
							return true
						}
						callee := common.StaticCallee()
						if callee != nil && len(callee.Blocks) == 0 {
							return true // external function
						}
					}
				}
			}
		case *ssa.MapUpdate:
			return true
		case *ssa.DebugRef:
			continue
		case *ssa.Field, *ssa.Index, *ssa.UnOp:
			// Loading from doesn't cause escape
		case *ssa.Lookup, *ssa.Range, *ssa.Next, *ssa.If, *ssa.Jump, *ssa.RunDefers:
			// Consuming operations
		case *ssa.Defer:
			// Defer is bounded by function exit; doesn't cause escape by itself.
			// But the deferred call might retain the value.
			common := use.Common()
			if common != nil && common.StaticCallee() == nil {
				return true
			}
		}
	}
	return false
}

// isMakeInterface returns true if the instruction is an ssa.MakeInterface.
func isMakeInterface(instr ssa.Instruction) bool {
	_, ok := instr.(*ssa.MakeInterface)
	return ok
}

// isSelfRecursive checks if the function containing this allocation calls itself.
func (sp *stackProofEngine) isSelfRecursive(site *allocationSite) bool {
	if site == nil || site.fn == nil {
		return false
	}
	callers := sp.engine.callGraph.callers[site.fn]
	for _, caller := range callers {
		if caller.Parent() == site.fn {
			return true
		}
	}
	return false
}
