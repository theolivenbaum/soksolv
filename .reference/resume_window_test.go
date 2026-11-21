package main

import (
	"container/heap"
	"soksolv/verify"
	"testing"
	"time"
)

func TestWindowContinuationAcrossSlices(t *testing.T) {
	oldW, oldN := MoveWindow, NodeBudget
	defer func() { MoveWindow, NodeBudget = oldW, oldN }()
	MoveWindow, NodeBudget = 1, 1
	s := testSolver(t, "#########", "#       #", "#  $ $  #", "#  . .  #", "#   @   #", "#########")
	s.noPlan = true
	deadline := time.Now().Add(10 * time.Second)
	r := s.solve(deadline, hugeMem)
	if r.Solved {
		t.Fatal("fixture unexpectedly solved in one expansion")
	}
	var pending fessEntry
	found := false
	for i, q := range s.frontier.queues {
		for _, e := range q {
			if e.move == -2 {
				pending = e
				found = true
			}
		}
		s.frontier.queues[i] = nil
	}
	if !found {
		t.Fatalf("no pending continuation after slice: %+v", r)
	}
	heap.Push(&s.frontier.queues[s.bucket[pending.node]], pending)
	s.cancelled = false
	NodeBudget = 2
	e, ok := s.selectMove()
	if !ok || e.node != pending.node || s.stats.Reopens == 0 {
		t.Fatalf("pending continuation was lost: %+v %v", e, ok)
	}
}

func TestWindowContinuationSolveResume(t *testing.T) {
	oldW, oldN := MoveWindow, NodeBudget
	defer func() { MoveWindow, NodeBudget = oldW, oldN }()
	MoveWindow, NodeBudget = 1, 1
	rows := []string{"#########", "#       #", "#  $ $  #", "#  . .  #", "#   @   #", "#########"}
	s := testSolver(t, rows...)
	s.noPlan = true
	deadline := time.Now().Add(5 * time.Second)
	for i := 1; i <= 100; i++ {
		NodeBudget = int64(i)
		r := s.solve(deadline, hugeMem)
		if r.Solved {
			if err := verify.Check(rows, r.Solution); err != nil {
				t.Fatal(err)
			}
			if i < 2 {
				t.Fatal("no resume")
			}
			return
		}
		if r.Reason != "timeout" {
			t.Fatalf("slice %d: %+v", i, r)
		}
	}
	t.Fatal("did not solve within 100 one-expansion slices")
}

func TestDeferredWindowSurvivesRetargetReparent(t *testing.T) {
	oldW, oldN, oldReuse := MoveWindow, NodeBudget, RetargetReuse
	defer func() { MoveWindow, NodeBudget, RetargetReuse = oldW, oldN, oldReuse }()
	MoveWindow, NodeBudget, RetargetReuse = 1, 1, true
	s := testSolver(t, "#########", "#       #", "#  $ $  #", "#  . .  #", "#   @   #", "#########")
	s.noPlan = true
	s.solve(time.Now().Add(time.Second), hugeMem)
	var id int32 = -1
	for _, q := range s.frontier.queues {
		for _, e := range q {
			if e.move == -2 {
				id = e.node
			}
		}
	}
	if id < 0 {
		t.Fatal("missing pending window")
	}
	s.retargetNodes = int32(len(s.player))
	boxes := make([]int32, s.n)
	s.unpack(id, boxes)
	oldVersion := s.version[id]
	s.add(boxes, s.player[id], -1, -1, -1, -1, nil, s.g[id]-1, 0, s.w[id], -1)
	if s.version[id] != oldVersion+1 {
		t.Fatal("fixture did not reparent")
	}
	fresh := false
	for _, q := range s.frontier.queues {
		for _, e := range q {
			if e.node == id && e.move == -2 && e.version == s.version[id] && s.bucket[id] < uint32(len(s.frontier.queues)) {
				fresh = true
			}
		}
	}
	if !fresh {
		t.Fatal("reparent invalidated continuation without replacing it")
	}
}
