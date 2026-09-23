package analyzer

import (
	"go/types"
	"testing"
)

func TestCycleAnalyzerAcyclicAndCyclic(t *testing.T) {
	ca := newCycleAnalyzer()

	// 1. Primitive int -> Acyclic
	intType := types.Typ[types.Int]
	proof := ca.ProveAcyclic(intType, nil, Allocation{})
	if !proof.IsAcyclic {
		t.Errorf("expected int to be acyclic, got: %s", proof.Reason)
	}

	// 2. Struct Leaf { Val int } -> Acyclic
	leafStruct := types.NewStruct([]*types.Var{
		types.NewField(0, nil, "Val", intType, false),
	}, nil)
	leafNamed := types.NewNamed(types.NewTypeName(0, nil, "Leaf", nil), leafStruct, nil)

	proof = ca.ProveAcyclic(leafNamed, nil, Allocation{})
	if !proof.IsAcyclic {
		t.Errorf("expected Leaf to be acyclic, got: %s", proof.Reason)
	}

	// 3. Struct DAGNode { L *Leaf, R *Leaf } -> Acyclic
	dagStruct := types.NewStruct([]*types.Var{
		types.NewField(0, nil, "L", types.NewPointer(leafNamed), false),
		types.NewField(0, nil, "R", types.NewPointer(leafNamed), false),
	}, nil)
	dagNamed := types.NewNamed(types.NewTypeName(0, nil, "DAGNode", nil), dagStruct, nil)

	proof = ca.ProveAcyclic(dagNamed, nil, Allocation{})
	if !proof.IsAcyclic {
		t.Errorf("expected DAGNode to be acyclic, got: %s", proof.Reason)
	}

	// 4. Recursive Struct: Node { Next *Node } -> Cyclic!
	nodeNamed := types.NewNamed(types.NewTypeName(0, nil, "Node", nil), nil, nil)
	nodeStruct := types.NewStruct([]*types.Var{
		types.NewField(0, nil, "Next", types.NewPointer(nodeNamed), false),
	}, nil)
	nodeNamed.SetUnderlying(nodeStruct)

	proof = ca.ProveAcyclic(nodeNamed, nil, Allocation{})
	if proof.IsAcyclic {
		t.Errorf("expected Node { Next *Node } to be detected as cyclic, but was marked acyclic")
	}

	// 5. Allocation with cycle risk reason -> Cyclic!
	proof = ca.ProveAcyclic(dagNamed, nil, Allocation{
		Reasons: []string{"A self-referential store creates a cycle risk for deterministic reference counting."},
	})
	if proof.IsAcyclic {
		t.Error("expected allocation with cycle risk reason to be rejected")
	}
}
