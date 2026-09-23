package transform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EmitRuntime writes the GOX runtime package into stagingDir/goxrt.
// This allows any transformed Go package to compile and run standalone
// with zero external dependencies.
func EmitRuntime(stagingDir string) error {
	goxrtDir := filepath.Join(stagingDir, "goxrt")
	if err := os.MkdirAll(goxrtDir, 0755); err != nil {
		return fmt.Errorf("create goxrt dir: %w", err)
	}

	files := map[string]string{
		"arc.go":      runtimeArcSrc,
		"arena.go":    runtimeArenaSrc,
		"immortal.go": runtimeImmortalSrc,
		"unique.go":   runtimeUniqueSrc,
	}

	for name, content := range files {
		path := filepath.Join(goxrtDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return nil
}

// FindModuleName reads the module name from a go.mod file in dir or any ancestor directory, or returns "goxapp".
func FindModuleName(dir string) string {
	curr := dir
	for {
		goModPath := filepath.Join(curr, "go.mod")
		content, err := os.ReadFile(goModPath)
		if err == nil {
			for _, line := range strings.Split(string(content), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "module ") {
					return strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
				}
			}
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	return "goxapp"
}

const runtimeArcSrc = `package goxrt

import (
	"sync"
	"sync/atomic"
)

type Box[T any] struct {
	val         T
	atomic      bool
	atomicStrong atomic.Int64
	atomicWeak   atomic.Int64
	plainStrong  int64
	plainWeak    int64
	mu          sync.Mutex
	destroyed   bool
}

type WeakBox[T any] struct {
	box *Box[T]
}

func NewBox[T any](val T, isAtomic bool) *Box[T] {
	b := &Box[T]{val: val, atomic: isAtomic}
	if isAtomic {
		b.atomicStrong.Store(1)
	} else {
		b.plainStrong = 1
	}
	return b
}

func AllocARC[T any](val T, isAtomic bool) *T {
	return AllocUnique(val)
}

func Retain[T any](b *Box[T]) *Box[T] {
	if b == nil {
		return nil
	}
	if b.atomic {
		for {
			cur := b.atomicStrong.Load()
			if cur <= 0 {
				panic("goxrt: Retain called on destroyed atomic Box")
			}
			if b.atomicStrong.CompareAndSwap(cur, cur+1) {
				break
			}
		}
	} else {
		if b.destroyed || b.plainStrong <= 0 {
			panic("goxrt: Retain called on destroyed Box")
		}
		b.plainStrong++
	}
	return b
}

func Release[T any](b *Box[T], destructor func(*T)) {
	if b == nil {
		return
	}
	if b.atomic {
		for {
			cur := b.atomicStrong.Load()
			if cur <= 0 {
				panic("goxrt: double free or Release called on destroyed atomic Box")
			}
			if b.atomicStrong.CompareAndSwap(cur, cur-1) {
				if cur == 1 {
					b.destroy(destructor)
				}
				break
			}
		}
	} else {
		if b.destroyed || b.plainStrong <= 0 {
			panic("goxrt: double free or Release called on destroyed Box")
		}
		b.plainStrong--
		if b.plainStrong == 0 {
			b.destroy(destructor)
		}
	}
}

func (b *Box[T]) destroy(destructor func(*T)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.destroyed {
		return
	}
	b.destroyed = true
	if destructor != nil {
		destructor(&b.val)
	}
	var zero T
	b.val = zero
}

func Downgrade[T any](b *Box[T]) *WeakBox[T] {
	if b == nil {
		return nil
	}
	if b.atomic {
		b.atomicWeak.Add(1)
	} else {
		b.plainWeak++
	}
	return &WeakBox[T]{box: b}
}

func Upgrade[T any](w *WeakBox[T]) *Box[T] {
	if w == nil || w.box == nil {
		return nil
	}
	b := w.box
	if b.atomic {
		for {
			cur := b.atomicStrong.Load()
			if cur <= 0 {
				return nil
			}
			if b.atomicStrong.CompareAndSwap(cur, cur+1) {
				return b
			}
		}
	} else {
		if b.destroyed || b.plainStrong <= 0 {
			return nil
		}
		b.plainStrong++
		return b
	}
}

func ReleaseWeak[T any](w *WeakBox[T]) {
	if w == nil || w.box == nil {
		return
	}
	b := w.box
	if b.atomic {
		b.atomicWeak.Add(-1)
	} else {
		b.plainWeak--
	}
	w.box = nil
}

func Value[T any](b *Box[T]) *T {
	if b == nil {
		return nil
	}
	return &b.val
}
`

const runtimeArenaSrc = `package goxrt

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"unsafe"
)

const defaultChunkSize = 64 * 1024

type chunk struct {
	buf    []byte
	offset int
}

type Arena struct {
	chunks     []*chunk
	currentIdx int
	chunkSize  int
	parent     *Arena
}

func NewArena() *Arena {
	return NewArenaWithChunkSize(defaultChunkSize)
}

func NewArenaWithChunkSize(chunkSize int) *Arena {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	first := &chunk{buf: make([]byte, chunkSize)}
	return &Arena{
		chunks:     []*chunk{first},
		currentIdx: 0,
		chunkSize:  chunkSize,
	}
}

func (a *Arena) allocRaw(size, align int) unsafe.Pointer {
	if size <= 0 {
		size = 1
	}
	if align <= 0 {
		align = 8
	}

	cur := a.chunks[a.currentIdx]
	pad := (align - (cur.offset % align)) % align
	needed := pad + size

	if cur.offset+needed > len(cur.buf) {
		allocSize := a.chunkSize
		if needed > allocSize {
			allocSize = needed + 4096
		}
		newChunk := &chunk{buf: make([]byte, allocSize)}
		a.chunks = append(a.chunks, newChunk)
		a.currentIdx = len(a.chunks) - 1
		cur = newChunk
		pad = (align - (cur.offset % align)) % align
		needed = pad + size
	}

	alignedOffset := cur.offset + pad
	cur.offset += needed
	return unsafe.Pointer(&cur.buf[alignedOffset])
}

var zeroByte byte

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

func (a *Arena) Reset() {
	for _, c := range a.chunks {
		c.offset = 0
	}
	a.currentIdx = 0
}

func (a *Arena) Free() {
	for i := range a.chunks {
		a.chunks[i] = nil
	}
	a.chunks = nil
}

func Free(a *Arena) {
	if a != nil {
		a.Free()
	}
}

type requestArenaKeyType struct{}

var requestArenaKey = requestArenaKeyType{}

var arenaPool = sync.Pool{
	New: func() any {
		return NewArena()
	},
}

func GetArena() *Arena {
	return arenaPool.Get().(*Arena)
}

func PutArena(a *Arena) {
	if a != nil {
		a.Reset()
		arenaPool.Put(a)
	}
}

func WithRequestArena(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arena := GetArena()
		defer PutArena(arena)
		ctx := context.WithValue(r.Context(), requestArenaKey, arena)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetRequestArena(ctx context.Context) *Arena {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(requestArenaKey).(*Arena); ok {
		return a
	}
	return nil
}

func RequestArenaContextKey() any {
	return requestArenaKey
}
`

const runtimeImmortalSrc = `package goxrt

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

type immortalPool struct {
	mu           sync.Mutex
	chunks       [][]byte
	currentChunk []byte
	offset       int
	chunkSize    int
	bytesAlloc   atomic.Int64
	allocCount   atomic.Int64
}

var globalPool = &immortalPool{
	chunkSize: 256 * 1024,
	chunks:    [][]byte{make([]byte, 256*1024)},
}

func init() {
	globalPool.currentChunk = globalPool.chunks[0]
}

func AllocImmortal[T any](val T) *T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if size == 0 {
		size = 1
	}
	if align <= 0 {
		align = 8
	}

	globalPool.mu.Lock()
	pad := (align - (globalPool.offset % align)) % align
	needed := pad + size

	if globalPool.offset+needed > len(globalPool.currentChunk) {
		allocSize := globalPool.chunkSize
		if needed > allocSize {
			allocSize = needed + 4096
		}
		newChunk := make([]byte, allocSize)
		globalPool.chunks = append(globalPool.chunks, newChunk)
		globalPool.currentChunk = newChunk
		globalPool.offset = 0
		pad = (align - (globalPool.offset % align)) % align
		needed = pad + size
	}

	alignedOffset := globalPool.offset + pad
	globalPool.offset += needed
	globalPool.bytesAlloc.Add(int64(needed))
	globalPool.allocCount.Add(1)

	ptr := (*T)(unsafe.Pointer(&globalPool.currentChunk[alignedOffset]))
	globalPool.mu.Unlock()

	*ptr = val
	return ptr
}

func AllocReadOnly[T any](val T) *T {
	return AllocImmortal(val)
}
`

const runtimeUniqueSrc = `package goxrt

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

type uniquePool struct {
	mu         sync.Mutex
	freeList   map[uintptr][]unsafe.Pointer
	allocCount atomic.Int64
	freeCount  atomic.Int64
}

var globalUniquePool = &uniquePool{
	freeList: make(map[uintptr][]unsafe.Pointer),
}

func AllocUnique[T any](val T) *T {
	var zero T
	size := unsafe.Sizeof(zero)
	if size == 0 {
		size = 1
	}

	globalUniquePool.mu.Lock()
	list := globalUniquePool.freeList[size]
	var ptr *T
	if len(list) > 0 {
		raw := list[len(list)-1]
		globalUniquePool.freeList[size] = list[:len(list)-1]
		globalUniquePool.mu.Unlock()
		ptr = (*T)(raw)
	} else {
		globalUniquePool.mu.Unlock()
		ptr = new(T)
	}

	globalUniquePool.allocCount.Add(1)
	*ptr = val
	return ptr
}

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

	globalUniquePool.mu.Lock()
	globalUniquePool.freeList[size] = append(globalUniquePool.freeList[size], unsafe.Pointer(ptr))
	globalUniquePool.mu.Unlock()

	globalUniquePool.freeCount.Add(1)
}
`
