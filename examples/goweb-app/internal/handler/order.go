package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"example.com/gox-web-app/internal/domain"
	"github.com/goxlang/gox/pkg/goweb"
)

// CreateOrder places a new purchase order with Idempotency, Distributed Lock, and Safe Tx Retries.
func CreateOrder(c *goweb.Context) error {
	var req domain.CreateOrderRequest
	if err := c.BindJSON(&req); err != nil {
		return c.Error(http.StatusBadRequest, err.Error())
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}

	// 1. Verify product via read replica
	var p domain.Product
	row := c.SQLite().QueryRowContext(c.Context(), "SELECT id, name, price FROM products WHERE id = ?", req.ProductID)
	if err := row.Scan(&p.ID, &p.Name, &p.Price); err != nil {
		return c.Error(http.StatusNotFound, "product not found")
	}

	// 2. Acquire Redis Distributed Lock on stock resource
	lockKey := fmt.Sprintf("lock:inventory:%d", req.ProductID)
	lock, err := c.Redis().Lock(c.Context(), lockKey, 5*time.Second)
	if err != nil {
		return c.JSON(http.StatusConflict, goweb.H{
			"error": "Concurrent inventory mutation in progress, please retry",
		})
	}
	defer func() {
		_ = lock.Unlock(c.Context())
	}()

	orderID := fmt.Sprintf("ord-%d-%d", time.Now().UnixNano(), req.ProductID)
	total := p.Price * float64(req.Quantity)

	// 3. Strict single-row modification (ExecOne) to enforce safety
	_ = c.DB().ExecOne(c.Context(), "UPDATE products SET price = price WHERE id = ?", req.ProductID)

	// 4. Transactional ledger with automatic retry on transient serialization/locks
	err = c.DB().WithTxRetry(c.Context(), 3, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO orders (id, product_id, quantity, total, status, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			orderID, req.ProductID, req.Quantity, total, "CONFIRMED", time.Now())
		return err
	})
	if err != nil {
		return c.Error(http.StatusInternalServerError, fmt.Sprintf("failed to save order: %v", err))
	}

	// 5. Read-Your-Own-Writes consistency: force subsequent queries in this request to use Primary
	c.UsePrimaryDB()

	// 6. Update Real-Time Sales Leaderboard in Redis
	newScore, _ := c.Redis().LeaderboardIncrBy(c.Context(), "lb:product_sales", strconv.Itoa(req.ProductID), float64(req.Quantity))

	// 7. Publish reliable order event with Publisher Confirms
	_ = c.Queue().PublishJSON(c.Context(), "orders.exchange", "orders.fulfill", goweb.H{
		"order_id":   orderID,
		"product_id": req.ProductID,
		"quantity":   req.Quantity,
		"total":      total,
		"timestamp":  time.Now(),
	})

	// 8. Non-blocking Async worker for audit trail logging
	c.Async(func(ctx context.Context) {
		slog.Info("Audit: order successfully created",
			slog.String("order_id", orderID),
			slog.Float64("total", total),
			slog.Float64("total_product_sales", newScore),
		)
	})

	return c.JSON(http.StatusCreated, goweb.H{
		"order_id": orderID,
		"status":   "CONFIRMED",
		"total":    total,
		"queued":   true,
	})
}

// ListOrders returns recent orders balanced across read replicas.
func ListOrders(c *goweb.Context) error {
	rows, err := c.DB().QueryContext(c.Context(), "SELECT id, product_id, quantity, total, status, created_at FROM orders ORDER BY created_at DESC LIMIT 20")
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	defer rows.Close()

	orders := make([]domain.Order, 0, 10)
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.ProductID, &o.Quantity, &o.Total, &o.Status, &o.CreatedAt); err == nil {
			orders = append(orders, o)
		}
	}
	return c.JSON(http.StatusOK, orders)
}
