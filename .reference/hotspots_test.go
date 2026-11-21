package main

import (
	"testing"

	"soksolv/reach"
)

// Forward relaxed push reachability is independent of the reverse BFS used
// by the table. It also checks that blocked player-support squares matter.
func relaxedGoals(s *solver, start, wall int32) int {
	seen := make([]bool, len(s.lv.Wall))
	seen[start] = true
	q := []int32{start}
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		if s.lv.Goal[c] {
			n++
		}
		for _, d := range s.dirs {
			to, support := c+d, c-d
			if !s.lv.Wall[to] && !s.lv.Wall[support] && to != wall && support != wall && !seen[to] {
				seen[to] = true
				q = append(q, to)
			}
		}
	}
	return n
}

func TestHotspotsAgainstForwardReachability(t *testing.T) {
	if reach.Enabled {
		t.Skip("relaxed forward reachability is the spec of the relaxed tables; -exact-reach restricts trees to exact cells (exp-464)")
	}
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	h := newHotspotTable(s)
	for _, blocker := range s.fcell {
		row := h.row(s, blocker)
		for _, box := range s.fcell {
			if box != blocker {
				want := relaxedGoals(s, box, -1) > relaxedGoals(s, box, blocker)
				if got := hotspotHits(row, s.fidx[box]); got != want {
					t.Fatalf("blocker=%d box=%d: got %v want %v", blocker, box, got, want)
				}
			}
		}
	}
}

func TestHotspotIncrementalCount(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	h := newHotspotTable(s)
	var ref hotspotRefState
	boxes := s.lv.Boxes
	counts := h.counts(s, boxes)
	for i := range boxes {
		for _, target := range s.fcell {
			occupied := false
			for _, b := range boxes {
				if b == target {
					occupied = true
				}
			}
			if occupied {
				continue
			}
			child := append([]int32(nil), boxes...)
			child[i] = target
			if got, want := h.after(s, boxes, counts, i, target), hotspotCount(h.countsRef(&ref, s, child)); got != want {
				t.Fatalf("moved=%d target=%d: %d != %d", i, target, got, want)
			}
		}
	}
}
