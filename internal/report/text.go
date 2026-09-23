package report

import (
	"fmt"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
)

func Text(result *analyzer.Result, detail bool) string {
	var out strings.Builder
	summary := result.Summary
	fmt.Fprintln(&out, "GOX Memory Analysis")
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "Packages analyzed: %d\n", summary.PackagesAnalyzed)
	fmt.Fprintf(&out, "Functions analyzed: %d\n", summary.FunctionsAnalyzed)
	fmt.Fprintf(&out, "Allocations discovered: %d\n", summary.AllocationsDiscovered)
	fmt.Fprintf(&out, "Call graph: %d functions, %d edges, %d dynamic edges, %d unresolved calls\n",
		result.CallGraph.Functions, result.CallGraph.CallEdges, result.CallGraph.DynamicCallEdges, result.CallGraph.UnresolvedCalls)
	fmt.Fprintln(&out)
	row(&out, "Stack-safe", summary.ByClassification[analyzer.StackSafe])
	row(&out, "Potentially stack-safe", summary.ByClassification[analyzer.PotentialStack])
	row(&out, "Region candidates", summary.ByClassification[analyzer.RegionCandidate])
	row(&out, "ARC candidates", summary.ByClassification[analyzer.ARCCandidate])
	row(&out, "Runtime fallback", summary.ByClassification[analyzer.RuntimeFallback])
	row(&out, "Unknown", summary.ByClassification[analyzer.Unknown])
	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "Potential tracing-GC reduction: %.1f%% (allocation sites, estimated)\n", summary.PotentialGCReductionPC)
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "Metrics")
	fmt.Fprintf(&out, "Throughput:             %.1f allocations/s\n", result.Metrics.ThroughputAllocationsPerSecond)
	fmt.Fprintf(&out, "Average package latency: %.3f ms\n", result.Metrics.AveragePackageLatencyMS)
	fmt.Fprintf(&out, "p95 package latency:     %.3f ms\n", result.Metrics.P95PackageLatencyMS)
	fmt.Fprintf(&out, "p99 package latency:     %.3f ms\n", result.Metrics.P99PackageLatencyMS)
	fmt.Fprintf(&out, "Memory usage:            %d bytes\n", result.Metrics.MemoryUsageBytes)
	fmt.Fprintf(&out, "CPU usage:               %.3f ms\n", result.Metrics.CPUUsageMS)
	fmt.Fprintf(&out, "Binary size:             %d bytes\n", result.Metrics.BinarySizeBytes)
	fmt.Fprintf(&out, "Compilation/SSA time:    %.3f ms\n", result.Metrics.CompilationTimeMS)
	fmt.Fprintf(&out, "Total analysis time:     %.3f ms\n", result.Metrics.AnalysisTimeMS)

	if detail {
		for _, allocation := range result.Allocations {
			fmt.Fprintln(&out)
			fmt.Fprintf(&out, "id: %s\n", allocation.ID)
			fmt.Fprintf(&out, "allocation: %s\n", allocation.Position)
			fmt.Fprintf(&out, "package: %s\n", allocation.Package)
			fmt.Fprintf(&out, "function: %s\n", allocation.Function)
			fmt.Fprintf(&out, "kind: %s\n", allocation.Kind)
			fmt.Fprintf(&out, "type: %s\n", allocation.Type)
			fmt.Fprintf(&out, "classification: %s\n", allocation.Classification)
			fmt.Fprintf(&out, "ownership: %s\n", allocation.Ownership)
			fmt.Fprintf(&out, "lifetime: %s\n", allocation.Lifetime)
			fmt.Fprintf(&out, "caller depth: %d\n", allocation.CallerDepth)
			if len(allocation.RetainedBy) > 0 {
				fmt.Fprintf(&out, "retained by: %s\n", strings.Join(allocation.RetainedBy, ", "))
			}
			fmt.Fprintln(&out, "reason:")
			for _, reason := range allocation.Reasons {
				fmt.Fprintf(&out, "  - %s\n", reason)
			}
			if len(allocation.Limitations) > 0 {
				fmt.Fprintln(&out, "limitations:")
				for _, limitation := range allocation.Limitations {
					fmt.Fprintf(&out, "  - %s\n", limitation)
				}
			}
			if len(allocation.Trace) > 0 {
				fmt.Fprintln(&out, "trace:")
				for _, event := range allocation.Trace {
					location := event.Position
					if location == "" {
						location = event.Function
					}
					fmt.Fprintf(&out, "  %d. %s [%s]: %s\n", event.Step, event.Operation, location, event.Detail)
				}
			}
		}
	}
	if len(result.Disclaimers) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Disclaimers")
		for _, disclaimer := range result.Disclaimers {
			fmt.Fprintf(&out, "- %s\n", disclaimer)
		}
	}
	if len(result.Limitations) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Limitations")
		for _, limitation := range result.Limitations {
			fmt.Fprintf(&out, "- %s\n", limitation)
		}
	}

	if len(result.Warnings) > 0 {
		fmt.Fprintln(&out)
		fmt.Fprintln(&out, "Notes")
		for _, warning := range result.Warnings {
			fmt.Fprintf(&out, "- %s\n", warning)
		}
	}
	return out.String()
}

func row(out *strings.Builder, label string, value int) {
	fmt.Fprintf(out, "%-25s %8d\n", label+":", value)
}
