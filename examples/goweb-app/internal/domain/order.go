package domain

import "time"

// Order represents a completed purchase transaction.
type Order struct {
	ID        string    `json:"id"`
	ProductID int       `json:"product_id"`
	Quantity  int       `json:"quantity"`
	Total     float64   `json:"total"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateOrderRequest represents the incoming payload for placing an order.
type CreateOrderRequest struct {
	ProductID int `json:"product_id"`
	Quantity  int `json:"quantity"`
}
