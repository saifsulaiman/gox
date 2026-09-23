package analyzer

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// arcProofResult holds the outcome of attempting to prove deterministic
// reference-counted management for an allocation.
type arcProofResult struct {
	proven          bool
	confidence      Confidence
	strategy        MemoryStrategy
	arcInfo         *ARCInfo
	proof           []string
	blockingReasons []BlockingReason
}

// arcProofEngine performs static cycle detection, retain/release placement,
// and borrow elision for reference-counted allocations.
type arcProofEngine struct {
	engine        *ownershipEngine
	cycleAnalyzer *CycleAnalyzer
}

func newARCProofEngine(engine *ownershipEngine) *arcProofEngine {
	return &arcProofEngine{
		engine:        engine,
		cycleAnalyzer: newCycleAnalyzer(),
	}
}

// prove evaluates whether an allocation can safely use Automatic Reference Counting (ARC).
func (ap *arcProofEngine) prove(site *allocationSite, alloc Allocation) arcProofResult {
	result := arcProofResult{
		confidence: ConfidenceUnproven,
		strategy:   StrategyTracingFallback,
	}

	// Gate 1: Inherent heap types that cannot be reference counted
	if site.kind == "channel" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_channel_heap",
			Description: "Channels coordinate independently scheduled concurrency and require runtime management.",
			Construct:   "channel",
		})
		return result
	}

	// Gate 2: Absolute escape blockers
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "global state"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "global_store",
				Description: "Allocation is stored in global state; ARC cannot track indefinite process lifetime.",
				Construct:   "global",
			})
			return result

		case strings.Contains(lower, "unsafe-pointer"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "unsafe_pointer",
				Description: "Unsafe-pointer conversion obscures reference counting provenance.",
				Construct:   "unsafe.Pointer",
			})
			return result

		case strings.Contains(lower, "reflection"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "reflection_escape",
				Description: "Reflection inspection can retain references outside visible ARC retain/release tracking.",
				Construct:   "reflection",
			})
			return result

		case strings.Contains(lower, "cgo") || strings.Contains(lower, "foreign-function"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ffi_boundary",
				Description: "FFI boundary prevents tracking external reference counts.",
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

		case strings.Contains(lower, "channel communication") || strings.Contains(lower, "channel"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "channel_escape",
				Description: "Allocation participates in channel communication and requires runtime tracing GC.",
				Construct:   "channel",
			})
			return result

		case strings.Contains(lower, "open-world dynamic dispatch"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "open_world_dispatch",
				Description: "Open-world dynamic dispatch may reach unknown implementations that leak or duplicate references.",
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

	// Gate 3: Cycle Safety Proof
	// ARC cannot collect cyclic data structures without complex cycle collectors.
	// Therefore, an allocation can only use ARC if its type graph is proven acyclic.
	if site.value == nil {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "no_ssa_value",
			Description: "No SSA value available to verify acyclic type properties.",
		})
		return result
	}

	acyclicProof := ap.cycleAnalyzer.ProveAcyclic(site.value.Type(), site, alloc)
	if !acyclicProof.IsAcyclic {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "cycle_risk",
			Description: acyclicProof.Reason,
			Construct:   "cycle",
		})
		return result
	}

	// Gate 4: Determine Atomic vs Non-Atomic ARC
	isAtomic := false
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "goroutine") || strings.Contains(lower, "concurrent") {
			isAtomic = true
			break
		}
	}
	if alloc.Ownership == OwnershipConcurrent {
		isAtomic = true
	}

	// Gate 5: Placement of Retain and Release Operations
	var rawRetains []ARCSite
	var rawReleases []ARCSite

	// Scan referrers for retain placement
	if refs := site.value.Referrers(); refs != nil {
		for _, ref := range *refs {
			var blockID int
			var posStr string
			var instrStr string
			var varName string

			if ref != nil {
				if ref.Block() != nil {
					blockID = ref.Block().Index
				}
				posStr = positionString(ap.engine.fset, ref.Pos())
				instrStr = ref.String()
			}

			switch r := ref.(type) {
			case *ssa.Store:
				varName = r.Addr.Name()
				rawRetains = append(rawRetains, ARCSite{
					Kind:        "RETAIN",
					Position:    posStr,
					Instruction: instrStr,
					BlockID:     blockID,
					Variable:    varName,
				})
			case *ssa.Return:
				rawRetains = append(rawRetains, ARCSite{
					Kind:        "RETAIN",
					Position:    posStr,
					Instruction: instrStr,
					BlockID:     blockID,
					Variable:    "return",
				})
			case ssa.CallInstruction:
				rawRetains = append(rawRetains, ARCSite{
					Kind:        "RETAIN",
					Position:    posStr,
					Instruction: instrStr,
					BlockID:     blockID,
					Variable:    "call_arg",
				})
			}
		}
	}

	// Scan liveness for release placement
	liveness := analyzeLiveness(site.value, site.fn, ap.engine.fset)
	for _, dp := range liveness.DestructionPoints {
		rawReleases = append(rawReleases, ARCSite{
			Kind:        "RELEASE",
			Position:    dp.Position,
			Instruction: dp.Instruction,
			BlockID:     dp.BlockID,
			Variable:    site.value.Name(),
		})
	}

	// Apply Swift-style borrow elision
	optRetains, optReleases, elidedCount := optimizeARCSites(rawRetains, rawReleases)

	// Build ARCInfo
	arcInfo := &ARCInfo{
		RetainSites:  optRetains,
		ReleaseSites: optReleases,
		AcyclicProof: acyclicProof.Invariants,
		IsAtomic:     isAtomic,
		ElidedSites:  elidedCount,
	}

	// ARC Proof successfully established!
	result.proven = true
	result.confidence = ConfidenceProven
	result.strategy = StrategyARC
	result.arcInfo = arcInfo

	// Proof invariants
	result.proof = append(result.proof, acyclicProof.Invariants...)
	if isAtomic {
		result.proof = append(result.proof, "Atomic reference counting enabled: reference crosses goroutine boundaries safely.")
	} else {
		result.proof = append(result.proof, "Non-atomic reference counting enabled: reference is strictly goroutine-confined (zero atomic overhead).")
	}

	if elidedCount > 0 {
		result.proof = append(result.proof, fmt.Sprintf("Swift-style borrow elision eliminated %d redundant retain/release pair(s).", elidedCount))
	}

	activeRetains := 0
	for _, r := range optRetains {
		if r.Kind == "RETAIN" {
			activeRetains++
		}
	}
	activeReleases := 0
	for _, r := range optReleases {
		if r.Kind == "RELEASE" {
			activeReleases++
		}
	}

	result.proof = append(result.proof,
		fmt.Sprintf("Placed %d active retain site(s) and %d active release site(s) in CFG.", activeRetains, activeReleases),
		"Deterministic ARC deallocation occurs immediately when reference count reaches zero.",
	)

	return result
}
