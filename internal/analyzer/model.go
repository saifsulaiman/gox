package analyzer

import "time"

// Classification is the safest allocation strategy suggested by v0.2.
// Candidate classifications are research signals, not compiler proofs.
type Classification string

const (
	StackSafe       Classification = "STACK_SAFE"
	PotentialStack  Classification = "POTENTIALLY_STACK_SAFE"
	RegionCandidate Classification = "REGION_CANDIDATE"
	ARCCandidate    Classification = "ARC_CANDIDATE"
	RuntimeFallback Classification = "RUNTIME_FALLBACK"
	Unknown         Classification = "UNKNOWN"
)

type Ownership string

const (
	OwnershipUnique      Ownership = "UNIQUE"
	OwnershipMoved       Ownership = "MOVED"
	OwnershipBorrowed    Ownership = "BORROWED"
	OwnershipTransferred Ownership = "TRANSFERRED"
	OwnershipShared      Ownership = "SHARED"
	OwnershipGlobal      Ownership = "GLOBAL"
	OwnershipConcurrent  Ownership = "CONCURRENT"
	OwnershipImmortal    Ownership = "IMMORTAL"
	OwnershipUnknown     Ownership = "UNKNOWN"
)

type Lifetime string

const (
	LifetimeFunction  Lifetime = "FUNCTION"
	LifetimeCaller    Lifetime = "CALLER"
	LifetimeRegion    Lifetime = "REGION"
	LifetimeShared    Lifetime = "SHARED"
	LifetimeGlobal    Lifetime = "GLOBAL"
	LifetimeGoroutine Lifetime = "GOROUTINE"
	LifetimeChannel   Lifetime = "CHANNEL"
	LifetimeExternal  Lifetime = "EXTERNAL"
	LifetimeRuntime   Lifetime = "RUNTIME_MANAGED"
	LifetimeUnknown   Lifetime = "UNKNOWN"
)

// MemoryStrategy represents a proven allocation placement decision.
// v0.3 only proves STACK; other strategies are preparation for future versions.
type MemoryStrategy string

const (
	StrategyStack           MemoryStrategy = "STACK"
	StrategyCallerStack     MemoryStrategy = "CALLER_STACK"
	StrategyRegion          MemoryStrategy = "REGION"
	StrategyUniqueOwned     MemoryStrategy = "UNIQUE_OWNED"
	StrategyARC             MemoryStrategy = "ARC"
	StrategyWeak            MemoryStrategy = "WEAK"
	StrategyImmortal        MemoryStrategy = "IMMORTAL"
	StrategyTracingFallback MemoryStrategy = "TRACING_FALLBACK"
)

// Confidence indicates the certainty level of a memory strategy decision.
type Confidence string

const (
	// ConfidenceProven means a sound proof exists; safe to transform.
	ConfidenceProven Confidence = "PROVEN"
	// ConfidenceProbable means analysis suggests safety but proof is incomplete.
	ConfidenceProbable Confidence = "PROBABLE"
	// ConfidenceUnproven means the analysis cannot prove safety; fallback required.
	ConfidenceUnproven Confidence = "UNPROVEN"
)

// BlockingReason explains why a proof could not be completed for a given allocation.
type BlockingReason struct {
	Kind        string `json:"kind"`                  // e.g. "returned_pointer", "goroutine_escape"
	Description string `json:"description"`           // human-readable explanation
	Position    string `json:"position,omitempty"`     // source position of the blocking construct
	Construct   string `json:"construct,omitempty"`    // Go construct: "closure", "goroutine", "channel", etc.
}

// AllocationDecision is the v0.3 proof-carrying decision for each allocation.
// It extends the v0.2 classification with a verifiable proof or explicit
// blocking reasons. Only PROVEN decisions may be used for transformation.
type AllocationDecision struct {
	AllocationID    string           `json:"allocation_id"`
	SourcePosition  string           `json:"source_position"`
	Function        string           `json:"function"`
	Package         string           `json:"package"`
	Type            string           `json:"type"`
	Kind            string           `json:"kind"`
	Strategy        MemoryStrategy   `json:"strategy"`
	Ownership       Ownership        `json:"ownership"`
	Lifetime        Lifetime         `json:"lifetime"`
	Confidence      Confidence       `json:"confidence"`
	Proof           []string         `json:"proof"`
	BlockingReasons []BlockingReason `json:"blocking_reasons,omitempty"`
	FallbackReason  string           `json:"fallback_reason,omitempty"`
	// V02Classification preserves the v0.2 classification for comparison.
	V02Classification Classification `json:"v02_classification"`
	EscapeAnalysis    *EscapeResult  `json:"escape_analysis,omitempty"`
	// v0.4 addition: inferred region metadata.
	Region            *RegionInfo    `json:"region,omitempty"`
	// v0.5 addition: deterministic destruction points for uniquely owned objects.
	DestructionPoints []DestructionPoint `json:"destruction_points,omitempty"`
	// v0.6 addition: inferred automatic reference counting metadata.
	ARC               *ARCInfo           `json:"arc,omitempty"`
	// v0.7 addition: inferred weak reference and cycle-breaking metadata.
	Weak              *WeakInfo          `json:"weak,omitempty"`
	// v0.8 addition: inferred immortal / static process-lifetime metadata.
	Immortal          *ImmortalInfo      `json:"immortal,omitempty"`
}

// ImmortalInfo describes an inferred process-lifetime or static allocation.
type ImmortalInfo struct {
	GlobalVar       string   `json:"global_var,omitempty"`      // Name of package global variable
	InitScope       string   `json:"init_scope"`                // Initialization function or scope ("init", "main", or "var_init")
	IsReadOnly      bool     `json:"is_read_only"`              // Proven never written after initialization
	PlacementKind   string   `json:"placement_kind"`            // "IMMUTABLE_RODATA", "STATIC_DATA", "IMMORTAL_ARENA"
	InvariantsProof []string `json:"invariants_proof"`          // Formal proof clauses for process lifetime
}

// WeakInfo describes an inferred weak reference allocation or cycle-breaking back-pointer.
type WeakInfo struct {
	CycleBrokenBy      string     `json:"cycle_broken_by"`      // Field name that breaks the cycle (e.g., "Parent", "Prev")
	StrongTargetType   string     `json:"strong_target_type"`   // Type of the strong owner
	AcyclicStrongProof []string   `json:"acyclic_strong_proof"` // Proof that residual strong graph is acyclic
	DowngradeSites     []WeakSite `json:"downgrade_sites,omitempty"`
	UpgradeSites       []WeakSite `json:"upgrade_sites,omitempty"`
	IsAtomic           bool       `json:"is_atomic"`
}

// WeakSite records an upgrade or downgrade operation in the CFG.
type WeakSite struct {
	Kind        string `json:"kind"`                  // "DOWNGRADE", "UPGRADE", "ELIDED_UPGRADE"
	Position    string `json:"position"`              // source position (file:line:col)
	Instruction string `json:"instruction,omitempty"` // SSA instruction string
	BlockID     int    `json:"block_id"`              // SSA BasicBlock index
	Field       string `json:"field,omitempty"`       // field name (e.g. "Parent", "Prev")
}

// ARCInfo describes an inferred reference-counted allocation.
type ARCInfo struct {
	RetainSites  []ARCSite `json:"retain_sites,omitempty"`
	ReleaseSites []ARCSite `json:"release_sites,omitempty"`
	AcyclicProof []string  `json:"acyclic_proof"`
	IsAtomic     bool      `json:"is_atomic"`
	ElidedSites  int       `json:"elided_sites"`
}

// ARCSite records a single retain or release operation in the CFG.
type ARCSite struct {
	Kind        string `json:"kind"`                  // "RETAIN", "RELEASE", "ELIDED_RETAIN", "ELIDED_RELEASE"
	Position    string `json:"position"`              // source position (file:line:col)
	Instruction string `json:"instruction,omitempty"` // SSA instruction string
	BlockID     int    `json:"block_id"`              // SSA BasicBlock index
	Variable    string `json:"variable,omitempty"`    // variable or value name
}

// DestructionPoint records an inferred deterministic destruction site in the CFG.
type DestructionPoint struct {
	Kind        string `json:"kind"`                  // "LAST_USE", "SCOPE_EXIT", "EARLY_RETURN", "DEFER", "PANIC_UNWIND"
	Position    string `json:"position"`              // source position (file:line:col)
	Instruction string `json:"instruction,omitempty"` // SSA instruction string
	BlockID     int    `json:"block_id"`              // SSA BasicBlock index
}

// RegionInfo describes an inferred memory region for an allocation.
type RegionInfo struct {
	RegionID     string `json:"region_id"`
	Scope        string `json:"scope"`                   // "CALLER", "FUNCTION", "LEXICAL", "REQUEST", "NESTED"
	OwningFunc   string `json:"owning_function"`         // function that owns the region lifecycle
	ParentRegion string `json:"parent_region,omitempty"` // parent region if nested
	DestroyPoint string `json:"destroy_point"`          // where the region is destroyed or reset
}

// EscapeResult captures cross-validation against the Go compiler's escape analysis.
type EscapeResult struct {
	Escapes       bool     `json:"escapes"`
	EscapeReasons []string `json:"escape_reasons,omitempty"`
	// GoCompilerSays is the raw compiler output line for this allocation site.
	GoCompilerSays string `json:"go_compiler_says,omitempty"`
}

// StrategySummary summarises memory strategy decisions for the report.
type StrategySummary struct {
	ByStrategy        map[MemoryStrategy]int `json:"by_strategy"`
	ByConfidence      map[Confidence]int     `json:"by_confidence"`
	ProvenStackCount       int                    `json:"proven_stack_count"`
	ProvenRegionCount      int                    `json:"proven_region_count"`
	ProvenUniqueOwnedCount int                    `json:"proven_unique_owned_count"`
	ProvenARCCount         int                    `json:"proven_arc_count"`
	ProvenWeakCount        int                    `json:"proven_weak_count"`
	ProvenImmortalCount    int                    `json:"proven_immortal_count"`
	TracingFallback        int                    `json:"tracing_fallback_count"`
	ProvenGCReduction float64                `json:"proven_gc_reduction_percent"`
	StrategyTimeMS    float64                `json:"strategy_time_ms"`
}

type TraceEvent struct {
	Step      int    `json:"step"`
	Operation string `json:"operation"`
	Function  string `json:"function,omitempty"`
	Position  string `json:"position,omitempty"`
	Detail    string `json:"detail"`
}

type Allocation struct {
	ID             string         `json:"id"`
	Package        string         `json:"package"`
	Function       string         `json:"function"`
	Position       string         `json:"position"`
	Kind           string         `json:"kind"`
	Type           string         `json:"type"`
	Classification Classification `json:"classification"`
	Ownership      Ownership      `json:"ownership"`
	Lifetime       Lifetime       `json:"lifetime"`
	SSAHeap        bool           `json:"ssa_heap"`
	CallerDepth    int            `json:"caller_depth"`
	RetainedBy     []string       `json:"retained_by,omitempty"`
	Reasons        []string       `json:"reasons"`
	Limitations    []string       `json:"limitations,omitempty"`
	Trace          []TraceEvent   `json:"trace,omitempty"`
}

type GraphNode struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Package  string `json:"package,omitempty"`
	Position string `json:"position,omitempty"`
}

type GraphEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Kind     string `json:"kind"`
	Position string `json:"position,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type ProgramGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type CallGraphSummary struct {
	Functions        int `json:"functions"`
	CallEdges        int `json:"call_edges"`
	DynamicCallEdges int `json:"dynamic_call_edges"`
	UnresolvedCalls  int `json:"unresolved_calls"`
}

type Summary struct {
	PackagesAnalyzed       int                    `json:"packages_analyzed"`
	FunctionsAnalyzed      int                    `json:"functions_analyzed"`
	AllocationsDiscovered  int                    `json:"allocations_discovered"`
	ByClassification       map[Classification]int `json:"by_classification"`
	PotentialGCReductionPC float64                `json:"potential_gc_reduction_percent"`
}

type Metrics struct {
	ThroughputAllocationsPerSecond float64 `json:"throughput_allocations_per_second"`
	AveragePackageLatencyMS        float64 `json:"average_package_latency_ms"`
	P95PackageLatencyMS            float64 `json:"p95_package_latency_ms"`
	P99PackageLatencyMS            float64 `json:"p99_package_latency_ms"`
	MemoryUsageBytes               uint64  `json:"memory_usage_bytes"`
	CPUUsageMS                     float64 `json:"cpu_usage_ms"`
	BinarySizeBytes                int64   `json:"binary_size_bytes"`
	CompilationTimeMS              float64 `json:"compilation_time_ms"`
	AnalysisTimeMS                 float64 `json:"analysis_time_ms"`
}

type Result struct {
	Version      string           `json:"version"`
	AnalysisMode string           `json:"analysis_mode"`
	GeneratedAt  time.Time        `json:"generated_at"`
	Patterns     []string         `json:"patterns"`
	Summary      Summary          `json:"summary"`
	Metrics      Metrics          `json:"metrics"`
	Allocations  []Allocation     `json:"allocations"`
	CallGraph    CallGraphSummary `json:"call_graph"`
	Graph        ProgramGraph     `json:"graph"`
	Disclaimers  []string         `json:"disclaimers"`
	Limitations  []string         `json:"limitations"`
	Warnings     []string         `json:"warnings,omitempty"`
	// v0.3 additions: proof-based memory strategy decisions.
	Decisions       []AllocationDecision `json:"decisions,omitempty"`
	StrategySummary *StrategySummary     `json:"strategy_summary,omitempty"`
}

type Config struct {
	Dir            string
	Patterns       []string
	IncludeTests   bool
	Trace          bool
	ClosedWorld    bool
	// v0.3: run memory-strategy analysis with proof engine.
	MemoryStrategy bool
	// v0.3: explain a specific allocation by ID.
	ExplainAlloc   string
	// v0.9.5: environment variables for cross-compilation (GOOS, GOARCH, CGO_ENABLED).
	Env            []string
	// v0.9.5: build flags (-tags, -mod, etc.).
	BuildFlags     []string
}
