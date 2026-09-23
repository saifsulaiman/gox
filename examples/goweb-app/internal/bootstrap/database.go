package bootstrap

import (
	"context"
	"strconv"

	"example.com/gox-web-app/internal/domain"
	"github.com/goxlang/gox/pkg/goweb"
)

// InitDatabases configures schemas and seeds initial catalog & filter state.
func InitDatabases(app *goweb.Engine) {
	// 1. Initialize SQLite Catalog Schema & Seed Data
	sqlite := app.SQLite()
	if sqlite != nil {
		_, _ = sqlite.Exec(`
			CREATE TABLE IF NOT EXISTS products (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				sku TEXT UNIQUE,
				name TEXT,
				category TEXT,
				price REAL,
				description TEXT
			);
		`)

		// Seed initial products if table empty
		var count int
		_ = sqlite.QueryRow("SELECT COUNT(*) FROM products").Scan(&count)
		if count == 0 {
			seedProducts := []domain.Product{
				{SKU: "NX-KB01", Name: "Apex Mechanical Keyboard", Category: "Electronics", Price: 149.99, Description: "Ultra-fast optical switches with per-key RGB"},
				{SKU: "NX-MS02", Name: "Pro Wireless Gaming Mouse", Category: "Electronics", Price: 79.99, Description: "Ergonomic 26k DPI sensor with 80h battery"},
				{SKU: "NX-HD03", Name: "Studio Hi-Fi Headphones", Category: "Audio", Price: 299.99, Description: "Planar magnetic audiophile drivers with active noise cancelling"},
				{SKU: "NX-MN04", Name: "UltraWide Curved Monitor 34-inch", Category: "Displays", Price: 599.99, Description: "165Hz refresh rate OLED display with HDR1000"},
				{SKU: "NX-CH05", Name: "Ergonomic Mesh Chair", Category: "Office", Price: 349.99, Description: "Dynamic lumbar support with breathable mesh"},
			}
			for _, p := range seedProducts {
				res, _ := sqlite.Exec("INSERT INTO products (sku, name, category, price, description) VALUES (?, ?, ?, ?, ?)",
					p.SKU, p.Name, p.Category, p.Price, p.Description)
				if res != nil {
					id, _ := res.LastInsertId()
					p.ID = int(id)
					idStr := strconv.Itoa(p.ID)

					// Seed Bloom filter & Cuckoo filter to prevent cache penetration
					app.Bloom().Add("product:" + idStr)
					_ = app.Redis().CuckooAdd(context.Background(), "cuckoo:products", idStr)
					_ = app.Redis().LeaderboardAdd(context.Background(), "lb:product_sales", idStr, float64(10*p.ID))

					// Populate initial search index in Elasticsearch
					_ = app.Search().Index(context.Background(), "products", idStr, p)
				}
			}
			// Pre-seed some trending search keywords into Top-K
			_ = app.Redis().TopKReserve(context.Background(), "trending:searches", 10)
			_, _ = app.Redis().TopKAdd(context.Background(), "trending:searches", "keyboard", "mouse", "monitor", "keyboard", "headphones")
		} else {
			// Populate bloom & cuckoo filter from existing records
			rows, err := sqlite.Query("SELECT id FROM products")
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var id int
					if err := rows.Scan(&id); err == nil {
						idStr := strconv.Itoa(id)
						app.Bloom().Add("product:" + idStr)
						_ = app.Redis().CuckooAdd(context.Background(), "cuckoo:products", idStr)
					}
				}
			}
		}
	}

	// 2. Initialize Orders Ledger (PostgreSQL or fallback to Primary DB)
	db := app.DB()
	if db != nil {
		_, _ = db.Exec(`
			CREATE TABLE IF NOT EXISTS orders (
				id TEXT PRIMARY KEY,
				product_id INTEGER,
				quantity INTEGER,
				total REAL,
				status TEXT,
				created_at DATETIME
			);
		`)
	}
}
