package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/gox-web-app/internal/domain"
	"github.com/goxlang/gox/pkg/goweb"
)

// ListProducts lists catalog products with Singleflight coalescing and Redis caching.
func ListProducts(c *goweb.Context) error {
	val, err := c.Singleflight("products:list", func() (any, error) {
		cacheKey := "products:list"
		var cached []domain.Product
		if err := c.Redis().GetJSON(c.Context(), cacheKey, &cached); err == nil {
			return cached, nil
		}

		// Reads round-robin load-balanced across read replicas (slaves)
		rows, err := c.SQLite().QueryContext(c.Context(), "SELECT id, sku, name, category, price, description FROM products LIMIT 50")
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		products := make([]domain.Product, 0, 16)
		for rows.Next() {
			var p domain.Product
			if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Category, &p.Price, &p.Description); err == nil {
				products = append(products, p)
			}
		}

		_ = c.Redis().SetJSON(c.Context(), cacheKey, products, 30*time.Second)
		return products, nil
	})

	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, val)
}

// GetProduct retrieves a product by ID, defended by a Bloom filter.
func GetProduct(c *goweb.Context) error {
	id, err := c.ParamInt("id")
	if err != nil {
		return c.Error(http.StatusBadRequest, "invalid product id")
	}

	// 1. Bloom filter instant rejection (cache penetration defense)
	if !c.Bloom().Contains("product:" + strconv.Itoa(id)) {
		return c.Error(http.StatusNotFound, "product not found (rejected by bloom filter)")
	}

	// 2. Query Read Replica (slave)
	var p domain.Product
	row := c.SQLite().QueryRowContext(c.Context(), "SELECT id, sku, name, category, price, description FROM products WHERE id = ?", id)
	if err := row.Scan(&p.ID, &p.SKU, &p.Name, &p.Category, &p.Price, &p.Description); err != nil {
		return c.Error(http.StatusNotFound, "product not found")
	}
	return c.JSON(http.StatusOK, p)
}

// CreateProduct creates a new product in the catalog (Admin only).
func CreateProduct(c *goweb.Context) error {
	var p domain.Product
	if err := c.BindJSON(&p); err != nil {
		return c.Error(http.StatusBadRequest, err.Error())
	}

	// Writes strictly target the Primary/Master database
	res, err := c.SQLite().ExecContext(c.Context(),
		"INSERT INTO products (sku, name, category, price, description) VALUES (?, ?, ?, ?, ?)",
		p.SKU, p.Name, p.Category, p.Price, p.Description)
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	id, _ := res.LastInsertId()
	p.ID = int(id)
	idStr := strconv.Itoa(p.ID)

	// Update Bloom filter & mutable Cuckoo filter
	c.Bloom().Add("product:" + idStr)
	_ = c.Redis().CuckooAdd(c.Context(), "cuckoo:products", idStr)
	_ = c.Redis().LeaderboardAdd(c.Context(), "lb:product_sales", idStr, 0)

	// Index into Elasticsearch for instant searchability
	_ = c.Search().Index(c.Context(), "products", idStr, p)
	// Invalidate catalog cache in Redis
	_ = c.Redis().Del(c.Context(), "products:list")

	return c.JSON(http.StatusCreated, p)
}

// DeleteProduct deletes a product, enforcing single-row mutation and updating Cuckoo filter.
func DeleteProduct(c *goweb.Context) error {
	id, err := c.ParamInt("id")
	if err != nil {
		return c.Error(http.StatusBadRequest, "invalid product id")
	}
	idStr := strconv.Itoa(id)

	// 1. Cuckoo Filter fast-check
	contains, _ := c.Redis().CuckooContains(c.Context(), "cuckoo:products", idStr)
	if !contains {
		return c.Error(http.StatusNotFound, "product not found (rejected by cuckoo filter)")
	}

	// 2. Strict single-row modification (ExecOne) to enforce safety on Primary
	err = c.SQLite().ExecOne(c.Context(), "DELETE FROM products WHERE id = ?", id)
	if err != nil {
		return c.Error(http.StatusNotFound, "product could not be deleted or does not exist")
	}

	// 3. Remove from Cuckoo filter (Cuckoo supports deletions unlike standard Bloom filter)
	_, _ = c.Redis().CuckooDelete(c.Context(), "cuckoo:products", idStr)
	_ = c.Redis().LeaderboardRemove(c.Context(), "lb:product_sales", idStr)
	_ = c.Redis().Del(c.Context(), "products:list")
	_ = c.Search().Delete(c.Context(), "products", idStr)

	return c.JSON(http.StatusOK, goweb.H{
		"message":    "product deleted successfully",
		"product_id": id,
	})
}

// SearchProducts performs full-text Elasticsearch search and records heavy-hitter queries in Top-K.
func SearchProducts(c *goweb.Context) error {
	q := c.Query("q")
	if q == "" {
		return c.Error(http.StatusBadRequest, "query parameter 'q' required")
	}

	// Track trending search terms using Top-K heavy hitters algorithm
	_, _ = c.Redis().TopKAdd(c.Context(), "trending:searches", strings.ToLower(strings.TrimSpace(q)))

	results, err := c.Search().Search(c.Context(), "products", q)
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, results)
}
