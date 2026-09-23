package runtime

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// UniqueStats records memory metrics for uniquely owned allocations.
type UniqueStats struct {
	AllocCount     int64 `json:"alloc_count"`     // Total number of unique allocations
	FreeCount      int64 `json:"free_count"`      // Total number of freed unique objects
	ActiveObjects  int64 `json:"active_objects"`  // Objects currently in use
	BytesAllocated int64 `json:"bytes_allocated"` // Total bytes allocated from the runtime
}

// slabPool manages memory chunks and free-list recycling for unique allocations.
type slabPool struct {
	mu            sync.Mutex
	freeList      map[uintptr][]unsafe.Pointer // size -> slice of available pointers
	allocCount    atomic.Int64
	freeCount     atomic.Int64
	bytesAlloc    atomic.Int64
}

var globalSlabPool = newSlabPool()

func newSlabPool() *slabPool {
	return &slabPool{
		freeList: make(map[uintptr][]unsafe.Pointer),
	}
}

// AllocUnique allocates zeroed memory for a value of type T from the slab pool,
// copies val into it, and returns a pointer.
//
// Because the object is uniquely owned, it can be deterministically freed at its
// point of last use via FreeUnique, avoiding GC tracing.
func AllocUnique[T any](val T) *T {
	var zero T
	size := unsafe.Sizeof(zero)
	if size == 0 {
		size = 1
	}

	globalSlabPool.mu.Lock()
	list := globalSlabPool.freeList[size]
	var ptr *T
	if len(list) > 0 {
		// Reuse from free list
		raw := list[len(list)-1]
		globalSlabPool.freeList[size] = list[:len(list)-1]
		globalSlabPool.mu.Unlock()
		ptr = (*T)(raw)
	} else {
		// Allocate new slot
		globalSlabPool.mu.Unlock()
		ptr = new(T)
		globalSlabPool.bytesAlloc.Add(int64(size))
	}

	globalSlabPool.allocCount.Add(1)
	*ptr = val
	return ptr
}

// FreeUnique deterministically reclaims a uniquely owned object at its point of last use.
// It zeroes the memory to prevent stale references and returns the slot to the free list.
func FreeUnique[T any](ptr *T) {
	if ptr == nil {
		return
	}

	var zero T
	size := unsafe.Sizeof(zero)
	if size == 0 {
		size = 1
	}

	// Zero the payload memory
	*ptr = zero

	globalSlabPool.mu.Lock()
	globalSlabPool.freeList[size] = append(globalSlabPool.freeList[size], unsafe.Pointer(ptr))
	globalSlabPool.mu.Unlock()

	globalSlabPool.freeCount.Add(1)
}

// GetUniqueStats returns the current statistics of unique allocations.
func GetUniqueStats() UniqueStats {
	allocs := globalSlabPool.allocCount.Load()
	frees := globalSlabPool.freeCount.Load()
	return UniqueStats{
		AllocCount:     allocs,
		FreeCount:      frees,
		ActiveObjects:  allocs - frees,
		BytesAllocated: globalSlabPool.bytesAlloc.Load(),
	}
}

// ResetUniqueForTesting resets the unique pool for hermetic unit testing.
func ResetUniqueForTesting() {
	globalSlabPool = newSlabPool()
}
