package analyzer

import "testing"

func BenchmarkAnalyzeWithStrategy(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchstrat\n\ngo 1.24\n")
	writeTestFile(b, dir, "bench.go", `package benchstrat

type Item struct { Value int }

func Build(n int) []*Item {
	out := make([]*Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &Item{Value: i})
	}
	return out
}

func Local() int {
	p := new(int)
	*p = 42
	return *p
}

var global *Item

func Escaped() {
	global = &Item{Value: 99}
}
`)
	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeWithoutStrategy(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchnostrat\n\ngo 1.24\n")
	writeTestFile(b, dir, "bench.go", `package benchnostrat

type Item struct { Value int }

func Build(n int) []*Item {
	out := make([]*Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, &Item{Value: i})
	}
	return out
}

func Local() int {
	p := new(int)
	*p = 42
	return *p
}

var global *Item

func Escaped() {
	global = &Item{Value: 99}
}
`)
	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAdversarialPatterns(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchadv\n\ngo 1.24\n")
	writeTestFile(b, dir, "adv.go", `package benchadv

import "reflect"

var sink interface{}
type Node struct { Children []*Node }

func InterfaceEscape() { sink = new(int) }
func ReflectionUse() { reflect.ValueOf(new(int)) }
func ClosureReturn() func() int {
	p := new(int)
	return func() int { return *p }
}
func SelfRef() *Node {
	n := &Node{}
	n.Children = append(n.Children, n)
	return n
}
func Local() int { p := new(int); *p = 1; return *p }
`)
	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
		if err != nil {
			b.Fatal(err)
		}
	}
}
