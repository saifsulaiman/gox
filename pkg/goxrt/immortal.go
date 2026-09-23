package goxrt

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// ImmortalStats reports runtime metrics for immortal and static allocations.
type ImmortalStats struct {
	BytesAllocated int64 `json:"bytes_allocated"`
	AllocCount     int64 `json:"alloc_count"`
	ReadOnlyCount  int64 `json:"read_only_count"`
}

type immortalPool struct {
	mu            sync.Mutex
	chunks        [][]byte
	currentChunk  []byte
	offset        int
	chunkSize     int
	bytesAlloc    atomic.Int64
	allocCount    atomic.Int64
	readOnlyCount atomic.Int64
}

const defaultImmortalChunkSize = 256 * 1024

var globalImmortalPool = newImmortalPool(defaultImmortalChunkSize)

func newImmortalPool(chunkSize int) *immortalPool {
	if chunkSize <= 0 {
		chunkSize = defaultImmortalChunkSize
	}
	first := make([]byte, chunkSize)
	return &immortalPool{
		chunks:       [][]byte{first},
		currentChunk: first,
		offset:       0,
		chunkSize:    chunkSize,
	}
}

func (p *immortalPool) allocRaw(size int, align int) unsafe.Pointer {
	if size <= 0 {
		size = 1
	}
	if align <= 0 {
		align = 8
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pad := (align - (p.offset % align)) % align
	if p.offset+pad+size > len(p.currentChunk) {
		allocSize := p.chunkSize
		if size > allocSize {
			allocSize = size + align
		}
		newChunk := make([]byte, allocSize)
		p.chunks = append(p.chunks, newChunk)
		p.currentChunk = newChunk
		p.offset = 0
		pad = 0
	}

	start := p.offset + pad
	p.offset = start + size
	p.bytesAlloc.Add(int64(size))
	p.allocCount.Add(1)

	return unsafe.Pointer(&p.currentChunk[start])
}

// AllocImmortal allocates memory from the global immortal bump allocator.
func AllocImmortal[T any](val T) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if size == 0 {
		size = 1
	}

	ptr := (*T)(globalImmortalPool.allocRaw(size, align))
	*ptr = val
	return ptr
}

// AllocReadOnly allocates immutable static memory.
func AllocReadOnly[T any](val T) *T {
	ptr := AllocImmortal(val)
	globalImmortalPool.readOnlyCount.Add(1)
	return ptr
}

// GetImmortalStats returns runtime metrics for immortal memory.
func GetImmortalStats() ImmortalStats {
	return ImmortalStats{
		BytesAllocated: globalImmortalPool.bytesAlloc.Load(),
		AllocCount:     globalImmortalPool.allocCount.Load(),
		ReadOnlyCount:  globalImmortalPool.readOnlyCount.Load(),
	}
}
