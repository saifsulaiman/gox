package transform

import (
	"fmt"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func BenchmarkTransformSource(b *testing.B) {
	src := `package main

type Config struct {
	Host string
	Port int
}

type Node struct {
	Val int
	Next *Node
}

var GlobalConfig *Config

func init() {
	GlobalConfig = &Config{Host: "0.0.0.0", Port: 9000}
}

func Process(val int) int {
	item := &Node{Val: val}
	return item.Val
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "main.go:16:17",
				Strategy:       analyzer.StrategyImmortal,
				Confidence:     analyzer.ConfidenceProven,
				Immortal: &analyzer.ImmortalInfo{
					GlobalVar:     "GlobalConfig",
					InitScope:     "init",
					IsReadOnly:    true,
					PlacementKind: "IMMUTABLE_RODATA",
				},
			},
			{
				SourcePosition: "main.go:20:10",
				Strategy:       analyzer.StrategyRegion,
				Confidence:     analyzer.ConfidenceProven,
				Region: &analyzer.RegionInfo{
					RegionID:   "region:1",
					Scope:      "FUNCTION",
					OwningFunc: "Process",
				},
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)

	for b.Loop() {
		_, _, err := transformer.TransformSource(src, "main.go")
		if err != nil {
			b.Fatalf("transform: %v", err)
		}
	}
}

func BenchmarkTransformLargeFile(b *testing.B) {
	var body string
	var decisions []analyzer.AllocationDecision
	for i := 0; i < 100; i++ {
		body += fmt.Sprintf("\tobj%d := &Config{Host: \"test\", Port: %d}\n", i, i)
		decisions = append(decisions, analyzer.AllocationDecision{
			SourcePosition: fmt.Sprintf("main.go:%d:10", i+6),
			Strategy:       analyzer.StrategyUniqueOwned,
			Confidence:     analyzer.ConfidenceProven,
		})
	}

	src := fmt.Sprintf("package main\n\ntype Config struct {\n\tHost string\n\tPort int\n}\n\nfunc Run() {\n%s}\n", body)

	result := &analyzer.Result{Decisions: decisions}
	transformer := NewTransformer(TransformConfig{}, result)

	for b.Loop() {
		_, _, err := transformer.TransformSource(src, "main.go")
		if err != nil {
			b.Fatalf("transform: %v", err)
		}
	}
}
