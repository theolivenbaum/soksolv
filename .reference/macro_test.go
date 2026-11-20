package main

import (
	"slices"
	"testing"
	"time"

	"soksolv/verify"
)

func TestMacroPreservesFinalPlayerComponents(t *testing.T) {
	// The test reads every stored move: store all of them (no move window).
	defer func(w int) { MoveWindow = w }(MoveWindow)
	MoveWindow = 0
	s := testSolver(t,
		"#############",
		"#     #     #",
		"# @$      . #",
		"#     #     #",
		"#############")
	box := s.lv.Boxes[0]
	// At the central door, the player can finish on either side, by first
	// pushing into the far room and walking around the box before returning.
	door := int32(3*s.lv.W + 7)
	s.occ[box] = true
	p := s.reach(s.lv.Player, s.mark)
	st := s.stamp
	id := s.add(s.lv.Boxes, p, -1, -1, -1, -1, nil, 0, s.dist[box], 0, -1)
	s.open(id, s.lv.Boxes, s.dist[box], st)
	components := map[int32]bool{}
	for _, m := range s.mv {
		_, to, _, _ := decodeMove(m)
		if to != door {
			continue
		}
		seq := s.boxPathTo(box, to, st, moveSide(m))
		if len(seq) == 0 {
			t.Fatal("generated macro cannot be reconstructed")
		}
		last := seq[len(seq)-1]
		s.occ[box], s.occ[to] = false, true
		np := s.reach(to-s.dirs[last.dir], s.mark2)
		components[np] = true
		s.occ[box], s.occ[to] = true, false
	}
	if len(components) != 2 {
		t.Fatalf("door endpoints=%v, want both player components", components)
	}
	// Returning a box to its original cell is legal. Applying such a macro
	// must restore the parent's occupancy despite from == to.
	child := make([]int32, 1)
	for _, m := range slices.Clone(s.mv) {
		_, to, _, _ := decodeMove(m)
		if to == box {
			s.apply(id, m, s.lv.Boxes, child, s.dist[box], st)
			if !s.occ[box] {
				t.Fatal("returning macro cleared parent's box")
			}
		}
	}
}

func TestMacroSolutionsReplay(t *testing.T) {
	for _, rows := range [][]string{
		{"#######", "#@ $ .#", "#######"},
		{"#############", "#     #     #", "# @$      . #", "#     #     #", "#############"},
		{"#########", "# .   . #", "#   $$@ #", "#       #", "#########"},
	} {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		res := s.solve(time.Now().Add(time.Second), hugeMem) // process-wide mapped memory counts: earlier tests leave heap behind
		if !res.Solved {
			t.Fatalf("not solved: %s: %v", res.Reason, rows)
		}
		if err := verify.Check(rows, res.Solution); err != nil {
			t.Fatalf("invalid macro solution: %v", err)
		}
	}
}

func TestMoveEncodingRetainsSideAndUsedFlag(t *testing.T) {
	for side := int32(0); side < 4; side++ {
		m := encodeMove(32767, 32767, 1, 32767, side) | moveUsed
		i, to, weight, r := decodeMove(m)
		if !slices.Equal([]int32{i, to, weight, int32(r), moveSide(m)}, []int32{32767, 32767, 1, 32767, side}) {
			t.Fatal("move encoding overflow")
		}
	}
}
