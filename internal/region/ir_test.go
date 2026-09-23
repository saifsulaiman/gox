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

func TestRegionDescriptorEdgeCoverage(t *testing.T) {
	var nilR *RegionDescriptor
	r1 := &RegionDescriptor{ID: "reg:1", Scope: ScopeCaller, Depth: 1}
	r2 := &RegionDescriptor{ID: "reg:2", Scope: ScopeFunction, Depth: 2}
	r3 := &RegionDescriptor{ID: "reg:3", Scope: ScopeLexical, Depth: 3}
	rReq := &RegionDescriptor{ID: "reg:req", Scope: ScopeRequest, Depth: 1}

	// Nil checks
	if nilR.Outlives(r1) {
		t.Error("nil descriptor cannot outlive r1")
	}
	if r1.Outlives(nilR) {
		t.Error("r1 cannot outlive nil descriptor")
	}
	if err := CheckReferenceLegality(nil, r1); err != nil {
		t.Errorf("nil source region should be legal: %v", err)
	}
	if err := CheckReferenceLegality(r1, nil); err != nil {
		t.Errorf("nil target region should be legal: %v", err)
	}

	// Same ID
	sameID := &RegionDescriptor{ID: "reg:1", Scope: ScopeFunction, Depth: 5}
	if !r1.Outlives(sameID) {
		t.Error("same ID should report outlives")
	}

	// ScopeCaller outlives ScopeFunction & ScopeLexical
	if !r1.Outlives(r2) {
		t.Error("ScopeCaller should outlive ScopeFunction")
	}
	if !r1.Outlives(r3) {
		t.Error("ScopeCaller should outlive ScopeLexical")
	}

	// ScopeRequest outlives ScopeFunction & ScopeLexical
	if !rReq.Outlives(r2) {
		t.Error("ScopeRequest should outlive ScopeFunction")
	}
	if !rReq.Outlives(r3) {
		t.Error("ScopeRequest should outlive ScopeLexical")
	}

	// Depth comparison
	dShallow := &RegionDescriptor{ID: "reg:shallow", Scope: ScopeNested, Depth: 2}
	dDeep := &RegionDescriptor{ID: "reg:deep", Scope: ScopeNested, Depth: 5}
	if !dShallow.Outlives(dDeep) {
		t.Error("shallow depth should outlive deep depth")
	}
	if dDeep.Outlives(dShallow) {
		t.Error("deep depth should not outlive shallow depth")
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
