package main

import (
	"os"
	"slices"
	"soksolv/meet"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetargetMultipleEpochsWindowIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: skipped in fast tier (go test -short); see research/exp-371")
	}
	// StallFinish is wall-clock driven (finish-from-best after a stall); under load it
	// fired during the 25-node resume and "solved" with 0 expansions (exp-377).
	savedReuse, savedWindow, savedDebug, savedStall := RetargetReuse, MoveWindow, DebugOpen, StallFinish
	defer func() {
		RetargetReuse = savedReuse
		MoveWindow = savedWindow
		DebugOpen = savedDebug
		StallFinish = savedStall
	}()
	StallFinish = false
	RetargetReuse = true
	MoveWindow = 3
	data, e := os.ReadFile("levels/Large Test Suite/XSokoban_90.xsb")
	if e != nil {
		t.Fatal(e)
	}
	rows := splitCollection(data)[7]
	lv, e := parseLevel(rows)
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Minute)
	var budget, tracked atomic.Uint64
	goal := &meet.Target{Boxes: append([]int32(nil), lv.Boxes...), Player: lv.Player}
	s := newTowardReal(rows, lv, goal, deadline, nil, &budget, &tracked, 2, 200)
	// Initial target is the actual goals so initialization does real work.
	goal.Boxes = append([]int32(nil), s.goals...)
	s.setTarget(goal.Boxes)
	s.tgtFull = goal
	s.towardRun(deadline, hugeMem)
	n := len(s.player)
	if n < 4 {
		t.Fatalf("insufficient initial tree: %d", n)
	}
	type k struct{ i, to, side int32 }
	totalWindows, transpositions := 0, 0
	for epoch := 0; epoch < 2; epoch++ {
		n = len(s.player)
		var target []int32
		for shift := epoch; shift+len(lv.Boxes) <= len(s.fcell); shift++ {
			candidate := append([]int32(nil), s.fcell[shift:shift+len(lv.Boxes)]...)
			slices.Sort(candidate)
			found := false
			buf := make([]int32, s.n)
			for id := range s.player {
				s.unpack(int32(id), buf)
				if slices.Equal(candidate, buf) {
					found = true
					break
				}
			}
			if !found {
				target = candidate
				break
			}
		}
		if len(target) == 0 {
			t.Fatal("no absent target")
		}
		tgt := &meet.Target{Boxes: target, Player: lv.Player}
		if _, hit := s.retargetTo(tgt); hit {
			t.Fatal("unexpected immediate terminal hit")
		}
		if int(s.retargetNodes) != n {
			t.Fatalf("epoch %d retained %d/%d", epoch, s.retargetNodes, n)
		}
		for id := range s.player {
			if s.mvStart[id] != -1 || s.mvOff[id] != 0 || s.mvTotal[id] != 0 {
				t.Fatalf("epoch %d node %d not reset", epoch, id)
			}
		}
		// Compare every identity emitted by the full generation callback with the
		// union of windows, including retained moves explicitly marked used.
		for id := int32(0); id < min(int32(n), 16); id++ {
			if s.dead[id] {
				continue
			}
			cur := make([]int32, s.n)
			s.unpack(id, cur)
			clear(s.occ)
			var h int32
			for _, b := range cur {
				s.occ[b] = true
				h += s.dist[b]
			}
			s.reach(s.player[id], s.mark)
			expected := map[k]bool{}
			got := map[k]int{}
			used := []macroCandidate{}
			DebugOpen = func(_ *solver, _ int32, _ []int32, ms []macroCandidate, _ []int32, _ []int) {
				for _, m := range ms {
					expected[k{m.i, m.t, m.side}] = true
					if m.alreadyUsed {
						used = append(used, m)
					}
				}
			}
			s.open(id, cur, h, s.stamp)
			DebugOpen = nil
			for _, m := range used {
				child := slices.Clone(cur)
				child[m.i] = m.t
				slices.Sort(child)
				_, existing, _ := s.find(s.pack(child), m.candNP)
				if existing < 0 || existing >= s.retargetNodes {
					t.Fatalf("used move has no retained child")
				}
				if !s.dead[existing] {
					edgeFound := false
					for edge := s.edgeHead[existing]; edge >= 0; edge = s.edges[edge].next {
						if s.edges[edge].parent == id {
							edgeFound = true
							break
						}
					}
					if !edgeFound {
						t.Fatalf("retained child %d missing incoming dependency from %d", existing, id)
					}
					if s.parent[existing] != id {
						transpositions++
					}
				}
			}
			for round := 0; round < 1000; round++ {
				for j := s.mvStart[id]; j < s.mvStart[id]+int32(s.mvLen[id]); j++ {
					i, to, _, _ := decodeMove(s.mv[j])
					got[k{i, to, moveSide(s.mv[j])}]++
				}
				if s.mvOff[id]+s.mvLen[id] >= s.mvTotal[id] || s.mvLen[id] == 0 {
					break
				}
				totalWindows++
				s.mvOff[id] += s.mvLen[id]
				s.reach(s.player[id], s.mark)
				s.open(id, cur, h, s.stamp)
			}
			if len(expected) != len(got) {
				t.Fatalf("epoch %d node %d expected %d got %d", epoch, id, len(expected), len(got))
			}
			for x := range expected {
				if got[x] != 1 {
					t.Fatalf("epoch %d node %d identity %+v count=%d", epoch, id, x, got[x])
				}
			}
			// Independent fresh-target solver, rooted at this exact state. Compare
			// the full candidate identities rather than only counts or ordering.
			fresh := newTowardReal(rows, lv, tgt, deadline, nil, &budget, &tracked, 2, 200)
			fresh.pairOffsets()
			fresh.buildWalk()
			fresh.dpair = make([]int8, len(lv.Wall)*len(fresh.poff))
			for _, b := range cur {
				fresh.occ[b] = true
			}
			p := fresh.reach(s.player[id], fresh.mark)
			fid := fresh.add(cur, p, -1, -1, -1, -1, nil, 0, h, 0, -1)
			actual := map[k]bool{}
			DebugOpen = func(_ *solver, _ int32, _ []int32, ms []macroCandidate, _ []int32, _ []int) {
				for _, m := range ms {
					actual[k{m.i, m.t, m.side}] = true
				}
			}
			fresh.open(fid, cur, h, fresh.stamp)
			DebugOpen = nil
			if len(actual) != len(expected) {
				t.Fatalf("fresh target epoch %d node %d: expected %d fresh %d", epoch, id, len(expected), len(actual))
			}
			for x := range expected {
				if !actual[x] {
					t.Fatalf("fresh target epoch %d node %d missing %+v", epoch, id, x)
				}
			}
			clear(s.occ)
		}
		before := s.expandedN
		s.nodeLimit = before + 25
		_, res := s.towardRun(deadline, hugeMem)
		if s.expandedN <= before {
			t.Fatalf("epoch %d did not resume: reason=%s res=%+v stop=%v nodeBudget=%d meetBack=%v meetFwd=%v cancelled=%v exp=%d limit=%d", epoch, res.Reason, res, Stop.Load(), NodeBudget, meet.Back.Load() != nil, meet.Fwd.Load() != nil, s.cancelled, s.expandedN, s.nodeLimit)
		}
	}
	if totalWindows == 0 {
		t.Fatal("no reopen windows tested")
	}
	if transpositions == 0 {
		t.Fatal("no non-parent transposition checked")
	}
	t.Logf("two epochs, retained %d nodes, %d extra windows, %d transposition dependencies", n, totalWindows, transpositions)
}
