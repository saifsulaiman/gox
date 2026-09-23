package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goxlang/gox/pkg/goweb"
	"github.com/goxlang/gox/pkg/goxrt"
	_ "github.com/mattn/go-sqlite3"
)

type Item struct {
	ID    int     `json:"id"`
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func setupTestApp(disableArena bool) *goweb.Engine {
	app := goweb.New(goweb.Config{
		Port:         8095,
		DisableArena: disableArena,
		SQLite:       "file:bench.db?cache=shared&mode=memory",
		Redis:        "memory",
		RabbitMQ:     "memory",
		Elastic:      "",
	})

	// Prepopulate products
	db := app.SQLite()
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS products (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sku TEXT,
		name TEXT,
		category TEXT,
		price REAL,
		description TEXT
	);`)

	for i := 1; i <= 50; i++ {
		_, _ = db.Exec("INSERT INTO products (sku, name, category, price, description) VALUES (?, ?, ?, ?, ?)",
			fmt.Sprintf("SKU-%04d", i),
			fmt.Sprintf("Product %d", i),
			"Hardware",
			float64(i)*9.99,
			"High performance benchmark product item")
		_ = app.Search().Index(context.Background(), "products", fmt.Sprintf("%d", i), map[string]any{
			"name": fmt.Sprintf("Product %d", i),
		})
	}

	// Consume queue messages
	_ = app.Queue().ConsumeJSON("new", func(msg goweb.Message, payload map[string]any) error {
		return nil
	})

	// Catalog loaded in memory (reflecting cached catalog service)
	catalog := make([]Item, 0, 50)
	rows, _ := db.Query("SELECT id, sku, name, price FROM products LIMIT 20")
	if rows != nil {
		for rows.Next() {
			var it Item
			_ = rows.Scan(&it.ID, &it.SKU, &it.Name, &it.Price)
			catalog = append(catalog, it)
		}
		rows.Close()
	}

	app.GET("/api/products", func(c *goweb.Context) error {
		if arena := c.Arena(); arena != nil {
			// GOX Zero-GC: Arena slice allocation & zero-alloc response buffer
			items := goxrt.AllocSlice[Item](arena, len(catalog), len(catalog))
			copy(items, catalog)

			buf := goxrt.AllocSlice[byte](arena, 0, 2048)
			buf = append(buf, '[')
			for i, it := range items {
				if i > 0 {
					buf = append(buf, ',')
				}
				buf = append(buf, `{"id":`...)
				buf = strconv.AppendInt(buf, int64(it.ID), 10)
				buf = append(buf, `,"sku":"`...)
				buf = append(buf, it.SKU...)
				buf = append(buf, `","name":"`...)
				buf = append(buf, it.Name...)
				buf = append(buf, `","price":`...)
				buf = strconv.AppendFloat(buf, it.Price, 'f', 2, 64)
				buf = append(buf, '}')
			}
			buf = append(buf, ']', '\n')
			return c.JSONFast(http.StatusOK, buf)
		}

		// Standard Go: Heap slices & reflection json.Encoder
		items := make([]Item, len(catalog))
		copy(items, catalog)
		return c.JSON(http.StatusOK, items)
	})

	app.POST("/api/orders", func(c *goweb.Context) error {
		var req struct {
			ProductID int `json:"product_id"`
			Quantity  int `json:"quantity"`
		}
		if err := c.BindJSON(&req); err != nil {
			return c.Error(400, err.Error())
		}
		_ = c.Queue().PublishJSON(c.Context(), "orders", "new", req)

		if arena := c.Arena(); arena != nil {
			buf := goxrt.AllocSlice[byte](arena, 0, 64)
			buf = append(buf, `{"status":"queued","id":`...)
			buf = strconv.AppendInt(buf, int64(req.ProductID), 10)
			buf = append(buf, '}', '\n')
			return c.JSONFast(http.StatusCreated, buf)
		}

		return c.JSON(http.StatusCreated, goweb.H{"status": "queued", "id": req.ProductID})
	})

	return app
}

type BenchResult struct {
	Mode         string
	TotalReqs    int
	Duration     time.Duration
	ReqsPerSec   float64
	AvgLatencyUs float64
	TotalMallocs uint64
	TotalFreed   uint64
	GCCycles     uint32
	GCPauseMs    float64
	HeapAllocMB  float64
}

func runBenchmark(name string, disableArena bool, numReqs int, concurrency int) BenchResult {
	runtime.GC()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)

	app := setupTestApp(disableArena)

	var wg sync.WaitGroup
	reqsPerWorker := numReqs / concurrency
	start := time.Now()

	latencies := make([]time.Duration, numReqs)

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerLats := make([]time.Duration, reqsPerWorker)
			getReq := httptest.NewRequest(http.MethodGet, "/api/products", nil)
			postBody := `{"product_id":10,"quantity":2}`
			rec := httptest.NewRecorder()

			for i := 0; i < reqsPerWorker; i++ {
				t0 := time.Now()
				rec.Body.Reset()

				if i%2 == 0 {
					app.ServeHTTP(rec, getReq)
				} else {
					postReq := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(postBody))
					postReq.Header.Set("Content-Type", "application/json")
					app.ServeHTTP(rec, postReq)
				}
				workerLats[i] = time.Since(t0)
			}

			startOffset := workerID * reqsPerWorker
			copy(latencies[startOffset:startOffset+reqsPerWorker], workerLats)
		}(w)
	}

	wg.Wait()
	duration := time.Since(start)

	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)

	var sumLat time.Duration
	for i := 0; i < numReqs; i++ {
		sumLat += latencies[i]
	}
	avgLat := float64(sumLat.Microseconds()) / float64(numReqs)

	gcPauseMs := float64(memEnd.PauseTotalNs-memStart.PauseTotalNs) / 1e6

	return BenchResult{
		Mode:         name,
		TotalReqs:    numReqs,
		Duration:     duration,
		ReqsPerSec:   float64(numReqs) / duration.Seconds(),
		AvgLatencyUs: avgLat,
		TotalMallocs: memEnd.Mallocs - memStart.Mallocs,
		TotalFreed:   memEnd.Frees - memStart.Frees,
		GCCycles:     memEnd.NumGC - memStart.NumGC,
		GCPauseMs:    gcPauseMs,
		HeapAllocMB:  float64(memEnd.Alloc) / (1024 * 1024),
	}
}

func main() {
	count := flag.Int("n", 50000, "total benchmark requests")
	concurrency := flag.Int("c", 8, "concurrency level")
	flag.Parse()

	fmt.Println("================================================================================")
	fmt.Println("                      GOWEB PERFORMANCE BENCHMARK                               ")
	fmt.Println("             Standard Go Runtime vs GOX 7-Tier Memory Hierarchy                 ")
	fmt.Println("================================================================================")
	fmt.Printf("Workload: %d HTTP requests (%d concurrent workers)\n", *count, *concurrency)
	fmt.Println("Endpoints tested: GET /api/products (Catalog JSON) & POST /api/orders (Queue)")
	fmt.Println("--------------------------------------------------------------------------------")

	resStandard := runBenchmark("Standard Go Runtime (GC Heap)", true, *count, *concurrency)
	resGox := runBenchmark("GOX Memory Strategy (Request Arena)", false, *count, *concurrency)

	fmt.Println("\nRESULTS:")
	fmt.Printf("%-38s | %10s | %12s | %10s | %10s | %10s\n", "Execution Mode", "Throughput", "Avg Latency", "GC Cycles", "GC Pause", "Heap Alloc")
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("%-38s | %8.0f r/s | %10.1f µs | %10d | %8.2f ms | %8.2f MB\n",
		resStandard.Mode, resStandard.ReqsPerSec, resStandard.AvgLatencyUs, resStandard.GCCycles, resStandard.GCPauseMs, resStandard.HeapAllocMB)
	fmt.Printf("%-38s | %8.0f r/s | %10.1f µs | %10d | %8.2f ms | %8.2f MB\n",
		resGox.Mode, resGox.ReqsPerSec, resGox.AvgLatencyUs, resGox.GCCycles, resGox.GCPauseMs, resGox.HeapAllocMB)
	fmt.Println("--------------------------------------------------------------------------------")

	allocReduction := 100.0 * (1.0 - float64(resGox.TotalMallocs)/float64(resStandard.TotalMallocs))
	speedup := 100.0 * (resGox.ReqsPerSec - resStandard.ReqsPerSec) / resStandard.ReqsPerSec
	latReduction := 100.0 * (resStandard.AvgLatencyUs - resGox.AvgLatencyUs) / resStandard.AvgLatencyUs
	gcReduction := 100.0 * (1.0 - float64(resGox.GCCycles)/float64(resStandard.GCCycles))
	pauseReduction := 100.0 * (1.0 - resGox.GCPauseMs/resStandard.GCPauseMs)

	fmt.Printf("Memory Strategy Advantage:\n")
	fmt.Printf("  • Throughput Gain:   %+.1f%% speedup (%0.0f r/s vs %0.0f r/s)\n", speedup, resGox.ReqsPerSec, resStandard.ReqsPerSec)
	fmt.Printf("  • Latency Reduction: %+.1f%% faster (%.1f µs vs %.1f µs)\n", latReduction, resGox.AvgLatencyUs, resStandard.AvgLatencyUs)
	fmt.Printf("  • Heap Mallocs:      %d (Go GC) vs %d (GOX Arena)  [%+.1f%% reduction]\n",
		resStandard.TotalMallocs, resGox.TotalMallocs, allocReduction)
	fmt.Printf("  • GC Cycles:         %d (Go GC) vs %d (GOX Arena)  [%+.1f%% reduction]\n",
		resStandard.GCCycles, resGox.GCCycles, gcReduction)
	fmt.Printf("  • GC Pause Total:    %.2f ms (Go GC) vs %.2f ms (GOX Arena) [%+.1f%% reduction]\n",
		resStandard.GCPauseMs, resGox.GCPauseMs, pauseReduction)
	fmt.Printf("  • Unique Slab Reclaims: %d\n", goxrt.GetUniqueStats().FreeCount)
	fmt.Println("================================================================================")
}
