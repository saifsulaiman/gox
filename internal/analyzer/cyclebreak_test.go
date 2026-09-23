package analyzer

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestCycleBreakInference(t *testing.T) {
	src := `package testcycle

type TreeNode struct {
	Value    int
	Parent   *TreeNode
	Left     *TreeNode
	Right    *TreeNode
}

type DoublyLinkedNode struct {
	ID   int
	Next *DoublyLinkedNode
	Prev *DoublyLinkedNode
}

type RingPeer struct {
	ID   int
	Peer *RingPeer
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}

	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("testcycle", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("TypeCheck error: %v", err)
	}

	ca := newCycleAnalyzer()

	// 1. Test TreeNode: has cycle, breakable via Parent
	treeType := pkg.Scope().Lookup("TreeNode").Type()
	treeProof := ca.ProveAcyclic(treeType, nil, Allocation{})
	if treeProof.IsAcyclic {
		t.Error("TreeNode should be identified as cyclic by ProveAcyclic")
	}

	treeBreak := ca.BreakCycle(treeType, nil, Allocation{})
	if !treeBreak.CanBreak {
		t.Fatalf("expected TreeNode cycle to be breakable, got: %s", treeBreak.Reason)
	}
	if treeBreak.BrokenByField != "Parent" {
		t.Errorf("expected cycle broken by 'Parent', got %q", treeBreak.BrokenByField)
	}
	if len(treeBreak.Invariants) == 0 {
		t.Error("expected proof invariants for TreeNode cycle breaking")
	}

	// 2. Test DoublyLinkedNode: has cycle, breakable via Prev
	listType := pkg.Scope().Lookup("DoublyLinkedNode").Type()
	listProof := ca.ProveAcyclic(listType, nil, Allocation{})
	if listProof.IsAcyclic {
		t.Error("DoublyLinkedNode should be identified as cyclic by ProveAcyclic")
	}

	listBreak := ca.BreakCycle(listType, nil, Allocation{})
	if !listBreak.CanBreak {
		t.Fatalf("expected DoublyLinkedNode cycle to be breakable, got: %s", listBreak.Reason)
	}
	if listBreak.BrokenByField != "Prev" {
		t.Errorf("expected cycle broken by 'Prev', got %q", listBreak.BrokenByField)
	}

	// 3. Test RingPeer: symmetric cycle, NOT breakable (must fall back)
	ringType := pkg.Scope().Lookup("RingPeer").Type()
	ringProof := ca.ProveAcyclic(ringType, nil, Allocation{})
	if ringProof.IsAcyclic {
		t.Error("RingPeer should be identified as cyclic by ProveAcyclic")
	}

	ringBreak := ca.BreakCycle(ringType, nil, Allocation{})
	if ringBreak.CanBreak {
		t.Errorf("expected symmetric RingPeer NOT to be breakable, but got broken by %q", ringBreak.BrokenByField)
	}
}
