package analyzer

import (
	"testing"
)

func TestParseEscapeOutputDetectsEscapes(t *testing.T) {
	output := `./main.go:7:6: p escapes to heap:
./main.go:7:6:   flow: ~r0 = p:
./main.go:7:6:     from return p (return) at ./main.go:8:2
./main.go:10:13: new(int) does not escape
./main.go:15:2: moved to heap: x
./main.go:20:8: leaking param: s to result ~r0 level=0
`
	idx := &escapeIndex{entries: make(map[string]*escapeInfo)}
	parseEscapeOutput(output, ".", idx)

	tests := []struct {
		key     string
		escapes bool
	}{
		{"./main.go:7", true},
		{"./main.go:10", false},
		{"./main.go:15", true},
		{"./main.go:20", true},
	}
	for _, test := range tests {
		info := idx.entries[test.key]
		if info == nil {
			t.Errorf("no entry for %s", test.key)
			continue
		}
		if info.escapes != test.escapes {
			t.Errorf("%s: escapes=%t, want %t (reasons: %v)", test.key, info.escapes, test.escapes, info.reasons)
		}
	}
}

func TestParseEscapeOutputHandlesEmptyInput(t *testing.T) {
	idx := &escapeIndex{entries: make(map[string]*escapeInfo)}
	parseEscapeOutput("", ".", idx)
	if len(idx.entries) != 0 {
		t.Errorf("expected empty index for empty input, got %d entries", len(idx.entries))
	}
}

func TestParseEscapeOutputHandlesGarbageInput(t *testing.T) {
	idx := &escapeIndex{entries: make(map[string]*escapeInfo)}
	parseEscapeOutput("not a valid compiler output\nrandom garbage\n", ".", idx)
	if len(idx.entries) != 0 {
		t.Errorf("expected empty index for garbage input, got %d entries", len(idx.entries))
	}
}

func TestEscapeIndexLookupStripsColumn(t *testing.T) {
	idx := &escapeIndex{entries: map[string]*escapeInfo{
		"main.go:42": {position: "main.go:42", escapes: true},
	}}
	result := idx.lookup("main.go:42:13")
	if result == nil {
		t.Fatal("lookup returned nil for file:line:col when file:line entry exists")
	}
	if !result.escapes {
		t.Error("expected escapes=true")
	}
}

func TestEscapeIndexLookupNilSafe(t *testing.T) {
	var idx *escapeIndex
	if result := idx.lookup("main.go:1"); result != nil {
		t.Error("lookup on nil index should return nil")
	}
}

func TestEscapeInfoToEscapeResult(t *testing.T) {
	info := &escapeInfo{
		position: "main.go:7",
		escapes:  true,
		reasons:  []string{"p escapes to heap"},
		raw:      "./main.go:7:6: p escapes to heap:",
	}
	result := info.toEscapeResult()
	if result == nil {
		t.Fatal("toEscapeResult returned nil")
	}
	if !result.Escapes {
		t.Error("expected Escapes=true")
	}
	if len(result.EscapeReasons) != 1 || result.EscapeReasons[0] != "p escapes to heap" {
		t.Errorf("unexpected reasons: %v", result.EscapeReasons)
	}
	if result.GoCompilerSays == "" {
		t.Error("expected non-empty GoCompilerSays")
	}
}

func TestNilEscapeInfoToEscapeResult(t *testing.T) {
	var info *escapeInfo
	if result := info.toEscapeResult(); result != nil {
		t.Error("nil escapeInfo should produce nil EscapeResult")
	}
}
