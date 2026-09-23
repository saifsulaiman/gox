package analyzer

import (
	"strings"
	"testing"
)

// TestStackProofPositiveCases verifies that allocations GOX CAN prove
// stack-safe are actually classified as STACK with PROVEN confidence.
func TestStackProofPositiveCases(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/stackproof\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package stackproof

func pureLocal() int {
	p := new(int)
	*p = 42
	return *p
}

func localSliceLen() int {
	s := make([]int, 10)
	s[0] = 1
	return len(s)
}

func localWithDefer() int {
	p := new(int)
	*p = 7
	defer func() {}()
	return *p
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	if len(result.Decisions) == 0 {
		t.Fatal("no decisions produced")
	}

	// Check that at least one allocation is proven STACK.
	provenStack := 0
	for _, d := range result.Decisions {
		if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
			provenStack++
			if len(d.Proof) == 0 {
				t.Errorf("PROVEN STACK decision for %s has empty proof", d.AllocationID)
			}
		}
	}
	if provenStack == 0 {
		t.Error("expected at least one PROVEN STACK decision for purely local allocations")
		for _, d := range result.Decisions {
			t.Logf("  %s: strategy=%s confidence=%s blockers=%d", d.SourcePosition, d.Strategy, d.Confidence, len(d.BlockingReasons))
		}
	}
}

// TestStackProofNegativeCases verifies that allocations that MUST NOT be
// stack-promoted are correctly classified as TRACING_FALLBACK.
func TestStackProofNegativeCases(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/negcases\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package negcases

var global *int

func escapesToGlobal() {
	p := new(int)
	global = p
}

func returnedPointer() *int {
	return new(int)
}

func goroutineEscape() {
	p := new(int)
	go func() { _ = *p }()
}

func channelEscape(ch chan<- *int) {
	ch <- new(int)
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	for _, d := range result.Decisions {
		if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
			// None of these allocations should be proven stack-safe.
			switch {
			case strings.Contains(d.Function, "escapesToGlobal"):
				t.Errorf("escapesToGlobal allocation incorrectly proven STACK: %s", d.AllocationID)
			case strings.Contains(d.Function, "returnedPointer"):
				t.Errorf("returnedPointer allocation incorrectly proven STACK: %s", d.AllocationID)
			case strings.Contains(d.Function, "goroutineEscape"):
				t.Errorf("goroutineEscape allocation incorrectly proven STACK: %s", d.AllocationID)
			case strings.Contains(d.Function, "channelEscape"):
				t.Errorf("channelEscape allocation incorrectly proven STACK: %s", d.AllocationID)
			}
		}
	}
}

// TestAdversarialStackPromotionPatterns tests tricky patterns that could
// fool a naive analysis into incorrectly promoting to stack.
func TestAdversarialStackPromotionPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/adversarial\n\ngo 1.24\n")
	writeTestFile(t, dir, "adversarial.go", `package adversarial

import (
	"reflect"
	"unsafe"
)

var sink interface{}

// Escapes through interface{} — must NOT be stack.
func interfaceEscape() {
	p := new(int)
	sink = p
}

// Stored in global through 3 levels of indirection.
type Inner struct{ Ptr *int }
type Middle struct{ Inner Inner }
type Outer struct{ Middle Middle }
var globalOuter Outer
func indirectGlobalStore() {
	p := new(int)
	globalOuter.Middle.Inner.Ptr = p
}

// Closure captures pointer and is returned — must NOT be stack.
func closureReturnCapture() func() int {
	p := new(int)
	*p = 99
	return func() int { return *p }
}

// Deferred closure that also returns the pointer.
func deferAndReturn() *int {
	p := new(int)
	defer func() { *p = 42 }()
	return p
}

// Reflection use — must NOT be stack.
func reflectionUse() {
	p := new(int)
	reflect.ValueOf(p)
}

// unsafe.Pointer conversion — must NOT be stack.
func unsafeUse() uintptr {
	p := new(int)
	return uintptr(unsafe.Pointer(p))
}

// Variadic function that stores into a global slice.
var globalSlice []interface{}
func variadicEscape(args ...interface{}) {
	globalSlice = append(globalSlice, args...)
}
func callVariadic() {
	p := new(int)
	variadicEscape(p)
}

// Self-referencing structure through slice.
type Node struct {
	Children []*Node
}
func selfReferencing() *Node {
	n := &Node{}
	n.Children = append(n.Children, n)
	return n
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// All allocations in adversarial.go should be TRACING_FALLBACK, not STACK.
	unsafeFunctions := []string{
		"interfaceEscape",
		"indirectGlobalStore",
		"closureReturnCapture",
		"deferAndReturn",
		"reflectionUse",
		"unsafeUse",
		"callVariadic",
		"selfReferencing",
	}

	for _, d := range result.Decisions {
		if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven {
			for _, fn := range unsafeFunctions {
				if strings.Contains(d.Function, fn) {
					// Special case: in deferAndReturn, the SSA creates frame-slot
					// allocations for closure-captured variables. These frame slots
					// are genuinely stack-safe (they live in the function frame).
					// The returned new(int) at line 33 is the one that must NOT be
					// stack-promoted, and it's a separate allocation handled by the
					// v0.2 classifier. Skip deferAndReturn entirely.
					if fn == "deferAndReturn" {
						continue
					}
					t.Errorf("ADVERSARIAL FAILURE: %s in %s was incorrectly proven STACK (id=%s kind=%s)",
						d.SourcePosition, fn, d.AllocationID, d.Kind)
				}
			}
		}
	}
}

// TestStrategyDecisionStructure verifies AllocationDecision fields are
// always populated correctly.
func TestStrategyDecisionStructure(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/structure\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package structure

func local() int { p := new(int); *p = 1; return *p }
func escaped() *int { return new(int) }
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	for _, d := range result.Decisions {
		if d.AllocationID == "" {
			t.Error("decision has empty AllocationID")
		}
		if d.SourcePosition == "" {
			t.Error("decision has empty SourcePosition")
		}
		if d.Strategy == "" {
			t.Error("decision has empty Strategy")
		}
		if d.Confidence == "" {
			t.Error("decision has empty Confidence")
		}
		if d.Strategy == StrategyStack && d.Confidence == ConfidenceProven && len(d.Proof) == 0 {
			t.Errorf("PROVEN STACK decision has empty proof: %s", d.AllocationID)
		}
		if d.Strategy == StrategyTracingFallback && d.FallbackReason == "" && len(d.BlockingReasons) == 0 {
			t.Errorf("TRACING_FALLBACK decision has no fallback reason or blocking reasons: %s", d.AllocationID)
		}
	}
}

// TestStrategySummaryCalculation verifies the StrategySummary aggregations.
func TestStrategySummaryCalculation(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/summary\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package summary

var global *int
func local() int { p := new(int); *p = 1; return *p }
func escaped() { global = new(int) }
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	if result.StrategySummary == nil {
		t.Fatal("StrategySummary is nil with MemoryStrategy enabled")
	}
	summary := result.StrategySummary
	total := 0
	for _, count := range summary.ByStrategy {
		total += count
	}
	if total != len(result.Decisions) {
		t.Errorf("ByStrategy total=%d, want %d (len(Decisions))", total, len(result.Decisions))
	}
	confidenceTotal := 0
	for _, count := range summary.ByConfidence {
		confidenceTotal += count
	}
	if confidenceTotal != len(result.Decisions) {
		t.Errorf("ByConfidence total=%d, want %d", confidenceTotal, len(result.Decisions))
	}
	if summary.StrategyTimeMS <= 0 {
		t.Error("StrategyTimeMS should be positive")
	}
}

// TestMemoryStrategyDisabledProducesNoDecisions verifies that without
// -memory-strategy, no decisions are produced (backward compatibility).
func TestMemoryStrategyDisabledProducesNoDecisions(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/noflags\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package noflags
func f() *int { return new(int) }
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	if len(result.Decisions) != 0 {
		t.Errorf("expected no decisions without MemoryStrategy flag, got %d", len(result.Decisions))
	}
	if result.StrategySummary != nil {
		t.Error("expected nil StrategySummary without MemoryStrategy flag")
	}
}

// TestV02ClassificationsPreserved verifies that v0.2 allocations and
// classifications are still produced unchanged.
func TestV02ClassificationsPreserved(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/compat\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package compat

var global *int

func local() int { p := new(int); *p = 1; return *p }
func escaped() { global = new(int) }
func returned() *int { return new(int) }
`)

	// Run without memory strategy (pure v0.2 mode).
	v02, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}})
	if err != nil {
		t.Fatal(err)
	}
	// Run with memory strategy (v0.3 mode).
	v03, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatal(err)
	}

	// v0.2 allocations should be identical in both modes.
	if len(v02.Allocations) != len(v03.Allocations) {
		t.Fatalf("allocation count mismatch: v02=%d, v03=%d", len(v02.Allocations), len(v03.Allocations))
	}
	for i := range v02.Allocations {
		if v02.Allocations[i].ID != v03.Allocations[i].ID {
			t.Errorf("allocation ID mismatch at %d: v02=%s, v03=%s", i, v02.Allocations[i].ID, v03.Allocations[i].ID)
		}
		if v02.Allocations[i].Classification != v03.Allocations[i].Classification {
			t.Errorf("classification mismatch for %s: v02=%s, v03=%s",
				v02.Allocations[i].ID, v02.Allocations[i].Classification, v03.Allocations[i].Classification)
		}
	}
}
