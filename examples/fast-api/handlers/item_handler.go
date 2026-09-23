package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"example.com/gox-fast-api/models"
	goxrt "github.com/goxlang/gox/pkg/goxrt"
)

// ItemHandler serves high-speed item REST endpoints.
type ItemHandler struct {
	Store *models.Store
}

// NewItemHandler creates a new handler.
func NewItemHandler(store *models.Store) *ItemHandler {
	return &ItemHandler{Store: store}
}

// HandleList returns a list of catalog items.
// When compiled or run with goxrt.WithRequestArena, slice memory is allocated
// in the request arena and bulk-reset with zero GC heap sweeps.
func (h *ItemHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 20
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	arena := goxrt.GetRequestArena(r.Context())
	if arena != nil {
		// Zero-GC Arena Allocation & Fast Buffer Serialization
		items := goxrt.AllocSlice[models.Item](arena, limit, limit)
		n := h.Store.CopyItems(items, limit)
		items = items[:n]

		w.Header().Set("Content-Type", "application/json")
		buf := goxrt.AllocSlice[byte](arena, 0, 4096)
		buf = append(buf, '[')
		for i, item := range items {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, `{"id":`...)
			buf = strconv.AppendInt(buf, int64(item.ID), 10)
			buf = append(buf, `,"sku":"`...)
			buf = append(buf, item.SKU...)
			buf = append(buf, `","name":"`...)
			buf = append(buf, item.Name...)
			buf = append(buf, `","price":`...)
			buf = strconv.AppendFloat(buf, item.Price, 'f', 2, 64)
			buf = append(buf, `,"inventory":`...)
			buf = strconv.AppendInt(buf, int64(item.Inventory), 10)
			buf = append(buf, '}')
		}
		buf = append(buf, ']', '\n')
		_, _ = w.Write(buf)
		return
	}

	// Standard Go: Heap Mallocs & Reflection Serialization
	items := make([]models.Item, limit)
	n := h.Store.CopyItems(items, limit)
	items = items[:n]

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// HandleGet retrieves a single item by ID.
func (h *ItemHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/api/items/")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "Invalid item ID", http.StatusBadRequest)
		return
	}

	item, ok := h.Store.Get(id)
	if !ok {
		http.Error(w, "Item not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
}

// HandleCreate adds a new item to the store.
func (h *ItemHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.Item
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid payload: %v", err), http.StatusBadRequest)
		return
	}

	created := h.Store.Create(req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}
