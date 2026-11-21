package main

import "testing"

func TestPackingObligationsPreserveExistingAdvisor(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	s.basin = nil
	a, b := s.lv.Boxes[0], s.lv.Boxes[1]
	s.packReady = make([]bool, len(s.occ))
	s.packUse = make([]int, len(s.occ))
	s.packBlock = make([]int, len(s.occ))
	s.packPrepared = true
	moves := []macroCandidate{{b: a, t: a + 1, packed: 1}, {b: b, t: b + 1}}
	s.packReady[b+1] = true
	chosen := s.advise(s.lv.Boxes, []int{0, 0, 0}, fessScore{}, moves, 0)
	if chosen[packingAdvisor] != 0 {
		t.Fatal("new advice displaced existing packing advice")
	}
	moves[0].packed = 0
	chosen = s.advise(s.lv.Boxes, []int{0, 0, 0}, fessScore{}, moves, 0)
	if chosen[packingAdvisor] != 1 {
		t.Fatal("ready commitment was not suggested")
	}
	clear(s.packReady)
	s.packBlock[b] = 2
	s.packUse[a+1] = 2
	chosen = s.advise(s.lv.Boxes, []int{0, 0, 0}, fessScore{}, moves, 0)
	if chosen[packingAdvisor] != 1 || s.stats.PackingPrepClears == 0 {
		t.Fatal("route clearance was not suggested")
	}
}

func TestPackingObligationsReadinessAndUnchangedScore(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	a, b, c := s.lv.Boxes[0], s.lv.Boxes[1], s.goals[0]
	d := &packingDAG{events: []packingEvent{{a, -1, 1}, {c, 0, -1}}, start: []int{0, 1, 1}, consumers: []int{1}, dependencyStart: []int{0, 0, 1}, dependencies: []int{0}, readStart: []int{0, 1, 4}, readCells: []int32{a, a, b, c}, credit: make([]bool, 2), live: make([]bool, len(s.occ))}
	s.plan = &packingPlan{dag: d, phases: []packingPhase{{required: nil}, {required: []int32{a}}}}
	clear(s.occ)
	s.occ[a] = true
	s.occ[b] = true
	s.preparePackingObligations()
	if !s.packReady[c] || s.packUse[a] != 0 || s.packBlock[b] != 1 {
		t.Fatal("wrong source/blocker readiness")
	}
	s.occ[a] = false
	s.occ[c] = true
	s.preparePackingObligations()
	if _, packed := s.plan.status(s.occ); packed != 0 {
		t.Fatal("advisory graph changed the checkpoint score")
	}
}

func TestPackingObligationsTinyOracle(t *testing.T) {
	old := PackingObligations
	PackingObligations = true
	defer func() { PackingObligations = old }()
	t.Run("primitive-oracle", TestFESSAgainstExhaustiveTinyPushOracle)
}
