package main

import (
	"slices"
	"testing"
	"time"
)

func TestPackingPlannerDiscoversTemporaryParking(t *testing.T) {
	s := testSolver(t,
		"#############",
		"#$$$$#      #",
		"#$$$ #      #",
		"#     .. ...#",
		"#@   #  . . #",
		"#############")
	if s.basin == nil {
		t.Fatal("missing basin")
	}
	s.basin.sink = s.rooms.cell[2*s.lv.W+2]
	plan := s.buildPackingPlan(time.Now().Add(100*time.Millisecond), 4096)
	if plan == nil || !plan.complete || plan.parkings == 0 {
		t.Fatalf("parking was not discovered: %+v", plan)
	}
	checkPackingWitness(t, s, plan)
}

func TestPackingPlanWitnessAndIsolation(t *testing.T) {
	s := testSolver(t, "#############", "#     #     #", "#@ $$    .. #", "#     #     #", "#############")
	before := slices.Clone(s.occ)
	plan := s.buildPackingPlan(time.Now().Add(100*time.Millisecond), 4096)
	if plan == nil || !plan.complete {
		t.Fatalf("simple goal-room plan incomplete: %+v", plan)
	}
	if !slices.Equal(before, s.occ) {
		t.Fatal("planner modified forward occupancy")
	}
	checkPackingWitness(t, s, plan)
	if len(plan.phases[0].required) != 0 || !slices.Equal(plan.phases[len(plan.phases)-1].required, s.goals) {
		t.Fatal("plan does not connect sink supply to final goals")
	}
}

func TestPackingPlanBoundedPartialWitness(t *testing.T) {
	s := testSolver(t, "#############", "#     #     #", "#@ $$    .. #", "#     #     #", "#############")
	plan := s.buildPackingPlan(time.Now().Add(time.Second), 1)
	if plan == nil || plan.complete || plan.remaining != 2 {
		t.Fatalf("node cap failed: %+v", plan)
	}
	checkPackingWitness(t, s, plan)
	s.plan = plan
	s.occ[s.goals[0]] = true
	if got := s.packed(); got != 1 {
		t.Fatalf("partial first checkpoint erased packing progress: %d", got)
	}
	if !s.placed(s.goals[0]) {
		t.Fatal("partially supplied checkpoint box not considered placed")
	}
}

func checkPackingWitness(t *testing.T, s *solver, plan *packingPlan) {
	t.Helper()
	for i := 0; i+1 < len(plan.phases); i++ {
		child, parent := plan.phases[i], plan.phases[i+1]
		m := child.reverseMove
		boxes := slices.Clone(parent.required)
		at, ok := slices.BinarySearch(boxes, m.from)
		if !ok {
			t.Fatal("witness source missing")
		}
		if m.remove {
			if s.rooms.cell[m.to] != s.basin.sink {
				t.Fatal("box removed outside sink")
			}
			boxes = append(boxes[:at], boxes[at+1:]...)
		} else {
			boxes[at] = m.to
			slices.Sort(boxes)
		}
		if !slices.Equal(boxes, child.required) {
			t.Fatal("packing checkpoint does not match transition")
		}
		if !pullWitnessPossible(s, parent.required, parent.player, m) {
			t.Fatalf("invalid pull witness: %+v -> %+v via %+v", parent.required, child.required, m)
		}
	}
}

// Independent push-sized pull BFS validates the planner's macro endpoints.
func pullWitnessPossible(s *solver, boxes []int32, player int32, move packingMove) bool {
	fixed := make([]bool, len(s.lv.Wall))
	for _, b := range boxes {
		if b != move.from {
			fixed[b] = true
		}
	}
	flood := func(box, p int32) (int32, []bool) {
		seen := make([]bool, len(fixed))
		seen[p] = true
		q := []int32{p}
		norm := p
		for i := 0; i < len(q); i++ {
			norm = min(norm, q[i])
			for _, d := range s.dirs {
				to := q[i] + d
				if !s.lv.Wall[to] && !fixed[to] && to != box && !seen[to] {
					seen[to] = true
					q = append(q, to)
				}
			}
		}
		return norm, seen
	}
	type state struct{ box, player int32 }
	norm, _ := flood(move.from, player)
	q := []state{{move.from, norm}}
	seen := map[state]bool{q[0]: true}
	for i := 0; i < len(q); i++ {
		st := q[i]
		norm, reach := flood(st.box, st.player)
		if st.box == move.to {
			if move.remove {
				norm, _ = flood(-1, st.player)
			}
			if norm == move.player {
				return true
			}
		}
		for _, d := range s.dirs {
			to, p := st.box+d, st.box+2*d
			if !reach[to] || s.lv.Wall[p] || fixed[p] {
				continue
			}
			norm, _ := flood(to, p)
			child := state{to, norm}
			if !seen[child] {
				seen[child] = true
				q = append(q, child)
			}
		}
	}
	return false
}

func TestParkingCheckpointCountsAndOOPExemption(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	a, b, c := s.lv.Boxes[0], s.lv.Boxes[1], s.goals[0]
	s.plan = &packingPlan{phases: []packingPhase{{required: nil}, {required: []int32{a, b}}, {required: []int32{a, c}}}}
	s.basin = &packingBasin{inside: make([]bool, len(s.occ))}
	s.occ[a], s.occ[b] = true, true
	if phase, count := s.plan.status(s.occ); phase != 1 || count != 2 {
		t.Fatalf("parking phase=%d packed=%d", phase, count)
	}
	if got := s.outOfPlan([]int32{a, b}); got != 0 {
		t.Fatalf("parked boxes marked OOP: %d", got)
	}
	s.occ[b] = false
	s.occ[c] = true
	if phase, count := s.plan.status(s.occ); phase != 2 || count != 2 {
		t.Fatalf("parking progress lost when physical count stayed equal: %d/%d", phase, count)
	}
	if s.placed(b) {
		t.Fatal("obsolete parking square still exempt")
	}
}
