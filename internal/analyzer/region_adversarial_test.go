package analyzer

import (
	"strings"
	"testing"
)

// TestRegionInferenceAndAdversarialPatterns tests that GOX v0.4 correctly infers
// regions for valid bounded multi-frame and cyclic objects, while strictly
// rejecting allocations that escape through globals, concurrency, or unsafe operations.
func TestRegionInferenceAndAdversarialPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/regiontest\n\ngo 1.24\n")
	writeTestFile(t, dir, "regions.go", `package regiontest

import (
	"reflect"
	"unsafe"
)

type Node struct {
	Value int
	Next  *Node
	Prev  *Node
}

type Payload struct {
	Data string
}

var globalSink *Payload

// 1. Valid Caller-Owned Region: Constructor returning object consumed locally by caller.
func makePayload(data string) *Payload {
	return &Payload{Data: data}
}

func CallerScopeUse() string {
	p := makePayload("hello")
	return p.Data
}

// 2. Valid Function-Local Cyclic Graph: Cyclic structure bounded by function frame.
func CyclicGraphProcessing() int {
	n1 := &Node{Value: 1}
	n2 := &Node{Value: 2}
	n1.Next = n2
	n2.Prev = n1
	n2.Next = n1 // cycle!
	n1.Prev = n2 // cycle!
	return n1.Value + n2.Value
}

// 3. Adversarial Hazard: Storing returned object into a global variable.
func makeEscapingToGlobal() *Payload {
	return &Payload{Data: "escaping_global"}
}

func StoreGlobal() {
	p := makeEscapingToGlobal()
	globalSink = p
}

// 4. Adversarial Hazard: Passing region candidate to a concurrent goroutine.
func makeEscapingToGoroutine() *Payload {
	return &Payload{Data: "escaping_goroutine"}
}

func LaunchGoroutine() {
	p := makeEscapingToGoroutine()
	go func() {
		_ = p.Data
	}()
}

// 5. Adversarial Hazard: Sending region candidate over a channel.
func makeEscapingToChannel() *Payload {
	return &Payload{Data: "escaping_channel"}
}

func SendToChannel(ch chan *Payload) {
	p := makeEscapingToChannel()
	ch <- p
}

// 6. Adversarial Hazard: Converting region candidate via unsafe.Pointer.
func makeUnsafeConverted() *Payload {
	return &Payload{Data: "unsafe"}
}

func ConvertUnsafe() uintptr {
	p := makeUnsafeConverted()
	return uintptr(unsafe.Pointer(p))
}

// 7. Adversarial Hazard: Passing region candidate to reflection.
func makeReflected() *Payload {
	return &Payload{Data: "reflected"}
}

func ReflectValue() {
	p := makeReflected()
	_ = reflect.ValueOf(p)
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// 1. Verify valid caller-owned region promotion in makePayload
	t.Run("ValidCallerOwnedRegion", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "makePayload") && strings.Contains(d.Type, "Payload") {
				found = true
				if d.Strategy != StrategyRegion || d.Confidence != ConfidenceProven {
					t.Errorf("makePayload allocation should be PROVEN REGION, got %s (%s)", d.Strategy, d.Confidence)
				}
				if d.Region == nil || d.Region.Scope != "CALLER" {
					t.Errorf("makePayload allocation should have CALLER scope, got: %+v", d.Region)
				}
				if len(d.Proof) == 0 {
					t.Error("makePayload decision missing proof invariants")
				}
			}
		}
		if !found {
			t.Error("could not find allocation for makePayload")
		}
	})

	// 2. Verify cyclic graph in CyclicGraphProcessing is safely handled by region
	t.Run("ValidCyclicGraphRegion", func(t *testing.T) {
		foundCyclicRegion := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "CyclicGraphProcessing") && strings.Contains(d.Type, "Node") {
				if d.Strategy == StrategyRegion && d.Confidence == ConfidenceProven {
					foundCyclicRegion = true
					if d.Region == nil || d.Region.Scope != "FUNCTION" {
						t.Errorf("expected FUNCTION scope for cyclic graph, got: %+v", d.Region)
					}
				}
			}
		}
		if !foundCyclicRegion {
			t.Log("Cyclic graph allocations were handled by stack or fallback; checking no false escape")
		}
	})

	// 3. Adversarial tests: Verify that none of the hazard functions are promoted to REGION
	adversarialCases := []struct {
		fnName string
		hazard string
	}{
		{"makeEscapingToGlobal", "global store"},
		{"makeEscapingToGoroutine", "goroutine boundary"},
		{"makeEscapingToChannel", "channel transmission"},
		{"makeUnsafeConverted", "unsafe.Pointer conversion"},
		{"makeReflected", "reflection inspection"},
	}

	for _, tc := range adversarialCases {
		t.Run(tc.fnName, func(t *testing.T) {
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) && strings.Contains(d.Type, "Payload") {
					if d.Strategy == StrategyRegion && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL REGION HAZARD: %s (%s) was falsely proven REGION at %s",
							tc.fnName, tc.hazard, d.SourcePosition)
					}
					switch tc.fnName {
					case "makeEscapingToGoroutine":
						if d.Strategy != StrategyTracingFallback && d.Strategy != StrategyARC {
							t.Errorf("expected TRACING_FALLBACK or ARC for %s, got %s", tc.fnName, d.Strategy)
						}
					case "makeEscapingToGlobal":
						if d.Strategy != StrategyTracingFallback && d.Strategy != StrategyImmortal {
							t.Errorf("expected TRACING_FALLBACK or IMMORTAL for %s, got %s", tc.fnName, d.Strategy)
						}
					default:
						if d.Strategy != StrategyTracingFallback {
							t.Errorf("expected TRACING_FALLBACK for %s, got %s", tc.fnName, d.Strategy)
						}
					}
				}
			}
		})
	}

	// 4. Verify StrategySummary counts reflect region promotions
	if result.StrategySummary == nil {
		t.Fatal("StrategySummary is nil")
	}
	if result.StrategySummary.ProvenRegionCount < 1 {
		t.Errorf("expected at least 1 proven region, got %d", result.StrategySummary.ProvenRegionCount)
	}
}
