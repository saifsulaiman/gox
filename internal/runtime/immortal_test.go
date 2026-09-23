package runtime

import (
	"sync"
	"testing"
)

type GlobalConfig struct {
	MaxConns int
	Host     string
	Port     int
	Enabled  bool
}

type LargeTable struct {
	Data [1024]int
}

func TestAllocImmortalBasic(t *testing.T) {
	ResetImmortalForTesting()

	cfg := AllocImmortal(GlobalConfig{
		MaxConns: 100,
		Host:     "localhost",
		Port:     8080,
		Enabled:  true,
	})

	if cfg == nil {
		t.Fatal("AllocImmortal returned nil")
	}
	if cfg.Host != "localhost" || cfg.Port != 8080 || cfg.MaxConns != 100 || !cfg.Enabled {
		t.Fatalf("AllocImmortal corrupted fields: %+v", *cfg)
	}

	stats := GetImmortalStats()
	if stats.AllocCount != 1 {
		t.Fatalf("expected AllocCount 1, got %d", stats.AllocCount)
	}
	if stats.BytesAllocated <= 0 {
		t.Fatalf("expected BytesAllocated > 0, got %d", stats.BytesAllocated)
	}
}

func TestAllocReadOnly(t *testing.T) {
	ResetImmortalForTesting()

	table := AllocReadOnly(LargeTable{})
	for i := 0; i < 1024; i++ {
		table.Data[i] = i * 2
	}

	for i := 0; i < 1024; i++ {
		if table.Data[i] != i*2 {
			t.Fatalf("data mismatch at index %d: expected %d, got %d", i, i*2, table.Data[i])
		}
	}

	stats := GetImmortalStats()
	if stats.AllocCount != 1 {
		t.Fatalf("expected AllocCount 1, got %d", stats.AllocCount)
	}
	if stats.ReadOnlyCount != 1 {
		t.Fatalf("expected ReadOnlyCount 1, got %d", stats.ReadOnlyCount)
	}
}

func TestAllocImmortalAlignmentAndMultiple(t *testing.T) {
	ResetImmortalForTesting()

	type Mixed struct {
		B byte
		I int64
		C byte
	}

	var ptrs []*Mixed
	for i := 0; i < 500; i++ {
		p := AllocImmortal(Mixed{
			B: byte(i),
			I: int64(i * 1000),
			C: byte(i + 1),
		})
		ptrs = append(ptrs, p)
	}

	for i, p := range ptrs {
		if p.B != byte(i) || p.I != int64(i*1000) || p.C != byte(i+1) {
			t.Fatalf("corruption in element %d: %+v", i, *p)
		}
	}
}

func TestAllocImmortalConcurrent(t *testing.T) {
	ResetImmortalForTesting()

	const numGoroutines = 20
	const allocsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	type Item struct {
		ID    int
		Value string
	}

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < allocsPerGoroutine; i++ {
				item := AllocImmortal(Item{
					ID:    gid*1000 + i,
					Value: "static-token",
				})
				if item.ID != gid*1000+i || item.Value != "static-token" {
					t.Errorf("concurrent alloc corrupted: %+v", *item)
				}
			}
		}(g)
	}

	wg.Wait()

	stats := GetImmortalStats()
	expectedAllocs := int64(numGoroutines * allocsPerGoroutine)
	if stats.AllocCount != expectedAllocs {
		t.Fatalf("expected AllocCount %d, got %d", expectedAllocs, stats.AllocCount)
	}
}
