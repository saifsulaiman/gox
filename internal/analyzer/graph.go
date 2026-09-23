package analyzer

import (
	"cmp"
	"slices"
)

type graphBuilder struct {
	nodes map[string]GraphNode
	edges map[string]GraphEdge
}

func newGraphBuilder() *graphBuilder {
	return &graphBuilder{nodes: make(map[string]GraphNode), edges: make(map[string]GraphEdge)}
}

func (builder *graphBuilder) addNode(node GraphNode) {
	if node.ID == "" {
		return
	}
	if _, exists := builder.nodes[node.ID]; !exists {
		builder.nodes[node.ID] = node
	}
}

func (builder *graphBuilder) addEdge(edge GraphEdge) {
	if edge.From == "" || edge.To == "" {
		return
	}
	key := edge.From + "\x00" + edge.To + "\x00" + edge.Kind + "\x00" + edge.Position
	if _, exists := builder.edges[key]; !exists {
		builder.edges[key] = edge
	}
}

func (builder *graphBuilder) build() ProgramGraph {
	graph := ProgramGraph{
		Nodes: make([]GraphNode, 0, len(builder.nodes)),
		Edges: make([]GraphEdge, 0, len(builder.edges)),
	}
	for _, node := range builder.nodes {
		graph.Nodes = append(graph.Nodes, node)
	}
	for _, edge := range builder.edges {
		graph.Edges = append(graph.Edges, edge)
	}
	slices.SortFunc(graph.Nodes, func(a, b GraphNode) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(graph.Edges, func(a, b GraphEdge) int {
		if c := cmp.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := cmp.Compare(a.To, b.To); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return cmp.Compare(a.Position, b.Position)
	})
	return graph
}

func (builder *graphBuilder) retentionCycles() map[string]bool {
	retentionKinds := map[string]bool{
		"container_store": true, "map_entry": true, "append_element": true, "closure_capture": true,
	}
	adjacency := make(map[string][]string)
	for _, edge := range builder.edges {
		if retentionKinds[edge.Kind] {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		}
	}

	index := 0
	indices := make(map[string]int)
	lowlink := make(map[string]int)
	onStack := make(map[string]bool)
	var stack []string
	cycles := make(map[string]bool)
	var connect func(string)
	connect = func(node string) {
		index++
		indices[node] = index
		lowlink[node] = index
		stack = append(stack, node)
		onStack[node] = true
		for _, target := range adjacency[node] {
			if indices[target] == 0 {
				connect(target)
				if lowlink[target] < lowlink[node] {
					lowlink[node] = lowlink[target]
				}
			} else if onStack[target] && indices[target] < lowlink[node] {
				lowlink[node] = indices[target]
			}
		}
		if lowlink[node] != indices[node] {
			return
		}
		var component []string
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		if len(component) > 1 {
			for _, member := range component {
				cycles[member] = true
			}
		}
	}
	for node := range adjacency {
		if indices[node] == 0 {
			connect(node)
		}
	}
	return cycles
}
