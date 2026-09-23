package analyzer

import (
	"go/types"

	"golang.org/x/tools/go/ssa"
)

type flowFacts struct {
	returned        bool
	heapStored      bool
	globalStored    bool
	channelUse      bool
	goroutineUse    bool
	deferUse        bool
	interfaceUse    bool
	reflectionUse   bool
	unsafeUse       bool
	dynamicCall     bool
	dynamicDispatch bool
	externalCall    bool
	externalReturn  bool
	ffiUse          bool
	closureCapture  bool
	crossCall       bool
	borrowed        bool
	cycleRisk       bool
	unknownUse      bool
	callerDepth     int
	retainedBy      map[string]bool
}

func derivedFromGlobal(value ssa.Value, seen map[ssa.Value]bool) bool {
	if value == nil || seen[value] {
		return false
	}
	seen[value] = true
	switch value := value.(type) {
	case *ssa.Global:
		return true
	case *ssa.FieldAddr:
		return derivedFromGlobal(value.X, seen)
	case *ssa.IndexAddr:
		return derivedFromGlobal(value.X, seen)
	case *ssa.Slice:
		return derivedFromGlobal(value.X, seen)
	case *ssa.ChangeType:
		return derivedFromGlobal(value.X, seen)
	case *ssa.Convert:
		return derivedFromGlobal(value.X, seen)
	}
	return false
}

func derivedFrom(candidate, root ssa.Value, seen map[ssa.Value]bool) bool {
	if candidate == root {
		return true
	}
	if candidate == nil || seen[candidate] {
		return false
	}
	seen[candidate] = true
	switch value := candidate.(type) {
	case *ssa.FieldAddr:
		return derivedFrom(value.X, root, seen)
	case *ssa.IndexAddr:
		return derivedFrom(value.X, root, seen)
	case *ssa.Slice:
		return derivedFrom(value.X, root, seen)
	case *ssa.ChangeType:
		return derivedFrom(value.X, root, seen)
	case *ssa.Convert:
		return derivedFrom(value.X, root, seen)
	case *ssa.MakeInterface:
		return derivedFrom(value.X, root, seen)
	case *ssa.ChangeInterface:
		return derivedFrom(value.X, root, seen)
	case *ssa.Phi:
		for _, edge := range value.Edges {
			if derivedFrom(edge, root, seen) {
				return true
			}
		}
	}
	return false
}

func isUnsafePointer(t types.Type) bool {
	return t != nil && t.String() == "unsafe.Pointer"
}
