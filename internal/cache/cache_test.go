package cache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/goxlang/gox/internal/analyzer"
)

func TestComputePackageHashDeterministic(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(file1, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	h1, err := ComputePackageHash(tmpDir, []string{"."}, []string{"GOOS=darwin", "GOARCH=arm64"}, []string{"-tags=netgo"})
	if err != nil {
		t.Fatalf("h1 failed: %v", err)
	}

	h2, err := ComputePackageHash(tmpDir, []string{"."}, []string{"GOOS=darwin", "GOARCH=arm64"}, []string{"-tags=netgo"})
	if err != nil {
		t.Fatalf("h2 failed: %v", err)
	}

	if h1 != h2 {
		t.Errorf("expected deterministic hashes, got %q vs %q", h1, h2)
	}
}

func TestComputePackageHashInvalidatesOnContentChange(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(file1, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	h1, err := ComputePackageHash(tmpDir, []string{"."}, nil, nil)
	if err != nil {
		t.Fatalf("h1 failed: %v", err)
	}

	// Modify content
	if err := os.WriteFile(file1, []byte("package main\n\nfunc main() { println(1) }\n"), 0644); err != nil {
		t.Fatalf("modify file1: %v", err)
	}

	h2, err := ComputePackageHash(tmpDir, []string{"."}, nil, nil)
	if err != nil {
		t.Fatalf("h2 failed: %v", err)
	}

	if h1 == h2 {
		t.Error("expected different hash after content modification")
	}
}

func TestComputePackageHashInvalidatesOnEnvChange(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(file1, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	hDarwin, err := ComputePackageHash(tmpDir, []string{"."}, []string{"GOOS=darwin", "GOARCH=arm64"}, nil)
	if err != nil {
		t.Fatalf("hDarwin failed: %v", err)
	}

	hLinux, err := ComputePackageHash(tmpDir, []string{"."}, []string{"GOOS=linux", "GOARCH=amd64"}, nil)
	if err != nil {
		t.Fatalf("hLinux failed: %v", err)
	}

	if hDarwin == hLinux {
		t.Error("expected different hash for different GOOS/GOARCH")
	}
}

func TestStoreAndLoadDecisionCache(t *testing.T) {
	tmpCacheDir := t.TempDir()
	key := "testkey123"

	// Initially missing
	res, found, err := LoadDecisionCache(tmpCacheDir, key)
	if err != nil {
		t.Fatalf("LoadDecisionCache error: %v", err)
	}
	if found || res != nil {
		t.Fatal("expected cache miss for non-existent key")
	}

	// Store
	sampleRes := &analyzer.Result{
		Version: "0.9.5",
		Decisions: []analyzer.AllocationDecision{
			{
				SourcePosition: "main.go:10:5",
				Strategy:       analyzer.StrategyImmortal,
				Confidence:     analyzer.ConfidenceProven,
			},
		},
	}

	if err := StoreDecisionCache(tmpCacheDir, key, sampleRes); err != nil {
		t.Fatalf("StoreDecisionCache error: %v", err)
	}

	// Load
	loaded, found, err := LoadDecisionCache(tmpCacheDir, key)
	if err != nil {
		t.Fatalf("LoadDecisionCache error: %v", err)
	}
	if !found || loaded == nil {
		t.Fatal("expected cache hit after store")
	}

	if loaded.Version != "0.9.5" {
		t.Errorf("version = %q, want 0.9.5", loaded.Version)
	}
	if len(loaded.Decisions) != 1 || loaded.Decisions[0].Strategy != analyzer.StrategyImmortal {
		t.Errorf("decisions mismatch: %+v", loaded.Decisions)
	}
}

func TestConcurrentCacheAccess(t *testing.T) {
	tmpCacheDir := t.TempDir()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := "shared_key"
			res := &analyzer.Result{
				Version: "0.9.5",
			}
			_ = StoreDecisionCache(tmpCacheDir, key, res)
			_, _, _ = LoadDecisionCache(tmpCacheDir, key)
		}(i)
	}

	wg.Wait()
}
