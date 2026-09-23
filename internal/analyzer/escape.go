package analyzer

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// escapeInfo holds the parsed escape analysis result for a single source position.
type escapeInfo struct {
	position string   // file:line:col
	escapes  bool     // does the allocation escape to heap?
	reasons  []string // compiler-reported reasons
	raw      string   // raw compiler output line
}

// escapeIndex maps normalised "file:line" keys to escape information.
type escapeIndex struct {
	entries map[string]*escapeInfo
}

// escapeLinePattern matches Go compiler escape analysis output lines.
//
// Examples:
//   ./main.go:7:6: p escapes to heap:
//   ./main.go:10:13: new(int) does not escape
//   ./main.go:12:2: moved to heap: x
var escapeLinePattern = regexp.MustCompile(
	`^(.*?):(\d+):(\d+):\s+(.+)$`,
)

// runEscapeAnalysis invokes the Go compiler's escape analysis on the packages
// at dir matching patterns, and returns a position-keyed index. If the Go
// compiler is unavailable or fails, the returned index is empty but non-nil.
// An empty index is safe: it simply means GOX has no compiler cross-check
// data, so the proof engine cannot use compiler evidence.
func runEscapeAnalysis(dir string, patterns []string, env []string, buildFlags []string) *escapeIndex {
	idx := &escapeIndex{entries: make(map[string]*escapeInfo)}
	if dir == "" {
		dir = "."
	}

	args := []string{"build", "-o", os.DevNull, "-gcflags=-m -m"}
	args = append(args, buildFlags...)
	args = append(args, patterns...)

	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}

	// The escape analysis output is written to stderr.
	output, _ := cmd.CombinedOutput()
	parseEscapeOutput(string(output), dir, idx)
	return idx
}

// parseEscapeOutput extracts per-position escape facts from the raw compiler
// output. It handles both "escapes to heap" and "does not escape" lines, as
// well as the verbose "-m -m" rationale lines.
func parseEscapeOutput(output, dir string, idx *escapeIndex) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		matches := escapeLinePattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		file := matches[1]
		lineNo := matches[2]
		// colNo := matches[3] — we key by file:line for robustness
		message := matches[4]

		// Normalise relative paths to absolute when possible.
		if !filepath.IsAbs(file) && dir != "" && dir != "." {
			if abs, err := filepath.Abs(filepath.Join(dir, file)); err == nil {
				file = abs
			}
		}

		key := fmt.Sprintf("%s:%s", file, lineNo)
		entry, exists := idx.entries[key]
		if !exists {
			entry = &escapeInfo{position: key}
			idx.entries[key] = entry
		}

		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "escapes to heap"):
			entry.escapes = true
			entry.reasons = append(entry.reasons, message)
		case strings.Contains(lower, "moved to heap"):
			entry.escapes = true
			entry.reasons = append(entry.reasons, message)
		case strings.Contains(lower, "does not escape"):
			// Explicit non-escape; keep escapes=false.
			entry.reasons = append(entry.reasons, message)
		case strings.Contains(lower, "leaking param"):
			entry.escapes = true
			entry.reasons = append(entry.reasons, message)
		default:
			// Informational lines (e.g. "flow:") — record but don't change escape status.
			entry.reasons = append(entry.reasons, message)
		}
		if entry.raw == "" {
			entry.raw = line
		}
	}
}

// lookup returns the escape information for a given source position.
// The position should be in "file:line:col" or "file:line" format.
func (idx *escapeIndex) lookup(position string) *escapeInfo {
	if idx == nil || position == "" {
		return nil
	}
	// Try exact match first.
	if entry := idx.entries[position]; entry != nil {
		return entry
	}
	// Try file:line only (strip column).
	parts := strings.SplitN(position, ":", 3)
	if len(parts) >= 2 {
		key := parts[0] + ":" + parts[1]
		if entry := idx.entries[key]; entry != nil {
			return entry
		}
		// Also match by basename:line (handles /var vs /private/var or rel vs abs)
		baseKey := filepath.Base(parts[0]) + ":" + parts[1]
		for k, v := range idx.entries {
			kParts := strings.SplitN(k, ":", 2)
			if len(kParts) >= 2 {
				if filepath.Base(kParts[0])+":"+kParts[1] == baseKey {
					return v
				}
			}
		}
	}
	return nil
}

// toEscapeResult converts an escapeInfo to the exported EscapeResult type.
func (info *escapeInfo) toEscapeResult() *EscapeResult {
	if info == nil {
		return nil
	}
	return &EscapeResult{
		Escapes:        info.escapes,
		EscapeReasons:  append([]string(nil), info.reasons...),
		GoCompilerSays: info.raw,
	}
}
