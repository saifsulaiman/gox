package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"time"

	"example.com/gox-fast-api/handlers"
	"example.com/gox-fast-api/models"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

type BenchMetrics struct {
	Name        string
	TotalTime   time.Duration
	LatencyOp   time.Duration
	Mallocs     uint64
	AllocBytes  uint64
	GCCycles    uint32
	GCPauseNs   uint64
}

func main() {
	requests := flag.Int("requests", 50000, "Number of requests to simulate per mode")
	flag.Parse()

	fmt.Println("================================================================================")
	fmt.Println("             GOX Fast-API Production Benchmark Suite                            ")
	fmt.Println("   Standard Go Tracing GC vs. GOX Request Arena & Deterministic Slices          ")
	fmt.Println("================================================================================")
	fmt.Printf("Workload: %d REST API requests (GET /api/items?limit=20)\n\n", *requests)

	// Setup store and handlers
	store := models.NewStore()
	store.Seed(1000)
	handler := handlers.NewItemHandler(store)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/items", handler.HandleList)

	// Mode 1: Standard Go GC (without Request Arena)
	fmt.Print("Running Standard Go GC (Heap Mallocs)... ")
	stdMetrics := runBenchmark(mux, false, *requests)
	fmt.Println("done.")

	// Mode 2: GOX Deterministic (with Request Arena & AllocSlice)
	fmt.Print("Running GOX v1.0 (Request Arena & AllocSlice)... ")
	goxMetrics := runBenchmark(mux, true, *requests)
	fmt.Println("done.")
	fmt.Println()

	// Render Markdown table
	printComparativeTable(stdMetrics, goxMetrics)
}

func runBenchmark(mux http.Handler, useArena bool, requests int) BenchMetrics {
	// Force GC and settle before measurement
	runtime.GC()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()

	rec := httptest.NewRecorder()
	baseReq := httptest.NewRequest(http.MethodGet, "/api/items?limit=20", nil)

	for i := 0; i < requests; i++ {
		rec.Body.Reset()

		if useArena {
			// GOX Request Arena lifecycle: borrowed from concurrent pool, O(1) bulk reset
			arena := goxrt.GetArena()
			ctx := context.WithValue(baseReq.Context(), goxrt.RequestArenaContextKey(), arena)
			mux.ServeHTTP(rec, baseReq.WithContext(ctx))
			goxrt.PutArena(arena)
		} else {
			// Standard Go handler invocation
			mux.ServeHTTP(rec, baseReq)
		}
	}

	elapsed := time.Since(start)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	var gcCycles uint32
	if after.NumGC >= before.NumGC {
		gcCycles = after.NumGC - before.NumGC
	}

	var gcPauseNs uint64
	if after.PauseTotalNs >= before.PauseTotalNs {
		gcPauseNs = after.PauseTotalNs - before.PauseTotalNs
	}

	var mallocs uint64
	if after.Mallocs >= before.Mallocs {
		mallocs = after.Mallocs - before.Mallocs
	}

	var allocBytes uint64
	if after.TotalAlloc >= before.TotalAlloc {
		allocBytes = after.TotalAlloc - before.TotalAlloc
	}

	return BenchMetrics{
		TotalTime:  elapsed,
		LatencyOp:  elapsed / time.Duration(requests),
		Mallocs:    mallocs,
		AllocBytes: allocBytes,
		GCCycles:   gcCycles,
		GCPauseNs:  gcPauseNs,
	}
}

func printComparativeTable(std, gox BenchMetrics) {
	fmt.Println("### Benchmark Results (Comparative REST Throughput & Memory)")
	fmt.Println()
	fmt.Printf("| Metric | Standard Go GC | GOX v1.0 | Improvement / Reduction | Winner |\n")
	fmt.Printf("| :--- | :--- | :--- | :--- | :--- |\n")

	// Total Time
	timeDiff := float64(std.TotalTime-gox.TotalTime) / float64(std.TotalTime) * 100.0
	timeWinner := "Standard Go"
	if gox.TotalTime <= std.TotalTime {
		timeWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **Total Time** | %v | %v | %+.1f%% speedup | %s |\n",
		std.TotalTime.Round(time.Millisecond), gox.TotalTime.Round(time.Millisecond), timeDiff, timeWinner)

	// Avg Latency (op)
	latDiff := float64(std.LatencyOp-gox.LatencyOp) / float64(std.LatencyOp) * 100.0
	latWinner := "Standard Go"
	if gox.LatencyOp <= std.LatencyOp {
		latWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **Avg Latency (op)** | %v/req | %v/req | %+.1f%% speedup | %s |\n",
		std.LatencyOp, gox.LatencyOp, latDiff, latWinner)

	// Total Mallocs
	mallocDiff := float64(int64(std.Mallocs)-int64(gox.Mallocs)) / float64(std.Mallocs) * 100.0
	mallocWinner := "Standard Go"
	if gox.Mallocs <= std.Mallocs {
		mallocWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **Total Mallocs** | %d | %d | %+.1f%% reduction | %s |\n",
		std.Mallocs, gox.Mallocs, mallocDiff, mallocWinner)

	// Total Alloc Bytes
	stdMB := float64(std.AllocBytes) / (1024 * 1024)
	goxMB := float64(gox.AllocBytes) / (1024 * 1024)
	bytesDiff := float64(int64(std.AllocBytes)-int64(gox.AllocBytes)) / float64(std.AllocBytes) * 100.0
	bytesWinner := "Standard Go"
	if gox.AllocBytes <= std.AllocBytes {
		bytesWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **Total Alloc Bytes** | %.1f MB | %.1f MB | %+.1f%% reduction | %s |\n",
		stdMB, goxMB, bytesDiff, bytesWinner)

	// GC Cycles
	var gcDiff float64
	if std.GCCycles > 0 {
		gcDiff = float64(int(std.GCCycles)-int(gox.GCCycles)) / float64(std.GCCycles) * 100.0
	}
	gcWinner := "Standard Go"
	if gox.GCCycles <= std.GCCycles {
		gcWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **GC Cycles (NumGC)** | %d | %d | %+.1f%% reduction | %s |\n",
		std.GCCycles, gox.GCCycles, gcDiff, gcWinner)

	// GC Pause Time
	var pauseDiff float64
	if std.GCPauseNs > 0 {
		pauseDiff = float64(int64(std.GCPauseNs)-int64(gox.GCPauseNs)) / float64(std.GCPauseNs) * 100.0
	}
	pauseWinner := "Standard Go"
	if gox.GCPauseNs <= std.GCPauseNs {
		pauseWinner = "**GOX v1.0**"
	}
	fmt.Printf("| **GC Pause Time** | %v | %v | %+.1f%% reduction | %s |\n",
		time.Duration(std.GCPauseNs), time.Duration(gox.GCPauseNs), pauseDiff, pauseWinner)
	fmt.Println()
}
