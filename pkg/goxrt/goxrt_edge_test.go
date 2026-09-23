package goxrt

import (
	"context"
	"testing"
)

func TestArenaEdgeCases(t *testing.T) {
	// 1. NewArenaWithChunkSize with 0/negative
	a1 := NewArenaWithChunkSize(0)
	if a1.chunkSize != defaultChunkSize {
		t.Errorf("expected defaultChunkSize, got %d", a1.chunkSize)
	}
	a1.Free()

	// 2. NewNestedArena and Parent
	parent := NewArena()
	defer parent.Free()
	child := NewNestedArena(parent)
	if child.Parent() != parent {
		t.Errorf("expected child parent to match parent arena")
	}
	if parent.Parent() != nil {
		t.Errorf("expected parent to have nil parent")
	}

	// 3. Zero-sized allocations
	type Empty struct{}
	pEmpty := Alloc[Empty](parent)
	if pEmpty == nil {
		t.Errorf("expected non-nil pointer for zero-sized Alloc")
	}
	pEmptyVal := AllocVal(parent, Empty{})
	if pEmptyVal == nil {
		t.Errorf("expected non-nil pointer for zero-sized AllocVal")
	}

	// 4. AllocSlice edge cases
	sZero := AllocSlice[int](parent, 0, 0)
	if len(sZero) != 0 || cap(sZero) != 0 {
		t.Errorf("expected len 0 cap 0 slice")
	}

	// Panic on invalid length/capacity
	assertPanic(t, "AllocSlice negative length", func() {
		AllocSlice[int](parent, -1, 10)
	})
	assertPanic(t, "AllocSlice cap < len", func() {
		AllocSlice[int](parent, 10, 5)
	})

	// Large allocation exceeding chunk size
	smallArena := NewArenaWithChunkSize(128)
	defer smallArena.Free()
	largeSlice := AllocSlice[byte](smallArena, 1024, 1024)
	if len(largeSlice) != 1024 {
		t.Errorf("expected 1024 bytes allocated across new chunk")
	}

	// 5. allocRaw panic on negative size and alignment default
	assertPanic(t, "allocRaw negative size", func() {
		smallArena.allocRaw(-5, 8)
	})
	ptr := smallArena.allocRaw(16, 0) // align <= 0 default
	if ptr == nil {
		t.Errorf("expected non-nil pointer from allocRaw with default align")
	}

	// 6. Free(nil) and Free(a)
	Free(nil)
	Free(child)
	if child.chunks != nil {
		t.Errorf("expected child.chunks to be nil after Free")
	}

	// 7. RequestArenaContextKey
	key := RequestArenaContextKey()
	ctx := context.WithValue(context.Background(), key, parent)
	if GetRequestArena(ctx) != parent {
		t.Errorf("expected GetRequestArena to retrieve arena via RequestArenaContextKey")
	}
}

func TestARCEdgeCases(t *testing.T) {
	// 1. AllocARC
	arcVal := AllocARC(777, false)
	if *arcVal != 777 {
		t.Errorf("expected 777, got %d", *arcVal)
	}

	// 2. Retain & Release with nil
	if Retain[int](nil) != nil {
		t.Errorf("expected Retain(nil) to return nil")
	}
	if Release[int](nil, nil) {
		t.Errorf("expected Release(nil) to return false")
	}
	if Downgrade[int](nil) != nil {
		t.Errorf("expected Downgrade(nil) to return nil")
	}
	var nilWeak *WeakBox[int]
	if nilWeak.Upgrade() != nil {
		t.Errorf("expected nil WeakBox Upgrade to return nil")
	}
	var nilBox *Box[int]
	if nilBox.Value() != nil {
		t.Errorf("expected nil Box Value to return nil")
	}

	// 3. Destructor callback
	destructorCalled := false
	box := NewBox("destruct-me", false)
	Release(box, func(s *string) {
		if *s == "destruct-me" {
			destructorCalled = true
		}
	})
	if !destructorCalled {
		t.Errorf("expected destructor to be called on final release")
	}

	// 4. Retain on destroyed object panic
	plainBox := NewBox(1, false)
	Release(plainBox, nil)
	assertPanic(t, "Retain destroyed plain box", func() {
		Retain(plainBox)
	})

	atomicBox := NewBox(2, true)
	Release(atomicBox, nil)
	assertPanic(t, "Retain destroyed atomic box", func() {
		Retain(atomicBox)
	})

	// 5. Double release (negative refcount) panic
	pBox2 := NewBox(10, false)
	Release(pBox2, nil)
	assertPanic(t, "Double release plain box", func() {
		Release(pBox2, nil)
	})

	aBox2 := NewBox(20, true)
	Release(aBox2, nil)
	assertPanic(t, "Double release atomic box", func() {
		Release(aBox2, nil)
	})

	// 6. Weak upgrade on destroyed object returns nil
	plainW := Downgrade(NewBox(100, false))
	Release(plainW.box, nil)
	if plainW.Upgrade() != nil {
		t.Errorf("expected Upgrade on destroyed plain box to return nil")
	}

	atomicW := Downgrade(NewBox(200, true))
	Release(atomicW.box, nil)
	if atomicW.Upgrade() != nil {
		t.Errorf("expected Upgrade on destroyed atomic box to return nil")
	}
}

func TestImmortalEdgeCases(t *testing.T) {
	// 1. newImmortalPool with 0
	pool := newImmortalPool(0)
	if pool.chunkSize != defaultImmortalChunkSize {
		t.Errorf("expected defaultImmortalChunkSize")
	}

	// 2. allocRaw with 0 size and 0 align
	ptr := pool.allocRaw(0, 0)
	if ptr == nil {
		t.Errorf("expected non-nil pointer")
	}

	// 3. Allocation exceeding chunk size
	smallPool := newImmortalPool(128)
	ptrLarge := smallPool.allocRaw(1024, 16)
	if ptrLarge == nil {
		t.Errorf("expected non-nil pointer for large allocation")
	}

	// 4. Zero-sized AllocImmortal
	type Empty struct{}
	pEmpty := AllocImmortal(Empty{})
	if pEmpty == nil {
		t.Errorf("expected non-nil pointer for AllocImmortal zero-sized")
	}
}

func TestUniqueEdgeCases(t *testing.T) {
	// 1. FreeUnique(nil)
	FreeUnique[int](nil)

	// 2. Alloc and Free with zero-sized struct
	type Empty struct{}
	e1 := AllocUnique(Empty{})
	FreeUnique(e1)

	// 3. Slab pool free-list reuse branch
	u1 := AllocUnique(42)
	FreeUnique(u1)
	u2 := AllocUnique(99) // takes from freeList
	if *u2 != 99 {
		t.Errorf("expected 99 from reused slab, got %d", *u2)
	}
	FreeUnique(u2)

	// 4. Unique stats
	stats := GetUniqueStats()
	if stats.AllocCount == 0 || stats.FreeCount == 0 {
		t.Errorf("expected non-zero unique stats: %+v", stats)
	}
}

func assertPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s: expected panic, but code executed without panic", name)
		}
	}()
	fn()
}
