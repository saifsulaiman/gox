package analyzer

import (
	"testing"

	"github.com/goxlang/gox/internal/runtime"
)

// SharedConfigNode represents a typical shared immutable config or routing node
// distributed across request pipelines.
type SharedConfigNode struct {
	ID        uint64
	Route     string
	Data      [512]byte
	Version   int
}

var arcSink any

// BenchmarkSharedObjectStandardHeap simulates a shared object passed across
// multiple handlers and discarded, relying on Go's tracing GC for collection.
func BenchmarkSharedObjectStandardHeap(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		node := &SharedConfigNode{
			ID:      uint64(i),
			Route:   "/api/v1/config",
			Version: i,
		}
		node.Data[0] = 0x11
		node.Data[511] = 0x22

		// Multiple shared consumers (simulating multiple owners)
		arcSink = node
		_ = node.ID + uint64(node.Data[0])
		i++
	}
}

// BenchmarkSharedObjectARCNonAtomic simulates GOX v0.6 non-atomic ARC Box
// for goroutine-confined shared objects.
func BenchmarkSharedObjectARCNonAtomic(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		box := runtime.NewBox(SharedConfigNode{
			ID:      uint64(i),
			Route:   "/api/v1/config",
			Version: i,
		}, false)

		// Consumer 1: retain
		runtime.Retain(box)
		val1 := runtime.Value(box)
		_ = val1.ID

		// Consumer 2: retain
		runtime.Retain(box)
		val2 := runtime.Value(box)
		_ = val2.Version

		// Consumer 1 done: release
		runtime.Release(box, nil)

		// Consumer 2 done: release
		runtime.Release(box, nil)

		// Original creator done: release -> count reaches 0, destroyed
		runtime.Release(box, nil)
		i++
	}
}

// BenchmarkSharedObjectARCAtomic simulates GOX v0.6 atomic ARC Box
// for multi-threaded / concurrent shared objects.
func BenchmarkSharedObjectARCAtomic(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		box := runtime.NewBox(SharedConfigNode{
			ID:      uint64(i),
			Route:   "/api/v1/config",
			Version: i,
		}, true)

		// Concurrent reference 1: retain
		runtime.Retain(box)
		val1 := runtime.Value(box)
		_ = val1.ID

		// Concurrent reference 2: retain
		runtime.Retain(box)
		val2 := runtime.Value(box)
		_ = val2.Version

		// Release 1
		runtime.Release(box, nil)

		// Release 2
		runtime.Release(box, nil)

		// Final release -> count reaches 0, destroyed
		runtime.Release(box, nil)
		i++
	}
}

// BenchmarkSharedObjectARCElided simulates GOX v0.6 borrow elision pass
// where temporary borrows do not emit retain/release pairs.
func BenchmarkSharedObjectARCElided(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		box := runtime.NewBox(SharedConfigNode{
			ID:      uint64(i),
			Route:   "/api/v1/config",
			Version: i,
		}, false)

		// Borrow 1: elided (no retain/release)
		val1 := runtime.Value(box)
		_ = val1.ID

		// Borrow 2: elided (no retain/release)
		val2 := runtime.Value(box)
		_ = val2.Version

		// Sole release upon scope exit -> count reaches 0, destroyed
		runtime.Release(box, nil)
		i++
	}
}

// BenchmarkARCAnalysisLatency measures analyzer latency for whole-package ARC proof,
// type-level cycle detection, and borrow elision.
func BenchmarkARCAnalysisLatency(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/bencharc\n\ngo 1.24\n")
	writeTestFile(b, dir, "arc.go", `package bencharc

type ConfigItem struct {
	Key   string
	Value string
	TTL   int
}

type Container struct {
	Items []*ConfigItem
}

func ProcessAcyclic(k, v string) int {
	item := &ConfigItem{Key: k, Value: v, TTL: 3600}
	c := &Container{Items: make([]*ConfigItem, 0, 2)}
	c.Items = append(c.Items, item)
	return len(c.Items)
}
`)

	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
		if err != nil {
			b.Fatalf("Analyze() error: %v", err)
		}
	}
}
