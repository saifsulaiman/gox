package doctor

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestDiagnoseClean(t *testing.T) {
	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				AllocationID:   "alloc:1",
				SourcePosition: "main.go:10:15",
				Function:       "main.main",
				Type:           "*main.Config",
				Strategy:       analyzer.StrategyImmortal,
				Confidence:     analyzer.ConfidenceProven,
			},
			{
				AllocationID:   "alloc:2",
				SourcePosition: "main.go:20:12",
				Function:       "main.Process",
				Type:           "*main.Item",
				Strategy:       analyzer.StrategyRegion,
				Confidence:     analyzer.ConfidenceProven,
			},
		},
	}

	report := Diagnose(result)
	if report.HealthScore != 100.0 {
		t.Errorf("expected 100%% health score, got %.1f%%", report.HealthScore)
	}
	if len(report.Issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(report.Issues))
	}

	text := report.FormatText(5)
	if !strings.Contains(text, "No GC fallback issues detected") {
		t.Errorf("expected clean message in output:\n%s", text)
	}
}

func TestDiagnoseIssues(t *testing.T) {
	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				AllocationID:   "alloc:1",
				SourcePosition: "todo.go:42:10",
				Function:       "models.FindTodos",
				Type:           "*models.Todo",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{
						Kind:        "reflection_call",
						Description: "Dynamic reflection call inside GORM prevents static lifetime bounding",
					},
				},
			},
			{
				AllocationID:   "alloc:2",
				SourcePosition: "main.go:15:59",
				Function:       "main.main",
				Type:           "*[3]any",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{
						Kind:        "external_call_escape",
						Description: "Reference is passed to an external or dynamic call",
						Construct:   "external_call",
					},
				},
			},
			{
				AllocationID:   "alloc:3",
				SourcePosition: "worker.go:24:28",
				Function:       "worker.ProcessBatch",
				Type:           "*models.BatchResult",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{
						Kind:        "exported_api_escape",
						Description: "Reference leaves the analyzed program through an exported library API",
						Construct:   "exported_return",
					},
				},
			},
		},
	}

	report := Diagnose(result)
	if report.HealthScore != 0.0 {
		t.Errorf("expected 0%% health score, got %.1f%%", report.HealthScore)
	}
	if len(report.Issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(report.Issues))
	}

	text := report.FormatText(5)
	if !strings.Contains(text, "Dynamic Reflection & ORM Boxing") {
		t.Errorf("expected Reflection issue in output:\n%s", text)
	}
	if !strings.Contains(text, "Interface Boxing & External Dynamic Calls") {
		t.Errorf("expected Interface Boxing issue in output:\n%s", text)
	}
	if !strings.Contains(text, "Exported API Return Escapes") {
		t.Errorf("expected Exported API issue in output:\n%s", text)
	}

	jsonData, err := report.FormatJSON()
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}
	if !strings.Contains(string(jsonData), `"health_score_pct": 0`) {
		t.Errorf("expected health_score_pct: 0 in json:\n%s", string(jsonData))
	}
}

func TestDiagnoseMoreIssues(t *testing.T) {
	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				AllocationID:   "alloc:1",
				SourcePosition: "main.go:10:1",
				Function:       "main.run",
				Strategy:       analyzer.StrategyImmortal,
				Confidence:     analyzer.ConfidenceProven,
			},
			{
				AllocationID:   "alloc:2",
				SourcePosition: "main.go:20:1",
				Function:       "main.closure",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{Construct: "closure", Description: "captured in closure"},
				},
			},
			{
				AllocationID:   "alloc:3",
				SourcePosition: "main.go:30:1",
				Function:       "main.cycle",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{Construct: "cycle", Description: "cyclic pointer graph"},
				},
			},
			{
				AllocationID:   "alloc:4",
				SourcePosition: "main.go:40:1",
				Function:       "main.global",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				BlockingReasons: []analyzer.BlockingReason{
					{Kind: "global_ref", Description: "stored in global variable"},
				},
			},
			{
				AllocationID:   "alloc:5",
				SourcePosition: "main.go:50:1",
				Function:       "main.fb1",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				FallbackReason: "dynamic external invocation",
			},
			{
				AllocationID:   "alloc:6",
				SourcePosition: "main.go:60:1",
				Function:       "main.fb2",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				FallbackReason: "exported api return",
			},
			{
				AllocationID:   "alloc:7",
				SourcePosition: "main.go:70:1",
				Function:       "main.fb3",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
				FallbackReason: "unbounded escape",
			},
		},
	}

	report := Diagnose(result)
	if report.HealthScore <= 0 || report.HealthScore >= 80 {
		t.Logf("HealthScore: %.1f%%", report.HealthScore)
	}

	text := report.FormatText(10)
	for _, want := range []string{
		"Closure Capture & Goroutine Escapes",
		"Cyclic Pointer Graphs",
		"Mutable Global State",
		"Complex Escape Paths",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("FormatText missing %q:\n%s", want, text)
		}
	}
}
