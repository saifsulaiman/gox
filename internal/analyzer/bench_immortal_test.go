package analyzer

import (
	"testing"

	"github.com/goxlang/gox/internal/runtime"
)

type BenchConfig struct {
	MaxWorkers int
	Host       string
	Port       int
	IsActive   bool
	Tags       [8]string
}

var benchGlobalSink any

// BenchmarkStandardHeapGlobalLookup measures reading from standard Go heap global object.
func BenchmarkStandardHeapGlobalLookup(b *testing.B) {
	cfg := &BenchConfig{
		MaxWorkers: 64,
		Host:       "127.0.0.1",
		Port:       9000,
		IsActive:   true,
		Tags:       [8]string{"api", "v1", "prod", "us-east", "primary", "go", "gox", "immortal"},
	}

	b.ReportAllocs()
	for b.Loop() {
		p := cfg.Port
		w := cfg.MaxWorkers
		t0 := cfg.Tags[0]
		if p != 9000 || w != 64 || t0 != "api" {
			b.Fatal("data corruption")
		}
	}
}

// BenchmarkImmortalReadThroughput measures reading from immortal memory.
// Zero GC tracking, zero reference counting, direct cache-friendly memory load.
func BenchmarkImmortalReadThroughput(b *testing.B) {
	runtime.ResetImmortalForTesting()
	immortalCfg := runtime.AllocReadOnly(BenchConfig{
		MaxWorkers: 64,
		Host:       "127.0.0.1",
		Port:       9000,
		IsActive:   true,
		Tags:       [8]string{"api", "v1", "prod", "us-east", "primary", "go", "gox", "immortal"},
	})

	b.ReportAllocs()
	for b.Loop() {
		p := immortalCfg.Port
		w := immortalCfg.MaxWorkers
		t0 := immortalCfg.Tags[0]
		if p != 9000 || w != 64 || t0 != "api" {
			b.Fatal("data corruption")
		}
	}
}

// BenchmarkImmortalAllocThroughput measures allocation throughput in the immortal pool.
func BenchmarkImmortalAllocThroughput(b *testing.B) {
	runtime.ResetImmortalForTesting()

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		ptr := runtime.AllocImmortal(BenchConfig{
			MaxWorkers: i,
			Host:       "localhost",
			Port:       8000,
			IsActive:   true,
		})
		benchGlobalSink = ptr
		i++
	}
}

// BenchmarkImmortalProofEngineLatency measures the static analysis time to detect
// global singletons, init() blocks, post-init mutability, and immortal proofs.
func BenchmarkImmortalProofEngineLatency(b *testing.B) {
	cfg := Config{
		Dir:            ".",
		Patterns:       []string{"."},
		IncludeTests:   true,
		MemoryStrategy: true,
	}

	for b.Loop() {
		res, err := Analyze(cfg)
		if err != nil {
			b.Fatalf("Analyze failed: %v", err)
		}
		if res.StrategySummary == nil || res.StrategySummary.ProvenImmortalCount < 1 {
			b.Fatal("expected proven immortal allocations")
		}
	}
}
