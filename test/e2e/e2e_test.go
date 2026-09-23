package e2e_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildGoxBinary compiles the gox CLI tool to a temporary binary.
func buildGoxBinary(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	goxBin := filepath.Join(tmpDir, "gox")

	cmd := exec.Command("go", "build", "-o", goxBin, "../../cmd/gox")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build gox binary: %v, output: %s", err, string(out))
	}
	return goxBin
}

func TestE2EBuildAndRunBasic(t *testing.T) {
	goxBin := buildGoxBinary(t)
	tmpDir := t.TempDir()
	appBin := filepath.Join(tmpDir, "app-basic")
	basicDir, err := filepath.Abs("../../examples/basic")
	if err != nil {
		t.Fatalf("resolve basic dir: %v", err)
	}

	// 1. Test 'gox build'
	buildCmd := exec.Command(goxBin, "build", "-o", appBin, "-v", "-dir", basicDir, ".")
	var buildStdout, buildStderr bytes.Buffer
	buildCmd.Stdout = &buildStdout
	buildCmd.Stderr = &buildStderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("gox build failed: %v\nstderr: %s\nstdout: %s", err, buildStderr.String(), buildStdout.String())
	}

	if _, err := os.Stat(appBin); os.IsNotExist(err) {
		t.Fatalf("expected binary %s was not created", appBin)
	}

	// Run compiled binary
	runAppCmd := exec.Command(appBin)
	appOut, err := runAppCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running compiled binary failed: %v, output: %s", err, string(appOut))
	}

	expectedOutput := "hello Gopher GOX\n"
	if string(appOut) != expectedOutput {
		t.Errorf("app output = %q, want %q", string(appOut), expectedOutput)
	}

	// 2. Test 'gox run'
	runCmd := exec.Command(goxBin, "run", "-dir", basicDir, ".")
	var runStdout, runStderr bytes.Buffer
	runCmd.Stdout = &runStdout
	runCmd.Stderr = &runStderr
	if err := runCmd.Run(); err != nil {
		t.Fatalf("gox run failed: %v\nstderr: %s\nstdout: %s", err, runStderr.String(), runStdout.String())
	}

	if runStdout.String() != expectedOutput {
		t.Errorf("gox run stdout = %q, want %q", runStdout.String(), expectedOutput)
	}
}

func TestE2EKeepTransformed(t *testing.T) {
	goxBin := buildGoxBinary(t)
	tmpDir := t.TempDir()

	// Create a standalone Go package in tmpDir
	src := `package main

import "fmt"

type Server struct {
	Addr string
}

var DefaultServer *Server

func init() {
	DefaultServer = &Server{Addr: ":8080"}
}

func main() {
	fmt.Println("Server running at", DefaultServer.Addr)
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	modContent := "module example.com/test-server\n\ngo 1.24.0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	outBin := filepath.Join(tmpDir, "server")
	buildCmd := exec.Command(goxBin, "build", "-keep-transformed", "-o", outBin, "-dir", tmpDir, ".")
	var buildStdout, buildStderr bytes.Buffer
	buildCmd.Stdout = &buildStdout
	buildCmd.Stderr = &buildStderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("gox build -keep-transformed failed: %v\nstderr: %s\nstdout: %s", err, buildStderr.String(), buildStdout.String())
	}

	// Verify that .gox-cache/staging exists
	stagingDir := filepath.Join(tmpDir, ".gox-cache", "staging")
	if fi, err := os.Stat(stagingDir); os.IsNotExist(err) || !fi.IsDir() {
		t.Fatalf(".gox-cache/staging was not retained")
	}

	// Verify transformed main.go contains AllocImmortal or AllocReadOnly
	transformedMain, err := os.ReadFile(filepath.Join(stagingDir, "main.go"))
	if err != nil {
		t.Fatalf("read transformed main.go: %v", err)
	}
	if !strings.Contains(string(transformedMain), "goxrt.Alloc") {
		t.Errorf("expected transformed main.go to contain goxrt.Alloc, got:\n%s", string(transformedMain))
	}

	// Run compiled binary
	runCmd := exec.Command(outBin)
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run server binary failed: %v, output: %s", err, string(out))
	}
	expected := "Server running at :8080\n"
	if string(out) != expected {
		t.Errorf("output = %q, want %q", string(out), expected)
	}
}

func TestE2ERunArgumentForwarding(t *testing.T) {
	goxBin := buildGoxBinary(t)
	tmpDir := t.TempDir()

	src := `package main

import (
	"fmt"
	"os"
)

func main() {
	for i, arg := range os.Args[1:] {
		fmt.Printf("arg%d=%s\n", i+1, arg)
	}
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	modContent := "module example.com/test-args\n\ngo 1.24.0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(modContent), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	runCmd := exec.Command(goxBin, "run", "-dir", tmpDir, ".", "--", "foo", "bar", "baz")
	var stdout, stderr bytes.Buffer
	runCmd.Stdout = &stdout
	runCmd.Stderr = &stderr
	if err := runCmd.Run(); err != nil {
		t.Fatalf("gox run with args failed: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}

	expected := "arg1=foo\narg2=bar\narg3=baz\n"
	if stdout.String() != expected {
		t.Errorf("output = %q, want %q", stdout.String(), expected)
	}
}

func TestE2ECrossCompilation(t *testing.T) {
	goxBin := buildGoxBinary(t)
	tmpDir := t.TempDir()
	basicDir, err := filepath.Abs("../../examples/basic")
	if err != nil {
		t.Fatalf("resolve basic dir: %v", err)
	}

	// 1. Cross-compile for Linux amd64
	linuxBin := filepath.Join(tmpDir, "basic-linux")
	cmdLinux := exec.Command(goxBin, "build", "-os", "linux", "-arch", "amd64", "-o", linuxBin, "-dir", basicDir, ".")
	if out, err := cmdLinux.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile linux failed: %v, output: %s", err, string(out))
	}
	dataLinux, err := os.ReadFile(linuxBin)
	if err != nil {
		t.Fatalf("read linux binary: %v", err)
	}
	// Check ELF magic: 0x7F 'E' 'L' 'F'
	if len(dataLinux) < 4 || dataLinux[0] != 0x7F || dataLinux[1] != 'E' || dataLinux[2] != 'L' || dataLinux[3] != 'F' {
		t.Errorf("expected ELF binary for linux, got header: %x", dataLinux[:min(4, len(dataLinux))])
	}

	// 2. Cross-compile for Windows amd64
	winBin := filepath.Join(tmpDir, "basic-win.exe")
	cmdWin := exec.Command(goxBin, "build", "-os", "windows", "-arch", "amd64", "-o", winBin, "-dir", basicDir, ".")
	if out, err := cmdWin.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile windows failed: %v, output: %s", err, string(out))
	}
	dataWin, err := os.ReadFile(winBin)
	if err != nil {
		t.Fatalf("read windows binary: %v", err)
	}
	// Check PE/MZ magic: 'M' 'Z'
	if len(dataWin) < 2 || dataWin[0] != 'M' || dataWin[1] != 'Z' {
		t.Errorf("expected PE binary for windows, got header: %x", dataWin[:min(2, len(dataWin))])
	}
}

func TestE2EMultiPackagePipeline(t *testing.T) {
	goxBin := buildGoxBinary(t)
	pipelineDir, err := filepath.Abs("../../examples/pipeline")
	if err != nil {
		t.Fatalf("resolve pipeline dir: %v", err)
	}

	runCmd := exec.Command(goxBin, "run", "-dir", pipelineDir, ".")
	var stdout, stderr bytes.Buffer
	runCmd.Stdout = &stdout
	runCmd.Stderr = &stderr
	if err := runCmd.Run(); err != nil {
		t.Fatalf("gox run pipeline failed: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}

	expected := "[GOX-StreamProcessor] Processed 50 records, Sum: 1725.0\n"
	if stdout.String() != expected {
		t.Errorf("output = %q, want %q", stdout.String(), expected)
	}
}

func TestE2EIncrementalCacheSpeedup(t *testing.T) {
	goxBin := buildGoxBinary(t)
	tmpDir := t.TempDir()
	pipelineDir, err := filepath.Abs("../../examples/pipeline")
	if err != nil {
		t.Fatalf("resolve pipeline dir: %v", err)
	}

	outBin := filepath.Join(tmpDir, "pipeline-bin")

	// First build - cache miss
	cmd1 := exec.Command(goxBin, "build", "-v", "-o", outBin, "-dir", pipelineDir, ".")
	var stdout1, stderr1 bytes.Buffer
	cmd1.Stdout = &stdout1
	cmd1.Stderr = &stderr1
	if err := cmd1.Run(); err != nil {
		t.Fatalf("first build failed: %v\nstderr: %s", err, stderr1.String())
	}

	// Second build - cache hit
	cmd2 := exec.Command(goxBin, "build", "-v", "-o", outBin, "-dir", pipelineDir, ".")
	var stdout2, stderr2 bytes.Buffer
	cmd2.Stdout = &stdout2
	cmd2.Stderr = &stderr2
	if err := cmd2.Run(); err != nil {
		t.Fatalf("second build failed: %v\nstderr: %s", err, stderr2.String())
	}

	if !strings.Contains(stdout2.String(), "using cached memory strategy decisions") {
		t.Errorf("expected cached decisions in second build, got:\n%s", stdout2.String())
	}
}
