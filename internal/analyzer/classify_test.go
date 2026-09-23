package analyzer

import "testing"

func TestClassifySafetyOrder(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		heap  bool
		facts flowFacts
		want  Classification
	}{
		{name: "frame local", kind: "alloc", want: StackSafe},
		{name: "heap local", kind: "alloc", heap: true, want: PotentialStack},
		{name: "returned", kind: "alloc", heap: true, facts: flowFacts{returned: true}, want: RegionCandidate},
		{name: "shared", kind: "alloc", heap: true, facts: flowFacts{heapStored: true}, want: ARCCandidate},
		{name: "global beats return", kind: "alloc", heap: true, facts: flowFacts{returned: true, globalStored: true}, want: RuntimeFallback},
		{name: "channel always falls back", kind: "channel", heap: true, facts: flowFacts{returned: true}, want: RuntimeFallback},
		{name: "ffi always falls back", kind: "alloc", heap: true, facts: flowFacts{ffiUse: true}, want: RuntimeFallback},
		{name: "exported return falls back", kind: "alloc", heap: true, facts: flowFacts{returned: true, externalReturn: true}, want: RuntimeFallback},
		{name: "open dynamic call falls back", kind: "alloc", heap: true, facts: flowFacts{dynamicCall: true, dynamicDispatch: true}, want: RuntimeFallback},
		{name: "unknown", kind: "alloc", heap: true, facts: flowFacts{unknownUse: true}, want: Unknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, _, _, _ := classify(test.kind, test.heap, test.facts)
			if got != test.want {
				t.Errorf("classify() = %s, want %s", got, test.want)
			}
		})
	}
}
