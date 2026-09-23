package models

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
)

// Todo represents a single task item.
type Todo struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Title     string    `gorm:"size:255;not null" json:"title"`
	Priority  Priority  `gorm:"size:20;default:'medium'" json:"priority"`
	Completed bool      `gorm:"default:false;index" json:"completed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TodoStore wraps GORM database access for todos.
type TodoStore struct {
	db *gorm.DB
}

// NewTodoStore initializes the SQLite database connection and auto-migrates schema.
func NewTodoStore(dbPath string) (*TodoStore, error) {
	if dbPath == "" {
		dbPath = "file::memory:?cache=shared"
	}

	config := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}

	db, err := gorm.Open(sqlite.Open(dbPath), config)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	if err := db.AutoMigrate(&Todo{}); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate todo schema: %w", err)
	}

	return &TodoStore{db: db}, nil
}

// Create inserts a new todo into the database.
func (s *TodoStore) Create(title string, priority Priority) (*Todo, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}

	if priority != PriorityLow && priority != PriorityHigh {
		priority = PriorityMedium
	}

	todo := &Todo{
		Title:     title,
		Priority:  priority,
		Completed: false,
	}

	if err := s.db.Create(todo).Error; err != nil {
		return nil, err
	}
	return todo, nil
}

// List returns todos matching the given filter ('all', 'active', 'completed').
func (s *TodoStore) List(filter string) ([]Todo, error) {
	var todos []Todo
	query := s.db.Order("completed ASC, created_at DESC")

	switch strings.ToLower(filter) {
	case "active":
		query = query.Where("completed = ?", false)
	case "completed":
		query = query.Where("completed = ?", true)
	}

	if err := query.Find(&todos).Error; err != nil {
		return nil, err
	}
	return todos, nil
}

// GetByID finds a single todo by primary key.
func (s *TodoStore) GetByID(id uint) (*Todo, error) {
	var todo Todo
	if err := s.db.First(&todo, id).Error; err != nil {
		return nil, err
	}
	return &todo, nil
}

// Update modifies title and priority for an existing todo.
func (s *TodoStore) Update(id uint, title string, priority Priority) (*Todo, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}

	todo, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	todo.Title = title
	if priority == PriorityLow || priority == PriorityMedium || priority == PriorityHigh {
		todo.Priority = priority
	}

	if err := s.db.Save(todo).Error; err != nil {
		return nil, err
	}
	return todo, nil
}

// Toggle flips the completed boolean status of a todo.
func (s *TodoStore) Toggle(id uint) (*Todo, error) {
	todo, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	todo.Completed = !todo.Completed
	if err := s.db.Save(todo).Error; err != nil {
		return nil, err
	}
	return todo, nil
}

// Delete removes a todo by ID.
func (s *TodoStore) Delete(id uint) error {
	result := s.db.Delete(&Todo{}, id)
	return result.Error
}

// ClearCompleted removes all completed tasks from the database.
func (s *TodoStore) ClearCompleted() (int64, error) {
	result := s.db.Where("completed = ?", true).Delete(&Todo{})
	return result.RowsAffected, result.Error
}

// Stats returns the counts of total, active, and completed todos.
func (s *TodoStore) Stats() (total, active, completed int64, err error) {
	if err = s.db.Model(&Todo{}).Count(&total).Error; err != nil {
		return
	}
	if err = s.db.Model(&Todo{}).Where("completed = ?", false).Count(&active).Error; err != nil {
		return
	}
	completed = total - active
	return
}
