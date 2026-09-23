package analyzer

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// UseSite records a single instruction where an allocation is referenced.
type UseSite struct {
	Instruction ssa.Instruction
	Block       *ssa.BasicBlock
	Index       int
	IsLastUse   bool
	IsMove      bool
	IsBorrow    bool
}

// LivenessReport contains the CFG liveness analysis results for an allocation value.
type LivenessReport struct {
	Value             ssa.Value
	AllocBlock        *ssa.BasicBlock
	UseSites          []UseSite
	LastUseSites      []UseSite
	DestructionPoints []DestructionPoint
	IsSingleOwner     bool
	HasConcurrentUse  bool
	HasEscaped        bool
}

// analyzeLiveness performs CFG-level liveness and use-def analysis on an allocation value.
func analyzeLiveness(val ssa.Value, fn *ssa.Function, fset *token.FileSet) LivenessReport {
	report := LivenessReport{
		Value:         val,
		IsSingleOwner: true,
	}

	if val == nil || fn == nil {
		report.IsSingleOwner = false
		return report
	}

	if instr, ok := val.(ssa.Instruction); ok && instr.Block() != nil {
		report.AllocBlock = instr.Block()
	} else if len(fn.Blocks) > 0 {
		report.AllocBlock = fn.Blocks[0]
	}

	referrers := val.Referrers()
	if referrers == nil || len(*referrers) == 0 {
		// Value is allocated but never used: destruction point is immediately in alloc block
		posStr := positionString(fset, val.Pos())
		blockID := 0
		if report.AllocBlock != nil {
			blockID = report.AllocBlock.Index
		}
		report.DestructionPoints = append(report.DestructionPoints, DestructionPoint{
			Kind:        "IMMEDIATE_UNUSED",
			Position:    posStr,
			Instruction: val.String(),
			BlockID:     blockID,
		})
		return report
	}

	// Map each instruction in fn to its (block, index)
	instrPos := make(map[ssa.Instruction]struct {
		block *ssa.BasicBlock
		index int
	})
	for _, block := range fn.Blocks {
		for idx, instr := range block.Instrs {
			instrPos[instr] = struct {
				block *ssa.BasicBlock
				index int
			}{block: block, index: idx}
		}
	}

	// Collect all use sites
	for _, ref := range *referrers {
		pos, exists := instrPos[ref]
		if !exists {
			continue
		}

		site := UseSite{
			Instruction: ref,
			Block:       pos.block,
			Index:       pos.index,
		}

		// Detect concurrency, global escape, or return in referrers
		switch r := ref.(type) {
		case *ssa.Go:
			report.HasConcurrentUse = true
			report.IsSingleOwner = false
		case *ssa.Send:
			report.HasConcurrentUse = true
			report.IsSingleOwner = false
		case *ssa.Return:
			report.HasEscaped = true
		case *ssa.Store:
			if r.Val == val {
				switch r.Addr.(type) {
				case *ssa.Global, *ssa.FieldAddr, *ssa.IndexAddr:
					report.IsSingleOwner = false
				}
			}
		case *ssa.Call:
			if b, ok := r.Call.Value.(*ssa.Builtin); ok && b.Name() == "append" {
				for _, arg := range r.Call.Args {
					if arg == val {
						report.IsSingleOwner = false
					}
				}
			}
		}

		report.UseSites = append(report.UseSites, site)
	}

	// Find the last use in each block that contains at least one use
	blockUses := make(map[*ssa.BasicBlock][]int) // block -> sorted list of use indices
	for _, site := range report.UseSites {
		blockUses[site.Block] = append(blockUses[site.Block], site.Index)
	}

	// Track blocks where destruction is already placed
	destroyedBlocks := make(map[*ssa.BasicBlock]bool)

	// Determine blocks where the value dies (no successor uses it)
	for block, indices := range blockUses {
		maxIdx := -1
		for _, idx := range indices {
			if idx > maxIdx {
				maxIdx = idx
			}
		}

		// Check if any successor block has reachable uses
		hasDownstreamUses := hasReachableUsesInSuccessors(block, blockUses, make(map[*ssa.BasicBlock]bool))

		if !hasDownstreamUses {
			lastInstr := block.Instrs[maxIdx]
			posStr := positionString(fset, lastInstr.Pos())
			kind := "LAST_USE"
			if _, isRet := block.Instrs[len(block.Instrs)-1].(*ssa.Return); isRet {
				kind = "PRE_RETURN"
			}

			report.LastUseSites = append(report.LastUseSites, UseSite{
				Instruction: lastInstr,
				Block:       block,
				Index:       maxIdx,
				IsLastUse:   true,
			})

			report.DestructionPoints = append(report.DestructionPoints, DestructionPoint{
				Kind:        kind,
				Position:    posStr,
				Instruction: lastInstr.String(),
				BlockID:     block.Index,
			})
			destroyedBlocks[block] = true
		}
	}

	// For every reachable exit block that was reached without passing through a destruction point,
	// add an EARLY_RETURN destruction point to ensure the Single-Destruction Invariant holds along all CFG paths.
	if report.AllocBlock != nil && !report.HasEscaped && report.IsSingleOwner && !report.HasConcurrentUse {
		exitBlocks := findExitBlocks(fn)
		for _, exit := range exitBlocks {
			if destroyedBlocks[exit] {
				continue
			}
			// Check if this exit is reachable from AllocBlock without traversing any destroyed block
			if canReachWithoutPassing(report.AllocBlock, exit, destroyedBlocks, make(map[*ssa.BasicBlock]bool)) {
				var retInstr ssa.Instruction
				if len(exit.Instrs) > 0 {
					retInstr = exit.Instrs[len(exit.Instrs)-1]
				}
				posStr := ""
				instrStr := "return"
				if retInstr != nil {
					posStr = positionString(fset, retInstr.Pos())
					instrStr = retInstr.String()
				}
				report.DestructionPoints = append(report.DestructionPoints, DestructionPoint{
					Kind:        "EARLY_RETURN",
					Position:    posStr,
					Instruction: instrStr,
					BlockID:     exit.Index,
				})
				destroyedBlocks[exit] = true
			}
		}
	}

	// Check for moves vs borrows across use sites
	if len(report.UseSites) > 1 {
		// If there are multiple uses, verify they are sequential (borrow/move) rather than conflicting
		for i := 0; i < len(report.UseSites)-1; i++ {
			curr := report.UseSites[i]
			next := report.UseSites[i+1]
			if curr.Block == next.Block && curr.Index < next.Index {
				// Successive use in same block: earlier use is a borrow
				report.UseSites[i].IsBorrow = true
			}
		}
	}

	return report
}

// findExitBlocks returns all basic blocks in fn with no successors (function returns/panics).
func findExitBlocks(fn *ssa.Function) []*ssa.BasicBlock {
	var exits []*ssa.BasicBlock
	for _, b := range fn.Blocks {
		if len(b.Succs) == 0 {
			exits = append(exits, b)
		}
	}
	return exits
}

// canReachWithoutPassing returns true if target can be reached from current without traversing any blocked block.
func canReachWithoutPassing(current, target *ssa.BasicBlock, blocked map[*ssa.BasicBlock]bool, visited map[*ssa.BasicBlock]bool) bool {
	if current == nil || visited[current] {
		return false
	}
	visited[current] = true

	if current == target {
		return true
	}
	if blocked[current] {
		return false
	}

	for _, succ := range current.Succs {
		if canReachWithoutPassing(succ, target, blocked, visited) {
			return true
		}
	}
	return false
}

// hasReachableUsesInSuccessors checks if any successor block of b has a use site.
func hasReachableUsesInSuccessors(b *ssa.BasicBlock, blockUses map[*ssa.BasicBlock][]int, visited map[*ssa.BasicBlock]bool) bool {
	if b == nil || visited[b] {
		return false
	}
	visited[b] = true

	for _, succ := range b.Succs {
		if len(blockUses[succ]) > 0 {
			return true
		}
		if hasReachableUsesInSuccessors(succ, blockUses, visited) {
			return true
		}
	}
	return false
}
