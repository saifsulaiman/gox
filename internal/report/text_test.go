package report

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestTextDetail(t *testing.T) {
	result := &analyzer.Result{
		Summary:     analyzer.Summary{ByClassification: map[analyzer.Classification]int{analyzer.StackSafe: 1}, AllocationsDiscovered: 1},
		Allocations: []analyzer.Allocation{{Position: "main.go:4:2", Classification: analyzer.StackSafe, Reasons: []string{"bounded"}}},
	}
	got := Text(result, true)
	for _, want := range []string{"GOX Memory Analysis", "Stack-safe:", "allocation: main.go:4:2", "bounded"} {
		if !strings.Contains(got, want) {
			t.Errorf("Text() missing %q:\n%s", want, got)
		}
	}
}
