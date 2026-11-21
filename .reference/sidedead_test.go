package main

import (
	"os"
	"slices"
	"soksolv/cluster"
	"testing"
	"time"

	"soksolv/reverse"
	"soksolv/sidedead"
)

// No state on a real solution may be side-dead.
func TestSideDeadNeverOnSolutionPaths(t *testing.T) {
	stride := 3
	if testing.Short() { // fast tier: every 12th level
		stride = 12
	}
	checked, states := 0, 0
	for _, f := range []string{"Microban_155.xsb", "XSokoban_90.xsb", "Microban II_135.xsb"} {
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
			sd := sidedead.New(lv.Wall, lv.Goal, lv.W)
			dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
			boxes := slices.Clone(lv.Boxes)
			p := lv.Player
			check := func(at int) {
				states++
				for _, c := range boxes {
					if !lv.Goal[c] && sd.Dead(c, p) {
						t.Fatalf("%s #%d: box %d flagged side-dead at move %d of a real solution", f, idx+1, c, at)
					}
				}
			}
			check(0)
			for i := 0; i < len(rr.Solution); i++ {
				c := rr.Solution[i]
				d := dirs[c|0x20]
				if c >= 'A' && c <= 'Z' {
					boxes[slices.Index(boxes, p+d)] = p + 2*d
				}
				p += d
				check(i + 1)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d levels checked", checked)
	}
	t.Logf("%d solutions, %d states, never flagged", checked, states)
}

// No push on a real solution may form a pair that is dead from the player's
// side.
func TestPairSideNeverOnSolutionPaths(t *testing.T) {
	stride := 3
	if testing.Short() { // fast tier: every 12th level
		stride = 12
	}
	checked, pushes := 0, 0
	for _, f := range []string{"Microban_155.xsb", "XSokoban_90.xsb", "Microban II_135.xsb"} {
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
			s := newSolver(lv)
			s.pairOffsets()
			s.dpair = make([]int8, len(lv.Wall)*len(s.poff))
			dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
			boxes := slices.Clone(lv.Boxes)
			p := lv.Player
			for i := 0; i < len(rr.Solution); i++ {
				c := rr.Solution[i]
				d := dirs[c|0x20]
				if c >= 'A' && c <= 'Z' {
					boxes[slices.Index(boxes, p+d)] = p + 2*d
					for _, b := range boxes {
						s.occ[b] = true
					}
					pushes++
					if s.pairSideDeadlock(p+2*d, p+d) {
						t.Fatalf("%s #%d: push %d formed a pair flagged dead from the player's side", f, idx+1, i)
					}
					for _, b := range boxes {
						s.occ[b] = false
					}
				}
				p += d
			}
		}
	}
	t.Logf("%d solutions, %d pushes, never flagged", checked, pushes)
}

// No push on a real solution may form a cluster proven dead.
func TestClusterNeverOnSolutionPaths(t *testing.T) {
	stride := 4
	if testing.Short() { // fast tier: every 16th level
		stride = 16
	}
	checked, pushes := 0, 0
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
			s := newSolver(lv)
			dead := make([]bool, len(lv.Wall))
			for c, d := range s.dist {
				dead[c] = d < 0
			}
			ck := cluster.NewChecker(cluster.NewBoard(lv.Wall, lv.Goal, dead, lv.W))
			occ := make([]bool, len(lv.Wall))
			dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
			boxes := slices.Clone(lv.Boxes)
			p := lv.Player
			for i := 0; i < len(rr.Solution); i++ {
				c := rr.Solution[i]
				d := dirs[c|0x20]
				if c >= 'A' && c <= 'Z' {
					boxes[slices.Index(boxes, p+d)] = p + 2*d
					for _, b := range boxes {
						occ[b] = true
					}
					pushes++
					if ck.Dead(occ, p+2*d, p+d, nil) {
						t.Fatalf("%s #%d: push %d formed a cluster proven dead", f, idx+1, i)
					}
					for _, b := range boxes {
						occ[b] = false
					}
				}
				p += d
			}
		}
	}
	t.Logf("%d solutions, %d pushes, never flagged", checked, pushes)
}
