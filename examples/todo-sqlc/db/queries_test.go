package db

import (
	"context"
	"database/sql"
	"testing"

	goxrt "github.com/goxlang/gox/pkg/goxrt"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, *Queries) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}

	q, err := NewQueries(db)
	if err != nil {
		t.Fatalf("failed to create queries: %v", err)
	}

	return db, q
}

func TestQueriesCRUD(t *testing.T) {
	db, q := setupTestDB(t)
	defer db.Close()
	defer q.Close()

	ctx := context.Background()

	// 1. Create
	item, err := q.Create(ctx, "Test Item", PriorityHigh)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if item.ID == 0 || item.Title != "Test Item" || item.Priority != PriorityHigh || item.Completed {
		t.Fatalf("unexpected item: %+v", item)
	}

	// 2. GetByID
	fetched, err := q.GetByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if fetched.ID != item.ID {
		t.Fatalf("mismatched ID: %d vs %d", fetched.ID, item.ID)
	}

	// 3. Toggle
	toggled, err := q.Toggle(ctx, item.ID)
	if err != nil {
		t.Fatalf("toggle failed: %v", err)
	}
	if !toggled.Completed {
		t.Fatalf("expected completed=true, got %v", toggled.Completed)
	}

	// 4. Update
	updated, err := q.Update(ctx, item.ID, "Updated Title", PriorityLow)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if updated.Title != "Updated Title" || updated.Priority != PriorityLow {
		t.Fatalf("unexpected updated item: %+v", updated)
	}

	// 5. Stats
	stats, err := q.Stats(ctx)
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	if stats.Total != 1 || stats.Completed != 1 || stats.Active != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// 6. List with Arena
	arena := goxrt.NewArena()
	defer arena.Free()

	list, err := q.ListArena(ctx, arena, "all")
	if err != nil {
		t.Fatalf("list arena failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 item in list, got %d", len(list))
	}

	// 7. Delete
	if err := q.Delete(ctx, item.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	// Verify delete
	_, err = q.GetByID(ctx, item.ID)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}
