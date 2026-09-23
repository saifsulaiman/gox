package transform

import (
	"strings"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestTransformImmortal(t *testing.T) {
	src := `package main

type Config struct {
	Host string
	Port int
}

var GlobalConfig *Config

func init() {
	GlobalConfig = &Config{Host: "localhost", Port: 8080}
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "config.go:11:17",
				Strategy:       analyzer.StrategyImmortal,
				Confidence:     analyzer.ConfidenceProven,
				Immortal: &analyzer.ImmortalInfo{
					GlobalVar:     "GlobalConfig",
					InitScope:     "init",
					IsReadOnly:    true,
					PlacementKind: "IMMUTABLE_RODATA",
				},
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "config.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if !modified {
		t.Fatal("expected file to be modified")
	}

	if !strings.Contains(out, "goxrt.AllocReadOnly(Config{") {
		t.Errorf("expected goxrt.AllocReadOnly in output:\n%s", out)
	}
	if !strings.Contains(out, `import goxrt "github.com/goxlang/gox/internal/runtime"`) {
		t.Errorf("expected runtime import in output:\n%s", out)
	}
}

func TestTransformRegion(t *testing.T) {
	src := `package main

type Item struct {
	ID int
}

func ProcessItems() int {
	item := &Item{ID: 42}
	return item.ID
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "process.go:8:10",
				Strategy:       analyzer.StrategyRegion,
				Confidence:     analyzer.ConfidenceProven,
				Region: &analyzer.RegionInfo{
					RegionID:   "region:1",
					Scope:      "FUNCTION",
					OwningFunc: "ProcessItems",
				},
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "process.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if !modified {
		t.Fatal("expected file to be modified")
	}

	if !strings.Contains(out, "_goxArena := goxrt.NewArena()") {
		t.Errorf("expected arena creation in output:\n%s", out)
	}
	if !strings.Contains(out, "defer goxrt.Free(_goxArena)") {
		t.Errorf("expected defer arena free in output:\n%s", out)
	}
	if !strings.Contains(out, "goxrt.AllocVal(_goxArena, Item{") {
		t.Errorf("expected AllocVal in output:\n%s", out)
	}
}

func TestTransformUniqueOwned(t *testing.T) {
	src := `package main

type Packet struct {
	Size int
}

func HandlePacket() int {
	p := &Packet{Size: 1024}
	return p.Size
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "packet.go:8:7",
				Strategy:       analyzer.StrategyUniqueOwned,
				Confidence:     analyzer.ConfidenceProven,
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "packet.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if !modified {
		t.Fatal("expected file to be modified")
	}

	if !strings.Contains(out, "goxrt.AllocUnique(Packet{") {
		t.Errorf("expected AllocUnique in output:\n%s", out)
	}
}

func TestTransformARC(t *testing.T) {
	src := `package main

type SharedNode struct {
	Val int
}

func BuildNode() {
	node := &SharedNode{Val: 99}
	_ = node
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "node.go:8:10",
				Strategy:       analyzer.StrategyARC,
				Confidence:     analyzer.ConfidenceProven,
				ARC: &analyzer.ARCInfo{
					IsAtomic: false,
				},
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "node.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if !modified {
		t.Fatal("expected file to be modified")
	}

	if !strings.Contains(out, "goxrt.AllocARC(SharedNode{Val: 99}, false)") {
		t.Errorf("expected AllocARC in output:\n%s", out)
	}
}

func TestTransformFallbackUntouched(t *testing.T) {
	src := `package main

type Node struct {
	Next *Node
}

func FallbackFn() *Node {
	return &Node{}
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "fallback.go:8:9",
				Strategy:       analyzer.StrategyTracingFallback,
				Confidence:     analyzer.ConfidenceUnproven,
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "fallback.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if modified {
		t.Errorf("expected file to remain untouched for fallback:\n%s", out)
	}
}

func TestTransformSliceRegion(t *testing.T) {
	src := `package main

type Item struct {
	ID int
}

func ProcessBatch() int {
	buf := make([]byte, 1024)
	items := make([]Item, 0, 50)
	return len(buf) + len(items)
}
`

	result := &analyzer.Result{
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "batch.go:8:9",
				Strategy:       analyzer.StrategyRegion,
				Confidence:     analyzer.ConfidenceProven,
				Region: &analyzer.RegionInfo{
					RegionID:   "region:buf",
					Scope:      "FUNCTION",
					OwningFunc: "ProcessBatch",
				},
			},
			{
				SourcePosition: "batch.go:9:11",
				Strategy:       analyzer.StrategyRegion,
				Confidence:     analyzer.ConfidenceProven,
				Region: &analyzer.RegionInfo{
					RegionID:   "region:items",
					Scope:      "FUNCTION",
					OwningFunc: "ProcessBatch",
				},
			},
		},
	}

	transformer := NewTransformer(TransformConfig{}, result)
	out, modified, err := transformer.TransformSource(src, "batch.go")
	if err != nil {
		t.Fatalf("TransformSource failed: %v", err)
	}
	if !modified {
		t.Fatal("expected file to be modified")
	}

	if !strings.Contains(out, "_goxArena := goxrt.NewArena()") {
		t.Errorf("expected arena creation in output:\n%s", out)
	}
	if !strings.Contains(out, "defer goxrt.Free(_goxArena)") {
		t.Errorf("expected defer arena free in output:\n%s", out)
	}
	if !strings.Contains(out, "goxrt.AllocSlice[byte](_goxArena, 1024, 1024)") {
		t.Errorf("expected AllocSlice[byte] in output:\n%s", out)
	}
	if !strings.Contains(out, "goxrt.AllocSlice[Item](_goxArena, 0, 50)") {
		t.Errorf("expected AllocSlice[Item] in output:\n%s", out)
	}
}
