package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeClassifiesRepresentativeFlows(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/sample\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", `package sample

var global *Node
type Node struct { Next *Node }
type Factory struct{}

func local() int {
	p := new(int)
	*p = 42
	return *p
}

func returned() *int { return new(int) }

func stored() *Node {
	n := new(Node)
	n.Next = n
	return n
}

func escapedGlobal() { global = new(Node) }

func closure() func() int {
	p := new(int)
	return func() int { return *p }
}

func localClosure() int {
	p := new(int)
	f := func() int { return *p }
	return f()
}

func (Factory) make() *int { return new(int) }
`)

	result, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, Trace: true})
	if err != nil {
		t.Fatalf("Analyze() error: %v", err)
	}
	if result.Summary.PackagesAnalyzed != 1 {
		t.Fatalf("packages = %d, want 1", result.Summary.PackagesAnalyzed)
	}
	if result.Summary.AllocationsDiscovered < 4 {
		t.Fatalf("allocations = %d, want at least 4", result.Summary.AllocationsDiscovered)
	}
	if result.Metrics.ThroughputAllocationsPerSecond <= 0 || result.Metrics.AnalysisTimeMS <= 0 ||
		result.Metrics.CompilationTimeMS <= 0 || result.Metrics.MemoryUsageBytes == 0 ||
		result.Metrics.BinarySizeBytes <= 0 {
		t.Errorf("mandatory metrics were not populated: %#v", result.Metrics)
	}
	if result.CallGraph.Functions == 0 || result.CallGraph.CallEdges == 0 || len(result.Graph.Nodes) == 0 {
		t.Errorf("whole-program graph was not populated: calls=%#v graph=%#v", result.CallGraph, result.Graph)
	}
	ids := make(map[string]bool)
	for _, allocation := range result.Allocations {
		if allocation.ID == "" || ids[allocation.ID] {
			t.Errorf("allocation identity is empty or duplicated: %q", allocation.ID)
		}
		ids[allocation.ID] = true
		if len(allocation.Trace) == 0 {
			t.Errorf("trace is empty for %s", allocation.ID)
		}
	}
	assertHasClassification(t, result, PotentialStack)
	assertHasClassification(t, result, RegionCandidate)
	assertHasClassification(t, result, ARCCandidate)
	assertHasClassification(t, result, RuntimeFallback)
	if result.Summary.FunctionsAnalyzed < 9 {
		t.Errorf("functions = %d, want source methods and closures to be included", result.Summary.FunctionsAnalyzed)
	}
}

func TestAnalyzeRejectsBrokenPackage(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/broken\n\ngo 1.24\n")
	writeTestFile(t, dir, "broken.go", "package broken\nfunc f(\n")
	if _, err := Analyze(Config{Dir: dir}); err == nil {
		t.Fatal("Analyze() error = nil, want package error")
	}
}

func TestAnalyzeIncludesTestsWithoutDuplicatingProductionFunctions(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/withtests\n\ngo 1.24\n")
	writeTestFile(t, dir, "sample.go", "package sample\nfunc production() *int { return new(int) }\n")
	writeTestFile(t, dir, "sample_test.go", "package sample\nfunc helper() *string { return new(string) }\n")

	withoutTests, err := Analyze(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	withTests, err := Analyze(Config{Dir: dir, IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := withTests.Summary.AllocationsDiscovered, withoutTests.Summary.AllocationsDiscovered+1; got != want {
		t.Errorf("allocations with tests = %d, want %d (production sites must not be duplicated): %#v", got, want, withTests.Allocations)
	}
}

func TestClosedWorldResolvesInterfaceDispatch(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/closed\n\ngo 1.24\n")
	writeTestFile(t, dir, "main.go", `package main
type Consumer interface { Consume(*int) }
type consumer struct{}
func (consumer) Consume(*int) {}
func dynamic(c Consumer) { c.Consume(new(int)) }
func main() { dynamic(consumer{}) }
`)

	open, err := Analyze(Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := Analyze(Config{Dir: dir, ClosedWorld: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := functionClassification(open, ".dynamic"); got != RuntimeFallback {
		t.Errorf("open-world dynamic allocation = %s, want %s", got, RuntimeFallback)
	}
	if got := functionClassification(closed, ".dynamic"); got != PotentialStack {
		t.Errorf("closed-world dynamic allocation = %s, want %s", got, PotentialStack)
	}
	if open.CallGraph.DynamicCallEdges == 0 {
		t.Error("interface dispatch did not produce a dynamic call-graph edge")
	}
}

func BenchmarkAnalyze(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/bench\n\ngo 1.24\n")
	writeTestFile(b, dir, "bench.go", `package bench
type Item struct { Value int }
func Build(n int) []*Item {
	out := make([]*Item, 0, n)
	for i := 0; i < n; i++ { out = append(out, &Item{Value: i}) }
	return out
}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Analyze(Config{Dir: dir}); err != nil {
			b.Fatal(err)
		}
	}
}

func writeTestFile(tb testing.TB, dir, name, contents string) {
	tb.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func assertHasClassification(t *testing.T, result *Result, want Classification) {
	t.Helper()
	if result.Summary.ByClassification[want] == 0 {
		t.Errorf("no allocation classified as %s; allocations: %#v", want, result.Allocations)
	}
}

func functionClassification(result *Result, suffix string) Classification {
	for _, allocation := range result.Allocations {
		if strings.HasSuffix(allocation.Function, suffix) {
			return allocation.Classification
		}
	}
	return ""
}

func TestAnalyzeWithCrossCompileEnv(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example.com/cross\n\ngo 1.24\n")
	writeTestFile(t, dir, "main.go", `package main

import "fmt"

type Config struct {
	Host string
	Port int
}

func newConfig(h string) *Config {
	return &Config{Host: h, Port: 8080}
}

func main() {
	cfg := newConfig("localhost")
	fmt.Println(cfg.Host)
}
`)

	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOOS=") && !strings.HasPrefix(e, "GOARCH=") {
			env = append(env, e)
		}
	}
	env = append(env, "GOOS=linux", "GOARCH=amd64")

	res, err := Analyze(Config{
		Dir:            dir,
		Patterns:       []string{"."},
		MemoryStrategy: true,
		Env:            env,
	})
	if err != nil {
		t.Fatalf("Analyze with GOOS=linux error: %v", err)
	}
	if res.Summary.AllocationsDiscovered == 0 {
		t.Error("expected at least 1 allocation discovered")
	}
}
