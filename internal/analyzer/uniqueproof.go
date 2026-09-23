package analyzer

import (
	"fmt"
	"strings"
)

// uniqueProofResult holds the outcome of attempting to prove deterministic
// unique-owner destruction for an allocation.
type uniqueProofResult struct {
	proven            bool
	confidence        Confidence
	strategy          MemoryStrategy
	destructionPoints []DestructionPoint
	proof             []string         // verified invariants proving unique ownership safety
	blockingReasons   []BlockingReason // constructs preventing unique-owner proof
}

// uniqueProofEngine performs CFG-level ownership tracking and deterministic
// destruction point insertion for uniquely owned objects.
type uniqueProofEngine struct {
	engine      *ownershipEngine
	escapeIndex *escapeIndex
}

func newUniqueProofEngine(engine *ownershipEngine, escapeIdx *escapeIndex) *uniqueProofEngine {
	return &uniqueProofEngine{engine: engine, escapeIndex: escapeIdx}
}

// prove attempts to prove that an allocation is uniquely owned and can be
// deterministically deallocated at its point of last use.
func (up *uniqueProofEngine) prove(site *allocationSite, alloc Allocation) uniqueProofResult {
	result := uniqueProofResult{
		confidence: ConfidenceUnproven,
		strategy:   StrategyTracingFallback,
	}

	// Gate 1: Check if the allocation has inherent or fatal escape hazards
	if site.kind == "channel" || site.kind == "map" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_heap_type",
			Description: fmt.Sprintf("%s types require runtime management and cannot be deterministically freed at last use.", site.kind),
			Construct:   site.kind,
		})
		return result
	}

	// Gate 2: Absolute escape blockers
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "global state"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "global_escape",
				Description: "Allocation is stored in global state; ownership is not unique.",
				Construct:   "global",
			})
			return result

		case strings.Contains(lower, "goroutine boundary"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "goroutine_escape",
				Description: "Allocation crosses concurrency boundary; cannot deterministically free without synchronization.",
				Construct:   "goroutine",
			})
			return result

		case strings.Contains(lower, "channel communication"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "channel_escape",
				Description: "Allocation participates in channel communication; ownership is transferred across goroutines.",
				Construct:   "channel",
			})
			return result

		case strings.Contains(lower, "unsafe-pointer"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "unsafe_pointer",
				Description: "Unsafe-pointer conversion obscures ownership provenance.",
				Construct:   "unsafe.Pointer",
			})
			return result

		case strings.Contains(lower, "reflection"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "reflection_escape",
				Description: "Reflection inspection can retain references beyond deterministic scope.",
				Construct:   "reflection",
			})
			return result

		case strings.Contains(lower, "cgo") || strings.Contains(lower, "foreign-function"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ffi_boundary",
				Description: "FFI boundary prevents verifying point of last use.",
				Construct:   "ffi",
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

	// Gate 3: Ownership check
	// Allocation must be uniquely owned or borrowed without shared retention
	if len(alloc.RetainedBy) > 0 {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "retained_in_container",
			Description: "Allocation is retained inside another object or container; ownership is not unique.",
		})
		return result
	}

	if alloc.Ownership != OwnershipUnique && alloc.Ownership != OwnershipBorrowed {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "non_unique_ownership",
			Description: fmt.Sprintf("Allocation ownership is %s; deterministic last-use destruction requires unique ownership.", alloc.Ownership),
		})
		return result
	}

	// Gate 4: CFG Liveness and Point of Last Use Analysis
	if site.value == nil || site.fn == nil {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "no_ssa_value",
			Description: "No SSA value available to perform CFG liveness analysis.",
		})
		return result
	}

	liveness := analyzeLiveness(site.value, site.fn, up.engine.fset)
	if liveness.HasEscaped {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "returned_escape",
			Description: "Allocation escapes the function frame via return statement; ownership transfers to caller.",
			Construct:   "return",
		})
		return result
	}

	if !liveness.IsSingleOwner || liveness.HasConcurrentUse {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "shared_aliasing_conflict",
			Description: "Liveness analysis detected concurrent or conflicting aliases across CFG paths.",
		})
		return result
	}

	if len(liveness.DestructionPoints) == 0 {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "no_destruction_point",
			Description: "Could not find a sound point of last use along all control-flow paths.",
		})
		return result
	}

	// Unique ownership proof established!
	result.proven = true
	result.confidence = ConfidenceProven
	result.strategy = StrategyUniqueOwned
	result.destructionPoints = liveness.DestructionPoints

	result.proof = append(result.proof,
		fmt.Sprintf("Allocation %s maintains unique ownership in function %s.", alloc.ID, alloc.Function),
		"Internal move/borrow semantics validated: no simultaneous active aliases.",
		fmt.Sprintf("Found %d deterministic destruction point(s) along all reachable CFG exit paths.", len(liveness.DestructionPoints)),
		"Zero use-after-free: value is strictly unreferenced after destruction points.",
	)

	for _, dp := range liveness.DestructionPoints {
		result.proof = append(result.proof,
			fmt.Sprintf("Deterministic destruction [%s] at %s (block %d).", dp.Kind, dp.Position, dp.BlockID),
		)
	}

	return result
}
