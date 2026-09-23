package domain

// Product represents an item in the store catalog.
type Product struct {
	ID          int     `json:"id"`
	SKU         string  `json:"sku"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
}
