package main

import (
	"os"
	"testing"
	"time"

	"soksolv/cluster"
	"soksolv/full"
)

// The cluster proof search's fast player normalization must agree with a
// flood fill on every child (cluster.CheckFast panics otherwise). Runs the
// full search on dense levels where cluster checks fire constantly.
func TestClusterFastNormalization(t *testing.T) {
	cluster.CheckFast = true
	defer func() { cluster.CheckFast = false }()
	cases := map[string][]int{
		"levels/Large Test Suite/Sven_1623.xsb":      {196, 228, 1244, 1516, 1108, 300, 700},
		"levels/Large Test Suite/XSokoban_90.xsb":    {29, 39, 75},
		"levels/Large Test Suite/Sasquatch_V_50.xsb": {44, 28},
	}
	if testing.Short() { // fast tier: one level per pack
		cases = map[string][]int{
			"levels/Large Test Suite/Sven_1623.xsb":   {196},
			"levels/Large Test Suite/XSokoban_90.xsb": {29},
		}
	}
	for f, idxs := range cases {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Skip(err)
		}
		levels := splitCollection(data)
		for _, i := range idxs {
			_, err := full.Solve(levels[i-1], time.Now().Add(3*time.Second), 1<<30)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
