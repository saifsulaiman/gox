package runtime

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"unsafe"
)

const defaultChunkSize = 64 * 1024 // 64 KB default chunk

// chunk represents a contiguous block of memory managed by an Arena.
type chunk struct {
	buf    []byte
	offset int
}

// Arena is a high-performance, chunk-based bump allocator for region-based
// memory management. It allows rapid $O(1)$ allocations with zero per-object
// overhead and instantaneous $O(1)$ bulk deallocation or reset.
//
// An Arena is goroutine-confined by design: it contains no synchronization
// primitives, providing maximum throughput for request, loop, or function scopes.
type Arena struct {
	chunks         []*chunk
	currentIdx     int
	chunkSize      int
	parent         *Arena
	totalAllocated int64
	bytesInUse     int64
	allocCount     int64
}

// ArenaStats reports memory usage statistics for an Arena.
type ArenaStats struct {
	TotalAllocated int64 `json:"total_allocated"` // Total bytes allocated from the OS
	BytesInUse     int64 `json:"bytes_in_use"`     // Bytes currently handed out to callers
	ChunkCount     int   `json:"chunk_count"`      // Number of chunks allocated
	AllocCount     int64 `json:"alloc_count"`      // Total allocation operations
}

// NewArena creates a new Arena with default chunk size (64 KB).
func NewArena() *Arena {
	return NewArenaWithChunkSize(defaultChunkSize)
}

// NewArenaWithChunkSize creates a new Arena with a specified chunk size.
func NewArenaWithChunkSize(chunkSize int) *Arena {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	firstChunk := &chunk{buf: make([]byte, chunkSize)}
	return &Arena{
		chunks:     []*chunk{firstChunk},
		currentIdx: 0,
		chunkSize:  chunkSize,
	}
}

// NewNestedArena creates a child arena nested within a parent arena.
// When the parent is freed, all nested children are also considered invalid.
func NewNestedArena(parent *Arena) *Arena {
	child := NewArena()
	child.parent = parent
	return child
}

var zeroByte byte

// Alloc allocates zeroed memory for a value of type T from the arena and returns
// a typed pointer to it.
func Alloc[T any](a *Arena) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if size == 0 {
		return (*T)(unsafe.Pointer(&zeroByte))
	}

	ptr := a.allocRaw(size, align)
	// Clear memory to match Go's zero-initialization guarantee using Go 1.21+ clear()
	clear(unsafe.Slice((*byte)(ptr), size))
	return (*T)(ptr)
}

// AllocVal allocates memory in the arena for a value of type T, initializes it directly with val,
// and returns a typed pointer to it without redundant zeroing.
func AllocVal[T any](a *Arena, val T) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if size == 0 {
		return (*T)(unsafe.Pointer(&zeroByte))
	}
	ptr := (*T)(a.allocRaw(size, align))
	*ptr = val
	return ptr
}

// AllocSlice allocates a slice of type []T with the specified length and capacity
// backed by memory inside the arena.
func AllocSlice[T any](a *Arena, length, capacity int) []T {
	if length < 0 || capacity < length {
		panic(fmt.Sprintf("runtime.AllocSlice: invalid length=%d, capacity=%d", length, capacity))
	}
	if capacity == 0 {
		return make([]T, 0)
	}

	var zero T
	elemSize := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	totalBytes := elemSize * capacity

	ptr := a.allocRaw(totalBytes, align)
	// Clear memory using Go 1.21+ clear()
	clear(unsafe.Slice((*byte)(ptr), totalBytes))

	slice := unsafe.Slice((*T)(ptr), capacity)
	return slice[:length]
}

// allocRaw allocates size bytes with the given alignment from the arena.
func (a *Arena) allocRaw(size, align int) unsafe.Pointer {
	if size < 0 {
		panic("negative allocation size")
	}
	if align <= 0 {
		align = int(unsafe.Sizeof(uintptr(0)))
	}

	curr := a.chunks[a.currentIdx]

	// Align offset
	offset := (curr.offset + (align - 1)) &^ (align - 1)

	// Check if allocation fits in current chunk
	if offset+size <= len(curr.buf) {
		curr.offset = offset + size
		a.bytesInUse += int64(size)
		a.allocCount++
		return unsafe.Pointer(&curr.buf[offset])
	}

	// Need a new chunk
	newChunkSize := a.chunkSize
	if size > newChunkSize {
		// Big allocation: allocate dedicated chunk of exact needed size
		newChunkSize = size + align
	}

	// Check if there is an existing already-allocated chunk we can reuse (after Reset)
	if a.currentIdx+1 < len(a.chunks) && len(a.chunks[a.currentIdx+1].buf) >= size {
		a.currentIdx++
		curr = a.chunks[a.currentIdx]
		curr.offset = 0
	} else {
		newChunk := &chunk{buf: make([]byte, newChunkSize)}
		a.chunks = append(a.chunks, newChunk)
		a.currentIdx = len(a.chunks) - 1
		curr = newChunk
		a.totalAllocated += int64(newChunkSize)
	}

	offset = (curr.offset + (align - 1)) &^ (align - 1)
	curr.offset = offset + size
	a.bytesInUse += int64(size)
	a.allocCount++
	return unsafe.Pointer(&curr.buf[offset])
}

// Reset resets the allocation pointers of all chunks in the arena to 0.
// This provides $O(1)$ bulk deallocation while retaining the allocated
// buffers for subsequent allocations, completely avoiding GC overhead
// in iterative workloads (e.g. loops, HTTP requests).
func (a *Arena) Reset() {
	for _, c := range a.chunks {
		c.offset = 0
	}
	a.currentIdx = 0
	a.bytesInUse = 0
	a.allocCount = 0
}

// Free releases all chunks held by the arena, allowing the underlying memory
// to be reclaimed.
func (a *Arena) Free() {
	a.chunks = nil
	a.currentIdx = 0
	a.bytesInUse = 0
	a.totalAllocated = 0
	a.allocCount = 0
}

// Free frees all memory held by the given arena.
func Free(a *Arena) {
	if a != nil {
		a.Free()
	}
}

// Stats returns the current usage statistics of the Arena.
func (a *Arena) Stats() ArenaStats {
	return ArenaStats{
		TotalAllocated: a.totalAllocated,
		BytesInUse:     a.bytesInUse,
		ChunkCount:     len(a.chunks),
		AllocCount:     a.allocCount,
	}
}

// Parent returns the parent arena if this is a nested arena, or nil.
func (a *Arena) Parent() *Arena {
	return a.parent
}

type requestArenaKeyType struct{}

var requestArenaKey = requestArenaKeyType{}

var arenaPool = sync.Pool{
	New: func() any {
		return NewArena()
	},
}

// GetArena borrows an Arena from the concurrent pool.
func GetArena() *Arena {
	return arenaPool.Get().(*Arena)
}

// PutArena resets an Arena and returns it to the pool for reuse with 0 heap mallocs.
func PutArena(a *Arena) {
	if a != nil {
		a.Reset()
		arenaPool.Put(a)
	}
}

// WithRequestArena returns an HTTP middleware that borrows a pooled Arena
// for the duration of an HTTP request, injects it into r.Context(),
// and automatically resets and returns it to the pool when the request completes.
func WithRequestArena(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arena := GetArena()
		defer PutArena(arena)
		ctx := context.WithValue(r.Context(), requestArenaKey, arena)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestArena retrieves the active request Arena from ctx, or nil if not present.
func GetRequestArena(ctx context.Context) *Arena {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(requestArenaKey).(*Arena); ok {
		return a
	}
	return nil
}

// RequestArenaContextKey returns the context key used for storing the request arena.
func RequestArenaContextKey() any {
	return requestArenaKey
}


