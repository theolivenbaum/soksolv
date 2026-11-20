package main

import (
	"os"
	"testing"
	"time"
)

// TestMoveAccessBitsMatchScan (exp-419): the row-bitset moveAccess equals the
// per-box scan (mobility, explorer, fact multiset) on every candidate of
// forward, novelty (recording facts) and pull searches.
func TestMoveAccessBitsMatchScan(t *testing.T) {
	checkAccess, checkRegions = true, true
	defer func() { checkAccess, checkRegions = false, false }()
	saved := NodeBudget
	NodeBudget = 800
	if testing.Short() {
		NodeBudget = 300
	}
	defer func() { NodeBudget = saved }()
	for _, c := range []struct {
		file string
		idx  int
	}{{"Holland_81.xsb", 4}, {"Sven_1623.xsb", 295}, {"Sasquatch_VII_50.xsb", 49}, {"XSokoban_90.xsb", 28}, {"Microban_155.xsb", 40}} {
		data, err := os.ReadFile("levels/Large Test Suite/" + c.file)
		if err != nil {
			t.Skip(err)
		}
		lv, _ := parseLevel(splitCollection(data)[c.idx-1])
		for mode := 0; mode < 3; mode++ {
			dl := time.Now().Add(time.Minute)
			var s *solver
			if mode == 2 {
				s = newPullSolver(lv, dl)
			} else {
				s = newSolverUntil(lv, dl)
				if mode == 1 {
					s.novelty = 100
				}
			}
			s.solve(dl, hugeMem) // panics on any disagreement
			t.Logf("%s %d mode %d plan %v", c.file, c.idx, mode, s.plan != nil)
		}
	}
}
