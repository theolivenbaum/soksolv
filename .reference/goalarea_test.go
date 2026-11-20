package main

import (
	"os"
	"testing"
	"time"

	"soksolv/reverse"
)

// Soundness: no position on a real solution may be flagged as a goal-area
// deadlock. Solutions come from the reverse search, which doesn't use the
// check; each push is replayed and the check run as forward search would.
func TestGoalAreaNeverFlagsSolutionPath(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	packs := map[string][]int{
		"levels/Large Test Suite/Microban_155.xsb":    nil,
		"levels/Large Test Suite/SokEvo_107.xsb":      nil,
		"levels/Large Test Suite/GrigrSpecial_40.xsb": nil,
		"levels/Large Test Suite/XSokoban_90.xsb":     {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 17, 19, 33, 38, 54, 63, 65, 73},
		"levels/Large Test Suite/Sven_1623.xsb":       {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25},
	}
	checked, flagsTried := 0, 0
	for path, idxs := range packs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skip("level files not available")
		}
		levels := splitCollection(data)
		if idxs == nil {
			for i := 1; i <= len(levels); i += 3 {
				idxs = append(idxs, i)
			}
		}
		for _, idx := range idxs {
			rows := levels[idx-1]
			rr, err := reverse.Solve(rows, time.Now().Add(500*time.Millisecond), 1<<30)
			if err != nil || !rr.Solved {
				continue
			}
			lv, err := parseLevel(rows)
			if err != nil {
				t.Fatal(err)
			}
			s := newSolver(lv)
			for _, b := range lv.Boxes {
				s.occ[b] = true
			}
			p := lv.Player
			dirOf := map[byte]int32{'u': s.dirs[0], 'd': s.dirs[1], 'l': s.dirs[2], 'r': s.dirs[3]}
			for _, c := range []byte(rr.Solution) {
				lower := c | 0x20
				d := dirOf[lower]
				p += d
				if c != lower { // push: the box at p moves to p+d
					s.occ[p], s.occ[p+d] = false, true
					flagsTried++
					if s.goalAreaDeadlock(p + d) {
						t.Fatalf("%s #%d: solution-path position flagged as goal-area deadlock", path, idx)
					}
				}
			}
			checked++
		}
	}
	t.Logf("checked %d solutions, %d pushes", checked, flagsTried)
	if checked < 50 {
		t.Fatalf("only %d solutions checked", checked)
	}
}
