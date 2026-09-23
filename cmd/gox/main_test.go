package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, stderr: %s", code, stderr.String())
	}
	if got := stdout.String(); got != "gox 1.0.0\n" {
		t.Errorf("version output = %q", got)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"compile"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr does not explain failure: %s", stderr.String())
	}
}

func TestRunRejectsUnknownGraphFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"analyze", "-graph-format", "graphml"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "graph-format must be dot or json") {
		t.Errorf("stderr does not explain failure: %s", stderr.String())
	}
}

func TestRunAnalyzeMemoryStrategy(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"analyze", "-memory-strategy", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Memory Strategy Summary") {
		t.Error("expected Memory Strategy Summary in text output")
	}
	if !strings.Contains(out, "Stack (proven)") {
		t.Error("expected Stack (proven) in text output")
	}
	if !strings.Contains(out, "Unique owned (proven)") {
		t.Error("expected Unique owned (proven) in text output")
	}
	if !strings.Contains(out, "ARC (proven)") {
		t.Error("expected ARC (proven) in text output")
	}
	if !strings.Contains(out, "Weak (proven)") {
		t.Error("expected Weak (proven) in text output")
	}
	if !strings.Contains(out, "Proven tracing-GC reduction") {
		t.Error("expected Proven tracing-GC reduction in text output")
	}
}

func TestRunAnalyzeMemoryStrategyJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"analyze", "-memory-strategy", "-format", "json", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"strategy_summary"`) {
		t.Error("expected strategy_summary in JSON output")
	}
	if !strings.Contains(out, `"decisions"`) {
		t.Error("expected decisions in JSON output")
	}
	if !strings.Contains(out, `"STACK"`) {
		t.Error("expected STACK strategy in JSON output")
	}
}

func TestRunAnalyzeExplainAlloc(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"analyze", "-explain-alloc", "alloc:fc93596a0dc55c2b", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() = %d, stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "strategy: STACK") {
		t.Errorf("expected strategy: STACK in explain output, got:\n%s", out)
	}
	if !strings.Contains(out, "confidence: PROVEN") {
		t.Errorf("expected confidence: PROVEN in explain output, got:\n%s", out)
	}
}

func TestRunAnalyzeExplainAllocNotFound(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"analyze", "-explain-alloc", "nonexistent_alloc_id", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("expected 'not found' in stderr, got: %s", stderr.String())
	}
}

func TestRunBuildBasic(t *testing.T) {
	tmpDir := t.TempDir()
	outBin := filepath.Join(tmpDir, "gox-basic")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "-o", outBin, "-v", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(build) = %d, stderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(outBin); os.IsNotExist(err) {
		t.Fatalf("expected binary at %s was not produced", outBin)
	}

	cmd := exec.Command(outBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execution of built binary failed: %v, output: %s", err, string(out))
	}
	expected := "hello Gopher GOX\n"
	if string(out) != expected {
		t.Errorf("output = %q, want %q", string(out), expected)
	}
}

func TestRunRunBasic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-dir", "../../examples/basic", "."}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(run) = %d, stderr: %s\nstdout: %s", code, stderr.String(), stdout.String())
	}
	expected := "hello Gopher GOX\n"
	if stdout.String() != expected {
		t.Errorf("stdout = %q, want %q", stdout.String(), expected)
	}
}

func TestRunBuildWithCacheAndFlags(t *testing.T) {
	tmpDir := t.TempDir()
	outBin := filepath.Join(tmpDir, "gox-cached")

	// First build - populates cache
	var stdout1, stderr1 bytes.Buffer
	code1 := run([]string{"build", "-o", outBin, "-v", "-dir", "../../examples/basic", "."}, &stdout1, &stderr1)
	if code1 != 0 {
		t.Fatalf("first build failed: %d, stderr: %s", code1, stderr1.String())
	}

	// Second build - should use cached decisions
	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"build", "-o", outBin, "-v", "-dir", "../../examples/basic", "."}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("second build failed: %d, stderr: %s", code2, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "using cached memory strategy decisions") {
		t.Errorf("expected cache hit in output, got:\n%s", stdout2.String())
	}

	// Third build with -no-cache - should bypass cache
	var stdout3, stderr3 bytes.Buffer
	code3 := run([]string{"build", "-no-cache", "-o", outBin, "-v", "-dir", "../../examples/basic", "."}, &stdout3, &stderr3)
	if code3 != 0 {
		t.Fatalf("third build failed: %d, stderr: %s", code3, stderr3.String())
	}
	if strings.Contains(stdout3.String(), "using cached memory strategy decisions") {
		t.Errorf("expected cache bypass with -no-cache, got:\n%s", stdout3.String())
	}
}

func TestRunDoctorTextAndJSON(t *testing.T) {
	// Text format
	var stdout1, stderr1 bytes.Buffer
	code1 := run([]string{"doctor", "-dir", "../../examples/basic", "."}, &stdout1, &stderr1)
	if code1 != 0 {
		t.Fatalf("run(doctor text) = %d, stderr: %s", code1, stderr1.String())
	}
	if !strings.Contains(stdout1.String(), "GOX Diagnostic Doctor") && len(stdout1.String()) == 0 {
		t.Errorf("unexpected doctor output: %s", stdout1.String())
	}

	// JSON format
	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"doctor", "-format", "json", "-dir", "../../examples/basic", "."}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("run(doctor json) = %d, stderr: %s", code2, stderr2.String())
	}

	// File output
	tmpFile := filepath.Join(t.TempDir(), "doctor_report.txt")
	var stdout3, stderr3 bytes.Buffer
	code3 := run([]string{"doctor", "-output", tmpFile, "-dir", "../../examples/basic", "."}, &stdout3, &stderr3)
	if code3 != 0 {
		t.Fatalf("run(doctor output) = %d, stderr: %s", code3, stderr3.String())
	}
	if _, err := os.Stat(tmpFile); err != nil {
		t.Errorf("expected doctor report file %s to exist", tmpFile)
	}
}

func TestRunHelpAndUsage(t *testing.T) {
	// No args -> usage, exit code 2
	var stdout1, stderr1 bytes.Buffer
	if code := run([]string{}, &stdout1, &stderr1); code != 2 {
		t.Errorf("run([]) = %d, want 2", code)
	}

	// help command -> usage, exit code 0
	var stdout2, stderr2 bytes.Buffer
	if code := run([]string{"help"}, &stdout2, &stderr2); code != 0 {
		t.Errorf("run(help) = %d, want 0", code)
	}
	if !strings.Contains(stdout2.String(), "usage:") {
		t.Errorf("expected usage info in help stdout")
	}

	// -h flag
	var stdout3, stderr3 bytes.Buffer
	if code := run([]string{"-h"}, &stdout3, &stderr3); code != 0 {
		t.Errorf("run(-h) = %d, want 0", code)
	}
}

func TestRunAnalyzeFlags(t *testing.T) {
	// -detail, -explain, -trace
	var stdout1, stderr1 bytes.Buffer
	code1 := run([]string{"analyze", "-detail", "-explain", "-trace", "-dir", "../../examples/basic", "."}, &stdout1, &stderr1)
	if code1 != 0 {
		t.Fatalf("run(analyze detail trace) = %d, stderr: %s", code1, stderr1.String())
	}

	// -output file
	tmpOut := filepath.Join(t.TempDir(), "report.txt")
	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"analyze", "-output", tmpOut, "-dir", "../../examples/basic", "."}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("run(analyze -output) = %d, stderr: %s", code2, stderr2.String())
	}
	if _, err := os.Stat(tmpOut); err != nil {
		t.Errorf("expected report file to exist")
	}

	// -graph file
	tmpGraph := filepath.Join(t.TempDir(), "graph.dot")
	var stdout3, stderr3 bytes.Buffer
	code3 := run([]string{"analyze", "-graph", tmpGraph, "-graph-format", "dot", "-dir", "../../examples/basic", "."}, &stdout3, &stderr3)
	if code3 != 0 {
		t.Fatalf("run(analyze -graph) = %d, stderr: %s", code3, stderr3.String())
	}
	if _, err := os.Stat(tmpGraph); err != nil {
		t.Errorf("expected graph file to exist")
	}
}

