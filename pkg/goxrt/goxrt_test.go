package goxrt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestArenaLifecycle(t *testing.T) {
	arena := NewArena()
	defer arena.Free()

	// 1. Allocate int and struct using AllocVal and Alloc
	pInt := AllocVal(arena, 42)
	if *pInt != 42 {
		t.Fatalf("expected 42, got %d", *pInt)
	}

	type User struct {
		ID   int
		Name string
	}
	pUser := AllocVal(arena, User{ID: 101, Name: "Alice"})
	if pUser.ID != 101 || pUser.Name != "Alice" {
		t.Fatalf("unexpected user: %+v", pUser)
	}

	pZero := Alloc[User](arena)
	if pZero.ID != 0 || pZero.Name != "" {
		t.Fatalf("expected zeroed struct")
	}

	// 2. Allocate Slice
	slice := AllocSlice[int](arena, 10, 16)
	if len(slice) != 10 || cap(slice) != 16 {
		t.Fatalf("expected len 10 cap 16, got len %d cap %d", len(slice), cap(slice))
	}

	// 3. Stats inspection
	stats := arena.Stats()
	if stats.AllocCount < 3 {
		t.Errorf("expected alloc count >= 3, got %d", stats.AllocCount)
	}
	if stats.ChunkCount < 1 {
		t.Errorf("expected at least 1 chunk")
	}

	// 4. Reset arena
	arena.Reset()
	postResetStats := arena.Stats()
	if postResetStats.BytesInUse != 0 {
		t.Errorf("expected 0 bytes in use after Reset(), got %d", postResetStats.BytesInUse)
	}
}

func TestArenaPoolAndMiddleware(t *testing.T) {
	// Test arena borrowing and returning to pool
	a := GetArena()
	p := AllocVal(a, "pooled-string")
	if *p != "pooled-string" {
		t.Fatalf("unexpected string: %s", *p)
	}
	PutArena(a)

	// Test HTTP Middleware integration
	handler := WithRequestArena(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqArena := GetRequestArena(r.Context())
		if reqArena == nil {
			t.Errorf("expected arena in request context")
		}
		val := AllocVal(reqArena, 999)
		if *val != 999 {
			t.Errorf("expected 999")
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// AdaptArenaHandler
	adaptHandler := AdaptArenaHandler(ArenaHandlerFunc(func(w http.ResponseWriter, r *http.Request, arena *Arena) {
		if arena == nil {
			t.Errorf("expected non-nil arena in AdaptArenaHandler")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	recAdapt := httptest.NewRecorder()
	adaptHandler.ServeHTTP(recAdapt, req)
	if recAdapt.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", recAdapt.Code)
	}

	// Fallback when context has no arena
	if GetRequestArena(context.Background()) != nil {
		t.Errorf("expected nil arena for empty context")
	}
}

func TestARCLifecycle(t *testing.T) {
	// Plain (Single-threaded) ARC Box
	box := NewBox(100, false)
	if *box.Value() != 100 {
		t.Fatalf("expected 100, got %d", *box.Value())
	}
	Retain(box)
	Release(box, nil)
	if *box.Value() != 100 {
		t.Fatalf("expected 100 still present")
	}

	// Weak reference downgrade and upgrade
	weak := Downgrade(box)
	upgraded := weak.Upgrade()
	if upgraded == nil || *upgraded.Value() != 100 {
		t.Fatalf("weak upgrade failed")
	}
	Release(upgraded, nil)
	Release(box, nil) // count -> 0, destroyed

	// Atomic ARC Box
	atomicBox := NewBox("concurrent-arc", true)
	Retain(atomicBox)
	atomicWeak := Downgrade(atomicBox)
	Release(atomicBox, nil)
	atomicUpgraded := atomicWeak.Upgrade()
	if atomicUpgraded == nil || *atomicUpgraded.Value() != "concurrent-arc" {
		t.Fatalf("atomic weak upgrade failed")
	}
	Release(atomicUpgraded, nil)
	Release(atomicBox, nil)
}

func TestImmortalAlloc(t *testing.T) {
	val := AllocImmortal(12345)
	if *val != 12345 {
		t.Fatalf("expected 12345, got %d", *val)
	}

	ro := AllocReadOnly("immutable-config")
	if *ro != "immutable-config" {
		t.Fatalf("expected immutable-config, got %s", *ro)
	}

	stats := GetImmortalStats()
	if stats.AllocCount == 0 {
		t.Errorf("expected non-zero immortal alloc count")
	}
}

func TestUniqueAlloc(t *testing.T) {
	ptr := AllocUnique(9876)
	if *ptr != 9876 {
		t.Fatalf("expected 9876, got %d", *ptr)
	}

	FreeUnique(ptr)

	stats := GetUniqueStats()
	if stats.AllocCount == 0 || stats.FreeCount == 0 {
		t.Errorf("expected non-zero unique stats")
	}
}
