package doctor

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
)

// DiagnosticCategory represents a high-level root cause for GC fallback.
type DiagnosticCategory string

const (
	CategoryReflection      DiagnosticCategory = "REFLECTION_OR_ORM"
	CategoryInterfaceBoxing DiagnosticCategory = "INTERFACE_BOXING"
	CategoryExportedAPI     DiagnosticCategory = "EXPORTED_API_RETURN"
	CategoryClosure         DiagnosticCategory = "CLOSURE_OR_GOROUTINE"
	CategoryUnresolvedCycle DiagnosticCategory = "UNRESOLVED_CYCLE"
	CategoryGlobalState     DiagnosticCategory = "GLOBAL_MUTATION"
	CategoryGeneralFallback DiagnosticCategory = "COMPLEX_ESCAPE"
)

// DiagnosticIssue represents a clustered group of allocation sites sharing a root cause.
type DiagnosticIssue struct {
	Category    DiagnosticCategory `json:"category"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Count       int                `json:"count"`
	Severity    string             `json:"severity"` // "CRITICAL", "HIGH", "MEDIUM"
	Remediation []string           `json:"remediation"`
	Sites       []IssueSite        `json:"sites"`
}

// IssueSite details a single allocation site that escaped.
type IssueSite struct {
	Position string `json:"position"`
	Function string `json:"function"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// DoctorReport holds the complete diagnostic health assessment.
type DoctorReport struct {
	TotalAllocations  int               `json:"total_allocations"`
	ProvenAllocations int               `json:"proven_allocations"`
	FallbackCount     int               `json:"fallback_count"`
	HealthScore       float64           `json:"health_score_pct"`
	StrategyCounts    map[string]int    `json:"strategy_counts"`
	Issues            []DiagnosticIssue `json:"issues"`
	SummaryAdvice     []string          `json:"summary_advice"`
}

// Diagnose evaluates an analyzer.Result and returns actionable escape diagnostics.
func Diagnose(result *analyzer.Result) *DoctorReport {
	report := &DoctorReport{
		StrategyCounts: make(map[string]int),
		Issues:         make([]DiagnosticIssue, 0),
		SummaryAdvice:  make([]string, 0),
	}

	if result == nil {
		return report
	}

	report.TotalAllocations = len(result.Decisions)
	if report.TotalAllocations == 0 && len(result.Allocations) > 0 {
		report.TotalAllocations = len(result.Allocations)
	}

	categorySites := make(map[DiagnosticCategory][]IssueSite)

	for _, d := range result.Decisions {
		stratStr := string(d.Strategy)
		if stratStr == "" {
			stratStr = "TRACING_FALLBACK"
		}
		report.StrategyCounts[stratStr]++

		if d.Confidence == analyzer.ConfidenceProven {
			report.ProvenAllocations++
			continue
		}

		report.FallbackCount++
		cat, reason := classifyDecision(d)
		categorySites[cat] = append(categorySites[cat], IssueSite{
			Position: d.SourcePosition,
			Function: d.Function,
			Type:     d.Type,
			Reason:   reason,
		})
	}

	if report.TotalAllocations > 0 {
		report.HealthScore = (float64(report.ProvenAllocations) / float64(report.TotalAllocations)) * 100.0
	}

	// Build Diagnostic Issues
	buildIssues(report, categorySites)

	// Sort issues by allocation count descending
	slices.SortFunc(report.Issues, func(a, b DiagnosticIssue) int {
		return cmp.Compare(b.Count, a.Count)
	})

	// Add high-level advice based on health score
	if report.HealthScore >= 80.0 {
		report.SummaryAdvice = append(report.SummaryAdvice,
			"Excellent deterministic memory profile! Most allocations are zero-GC stack/region/unique managed.",
		)
	} else if report.HealthScore >= 40.0 {
		report.SummaryAdvice = append(report.SummaryAdvice,
			"Moderate GC reliance. Converting remaining slice and interface escapes will push performance to 90%+ zero-GC.",
		)
	} else {
		report.SummaryAdvice = append(report.SummaryAdvice,
			"High GC pressure detected. Heavy reflection, interface boxing, or unanalyzed external calls are preventing static proofs.",
			"Consider replacing reflection-based ORMs/serializers with compile-time code generation (e.g., sqlc, templ).",
			"Wrap request lifecycles with goxrt.WithRequestArena() to reclaim request memory in O(1) time.",
		)
	}

	return report
}

func classifyDecision(d analyzer.AllocationDecision) (DiagnosticCategory, string) {
	for _, br := range d.BlockingReasons {
		descLower := strings.ToLower(br.Description + " " + br.Kind + " " + br.Construct)
		if strings.Contains(descLower, "reflection") || strings.Contains(descLower, "gorm") {
			return CategoryReflection, "Dynamic reflection call prevents static lifetime bounding"
		}
		if br.Construct == "external_call" || strings.Contains(descLower, "dynamic or external call") {
			return CategoryInterfaceBoxing, "Passed to external/dynamic call or boxed into interface{}/any parameter"
		}
		if br.Construct == "exported_return" || strings.Contains(descLower, "exported library api") {
			return CategoryExportedAPI, "Reference leaves package through exported function return"
		}
		if br.Construct == "closure" || br.Construct == "goroutine" || strings.Contains(descLower, "closure") {
			return CategoryClosure, "Captured by closure or asynchronous goroutine"
		}
		if br.Construct == "cycle" || strings.Contains(descLower, "cyclic") {
			return CategoryUnresolvedCycle, "Data structure is cyclic with no identifiable back-pointer field"
		}
		if strings.Contains(descLower, "global") {
			return CategoryGlobalState, "Referenced or stored in mutable package global state"
		}
	}

	if d.FallbackReason != "" {
		fl := strings.ToLower(d.FallbackReason)
		if strings.Contains(fl, "dynamic") || strings.Contains(fl, "external") {
			return CategoryInterfaceBoxing, d.FallbackReason
		}
		if strings.Contains(fl, "exported") {
			return CategoryExportedAPI, d.FallbackReason
		}
	}

	return CategoryGeneralFallback, "Escapes lexical scope and cannot be safely bounded by current proof engine"
}

func buildIssues(report *DoctorReport, categorySites map[DiagnosticCategory][]IssueSite) {
	if sites, ok := categorySites[CategoryReflection]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryReflection,
			Title:       "Dynamic Reflection & ORM Boxing",
			Description: "Pointers passed to reflection-heavy libraries (GORM, html/template) cannot be proven safe at compile time.",
			Count:       len(sites),
			Severity:    "CRITICAL",
			Remediation: []string{
				"Replace reflection ORM queries (db.Find(&todos)) with typed SQL (database/sql or sqlc).",
				"Use pre-compiled template generators (e.g., templ, quicktemplate) instead of runtime reflection templates.",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryInterfaceBoxing]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryInterfaceBoxing,
			Title:       "Interface Boxing & External Dynamic Calls",
			Description: "Values boxed into any / interface{} (e.g., fmt.Println, loggers, or generic dispatchers) escape to the heap.",
			Count:       len(sites),
			Severity:    "HIGH",
			Remediation: []string{
				"Avoid interface{} boxing in hot paths; use concrete struct types or strconv formatting.",
				"Pre-allocate reusable buffers or allocate slices inside active region arenas (goxrt.AllocSlice).",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryExportedAPI]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryExportedAPI,
			Title:       "Exported API Return Escapes",
			Description: "Pointers returned by exported functions are assumed to escape to unknown external callers.",
			Count:       len(sites),
			Severity:    "MEDIUM",
			Remediation: []string{
				"For standalone applications (main package), pass -closed-world to treat whole program as complete.",
				"Accept a caller-provided destination pointer or arena parameter instead of returning new heap pointers.",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryClosure]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryClosure,
			Title:       "Closure Capture & Goroutine Escapes",
			Description: "Variables captured by reference inside closures or passed to goroutines outlive the stack frame.",
			Count:       len(sites),
			Severity:    "HIGH",
			Remediation: []string{
				"Pass values explicitly by value to goroutines: go func(v T) { ... }(val).",
				"For worker pipelines, use goxrt.NewArena() per worker task and reset after each job.",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryUnresolvedCycle]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryUnresolvedCycle,
			Title:       "Cyclic Pointer Graphs Without Weak Back-Edges",
			Description: "Circular reference chains prevent reference counting (ARC) and unique-owner reclamation.",
			Count:       len(sites),
			Severity:    "MEDIUM",
			Remediation: []string{
				"Name back-pointer fields standardly (e.g., 'Parent', 'Prev', 'Owner') so GOX cycle-breaking can transform them to weak references.",
				"Use a Region Arena to manage the entire cyclic tree as a single bump allocation group.",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryGlobalState]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryGlobalState,
			Title:       "Mutable Global State",
			Description: "Allocations stored in package global variables that are mutated outside package init.",
			Count:       len(sites),
			Severity:    "LOW",
			Remediation: []string{
				"Ensure global configs are initialized once in init() and never written afterwards to qualify for IMMUTABLE_RODATA.",
			},
			Sites: sites,
		})
	}

	if sites, ok := categorySites[CategoryGeneralFallback]; ok && len(sites) > 0 {
		report.Issues = append(report.Issues, DiagnosticIssue{
			Category:    CategoryGeneralFallback,
			Title:       "Complex Escape Paths",
			Description: "Allocations whose lifetimes could not be statically bounded to stack or region scopes.",
			Count:       len(sites),
			Severity:    "LOW",
			Remediation: []string{
				"Inspect individual allocations with: gox analyze -explain-alloc <id>.",
			},
			Sites: sites,
		})
	}
}

// FormatText formats the DoctorReport into a clean, human-readable terminal string.
func (r *DoctorReport) FormatText(topSites int) string {
	var sb strings.Builder

	sb.WriteString("================================================================================\n")
	sb.WriteString("                           GOX Memory Doctor v1.0                               \n")
	sb.WriteString("           Static Escape Diagnostics & Zero-GC Remediation Guide                \n")
	sb.WriteString("================================================================================\n\n")

	sb.WriteString(fmt.Sprintf("Memory Health Score:  %.1f%% Deterministic\n", r.HealthScore))
	sb.WriteString(fmt.Sprintf("Total Allocations:    %d\n", r.TotalAllocations))
	sb.WriteString(fmt.Sprintf("Proven (Zero-GC):     %d\n", r.ProvenAllocations))
	sb.WriteString(fmt.Sprintf("Tracing Fallbacks:    %d\n\n", r.FallbackCount))

	sb.WriteString("Memory Strategy Breakdown:\n")
	stratKeys := make([]string, 0, len(r.StrategyCounts))
	for k := range r.StrategyCounts {
		stratKeys = append(stratKeys, k)
	}
	slices.Sort(stratKeys)
	for _, k := range stratKeys {
		sb.WriteString(fmt.Sprintf("  • %-20s : %d\n", k, r.StrategyCounts[k]))
	}
	sb.WriteString("\n")

	if len(r.Issues) == 0 {
		sb.WriteString("🎉 No GC fallback issues detected! All allocations are proven deterministic.\n")
		return sb.String()
	}

	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString("🚨 TOP GC CULPRITS & REMEDIATION HINTS:\n")
	sb.WriteString("--------------------------------------------------------------------------------\n\n")

	for i, issue := range r.Issues {
		sb.WriteString(fmt.Sprintf("[%d] [%s] %s (%d sites)\n", i+1, issue.Severity, issue.Title, issue.Count))
		sb.WriteString(fmt.Sprintf("    Cause: %s\n", issue.Description))
		sb.WriteString("    Remediation:\n")
		for _, rem := range issue.Remediation {
			sb.WriteString(fmt.Sprintf("      → %s\n", rem))
		}

		limit := topSites
		if limit <= 0 || limit > len(issue.Sites) {
			limit = len(issue.Sites)
		}
		if limit > 5 {
			limit = 5 // default sample size
		}

		sb.WriteString("    Sample Sites:\n")
		for j := 0; j < limit; j++ {
			site := issue.Sites[j]
			sb.WriteString(fmt.Sprintf("      • %s (%s in %s)\n", site.Position, site.Type, site.Function))
		}
		if len(issue.Sites) > limit {
			sb.WriteString(fmt.Sprintf("      ... and %d more sites\n", len(issue.Sites)-limit))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString("💡 ARCHITECTURAL SUMMARY:\n")
	sb.WriteString("--------------------------------------------------------------------------------\n")
	for _, adv := range r.SummaryAdvice {
		sb.WriteString(fmt.Sprintf("  • %s\n", adv))
	}
	sb.WriteString("\n")

	return sb.String()
}

// FormatJSON returns the DoctorReport serialized as JSON.
func (r *DoctorReport) FormatJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
