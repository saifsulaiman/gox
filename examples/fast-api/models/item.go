package models

import (
	"fmt"
	"sync"
)

// Item represents a high-throughput product catalog entity.
type Item struct {
	ID        int     `json:"id"`
	SKU       string  `json:"sku"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Inventory int     `json:"inventory"`
}

// Store provides high-speed concurrent typed storage with zero reflection.
type Store struct {
	mu    sync.RWMutex
	items map[int]Item
	seq   int
}

// NewStore initializes a new typed in-memory store.
func NewStore() *Store {
	return &Store{
		items: make(map[int]Item),
		seq:   1,
	}
}

// Seed populates the store with n sample items.
func (s *Store) Seed(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := 1; i <= n; i++ {
		s.items[i] = Item{
			ID:        i,
			SKU:       fmt.Sprintf("SKU-%05d", i),
			Name:      fmt.Sprintf("Product Item %d", i),
			Price:     19.99 + float64(i%100),
			Inventory: 50 + (i % 500),
		}
	}
	s.seq = n + 1
}

// Get retrieves an item by ID.
func (s *Store) Get(id int) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	return item, ok
}

// Create inserts a new item into the store.
func (s *Store) Create(item Item) Item {
	s.mu.Lock()
	defer s.mu.Unlock()

	item.ID = s.seq
	s.seq++
	s.items[item.ID] = item
	return item
}

// Count returns the number of items in the store.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// CopyItems fills dest with up to limit items.
func (s *Store) CopyItems(dest []Item, limit int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n := 0
	for _, it := range s.items {
		if n >= limit || n >= len(dest) {
			break
		}
		dest[n] = it
		n++
	}
	return n
}
