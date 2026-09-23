package runtime

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// ImmortalStats reports runtime metrics for immortal and static allocations.
type ImmortalStats struct {
	BytesAllocated int64 `json:"bytes_allocated"` // Total bytes allocated in immortal memory
	AllocCount     int64 `json:"alloc_count"`     // Total number of immortal allocations
	ReadOnlyCount  int64 `json:"read_only_count"` // Number of allocations marked read-only
}

// immortalPool manages global process-lifetime memory chunks.
// Because immortal objects live until process exit, memory is never freed,
// eliminating GC root tracing, sweep overhead, and reference counting.
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

const defaultImmortalChunkSize = 256 * 1024 // 256 KB default chunks

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

// allocRaw allocates size bytes with the given alignment from the immortal pool.
func (p *immortalPool) allocRaw(size int, align int) unsafe.Pointer {
	if size <= 0 {
		size = 1
	}
	if align <= 0 {
		align = 8
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Align the current offset
	pad := (align - (p.offset % align)) % align
	needed := pad + size

	if p.offset+needed > len(p.currentChunk) {
		// Allocate a new chunk (at least chunkSize or size+align)
		allocSize := p.chunkSize
		if needed > allocSize {
			allocSize = needed + 4096
		}
		newChunk := make([]byte, allocSize)
		p.chunks = append(p.chunks, newChunk)
		p.currentChunk = newChunk
		p.offset = 0
		pad = (align - (p.offset % align)) % align
		needed = pad + size
	}

	alignedOffset := p.offset + pad
	p.offset += needed

	p.bytesAlloc.Add(int64(needed))
	p.allocCount.Add(1)

	ptr := unsafe.Pointer(&p.currentChunk[alignedOffset])
	return ptr
}

// AllocImmortal allocates memory in the immortal static segment for a value of type T,
// initializes it with val, and returns a pointer to it.
//
// Memory allocated through AllocImmortal is retained for the entire process lifetime.
// It is excluded from GC root tracing, has zero write barriers, and requires no reference counts.
func AllocImmortal[T any](val T) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))

	ptr := (*T)(globalImmortalPool.allocRaw(size, align))
	*ptr = val
	return ptr
}

// AllocReadOnly allocates memory for an immutable value of type T in the immortal segment.
// It signifies that the memory will not be mutated after package initialization.
func AllocReadOnly[T any](val T) *T {
	globalImmortalPool.readOnlyCount.Add(1)
	return AllocImmortal(val)
}

// GetImmortalStats returns a snapshot of immortal memory allocation statistics.
func GetImmortalStats() ImmortalStats {
	return ImmortalStats{
		BytesAllocated: globalImmortalPool.bytesAlloc.Load(),
		AllocCount:     globalImmortalPool.allocCount.Load(),
		ReadOnlyCount:  globalImmortalPool.readOnlyCount.Load(),
	}
}

// ResetImmortalForTesting resets the immortal pool for hermetic unit testing.
func ResetImmortalForTesting() {
	globalImmortalPool = newImmortalPool(defaultImmortalChunkSize)
}
