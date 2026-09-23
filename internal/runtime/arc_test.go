package runtime

import (
	"sync"
	"sync/atomic"
	"testing"
)

type Resource struct {
	Name     string
	Cleaned  bool
	CleanOps int
}

func TestARCPlainLifecycle(t *testing.T) {
	res := Resource{Name: "config"}
	cleaned := false

	box := NewBox(res, false)
	if Count(box) != 1 {
		t.Fatalf("expected initial count 1, got %d", Count(box))
	}
	if IsAtomic(box) {
		t.Error("expected non-atomic box")
	}

	val := Value(box)
	if val.Name != "config" {
		t.Errorf("expected name 'config', got %q", val.Name)
	}

	// Retain
	Retain(box)
	if Count(box) != 2 {
		t.Errorf("expected count 2 after Retain, got %d", Count(box))
	}

	// First release
	freed := Release(box, func(r *Resource) {
		cleaned = true
	})
	if freed {
		t.Error("expected box NOT to be freed on first release")
	}
	if cleaned {
		t.Error("destructor should not be called while count > 0")
	}
	if Count(box) != 1 {
		t.Errorf("expected count 1, got %d", Count(box))
	}

	// Second release -> should free
	freed = Release(box, func(r *Resource) {
		cleaned = true
	})
	if !freed {
		t.Error("expected box to be freed on second release")
	}
	if !cleaned {
		t.Error("destructor should have been called when count reached 0")
	}
	if Count(box) != 0 {
		t.Errorf("expected count 0, got %d", Count(box))
	}
}

func TestARCAtomicConcurrent(t *testing.T) {
	res := Resource{Name: "shared_table"}
	cleanups := 0
	var mu sync.Mutex

	box := NewBox(res, true)
	if !IsAtomic(box) {
		t.Error("expected atomic box")
	}

	const numGoroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				Retain(box)
				// Access value safely
				_ = Value(box).Name
				Release(box, func(r *Resource) {
					mu.Lock()
					cleanups++
					mu.Unlock()
				})
			}
		}()
	}

	wg.Wait()

	// Box should still have its initial 1 reference
	if Count(box) != 1 {
		t.Fatalf("expected count 1 after worker completion, got %d", Count(box))
	}
	if cleanups != 0 {
		t.Errorf("expected 0 cleanups while primary ref is held, got %d", cleanups)
	}

	// Final release
	freed := Release(box, func(r *Resource) {
		mu.Lock()
		cleanups++
		mu.Unlock()
	})

	if !freed {
		t.Error("expected final release to free box")
	}
	if cleanups != 1 {
		t.Errorf("expected exactly 1 cleanup, got %d", cleanups)
	}
}

func TestARCDoubleFreePanics(t *testing.T) {
	box := NewBox(42, false)
	Release(box, nil) // Count drops to 0

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected double-release to panic")
		}
	}()

	Release(box, nil) // Should panic
}

func TestARCRetainDestroyedPanics(t *testing.T) {
	box := NewBox(42, false)
	Release(box, nil) // Count drops to 0

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected retain of destroyed box to panic")
		}
	}()

	Retain(box) // Should panic
}

func TestWeakReferencePlain(t *testing.T) {
	cleaned := false
	box := NewBox(Resource{Name: "parent"}, false)

	// Create weak reference
	weak := Downgrade(box)
	if WeakCount(box) != 1 {
		t.Fatalf("expected 1 weak ref, got %d", WeakCount(box))
	}
	if Count(box) != 1 {
		t.Fatalf("expected 1 strong ref, got %d", Count(box))
	}

	// Successful upgrade while strong ref is alive
	upgraded := Upgrade(weak)
	if upgraded == nil {
		t.Fatal("expected successful upgrade while target is alive")
	}
	if Count(box) != 2 {
		t.Errorf("expected strong count 2 after upgrade, got %d", Count(box))
	}
	if Value(upgraded).Name != "parent" {
		t.Errorf("expected value 'parent', got %q", Value(upgraded).Name)
	}

	// Release upgraded reference
	Release(upgraded, nil)
	if Count(box) != 1 {
		t.Errorf("expected strong count 1 after release, got %d", Count(box))
	}

	// Release original strong reference
	freed := Release(box, func(r *Resource) {
		cleaned = true
	})
	if !freed {
		t.Error("expected box to be freed when strong count reached 0")
	}
	if !cleaned {
		t.Error("expected destructor to run when strong count reached 0")
	}
	if Count(box) != 0 {
		t.Errorf("expected strong count 0, got %d", Count(box))
	}

	// Weak reference should still be active
	if WeakCount(box) != 1 {
		t.Errorf("expected weak count 1 after strong release, got %d", WeakCount(box))
	}

	// Upgrade after target destroyed must return nil safely (Zero Use-After-Free)
	deadUpgrade := Upgrade(weak)
	if deadUpgrade != nil {
		t.Error("expected Upgrade() on destroyed target to return nil")
	}

	// Release weak reference
	weakFreed := ReleaseWeak(weak)
	if !weakFreed {
		t.Error("expected ReleaseWeak to report true for final weak ref")
	}
}

func TestWeakReferenceAtomicConcurrent(t *testing.T) {
	box := NewBox(Resource{Name: "concurrent_tree_root"}, true)
	weak := Downgrade(box)

	const numWorkers = 30
	const iterations = 50

	var wg sync.WaitGroup
	var successfulUpgrades atomic.Int64
	var failedUpgrades atomic.Int64

	// Concurrently attempt upgrades
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				up := Upgrade(weak)
				if up != nil {
					// Safely read payload
					_ = Value(up).Name
					successfulUpgrades.Add(1)
					Release(up, nil)
				} else {
					failedUpgrades.Add(1)
				}
			}
		}()
	}

	// Concurrently release the primary strong reference
	go func() {
		Release(box, nil)
	}()

	wg.Wait()

	// Once all workers finish, upgrade MUST return nil
	if up := Upgrade(weak); up != nil {
		t.Error("expected Upgrade() to return nil after full completion")
	}

	ReleaseWeak(weak)
}

