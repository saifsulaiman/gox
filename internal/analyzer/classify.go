package analyzer

import "fmt"

func classify(kind string, ssaHeap bool, facts flowFacts) (Classification, Ownership, Lifetime, []string, []string) {
	limitations := []string{}
	if kind == "channel" {
		return RuntimeFallback, OwnershipConcurrent, LifetimeChannel,
			[]string{"Channels coordinate independently scheduled lifetimes and require runtime management in v0.2."}, limitations
	}
	if facts.ffiUse {
		return RuntimeFallback, OwnershipUnknown, LifetimeExternal,
			[]string{"The reference crosses a cgo, plugin, syscall, or foreign-function boundary whose retention behavior is not represented in Go SSA."},
			[]string{"An audited FFI ownership summary would be required before deterministic management."}
	}

	if facts.unsafeUse || facts.reflectionUse || facts.globalStored || facts.goroutineUse || facts.channelUse || facts.cycleRisk {
		reasons := []string{"The reference reaches behavior whose lifetime cannot be bounded safely by GOX v0.2."}
		switch {
		case facts.unsafeUse:
			reasons = append(reasons, "Unsafe-pointer conversion obscures aliasing and object provenance.")
		case facts.reflectionUse:
			reasons = append(reasons, "Reflection can retain or reproduce references outside the visible SSA flow.")
		case facts.globalStored:
			reasons = append(reasons, "The object is stored in global state and may live for the process lifetime.")
		case facts.goroutineUse:
			reasons = append(reasons, "The object crosses a goroutine boundary with an independently scheduled lifetime.")
		case facts.channelUse:
			reasons = append(reasons, "The object participates in channel communication and may outlive the sender.")
		case facts.cycleRisk:
			reasons = append(reasons, "A self-referential store creates a cycle risk for deterministic reference counting.")
		}
		if facts.returned {
			reasons = append(reasons, fmt.Sprintf("The reference propagates through %d caller boundary/boundaries.", facts.callerDepth))
		}
		ownership, lifetime := OwnershipShared, LifetimeRuntime
		if facts.globalStored {
			ownership, lifetime = OwnershipGlobal, LifetimeGlobal
		} else if facts.goroutineUse {
			ownership, lifetime = OwnershipConcurrent, LifetimeGoroutine
		} else if facts.channelUse {
			ownership, lifetime = OwnershipConcurrent, LifetimeChannel
		}
		return RuntimeFallback, ownership, lifetime, reasons, limitations
	}

	if facts.externalReturn {
		return RuntimeFallback, OwnershipTransferred, LifetimeExternal,
			[]string{"The reference can leave the analyzed program through an exported library API."},
			[]string{"Analyze a closed executable or provide an external ownership contract before optimizing this allocation."}
	}

	if facts.dynamicCall || facts.externalCall {
		reason := "The reference is passed to a dynamic or external call whose retention behavior is unknown."
		if facts.dynamicDispatch {
			reason = "Open-world dynamic dispatch may reach implementations outside the analyzed call graph."
		}
		return RuntimeFallback, OwnershipUnknown, LifetimeExternal,
			[]string{reason},
			[]string{"A future package-summary system could make this call analyzable."}
	}

	if facts.unknownUse {
		return Unknown, OwnershipUnknown, LifetimeUnknown,
			[]string{"GOX v0.2 encountered an SSA use it cannot model safely."},
			[]string{"The allocation remains runtime-managed until this SSA operation is modeled."}
	}

	if facts.heapStored || facts.closureCapture {
		reasons := []string{"The reference is retained by another heap object or closure."}
		if facts.closureCapture {
			reasons = append(reasons, "Closure lifetime can be deterministic, but it is not necessarily bounded by this function.")
		}
		if facts.interfaceUse {
			reasons = append(reasons, "Interface conversion introduces shared ownership but remains statically visible.")
		}
		if facts.returned {
			reasons = append(reasons, fmt.Sprintf("The retained object graph crosses %d caller boundary/boundaries and may share a caller-owned region.", facts.callerDepth))
			return RegionCandidate, OwnershipShared, LifetimeRegion, reasons,
				[]string{"Region destruction still requires whole-object-graph proof; context-insensitive call paths may overapproximate retention."}
		}
		return ARCCandidate, OwnershipShared, LifetimeShared, reasons,
			[]string{"Cycle detection is allocation-sensitive but not field-sensitive in v0.2; code generation must retain a tracing fallback."}
	}

	if facts.returned {
		return RegionCandidate, OwnershipTransferred, LifetimeCaller,
			[]string{fmt.Sprintf("The reference propagates through %d caller boundary/boundaries without a modeled concurrent, global, reflective, unsafe, FFI, or unknown escape.", facts.callerDepth), "A caller-owned region may bound the object's lifetime."},
			[]string{"Caller propagation is context-insensitive; code generation still requires a path-sensitive proof."}
	}

	if !ssaHeap {
		ownership := OwnershipUnique
		if facts.borrowed {
			ownership = OwnershipBorrowed
		}
		return StackSafe, ownership, LifetimeFunction,
			[]string{"The Go SSA builder represents this as frame-local storage.", "All modeled references remain bounded by the allocating function."}, limitations
	}

	reasons := []string{"All modeled references remain local, but the Go SSA builder represents the allocation as heap-resident."}
	if facts.deferUse {
		reasons = append(reasons, "Deferred use is bounded by function exit.")
	}
	if facts.crossCall {
		reasons = append(reasons, "Static callees were inspected and did not expose a modeled escape.")
	}
	ownership := OwnershipUnique
	if facts.borrowed {
		ownership = OwnershipBorrowed
	}
	return PotentialStack, ownership, LifetimeFunction, reasons,
		[]string{"Revalidation against the compiler's escape rationale is required before changing allocation behavior."}
}
