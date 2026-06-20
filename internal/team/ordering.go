package team

import (
	"sort"
	"strconv"
	"strings"
)

// GridCapacity returns the number of slots a grid layout ("CxR" e.g. "2x3") can
// display, or -1 for unlimited ("custom") and for any unparseable/invalid layout
// (lenient: an unknown layout never causes agents to be wrongly skipped). Mirrors
// the frontend gridCapacity (frontend/src/lib/types.ts).
func GridCapacity(layout string) int {
	if layout == "custom" {
		return -1
	}
	parts := strings.Split(layout, "x")
	if len(parts) != 2 {
		return -1
	}
	cols, err1 := strconv.Atoi(parts[0])
	rows, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || cols <= 0 || rows <= 0 {
		return -1
	}
	return cols * rows
}

// AgentsInOpenOrder returns the team's agents ordered for reopening, as a copy
// (the input is never mutated).
//
// Normally agents are sorted ascending by SlotIndex and their slots are kept as
// recorded. The slots are treated as ambiguous — and remapped to positional slots
// (0,1,2…) in the original array order — when either:
//   - two or more agents share a SlotIndex (e.g. legacy records written before
//     slot_index existed all defaulted to 0), or
//   - a SlotIndex is negative (corrupt/hand-edited data), which maps to no grid
//     slot yet would still spawn a PTY — a leak.
//
// This keeps reopened terminals from piling into one slot or leaking off-grid.
func AgentsInOpenOrder(agents []AgentConfig) []AgentConfig {
	out := make([]AgentConfig, len(agents))
	copy(out, agents)

	seen := make(map[int]bool, len(out))
	needsPositional := false
	for _, a := range out {
		if a.SlotIndex < 0 || seen[a.SlotIndex] {
			needsPositional = true
			break
		}
		seen[a.SlotIndex] = true
	}

	if needsPositional {
		for i := range out {
			out[i].SlotIndex = i
		}
		return out
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SlotIndex < out[j].SlotIndex
	})
	return out
}
