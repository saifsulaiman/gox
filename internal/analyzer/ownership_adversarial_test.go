package analyzer

import (
	"strings"
	"testing"
)

// TestOwnershipInferenceAndAdversarialPatterns tests GOX v0.5 ownership-based
// deterministic reclamation, move/borrow inference, and destruction point insertion.
func TestOwnershipInferenceAndAdversarialPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/ownershiptest\n\ngo 1.24\n")
	writeTestFile(t, dir, "ownership.go", `package ownershiptest

import (
	"reflect"
	"unsafe"
)

// LargeBuffer exceeds the Go compiler's 64KB stack frame threshold,
// causing the Go compiler to report that &LargeBuffer escapes to heap (too large).
type LargeBuffer struct {
	Data [100000]byte
	ID   int
}

type Config struct {
	MaxRetries int
	TimeoutMs  int
	Endpoint   string
}

var globalConfigSink *Config

//go:noinline
func inspectBuffer(b *LargeBuffer) int {
	return int(b.Data[0]) + b.ID
}

// 1. Valid Unique Owned: Allocation with multiple branches and early returns.
// The allocation exceeds the stack threshold and escapes compiler stack placement.
// Stack proof is blocked by compiler escape cross-check (too large for stack).
// Region proof does not claim it (not returned to caller, not cyclic).
// Unique proof proves unique ownership and inserts destruction points on all branches.
func ProcessWithBranches(n int) int {
	buf := &LargeBuffer{ID: n}
	if n <= 0 {
		return 0 // early return branch 1 (no uses)
	}
	buf.Data[0] = byte(n)
	if n > 100 {
		return inspectBuffer(buf) * 2 // early return branch 2 (with last use)
	}
	return inspectBuffer(buf) // normal exit path (with last use)
}

// 2. Valid Borrow Semantics: Passed to callee, then used again locally.
func BorrowingWorkflow(n int) int {
	buf := &LargeBuffer{ID: n}
	sum := inspectBuffer(buf) // Borrowed by inspectBuffer
	buf.ID = sum + 1          // Used again after borrow
	return buf.ID
}

// 3. Valid Internal Move: Passed to consumer and never accessed again.
func MoveWorkflow(n int) int {
	buf := &LargeBuffer{ID: n}
	return inspectBuffer(buf) // Ownership moved to callee / last-use
}

// 4. Adversarial Hazard: Escaping to global state.
func EscapingGlobalStore(n int) {
	cfg := &Config{MaxRetries: n, TimeoutMs: 200, Endpoint: "https://global.internal"}
	globalConfigSink = cfg
}

// 5. Adversarial Hazard: Passing candidate to concurrent goroutine.
func EscapingGoroutine(n int) {
	cfg := &Config{MaxRetries: n, TimeoutMs: 300, Endpoint: "https://goroutine.internal"}
	go func() {
		_ = cfg.MaxRetries
	}()
}

// 6. Adversarial Hazard: Sending candidate over a channel.
func EscapingChannel(ch chan *Config, n int) {
	cfg := &Config{MaxRetries: n, TimeoutMs: 400, Endpoint: "https://channel.internal"}
	ch <- cfg
}

// 7. Adversarial Hazard: Unsafe pointer conversion.
func EscapingUnsafe(n int) uintptr {
	cfg := &Config{MaxRetries: n, TimeoutMs: 600, Endpoint: "https://unsafe.internal"}
	return uintptr(unsafe.Pointer(cfg))
}

// 8. Adversarial Hazard: Reflection inspection.
func EscapingReflection(n int) {
	cfg := &Config{MaxRetries: n, TimeoutMs: 700, Endpoint: "https://reflect.internal"}
	_ = reflect.ValueOf(cfg)
}
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}

	// 1. Verify ProcessWithBranches has unique-owned allocation with early returns
	t.Run("ValidUniqueOwnedWithEarlyReturns", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "ProcessWithBranches") && strings.Contains(d.Type, "LargeBuffer") {
				found = true
				if d.Strategy != StrategyUniqueOwned || d.Confidence != ConfidenceProven {
					t.Errorf("ProcessWithBranches LargeBuffer should be PROVEN UNIQUE_OWNED, got %s (%s)", d.Strategy, d.Confidence)
				}
				if len(d.DestructionPoints) < 2 {
					t.Errorf("expected at least 2 destruction points across branches, got %d: %+v",
						len(d.DestructionPoints), d.DestructionPoints)
				}
				hasEarlyReturn := false
				hasLastUseOrReturn := false
				for _, dp := range d.DestructionPoints {
					if dp.Kind == "EARLY_RETURN" {
						hasEarlyReturn = true
					}
					if dp.Kind == "LAST_USE" || dp.Kind == "PRE_RETURN" {
						hasLastUseOrReturn = true
					}
				}
				if !hasEarlyReturn {
					t.Errorf("expected EARLY_RETURN destruction point on early exit branch, got: %+v", d.DestructionPoints)
				}
				if !hasLastUseOrReturn {
					t.Errorf("expected LAST_USE or PRE_RETURN destruction point on active branch, got: %+v", d.DestructionPoints)
				}
			}
		}
		if !found {
			t.Error("could not find LargeBuffer allocation for ProcessWithBranches")
		}
	})

	// 2. Verify BorrowingWorkflow handles borrow without premature destruction
	t.Run("ValidBorrowSemantics", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "BorrowingWorkflow") && strings.Contains(d.Type, "LargeBuffer") {
				found = true
				if d.Strategy != StrategyUniqueOwned || d.Confidence != ConfidenceProven {
					t.Errorf("BorrowingWorkflow expected PROVEN UNIQUE_OWNED, got %s (%s)", d.Strategy, d.Confidence)
				}
				if len(d.DestructionPoints) == 0 {
					t.Error("BorrowingWorkflow missing destruction points")
				}
			}
		}
		if !found {
			t.Error("could not find LargeBuffer allocation for BorrowingWorkflow")
		}
	})

	// 3. Verify MoveWorkflow handles internal move
	t.Run("ValidMoveWorkflow", func(t *testing.T) {
		found := false
		for _, d := range result.Decisions {
			if strings.Contains(d.Function, "MoveWorkflow") && strings.Contains(d.Type, "LargeBuffer") {
				found = true
				if d.Strategy != StrategyUniqueOwned || d.Confidence != ConfidenceProven {
					t.Errorf("MoveWorkflow expected PROVEN UNIQUE_OWNED, got %s (%s)", d.Strategy, d.Confidence)
				}
				if len(d.DestructionPoints) == 0 {
					t.Error("MoveWorkflow missing destruction points")
				}
			}
		}
		if !found {
			t.Error("could not find LargeBuffer allocation for MoveWorkflow")
		}
	})

	// 4. Adversarial hazards: Verify strict rejection of unsafe escapes
	adversarialCases := []struct {
		fnName string
		hazard string
	}{
		{"EscapingGlobalStore", "global store"},
		{"EscapingGoroutine", "goroutine concurrency"},
		{"EscapingChannel", "channel send"},
		{"EscapingUnsafe", "unsafe.Pointer"},
		{"EscapingReflection", "reflection inspection"},
	}

	for _, tc := range adversarialCases {
		t.Run(tc.fnName, func(t *testing.T) {
			for _, d := range result.Decisions {
				if strings.Contains(d.Function, tc.fnName) && strings.Contains(d.Type, "Config") {
					if d.Strategy == StrategyUniqueOwned && d.Confidence == ConfidenceProven {
						t.Errorf("ADVERSARIAL HAZARD: %s (%s) was falsely proven UNIQUE_OWNED at %s",
							tc.fnName, tc.hazard, d.SourcePosition)
					}
					switch tc.fnName {
					case "EscapingGoroutine":
						if d.Strategy != StrategyTracingFallback && d.Strategy != StrategyARC {
							t.Errorf("expected TRACING_FALLBACK or ARC for %s, got %s", tc.fnName, d.Strategy)
						}
					case "EscapingGlobalStore":
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

	// 5. Verify StrategySummary counts reflect unique-owned promotions
	if result.StrategySummary == nil {
		t.Fatal("StrategySummary is nil")
	}
	if result.StrategySummary.ProvenUniqueOwnedCount < 1 {
		t.Errorf("expected at least 1 proven unique-owned allocation, got %d",
			result.StrategySummary.ProvenUniqueOwnedCount)
	}
}
