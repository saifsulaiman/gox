package benchmarks

import (
	"sync"
	"sync/atomic"
	"testing"
)

type WorkItem struct {
	ID    int
	Key   string
	Value int64
}

type WorkResult struct {
	ID       int
	Computed int64
}

func runWorkerPipeline(workers int, itemCount int) int64 {
	jobs := make(chan *WorkItem, 256)
	results := make(chan *WorkResult, 256)

	var totalComputed int64
	var wg sync.WaitGroup

	// Start workers
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				// Per-item computation
				res := &WorkResult{
					ID:       item.ID,
					Computed: item.Value * 3,
				}
				results <- res
			}
		}()
	}

	// Result collector
	var collectorWg sync.WaitGroup
	collectorWg.Add(1)
	go func() {
		defer collectorWg.Done()
		for res := range results {
			atomic.AddInt64(&totalComputed, res.Computed)
		}
	}()

	// Producer
	for i := 0; i < itemCount; i++ {
		item := &WorkItem{
			ID:    i,
			Key:   "work-key",
			Value: int64(i + 1),
		}
		jobs <- item
	}
	close(jobs)

	wg.Wait()
	close(results)
	collectorWg.Wait()

	return totalComputed
}

func BenchmarkConcurrentPipeline(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		total := runWorkerPipeline(4, 1000)
		if total == 0 {
			b.Fatal("unexpected zero total")
		}
	}
}
