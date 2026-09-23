package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
)

// ComputePackageHash computes a deterministic SHA-256 fingerprint for a package source tree,
// its dependencies, target architecture, and compilation flags.
func ComputePackageHash(dir string, patterns []string, env []string, flags []string) (string, error) {
	h := sha256.New()

	// 1. Hash patterns, env, flags
	fmt.Fprintf(h, "version:%s\n", "0.9.5")
	for _, p := range patterns {
		fmt.Fprintf(h, "pattern:%s\n", p)
	}

	sortedEnv := slices.Clone(env)
	slices.Sort(sortedEnv)
	for _, e := range sortedEnv {
		// Include relevant environment variables
		if strings.HasPrefix(e, "GOOS=") || strings.HasPrefix(e, "GOARCH=") || strings.HasPrefix(e, "CGO_ENABLED=") {
			fmt.Fprintf(h, "env:%s\n", e)
		}
	}

	sortedFlags := slices.Clone(flags)
	slices.Sort(sortedFlags)
	for _, f := range sortedFlags {
		fmt.Fprintf(h, "flag:%s\n", f)
	}

	// 2. Hash source files deterministically
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() {
			if name == ".git" || name == ".gox-cache" || name == "bin" || name == "vendor" || name == "goxrt" {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum" {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk package dir %s: %w", dir, err)
	}

	slices.Sort(files)

	for _, rel := range files {
		fullPath := filepath.Join(dir, rel)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return "", fmt.Errorf("read file %s: %w", fullPath, err)
		}
		fmt.Fprintf(h, "file:%s:%d\n", filepath.ToSlash(rel), len(content))
		h.Write(content)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// LoadDecisionCache attempts to load cached analyzer results for the specified key.
func LoadDecisionCache(cacheDir, key string) (*analyzer.Result, bool, error) {
	if cacheDir == "" || key == "" {
		return nil, false, nil
	}
	cacheFile := filepath.Join(cacheDir, "decisions", key+".json")
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read cache file: %w", err)
	}

	var res analyzer.Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, false, fmt.Errorf("unmarshal cached decisions: %w", err)
	}

	return &res, true, nil
}

// StoreDecisionCache persists analyzer results under the specified key atomically.
func StoreDecisionCache(cacheDir, key string, result *analyzer.Result) error {
	if cacheDir == "" || key == "" || result == nil {
		return nil
	}
	decisionsDir := filepath.Join(cacheDir, "decisions")
	if err := os.MkdirAll(decisionsDir, 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal decisions: %w", err)
	}

	tmpFile, err := os.CreateTemp(decisionsDir, "cache-*")
	if err != nil {
		return fmt.Errorf("create temp cache file: %w", err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write cache data: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	finalPath := filepath.Join(decisionsDir, key+".json")
	if err := os.Rename(tmpName, finalPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic rename cache file: %w", err)
	}

	return nil
}

// CleanCache removes cached decisions in cacheDir.
func CleanCache(cacheDir string) error {
	if cacheDir == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(cacheDir, "decisions"))
}
