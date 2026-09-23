package analyzer

import (
	"fmt"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// immortalProofResult holds the outcome of attempting to prove that an allocation
// has process lifetime and can be managed without garbage collection or reference counting.
type immortalProofResult struct {
	proven          bool
	confidence      Confidence
	strategy        MemoryStrategy
	immortalInfo    *ImmortalInfo
	proof           []string
	blockingReasons []BlockingReason
}

// immortalProofEngine evaluates whether an allocation belongs to process-lifetime
// static memory, package globals, or immutable lookup tables.
type immortalProofEngine struct {
	engine *ownershipEngine
}

func newImmortalProofEngine(engine *ownershipEngine) *immortalProofEngine {
	return &immortalProofEngine{engine: engine}
}

// isInitFunction checks if a function represents package-level initialization.
func isInitFunction(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	name := fn.Name()
	return name == "init" || strings.HasPrefix(name, "init#") || strings.Contains(name, ".init")
}

// findGlobalRoot finds the *ssa.Global variable that an address originates from.
func findGlobalRoot(value ssa.Value, seen map[ssa.Value]bool) *ssa.Global {
	if value == nil || seen[value] {
		return nil
	}
	seen[value] = true
	switch v := value.(type) {
	case *ssa.Global:
		return v
	case *ssa.UnOp:
		return findGlobalRoot(v.X, seen)
	case *ssa.FieldAddr:
		return findGlobalRoot(v.X, seen)
	case *ssa.IndexAddr:
		return findGlobalRoot(v.X, seen)
	case *ssa.Slice:
		return findGlobalRoot(v.X, seen)
	case *ssa.ChangeType:
		return findGlobalRoot(v.X, seen)
	case *ssa.Convert:
		return findGlobalRoot(v.X, seen)
	}
	return nil
}

// prove evaluates whether an allocation can safely use StrategyImmortal.
func (ipe *immortalProofEngine) prove(site *allocationSite, alloc Allocation) immortalProofResult {
	result := immortalProofResult{
		confidence: ConfidenceUnproven,
		strategy:   StrategyTracingFallback,
	}

	// Gate 1: Inherent heap types that cannot be immortal
	if site.kind == "channel" {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "inherent_channel_heap",
			Description: "Channels require runtime synchronization and wait queues; cannot be static memory.",
			Construct:   "channel",
		})
		return result
	}

	// Gate 2: Absolute escape blockers (channels, unsafe pointers, reflection, Cgo/FFI)
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "channel"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "channel_escape",
				Description: "Allocation participates in channel communication; cannot be static memory.",
				Construct:   "channel",
			})
			return result

		case strings.Contains(lower, "unsafe-pointer"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "unsafe_pointer",
				Description: "Unsafe-pointer conversion obscures static memory integrity.",
				Construct:   "unsafe.Pointer",
			})
			return result

		case strings.Contains(lower, "reflection"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "reflection_escape",
				Description: "Reflection inspection allows unmodeled dynamic mutation or aliasing.",
				Construct:   "reflection",
			})
			return result

		case strings.Contains(lower, "cgo") || strings.Contains(lower, "foreign-function"):
			result.blockingReasons = append(result.blockingReasons, BlockingReason{
				Kind:        "ffi_boundary",
				Description: "FFI boundary prevents proving memory lifecycle against external deallocation.",
				Construct:   "ffi",
			})
			return result
		}
	}

	// Gate 3: Process Lifetime Verification
	// Must be stored into a package global OR allocated inside an init() function / var init.
	isGlobal := alloc.Ownership == OwnershipGlobal || alloc.Lifetime == LifetimeGlobal
	inInit := site.fn != nil && isInitFunction(site.fn)

	// Check if SSA value is stored to a global
	var globalTarget *ssa.Global
	if site.value != nil && site.value.Referrers() != nil {
		for _, ref := range *site.value.Referrers() {
			if store, ok := ref.(*ssa.Store); ok && store.Val == site.value {
				if g := findGlobalRoot(store.Addr, make(map[ssa.Value]bool)); g != nil {
					globalTarget = g
					isGlobal = true
					break
				}
			}
		}
	}

	if !isGlobal && !inInit {
		result.blockingReasons = append(result.blockingReasons, BlockingReason{
			Kind:        "not_process_lifetime",
			Description: "Allocation is not rooted in global state or package initialization; cannot prove immortal.",
		})
		return result
	}

	// Gate 4: Post-Init Mutability Analysis
	// Check if this allocation or its global target is ever mutated outside init functions.
	initScope := "package_var"
	if inInit {
		initScope = site.fn.Name()
	} else if site.fn != nil {
		initScope = site.fn.Name()
	}

	globalVarName := ""
	if globalTarget != nil {
		globalVarName = globalTarget.Name()
	}

	mutatedOutsideInit := ipe.checkMutatedOutsideInit(site, globalTarget)

	isReadOnly := !mutatedOutsideInit
	placementKind := "STATIC_DATA"
	if isReadOnly {
		placementKind = "IMMUTABLE_RODATA"
	}

	// Gate 5: Invariant Synthesis & Proof Construction
	proof := []string{
		fmt.Sprintf("Allocation is rooted in process-lifetime scope (scope: %s, global: %s).", initScope, globalVarName),
		"Object is never deallocated prior to process exit; lifetime tau(alpha) = infinity.",
	}
	if isReadOnly {
		proof = append(proof, "Proven immutable: zero store instructions mutate this allocation outside package initialization.")
		proof = append(proof, "Eligible for write-protected rodada memory segment.")
	} else {
		proof = append(proof, "Mutable static singleton: fields may be updated at runtime, but container is immortal.")
	}
	proof = append(proof, "Immortal static allocation completely eliminates GC root tracing, sweep overhead, and reference counting.")

	result.proven = true
	result.confidence = ConfidenceProven
	result.strategy = StrategyImmortal
	result.proof = proof
	result.immortalInfo = &ImmortalInfo{
		GlobalVar:       globalVarName,
		InitScope:       initScope,
		IsReadOnly:      isReadOnly,
		PlacementKind:   placementKind,
		InvariantsProof: proof,
	}

	return result
}

// checkMutatedOutsideInit checks whether any store instruction writes to the allocation
// or through its global variable from a function that is not an init function.
func (ipe *immortalProofEngine) checkMutatedOutsideInit(site *allocationSite, global *ssa.Global) bool {
	seen := make(map[ssa.Value]bool)

	// 1. Check referrers of the allocation site value
	if site.value != nil && valueHasMutation(site.value, seen) {
		return true
	}

	// 2. Check if the global variable is mutated anywhere in the package outside init functions
	if global != nil && site.pkg != nil {
		if isGlobalMutatedInPackage(site.pkg, global) {
			return true
		}
	}

	return false
}

// isGlobalMutatedInPackage scans all non-init functions in the package for any stores to the global variable.
func isGlobalMutatedInPackage(pkg *ssa.Package, global *ssa.Global) bool {
	if pkg == nil || global == nil {
		return false
	}
	var checkFn func(*ssa.Function) bool
	checkFn = func(fn *ssa.Function) bool {
		if fn == nil {
			return false
		}
		if isInitFunction(fn) {
			return false
		}
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				if store, ok := instr.(*ssa.Store); ok {
					if findGlobalRoot(store.Addr, make(map[ssa.Value]bool)) == global {
						return true
					}
				}
			}
		}
		for _, anon := range fn.AnonFuncs {
			if checkFn(anon) {
				return true
			}
		}
		return false
	}

	for _, member := range pkg.Members {
		if fn, ok := member.(*ssa.Function); ok {
			if checkFn(fn) {
				return true
			}
		}
	}
	return false
}

// valueHasMutation recursively inspects SSA value referrers to detect any stores outside init.
func valueHasMutation(val ssa.Value, seen map[ssa.Value]bool) bool {
	if val == nil || seen[val] {
		return false
	}
	seen[val] = true
	refs := val.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		switch instr := ref.(type) {
		case *ssa.Store:
			// If storing into this address outside init
			if instr.Addr == val && isMutationInstruction(instr, nil) {
				return true
			}
		case *ssa.FieldAddr:
			if valueHasMutation(instr, seen) {
				return true
			}
		case *ssa.IndexAddr:
			if valueHasMutation(instr, seen) {
				return true
			}
		case *ssa.Slice:
			if valueHasMutation(instr, seen) {
				return true
			}
		case *ssa.ChangeType:
			if valueHasMutation(instr, seen) {
				return true
			}
		case *ssa.Convert:
			if valueHasMutation(instr, seen) {
				return true
			}
		case *ssa.UnOp:
			// Pointer dereference / load
			if valueHasMutation(instr, seen) {
				return true
			}
		}
	}
	return false
}

// isMutationInstruction checks if an instruction is a write outside initialization.
func isMutationInstruction(instr ssa.Instruction, allowedInitFn *ssa.Function) bool {
	store, ok := instr.(*ssa.Store)
	if !ok {
		return false
	}
	parent := store.Parent()
	if parent == nil {
		return false
	}
	if isInitFunction(parent) {
		return false
	}
	if allowedInitFn != nil && parent == allowedInitFn && isInitFunction(allowedInitFn) {
		return false
	}
	return true
}
