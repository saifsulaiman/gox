package analyzer

import (
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// weakProofResult holds the outcome of attempting to prove deterministic
// cycle-breaking weak reference management for an allocation.
type weakProofResult struct {
	proven          bool
	confidence      Confidence
	strategy        MemoryStrategy
	weakInfo        *WeakInfo
	proof           []string
	blockingReasons []BlockingReason
}

// weakProofEngine performs static cycle-breaking, asymmetric back-pointer inference,
// and upgrade/downgrade placement for cyclic data structures.
type weakProofEngine struct {
	engine        *ownershipEngine
	cycleAnalyzer *CycleAnalyzer
}

func newWeakProofEngine(engine *ownershipEngine, cycleAnalyzer *CycleAnalyzer) *weakProofEngine {
	if cycleAnalyzer == nil {
		cycleAnalyzer = newCycleAnalyzer()
	}
	return &weakProofEngine{
		engine:        engine,
		cycleAnalyzer: cycleAnalyzer,
	}
}

// prove evaluates whether an allocation with potential reference cycles can be safely
// broken into an acyclic strong ownership graph via non-owning weak references.
func (wp *weakProofEngine) prove(site *allocationSite, alloc Allocation) weakProofResult {
	result := weakProofResult{
		confidence: ConfidenceUnproven,
		strategy:   StrategyTracingFallback,
	}

	// Gate 1: Inherent heap types that cannot be reference counted
	if site.kind == "channel" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_channel_heap",
			Description: "Channels coordinate independently scheduled concurrency and require runtime management.",
			Construct:   "channel",
		})
		return result
	}

	// Gate 2: Absolute escape blockers (globals, channels, unsafe pointers, reflection, FFI)
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "global state"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "global_store",
				Description: "Allocation is stored in global state; weak reference lifecycle cannot track process lifetime.",
				Construct:   "global",
			})
			return result

		case strings.Contains(lower, "channel communication") || strings.Contains(lower, "channel"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "channel_escape",
				Description: "Allocation participates in channel communication and requires runtime tracing GC.",
				Construct:   "channel",
			})
			return result

		case strings.Contains(lower, "unsafe-pointer"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "unsafe_pointer",
				Description: "Unsafe-pointer conversion obscures weak reference tracking and upgrade safety.",
				Construct:   "unsafe.Pointer",
			})
			return result

		case strings.Contains(lower, "reflection"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "reflection_escape",
				Description: "Reflection inspection can retain references outside visible weak reference tracking.",
				Construct:   "reflection",
			})
			return result

		case strings.Contains(lower, "cgo") || strings.Contains(lower, "foreign-function"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ffi_boundary",
				Description: "FFI boundary prevents tracking external weak references.",
				Construct:   "ffi",
			})
			return result

		case strings.Contains(lower, "open-world dynamic dispatch"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "open_world_dispatch",
				Description: "Open-world dynamic dispatch may reach unknown implementations that leak or duplicate references.",
				Construct:   "interface_dispatch",
			})
			return result
		}
	}

	// Gate 3: SSA Value and Type Availability
	if site.value == nil {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "no_ssa_value",
			Description: "No SSA value available to verify cycle-breaking properties.",
		})
		return result
	}

	// Gate 4: Cycle-Breaking Inference
	breakResult := wp.cycleAnalyzer.BreakCycle(site.value.Type(), site, alloc)
	if !breakResult.CanBreak {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "unbreakable_cycle",
			Description: breakResult.Reason,
			Construct:   "cycle",
		})
		return result
	}

	// Gate 5: Determine Atomic vs Non-Atomic mode
	isAtomic := false
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "goroutine") || strings.Contains(lower, "concurrent") {
			isAtomic = true
			break
		}
	}
	if alloc.Ownership == OwnershipConcurrent {
		isAtomic = true
	}

	// Gate 6: Upgrade and Downgrade Placement
	var downgradeSites []WeakSite
	var upgradeSites []WeakSite

	if site.fn != nil {
		for _, b := range site.fn.Blocks {
			for _, instr := range b.Instrs {
				switch ins := instr.(type) {
				case *ssa.Store:
					if fa, ok := ins.Addr.(*ssa.FieldAddr); ok {
						fieldName := wp.lookupFieldName(fa)
						if fieldName == breakResult.BrokenByField {
							downgradeSites = append(downgradeSites, WeakSite{
								Kind:        "DOWNGRADE",
								Position:    positionString(wp.engine.fset, ins.Pos()),
								Instruction: ins.String(),
								BlockID:     b.Index,
								Field:       fieldName,
							})
						}
					}
				case *ssa.FieldAddr:
					fieldName := wp.lookupFieldName(ins)
					if fieldName == breakResult.BrokenByField {
						// Address of weak field taken for read or dereference
						upgradeSites = append(upgradeSites, WeakSite{
							Kind:        "UPGRADE",
							Position:    positionString(wp.engine.fset, ins.Pos()),
							Instruction: ins.String(),
							BlockID:     b.Index,
							Field:       fieldName,
						})
					}
				case *ssa.Field:
					fieldName := wp.lookupFieldDirectName(ins)
					if fieldName == breakResult.BrokenByField {
						upgradeSites = append(upgradeSites, WeakSite{
							Kind:        "UPGRADE",
							Position:    positionString(wp.engine.fset, ins.Pos()),
							Instruction: ins.String(),
							BlockID:     b.Index,
							Field:       fieldName,
						})
					}
				}
			}
		}
	}

	// Default fallback site if no explicit stores found in current function
	if len(downgradeSites) == 0 {
		downgradeSites = append(downgradeSites, WeakSite{
			Kind:        "DOWNGRADE",
			Position:    alloc.Position,
			Instruction: fmt.Sprintf("back-pointer .%s initialized to nil/weak", breakResult.BrokenByField),
			BlockID:     0,
			Field:       breakResult.BrokenByField,
		})
	}

	weakInfo := &WeakInfo{
		CycleBrokenBy:      breakResult.BrokenByField,
		StrongTargetType:   breakResult.StrongTargetType,
		AcyclicStrongProof: breakResult.Invariants,
		DowngradeSites:     downgradeSites,
		UpgradeSites:       upgradeSites,
		IsAtomic:           isAtomic,
	}

	result.proven = true
	result.confidence = ConfidenceProven
	result.strategy = StrategyWeak
	result.weakInfo = weakInfo

	result.proof = append(result.proof, breakResult.Invariants...)
	if isAtomic {
		result.proof = append(result.proof, "Atomic dual-counter weak reference tracking enabled: thread-safe Upgrade() and Downgrade().")
	} else {
		result.proof = append(result.proof, "Non-atomic dual-counter weak reference tracking enabled: zero atomic overhead for thread-local structures.")
	}
	result.proof = append(result.proof,
		fmt.Sprintf("Placed %d downgrade site(s) and %d upgrade site(s) for weak field '%s'.", len(downgradeSites), len(upgradeSites), breakResult.BrokenByField),
		"Safe dereference guarantee: weak pointers checked via Upgrade(); returns nil safely if strong owner is reclaimed.",
	)

	return result
}

func (wp *weakProofEngine) lookupFieldName(fa *ssa.FieldAddr) string {
	if fa == nil || fa.X == nil {
		return ""
	}
	t := fa.X.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if st, ok := t.Underlying().(*types.Struct); ok {
		if fa.Field >= 0 && fa.Field < st.NumFields() {
			return st.Field(fa.Field).Name()
		}
	}
	return ""
}

func (wp *weakProofEngine) lookupFieldDirectName(f *ssa.Field) string {
	if f == nil || f.X == nil {
		return ""
	}
	t := f.X.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if st, ok := t.Underlying().(*types.Struct); ok {
		if f.Field >= 0 && f.Field < st.NumFields() {
			return st.Field(f.Field).Name()
		}
	}
	return ""
}
