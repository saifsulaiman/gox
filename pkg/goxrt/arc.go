package goxrt

import (
	"fmt"
	"sync/atomic"
)

// Box wraps a value of type T with deterministic reference-counting metadata.
type Box[T any] struct {
	val           T
	isAtomic      bool
	atomicRefs    atomic.Int64
	atomicWeak    atomic.Int64
	atomicDestroy atomic.Bool
	plainRefs     int64
	plainWeak     int64
	plainDestroy  bool
}

// WeakBox holds a non-owning weak reference to a Box[T].
type WeakBox[T any] struct {
	box *Box[T]
}

// NewBox creates a new reference-counted Box initialized with a strong reference count of 1.
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

// AllocARC allocates a reference-counted instance of T.
func AllocARC[T any](val T, isAtomic bool) *T {
	return AllocUnique(val)
}

// Retain increments the reference count of the Box.
func Retain[T any](b *Box[T]) *Box[T] {
	if b == nil {
		return nil
	}
	if b.isAtomic {
		old := b.atomicRefs.Add(1)
		if old <= 0 {
			panic(fmt.Sprintf("goxrt: ARC retain of destroyed object (refcount: %d)", old))
		}
	} else {
		if b.plainRefs <= 0 {
			panic(fmt.Sprintf("goxrt: ARC retain of destroyed object (refcount: %d)", b.plainRefs))
		}
		b.plainRefs++
	}
	return b
}

// Release decrements the reference count of the Box.
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
		panic(fmt.Sprintf("goxrt: ARC double-free or negative refcount (%d)", remaining))
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

// Downgrade creates a WeakBox pointing to the given Box.
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

// Upgrade attempts to promote a weak reference to a strong reference.
func (w *WeakBox[T]) Upgrade() *Box[T] {
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

// Value returns a pointer to the wrapped value.
func (b *Box[T]) Value() *T {
	if b == nil {
		return nil
	}
	return &b.val
}
