package main

import (
	"slices"
	"testing"
)

// TestLaneTable: every default lane declares its memory (exp-640), and the
// default portfolio is the eight lanes of docs/LANES.md (one thread each).
func TestLaneTable(t *testing.T) {
	defer func(v bool) { festBLane = v }(festBLane)
	festBLane = true // registered (default true) in main, not in tests
	sh := laneShares("all")
	var names []string
	for n := range sh {
		names = append(names, n)
		if m, ok := laneTable[n]; !ok || m.budget == nil || len(m.slots) == 0 {
			t.Errorf("lane %s: no memory declaration", n)
		}
	}
	slices.Sort(names)
	want := []string{"fa2", "hub-gen", "hub-pull", "macro", "novelty", "pull", "reverse", "toward"}
	if !slices.Equal(names, want) {
		t.Fatalf("default lanes %v, want %v", names, want)
	}
}
