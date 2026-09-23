package analyzer

import (
	"strings"
	"testing"
)

// TestAll12AdversarialScenarios systematically verifies that 12 tricky
// language constructs and adversarial escape patterns are NEVER incorrectly
// marked as PROVEN STACK allocations.
func TestAll12AdversarialScenarios(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/adversarial12\n\ngo 1.24\n")
	writeTestFile(t, dir, "scenarios.go", `package adversarial12

import (
	"reflect"
	"sync"
	"unsafe"
)

// Global sinks
var (
	globalInterface interface{}
	globalMap       sync.Map
	globalSlice     []*int
	globalDeep      Level1
)

// 1. Interface escape
func Pattern1_InterfaceEscape() {
	p := new(int)
	*p = 1
	globalInterface = p
}

// 2. sync.Map storage
func Pattern2_SyncMapStorage() {
	p := new(int)
	*p = 2
	globalMap.Store("key", p)
}

// 3. Channel transfer
func Pattern3_ChannelTransfer(ch chan *int) {
	p := new(int)
	*p = 3
	ch <- p
}

// 4. Five levels of struct indirection
type Level5 struct{ Ptr *int }
type Level4 struct{ Next Level5 }
type Level3 struct{ Next Level4 }
type Level2 struct{ Next Level3 }
type Level1 struct{ Next Level2 }

func Pattern4_FiveLevelIndirection() {
	p := new(int)
	*p = 4
	globalDeep.Next.Next.Next.Next.Ptr = p
}

// 5. Closure return capture
func Pattern5_ClosureReturn() func() int {
	p := new(int)
	*p = 5
	return func() int {
		return *p
	}
}

// 6. Defer and return
func Pattern6_DeferAndReturn() *int {
	p := new(int)
	*p = 6
	defer func() {
		*p = 66
	}()
	return p
}

// 7. Reflection ValueOf
func Pattern7_Reflection() {
	p := new(int)
	*p = 7
	_ = reflect.ValueOf(p)
}

// 8. unsafe.Pointer conversion
func Pattern8_UnsafePointer() uintptr {
	p := new(int)
	*p = 8
	return uintptr(unsafe.Pointer(p))
}

// 9. Variadic sink
func variadicSink(args ...*int) {
	globalSlice = append(globalSlice, args...)
}
func Pattern9_VariadicEscape() {
	p := new(int)
	*p = 9
	variadicSink(p)
}

// 10. Slice appended to global slice
func Pattern10_SliceAppendGlobal() {
	s := make([]*int, 1)
	p := new(int)
	*p = 10
	s[0] = p
	globalSlice = append(globalSlice, s...)
}

// 11. Recursive self-referencing structure
type Node struct {
	Val  int
	Self *Node
}

func Pattern11_RecursiveSelfRef(depth int) *Node {
	if depth <= 0 {
		return nil
	}
	n := &Node{Val: depth}
	n.Self = n
	_ = Pattern11_RecursiveSelfRef(depth - 1)
	return n
}

// 12. Generic function returning pointer through type parameter
func identity[T any](x T) T {
	return x
}

func Pattern12_GenericReturn() *int {
	p := new(int)
	*p = 12
	return identity(p)
}

// Control function: A purely frame-local allocation that CAN be proven stack.
func SafeControl() int {
	p := new(int)
	*p = 100
	return *p
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	adversarialPatterns := []struct {
		fnName      string
		description string
	}{
		{"Pattern1_InterfaceEscape", "interface{} escape"},
		{"Pattern2_SyncMapStorage", "sync.Map storage"},
		{"Pattern3_ChannelTransfer", "channel send"},
		{"Pattern4_FiveLevelIndirection", "5-level deep global indirection"},
		{"Pattern5_ClosureReturn", "closure return capturing pointer"},
		{"Pattern7_Reflection", "reflection ValueOf"},
		{"Pattern8_UnsafePointer", "unsafe.Pointer conversion"},
		{"Pattern9_VariadicEscape", "variadic escape to global slice"},
		{"Pattern10_SliceAppendGlobal", "slice appended to global slice"},
		{"Pattern11_RecursiveSelfRef", "recursive self-referencing structure"},
		{"Pattern12_GenericReturn", "generic identity returning pointer"},
	}

	for _, tc := range adversarialPatterns {
		t.Run(tc.fnName, func(t *testing.T) {
			foundAlloc := false
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) {
					foundAlloc = true
					if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL VULNERABILITY DETECTED in %s (%s): allocation %s at %s was PROVEN STACK but must escape",
							tc.fnName, tc.description, d.AllocationID, d.SourcePosition)
					}
				}
			}
			if !foundAlloc {
				t.Errorf("expected to find allocations for %s", tc.fnName)
			}
		})
	}

	t.Run("Pattern6_DeferAndReturn", func(t *testing.T) {
		foundReturnedAlloc := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "Pattern6_DeferAndReturn") {
				// The new(int) allocation:
				if strings.Contains(d.Type, "*int") && strings.Contains(d.SourcePosition, "scenarios.go:62:") {
					foundReturnedAlloc = true
					if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
						t.Errorf("new(int) in Pattern6_DeferAndReturn was incorrectly proven STACK: %s", d.AllocationID)
					}
					// In v0.6, caller-returned allocations without global/concurrency escape
					// are proven REGION, ARC, or fallback, but must NEVER be STACK.
					if d.Strategy != StrategyTracingFallback && d.Strategy != StrategyRegion && d.Strategy != StrategyARC {
						t.Errorf("expected TRACING_FALLBACK, REGION, or ARC for returned pointer in Pattern6, got %s", d.Strategy)
					}
				}
			}
		}
		if !foundReturnedAlloc {
			t.Error("expected to find returned new(int) allocation in Pattern6_DeferAndReturn")
		}
	})

	// Verify the control function is indeed proven stack safe.
	controlProven := false
	for _, d := range result.Decisions {
		if strings.Contains(d.Function, "SafeControl") && d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
			controlProven = true
			break
		}
	}
	if !controlProven {
		t.Error("SafeControl allocation was expected to be PROVEN STACK")
	}
}
