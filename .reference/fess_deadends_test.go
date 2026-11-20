package main

import "testing"

func deadendTestSolver(n int) *solver {
	s := &solver{dead: make([]bool, n), edgeHead: make([]int32, n), liveChildren: make([]uint32, n), version: make([]uint32, n), mvStart: make([]int32, n), mvLeft: make([]uint32, n)}
	for i := range s.edgeHead {
		s.edgeHead[i] = -1
	}
	return s
}

func TestDeadEndPropagationAcrossTranspositionDiamond(t *testing.T) {
	s := deadendTestSolver(4)
	s.recordEdge(0, 1)
	s.recordEdge(0, 2)
	s.recordEdge(1, 3)
	s.recordEdge(2, 3)
	s.propagateDead(1)
	if s.dead[1] {
		t.Fatal("unresolved descendant was assumed dead")
	}
	s.propagateDead(3)
	for id, dead := range s.dead {
		if !dead {
			t.Fatalf("dead end did not reach ancestor %d", id)
		}
	}
	if s.stats.DeadEnds != 4 {
		t.Fatal("transposition propagated twice")
	}
}

func TestDeadEndPropagationPreservesCyclesAndUntriedMoves(t *testing.T) {
	s := deadendTestSolver(3)
	s.mvLeft[0] = 1
	s.recordEdge(0, 1)
	s.recordEdge(1, 2)
	s.recordEdge(2, 1)
	s.propagateDead(1)
	s.propagateDead(2)
	if s.dead[0] || s.dead[1] || s.dead[2] {
		t.Fatal("unresolved cycle was marked dead")
	}
	s = deadendTestSolver(2)
	s.mvLeft[0] = 1
	s.recordEdge(0, 1)
	s.propagateDead(1)
	if s.dead[0] {
		t.Fatal("untried alternative was pruned")
	}
	s.mvLeft[0] = 0
	s.propagateDead(0)
	if !s.dead[0] {
		t.Fatal("fully dead parent was not marked")
	}
}

func TestCancelledExpansionCannotProveDeadEnd(t *testing.T) {
	s := deadendTestSolver(1)
	s.cancelled = true
	s.propagateDead(0)
	if s.dead[0] {
		t.Fatal("partial expansion used as deadlock proof")
	}
}
