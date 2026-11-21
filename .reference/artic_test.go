package main

import (
	"os"
	"testing"
	"time"
)

// Every incremental child region count must equal a full flood fill.
func TestIncrementalRegionsMatchFlood(t *testing.T) {
	checkRegions = true
	defer func() { checkRegions = false }()
	files := map[string][]int{
		"levels/Large Test Suite/Microban_155.xsb":    {1, 5, 9, 33, 77, 101, 145},
		"levels/Large Test Suite/XSokoban_90.xsb":     {1, 10, 23, 29, 50, 90},
		"levels/Large Test Suite/Sven_1623.xsb":       {7, 300, 1001},
		"levels/Large Test Suite/Sasquatch_50.xsb":    {29},
		"levels/Large Test Suite/GrigrSpecial_40.xsb": {3, 17},
	}
	for path, idxs := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skip("level files not available")
		}
		levels := splitCollection(data)
		for _, i := range idxs {
			lv, err := parseLevel(levels[i-1])
			if err != nil {
				t.Fatal(err)
			}
			d := time.Now().Add(300 * time.Millisecond)
			newSolverUntil(lv, d).solve(d, hugeMem) // panics on a mismatch
		}
	}
}
