package models

import (
	"testing"
)

func setupTestStore(t *testing.T) *TodoStore {
	store, err := NewTodoStore("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	return store
}

func TestTodoCRUD(t *testing.T) {
	store := setupTestStore(t)

	// 1. Create
	item, err := store.Create("Buy milk and coffee", PriorityHigh)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if item.ID == 0 {
		t.Errorf("expected non-zero ID, got %d", item.ID)
	}
	if item.Title != "Buy milk and coffee" || item.Priority != PriorityHigh || item.Completed {
		t.Errorf("unexpected created item: %+v", item)
	}

	// 2. Read / GetByID
	fetched, err := store.GetByID(item.ID)
	if err != nil {
		t.Fatalf("GetByID error: %v", err)
	}
	if fetched.Title != item.Title {
		t.Errorf("expected title %s, got %s", item.Title, fetched.Title)
	}

	// 3. Update
	updated, err := store.Update(item.ID, "Buy oat milk and tea", PriorityMedium)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Title != "Buy oat milk and tea" || updated.Priority != PriorityMedium {
		t.Errorf("unexpected updated item: %+v", updated)
	}

	// 4. Toggle
	toggled, err := store.Toggle(item.ID)
	if err != nil {
		t.Fatalf("Toggle error: %v", err)
	}
	if !toggled.Completed {
		t.Errorf("expected completed=true, got false")
	}

	// 5. Stats
	total, active, completed, err := store.Stats()
	if err != nil {
		t.Fatalf("Stats error: %v", err)
	}
	if total != 1 || active != 0 || completed != 1 {
		t.Errorf("expected total=1, active=0, completed=1, got %d/%d/%d", total, active, completed)
	}

	// 6. Delete
	if err := store.Delete(item.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err = store.GetByID(item.ID)
	if err == nil {
		t.Errorf("expected error getting deleted item, got nil")
	}
}

func TestTodoListAndFilters(t *testing.T) {
	store := setupTestStore(t)

	// Add 3 items
	t1, _ := store.Create("Task 1", PriorityLow)
	_, _ = store.Create("Task 2", PriorityMedium)
	t3, _ := store.Create("Task 3", PriorityHigh)

	// Complete t1 and t3
	_, _ = store.Toggle(t1.ID)
	_, _ = store.Toggle(t3.ID)

	all, err := store.List("all")
	if err != nil || len(all) != 3 {
		t.Fatalf("expected 3 items, got %d (err: %v)", len(all), err)
	}

	active, err := store.List("active")
	if err != nil || len(active) != 1 {
		t.Fatalf("expected 1 active item, got %d (err: %v)", len(active), err)
	}
	if active[0].Title != "Task 2" {
		t.Errorf("expected Task 2 active, got %s", active[0].Title)
	}

	completed, err := store.List("completed")
	if err != nil || len(completed) != 2 {
		t.Fatalf("expected 2 completed items, got %d (err: %v)", len(completed), err)
	}

	// Clear completed
	cleared, err := store.ClearCompleted()
	if err != nil || cleared != 2 {
		t.Fatalf("expected 2 cleared, got %d (err: %v)", cleared, err)
	}

	remaining, _ := store.List("all")
	if len(remaining) != 1 {
		t.Errorf("expected 1 remaining, got %d", len(remaining))
	}
}
