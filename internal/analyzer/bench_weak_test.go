package analyzer

import (
	"testing"

	"github.com/goxlang/gox/internal/runtime"
)

type BenchTreeNode struct {
	ID    int
	Value string
}

var weakSink any

// BenchmarkCyclicTreeStandardHeap simulates building and destroying an asymmetric
// cyclic tree (root + children with parent pointers) on the standard Go heap,
// leaving tracing GC to trace and collect the cycles.
func BenchmarkCyclicTreeStandardHeap(b *testing.B) {
	type Node struct {
		ID       int
		Parent   *Node
		Children []*Node
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		root := &Node{ID: i, Children: make([]*Node, 0, 4)}
		for c := 0; c < 4; c++ {
			child := &Node{ID: i*10 + c, Parent: root}
			root.Children = append(root.Children, child)
		}
		weakSink = root
		i++
	}
}

// BenchmarkCyclicTreeWeakARC simulates GOX v0.7 deterministic reclamation of the same
// asymmetric cyclic tree using Strong ARC Box for root/children and Weak Box for parent back-pointers.
func BenchmarkCyclicTreeWeakARC(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Allocate root in strong Box
		rootBox := runtime.NewBox(BenchTreeNode{ID: i, Value: "root"}, false)

		// Create weak back-pointer to root
		parentWeak := runtime.Downgrade(rootBox)

		// Allocate 4 children with weak parent pointer
		childBoxes := make([]*runtime.Box[BenchTreeNode], 4)
		for c := 0; c < 4; c++ {
			childBoxes[c] = runtime.NewBox(BenchTreeNode{ID: i*10 + c, Value: "child"}, false)

			// Access parent safely via Upgrade()
			if parentStrong := runtime.Upgrade(parentWeak); parentStrong != nil {
				_ = runtime.Value(parentStrong).ID
				runtime.Release(parentStrong, nil) // Temporary strong handle released
			}
		}

		// Reclaim children
		for c := 0; c < 4; c++ {
			runtime.Release(childBoxes[c], nil)
		}

		// Reclaim root
		runtime.Release(rootBox, nil)

		// Release weak back-pointer: weak count reaches 0, control block freed
		runtime.ReleaseWeak(parentWeak)
		i++
	}
}

// BenchmarkWeakUpgradeDowngradeNonAtomic measures the raw throughput of
// Downgrade and Upgrade operations in thread-local non-atomic mode.
func BenchmarkWeakUpgradeDowngradeNonAtomic(b *testing.B) {
	box := runtime.NewBox(BenchTreeNode{ID: 42, Value: "test"}, false)
	weak := runtime.Downgrade(box)

	b.ReportAllocs()
	for b.Loop() {
		if strong := runtime.Upgrade(weak); strong != nil {
			runtime.Release(strong, nil)
		}
	}

	runtime.Release(box, nil)
	runtime.ReleaseWeak(weak)
}

// BenchmarkWeakUpgradeDowngradeAtomic measures the throughput of
// atomic Upgrade operations with thread-safe CAS coordination.
func BenchmarkWeakUpgradeDowngradeAtomic(b *testing.B) {
	box := runtime.NewBox(BenchTreeNode{ID: 42, Value: "test"}, true)
	weak := runtime.Downgrade(box)

	b.ReportAllocs()
	for b.Loop() {
		if strong := runtime.Upgrade(weak); strong != nil {
			runtime.Release(strong, nil)
		}
	}

	runtime.Release(box, nil)
	runtime.ReleaseWeak(weak)
}

// BenchmarkWeakProofCycleBreakLatency measures analyzer latency for whole-package
// static cycle breaking, asymmetric back-pointer inference, and upgrade/downgrade placement.
func BenchmarkWeakProofCycleBreakLatency(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchweak\n\ngo 1.24\n")
	writeTestFile(b, dir, "weak.go", `package benchweak

type TreeNode struct {
	ID       int
	Parent   *TreeNode
	Children []*TreeNode
}

func BuildTree(id int) *TreeNode {
	root := &TreeNode{ID: id, Children: make([]*TreeNode, 0, 2)}
	child := &TreeNode{ID: id + 1, Parent: root}
	root.Children = append(root.Children, child)
	return root
}
`)

	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
		if err != nil {
			b.Fatalf("Analyze() error: %v", err)
		}
	}
}
