package main

import (
	"os"
	"testing"
	"time"
)

// Incremental room connectivity (exp-201) equals a full recomputation on
// every candidate FESS generates.
func TestRoomConnIncremental(t *testing.T) {
	checkRooms = true
	defer func() { checkRooms = false }()
	saved := NodeBudget
	NodeBudget = 1500
	defer func() { NodeBudget = saved }()
	for _, c := range []struct {
		file string
		idx  int
	}{{"Holland_81.xsb", 4}, {"Sven_1623.xsb", 1036}, {"Sasquatch_III_50.xsb", 36}, {"XSokoban_90.xsb", 28}, {"Microban_155.xsb", 40}} {
		data, err := os.ReadFile("levels/Large Test Suite/" + c.file)
		if err != nil {
			t.Skip(err)
		}
		lv, _ := parseLevel(splitCollection(data)[c.idx-1])
		s := newSolverUntil(lv, time.Now().Add(time.Minute))
		s.solve(time.Now().Add(time.Minute), hugeMem) // panics on any disagreement
	}
}

// exp-659: the lazy affected-room set gives the same totals.
func TestRoomConnIncrementalLazy(t *testing.T) {
	LazyChild = true
	defer func() { LazyChild = false }()
	TestRoomConnIncremental(t)
}

// exp-659: on huge boards (forward and pull), the -lazy-child room, plan
// phase and out-of-plan updates equal full recomputation on every candidate.
func TestLazyChildHuge(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	checkRooms, checkRegions, LazyChild = true, true, true
	defer func() { checkRooms, checkRegions, LazyChild = false, false, false }()
	saved := NodeBudget
	NodeBudget = 150
	defer func() { NodeBudget = saved }()
	for _, c := range []struct {
		file string
		idx  int
	}{{"Open Test Suite/Haikemono.xsb", 24}, {"Large Test Suite/Sasquatch II_50.xsb", 50}, {"Large Test Suite/Sven_1623.xsb", 198}, {"Large Test Suite/Sasquatch_III_50.xsb", 46}} {
		data, err := os.ReadFile("levels/" + c.file)
		if err != nil {
			t.Skip(err)
		}
		lv, _ := parseLevel(splitCollection(data)[c.idx-1])
		newSolverUntil(lv, time.Now().Add(time.Minute)).solve(time.Now().Add(time.Minute), hugeMem)
		newPullSolver(lv, time.Now().Add(time.Minute)).solve(time.Now().Add(time.Minute), hugeMem)
	}
}
