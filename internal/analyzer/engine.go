package analyzer

import (
	"cmp"
	"fmt"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/tools/go/ssa"
)

type allocationSite struct {
	id       string
	value    ssa.Value
	fn       *ssa.Function
	pkg      *ssa.Package
	kind     string
	ssaHeap  bool
	pos      string
	typeName string
}

type ownershipEngine struct {
	fset        *token.FileSet
	callGraph   callGraphIndex
	graph       *graphBuilder
	sites       []*allocationSite
	siteByValue map[ssa.Value]*allocationSite
	trace       bool
	closedWorld bool
}

func newOwnershipEngine(fset *token.FileSet, callGraph callGraphIndex, graph *graphBuilder, trace, closedWorld bool) *ownershipEngine {
	return &ownershipEngine{
		fset: fset, callGraph: callGraph, graph: graph,
		siteByValue: make(map[ssa.Value]*allocationSite), trace: trace, closedWorld: closedWorld,
	}
}

func (engine *ownershipEngine) discover(pkg *ssa.Package, function *ssa.Function, packageDir string) {
	ordinal := 0
	for _, block := range function.Blocks {
		for _, instruction := range block.Instrs {
			value, kind, ssaHeap, ok := allocationValue(instruction)
			if !ok {
				continue
			}
			ordinal++
			position := engine.fset.Position(instruction.Pos())
			relative := position.Filename
			if packageDir != "" && position.IsValid() {
				if rel, err := filepath.Rel(packageDir, position.Filename); err == nil && !strings.HasPrefix(rel, "..") {
					relative = filepath.ToSlash(rel)
				}
			}
			identity := fmt.Sprintf("%s|%s|%s:%d:%d|%s|%d", pkg.Pkg.Path(), function.String(), relative, position.Line, position.Column, kind, ordinal)
			site := &allocationSite{
				id: stableID("alloc", identity), value: value, fn: function, pkg: pkg,
				kind: kind, ssaHeap: ssaHeap, pos: positionString(engine.fset, instruction.Pos()),
				typeName: value.Type().String(),
			}
			engine.sites = append(engine.sites, site)
			engine.siteByValue[value] = site
			engine.graph.addNode(GraphNode{
				ID: site.id, Kind: "allocation", Label: kind + " " + site.typeName,
				Package: pkg.Pkg.Path(), Position: site.pos,
			})
			functionID := engine.callGraph.function[function]
			engine.graph.addEdge(GraphEdge{From: functionID, To: site.id, Kind: "allocates", Position: site.pos})
		}
	}
}

func (engine *ownershipEngine) analyzeAll() ([]Allocation, map[string]time.Duration) {
	allocations := make([]Allocation, 0, len(engine.sites))
	durations := make(map[string]time.Duration)
	for _, site := range engine.sites {
		started := time.Now()
		allocations = append(allocations, engine.analyze(site))
		durations[site.pkg.Pkg.Path()] += time.Since(started)
	}
	cyclicAllocations := engine.graph.retentionCycles()
	for index := range allocations {
		if !cyclicAllocations[allocations[index].ID] {
			continue
		}
		allocations[index].Classification = RuntimeFallback
		if allocations[index].Ownership != OwnershipConcurrent && allocations[index].Ownership != OwnershipGlobal {
			allocations[index].Ownership = OwnershipShared
		}
		allocations[index].Lifetime = LifetimeRuntime
		allocations[index].Reasons = append(allocations[index].Reasons,
			"The allocation participates in a strongly connected component of the container-retention graph.",
			"Deterministic reference counting cannot reclaim this cycle without an additional cycle strategy.",
		)
		allocations[index].Limitations = append(allocations[index].Limitations, "The cycle is derived from visible SSA retention edges; hidden runtime edges may add more cycles.")
	}
	slices.SortFunc(allocations, func(a, b Allocation) int {
		if c := cmp.Compare(a.Position, b.Position); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return allocations, durations
}

func (engine *ownershipEngine) analyze(site *allocationSite) Allocation {
	facts := flowFacts{retainedBy: make(map[string]bool)}
	seen := make(map[ssa.Value]bool)
	var trace []TraceEvent
	addTrace := func(operation string, instruction ssa.Instruction, detail string) {
		if !engine.trace {
			return
		}
		event := TraceEvent{Step: len(trace) + 1, Operation: operation, Detail: detail}
		if instruction != nil {
			event.Position = positionString(engine.fset, instruction.Pos())
			if parent := instruction.Parent(); parent != nil {
				event.Function = parent.String()
			}
		}
		trace = append(trace, event)
	}

	var visit func(ssa.Value, int)
	var inspectCall func(ssa.CallInstruction, ssa.Value, ssa.Value, int)

	retainIn := func(containerValue ssa.Value, kind string, instruction ssa.Instruction, depth int) {
		containers := engine.allocationSites(containerValue, make(map[ssa.Value]bool))
		if len(containers) == 0 {
			facts.heapStored = true
			addTrace("retention", instruction, "destination is not provably frame-local")
			return
		}
		for _, container := range containers {
			if container == site {
				facts.cycleRisk = true
				facts.heapStored = true
				addTrace("cycle", instruction, "allocation retains itself through "+kind)
				continue
			}
			facts.heapStored = true
			facts.retainedBy[container.id] = true
			engine.graph.addEdge(GraphEdge{
				From: site.id, To: container.id, Kind: kind,
				Position: positionString(engine.fset, instruction.Pos()), Detail: "container retains allocation",
			})
			addTrace("retention", instruction, "retained by "+container.id+" via "+kind)
			visit(container.value, depth)
		}
	}

	propagateReturn := func(ret *ssa.Return, value ssa.Value, depth int) {
		facts.returned = true
		if depth+1 > facts.callerDepth {
			facts.callerDepth = depth + 1
		}
		function := ret.Parent()
		addTrace("return", ret, "reference crosses return boundary from "+function.String())
		if engine.externallyCallable(function) {
			facts.externalReturn = true
			addTrace("external_boundary", ret, "exported library API may return the reference to code outside the analyzed program")
		}
		for resultIndex, result := range ret.Results {
			if result != value && !derivedFrom(result, value, make(map[ssa.Value]bool)) {
				continue
			}
			for _, caller := range engine.callGraph.callers[function] {
				callValue, ok := caller.(ssa.Value)
				if !ok {
					continue
				}
				callerFunction := caller.Parent()
				if callerFunction != nil {
					engine.graph.addEdge(GraphEdge{
						From: site.id, To: engine.functionNode(callerFunction), Kind: "lifetime_return",
						Position: positionString(engine.fset, caller.Pos()), Detail: fmt.Sprintf("return value %d", resultIndex),
					})
				}
				if len(ret.Results) == 1 {
					visit(callValue, depth+1)
					continue
				}
				if refs := callValue.Referrers(); refs != nil {
					for _, ref := range *refs {
						if extract, ok := ref.(*ssa.Extract); ok && extract.Index == resultIndex {
							visit(extract, depth+1)
						}
					}
				}
			}
		}
	}

	inspectCall = func(callInstruction ssa.CallInstruction, value, result ssa.Value, depth int) {
		common := callInstruction.Common()
		if common == nil {
			facts.unknownUse = true
			addTrace("unknown_call", callInstruction, "call has no SSA CallCommon")
			return
		}
		if builtin, ok := common.Value.(*ssa.Builtin); ok {
			switch builtin.Name() {
			case "append":
				for index, argument := range common.Args {
					if argument != value && !derivedFrom(argument, value, make(map[ssa.Value]bool)) {
						continue
					}
					if index == 0 {
						addTrace("alias", callInstruction, "append result may alias the input backing array")
						visit(result, depth)
					} else {
						retainIn(common.Args[0], "append_element", callInstruction, depth)
					}
				}
			case "copy", "len", "cap", "delete", "clear", "print", "println", "close", "complex", "real", "imag":
				addTrace("builtin", callInstruction, builtin.Name()+" does not retain the reference")
			default:
				facts.unknownUse = true
				addTrace("unknown_builtin", callInstruction, builtin.Name())
			}
			return
		}

		targets := append([]*ssa.Function(nil), engine.callGraph.targets[callInstruction]...)
		if static := common.StaticCallee(); static != nil {
			targets = appendUniqueFunction(targets, static)
		}
		sortCallTargets(targets)
		if common.StaticCallee() == nil {
			facts.dynamicDispatch = true
			addTrace("dynamic_dispatch", callInstruction, fmt.Sprintf("CHA found %d possible target(s)", len(targets)))
			if !engine.closedWorld || len(targets) == 0 {
				facts.dynamicCall = true
			}
		}
		if len(targets) == 0 {
			facts.dynamicCall = true
			addTrace("unresolved_call", callInstruction, common.Description())
			return
		}

		for _, target := range targets {
			facts.crossCall = true
			if isReflectionFunction(target) {
				facts.reflectionUse = true
			}
			if isUnsafeFunction(target) {
				facts.unsafeUse = true
			}
			if isFFIFunction(target) {
				facts.ffiUse = true
			}
			if len(target.Blocks) == 0 {
				facts.externalCall = true
				addTrace("external_call", callInstruction, target.String()+" has no analyzable SSA body")
				continue
			}

			matched := false
			if common.IsInvoke() && (common.Value == value || derivedFrom(common.Value, value, make(map[ssa.Value]bool))) {
				if len(target.Params) > 0 {
					matched = true
					facts.borrowed = true
					visit(target.Params[0], depth)
				}
			}
			parameterOffset := 0
			if common.IsInvoke() {
				parameterOffset = 1
			}
			for index, argument := range common.Args {
				if argument != value && !derivedFrom(argument, value, make(map[ssa.Value]bool)) {
					continue
				}
				parameterIndex := index + parameterOffset
				if parameterIndex >= len(target.Params) {
					facts.unknownUse = true
					addTrace("parameter_mismatch", callInstruction, target.String())
					continue
				}
				matched = true
				facts.borrowed = true
				addTrace("borrow", callInstruction, "reference passed to "+target.String())
				visit(target.Params[parameterIndex], depth)
			}
			if !matched && common.Value != value {
				facts.unknownUse = true
				addTrace("unmapped_call_use", callInstruction, "could not map reference to a target parameter")
			}
		}
	}

	visit = func(value ssa.Value, depth int) {
		if value == nil || seen[value] {
			return
		}
		seen[value] = true
		refs := value.Referrers()
		if refs == nil {
			facts.unknownUse = true
			addTrace("unknown_referrers", nil, "SSA value has no referrer list")
			return
		}
		for _, ref := range *refs {
			switch use := ref.(type) {
			case *ssa.DebugRef:
				continue
			case *ssa.Return:
				propagateReturn(use, value, depth)
			case *ssa.Store:
				if use.Val != value {
					continue
				}
				if derivedFromGlobal(use.Addr, make(map[ssa.Value]bool)) {
					facts.globalStored = true
					addTrace("global_store", use, "reference is retained by global state")
				} else {
					retainIn(use.Addr, "container_store", use, depth)
				}
			case *ssa.Send:
				facts.channelUse = true
				addTrace("channel", use, "reference crosses a channel operation")
			case *ssa.Go:
				facts.goroutineUse = true
				addTrace("goroutine", use, "reference crosses a goroutine boundary")
				inspectCall(use, value, nil, depth)
			case *ssa.Defer:
				facts.deferUse = true
				addTrace("defer", use, "reference remains live until function exit")
				inspectCall(use, value, nil, depth)
			case *ssa.Call:
				inspectCall(use, value, use, depth)
			case *ssa.MakeInterface:
				facts.interfaceUse = true
				addTrace("alias", use, "reference is boxed in an interface")
				visit(use, depth)
			case *ssa.MakeClosure:
				facts.closureCapture = true
				addTrace("closure_capture", use, "reference is captured by a closure")
				retainIn(use, "closure_capture", use, depth)
				visit(use, depth)
			case *ssa.Phi:
				addTrace("alias", use, "reference participates in an SSA phi")
				visit(use, depth)
			case *ssa.ChangeType:
				visit(use, depth)
			case *ssa.Convert:
				if isUnsafePointer(use.Type()) || isUnsafePointer(value.Type()) {
					facts.unsafeUse = true
					addTrace("unsafe", use, "conversion crosses unsafe.Pointer")
				}
				visit(use, depth)
			case *ssa.ChangeInterface:
				facts.interfaceUse = true
				visit(use, depth)
			case *ssa.TypeAssert, *ssa.Extract, *ssa.FieldAddr, *ssa.IndexAddr, *ssa.Slice:
				if derived, ok := ref.(ssa.Value); ok {
					addTrace("alias", use, fmt.Sprintf("reference flows through %T", use))
					visit(derived, depth)
				}
			case *ssa.Field, *ssa.Index, *ssa.UnOp:
				// Loading from an object does not retain the object's storage.
			case *ssa.MapUpdate:
				if use.Value == value || use.Key == value {
					retainIn(use.Map, "map_entry", use, depth)
				}
			case *ssa.Select:
				for _, state := range use.States {
					if state.Chan == value || state.Send == value || derivedFrom(state.Send, value, make(map[ssa.Value]bool)) {
						facts.channelUse = true
						addTrace("channel", use, "reference crosses a select case")
					}
				}
			case *ssa.Panic:
				facts.dynamicCall = true
				addTrace("panic", use, "recover creates an implicit open retention path")
			case *ssa.Lookup, *ssa.Range, *ssa.Next, *ssa.If, *ssa.Jump, *ssa.RunDefers:
				// Consuming operations that do not retain the source storage.
			default:
				facts.unknownUse = true
				addTrace("unknown_ssa", use, fmt.Sprintf("unmodeled instruction %T", use))
			}
		}
	}

	allocationInstruction, _ := site.value.(ssa.Instruction)
	addTrace("allocation", allocationInstruction, "begin ownership analysis for "+site.id)
	visit(site.value, 0)
	classification, ownership, lifetime, reasons, limitations := classify(site.kind, site.ssaHeap, facts)
	retainedBy := make([]string, 0, len(facts.retainedBy))
	for id := range facts.retainedBy {
		retainedBy = append(retainedBy, id)
	}
	slices.Sort(retainedBy)
	return Allocation{
		ID: site.id, Package: site.pkg.Pkg.Path(), Function: site.fn.String(), Position: site.pos,
		Kind: site.kind, Type: site.typeName, Classification: classification,
		Ownership: ownership, Lifetime: lifetime, SSAHeap: site.ssaHeap,
		CallerDepth: facts.callerDepth, RetainedBy: retainedBy,
		Reasons: reasons, Limitations: limitations, Trace: trace,
	}
}

func (engine *ownershipEngine) allocationSites(value ssa.Value, seen map[ssa.Value]bool) []*allocationSite {
	if value == nil || seen[value] {
		return nil
	}
	seen[value] = true
	if site := engine.siteByValue[value]; site != nil {
		return []*allocationSite{site}
	}
	var values []ssa.Value
	switch value := value.(type) {
	case *ssa.FieldAddr:
		values = []ssa.Value{value.X}
	case *ssa.IndexAddr:
		values = []ssa.Value{value.X}
	case *ssa.Slice:
		values = []ssa.Value{value.X}
	case *ssa.ChangeType:
		values = []ssa.Value{value.X}
	case *ssa.Convert:
		values = []ssa.Value{value.X}
	case *ssa.MakeInterface:
		values = []ssa.Value{value.X}
	case *ssa.ChangeInterface:
		values = []ssa.Value{value.X}
	case *ssa.Phi:
		values = value.Edges
	}
	unique := make(map[*allocationSite]bool)
	var sites []*allocationSite
	for _, source := range values {
		for _, site := range engine.allocationSites(source, seen) {
			if !unique[site] {
				unique[site] = true
				sites = append(sites, site)
			}
		}
	}
	slices.SortFunc(sites, func(a, b *allocationSite) int { return cmp.Compare(a.id, b.id) })
	return sites
}

func (engine *ownershipEngine) externallyCallable(function *ssa.Function) bool {
	if function == nil || function.Pkg == nil || function.Pkg.Pkg == nil {
		return function != nil && token.IsExported(function.Name())
	}
	if function.Pkg.Pkg.Name() == "main" {
		return false
	}
	object := function.Object()
	return object != nil && object.Exported()
}

func (engine *ownershipEngine) functionNode(function *ssa.Function) string {
	if id := engine.callGraph.function[function]; id != "" {
		return id
	}
	return engine.callGraph.addFunctionNode(function, engine.fset, engine.graph, false)
}

func isReflectionFunction(function *ssa.Function) bool {
	path := functionPackagePath(function)
	return path == "reflect" || strings.HasPrefix(path, "reflect/")
}

func isUnsafeFunction(function *ssa.Function) bool {
	return functionPackagePath(function) == "unsafe" || strings.Contains(function.String(), "unsafe.")
}

func isFFIFunction(function *ssa.Function) bool {
	path := functionPackagePath(function)
	name := function.String()
	return isFFIBoundary(path, name)
}

func isFFIBoundary(path, name string) bool {
	return path == "C" || path == "plugin" || strings.HasPrefix(path, "plugin/") ||
		strings.Contains(name, "runtime.cgocall") || strings.Contains(name, "syscall.Syscall")
}

func functionPackagePath(function *ssa.Function) string {
	if function == nil || function.Pkg == nil || function.Pkg.Pkg == nil {
		return ""
	}
	return function.Pkg.Pkg.Path()
}
