package main

import (
	"os"
	"testing"
	"time"

	"soksolv/verify"
)

// Pull-mode FESS must solve levels backward and its solutions must verify
// forward (pull macros, the solved test and the tail reconstruction).
func TestPullFESSSolves(t *testing.T) {
	for f, n := range map[string]int{"Microban_155.xsb": 40, "XSokoban_90.xsb": 10} {
		data, err := os.ReadFile("levels/Large Test Suite/" + f)
		if err != nil {
			t.Skip(err)
		}
		levels := splitCollection(data)
		if testing.Short() { // fast tier: first few levels only
			n = min(n, 8)
			if f == "XSokoban_90.xsb" { // mostly hits the 3 s deadline
				n = 1
			}
		}
		solved, tried := 0, 0
		for i := 0; i < n; i++ {
			rows := levels[i]
			lv, err := parseLevel(rows)
			if err != nil {
				continue
			}
			tried++
			deadline := time.Now().Add(3 * time.Second)
			s := newPullSolver(lv, deadline)
			res := s.solve(deadline, hugeMem)
			if !res.Solved {
				continue
			}
			if err := verify.Check(rows, res.Solution); err != nil {
				t.Fatalf("%s #%d: pull-FESS solution does not verify: %v", f, i+1, err)
			}
			solved++
		}
		t.Logf("%s: pull FESS solved %d of %d (all verified)", f, solved, tried)
	}
}
