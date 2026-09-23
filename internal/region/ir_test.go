package region

import (
	"strings"
	"testing"
)

func TestRegionDescriptorOutlives(t *testing.T) {
	parent := &RegionDescriptor{
		ID:    "reg:root",
		Scope: ScopeRequest,
		Depth: 0,
	}
	child := &RegionDescriptor{
		ID:             "reg:child",
		Scope:          ScopeNested,
		ParentRegionID: "reg:root",
		Depth:          1,
	}
	fnLocal := &RegionDescriptor{
		ID:    "reg:fn",
		Scope: ScopeFunction,
		Depth: 2,
	}

	// Root should outlive child and fnLocal
	if !parent.Outlives(child) {
		t.Error("parent should outlive child")
	}
	if !parent.Outlives(fnLocal) {
		t.Error("parent should outlive fnLocal")
	}
	// Child does not outlive parent
	if child.Outlives(parent) {
		t.Error("child should not outlive parent")
	}

	// Intra-region reference is always legal (even cyclic)
	if err := CheckReferenceLegality(parent, parent); err != nil {
		t.Errorf("intra-region reference should be legal: %v", err)
	}

	// Reference from shorter-lived (fnLocal) to longer-lived (parent) is legal
	if err := CheckReferenceLegality(fnLocal, parent); err != nil {
		t.Errorf("reference from short to long lived region should be legal: %v", err)
	}

	// Reference from longer-lived (parent) to shorter-lived (fnLocal) is ILLEGAL (dangling ptr hazard)
	if err := CheckReferenceLegality(parent, fnLocal); err == nil {
		t.Error("expected dangling pointer error when long-lived holds short-lived reference")
	} else if !strings.Contains(err.Error(), "dangling reference violation") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRegionOpFormatting(t *testing.T) {
	op := RegionOp{
		Kind:     OpRegionAlloc,
		RegionID: "reg:1",
		Target:   "userObj",
		Type:     "*User",
		Position: "main.go:10:5",
	}
	formatted := op.String()
	for _, want := range []string{"region.alloc", "reg:1", "userObj", "*User", "main.go:10:5"} {
		if !strings.Contains(formatted, want) {
			t.Errorf("op.String() missing %q: %s", want, formatted)
		}
	}
}
