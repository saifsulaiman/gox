package region

import (
	"fmt"
	"strings"
)

// RegionScope defines the lifecycle boundary of an inferred memory region.
type RegionScope string

const (
	// ScopeFunction represents a region whose allocations are strictly bounded
	// by the execution of a single function. All objects in this region are
	// deallocated or reset in bulk when the function returns.
	ScopeFunction RegionScope = "FUNCTION"

	// ScopeCaller represents a region owned by a caller function. A callee
	// allocates into this region and returns the constructed object graph to
	// the caller, avoiding heap escape and tracing GC.
	ScopeCaller RegionScope = "CALLER"

	// ScopeLexical represents a region tied to a lexical block or loop body.
	// The region is reset or deallocated on every loop iteration or block exit.
	ScopeLexical RegionScope = "LEXICAL"

	// ScopeRequest represents a dynamic region bounded by a request handler,
	// transaction, or job lifecycle (e.g. HTTP request handling).
	ScopeRequest RegionScope = "REQUEST"

	// ScopeNested represents a sub-region whose lifetime is strictly shorter
	// than and contained within a parent region.
	ScopeNested RegionScope = "NESTED"
)

// RegionOpKind identifies conceptual region operations in the GOX internal IR.
// These are compiler analysis concepts, not Go source syntax.
type RegionOpKind string

const (
	OpRegionCreate  RegionOpKind = "region.create"
	OpRegionAlloc   RegionOpKind = "region.alloc"
	OpRegionBorrow  RegionOpKind = "region.borrow"
	OpRegionEscape  RegionOpKind = "region.escape"
	OpRegionReset   RegionOpKind = "region.reset"
	OpRegionDestroy RegionOpKind = "region.destroy"
)

// RegionOp represents an operation in the internal region IR.
type RegionOp struct {
	Kind      RegionOpKind `json:"kind"`
	RegionID  string       `json:"region_id"`
	Target    string       `json:"target,omitempty"`
	Type      string       `json:"type,omitempty"`
	Position  string       `json:"position,omitempty"`
	Detail    string       `json:"detail,omitempty"`
}

func (op RegionOp) String() string {
	var sb strings.Builder
	sb.WriteString(string(op.Kind))
	sb.WriteString("(")
	sb.WriteString(op.RegionID)
	if op.Target != "" {
		sb.WriteString(", target=")
		sb.WriteString(op.Target)
	}
	if op.Type != "" {
		sb.WriteString(", type=")
		sb.WriteString(op.Type)
	}
	if op.Position != "" {
		sb.WriteString(", at=")
		sb.WriteString(op.Position)
	}
	sb.WriteString(")")
	return sb.String()
}

// RegionDescriptor records the static and dynamic properties of an inferred region.
type RegionDescriptor struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Scope          RegionScope `json:"scope"`
	OwningFunction string      `json:"owning_function"`
	ParentRegionID string      `json:"parent_region_id,omitempty"`
	CreationPos    string      `json:"creation_position,omitempty"`
	DestructionPos string      `json:"destruction_position,omitempty"`
	// Allocations lists all allocation site IDs grouped into this region.
	Allocations    []string    `json:"allocations"`
	// Ops tracks the conceptual IR operations for this region.
	Ops            []RegionOp  `json:"ops,omitempty"`
	// Depth is the call-nesting level of the region (0 for root/outermost).
	Depth          int         `json:"depth"`
}

// Outlives reports whether this region is guaranteed to outlive other.
//
// Invariant:
// References may point from a shorter-lived region to a longer-lived region,
// but NEVER from a longer-lived region to a shorter-lived region.
func (r *RegionDescriptor) Outlives(other *RegionDescriptor) bool {
	if r == nil || other == nil {
		return false
	}
	if r.ID == other.ID {
		return true // same lifetime
	}
	// A parent region always outlives its nested sub-regions.
	if other.ParentRegionID == r.ID {
		return true
	}
	// A caller-owned region outlives a function-local region of its callee.
	if r.Scope == ScopeCaller && (other.Scope == ScopeFunction || other.Scope == ScopeLexical) {
		return true
	}
	// ScopeRequest outlives function-local and lexical scopes within it.
	if r.Scope == ScopeRequest && (other.Scope == ScopeFunction || other.Scope == ScopeLexical) {
		return true
	}
	// Lower depth implies earlier in the call stack / longer lifetime.
	if r.Depth < other.Depth {
		return true
	}
	return false
}

// CheckReferenceLegality validates that a reference from sourceRegion to
// targetRegion does not violate the region outlive ordering theorem.
// Returns an error explaining the violation if illegal.
func CheckReferenceLegality(sourceRegion, targetRegion *RegionDescriptor) error {
	if sourceRegion == nil || targetRegion == nil {
		return nil
	}
	if sourceRegion.ID == targetRegion.ID {
		return nil // intra-region references are always valid, even with cycles!
	}
	// If sourceRegion outlives targetRegion, storing targetRegion into sourceRegion
	// would result in a dangling pointer when targetRegion is destroyed first.
	if sourceRegion.Outlives(targetRegion) {
		return fmt.Errorf("dangling reference violation: longer-lived region %q (%s) cannot hold reference to shorter-lived region %q (%s)",
			sourceRegion.ID, sourceRegion.Scope, targetRegion.ID, targetRegion.Scope)
	}
	return nil
}
