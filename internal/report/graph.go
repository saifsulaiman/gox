package report

import (
	"fmt"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
)

// GraphDOT renders the whole-program call and retention graph as Graphviz DOT.
func GraphDOT(graph analyzer.ProgramGraph) string {
	var out strings.Builder
	fmt.Fprintln(&out, "digraph gox {")
	fmt.Fprintln(&out, "  rankdir=LR;")
	for _, node := range graph.Nodes {
		shape := "box"
		if node.Kind == "allocation" {
			shape = "ellipse"
		} else if node.Kind == "external" || node.Kind == "external_function" {
			shape = "diamond"
		}
		fmt.Fprintf(&out, "  %q [label=%q, shape=%s];\n", node.ID, node.Label, shape)
	}
	for _, edge := range graph.Edges {
		label := edge.Kind
		if edge.Detail != "" {
			label += ": " + edge.Detail
		}
		fmt.Fprintf(&out, "  %q -> %q [label=%q];\n", edge.From, edge.To, label)
	}
	fmt.Fprintln(&out, "}")
	return out.String()
}
