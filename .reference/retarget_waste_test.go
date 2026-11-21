package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"soksolv/meet"
	"soksolv/verify"
)

func TestRetargetWasteAccounting(t *testing.T) {
	file := "levels/Large Test Suite/XSokoban_90.xsb"
	idx := 8

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	all := splitCollection(data)
	boardLines := all[idx-1]
	lv, err := parseLevel(boardLines)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	// Create first toward solver aiming at a dummy target
	tgt1 := &meet.Target{
		Boxes:  lv.Boxes,
		Player: lv.Player,
	}
	var b, tr atomic.Uint64
	s1 := newTowardReal(boardLines, lv, tgt1, deadline, nil, &b, &tr, 2, 50)
	s1.towardRun(deadline, hugeMem)

	if len(s1.player) == 0 {
		t.Fatal("expected s1 to expand nodes")
	}

	// Create second toward solver with s1 in prevTrees
	tgt2 := &meet.Target{
		Boxes:  lv.Boxes,
		Player: lv.Player,
	}
	s2 := newTowardReal(boardLines, lv, tgt2, deadline, nil, &b, &tr, 2, 50)
	s2.prevTrees = []*solver{s1}
	s2.towardRun(deadline, hugeMem)

	t.Logf("s1 nodes: %d", len(s1.player))
	t.Logf("s2 RetargetTotalAdded: %d, RetargetRegenAdded: %d", s2.stats.RetargetTotalAdded, s2.stats.RetargetRegenAdded)
	t.Logf("s2 RetargetTotalExpanded: %d, RetargetRegenExpanded: %d", s2.stats.RetargetTotalExpanded, s2.stats.RetargetRegenExpanded)

	if s2.stats.RetargetTotalAdded == 0 {
		t.Fatal("expected s2.stats.RetargetTotalAdded > 0")
	}
	if s2.stats.RetargetRegenAdded == 0 {
		t.Fatal("expected s2.stats.RetargetRegenAdded > 0 when restarted from root with s1 in prevTrees")
	}
	if s2.stats.RetargetRegenAdded > s2.stats.RetargetTotalAdded {
		t.Fatalf("regen added %d > total added %d", s2.stats.RetargetRegenAdded, s2.stats.RetargetTotalAdded)
	}
}

func TestRetargetReuse(t *testing.T) {
	savedReuse := RetargetReuse
	RetargetReuse = true
	defer func() { RetargetReuse = savedReuse }()

	file := "levels/Large Test Suite/XSokoban_90.xsb"
	idx := 8

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	all := splitCollection(data)
	boardLines := all[idx-1]
	lv, err := parseLevel(boardLines)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	tgt1 := &meet.Target{
		Boxes:  lv.Boxes,
		Player: lv.Player,
	}
	var b, tr atomic.Uint64
	s := newTowardReal(boardLines, lv, tgt1, deadline, nil, &b, &tr, 2, 50)
	s.towardRun(deadline, hugeMem)

	nodes1 := len(s.player)
	if nodes1 == 0 {
		t.Fatal("expected s to have nodes after round 1")
	}

	// Now retarget without restarting
	tgt2 := &meet.Target{
		Boxes:  lv.Boxes,
		Player: lv.Player,
	}
	sol, ok := s.retargetTo(tgt2)
	t.Logf("retargetTo ok=%v, sol len=%d", ok, len(sol))

	if !ok {
		// Run second round
		sol, _ = s.towardRun(deadline, hugeMem)
	}

	// Graph was reused in place: RetargetRegenAdded must be 0!
	if s.stats.RetargetRegenAdded != 0 {
		t.Fatalf("expected 0 RetargetRegenAdded with graph reuse, got %d", s.stats.RetargetRegenAdded)
	}
	if len(s.player) < nodes1 {
		t.Fatalf("expected graph to preserve >= %d nodes, got %d", nodes1, len(s.player))
	}
	t.Logf("nodes after retarget: %d (started from %d)", len(s.player), nodes1)
}

func TestRetargetImmediateHit(t *testing.T) {
	savedReuse := RetargetReuse
	RetargetReuse = true
	defer func() { RetargetReuse = savedReuse }()

	file := "levels/Open Test Suite/Microban.xsb"
	idx := 1

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	all := splitCollection(data)
	boardLines := all[idx-1]
	lv, err := parseLevel(boardLines)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	ps := newPullSolver(lv, deadline)
	ps.realRows = boardLines
	ps.nodeLimit = 200
	ps.solve(deadline, hugeMem)

	tgt := ps.bestTarget()
	if tgt == nil {
		t.Skip("no target from pull solver")
	}

	var b, tr atomic.Uint64
	s := newTowardReal(boardLines, lv, tgt, deadline, nil, &b, &tr, 2, 500)
	sol, res := s.towardRun(deadline, hugeMem)
	if !res.Solved || sol == "" {
		t.Skip("level not solved in 500 nodes")
	}

	// Now retargetTo the EXACT same target: node onTarget should immediately hit and return solution!
	solHit, ok := s.retargetTo(tgt)
	if !ok {
		t.Fatalf("expected immediate hit on retargetTo matching solved target")
	}
	if solHit == "" {
		t.Fatalf("expected non-empty solution on immediate hit")
	}
	if err := verify.Check(boardLines, solHit); err != nil {
		t.Fatalf("immediate hit solution invalid: %v", err)
	}
	t.Logf("immediate hit solution verified successfully: %s", solHit)
}
