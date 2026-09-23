package runtime

import (
	"context"
	"testing"
)

func TestInternalArenaEdgeCases(t *testing.T) {
	// 1. NewArenaWithChunkSize with 0
	a1 := NewArenaWithChunkSize(0)
	if a1.chunkSize != defaultChunkSize {
		t.Errorf("expected defaultChunkSize, got %d", a1.chunkSize)
	}
	a1.Free()

	parent := NewArena()
	defer parent.Free()

	// 2. AllocVal
	pVal := AllocVal(parent, 12345)
	if *pVal != 12345 {
		t.Errorf("expected 12345, got %d", *pVal)
	}

	// 3. Zero-sized allocations
	type Empty struct{}
	pEmpty := Alloc[Empty](parent)
	if pEmpty == nil {
		t.Errorf("expected non-nil pointer for Alloc[Empty]")
	}
	pEmptyVal := AllocVal(parent, Empty{})
	if pEmptyVal == nil {
		t.Errorf("expected non-nil pointer for AllocVal[Empty]")
	}

	// 4. AllocSlice edge cases
	sZero := AllocSlice[int](parent, 0, 0)
	if len(sZero) != 0 || cap(sZero) != 0 {
		t.Errorf("expected len 0 cap 0")
	}

	assertInternalPanic(t, "AllocSlice negative length", func() {
		AllocSlice[int](parent, -1, 10)
	})
	assertInternalPanic(t, "AllocSlice cap < len", func() {
		AllocSlice[int](parent, 10, 5)
	})

	// Large slice exceeding chunk size
	smallArena := NewArenaWithChunkSize(128)
	defer smallArena.Free()
	largeSlice := AllocSlice[byte](smallArena, 1024, 1024)
	if len(largeSlice) != 1024 {
		t.Errorf("expected 1024 bytes allocated")
	}

	// 5. allocRaw panic on negative size and alignment default
	assertInternalPanic(t, "allocRaw negative size", func() {
		smallArena.allocRaw(-5, 8)
	})
	ptr := smallArena.allocRaw(16, 0)
	if ptr == nil {
		t.Errorf("expected non-nil pointer from allocRaw with default align")
	}

	// 6. Free(nil) and Free(a)
	Free(nil)
	child := NewArena()
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

func TestInternalARCEdgeCases(t *testing.T) {
	// 1. AllocARC
	arcVal := AllocARC(999, false)
	if *arcVal != 999 {
		t.Errorf("expected 999, got %d", *arcVal)
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
	if Upgrade(nilWeak) != nil {
		t.Errorf("expected nil WeakBox Upgrade to return nil")
	}
	if RetainWeak[int](nil) != nil {
		t.Errorf("expected RetainWeak(nil) to return nil")
	}
	if ReleaseWeak[int](nil) {
		t.Errorf("expected ReleaseWeak(nil) to return false")
	}

	// Accessors with nil
	if Value[int](nil) != nil {
		t.Errorf("expected Value(nil) to return nil")
	}
	if Count[int](nil) != 0 {
		t.Errorf("expected Count(nil) to return 0")
	}
	if WeakCount[int](nil) != 0 {
		t.Errorf("expected WeakCount(nil) to return 0")
	}
	if IsAtomic[int](nil) {
		t.Errorf("expected IsAtomic(nil) to return false")
	}

	// 3. Normal accessors and weak reference tracking
	plainBox := NewBox("plain-val", false)
	if *Value(plainBox) != "plain-val" {
		t.Errorf("Value mismatch")
	}
	if Count(plainBox) != 1 {
		t.Errorf("expected Count=1, got %d", Count(plainBox))
	}
	if IsAtomic(plainBox) {
		t.Errorf("expected IsAtomic=false")
	}

	plainWeak := Downgrade(plainBox)
	if WeakCount(plainBox) != 1 {
		t.Errorf("expected WeakCount=1, got %d", WeakCount(plainBox))
	}
	RetainWeak(plainWeak)
	if WeakCount(plainBox) != 2 {
		t.Errorf("expected WeakCount=2, got %d", WeakCount(plainBox))
	}
	if ReleaseWeak(plainWeak) { // 2 -> 1, not 0
		t.Errorf("expected ReleaseWeak to return false when remaining > 0")
	}
	if !ReleaseWeak(plainWeak) { // 1 -> 0, returns true
		t.Errorf("expected ReleaseWeak to return true when remaining == 0")
	}

	// Atomic box weak ref tracking
	atomicBox := NewBox(42, true)
	if !IsAtomic(atomicBox) {
		t.Errorf("expected IsAtomic=true")
	}
	if Count(atomicBox) != 1 {
		t.Errorf("expected Count=1, got %d", Count(atomicBox))
	}
	atomicWeak := Downgrade(atomicBox)
	RetainWeak(atomicWeak)
	ReleaseWeak(atomicWeak)
	ReleaseWeak(atomicWeak)

	// Weak reference underflow panic
	assertInternalPanic(t, "plain weak underflow", func() {
		ReleaseWeak(plainWeak)
	})
	assertInternalPanic(t, "atomic weak underflow", func() {
		ReleaseWeak(atomicWeak)
	})

	// 4. Destructor callback
	destructorCalled := false
	box := NewBox("destruct-me", false)
	Release(box, func(s *string) {
		if *s == "destruct-me" {
			destructorCalled = true
		}
	})
	if !destructorCalled {
		t.Errorf("expected destructor to be called")
	}

	// 5. Retain on destroyed object panic
	pBox := NewBox(1, false)
	Release(pBox, nil)
	assertInternalPanic(t, "Retain destroyed plain box", func() {
		Retain(pBox)
	})

	aBox := NewBox(2, true)
	Release(aBox, nil)
	assertInternalPanic(t, "Retain destroyed atomic box", func() {
		Retain(aBox)
	})

	// 6. Double release (negative refcount) panic
	pBox2 := NewBox(10, false)
	Release(pBox2, nil)
	assertInternalPanic(t, "Double release plain box", func() {
		Release(pBox2, nil)
	})

	aBox2 := NewBox(20, true)
	Release(aBox2, nil)
	assertInternalPanic(t, "Double release atomic box", func() {
		Release(aBox2, nil)
	})

	// 7. Weak upgrade on destroyed object returns nil
	plainW := Downgrade(NewBox(100, false))
	Release(plainW.box, nil)
	if Upgrade(plainW) != nil {
		t.Errorf("expected Upgrade on destroyed plain box to return nil")
	}

	atomicW := Downgrade(NewBox(200, true))
	Release(atomicW.box, nil)
	if Upgrade(atomicW) != nil {
		t.Errorf("expected Upgrade on destroyed atomic box to return nil")
	}
}

func TestInternalImmortalEdgeCases(t *testing.T) {
	pool := newImmortalPool(0)
	if pool.chunkSize != defaultImmortalChunkSize {
		t.Errorf("expected defaultImmortalChunkSize")
	}

	ptr := pool.allocRaw(0, 0)
	if ptr == nil {
		t.Errorf("expected non-nil pointer")
	}

	smallPool := newImmortalPool(128)
	ptrLarge := smallPool.allocRaw(1024, 16)
	if ptrLarge == nil {
		t.Errorf("expected non-nil pointer for large allocation")
	}

	type Empty struct{}
	pEmpty := AllocImmortal(Empty{})
	if pEmpty == nil {
		t.Errorf("expected non-nil pointer for AllocImmortal zero-sized")
	}
}

func TestInternalUniqueEdgeCases(t *testing.T) {
	FreeUnique[int](nil)

	type Empty struct{}
	e1 := AllocUnique(Empty{})
	FreeUnique(e1)

	u1 := AllocUnique(42)
	FreeUnique(u1)
	u2 := AllocUnique(99)
	if *u2 != 99 {
		t.Errorf("expected 99 from reused slab, got %d", *u2)
	}
	FreeUnique(u2)

	stats := GetUniqueStats()
	if stats.AllocCount == 0 || stats.FreeCount == 0 {
		t.Errorf("expected non-zero unique stats: %+v", stats)
	}
}

func assertInternalPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s: expected panic, but code executed without panic", name)
		}
	}()
	fn()
}
