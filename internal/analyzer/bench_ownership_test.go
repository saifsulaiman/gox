package analyzer

import (
	"sync"
	"testing"
)

// MessagePacket represents a typical service payload with routing metadata and payload.
type MessagePacket struct {
	ID        uint64
	Route     string
	Payload   [1024]byte
	Timestamp int64
}

var (
	packetPool = sync.Pool{
		New: func() any {
			return new(MessagePacket)
		},
	}
	benchSink any
)

// BenchmarkUniqueOwnerStandardHeap simulates message processing where each request
// allocates a message packet on the tracing GC heap.
func BenchmarkUniqueOwnerStandardHeap(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		pkt := &MessagePacket{
			ID:        uint64(i),
			Route:     "/events/telemetry",
			Timestamp: 1700000000 + int64(i),
		}
		pkt.Payload[0] = 0xAA
		pkt.Payload[1023] = 0xBB

		// Process packet and prevent escape elision
		benchSink = pkt
		i++
	}
}

// BenchmarkUniqueOwnerDeterministicFree simulates GOX v0.5 deterministic unique-owner
// reclamation where the compiler inserts immediate O(1) deallocation at point of last use.
func BenchmarkUniqueOwnerDeterministicFree(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		pkt := packetPool.Get().(*MessagePacket)
		pkt.ID = uint64(i)
		pkt.Route = "/events/telemetry"
		pkt.Timestamp = 1700000000 + int64(i)
		pkt.Payload[0] = 0xAA
		pkt.Payload[1023] = 0xBB

		// Process packet
		res := int(pkt.Payload[0]) + int(pkt.Payload[1023])
		_ = res

		// Point of Last Use: Deterministic destruction point inserted by GOX compiler
		packetPool.Put(pkt)
		i++
	}
}

// BenchmarkOwnershipAnalysisOverhead measures analyzer latency when computing CFG liveness,
// move/borrow tracking, and destruction point insertion across a multi-function package.
func BenchmarkOwnershipAnalysisOverhead(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchownership\n\ngo 1.24\n")
	writeTestFile(b, dir, "ownership.go", `package benchownership

type Task struct {
	ID   int
	Name string
}

func helper(t *Task) int {
	return t.ID
}

func ProcessWorkflow(n int) int {
	t := &Task{ID: n, Name: "worker"}
	if n < 0 {
		return -1
	}
	val := helper(t)
	t.ID = val * 2
	return t.ID
}

func MoveWorkflow(n int) int {
	t := &Task{ID: n, Name: "mover"}
	return helper(t)
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
