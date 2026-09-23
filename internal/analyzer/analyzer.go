package analyzer

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const analyzerVersion = "1.0.0"

type sourceFunction struct {
	pkg        *ssa.Package
	function   *ssa.Function
	packageDir string
}

// Analyze loads the requested Go packages, builds SSA, discovers allocation
// sites, and applies a deliberately conservative lifetime classification.
func Analyze(cfg Config) (*Result, error) {
	if len(cfg.Patterns) == 0 {
		cfg.Patterns = []string{"."}
	}
	started := time.Now()
	cpuStarted := processCPUTime()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	loadStarted := time.Now()
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:        cfg.Dir,
		Tests:      cfg.IncludeTests,
		Env:        cfg.Env,
		BuildFlags: cfg.BuildFlags,
	}, cfg.Patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if err := packageErrors(loaded); err != nil {
		return nil, err
	}

	if len(loaded) == 0 {
		return nil, fmt.Errorf("no packages matched %s", strings.Join(cfg.Patterns, ", "))
	}

	prog, ssaPackages := ssautil.AllPackages(loaded, ssa.InstantiateGenerics)
	for _, pkg := range ssaPackages {
		if pkg != nil {
			pkg.Build()
		}
	}
	compilationTime := time.Since(loadStarted)

	analysisMode := "ownership"
	if cfg.MemoryStrategy {
		analysisMode = "memory-strategy"
	}
	result := &Result{
		Version:      analyzerVersion,
		AnalysisMode: analysisMode,
		GeneratedAt:  time.Now().UTC(),
		Patterns:     append([]string(nil), cfg.Patterns...),
		Summary: Summary{
			ByClassification: map[Classification]int{
				StackSafe: 0, PotentialStack: 0, RegionCandidate: 0,
				ARCCandidate: 0, RuntimeFallback: 0, Unknown: 0,
			},
		},
	}

	seenPackages := make(map[string]bool)
	seenFunctions := make(map[string]bool)
	sourceFunctions := make(map[*ssa.Function]bool)
	var functionsToAnalyze []sourceFunction
	for index, pkg := range ssaPackages {
		if pkg == nil || pkg.Pkg == nil {
			continue
		}
		if loaded[index].Name == "main" && strings.HasSuffix(loaded[index].PkgPath, ".test") {
			// packages.Load synthesizes a test-driver main package. Its
			// generated harness is not user code and would skew allocation
			// counts whenever -tests is enabled.
			continue
		}
		seenPackages[pkg.Pkg.Path()] = true
		functions := packageFunctions(prog, pkg, loaded[index])
		for _, fn := range functions {
			key := fmt.Sprintf("%s@%d", fn.String(), fn.Pos())
			if seenFunctions[key] {
				continue
			}
			seenFunctions[key] = true
			result.Summary.FunctionsAnalyzed++
			sourceFunctions[fn] = true
			functionsToAnalyze = append(functionsToAnalyze, sourceFunction{pkg: pkg, function: fn, packageDir: loaded[index].Dir})
		}
	}
	result.Summary.PackagesAnalyzed = len(seenPackages)
	graph := newGraphBuilder()
	callGraph := buildCallGraph(prog, sourceFunctions, prog.Fset, graph)
	engine := newOwnershipEngine(prog.Fset, callGraph, graph, cfg.Trace, cfg.ClosedWorld)
	for _, source := range functionsToAnalyze {
		engine.discover(source.pkg, source.function, source.packageDir)
	}
	var packageDurationMap map[string]time.Duration
	result.Allocations, packageDurationMap = engine.analyzeAll()
	packageDurations := make([]time.Duration, 0, len(packageDurationMap))
	for _, duration := range packageDurationMap {
		packageDurations = append(packageDurations, duration)
	}
	result.CallGraph = callGraph.summary
	result.Graph = graph.build()
	result.Summary.AllocationsDiscovered = len(result.Allocations)

	slices.SortFunc(result.Allocations, func(a, b Allocation) int {
		if c := cmp.Compare(a.Position, b.Position); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Function, b.Function); c != 0 {
			return c
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	for _, allocation := range result.Allocations {
		result.Summary.ByClassification[allocation.Classification]++
	}
	if total := result.Summary.AllocationsDiscovered; total > 0 {
		fallback := result.Summary.ByClassification[RuntimeFallback] + result.Summary.ByClassification[Unknown]
		result.Summary.PotentialGCReductionPC = 100 * float64(total-fallback) / float64(total)
	}

	// v0.8: Run memory-strategy analysis with stack, immortal, region, unique-owner, ARC, and weak proof engines.
	if cfg.MemoryStrategy {
		escapeIdx := runEscapeAnalysis(cfg.Dir, cfg.Patterns, cfg.Env, cfg.BuildFlags)
		spEngine := newStackProofEngine(engine, escapeIdx)
		immortalEngine := newImmortalProofEngine(engine)
		rpEngine := newRegionProofEngine(engine, escapeIdx)
		upEngine := newUniqueProofEngine(engine, escapeIdx)
		arcEngine := newARCProofEngine(engine)
		weakEngine := newWeakProofEngine(engine, nil)
		stratEngine := newStrategyEngine(spEngine, immortalEngine, rpEngine, upEngine, arcEngine, weakEngine)
		result.Decisions, result.StrategySummary = stratEngine.decide(result.Allocations, engine.sites)
	}

	elapsed := time.Since(started)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	result.Metrics = Metrics{
		AveragePackageLatencyMS: durationMillis(averageDuration(packageDurations)),
		P95PackageLatencyMS:     durationMillis(percentile(packageDurations, 0.95)),
		P99PackageLatencyMS:     durationMillis(percentile(packageDurations, 0.99)),
		MemoryUsageBytes:        max(after.Sys, before.Sys),
		CPUUsageMS:              durationMillis(processCPUTime() - cpuStarted),
		BinarySizeBytes:         executableSize(),
		CompilationTimeMS:       durationMillis(compilationTime),
		AnalysisTimeMS:          durationMillis(elapsed),
	}
	if elapsed > 0 {
		result.Metrics.ThroughputAllocationsPerSecond = float64(len(result.Allocations)) / elapsed.Seconds()
	}
	result.Disclaimers = []string{
		"GOX v0.7 is a proof-producing memory strategy engine (Stack, Region, Unique-Owner, ARC, Weak). It does not yet generate native code or replace the Go garbage collector.",
		"Only PROVEN decisions (Stack, Region, UniqueOwned, ARC, Weak) are safe for transformation; PROBABLE and UNPROVEN require further analysis.",
		"Potential GC reduction is allocation-site coverage and is not weighted by execution frequency or allocated bytes.",
	}
	result.Limitations = []string{
		"Weak references and cycle breaking handle asymmetric pointer cycles (Parent, Prev, Owner); general symmetric peer-to-peer cycles fall back to tracing GC.",
		"Unique-owner reclamation inserts deterministic destruction at the point of last use; shared and cyclic ownership outside regions are handled by ARC and Weak references.",
		"Region inference groups compatible allocations into function-local, caller-owned, or request-scoped arenas; dynamic region migration across arbitrary goroutines is not supported.",
		"The CHA call graph is context-insensitive and may merge unrelated values at shared parameters and returns.",
		"Open-world dynamic dispatch, external functions without SSA bodies, reflection, unsafe pointers, cgo/FFI, and recovered panic values fall back.",
		"Goroutine and channel lifetimes are not ordered by a happens-before analysis.",
		"Runtime allocation frequency and byte volume require external profiling.",
		"Immortal allocations are planned for v0.8.",
	}
	result.Warnings = []string{
		"GOX v0.7 memory-strategy decisions require PROVEN confidence before use in code generation.",
		"Potential GC reduction is allocation-site coverage, not allocation-weighted runtime memory reduction.",
		"Compilation time measures package loading plus SSA construction; memory usage is process reserved memory.",
	}
	return result, nil
}

func packageFunctions(prog *ssa.Program, pkg *ssa.Package, loaded *packages.Package) []*ssa.Function {
	seen := make(map[*ssa.Function]bool)
	var functions []*ssa.Function
	var add func(*ssa.Function)
	add = func(fn *ssa.Function) {
		if fn == nil || seen[fn] {
			return
		}
		seen[fn] = true
		if len(fn.Blocks) > 0 {
			functions = append(functions, fn)
		}
		for _, child := range fn.AnonFuncs {
			add(child)
		}
	}
	for _, file := range loaded.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			object, ok := loaded.TypesInfo.Defs[declaration.Name].(*types.Func)
			if ok {
				add(prog.FuncValue(object))
			}
			return false
		})
	}
	add(pkg.Func("init"))
	for _, member := range pkg.Members {
		if fn, ok := member.(*ssa.Function); ok {
			add(fn)
		}
	}
	slices.SortFunc(functions, func(a, b *ssa.Function) int {
		return cmp.Compare(a.String(), b.String())
	})
	return functions
}

func packageErrors(loaded []*packages.Package) error {
	var messages []string
	seen := make(map[string]bool)
	var visit func(*packages.Package)
	visit = func(pkg *packages.Package) {
		if pkg == nil || seen[pkg.ID] {
			return
		}
		seen[pkg.ID] = true
		for _, pkgErr := range pkg.Errors {
			messages = append(messages, pkgErr.Error())
		}
		for _, imported := range pkg.Imports {
			visit(imported)
		}
	}
	for _, pkg := range loaded {
		visit(pkg)
	}
	if len(messages) == 0 {
		return nil
	}
	slices.Sort(messages)
	const limit = 5
	shown := messages
	if len(shown) > limit {
		shown = shown[:limit]
	}
	message := strings.Join(shown, "; ")
	if len(messages) > limit {
		message += fmt.Sprintf("; and %d more", len(messages)-limit)
	}
	return fmt.Errorf("package loading reported %d error(s): %s", len(messages), message)
}

func allocationValue(instruction ssa.Instruction) (ssa.Value, string, bool, bool) {
	switch value := instruction.(type) {
	case *ssa.Alloc:
		return value, "alloc", value.Heap, true
	case *ssa.MakeSlice:
		return value, "slice", true, true
	case *ssa.MakeMap:
		return value, "map", true, true
	case *ssa.MakeChan:
		return value, "channel", true, true
	case *ssa.MakeClosure:
		return value, "closure", true, true
	default:
		return nil, "", false, false
	}
}

func durationMillis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func averageDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var sum time.Duration
	for _, value := range values {
		sum += value
	}
	return sum / time.Duration(len(values))
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	copyOfValues := slices.Clone(values)
	slices.Sort(copyOfValues)
	index := int(float64(len(copyOfValues)-1)*p + 0.5)
	return copyOfValues[index]
}

func executableSize() int64 {
	path, err := os.Executable()
	if err != nil {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
