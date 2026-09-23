package report

import (
	"fmt"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
)

// StrategyText produces a text report for memory-strategy analysis mode.
// It includes the v0.2 analysis summary plus the v0.3 strategy decisions.
func StrategyText(result *analyzer.Result, detail bool) string {
	var out strings.Builder

	// Start with the standard v0.2 summary.
	out.WriteString(Text(result, false))

	// Add strategy summary.
	if result.StrategySummary != nil {
		summary := result.StrategySummary
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Memory Strategy Summary")
		fmt.Fprintln(&out)
		strategyRow(&out, "Stack (proven)", summary.ByStrategy[analyzer.StrategyStack])
		strategyRow(&out, "Caller stack", summary.ByStrategy[analyzer.StrategyCallerStack])
		strategyRow(&out, "Region", summary.ByStrategy[analyzer.StrategyRegion])
		strategyRow(&out, "Unique owned (proven)", summary.ByStrategy[analyzer.StrategyUniqueOwned])
		strategyRow(&out, "ARC (proven)", summary.ByStrategy[analyzer.StrategyARC])
		strategyRow(&out, "Weak (proven)", summary.ByStrategy[analyzer.StrategyWeak])
		strategyRow(&out, "Immortal / Static (proven)", summary.ByStrategy[analyzer.StrategyImmortal])
		strategyRow(&out, "Tracing fallback", summary.ByStrategy[analyzer.StrategyTracingFallback])
		fmt.Fprintln(&out)

		confidenceRow(&out, "Proven", summary.ByConfidence[analyzer.ConfidenceProven])
		confidenceRow(&out, "Probable", summary.ByConfidence[analyzer.ConfidenceProbable])
		confidenceRow(&out, "Unproven", summary.ByConfidence[analyzer.ConfidenceUnproven])
		fmt.Fprintln(&out)

		fmt.Fprintf(&out, "Proven tracing-GC reduction: %.1f%% (allocation sites, proven only)\n", summary.ProvenGCReduction)
		fmt.Fprintf(&out, "Strategy analysis time:      %.3f ms\n", summary.StrategyTimeMS)
	}

	// Add per-decision detail.
	if detail && len(result.Decisions) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Allocation Decisions")
		for _, decision := range result.Decisions {
			fmt.Fprintln(&out)
			fmt.Fprintln(&out, formatDecision(decision))
		}
	}

	return out.String()
}

// ExplainDecision formats a single AllocationDecision for human reading.
func ExplainDecision(decision analyzer.AllocationDecision) string {
	return formatDecision(decision)
}

func formatDecision(decision analyzer.AllocationDecision) string {
	var out strings.Builder
	fmt.Fprintf(&out, "allocation: %s\n", decision.SourcePosition)
	fmt.Fprintf(&out, "id: %s\n", decision.AllocationID)
	fmt.Fprintf(&out, "package: %s\n", decision.Package)
	fmt.Fprintf(&out, "function: %s\n", decision.Function)
	fmt.Fprintf(&out, "type: %s\n", decision.Type)
	fmt.Fprintf(&out, "kind: %s\n", decision.Kind)
	fmt.Fprintf(&out, "strategy: %s\n", decision.Strategy)
	fmt.Fprintf(&out, "ownership: %s\n", decision.Ownership)
	fmt.Fprintf(&out, "lifetime: %s\n", decision.Lifetime)
	fmt.Fprintf(&out, "confidence: %s\n", decision.Confidence)
	fmt.Fprintf(&out, "v0.2 classification: %s\n", decision.V02Classification)

	if decision.Region != nil {
		fmt.Fprintf(&out, "region: %s (scope: %s, owner: %s, destroy: %s)\n",
			decision.Region.RegionID, decision.Region.Scope, decision.Region.OwningFunc, decision.Region.DestroyPoint)
	}

	if len(decision.DestructionPoints) > 0 {
		fmt.Fprintln(&out, "destruction_points:")
		for _, dp := range decision.DestructionPoints {
			fmt.Fprintf(&out, "  - [%s] block=%d pos=%s", dp.Kind, dp.BlockID, dp.Position)
			if dp.Instruction != "" {
				fmt.Fprintf(&out, " (%s)", dp.Instruction)
			}
			fmt.Fprintln(&out)
		}
	}

	if decision.ARC != nil {
		mode := "non-atomic"
		if decision.ARC.IsAtomic {
			mode = "atomic"
		}
		fmt.Fprintf(&out, "arc: %s (elided: %d pairs)\n", mode, decision.ARC.ElidedSites)
		if len(decision.ARC.RetainSites) > 0 {
			fmt.Fprintln(&out, "  retain_sites:")
			for _, r := range decision.ARC.RetainSites {
				fmt.Fprintf(&out, "    - [%s] block=%d pos=%s (%s)\n", r.Kind, r.BlockID, r.Position, r.Instruction)
			}
		}
		if len(decision.ARC.ReleaseSites) > 0 {
			fmt.Fprintln(&out, "  release_sites:")
			for _, r := range decision.ARC.ReleaseSites {
				fmt.Fprintf(&out, "    - [%s] block=%d pos=%s (%s)\n", r.Kind, r.BlockID, r.Position, r.Instruction)
			}
		}
	}

	if decision.Weak != nil {
		mode := "non-atomic"
		if decision.Weak.IsAtomic {
			mode = "atomic"
		}
		fmt.Fprintf(&out, "weak: %s (cycle broken by field '%s', strong target: %s)\n",
			mode, decision.Weak.CycleBrokenBy, decision.Weak.StrongTargetType)
		if len(decision.Weak.DowngradeSites) > 0 {
			fmt.Fprintln(&out, "  downgrade_sites:")
			for _, d := range decision.Weak.DowngradeSites {
				fmt.Fprintf(&out, "    - [%s] block=%d pos=%s field=%s (%s)\n", d.Kind, d.BlockID, d.Position, d.Field, d.Instruction)
			}
		}
		if len(decision.Weak.UpgradeSites) > 0 {
			fmt.Fprintln(&out, "  upgrade_sites:")
			for _, u := range decision.Weak.UpgradeSites {
				fmt.Fprintf(&out, "    - [%s] block=%d pos=%s field=%s (%s)\n", u.Kind, u.BlockID, u.Position, u.Field, u.Instruction)
			}
		}
	}

	if decision.Immortal != nil {
		fmt.Fprintf(&out, "immortal: %s (scope: %s, global: %s, readonly: %t)\n",
			decision.Immortal.PlacementKind, decision.Immortal.InitScope, decision.Immortal.GlobalVar, decision.Immortal.IsReadOnly)
	}

	if len(decision.Proof) > 0 {
		fmt.Fprintln(&out, "proof:")
		for _, p := range decision.Proof {
			fmt.Fprintf(&out, "  - %s\n", p)
		}
	}

	if len(decision.BlockingReasons) > 0 {
		fmt.Fprintln(&out, "blocking_reasons:")
		for _, br := range decision.BlockingReasons {
			fmt.Fprintf(&out, "  - [%s] %s", br.Kind, br.Description)
			if br.Construct != "" {
				fmt.Fprintf(&out, " (construct: %s)", br.Construct)
			}
			if br.Position != "" {
				fmt.Fprintf(&out, " at %s", br.Position)
			}
			fmt.Fprintln(&out)
		}
	}

	if decision.FallbackReason != "" {
		fmt.Fprintf(&out, "fallback_reason: %s\n", decision.FallbackReason)
	}

	if decision.EscapeAnalysis != nil {
		fmt.Fprintf(&out, "escape_analysis: escapes=%t\n", decision.EscapeAnalysis.Escapes)
		if decision.EscapeAnalysis.GoCompilerSays != "" {
			fmt.Fprintf(&out, "  go_compiler: %s\n", decision.EscapeAnalysis.GoCompilerSays)
		}
	}

	return out.String()
}

func strategyRow(out *strings.Builder, label string, value int) {
	fmt.Fprintf(out, "%-25s %8d\n", label+":", value)
}

func confidenceRow(out *strings.Builder, label string, value int) {
	fmt.Fprintf(out, "%-25s %8d\n", label+":", value)
}
