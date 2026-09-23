package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

// Queries wraps typed prepared statements for SQLite without reflection.
type Queries struct {
	db                 *sql.DB
	stmtCreate         *sql.Stmt
	stmtListAll        *sql.Stmt
	stmtListActive     *sql.Stmt
	stmtListCompleted  *sql.Stmt
	stmtGetByID        *sql.Stmt
	stmtToggle         *sql.Stmt
	stmtUpdate         *sql.Stmt
	stmtDelete         *sql.Stmt
	stmtClearCompleted *sql.Stmt
	stmtStats          *sql.Stmt
}

// NewQueries initializes schema and prepares all SQL statements.
func NewQueries(db *sql.DB) (*Queries, error) {
	schema := `
	CREATE TABLE IF NOT EXISTS todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		priority TEXT NOT NULL DEFAULT 'medium',
		completed INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_todos_completed ON todos(completed);
	CREATE INDEX IF NOT EXISTS idx_todos_created_at ON todos(created_at DESC);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to apply schema: %w", err)
	}

	q := &Queries{db: db}
	var err error

	q.stmtCreate, err = db.Prepare(`INSERT INTO todos (title, priority, completed, created_at, updated_at) VALUES (?, ?, 0, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtCreate: %w", err)
	}

	q.stmtListAll, err = db.Prepare(`SELECT id, title, priority, completed, created_at, updated_at FROM todos ORDER BY completed ASC, created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtListAll: %w", err)
	}

	q.stmtListActive, err = db.Prepare(`SELECT id, title, priority, completed, created_at, updated_at FROM todos WHERE completed = 0 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtListActive: %w", err)
	}

	q.stmtListCompleted, err = db.Prepare(`SELECT id, title, priority, completed, created_at, updated_at FROM todos WHERE completed = 1 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtListCompleted: %w", err)
	}

	q.stmtGetByID, err = db.Prepare(`SELECT id, title, priority, completed, created_at, updated_at FROM todos WHERE id = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtGetByID: %w", err)
	}

	q.stmtToggle, err = db.Prepare(`UPDATE todos SET completed = CASE WHEN completed = 1 THEN 0 ELSE 1 END, updated_at = ? WHERE id = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtToggle: %w", err)
	}

	q.stmtUpdate, err = db.Prepare(`UPDATE todos SET title = ?, priority = ?, updated_at = ? WHERE id = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtUpdate: %w", err)
	}

	q.stmtDelete, err = db.Prepare(`DELETE FROM todos WHERE id = ?`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtDelete: %w", err)
	}

	q.stmtClearCompleted, err = db.Prepare(`DELETE FROM todos WHERE completed = 1`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtClearCompleted: %w", err)
	}

	q.stmtStats, err = db.Prepare(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN completed = 0 THEN 1 ELSE 0 END), 0) FROM todos`)
	if err != nil {
		return nil, fmt.Errorf("prepare stmtStats: %w", err)
	}

	return q, nil
}

// Close closes all prepared statements.
func (q *Queries) Close() error {
	_ = q.stmtCreate.Close()
	_ = q.stmtListAll.Close()
	_ = q.stmtListActive.Close()
	_ = q.stmtListCompleted.Close()
	_ = q.stmtGetByID.Close()
	_ = q.stmtToggle.Close()
	_ = q.stmtUpdate.Close()
	_ = q.stmtDelete.Close()
	_ = q.stmtClearCompleted.Close()
	return q.stmtStats.Close()
}

// Create inserts a new task.
func (q *Queries) Create(ctx context.Context, title string, priority Priority) (Todo, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.stmtCreate.ExecContext(ctx, title, string(priority), now, now)
	if err != nil {
		return Todo{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Todo{}, err
	}
	t, _ := time.Parse(time.RFC3339, now)
	return Todo{
		ID:        uint(id),
		Title:     title,
		Priority:  priority,
		Completed: false,
		CreatedAt: t,
		UpdatedAt: t,
	}, nil
}

// List executes a query and allocates the result slice on the standard Go heap.
func (q *Queries) List(ctx context.Context, filter string) ([]Todo, error) {
	stmt := q.selectListStmt(filter)
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	todos := make([]Todo, 0, 32)
	for rows.Next() {
		var t Todo
		var compInt int
		var pStr, cStr, uStr string
		if err := rows.Scan(&t.ID, &t.Title, &pStr, &compInt, &cStr, &uStr); err != nil {
			return nil, err
		}
		t.Priority = Priority(pStr)
		t.Completed = compInt == 1
		t.CreatedAt, _ = time.Parse(time.RFC3339, cStr)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, uStr)
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// ListArena executes a query and allocates the result slice from the given GOX Arena.
// It bypasses the Go GC heap completely for slice allocation.
func (q *Queries) ListArena(ctx context.Context, a *goxrt.Arena, filter string) ([]Todo, error) {
	stmt := q.selectListStmt(filter)
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var todos []Todo
	if a != nil {
		todos = goxrt.AllocSlice[Todo](a, 0, 32)
	} else {
		todos = make([]Todo, 0, 32)
	}

	for rows.Next() {
		var t Todo
		var compInt int
		var pStr, cStr, uStr string
		if err := rows.Scan(&t.ID, &t.Title, &pStr, &compInt, &cStr, &uStr); err != nil {
			return nil, err
		}
		t.Priority = Priority(pStr)
		t.Completed = compInt == 1
		t.CreatedAt, _ = time.Parse(time.RFC3339, cStr)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, uStr)
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// GetByID finds a single todo by ID.
func (q *Queries) GetByID(ctx context.Context, id uint) (Todo, error) {
	var t Todo
	var compInt int
	var pStr, cStr, uStr string
	err := q.stmtGetByID.QueryRowContext(ctx, id).Scan(&t.ID, &t.Title, &pStr, &compInt, &cStr, &uStr)
	if err != nil {
		return Todo{}, err
	}
	t.Priority = Priority(pStr)
	t.Completed = compInt == 1
	t.CreatedAt, _ = time.Parse(time.RFC3339, cStr)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, uStr)
	return t, nil
}

// Toggle flips the completed state.
func (q *Queries) Toggle(ctx context.Context, id uint) (Todo, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.stmtToggle.ExecContext(ctx, now, id)
	if err != nil {
		return Todo{}, err
	}
	aff, err := res.RowsAffected()
	if err != nil || aff == 0 {
		return Todo{}, sql.ErrNoRows
	}
	return q.GetByID(ctx, id)
}

// Update modifies title and priority.
func (q *Queries) Update(ctx context.Context, id uint, title string, priority Priority) (Todo, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.stmtUpdate.ExecContext(ctx, title, string(priority), now, id)
	if err != nil {
		return Todo{}, err
	}
	aff, err := res.RowsAffected()
	if err != nil || aff == 0 {
		return Todo{}, sql.ErrNoRows
	}
	return q.GetByID(ctx, id)
}

// Delete removes a todo by ID.
func (q *Queries) Delete(ctx context.Context, id uint) error {
	res, err := q.stmtDelete.ExecContext(ctx, id)
	if err != nil {
		return err
	}
	aff, err := res.RowsAffected()
	if err != nil || aff == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClearCompleted removes all completed tasks.
func (q *Queries) ClearCompleted(ctx context.Context) (int64, error) {
	res, err := q.stmtClearCompleted.ExecContext(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Stats returns dashboard metrics in a single query.
func (q *Queries) Stats(ctx context.Context) (StatsResult, error) {
	var s StatsResult
	err := q.stmtStats.QueryRowContext(ctx).Scan(&s.Total, &s.Active)
	if err != nil {
		return StatsResult{}, err
	}
	s.Completed = s.Total - s.Active
	return s, nil
}

func (q *Queries) selectListStmt(filter string) *sql.Stmt {
	switch strings.ToLower(filter) {
	case "active":
		return q.stmtListActive
	case "completed":
		return q.stmtListCompleted
	default:
		return q.stmtListAll
	}
}
