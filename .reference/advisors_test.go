package main

import "testing"

func TestAdvisorsSelectIndependently(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	s.basin = nil
	parent := fessScore{oop: 2, packed: 0, conn: 3, room: 3, hot: 3}
	moves := []macroCandidate{
		{packed: 1, oop: 2, r: 4, room: 4, hot: 4},
		{packed: 0, oop: 2, r: 2, room: 4, hot: 4},
		{packed: 0, oop: 2, r: 4, room: 2, hot: 4},
		{packed: 0, oop: 2, r: 4, room: 4, hot: 2},
		{packed: 0, oop: 2, r: 4, room: 4, hot: 4, explorer: true},
	}
	chosen := s.advise(s.lv.Boxes, []int{0, 0, 0}, parent, moves, 0)
	for advisor := packingAdvisor; advisor <= explorerAdvisor; advisor++ {
		if chosen[advisor] != advisor {
			t.Fatalf("advisor %d chose %d", advisor, chosen[advisor])
		}
	}
	if chosen[openerAdvisor] != -1 || chosen[oopAdvisor] != -1 || chosen[displacementAdvisor] != -1 {
		t.Fatal("unavailable hotspot/basin/displacement must not invent recommendations")
	}
}

func TestOpenerMovesNearestBoxAwayWithoutWorseningConnectivity(t *testing.T) {
	s := testSolver(t, "##########", "#@$$  $..#", "#     .  #", "##########")
	s.basin = nil
	cur := s.lv.Boxes
	moves := []macroCandidate{
		{i: 1, b: cur[1], t: cur[1] + 1, r: 1}, // nearest blocker moves away
		{i: 2, b: cur[2], t: cur[2] + 1, r: 1}, // farther blocker
		{i: 1, b: cur[1], t: cur[1] + 2, r: 2}, // connectivity worsens
	}
	chosen := s.advise(cur, []int{2, 0, 0}, fessScore{conn: 1}, moves, 0)
	if chosen[openerAdvisor] != 0 {
		t.Fatalf("opener chose %d", chosen[openerAdvisor])
	}
}

func TestDisplacementAdvisorSelectsCandidate(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	s.dl = NewDisplacementLandmarks(s.lv, s.dirs, s.goals)
	cur := s.lv.Boxes
	// Goals are s.goals. Let goal 0 be displaced.
	g0 := s.goals[0]
	moves := []macroCandidate{
		{i: 0, b: cur[0], t: cur[0] + 1},        // not from displaced goal
		{i: 1, b: g0, t: g0 + 1, r: 2, plen: 2}, // vacates displaced goal
		{i: 1, b: g0, t: g0 - 1, r: 1, plen: 1}, // vacates displaced goal with better connectivity & plen
	}
	displaceMask := uint64(1) << s.dl.goalIdx[g0]
	chosen := s.advise(cur, []int{0, 0, 0}, fessScore{conn: 2}, moves, displaceMask)
	if chosen[displacementAdvisor] != 2 {
		t.Fatalf("displacementAdvisor chose %d, want 2", chosen[displacementAdvisor])
	}
}
