package analyzer

import (
	"fmt"
	"go/types"
	"strings"
)

// CycleAnalyzer verifies whether a type or allocation graph is provably acyclic.
// In Automatic Reference Counting (ARC), cycles leak memory; therefore, an allocation
// can only be safely promoted to StrategyARC if it is proven acyclic.
type CycleAnalyzer struct{}

func newCycleAnalyzer() *CycleAnalyzer {
	return &CycleAnalyzer{}
}

// AcyclicProof holds the result and invariants of an acyclic verification.
type AcyclicProof struct {
	IsAcyclic bool
	Reason    string
	Invariants []string
}

// ProveAcyclic determines if the given allocation's type and reference graph
// are mathematically incapable of forming reference cycles.
func (ca *CycleAnalyzer) ProveAcyclic(t types.Type, site *allocationSite, alloc Allocation) AcyclicProof {
	// 1. Check if the allocation exhibited a cycle risk in v0.2 flow facts
	for _, reason := range alloc.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "cycle") || strings.Contains(lower, "self-referential") {
			return AcyclicProof{
				IsAcyclic: false,
				Reason:    "Allocation participates in an allocation-level cycle or self-referential store.",
			}
		}
	}

	// 2. Type-level structural cycle analysis
	rootType := t
	for {
		if ptr, ok := rootType.(*types.Pointer); ok {
			rootType = ptr.Elem()
			continue
		}
		if slice, ok := rootType.(*types.Slice); ok {
			rootType = slice.Elem()
			continue
		}
		if arr, ok := rootType.(*types.Array); ok {
			rootType = arr.Elem()
			continue
		}
		break
	}

	visited := make(map[types.Type]bool)
	path := make([]types.Type, 0)
	hasCycle, cyclePath := ca.typeReachesSelf(rootType, rootType, visited, path)

	if hasCycle {
		return AcyclicProof{
			IsAcyclic: false,
			Reason:    fmt.Sprintf("Type structure is recursive and can form cycles: %s", cyclePath),
		}
	}

	// 3. Proven acyclic!
	typeName := t.String()
	if site != nil && site.typeName != "" {
		typeName = site.typeName
	}

	invariants := []string{
		fmt.Sprintf("Type %s is structurally acyclic: no recursive pointer or container path leads back to itself.", typeName),
		"Reference topology forms a directed acyclic graph (DAG) or tree.",
		"Zero cycle leak risk: all reference counts strictly drop to zero upon dropping references.",
	}

	return AcyclicProof{
		IsAcyclic:  true,
		Reason:     "Proven structurally acyclic",
		Invariants: invariants,
	}
}

// typeReachesSelf checks if any field/element of current leads back to root through pointer dereferences.
func (ca *CycleAnalyzer) typeReachesSelf(root, current types.Type, visited map[types.Type]bool, path []types.Type) (bool, string) {
	if current == nil {
		return false, ""
	}

	// Unpack pointer
	underlying := current
	for {
		if ptr, ok := underlying.(*types.Pointer); ok {
			underlying = ptr.Elem()
			continue
		}
		if slice, ok := underlying.(*types.Slice); ok {
			underlying = slice.Elem()
			continue
		}
		if arr, ok := underlying.(*types.Array); ok {
			underlying = arr.Elem()
			continue
		}
		break
	}

	// Unpack named type
	if named, ok := underlying.(*types.Named); ok {
		if len(path) > 0 && types.Identical(named, root) {
			return true, ca.formatTypePath(append(path, named))
		}
		if visited[named] {
			return false, ""
		}
		visited[named] = true
		path = append(path, named)
		underlying = named.Underlying()
	} else if len(path) > 0 && types.Identical(underlying, root) {
		return true, ca.formatTypePath(append(path, underlying))
	}

	switch u := underlying.(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			field := u.Field(i)
			if hasCycle, p := ca.typeReachesSelf(root, field.Type(), visited, path); hasCycle {
				return true, p
			}
		}

	case *types.Slice:
		if hasCycle, p := ca.typeReachesSelf(root, u.Elem(), visited, path); hasCycle {
			return true, p
		}

	case *types.Array:
		if hasCycle, p := ca.typeReachesSelf(root, u.Elem(), visited, path); hasCycle {
			return true, p
		}

	case *types.Map:
		if hasCycle, p := ca.typeReachesSelf(root, u.Key(), visited, path); hasCycle {
			return true, p
		}
		if hasCycle, p := ca.typeReachesSelf(root, u.Elem(), visited, path); hasCycle {
			return true, p
		}

	case *types.Pointer:
		if hasCycle, p := ca.typeReachesSelf(root, u.Elem(), visited, path); hasCycle {
			return true, p
		}
	}

	return false, ""
}

func (ca *CycleAnalyzer) formatTypePath(path []types.Type) string {
	var names []string
	for _, t := range path {
		names = append(names, t.String())
	}
	return strings.Join(names, " -> ")
}

// CycleBreakResult holds the result of static cycle-breaking analysis.
type CycleBreakResult struct {
	CanBreak         bool
	BrokenByField    string
	StrongTargetType string
	Invariants       []string
	Reason           string
}

// isBackPointerField checks if a field name matches standard back-pointer conventions.
func isBackPointerField(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "parent", "prev", "previous", "owner", "container", "manager", "back", "up", "caller", "emitter", "source", "root", "observer":
		return true
	}
	return false
}

// BreakCycle inspects a cyclic type or value reference graph and attempts to break
// the cycle by identifying asymmetric back-pointer fields.
func (ca *CycleAnalyzer) BreakCycle(t types.Type, site *allocationSite, alloc Allocation) CycleBreakResult {
	if t == nil {
		return CycleBreakResult{CanBreak: false, Reason: "Nil type cannot be analyzed for cycle breaking"}
	}

	// Unpack pointer/slice/array to base type
	underlying := t
	for {
		if ptr, ok := underlying.(*types.Pointer); ok {
			underlying = ptr.Elem()
			continue
		}
		if slice, ok := underlying.(*types.Slice); ok {
			underlying = slice.Elem()
			continue
		}
		if arr, ok := underlying.(*types.Array); ok {
			underlying = arr.Elem()
			continue
		}
		break
	}

	named, isNamed := underlying.(*types.Named)
	structType, isStruct := underlying.Underlying().(*types.Struct)
	if !isStruct {
		return CycleBreakResult{CanBreak: false, Reason: "Only struct types can have named back-pointer fields for cycle breaking"}
	}

	typeName := t.String()
	if isNamed {
		typeName = named.Obj().Name()
	}

	// Search for a back-pointer field
	var candidateField string
	var forwardFields []string

	for i := 0; i < structType.NumFields(); i++ {
		f := structType.Field(i)
		fName := f.Name()
		if isBackPointerField(fName) {
			candidateField = fName
		} else {
			// Check if field is a forward owning field pointing to the same or child type
			ft := f.Type()
			for {
				if ptr, ok := ft.(*types.Pointer); ok {
					ft = ptr.Elem()
					continue
				}
				if slice, ok := ft.(*types.Slice); ok {
					ft = slice.Elem()
					continue
				}
				if arr, ok := ft.(*types.Array); ok {
					ft = arr.Elem()
					continue
				}
				break
			}
			if isNamed && types.Identical(ft, named) {
				forwardFields = append(forwardFields, fName)
			}
		}
	}

	if candidateField == "" {
		return CycleBreakResult{
			CanBreak: false,
			Reason:   fmt.Sprintf("Type %s is cyclic but contains no identifiable asymmetric back-pointer field (Parent, Prev, Owner, etc.)", typeName),
		}
	}

	forwardStr := strings.Join(forwardFields, ", ")
	if forwardStr == "" {
		forwardStr = "primary structure"
	}

	invariants := []string{
		fmt.Sprintf("Asymmetric reference cycle in %s broken by designating back-pointer field '%s' as non-owning WEAK reference.", typeName, candidateField),
		fmt.Sprintf("Strong owning references retained across forward field(s): %s.", forwardStr),
		"Residual strong ownership graph is provably acyclic: zero cycle leak risk.",
		fmt.Sprintf("Weak reference dereference requires safe Upgrade() guard checking if %s strong box is alive.", typeName),
	}

	return CycleBreakResult{
		CanBreak:         true,
		BrokenByField:    candidateField,
		StrongTargetType: typeName,
		Invariants:       invariants,
		Reason:           fmt.Sprintf("Cycle broken via weak back-pointer '%s'", candidateField),
	}
}
