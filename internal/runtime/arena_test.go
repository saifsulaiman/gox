package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type User struct {
	ID    int
	Name  string
	Email string
}

type Node struct {
	Value int
	Next  *Node
	Prev  *Node
}

func TestArenaAlloc(t *testing.T) {
	arena := NewArena()
	defer arena.Free()

	u1 := Alloc[User](arena)
	u1.ID = 1
	u1.Name = "Alice"
	u1.Email = "alice@example.com"

	u2 := Alloc[User](arena)
	u2.ID = 2
	u2.Name = "Bob"
	u2.Email = "bob@example.com"

	if u1.ID != 1 || u1.Name != "Alice" {
		t.Errorf("u1 corrupted: %+v", u1)
	}
	if u2.ID != 2 || u2.Name != "Bob" {
		t.Errorf("u2 corrupted: %+v", u2)
	}

	stats := arena.Stats()
	if stats.AllocCount != 2 {
		t.Errorf("expected 2 allocs, got %d", stats.AllocCount)
	}
	if stats.BytesInUse <= 0 {
		t.Errorf("expected positive bytes in use, got %d", stats.BytesInUse)
	}
}

func TestArenaAllocSlice(t *testing.T) {
	arena := NewArena()
	defer arena.Free()

	slice := AllocSlice[int](arena, 5, 10)
	if len(slice) != 5 || cap(slice) != 10 {
		t.Fatalf("slice dimensions wrong: len=%d, cap=%d", len(slice), cap(slice))
	}
	for i := 0; i < 5; i++ {
		slice[i] = i * 10
	}
	for i := 0; i < 5; i++ {
		if slice[i] != i*10 {
			t.Errorf("slice[%d] = %d, want %d", i, slice[i], i*10)
		}
	}
}

func TestArenaReset(t *testing.T) {
	arena := NewArena()
	defer arena.Free()

	// Allocate 1000 users
	for i := 0; i < 1000; i++ {
		u := Alloc[User](arena)
		u.ID = i
	}
	statsBefore := arena.Stats()
	if statsBefore.AllocCount != 1000 {
		t.Fatalf("expected 1000 allocs, got %d", statsBefore.AllocCount)
	}

	// Reset arena
	arena.Reset()
	statsAfter := arena.Stats()
	if statsAfter.AllocCount != 0 || statsAfter.BytesInUse != 0 {
		t.Fatalf("reset failed: %+v", statsAfter)
	}
	// Chunks should still be retained for fast reuse
	if statsAfter.ChunkCount == 0 {
		t.Fatal("chunks should be preserved across Reset")
	}

	// Reuse arena
	u := Alloc[User](arena)
	u.ID = 42
	if u.ID != 42 {
		t.Fatalf("reuse failed: u.ID = %d", u.ID)
	}
}

func TestArenaCyclicDataStructures(t *testing.T) {
	arena := NewArena()
	defer arena.Free()

	// Build cyclic doubly-linked list
	head := Alloc[Node](arena)
	head.Value = 1

	tail := Alloc[Node](arena)
	tail.Value = 2

	head.Next = tail
	tail.Prev = head
	tail.Next = head // cycle!
	head.Prev = tail // cycle!

	if head.Next.Next != head {
		t.Error("cyclic reference broken")
	}
	if tail.Prev.Prev != tail {
		t.Error("cyclic reference broken")
	}

	// Bulk free cleanly reclaims entire cyclic structure without cycle detector
	arena.Reset()
	if arena.Stats().BytesInUse != 0 {
		t.Errorf("bytes in use after reset = %d", arena.Stats().BytesInUse)
	}
}

func TestNestedArena(t *testing.T) {
	parent := NewArena()
	defer parent.Free()

	child := NewNestedArena(parent)
	defer child.Free()

	if child.Parent() != parent {
		t.Error("child parent pointer incorrect")
	}

	pUser := Alloc[User](parent)
	cUser := Alloc[User](child)
	pUser.ID = 100
	cUser.ID = 200

	if pUser.ID != 100 || cUser.ID != 200 {
		t.Error("nested arena allocation collision")
	}
}

func BenchmarkArenaAlloc(b *testing.B) {
	arena := NewArena()
	defer arena.Free()

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		u := Alloc[User](arena)
		u.ID = i
		i++
	}
}

func BenchmarkStandardHeapAlloc(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		u := new(User)
		u.ID = i
		_ = u
		i++
	}
}

func BenchmarkArenaResetReuse(b *testing.B) {
	arena := NewArena()
	defer arena.Free()

	b.ReportAllocs()
	for b.Loop() {
		// Simulate a request: allocate 50 objects and reset
		for j := 0; j < 50; j++ {
			u := Alloc[User](arena)
			u.ID = j
		}
		arena.Reset()
	}
}

func TestWithRequestArena(t *testing.T) {
	executed := false
	handler := WithRequestArena(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arena := GetRequestArena(r.Context())
		if arena == nil {
			t.Fatal("expected request arena in context, got nil")
		}

		u := Alloc[User](arena)
		u.ID = 100
		u.Name = "RequestUser"

		slice := AllocSlice[byte](arena, 256, 256)
		if len(slice) != 256 {
			t.Fatalf("expected slice length 256, got %d", len(slice))
		}

		stats := arena.Stats()
		if stats.AllocCount != 2 {
			t.Errorf("expected 2 allocs in request arena, got %d", stats.AllocCount)
		}
		executed = true
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !executed {
		t.Fatal("handler was not executed")
	}
}
