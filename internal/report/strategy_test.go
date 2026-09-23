package report

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestStrategyTextIncludesStrategySection(t *testing.T) {
	result := &analyzer.Result{
		Summary: analyzer.Summary{
			ByClassification:      map[analyzer.Classification]int{analyzer.StackSafe: 2, analyzer.RuntimeFallback: 1},
			AllocationsDiscovered: 3,
		},
		Allocations: []analyzer.Allocation{
			{Position: "main.go:4:2", Classification: analyzer.StackSafe, Reasons: []string{"bounded"}},
			{Position: "main.go:8:2", Classification: analyzer.StackSafe, Reasons: []string{"bounded"}},
			{Position: "main.go:12:2", Classification: analyzer.RuntimeFallback, Reasons: []string{"goroutine"}},
		},
		StrategySummary: &analyzer.StrategySummary{
			ByStrategy: map[analyzer.MemoryStrategy]int{
				analyzer.StrategyStack:           2,
				analyzer.StrategyTracingFallback: 1,
			},
			ByConfidence: map[analyzer.Confidence]int{
				analyzer.ConfidenceProven:   2,
				analyzer.ConfidenceUnproven: 1,
			},
			ProvenStackCount:  2,
			TracingFallback:   1,
			ProvenGCReduction: 66.7,
			StrategyTimeMS:    0.5,
		},
		Decisions: []analyzer.AllocationDecision{
			{
				AllocationID:      "alloc:1",
				SourcePosition:    "main.go:4:2",
				Strategy:          analyzer.StrategyStack,
				Confidence:        analyzer.ConfidenceProven,
				V02Classification: analyzer.StackSafe,
				Proof:             []string{"frame-local"},
			},
		},
	}

	got := StrategyText(result, true)
	for _, want := range []string{
		"Memory Strategy Summary",
		"Stack (proven):",
		"Weak (proven):",
		"Immortal / Static (proven):",
		"Tracing fallback:",
		"Proven:",
		"Unproven:",
		"Proven tracing-GC reduction:",
		"Allocation Decisions",
		"strategy: STACK",
		"confidence: PROVEN",
		"proof:",
		"frame-local",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("StrategyText() missing %q:\n%s", want, got)
		}
	}
}

func TestExplainDecisionFormatsBlockingReasons(t *testing.T) {
	decision := analyzer.AllocationDecision{
		AllocationID:   "alloc:2",
		SourcePosition: "main.go:42:13",
		Strategy:       analyzer.StrategyTracingFallback,
		Confidence:     analyzer.ConfidenceUnproven,
		FallbackReason: "goroutine boundary",
		BlockingReasons: []analyzer.BlockingReason{
			{Kind: "goroutine_escape", Description: "crosses goroutine boundary", Construct: "goroutine", Position: "main.go:42:13"},
		},
		EscapeAnalysis: &analyzer.EscapeResult{Escapes: true, GoCompilerSays: "escapes to heap"},
	}
	got := ExplainDecision(decision)
	for _, want := range []string{
		"allocation: main.go:42:13",
		"strategy: TRACING_FALLBACK",
		"confidence: UNPROVEN",
		"fallback_reason: goroutine boundary",
		"blocking_reasons:",
		"goroutine_escape",
		"escape_analysis: escapes=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExplainDecision() missing %q:\n%s", want, got)
		}
	}
}

func TestExplainDecisionFormatsDestructionPoints(t *testing.T) {
	decision := analyzer.AllocationDecision{
		AllocationID:   "alloc:unique:1",
		SourcePosition: "process.go:15:6",
		Strategy:       analyzer.StrategyUniqueOwned,
		Confidence:     analyzer.ConfidenceProven,
		DestructionPoints: []analyzer.DestructionPoint{
			{Kind: "LAST_USE", Position: "process.go:20:3", Instruction: "free(buf)", BlockID: 1},
			{Kind: "EARLY_RETURN", Position: "process.go:18:4", Instruction: "free(buf)", BlockID: 2},
		},
	}
	got := ExplainDecision(decision)
	for _, want := range []string{
		"strategy: UNIQUE_OWNED",
		"destruction_points:",
		"- [LAST_USE] block=1 pos=process.go:20:3 (free(buf))",
		"- [EARLY_RETURN] block=2 pos=process.go:18:4 (free(buf))",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExplainDecision() missing %q:\n%s", want, got)
		}
	}
}

func TestExplainDecisionFormatsARC(t *testing.T) {
	decision := analyzer.AllocationDecision{
		AllocationID:   "alloc:arc:1",
		SourcePosition: "shared.go:10:8",
		Strategy:       analyzer.StrategyARC,
		Confidence:     analyzer.ConfidenceProven,
		ARC: &analyzer.ARCInfo{
			IsAtomic:    true,
			ElidedSites: 1,
			RetainSites: []analyzer.ARCSite{
				{Kind: "RETAIN", Position: "shared.go:12:4", Instruction: "store t0", BlockID: 0},
			},
			ReleaseSites: []analyzer.ARCSite{
				{Kind: "RELEASE", Position: "shared.go:25:2", Instruction: "return", BlockID: 2},
			},
		},
	}
	got := ExplainDecision(decision)
	for _, want := range []string{
		"strategy: ARC",
		"arc: atomic (elided: 1 pairs)",
		"retain_sites:",
		"- [RETAIN] block=0 pos=shared.go:12:4 (store t0)",
		"release_sites:",
		"- [RELEASE] block=2 pos=shared.go:25:2 (return)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExplainDecision() missing %q:\n%s", want, got)
		}
	}
}

func TestExplainDecisionFormatsWeak(t *testing.T) {
	decision := analyzer.AllocationDecision{
		AllocationID:   "alloc:weak:1",
		SourcePosition: "tree.go:15:8",
		Strategy:       analyzer.StrategyWeak,
		Confidence:     analyzer.ConfidenceProven,
		Weak: &analyzer.WeakInfo{
			CycleBrokenBy:    "Parent",
			StrongTargetType: "*tree.Node",
			IsAtomic:         false,
			DowngradeSites: []analyzer.WeakSite{
				{Kind: "DOWNGRADE", Position: "tree.go:20:4", Instruction: "store t0 -> t1.Parent", BlockID: 0, Field: "Parent"},
			},
			UpgradeSites: []analyzer.WeakSite{
				{Kind: "UPGRADE", Position: "tree.go:35:2", Instruction: "t2 = t1.Parent", BlockID: 1, Field: "Parent"},
			},
		},
	}
	got := ExplainDecision(decision)
	for _, want := range []string{
		"strategy: WEAK",
		"weak: non-atomic (cycle broken by field 'Parent', strong target: *tree.Node)",
		"downgrade_sites:",
		"- [DOWNGRADE] block=0 pos=tree.go:20:4 field=Parent (store t0 -> t1.Parent)",
		"upgrade_sites:",
		"- [UPGRADE] block=1 pos=tree.go:35:2 field=Parent (t2 = t1.Parent)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExplainDecision() missing %q:\n%s", want, got)
		}
	}
}

func TestExplainDecisionFormatsImmortal(t *testing.T) {
	decision := analyzer.AllocationDecision{
		AllocationID:   "alloc:immortal:1",
		SourcePosition: "config.go:10:5",
		Strategy:       analyzer.StrategyImmortal,
		Confidence:     analyzer.ConfidenceProven,
		Immortal: &analyzer.ImmortalInfo{
			GlobalVar:     "DefaultConfig",
			InitScope:     "init",
			IsReadOnly:    true,
			PlacementKind: "IMMUTABLE_RODATA",
		},
	}
	got := ExplainDecision(decision)
	for _, want := range []string{
		"strategy: IMMORTAL",
		"immortal: IMMUTABLE_RODATA (scope: init, global: DefaultConfig, readonly: true)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExplainDecision() missing %q:\n%s", want, got)
		}
	}
}



