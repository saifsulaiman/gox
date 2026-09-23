package goxrt

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// UniqueStats records memory metrics for uniquely owned allocations.
type UniqueStats struct {
	AllocCount     int64 `json:"alloc_count"`
	FreeCount      int64 `json:"free_count"`
	ActiveObjects  int64 `json:"active_objects"`
	BytesAllocated int64 `json:"bytes_allocated"`
}

type slabPool struct {
	mu         sync.Mutex
	freeList   map[uintptr][]unsafe.Pointer
	allocCount atomic.Int64
	freeCount  atomic.Int64
	bytesAlloc atomic.Int64
}

var globalSlabPool = newSlabPool()

func newSlabPool() *slabPool {
	return &slabPool{
		freeList: make(map[uintptr][]unsafe.Pointer),
	}
}

// AllocUnique allocates zeroed memory for val from the slab pool.
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
		raw := list[len(list)-1]
		globalSlabPool.freeList[size] = list[:len(list)-1]
		globalSlabPool.mu.Unlock()
		ptr = (*T)(raw)
	} else {
		globalSlabPool.mu.Unlock()
		ptr = new(T)
		globalSlabPool.bytesAlloc.Add(int64(size))
	}

	*ptr = val
	globalSlabPool.allocCount.Add(1)
	return ptr
}

// FreeUnique returns the slot back to the slab pool's free list.
func FreeUnique[T any](ptr *T) {
	if ptr == nil {
		return
	}
	var zero T
	size := unsafe.Sizeof(zero)
	if size == 0 {
		size = 1
	}
	*ptr = zero

	globalSlabPool.mu.Lock()
	globalSlabPool.freeList[size] = append(globalSlabPool.freeList[size], unsafe.Pointer(ptr))
	globalSlabPool.mu.Unlock()
	globalSlabPool.freeCount.Add(1)
}

// GetUniqueStats returns runtime metrics for uniquely owned memory.
func GetUniqueStats() UniqueStats {
	alloc := globalSlabPool.allocCount.Load()
	freed := globalSlabPool.freeCount.Load()
	return UniqueStats{
		AllocCount:     alloc,
		FreeCount:      freed,
		ActiveObjects:  alloc - freed,
		BytesAllocated: globalSlabPool.bytesAlloc.Load(),
	}
}
