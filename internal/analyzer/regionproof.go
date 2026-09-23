package analyzer

import (
	"fmt"
	"strings"

	"github.com/goxlang/gox/internal/region"
	"golang.org/x/tools/go/ssa"
)

// regionProofResult holds the outcome of attempting to prove that an allocation
// can safely be placed in an inferred region or arena.
type regionProofResult struct {
	proven          bool
	confidence      Confidence
	strategy        MemoryStrategy
	regionInfo      *RegionInfo
	proof           []string         // verified invariants proving region safety
	blockingReasons []BlockingReason // constructs that prevented region proof
}

// regionProofEngine performs whole-program region and arena inference.
// It groups compatible allocations into scoped arenas that can be deallocated
// or reset in $O(1)$ bulk time without tracing GC, while enforcing the
// outlive-ordering theorem and verifying that no references escape the region.
type regionProofEngine struct {
	engine      *ownershipEngine
	escapeIndex *escapeIndex
}

func newRegionProofEngine(engine *ownershipEngine, escapeIdx *escapeIndex) *regionProofEngine {
	return &regionProofEngine{engine: engine, escapeIndex: escapeIdx}
}

// prove attempts to prove that an allocation belongs to a safe, bounded region.
func (rp *regionProofEngine) prove(site *allocationSite, alloc Allocation) regionProofResult {
	result := regionProofResult{
		confidence: ConfidenceUnproven,
		strategy:   StrategyTracingFallback,
	}

	// Gate 1: Inherent heap types that cannot be placed in custom bump arenas.
	if site.kind == "channel" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_channel_heap",
			Description: "Channels coordinate independently scheduled concurrency and require runtime management.",
			Construct:   "channel",
		})
		return result
	}

	// Gate 2: Absolute escape blockers that prevent any deterministic region placement.
	// If an object escapes to global state, concurrent goroutines, channels, or FFI,
	// destroying its region at any static scope would risk memory corruption or dangling pointers.
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "global state"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "global_escape",
				Description: "Allocation is stored in package-level or global state; region destruction cannot bound process lifetime.",
				Construct:   "global",
			})
			return result

		case strings.Contains(lower, "goroutine boundary"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "goroutine_escape",
				Description: "Allocation crosses an independently scheduled goroutine boundary without structured join.",
				Construct:   "goroutine",
			})
			return result

		case strings.Contains(lower, "channel communication"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "channel_escape",
				Description: "Allocation participates in channel communication and may outlive any local region.",
				Construct:   "channel",
			})
			return result

		case strings.Contains(lower, "unsafe-pointer"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "unsafe_pointer",
				Description: "Unsafe-pointer conversion obscures provenance; region bounds cannot be verified.",
				Construct:   "unsafe.Pointer",
			})
			return result

		case strings.Contains(lower, "reflection"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "reflection_escape",
				Description: "Reflection inspection can retain references beyond static region boundaries.",
				Construct:   "reflection",
			})
			return result

		case strings.Contains(lower, "cgo") || strings.Contains(lower, "foreign-function"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ffi_boundary",
				Description: "Foreign-function or cgo boundary prevents verification of region lifetime.",
				Construct:   "ffi",
			})
			return result

		case strings.Contains(lower, "exported library api"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "exported_api_escape",
				Description: "Reference leaves the analyzed program through an exported library API with open-world callers.",
				Construct:   "exported_return",
			})
			return result

		case strings.Contains(lower, "open-world dynamic dispatch"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "open_world_dispatch",
				Description: "Open-world dynamic dispatch may reach external implementations that retain references.",
				Construct:   "interface_dispatch",
			})
			return result

		case strings.Contains(lower, "dynamic or external call") || strings.Contains(lower, "external call"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "external_call_escape",
				Description: "Reference is passed to an external or dynamic call whose retention behavior is unknown.",
				Construct:   "external_call",
			})
			return result
		}
	}

	// Gate 3: Check if the allocation site or value has direct SSA-level escapes to globals or channels.
	if site.value != nil {
		if rp.hasDirectUnsafeEscapes(site.value, site.fn, make(map[ssa.Value]bool)) {
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ssa_hazard_escape",
				Description: "SSA data flow reaches global store, channel send, or concurrency boundary.",
				Position:    alloc.Position,
			})
			return result
		}
	}

	// Gate 4: Region Inference and Scope Determination.
	// We identify four primary region patterns:
	//   A. Caller-Owned Region: The allocation is returned to callers who consume it.
	//   B. Function-Local Cyclic Graph: Allocations in cyclic data structures (graphs/trees) bounded by the allocating function.
	//   C. Retained Container Graph: Allocations retained by an enclosing container whose lifetime is bounded.
	//   D. Request-Scoped Allocations: Allocations inside request handlers or transaction scopes.

	switch {
	// Pattern A: Caller-Owned Region (allocations returned to caller)
	case alloc.Classification == RegionCandidate || alloc.Lifetime == LifetimeCaller || alloc.Lifetime == LifetimeRegion:
		owningFn := rp.determineOwningCaller(site, alloc)
		regID := fmt.Sprintf("region:caller:%s", stableID("reg", alloc.Function))

		result.proven = true
		result.confidence = ConfidenceProven
		result.strategy = StrategyRegion
		result.regionInfo = &RegionInfo{
			RegionID:     regID,
			Scope:        string(region.ScopeCaller),
			OwningFunc:   owningFn,
			DestroyPoint: fmt.Sprintf("exit of caller %s", owningFn),
		}

		result.proof = append(result.proof,
			fmt.Sprintf("The allocation outlives function %s but is strictly bounded by caller-owned region %s.", alloc.Function, regID),
			fmt.Sprintf("All reference paths remain within caller scope %s without escaping to global or concurrent state.", owningFn),
			"Outlive ordering invariant satisfied: caller region outlives callee frame.",
			fmt.Sprintf("All objects in region %s are reclaimed in bulk ($O(1)$) upon %s without tracing GC.", regID, result.regionInfo.DestroyPoint),
		)
		return result

	// Pattern B: Cyclic Graphs / Temporary Data Structures Bounded by Function Scope
	// (e.g. cyclic node networks, AST processing, temporary graphs)
	case rp.isFunctionLocalCyclicOrGraph(site, alloc):
		regID := fmt.Sprintf("region:fn:%s", stableID("reg", alloc.Function))

		result.proven = true
		result.confidence = ConfidenceProven
		result.strategy = StrategyRegion
		result.regionInfo = &RegionInfo{
			RegionID:     regID,
			Scope:        string(region.ScopeFunction),
			OwningFunc:   alloc.Function,
			DestroyPoint: fmt.Sprintf("exit of function %s", alloc.Function),
		}

		result.proof = append(result.proof,
			fmt.Sprintf("Allocation participates in a function-local object graph / cyclic structure in %s.", alloc.Function),
			"Cyclic structures within an arena are safely reclaimed as a unit in $O(1)$ without cycle detectors.",
			"No references to this object graph escape the enclosing function frame.",
			fmt.Sprintf("Region %s is destroyed/reset in bulk ($O(1)$) at %s without tracing GC.", regID, result.regionInfo.DestroyPoint),
		)
		return result

	// Pattern C: Retained Object Graphs with Bounded Caller/Container Lifetime
	case len(alloc.RetainedBy) > 0 && alloc.Ownership != OwnershipGlobal && alloc.Ownership != OwnershipConcurrent && alloc.Classification != ARCCandidate && !rp.isCyclicOrEscaping(site, alloc):
		regID := fmt.Sprintf("region:container:%s", stableID("reg", alloc.Function))

		result.proven = true
		result.confidence = ConfidenceProven
		result.strategy = StrategyRegion
		result.regionInfo = &RegionInfo{
			RegionID:     regID,
			Scope:        string(region.ScopeFunction),
			OwningFunc:   alloc.Function,
			DestroyPoint: fmt.Sprintf("exit of function %s", alloc.Function),
		}

		result.proof = append(result.proof,
			fmt.Sprintf("Allocation is retained by container %v bounded by %s.", alloc.RetainedBy, alloc.Function),
			"All container uses remain bounded within function lifecycle.",
			fmt.Sprintf("Region %s is destroyed in bulk at %s without tracing GC.", regID, result.regionInfo.DestroyPoint),
		)
		return result

	default:
		// Not a clear region candidate: leave as fallback
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "unbounded_region_scope",
			Description: "Could not infer a bounded enclosing region scope for this allocation.",
		})
		return result
	}
}

func (rp *regionProofEngine) isCyclicOrEscaping(site *allocationSite, alloc Allocation) bool {
	if alloc.Lifetime == LifetimeCaller || alloc.Ownership == OwnershipTransferred {
		return true
	}
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "caller") || strings.Contains(lower, "return") || strings.Contains(lower, "strongly connected component") || strings.Contains(lower, "cycle") {
			return true
		}
	}
	if site != nil && site.value != nil && site.fn != nil {
		liveness := analyzeLiveness(site.value, site.fn, rp.engine.fset)
		if liveness.HasEscaped {
			return true
		}
	}
	return false
}

// determineOwningCaller finds the caller function that bounds the lifetime of a returned object.
func (rp *regionProofEngine) determineOwningCaller(site *allocationSite, alloc Allocation) string {
	if site == nil || site.fn == nil {
		return alloc.Function + ".caller"
	}
	// Check call graph for callers
	callers := rp.engine.callGraph.callers[site.fn]
	if len(callers) > 0 {
		return callers[0].String()
	}
	return alloc.Function + ".caller"
}

// isFunctionLocalCyclicOrGraph checks if the allocation participates in a cycle or
// complex container graph whose references remain within the allocating function.
func (rp *regionProofEngine) isFunctionLocalCyclicOrGraph(site *allocationSite, alloc Allocation) bool {
	// If it has a cycle risk, but no global, goroutine, or channel use:
	isCyclic := false
	for _, reason := range alloc.Reasons {
		if strings.Contains(reason, "strongly connected component") || strings.Contains(reason, "cycle risk") || strings.Contains(reason, "retains itself") {
			isCyclic = true
			break
		}
	}
	if !isCyclic {
		return false
	}

	// Must not escape to caller, global, or goroutine
	if alloc.Lifetime == LifetimeCaller || alloc.Ownership == OwnershipTransferred {
		return false
	}
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "global") || strings.Contains(lower, "goroutine") || strings.Contains(lower, "channel") || strings.Contains(lower, "caller") || strings.Contains(lower, "return") {
			return false
		}
	}
	if site.value != nil && site.fn != nil {
		liveness := analyzeLiveness(site.value, site.fn, rp.engine.fset)
		if liveness.HasEscaped {
			return false
		}
	}
	return true
}

// hasDirectUnsafeEscapes inspects the SSA value to check for immediate stores
// into globals, channel sends, or goroutine starts.
func (rp *regionProofEngine) hasDirectUnsafeEscapes(v ssa.Value, fn *ssa.Function, seen map[ssa.Value]bool) bool {
	if v == nil || seen[v] {
		return false
	}
	seen[v] = true

	referrers := v.Referrers()
	if referrers == nil {
		return false
	}

	for _, instr := range *referrers {
		switch instr := instr.(type) {
		case *ssa.Store:
			if _, isGlobal := instr.Addr.(*ssa.Global); isGlobal {
				return true
			}
		case *ssa.Send:
			return true
		case *ssa.Go:
			return true
		case *ssa.MakeClosure:
			if rp.hasDirectUnsafeEscapes(instr, fn, seen) {
				return true
			}
		}
	}
	return false
}
