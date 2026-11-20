package full

import (
	"fmt"
	"os"
	"testing"
	"time"

	"soksolv/verify"
)

// The worklist survivors fixpoint must equal the reference on every call.
func TestSurvivorsAgree(t *testing.T) {
	calls, bad := 0, 0
	checkSurvivors = func(ga *goalArea, zones [][]int32, wall []bool, goals []int32, dirs [4]int32) {
		calls++
		saved := append([]bool(nil), ga.box...)
		ref := ga.survivorsOld(wall, goals, dirs)
		copy(ga.box, saved)
		if fmt.Sprint(ref) != fmt.Sprint(zones) {
			bad++
			if bad < 3 {
				t.Errorf("survivors differ: new %v ref %v", zones, ref)
			}
		}
	}
	defer func() { checkSurvivors = nil }()
	cases := map[string][]int{
		"../levels/Large Test Suite/XSokoban_90.xsb": {1, 29, 39, 75},
		"../levels/Large Test Suite/Sven_1623.xsb":   {196, 1244, 300},
		"../levels/Large Test Suite/SokHard_163.xsb": {100},
	}
	if testing.Short() { // fast tier: one level per pack
		cases = map[string][]int{
			"../levels/Large Test Suite/XSokoban_90.xsb": {29},
			"../levels/Large Test Suite/Sven_1623.xsb":   {196},
		}
	}
	for f, idxs := range cases {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Skip(err)
		}
		levels := verify.SplitLevels(data)
		for _, i := range idxs {
			Solve(levels[i-1], time.Now().Add(2*time.Second), 1<<30)
		}
	}
	t.Logf("%d survivor computations compared, %d differ", calls, bad)
	if calls == 0 {
		t.Fatal("no survivor computations ran")
	}
}
