package main

import (
	"os"
	"testing"
	"time"
)

// exp-530: every incremental child room score must equal a full recompute.
func TestFA2RoomsIncremental(t *testing.T) {
	fa2RoomsCheck = true
	defer func() { fa2RoomsCheck = false }()
	files := map[string][]int{
		"levels/Large Test Suite/Sven_1623.xsb":    {18, 23, 295, 1328, 1449},
		"levels/Large Test Suite/XSokoban_90.xsb":  {48},
		"levels/Large Test Suite/Holland_81.xsb":   {64},
		"levels/Large Test Suite/Sasquatch_50.xsb": {35},
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
			d := time.Now().Add(400 * time.Millisecond)
			fa2Solve(newSolverUntil(lv, d), d, hugeMem) // panics on a mismatch
		}
	}
}
