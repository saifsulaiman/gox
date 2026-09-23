package analyzer

import "testing"

func TestRetentionCycles(t *testing.T) {
	graph := newGraphBuilder()
	graph.addEdge(GraphEdge{From: "alloc:a", To: "alloc:b", Kind: "container_store"})
	graph.addEdge(GraphEdge{From: "alloc:b", To: "alloc:a", Kind: "map_entry"})
	graph.addEdge(GraphEdge{From: "alloc:c", To: "alloc:d", Kind: "closure_capture"})

	cycles := graph.retentionCycles()
	if !cycles["alloc:a"] || !cycles["alloc:b"] {
		t.Fatalf("cycle members not detected: %#v", cycles)
	}
	if cycles["alloc:c"] || cycles["alloc:d"] {
		t.Fatalf("acyclic retention marked cyclic: %#v", cycles)
	}
}

func TestFFIBoundaries(t *testing.T) {
	tests := []struct {
		path string
		name string
		want bool
	}{
		{path: "C", name: "C.malloc", want: true},
		{path: "plugin", name: "plugin.Open", want: true},
		{path: "runtime", name: "runtime.cgocall", want: true},
		{path: "syscall", name: "syscall.Syscall", want: true},
		{path: "fmt", name: "fmt.Println", want: false},
	}
	for _, test := range tests {
		if got := isFFIBoundary(test.path, test.name); got != test.want {
			t.Errorf("isFFIBoundary(%q, %q) = %t, want %t", test.path, test.name, got, test.want)
		}
	}
}
