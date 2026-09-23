package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
	"github.com/goxlang/gox/internal/cache"
	"github.com/goxlang/gox/internal/transform"
)

// runBuild compiles Go source code using memory-strategy AST transformation.
func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output binary path")
	dir := fs.String("dir", "", "source directory (defaults to current directory)")
	keepTransformed := fs.Bool("keep-transformed", false, "retain transformed code in .gox-cache/staging")
	verbose := fs.Bool("v", false, "verbose output")
	closedWorld := fs.Bool("closed-world", true, "treat CHA dynamic-call targets as complete (executables only)")
	noCache := fs.Bool("no-cache", false, "bypass incremental build decision cache")
	targetOS := fs.String("os", "", "target operating system (GOOS)")
	targetArch := fs.String("arch", "", "target architecture (GOARCH)")
	tags := fs.String("tags", "", "build tags passed to analyzer and compiler")
	ldflags := fs.String("ldflags", "", "arguments to pass to the go tool link invocation")
	gcflags := fs.String("gcflags", "", "arguments to pass to each go tool compile invocation")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: gox build [flags] [package patterns]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"."}
	}

	srcDir := "."
	if *dir != "" {
		srcDir = *dir
	} else if len(patterns) == 1 && patterns[0] != "." && !strings.Contains(patterns[0], "...") {
		if fi, err := os.Stat(patterns[0]); err == nil && fi.IsDir() {
			srcDir = patterns[0]
			patterns = []string{"."}
		}
	}

	absSrcDir, err := filepath.Abs(srcDir)
	if err != nil {
		fmt.Fprintf(stderr, "gox build: resolve source dir: %v\n", err)
		return 1
	}

	goos := *targetOS
	if goos == "" {
		goos = os.Getenv("GOOS")
		if goos == "" {
			goos = runtime.GOOS
		}
	}

	goarch := *targetArch
	if goarch == "" {
		goarch = os.Getenv("GOARCH")
		if goarch == "" {
			goarch = runtime.GOARCH
		}
	}

	outBin := *output
	if outBin == "" {
		base := filepath.Base(absSrcDir)
		if base == "." || base == "/" || base == "\\" {
			base = "app"
		}
		if goos == "windows" {
			base += ".exe"
		}
		outBin = base
	}
	absOutBin, err := filepath.Abs(outBin)
	if err != nil {
		fmt.Fprintf(stderr, "gox build: resolve output binary path: %v\n", err)
		return 1
	}

	// Prepare analyzer environment and build flags for target platform
	var analysisEnv []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOOS=") && !strings.HasPrefix(e, "GOARCH=") {
			analysisEnv = append(analysisEnv, e)
		}
	}
	analysisEnv = append(analysisEnv, "GOOS="+goos, "GOARCH="+goarch)

	var buildFlags []string
	if *tags != "" {
		buildFlags = append(buildFlags, "-tags", *tags)
	}

	analysisPatterns := patterns
	if len(patterns) == 1 && patterns[0] == "." {
		analysisPatterns = []string{"./..."}
	}

	// 1. Check Incremental Decision Cache
	cacheDir := filepath.Join(absSrcDir, ".gox-cache")
	var res *analyzer.Result
	var cacheHit bool

	pkgHash, err := cache.ComputePackageHash(absSrcDir, analysisPatterns, []string{"GOOS=" + goos, "GOARCH=" + goarch}, buildFlags)
	if err == nil && !*noCache {
		cachedRes, found, err := cache.LoadDecisionCache(cacheDir, pkgHash)
		if err == nil && found && cachedRes != nil {
			res = cachedRes
			cacheHit = true
			if *verbose {
				fmt.Fprintf(stdout, "gox: using cached memory strategy decisions (%s)\n", pkgHash[:12])
			}
		}
	}

	if !cacheHit {
		if *verbose {
			fmt.Fprintf(stdout, "gox: analyzing memory strategies in %s (target %s/%s)...\n", absSrcDir, goos, goarch)
		}
		res, err = analyzer.Analyze(analyzer.Config{
			Dir:            absSrcDir,
			Patterns:       analysisPatterns,
			MemoryStrategy: true,
			ClosedWorld:    *closedWorld,
			Env:            analysisEnv,
			BuildFlags:     buildFlags,
		})
		if err != nil {
			fmt.Fprintf(stderr, "gox build: analysis failed: %v\n", err)
			return 1
		}

		if !*noCache && pkgHash != "" {
			_ = cache.StoreDecisionCache(cacheDir, pkgHash, res)
		}
	}

	if *verbose && res != nil && res.StrategySummary != nil {
		fmt.Fprintf(stdout, "gox: %d allocations, %.1f%% proven GC reduction\n",
			res.Summary.AllocationsDiscovered, res.StrategySummary.ProvenGCReduction)
	}

	// 2. Set up staging directory
	var stagingDir string
	if *keepTransformed {
		stagingDir = filepath.Join(absSrcDir, ".gox-cache", "staging")
		_ = os.RemoveAll(stagingDir)
	} else {
		stagingDir, err = os.MkdirTemp("", "gox-staging-*")
		if err != nil {
			fmt.Fprintf(stderr, "gox build: create temp dir: %v\n", err)
			return 1
		}
		defer os.RemoveAll(stagingDir)
	}

	if err := copyDirectory(absSrcDir, stagingDir); err != nil {
		fmt.Fprintf(stderr, "gox build: copy source files: %v\n", err)
		return 1
	}

	// 3. Emit standalone runtime package
	if err := transform.EmitRuntime(stagingDir); err != nil {
		fmt.Fprintf(stderr, "gox build: emit runtime: %v\n", err)
		return 1
	}

	// 4. Ensure go.mod exists in staging directory
	modName := transform.FindModuleName(absSrcDir)
	stagingMod := filepath.Join(stagingDir, "go.mod")
	if _, err := os.Stat(stagingMod); os.IsNotExist(err) {
		defaultMod := fmt.Sprintf("module %s\n\ngo 1.24.0\n", modName)
		if err := os.WriteFile(stagingMod, []byte(defaultMod), 0644); err != nil {
			fmt.Fprintf(stderr, "gox build: write go.mod: %v\n", err)
			return 1
		}
	} else {
		if err := normalizeStagingGoMod(stagingMod, absSrcDir); err != nil {
			fmt.Fprintf(stderr, "gox build: normalize go.mod: %v\n", err)
			return 1
		}
	}

	// 5. Transform all Go sources
	runtimeImportPath := modName + "/goxrt"
	transformer := transform.NewTransformer(transform.TransformConfig{
		OutputDir:         stagingDir,
		RuntimeImportPath: runtimeImportPath,
		PreserveComments:  true,
	}, res)

	transformedFiles, err := transformer.TransformDirectory(stagingDir, stagingDir)
	if err != nil {
		fmt.Fprintf(stderr, "gox build: transform error: %v\n", err)
		return 1
	}

	if *verbose {
		fmt.Fprintf(stdout, "gox: transformed %d files\n", len(transformedFiles))
		for _, f := range transformedFiles {
			fmt.Fprintf(stdout, "  rewritten: %s\n", f)
		}
	}

	// 6. Compile executable using Go toolchain
	goBuildArgs := []string{"build", "-o", absOutBin}
	if *tags != "" {
		goBuildArgs = append(goBuildArgs, "-tags", *tags)
	}
	if *ldflags != "" {
		goBuildArgs = append(goBuildArgs, "-ldflags", *ldflags)
	}
	if *gcflags != "" {
		goBuildArgs = append(goBuildArgs, "-gcflags", *gcflags)
	}

	pkgTarget := "."
	if len(patterns) > 0 && patterns[0] != "." && !strings.Contains(patterns[0], "...") {
		pkgTarget = patterns[0]
	}
	goBuildArgs = append(goBuildArgs, pkgTarget)

	cmd := exec.Command("go", goBuildArgs...)
	cmd.Dir = stagingDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	var buildEnv []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOOS=") && !strings.HasPrefix(e, "GOARCH=") {
			buildEnv = append(buildEnv, e)
		}
	}
	buildEnv = append(buildEnv, "GOOS="+goos, "GOARCH="+goarch)
	cmd.Env = buildEnv
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "gox build: compilation failed: %v\n", err)
		return 1
	}

	if *verbose {
		fmt.Fprintf(stdout, "gox: binary written to %s\n", absOutBin)
	}
	return 0
}

// runRun builds and directly executes a Go program.
func runRun(args []string, stdout, stderr io.Writer) int {
	var buildArgs []string
	var appArgs []string

	for i, arg := range args {
		if arg == "--" {
			buildArgs = args[:i]
			appArgs = args[i+1:]
			break
		}
	}
	if appArgs == nil {
		buildArgs = args
	}

	// Check if cross-compilation is requested (cannot execute cross-compiled binaries directly)
	for i, a := range buildArgs {
		if (a == "-os" || strings.HasPrefix(a, "-os=")) && i < len(buildArgs) {
			val := strings.TrimPrefix(a, "-os=")
			if val == "-os" && i+1 < len(buildArgs) {
				val = buildArgs[i+1]
			}
			if val != "" && val != runtime.GOOS {
				fmt.Fprintf(stderr, "gox run: cannot execute binary cross-compiled for %s on %s\n", val, runtime.GOOS)
				return 1
			}
		}
		if (a == "-arch" || strings.HasPrefix(a, "-arch=")) && i < len(buildArgs) {
			val := strings.TrimPrefix(a, "-arch=")
			if val == "-arch" && i+1 < len(buildArgs) {
				val = buildArgs[i+1]
			}
			if val != "" && val != runtime.GOARCH {
				fmt.Fprintf(stderr, "gox run: cannot execute binary cross-compiled for %s on %s\n", val, runtime.GOARCH)
				return 1
			}
		}
	}

	tmpDir, err := os.MkdirTemp("", "gox-run-*")
	if err != nil {
		fmt.Fprintf(stderr, "gox run: create temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	binName := "app"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	tmpBin := filepath.Join(tmpDir, binName)

	filtered := make([]string, 0, len(buildArgs))
	skipNext := false
	for _, a := range buildArgs {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-o" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(a, "-o=") {
			continue
		}
		filtered = append(filtered, a)
	}
	fullBuildArgs := append([]string{"-o", tmpBin}, filtered...)

	if code := runBuild(fullBuildArgs, stdout, stderr); code != 0 {
		return code
	}

	targetDir := "."
	for i, a := range buildArgs {
		if (a == "-dir" || strings.HasPrefix(a, "-dir=")) && i < len(buildArgs) {
			val := strings.TrimPrefix(a, "-dir=")
			if val == "-dir" && i+1 < len(buildArgs) {
				val = buildArgs[i+1]
			}
			if val != "" {
				targetDir = val
			}
		}
	}
	absTargetDir, _ := filepath.Abs(targetDir)

	cmd := exec.Command(tmpBin, appArgs...)
	cmd.Dir = absTargetDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "gox run: %v\n", err)
		return 1
	}
	return 0
}

func copyDirectory(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" || name == ".gox-cache" || name == "goxrt" {
			continue
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeStagingGoMod(stagingModPath, absSrcDir string) error {
	content, err := os.ReadFile(stagingModPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	changed := false
	for i, line := range lines {
		if strings.Contains(line, "=>") {
			parts := strings.Split(line, "=>")
			if len(parts) == 2 {
				target := strings.TrimSpace(parts[1])
				if strings.HasPrefix(target, ".") {
					absTarget := filepath.Clean(filepath.Join(absSrcDir, target))
					lines[i] = parts[0] + "=> " + absTarget
					changed = true
				}
			}
		}
	}
	if changed {
		return os.WriteFile(stagingModPath, []byte(strings.Join(lines, "\n")), 0644)
	}
	return nil
}

