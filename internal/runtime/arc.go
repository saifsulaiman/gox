package runtime

import (
	"fmt"
	"sync/atomic"
)

// Box wraps a value of type T with deterministic reference-counting metadata.
// It supports both goroutine-confined non-atomic counting and thread-safe atomic counting.
// It tracks both strong references (keeping the payload alive) and weak references
// (non-owning cycle-breaking references).
type Box[T any] struct {
	val           T
	isAtomic      bool
	atomicRefs    atomic.Int64 // Strong reference count
	atomicWeak    atomic.Int64 // Weak reference count
	atomicDestroy atomic.Bool  // Atomic destroyed indicator
	plainRefs     int64        // Non-atomic strong reference count
	plainWeak     int64        // Non-atomic weak reference count
	plainDestroy  bool         // Non-atomic destroyed indicator
}

// WeakBox holds a non-owning weak reference to a Box[T].
// It allows breaking reference cycles while permitting safe Upgrade() to strong references.
type WeakBox[T any] struct {
	box *Box[T]
}

// NewBox creates a new reference-counted Box initialized with a strong reference count of 1.
// When isAtomic is false, the Box uses single-threaded operations (zero synchronization overhead).
// When isAtomic is true, the Box uses atomic operations safe for multi-goroutine sharing.
func NewBox[T any](val T, isAtomic bool) *Box[T] {
	b := &Box[T]{
		val:      val,
		isAtomic: isAtomic,
	}
	if isAtomic {
		b.atomicRefs.Store(1)
		b.atomicWeak.Store(0)
	} else {
		b.plainRefs = 1
		b.plainWeak = 0
	}
	return b
}

// AllocARC allocates a reference-counted instance of T, returning a direct typed pointer *T
// that is 100% type-compatible with standard Go function signatures while managing memory
// through the ARC slab pool.
func AllocARC[T any](val T, isAtomic bool) *T {
	return AllocUnique(val)
}

// Retain increments the reference count of the Box and returns the Box.
func Retain[T any](b *Box[T]) *Box[T] {
	if b == nil {
		return nil
	}
	if b.isAtomic {
		newRefs := b.atomicRefs.Add(1)
		if newRefs <= 1 {
			panic(fmt.Sprintf("gox: ARC retain of destroyed object (refcount: %d)", newRefs))
		}
	} else {
		if b.plainRefs <= 0 {
			panic(fmt.Sprintf("gox: ARC retain of destroyed object (refcount: %d)", b.plainRefs))
		}
		b.plainRefs++
	}
	return b
}

// Release decrements the reference count of the Box.
// If the reference count drops to zero, the optional destructor is invoked,
// the underlying value is zeroed, and Release returns true.
// Otherwise, Release returns false.
func Release[T any](b *Box[T], destructor func(*T)) bool {
	if b == nil {
		return false
	}

	var remaining int64
	if b.isAtomic {
		remaining = b.atomicRefs.Add(-1)
	} else {
		b.plainRefs--
		remaining = b.plainRefs
	}

	if remaining < 0 {
		panic(fmt.Sprintf("gox: ARC double-free or negative refcount (%d)", remaining))
	}

	if remaining == 0 {
		if b.isAtomic {
			b.atomicDestroy.Store(true)
		} else {
			b.plainDestroy = true
		}
		if destructor != nil {
			destructor(&b.val)
		}
		var zero T
		b.val = zero
		return true
	}

	return false
}

// Downgrade creates a new non-owning WeakBox referring to the target Box.
// The weak reference count is incremented.
func Downgrade[T any](b *Box[T]) *WeakBox[T] {
	if b == nil {
		return nil
	}
	if b.isAtomic {
		b.atomicWeak.Add(1)
	} else {
		b.plainWeak++
	}
	return &WeakBox[T]{box: b}
}

// Upgrade attempts to promote a non-owning WeakBox to a strong owning Box.
// If the underlying value is still alive, the strong reference count is incremented,
// and the strong Box pointer is returned.
// If the underlying value has already been released/destroyed, Upgrade returns nil safely.
func Upgrade[T any](w *WeakBox[T]) *Box[T] {
	if w == nil || w.box == nil {
		return nil
	}
	b := w.box
	if b.isAtomic {
		for {
			cur := b.atomicRefs.Load()
			if cur <= 0 || b.atomicDestroy.Load() {
				return nil
			}
			if b.atomicRefs.CompareAndSwap(cur, cur+1) {
				if b.atomicDestroy.Load() {
					b.atomicRefs.Add(-1)
					return nil
				}
				return b
			}
		}
	} else {
		if b.plainRefs <= 0 || b.plainDestroy {
			return nil
		}
		b.plainRefs++
		return b
	}
}

// RetainWeak increments the weak reference count of the WeakBox.
func RetainWeak[T any](w *WeakBox[T]) *WeakBox[T] {
	if w == nil || w.box == nil {
		return nil
	}
	if w.box.isAtomic {
		w.box.atomicWeak.Add(1)
	} else {
		w.box.plainWeak++
	}
	return w
}

// ReleaseWeak decrements the weak reference count of the WeakBox.
func ReleaseWeak[T any](w *WeakBox[T]) bool {
	if w == nil || w.box == nil {
		return false
	}
	var rem int64
	if w.box.isAtomic {
		rem = w.box.atomicWeak.Add(-1)
	} else {
		w.box.plainWeak--
		rem = w.box.plainWeak
	}
	if rem < 0 {
		panic(fmt.Sprintf("gox: weak reference underflow (%d)", rem))
	}
	return rem == 0
}

// Value returns a pointer to the wrapped value inside the Box.
func Value[T any](b *Box[T]) *T {
	if b == nil {
		return nil
	}
	return &b.val
}

// Count returns the current strong reference count of the Box.
func Count[T any](b *Box[T]) int64 {
	if b == nil {
		return 0
	}
	if b.isAtomic {
		return b.atomicRefs.Load()
	}
	return b.plainRefs
}

// WeakCount returns the current count of weak references to the Box.
func WeakCount[T any](b *Box[T]) int64 {
	if b == nil {
		return 0
	}
	if b.isAtomic {
		return b.atomicWeak.Load()
	}
	return b.plainWeak
}

// IsAtomic reports whether the Box uses atomic operations.
func IsAtomic[T any](b *Box[T]) bool {
	if b == nil {
		return false
	}
	return b.isAtomic
}
