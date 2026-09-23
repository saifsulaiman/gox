package analyzer

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"go/token"
	"slices"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/ssa"
)

type callGraphIndex struct {
	targets  map[ssa.CallInstruction][]*ssa.Function
	callers  map[*ssa.Function][]ssa.CallInstruction
	summary  CallGraphSummary
	function map[*ssa.Function]string
}

func buildCallGraph(prog *ssa.Program, sourceFunctions map[*ssa.Function]bool, fset *token.FileSet, graph *graphBuilder) callGraphIndex {
	index := callGraphIndex{
		targets:  make(map[ssa.CallInstruction][]*ssa.Function),
		callers:  make(map[*ssa.Function][]ssa.CallInstruction),
		function: make(map[*ssa.Function]string),
	}
	for function := range sourceFunctions {
		index.addFunctionNode(function, fset, graph, true)
	}

	cg := cha.CallGraph(prog)
	for _, node := range cg.Nodes {
		if node == nil || node.Func == nil {
			continue
		}
		for _, edge := range node.Out {
			if edge == nil || edge.Site == nil || edge.Callee == nil || edge.Callee.Func == nil {
				continue
			}
			target := edge.Callee.Func
			index.targets[edge.Site] = appendUniqueFunction(index.targets[edge.Site], target)
			index.callers[target] = appendUniqueCall(index.callers[target], edge.Site)
			// The exported graph is rooted in analyzed source functions.
			// Incoming CHA edges remain indexed for conservative return-flow
			// propagation, but displaying them would swamp the useful graph
			// with signature-compatible standard-library callbacks.
			if !sourceFunctions[node.Func] {
				continue
			}
			from := index.addFunctionNode(node.Func, fset, graph, sourceFunctions[node.Func])
			to := index.addFunctionNode(target, fset, graph, sourceFunctions[target])
			kind := "call_static"
			if edge.Site.Common().StaticCallee() == nil {
				kind = "call_dynamic"
				index.summary.DynamicCallEdges++
			}
			index.summary.CallEdges++
			graph.addEdge(GraphEdge{
				From: from, To: to, Kind: kind,
				Position: positionString(fset, edge.Site.Pos()), Detail: edge.Description(),
			})
		}
	}

	for function := range sourceFunctions {
		for _, block := range function.Blocks {
			for _, instruction := range block.Instrs {
				call, ok := instruction.(ssa.CallInstruction)
				if !ok || call.Common() == nil {
					continue
				}
				if _, builtin := call.Common().Value.(*ssa.Builtin); builtin {
					continue
				}
				if len(index.targets[call]) == 0 && call.Common().StaticCallee() == nil {
					index.summary.UnresolvedCalls++
					from := index.addFunctionNode(function, fset, graph, true)
					to := "external:unresolved"
					graph.addNode(GraphNode{ID: to, Kind: "external", Label: "unresolved dynamic call"})
					graph.addEdge(GraphEdge{From: from, To: to, Kind: "call_unresolved", Position: positionString(fset, call.Pos())})
				}
			}
		}
	}
	index.summary.Functions = len(sourceFunctions)
	return index
}

func (index *callGraphIndex) addFunctionNode(function *ssa.Function, fset *token.FileSet, graph *graphBuilder, source bool) string {
	if id := index.function[function]; id != "" {
		return id
	}
	id := stableID("fn", function.String())
	index.function[function] = id
	kind := "external_function"
	if source {
		kind = "function"
	}
	packagePath := ""
	if function.Pkg != nil && function.Pkg.Pkg != nil {
		packagePath = function.Pkg.Pkg.Path()
	}
	graph.addNode(GraphNode{
		ID: id, Kind: kind, Label: function.String(), Package: packagePath,
		Position: positionString(fset, function.Pos()),
	})
	return id
}

func appendUniqueFunction(functions []*ssa.Function, candidate *ssa.Function) []*ssa.Function {
	for _, function := range functions {
		if function == candidate {
			return functions
		}
	}
	return append(functions, candidate)
}

func appendUniqueCall(calls []ssa.CallInstruction, candidate ssa.CallInstruction) []ssa.CallInstruction {
	for _, call := range calls {
		if call == candidate {
			return calls
		}
	}
	return append(calls, candidate)
}

func stableID(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s:%x", prefix, digest[:8])
}

func positionString(fset *token.FileSet, pos token.Pos) string {
	position := fset.Position(pos)
	if !position.IsValid() {
		return ""
	}
	return position.String()
}

func sortCallTargets(targets []*ssa.Function) {
	slices.SortFunc(targets, func(a, b *ssa.Function) int {
		return cmp.Compare(a.String(), b.String())
	})
}
