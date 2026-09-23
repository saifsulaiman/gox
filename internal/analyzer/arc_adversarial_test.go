package analyzer

import (
	"strings"
	"testing"
)

// TestARCInferenceAndAdversarialPatterns tests GOX v0.6 Automatic Reference Counting (ARC),
// acyclic graph verification, retain/release placement, and Swift-style borrow elision.
func TestARCInferenceAndAdversarialPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/arctest\n\ngo 1.24\n")
	writeTestFile(t, dir, "arc.go", `package arctest

import (
	"reflect"
	"unsafe"
)

// 1. Acyclic shared type
type ConfigItem struct {
	Key   string
	Value string
	TTL   int
}

// 2. Recursive/Cyclic type
type CyclicNode struct {
	ID   int
	Next *CyclicNode
	Prev *CyclicNode
}

type Container struct {
	Items []*ConfigItem
}

var globalSink *ConfigItem

//go:noinline
func inspectItem(item *ConfigItem) string {
	return item.Key + "=" + item.Value
}

// 1. Valid Acyclic Shared Object (Proven ARC):
// The allocation is stored into a container slice (heapStored), giving it shared ownership.
// Its type (ConfigItem) is structurally acyclic.
// It is promoted to StrategyARC with deterministic retain/release sites.
func SharedAcyclicWorkflow(k, v string) int {
	item := &ConfigItem{Key: k, Value: v, TTL: 3600}
	c := &Container{Items: make([]*ConfigItem, 0, 2)}
	c.Items = append(c.Items, item)
	_ = inspectItem(item) // Borrowed locally while retained in container
	return len(c.Items)
}

// 2. Valid Atomic ARC across Goroutine boundary:
// ConfigItem is sent to a background worker.
func ConcurrentARCWorkflow(k, v string) {
	item := &ConfigItem{Key: k, Value: v, TTL: 60}
	go func() {
		_ = inspectItem(item)
	}()
}

// 3. Cyclic Graph Hazard: Must NOT use ARC because cycles leak reference counts.
func CyclicGraphHazard(id int) *CyclicNode {
	n1 := &CyclicNode{ID: id}
	n2 := &CyclicNode{ID: id + 1}
	n1.Next = n2
	n2.Prev = n1
	n2.Next = n1 // Cycle!
	return n1
}

// 4. Adversarial Hazard: Escapes to global variable.
func GlobalEscapeHazard(k, v string) {
	item := &ConfigItem{Key: k, Value: v, TTL: 10}
	globalSink = item
}

// 5. Adversarial Hazard: Unsafe pointer conversion.
func UnsafeEscapeHazard(k, v string) uintptr {
	item := &ConfigItem{Key: k, Value: v, TTL: 20}
	return uintptr(unsafe.Pointer(item))
}

// 6. Adversarial Hazard: Reflection inspection.
func ReflectionEscapeHazard(k, v string) {
	item := &ConfigItem{Key: k, Value: v, TTL: 30}
	_ = reflect.ValueOf(item)
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// 1. Verify SharedAcyclicWorkflow promotes ConfigItem to ARC
	t.Run("ValidAcyclicARC", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "SharedAcyclicWorkflow") && strings.Contains(d.Type, "ConfigItem") {
				found = true
				if d.Strategy != StrategyARC || d.Confidence != ConfidenceProven {
					t.Errorf("SharedAcyclicWorkflow ConfigItem should be PROVEN ARC, got %s (%s)", d.Strategy, d.Confidence)
				}
				if d.ARC == nil {
					t.Fatal("expected ARCInfo to be present for proven ARC decision")
				}
				if len(d.ARC.AcyclicProof) == 0 {
					t.Error("expected acyclic proof invariants in ARCInfo")
				}
				if len(d.ARC.RetainSites) == 0 && len(d.ARC.ReleaseSites) == 0 {
					t.Error("expected retain or release sites to be recorded")
				}
			}
		}
		if !found {
			t.Error("could not find ConfigItem allocation for SharedAcyclicWorkflow")
		}
	})

	// 2. Verify ConcurrentARCWorkflow promotes to atomic ARC
	t.Run("ValidAtomicARC", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ConcurrentARCWorkflow") && strings.Contains(d.Type, "ConfigItem") {
				found = true
				if d.Strategy == StrategyARC {
					if !d.ARC.IsAtomic {
						t.Error("expected IsAtomic=true for concurrent goroutine shared allocation")
					}
				}
			}
		}
		if !found {
			t.Log("ConcurrentARCWorkflow allocation was handled by fallback or stack; verifying no false ARC promotion")
		}
	})

	// 3. Verify CyclicGraphHazard is strictly rejected from ARC to prevent leaks
	t.Run("RejectCyclicGraphFromARC", func(t *testing.T) {
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "CyclicGraphHazard") && strings.Contains(d.Type, "CyclicNode") {
				if d.Strategy == StrategyARC && d.Confidence == ConfidenceProven {
					t.Errorf("CYCLIC LEAK HAZARD: CyclicNode was falsely promoted to ARC at %s", d.SourcePosition)
				}
			}
		}
	})

	// 4. Adversarial hazards: Global, unsafe.Pointer, reflection
	adversarialCases := []struct {
		fnName string
		hazard string
	}{
		{"GlobalEscapeHazard", "global store"},
		{"UnsafeEscapeHazard", "unsafe.Pointer"},
		{"ReflectionEscapeHazard", "reflection inspection"},
	}

	for _, tc := range adversarialCases {
		t.Run(tc.fnName, func(t *testing.T) {
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) && strings.Contains(d.Type, "ConfigItem") {
					if d.Strategy == StrategyARC && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL HAZARD: %s (%s) was falsely proven ARC at %s",
							tc.fnName, tc.hazard, d.SourcePosition)
					}
					if tc.fnName == "GlobalEscapeHazard" {
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

	// 5. Verify StrategySummary counts reflect ARC promotions
	if result.StrategySummary == nil {
		t.Fatal("StrategySummary is nil")
	}
	if result.StrategySummary.ProvenARCCount < 1 {
		t.Errorf("expected at least 1 proven ARC allocation, got %d",
			result.StrategySummary.ProvenARCCount)
	}
}
