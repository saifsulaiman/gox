package benchmarks

import (
	"testing"
)

type TreeNode struct {
	Value int
	Left  *TreeNode
	Right *TreeNode
}

func buildTree(depth int, val int) *TreeNode {
	if depth <= 0 {
		return nil
	}
	return &TreeNode{
		Value: val,
		Left:  buildTree(depth-1, val*2),
		Right: buildTree(depth-1, val*2+1),
	}
}

func sumTree(node *TreeNode) int {
	if node == nil {
		return 0
	}
	return node.Value + sumTree(node.Left) + sumTree(node.Right)
}

func BenchmarkBinaryTreeTraversal(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		root := buildTree(12, 1) // 2^12 - 1 = 4095 nodes
		s := sumTree(root)
		if s == 0 {
			b.Fatal("unexpected zero sum")
		}
	}
}
