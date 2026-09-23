package db

import (
	"time"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
)

// Todo represents a single task item.
type Todo struct {
	ID        uint      `json:"id"`
	Title     string    `json:"title"`
	Priority  Priority  `json:"priority"`
	Completed bool      `json:"completed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StatsResult holds counts for the dashboard.
type StatsResult struct {
	Total     int64 `json:"total"`
	Active    int64 `json:"active"`
	Completed int64 `json:"completed"`
}
