package main

import (
	"os"
	"slices"
	"testing"
	"time"

	"soksolv/reverse"
)

// Every position on a real solution can be pulled back to the start (undo
// the prefix), so pull-side deadlock checks must never flag one.
func TestPullDeadlockNeverOnSolutionPaths(t *testing.T) {
	stride := 3
	if testing.Short() { // fast tier: every 12th level
		stride = 12
	}
	checked, positions := 0, 0
	for _, f := range []string{"Microban_155.xsb", "XSokoban_90.xsb", "Microban II_135.xsb", "SokHard_163.xsb"} {
		data, err := os.ReadFile("levels/Large Test Suite/" + f)
		if err != nil {
			t.Skip(err)
		}
		for idx, rows := range splitCollection(data) {
			if idx%stride != 0 {
				continue
			}
			lv, err := parseLevel(rows)
			if err != nil {
				continue
			}
			rr, err := reverse.Solve(rows, time.Now().Add(300*time.Millisecond), 1<<30)
			if err != nil || !rr.Solved {
				continue
			}
			checked++
			s := newPullSolver(lv, time.Time{})
			dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
			boxes := slices.Clone(lv.Boxes)
			p := lv.Player
			check := func(step int) {
				positions++
				for _, b := range boxes {
					s.occ[b] = true
				}
				for _, b := range boxes {
					if s.pullFreezeDead(b) {
						t.Fatalf("%s #%d: position after move %d flagged pull-dead (box %d)", f, idx+1, step, b)
					}
				}
				for _, b := range boxes {
					s.occ[b] = false
				}
			}
			check(0)
			for i := 0; i < len(rr.Solution); i++ {
				c := rr.Solution[i]
				d := dirs[c|0x20]
				if c >= 'A' && c <= 'Z' {
					boxes[slices.Index(boxes, p+d)] = p + 2*d
					check(i)
				}
				p += d
			}
		}
	}
	t.Logf("%d solutions, %d positions, never flagged", checked, positions)
}
