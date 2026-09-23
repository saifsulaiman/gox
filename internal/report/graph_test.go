package report

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestGraphDOT(t *testing.T) {
	graph := analyzer.ProgramGraph{
		Nodes: []analyzer.GraphNode{
			{ID: "fn:1", Kind: "function", Label: `main.say"hello`},
			{ID: "alloc:1", Kind: "allocation", Label: "*User"},
		},
		Edges: []analyzer.GraphEdge{{From: "fn:1", To: "alloc:1", Kind: "allocates"}},
	}
	got := GraphDOT(graph)
	for _, want := range []string{"digraph gox", `"fn:1" -> "alloc:1"`, "shape=ellipse", `say\"hello`} {
		if !strings.Contains(got, want) {
			t.Errorf("GraphDOT() missing %q:\n%s", want, got)
		}
	}
}
