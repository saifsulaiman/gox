package runtime

import (
	"sync"
	"testing"
)

type WorkerPayload struct {
	ID    int
	Title string
	Data  [64]byte
}

func TestAllocUniqueAndFree(t *testing.T) {
	ResetUniqueForTesting()

	p1 := AllocUnique(WorkerPayload{ID: 101, Title: "task-A"})
	if p1 == nil || p1.ID != 101 || p1.Title != "task-A" {
		t.Fatalf("AllocUnique corrupted: %+v", p1)
	}

	stats := GetUniqueStats()
	if stats.AllocCount != 1 || stats.ActiveObjects != 1 {
		t.Fatalf("unexpected stats after alloc: %+v", stats)
	}

	FreeUnique(p1)

	stats = GetUniqueStats()
	if stats.FreeCount != 1 || stats.ActiveObjects != 0 {
		t.Fatalf("unexpected stats after free: %+v", stats)
	}

	// Allocate again - should reuse memory slot
	p2 := AllocUnique(WorkerPayload{ID: 202, Title: "task-B"})
	if p2.ID != 202 || p2.Title != "task-B" {
		t.Fatalf("reused slot corrupted: %+v", p2)
	}

	FreeUnique(p2)
}

func TestAllocUniqueConcurrent(t *testing.T) {
	ResetUniqueForTesting()

	const workers = 16
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers)

	for w := 0; w < workers; w++ {
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				p := AllocUnique(WorkerPayload{
					ID:    wid*1000 + i,
					Title: "concurrent-worker",
				})
				if p.ID != wid*1000+i {
					t.Errorf("data mismatch in worker %d", wid)
				}
				FreeUnique(p)
			}
		}(w)
	}

	wg.Wait()

	stats := GetUniqueStats()
	expectedOps := int64(workers * iterations)
	if stats.AllocCount != expectedOps || stats.FreeCount != expectedOps || stats.ActiveObjects != 0 {
		t.Fatalf("stats mismatch after concurrent operations: %+v (expected %d ops)", stats, expectedOps)
	}
}
