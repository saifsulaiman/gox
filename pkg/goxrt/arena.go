package goxrt

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
// memory management. It allows rapid O(1) allocations with zero per-object
// overhead and instantaneous O(1) bulk deallocation or reset.
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
	TotalAllocated int64 `json:"total_allocated"`
	BytesInUse     int64 `json:"bytes_in_use"`
	ChunkCount     int   `json:"chunk_count"`
	AllocCount     int64 `json:"alloc_count"`
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
func NewNestedArena(parent *Arena) *Arena {
	child := NewArena()
	child.parent = parent
	return child
}

var zeroByte byte

// Alloc allocates zeroed memory for a value of type T from the arena.
func Alloc[T any](a *Arena) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if size == 0 {
		return (*T)(unsafe.Pointer(&zeroByte))
	}

	ptr := a.allocRaw(size, align)
	clear(unsafe.Slice((*byte)(ptr), size))
	return (*T)(ptr)
}

// AllocVal allocates memory in the arena initialized with val without redundant zeroing.
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

// AllocSlice allocates a slice of type []T backed by memory inside the arena.
func AllocSlice[T any](a *Arena, length, capacity int) []T {
	if length < 0 || capacity < length {
		panic(fmt.Sprintf("goxrt.AllocSlice: invalid length=%d, capacity=%d", length, capacity))
	}
	if capacity == 0 {
		return make([]T, 0)
	}

	var zero T
	elemSize := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	totalBytes := elemSize * capacity

	ptr := a.allocRaw(totalBytes, align)
	clear(unsafe.Slice((*byte)(ptr), totalBytes))

	slice := unsafe.Slice((*T)(ptr), capacity)
	return slice[:length]
}

func (a *Arena) allocRaw(size, align int) unsafe.Pointer {
	if size < 0 {
		panic("negative allocation size")
	}
	if align <= 0 {
		align = int(unsafe.Sizeof(uintptr(0)))
	}

	curr := a.chunks[a.currentIdx]
	offset := (curr.offset + (align - 1)) &^ (align - 1)

	if offset+size <= len(curr.buf) {
		curr.offset = offset + size
		a.bytesInUse += int64(size)
		a.allocCount++
		return unsafe.Pointer(&curr.buf[offset])
	}

	nextChunkSize := a.chunkSize
	if size > nextChunkSize {
		nextChunkSize = size
	}
	newChunk := &chunk{buf: make([]byte, nextChunkSize)}
	a.chunks = append(a.chunks, newChunk)
	a.currentIdx = len(a.chunks) - 1
	a.totalAllocated += int64(nextChunkSize)

	newChunk.offset = size
	a.bytesInUse += int64(size)
	a.allocCount++
	return unsafe.Pointer(&newChunk.buf[0])
}

// Reset rewinds all chunk offsets to 0 for instant reuse without deallocation.
func (a *Arena) Reset() {
	for _, c := range a.chunks {
		c.offset = 0
	}
	a.currentIdx = 0
	a.bytesInUse = 0
	a.allocCount = 0
}

// Free releases all chunks held by the arena.
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

// GetArena borrows an Arena from the pool.
func GetArena() *Arena {
	return arenaPool.Get().(*Arena)
}

// PutArena resets the Arena and returns it to the pool for reuse with 0 heap mallocs.
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

// ArenaHandler is an HTTP handler that receives an active Arena directly,
// eliminating context allocations (context.WithValue) and request cloning (r.WithContext).
type ArenaHandler interface {
	ServeHTTPWithArena(w http.ResponseWriter, r *http.Request, a *Arena)
}

// ArenaHandlerFunc is an adapter to allow the use of ordinary functions as Arena handlers.
type ArenaHandlerFunc func(w http.ResponseWriter, r *http.Request, a *Arena)

// ServeHTTPWithArena calls f(w, r, a).
func (f ArenaHandlerFunc) ServeHTTPWithArena(w http.ResponseWriter, r *http.Request, a *Arena) {
	f(w, r, a)
}

// AdaptArenaHandler wraps an ArenaHandler as a standard http.Handler,
// borrowing a pooled Arena on every request with 0 context allocations.
func AdaptArenaHandler(h ArenaHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arena := GetArena()
		defer PutArena(arena)
		h.ServeHTTPWithArena(w, r, arena)
	})
}


