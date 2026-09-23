package report

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestTextDetail(t *testing.T) {
	result := &analyzer.Result{
		Summary: analyzer.Summary{
			PackagesAnalyzed:         1,
			FunctionsAnalyzed:        2,
			AllocationsDiscovered:    2,
			PotentialGCReductionPC:   85.5,
			ByClassification: map[analyzer.Classification]int{
				analyzer.StackSafe: 1,
				analyzer.ARCCandidate: 1,
			},
		},
		CallGraph: analyzer.CallGraphSummary{
			Functions:        2,
			CallEdges:        1,
			DynamicCallEdges: 0,
			UnresolvedCalls:  0,
		},
		Metrics: analyzer.Metrics{
			ThroughputAllocationsPerSecond: 1000.0,
			AveragePackageLatencyMS:        1.2,
			P95PackageLatencyMS:            2.5,
			P99PackageLatencyMS:            3.0,
			MemoryUsageBytes:               4096,
			CPUUsageMS:                     15.0,
			BinarySizeBytes:                10240,
			CompilationTimeMS:              5.0,
			AnalysisTimeMS:                 10.0,
		},
		Allocations: []analyzer.Allocation{
			{
				ID:             "alloc:1",
				Position:       "main.go:4:2",
				Package:        "main",
				Function:       "main.run",
				Kind:           "make_slice",
				Type:           "[]byte",
				Classification: analyzer.StackSafe,
				Ownership:      "unique",
				Lifetime:       "local",
				CallerDepth:    1,
				RetainedBy:     []string{"scope:req"},
				Reasons:        []string{"bounded"},
				Limitations:    []string{"none"},
				Trace: []analyzer.TraceEvent{
					{Step: 1, Operation: "alloc", Position: "main.go:4:2", Detail: "allocated on stack"},
					{Step: 2, Operation: "pass", Position: "", Function: "main.run", Detail: "passed to callee"},
				},
			},
		},
		Disclaimers: []string{"Experimental software"},
		Limitations: []string{"Requires CGO disabled"},
		Warnings:    []string{"Consider arena recycling"},
	}
	got := Text(result, true)
	for _, want := range []string{
		"GOX Memory Analysis",
		"Stack-safe:",
		"allocation: main.go:4:2",
		"retained by: scope:req",
		"limitations:",
		"trace:",
		"1. alloc [main.go:4:2]: allocated on stack",
		"2. pass [main.run]: passed to callee",
		"Disclaimers",
		"- Experimental software",
		"Limitations",
		"- Requires CGO disabled",
		"Notes",
		"- Consider arena recycling",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Text() missing %q:\n%s", want, got)
		}
	}

	gotBrief := Text(result, false)
	if strings.Contains(gotBrief, "retained by: scope:req") {
		t.Errorf("brief Text() should not contain details")
	}
}
