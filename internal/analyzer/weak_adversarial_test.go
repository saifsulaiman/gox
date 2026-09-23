package analyzer

import (
	"strings"
	"testing"
)

// TestWeakReferenceInferenceAndAdversarialPatterns verifies GOX v0.7 Weak References,
// static cycle breaking, asymmetric back-pointer inference (Parent, Prev, Owner),
// and conservative fallback for symmetric cycles and escape hazards.
func TestWeakReferenceInferenceAndAdversarialPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/weaktest\n\ngo 1.24\n")
	writeTestFile(t, dir, "weak.go", `package weaktest

import (
	"reflect"
	"unsafe"
)

// 1. Asymmetric Tree type with Parent back-pointer
type TreeNode struct {
	Value    int
	Parent   *TreeNode
	Children []*TreeNode
}

// 2. Asymmetric Doubly-Linked List with Prev back-pointer
type DListNode struct {
	Value int
	Next  *DListNode
	Prev  *DListNode
}

// 3. Asymmetric Owner/Task relationship
type Worker struct {
	ID    int
	Tasks []*Task
}

type Task struct {
	Name  string
	Owner *Worker
}

// 4. Symmetric Peer Mesh (unbreakable cycle)
type PeerNode struct {
	Value int
	Peers []*PeerNode
}

var globalTreeSink *TreeNode

//go:noinline
func inspectTree(node *TreeNode) int {
	return node.Value
}

// Workflow 1: Valid Asymmetric Tree Hierarchy with Parent back-pointer.
// Root and child reference each other. Parent back-pointer is proven weak,
// leaving an acyclic strong ownership tree.
func ValidTreeWorkflow(val int) *TreeNode {
	root := &TreeNode{Value: val, Children: make([]*TreeNode, 0, 2)}
	child := &TreeNode{Value: val + 1, Children: make([]*TreeNode, 0, 1)}
	child.Parent = root
	root.Children = append(root.Children, child)
	return root
}

// Workflow 2: Valid Asymmetric Doubly-Linked List with Prev back-pointer.
// Prev is proven weak, leaving an acyclic Next-only strong chain.
func ValidDListWorkflow(val int) *DListNode {
	head := &DListNode{Value: val}
	tail := &DListNode{Value: val + 1}
	head.Next = tail
	tail.Prev = head
	return head
}

// Workflow 3: Valid Asymmetric Task/Owner with Owner back-pointer.
func ValidOwnerTaskWorkflow(id int, name string) *Worker {
	w := &Worker{ID: id, Tasks: make([]*Task, 0, 2)}
	task := &Task{Name: name, Owner: w}
	w.Tasks = append(w.Tasks, task)
	return w
}

// Workflow 4: Symmetric Peer Mesh: Must NOT break cycle because there is no asymmetric back-pointer.
// Strictly falls back to tracing GC.
func SymmetricMeshHazard(val int) *PeerNode {
	p1 := &PeerNode{Value: val, Peers: make([]*PeerNode, 0, 2)}
	p2 := &PeerNode{Value: val + 1, Peers: make([]*PeerNode, 0, 2)}
	p1.Peers = append(p1.Peers, p2)
	p2.Peers = append(p2.Peers, p1)
	return p1
}

// Workflow 5: Adversarial Hazard: Tree node escapes to global variable.
func GlobalTreeHazard(val int) {
	node := &TreeNode{Value: val}
	globalTreeSink = node
}

// Workflow 6: Adversarial Hazard: Tree node escapes through channel.
func ChannelTreeHazard(val int, ch chan *TreeNode) {
	node := &TreeNode{Value: val}
	ch <- node
}

// Workflow 7: Adversarial Hazard: Tree node converted via unsafe.Pointer.
func UnsafeTreeHazard(val int) uintptr {
	node := &TreeNode{Value: val}
	return uintptr(unsafe.Pointer(node))
}

// Workflow 8: Adversarial Hazard: Tree node inspected via reflection.
func ReflectionTreeHazard(val int) {
	node := &TreeNode{Value: val}
	_ = reflect.ValueOf(node)
}

// Workflow 9: Concurrent Tree Workflow: Tree node shared across goroutines.
func ConcurrentTreeWorkflow(val int) {
	root := &TreeNode{Value: val, Children: make([]*TreeNode, 0, 1)}
	child := &TreeNode{Value: val + 1}
	child.Parent = root
	root.Children = append(root.Children, child)
	go func() {
		_ = inspectTree(child)
	}()
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// 1. Verify ValidTreeWorkflow promotes TreeNode to StrategyWeak with Parent cycle-breaker
	t.Run("ValidTreeWithParentCycleBreaker", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ValidTreeWorkflow") && strings.Contains(d.Type, "TreeNode") {
				found = true
				if d.Strategy != StrategyWeak || d.Confidence != ConfidenceProven {
					t.Errorf("expected PROVEN WEAK strategy for TreeNode in ValidTreeWorkflow, got %s (%s)", d.Strategy, d.Confidence)
				}
				if d.Weak == nil {
					t.Fatal("expected WeakInfo to be present for proven WEAK decision")
				}
				if d.Weak.CycleBrokenBy != "Parent" {
					t.Errorf("expected CycleBrokenBy='Parent', got %q", d.Weak.CycleBrokenBy)
				}
				if len(d.Weak.AcyclicStrongProof) == 0 {
					t.Error("expected acyclic strong graph proof invariants")
				}
				if len(d.Weak.DowngradeSites) == 0 {
					t.Error("expected downgrade site to be recorded for Parent")
				}
			}
		}
		if !found {
			t.Error("could not find TreeNode allocation in ValidTreeWorkflow")
		}
	})

	// 2. Verify ValidDListWorkflow promotes DListNode to StrategyWeak with Prev cycle-breaker
	t.Run("ValidDListWithPrevCycleBreaker", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ValidDListWorkflow") && strings.Contains(d.Type, "DListNode") {
				found = true
				if d.Strategy != StrategyWeak || d.Confidence != ConfidenceProven {
					t.Errorf("expected PROVEN WEAK strategy for DListNode in ValidDListWorkflow, got %s (%s)", d.Strategy, d.Confidence)
				}
				if d.Weak == nil {
					t.Fatal("expected WeakInfo to be present for proven WEAK decision")
				}
				if d.Weak.CycleBrokenBy != "Prev" {
					t.Errorf("expected CycleBrokenBy='Prev', got %q", d.Weak.CycleBrokenBy)
				}
			}
		}
		if !found {
			t.Error("could not find DListNode allocation in ValidDListWorkflow")
		}
	})

	// 3. Verify ValidOwnerTaskWorkflow promotes Task to StrategyWeak with Owner cycle-breaker
	t.Run("ValidOwnerTaskCycleBreaker", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ValidOwnerTaskWorkflow") && strings.Contains(d.Type, "Task") && !strings.Contains(d.Type, "Worker") {
				found = true
				if d.Strategy != StrategyWeak || d.Confidence != ConfidenceProven {
					t.Errorf("expected PROVEN WEAK strategy for Task in ValidOwnerTaskWorkflow, got %s (%s)", d.Strategy, d.Confidence)
				}
				if d.Weak == nil {
					t.Fatal("expected WeakInfo to be present for proven WEAK decision")
				}
				if d.Weak.CycleBrokenBy != "Owner" {
					t.Errorf("expected CycleBrokenBy='Owner', got %q", d.Weak.CycleBrokenBy)
				}
			}
		}
		if !found {
			t.Error("could not find Task allocation in ValidOwnerTaskWorkflow")
		}
	})

	// 4. Verify SymmetricMeshHazard is strictly rejected from WEAK and ARC -> TRACING_FALLBACK
	t.Run("RejectSymmetricMeshFromWeakAndARC", func(t *testing.T) {
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "SymmetricMeshHazard") && strings.HasPrefix(d.Type, "*") && strings.Contains(d.Type, "PeerNode") {
				if (d.Strategy == StrategyWeak || d.Strategy == StrategyARC) && d.Confidence == ConfidenceProven {
					t.Errorf("SYMMETRIC CYCLE LEAK HAZARD: PeerNode was falsely proven %s at %s", d.Strategy, d.SourcePosition)
				}
				if d.Strategy != StrategyTracingFallback {
					t.Errorf("expected TRACING_FALLBACK for symmetric mesh, got %s", d.Strategy)
				}
			}
		}
	})

	// 5. Verify Adversarial Hazards: Global, Channel, Unsafe, Reflection
	adversarialHazards := []struct {
		fnName string
		hazard string
	}{
		{"GlobalTreeHazard", "global store"},
		{"ChannelTreeHazard", "channel transfer"},
		{"UnsafeTreeHazard", "unsafe.Pointer"},
		{"ReflectionTreeHazard", "reflection inspection"},
	}

	for _, tc := range adversarialHazards {
		t.Run(tc.fnName, func(t *testing.T) {
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) && strings.Contains(d.Type, "TreeNode") {
					if (d.Strategy == StrategyWeak || d.Strategy == StrategyARC) && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL HAZARD: %s (%s) was falsely proven %s at %s",
							tc.fnName, tc.hazard, d.Strategy, d.SourcePosition)
					}
					if tc.fnName == "GlobalTreeHazard" {
						if d.Strategy != StrategyTracingFallback && d.Strategy != StrategyImmortal {
							t.Errorf("expected TRACING_FALLBACK or IMMORTAL for %s, got %s", tc.fnName, d.Strategy)
						}
					} else {
						if d.Strategy != StrategyTracingFallback {
							t.Errorf("expected TRACING_FALLBACK for %s, got %s", tc.fnName, d.Strategy)
						}
					}
				}
			}
		})
	}

	// 6. Verify ConcurrentTreeWorkflow uses atomic weak reference tracking for goroutine-shared node
	t.Run("ConcurrentWeakIsAtomic", func(t *testing.T) {
		foundAtomic := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ConcurrentTreeWorkflow") && strings.Contains(d.Type, "TreeNode") {
				if d.Ownership == OwnershipConcurrent && d.Strategy == StrategyWeak {
					foundAtomic = true
					if !d.Weak.IsAtomic {
						t.Errorf("expected IsAtomic=true for concurrent tree node at %s", d.SourcePosition)
					}
				}
			}
		}
		if !foundAtomic {
			t.Error("expected to find concurrent tree node with IsAtomic=true")
		}
	})
}
