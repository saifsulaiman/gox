package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
	"github.com/goxlang/gox/internal/doctor"
	"github.com/goxlang/gox/internal/report"
)

const version = "1.0.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "gox %s\n", version)
		return 0
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gox: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "report format: text or json")
	detail := fs.Bool("detail", false, "include every allocation and its explanation (v0.1 compatibility)")
	explain := fs.Bool("explain", false, "include every allocation and its inference explanation")
	trace := fs.Bool("trace", false, "include step-by-step inference traces (implies -explain)")
	output := fs.String("output", "", "write report to a file instead of stdout")
	graphOutput := fs.String("graph", "", "export the call and retention graph to this file")
	graphFormat := fs.String("graph-format", "dot", "graph format: dot or json")
	tests := fs.Bool("tests", false, "include test variants of packages")
	closedWorld := fs.Bool("closed-world", false, "treat CHA dynamic-call targets as complete (executables only)")
	dir := fs.String("dir", "", "project directory (defaults to current directory)")
	// v0.3 flags
	memoryStrategy := fs.Bool("memory-strategy", false, "run proof-based memory strategy analysis")
	explainAlloc := fs.String("explain-alloc", "", "explain a specific allocation by ID")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: gox analyze [flags] [package patterns]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(stderr, "gox: -format must be text or json")
		return 2
	}
	if *graphFormat != "dot" && *graphFormat != "json" {
		fmt.Fprintln(stderr, "gox: -graph-format must be dot or json")
		return 2
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"."}
	}

	// If -explain-alloc is set, imply -memory-strategy.
	if *explainAlloc != "" {
		*memoryStrategy = true
	}

	result, err := analyzer.Analyze(analyzer.Config{
		Dir:            *dir,
		Patterns:       patterns,
		IncludeTests:   *tests,
		Trace:          *trace,
		ClosedWorld:    *closedWorld,
		MemoryStrategy: *memoryStrategy,
		ExplainAlloc:   *explainAlloc,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gox: analysis failed: %v\n", err)
		return 1
	}

	// Handle -explain-alloc: find and print the specific allocation decision.
	if *explainAlloc != "" {
		return runExplainAlloc(result, *explainAlloc, *format, stdout, stderr)
	}

	var data []byte
	if *format == "json" {
		data, err = json.MarshalIndent(result, "", "  ")
		if err == nil {
			data = append(data, '\n')
		}
	} else if *memoryStrategy {
		data = []byte(report.StrategyText(result, *detail || *explain || *trace))
	} else {
		data = []byte(report.Text(result, *detail || *explain || *trace))
	}
	if err != nil {
		fmt.Fprintf(stderr, "gox: report failed: %v\n", err)
		return 1
	}
	if *graphOutput != "" {
		var graphData []byte
		if *graphFormat == "json" {
			graphData, err = json.MarshalIndent(result.Graph, "", "  ")
			if err == nil {
				graphData = append(graphData, '\n')
			}
		} else {
			graphData = []byte(report.GraphDOT(result.Graph))
		}
		if err != nil {
			fmt.Fprintf(stderr, "gox: graph export failed: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*graphOutput, graphData, 0o644); err != nil {
			fmt.Fprintf(stderr, "gox: write graph: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "GOX graph written to %s\n", *graphOutput)
	}
	if *output != "" {
		if err := os.WriteFile(*output, data, 0o644); err != nil {
			fmt.Fprintf(stderr, "gox: write report: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "GOX report written to %s\n", *output)
		return 0
	}
	_, _ = stdout.Write(data)
	return 0
}

func runExplainAlloc(result *analyzer.Result, allocID, format string, stdout, stderr io.Writer) int {
	for _, decision := range result.Decisions {
		if decision.AllocationID == allocID {
			if format == "json" {
				data, err := json.MarshalIndent(decision, "", "  ")
				if err != nil {
					fmt.Fprintf(stderr, "gox: json marshal: %v\n", err)
					return 1
				}
				fmt.Fprintln(stdout, string(data))
			} else {
				fmt.Fprintln(stdout, report.ExplainDecision(decision))
			}
			return 0
		}
	}
	// Also search in v0.2 allocations if no decision found.
	for _, alloc := range result.Allocations {
		if alloc.ID == allocID {
			if format == "json" {
				data, err := json.MarshalIndent(alloc, "", "  ")
				if err != nil {
					fmt.Fprintf(stderr, "gox: json marshal: %v\n", err)
					return 1
				}
				fmt.Fprintln(stdout, string(data))
			} else {
				fmt.Fprintf(stdout, "Allocation %s (v0.2 only, no strategy decision)\n", allocID)
				fmt.Fprintf(stdout, "  position: %s\n", alloc.Position)
				fmt.Fprintf(stdout, "  classification: %s\n", alloc.Classification)
				for _, r := range alloc.Reasons {
					fmt.Fprintf(stdout, "  reason: %s\n", r)
				}
			}
			return 0
		}
	}
	fmt.Fprintf(stderr, "gox: allocation %q not found\n", allocID)
	return 1
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "report format: text or json")
	top := fs.Int("top", 5, "maximum sample sites to display per issue")
	dir := fs.String("dir", "", "project directory (defaults to current directory)")
	closedWorld := fs.Bool("closed-world", false, "treat CHA dynamic-call targets as complete (executables only)")
	tests := fs.Bool("tests", false, "include test variants of packages")
	output := fs.String("output", "", "write report to a file instead of stdout")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: gox doctor [flags] [package patterns]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"."}
	}

	result, err := analyzer.Analyze(analyzer.Config{
		Dir:            *dir,
		Patterns:       patterns,
		IncludeTests:   *tests,
		ClosedWorld:    *closedWorld,
		MemoryStrategy: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gox: doctor analysis failed: %v\n", err)
		return 1
	}

	docReport := doctor.Diagnose(result)

	var data []byte
	if *format == "json" {
		data, err = docReport.FormatJSON()
		if err != nil {
			fmt.Fprintf(stderr, "gox: json format failed: %v\n", err)
			return 1
		}
		data = append(data, '\n')
	} else {
		data = []byte(docReport.FormatText(*top))
	}

	if *output != "" {
		if err := os.WriteFile(*output, data, 0o644); err != nil {
			fmt.Fprintf(stderr, "gox: write doctor report: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "GOX Doctor report written to %s\n", *output)
		return 0
	}

	_, _ = stdout.Write(data)
	return 0
}

func usage(w io.Writer) {
	lines := []string{
		"GOX v1.0.0 - production-oriented Go toolchain and memory strategy engine",
		"",
		"usage:",
		"  gox build [flags] [package patterns]",
		"  gox run [flags] [package patterns] [-- args...]",
		"  gox analyze [flags] [package patterns]",
		"  gox doctor [flags] [package patterns]",
		"  gox version",
		"",
		"examples:",
		"  gox build -o myapp .",
		"  gox build ./examples/basic",
		"  gox run .",
		"  gox run ./examples/basic",
		"  gox doctor ./...",
		"  gox doctor -dir ./examples/todo-htmx ./...",
		"  gox doctor -format json -output doctor-report.json ./...",
		"  gox analyze ./...",
		"  gox analyze -memory-strategy ./...",
		"  gox analyze -memory-strategy -explain ./...",
		"  gox analyze -explain-alloc <id> ./...",
		"  gox analyze -detail ./cmd/...",
		"  gox analyze -explain -trace ./...",
		"  gox analyze -graph gox.dot -graph-format dot ./...",
		"  gox analyze -format json -output gox-report.json ./...",
	}
	fmt.Fprintln(w, strings.Join(lines, "\n"))
}


