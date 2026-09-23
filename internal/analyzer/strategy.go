package analyzer

import (
	"cmp"
	"slices"
	"time"
)

// strategyEngine consumes v0.2 classifications, stack proof results,
// region proof results, and unique-owner proof results to produce an
// AllocationDecision for every allocation site.
type strategyEngine struct {
	stackProof  *stackProofEngine
	regionProof *regionProofEngine
	uniqueProof *uniqueProofEngine
	arcProof      *arcProofEngine
	weakProof     *weakProofEngine
	immortalProof *immortalProofEngine
}

func newStrategyEngine(stackProof *stackProofEngine, immortalProof *immortalProofEngine, regionProof *regionProofEngine, uniqueProof *uniqueProofEngine, arcProof *arcProofEngine, weakProof *weakProofEngine) *strategyEngine {
	return &strategyEngine{
		stackProof:    stackProof,
		immortalProof: immortalProof,
		regionProof:   regionProof,
		uniqueProof:   uniqueProof,
		arcProof:      arcProof,
		weakProof:     weakProof,
	}
}

// decide produces AllocationDecisions for all allocations.
func (se *strategyEngine) decide(allocations []Allocation, sites []*allocationSite) ([]AllocationDecision, *StrategySummary) {
	started := time.Now()

	// Build a map from allocation ID to site for SSA-level checks.
	siteByID := make(map[string]*allocationSite, len(sites))
	for _, site := range sites {
		siteByID[site.id] = site
	}

	decisions := make([]AllocationDecision, 0, len(allocations))
	summary := &StrategySummary{
		ByStrategy:   make(map[MemoryStrategy]int),
		ByConfidence: make(map[Confidence]int),
	}

	for i := range allocations {
		alloc := &allocations[i]
		site := siteByID[alloc.ID]
		decision := se.decideOne(alloc, site)
		decisions = append(decisions, decision)
		summary.ByStrategy[decision.Strategy]++
		summary.ByConfidence[decision.Confidence]++
	}

	summary.ProvenStackCount = summary.ByStrategy[StrategyStack]
	summary.ProvenImmortalCount = summary.ByStrategy[StrategyImmortal]
	summary.ProvenRegionCount = summary.ByStrategy[StrategyRegion]
	summary.ProvenUniqueOwnedCount = summary.ByStrategy[StrategyUniqueOwned]
	summary.ProvenARCCount = summary.ByStrategy[StrategyARC]
	summary.ProvenWeakCount = summary.ByStrategy[StrategyWeak]
	summary.TracingFallback = summary.ByStrategy[StrategyTracingFallback]
	total := len(decisions)
	if total > 0 {
		summary.ProvenGCReduction = 100 * float64(total-summary.TracingFallback) / float64(total)
	}
	summary.StrategyTimeMS = float64(time.Since(started)) / float64(time.Millisecond)

	slices.SortFunc(decisions, func(a, b AllocationDecision) int {
		if c := cmp.Compare(a.SourcePosition, b.SourcePosition); c != 0 {
			return c
		}
		return cmp.Compare(a.AllocationID, b.AllocationID)
	})

	return decisions, summary
}

// decideOne produces a single AllocationDecision from a v0.2 Allocation.
func (se *strategyEngine) decideOne(alloc *Allocation, site *allocationSite) AllocationDecision {
	decision := AllocationDecision{
		AllocationID:      alloc.ID,
		SourcePosition:    alloc.Position,
		Function:          alloc.Function,
		Package:           alloc.Package,
		Type:              alloc.Type,
		Kind:              alloc.Kind,
		Ownership:         alloc.Ownership,
		Lifetime:          alloc.Lifetime,
		V02Classification: alloc.Classification,
	}

	// Attempt stack proof.
	if site != nil {
		proofResult := se.stackProof.prove(site, *alloc)

		// Capture escape analysis result if available.
		if escInfo := se.stackProof.escapeIndex.lookup(alloc.Position); escInfo != nil {
			decision.EscapeAnalysis = escInfo.toEscapeResult()
		}

		if proofResult.proven {
			decision.Strategy = StrategyStack
			decision.Confidence = ConfidenceProven
			decision.Proof = proofResult.proof
			return decision
		}

		// Not proven for stack. Record stack blocking reasons and attempt immortal / static proof.
		decision.BlockingReasons = append(decision.BlockingReasons, proofResult.blockingReasons...)
		decision.Confidence = proofResult.confidence

		if se.immortalProof != nil {
			immortalResult := se.immortalProof.prove(site, *alloc)
			if immortalResult.proven {
				decision.Strategy = StrategyImmortal
				decision.Confidence = ConfidenceProven
				decision.Immortal = immortalResult.immortalInfo
				decision.Proof = immortalResult.proof
				return decision
			}
			decision.BlockingReasons = append(decision.BlockingReasons, immortalResult.blockingReasons...)
		}

		if se.regionProof != nil {
			regionResult := se.regionProof.prove(site, *alloc)
			if regionResult.proven {
				decision.Strategy = StrategyRegion
				decision.Confidence = ConfidenceProven
				decision.Region = regionResult.regionInfo
				decision.Proof = regionResult.proof
				return decision
			}
			decision.BlockingReasons = append(decision.BlockingReasons, regionResult.blockingReasons...)
		}

		// Not proven for region. Attempt unique-owner deterministic destruction proof.
		if se.uniqueProof != nil {
			uniqueResult := se.uniqueProof.prove(site, *alloc)
			if uniqueResult.proven {
				decision.Strategy = StrategyUniqueOwned
				decision.Confidence = ConfidenceProven
				decision.DestructionPoints = uniqueResult.destructionPoints
				decision.Proof = uniqueResult.proof
				return decision
			}
			decision.BlockingReasons = append(decision.BlockingReasons, uniqueResult.blockingReasons...)
		}

		// Not proven for unique owner. Attempt Automatic Reference Counting (ARC) proof.
		if se.arcProof != nil {
			arcResult := se.arcProof.prove(site, *alloc)
			if arcResult.proven {
				decision.Strategy = StrategyARC
				decision.Confidence = ConfidenceProven
				decision.ARC = arcResult.arcInfo
				decision.Proof = arcResult.proof
				return decision
			}
			decision.BlockingReasons = append(decision.BlockingReasons, arcResult.blockingReasons...)
		}

		// Not proven for ARC (e.g. cyclic data structure). Attempt Weak Reference and Cycle Breaking proof.
		if se.weakProof != nil {
			weakResult := se.weakProof.prove(site, *alloc)
			if weakResult.proven {
				decision.Strategy = StrategyWeak
				decision.Confidence = ConfidenceProven
				decision.Weak = weakResult.weakInfo
				decision.Proof = weakResult.proof
				return decision
			}
			decision.BlockingReasons = append(decision.BlockingReasons, weakResult.blockingReasons...)
		}
	} else {
		decision.Confidence = ConfidenceUnproven
		decision.BlockingReasons = []BlockingReason{{
			Kind:        "no_ssa_site",
			Description: "No SSA allocation site found for this allocation ID.",
		}}
	}

	// Map v0.2 classifications to preliminary strategies for remaining unproven cases.
	switch alloc.Classification {
	case StackSafe, PotentialStack:
		decision.Strategy = StrategyTracingFallback
		if decision.Confidence == "" {
			decision.Confidence = ConfidenceProbable
		}
		decision.FallbackReason = "Stack proof incomplete; requires additional validation before transformation."

	case RegionCandidate:
		decision.Strategy = StrategyTracingFallback
		if decision.Confidence == "" {
			decision.Confidence = ConfidenceProbable
		}
		decision.FallbackReason = "Region proof incomplete; allocation has escape or boundary hazards preventing arena placement."

	case ARCCandidate:
		decision.Strategy = StrategyTracingFallback
		if decision.Confidence == "" {
			decision.Confidence = ConfidenceProbable
		}
		decision.FallbackReason = "ARC proof incomplete; cyclic hazard or unverified escape prevents reference counting."

	case RuntimeFallback:
		decision.Strategy = StrategyTracingFallback
		decision.Confidence = ConfidenceUnproven
		if len(alloc.Reasons) > 0 {
			decision.FallbackReason = alloc.Reasons[0]
		} else {
			decision.FallbackReason = "No deterministic strategy can be proven safe."
		}

	case Unknown:
		decision.Strategy = StrategyTracingFallback
		decision.Confidence = ConfidenceUnproven
		decision.FallbackReason = "GOX encountered unmodeled SSA behavior."

	default:
		decision.Strategy = StrategyTracingFallback
		decision.Confidence = ConfidenceUnproven
		decision.FallbackReason = "Unknown classification."
	}

	return decision
}
