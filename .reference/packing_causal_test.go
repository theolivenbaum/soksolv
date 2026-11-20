package main

import (
	"math/bits"
	"testing"
	"time"
)

func TestPackingCausalInferenceAgainstAllEventSubsets(t *testing.T) {
	events := []packingEvent{{1, -1, 2}, {2, -1, 3}, {3, 0, 4}, {4, 1, -1}, {5, 2, -1}, {6, -1, -1}}
	prereq := []uint{0, 0, 1 << 0, (1 << 1) | (1 << 2), 1 << 2, 0}
	d := &packingDAG{events: events, start: []int{0, 1, 2, 4, 4, 4, 4}, consumers: []int{2, 3, 3, 4}, credit: make([]bool, 6), live: make([]bool, 7)}
	occ := make([]bool, 7)
	for state := 0; state < 64; state++ {
		for c := 1; c <= 6; c++ {
			occ[c] = state>>(c-1)&1 != 0
		}
		greatest := uint(0)
		for mask := uint(0); mask < 64; mask++ {
			ok := true
			for i, e := range events {
				if mask>>i&1 == 0 {
					continue
				}
				if mask&prereq[i] != prereq[i] || !occ[e.to] && (e.next < 0 || mask>>e.next&1 == 0) {
					ok = false
					break
				}
			}
			if ok {
				greatest |= mask
			}
		}
		score := d.infer(occ)
		if score != bits.OnesCount(greatest) {
			t.Fatalf("occupancy%d score%d want%d", state, score, bits.OnesCount(greatest))
		}
		for i, credit := range d.credit {
			if credit != (greatest>>i&1 != 0) {
				t.Fatalf("occupancy%d event%d", state, i)
			}
		}
	}
}

func TestPackingCausalIndependentCommitmentAndRetirement(t *testing.T) {
	d := &packingDAG{events: []packingEvent{{1, -1, 2}, {2, -1, -1}, {3, 0, -1}}, start: []int{0, 1, 1, 1}, consumers: []int{2}, credit: make([]bool, 3), live: make([]bool, 4)}
	occ := make([]bool, 4)
	occ[2] = true
	if score := d.infer(occ); score != 1 || !d.live[2] {
		t.Fatal("independent later commitment was not credited")
	}
	occ[3] = true
	if score := d.infer(occ); score != 3 {
		t.Fatalf("superseded parking score%d", score)
	}
	if d.live[1] {
		t.Fatal("unoccupied expired parking is placed")
	}
	occ[3] = false
	if score := d.infer(occ); score != 1 {
		t.Fatal("stale credits survived occupancy change")
	}
}

func TestPackingCausalCompilerWitnessCheckpoints(t *testing.T) {
	s := testSolver(t, "#############", "#$$$$#      #", "#$$$ #      #", "#     .. ...#", "#@   #  . . #", "#############")
	s.basin.sink = s.rooms.cell[2*s.lv.W+2]
	p := s.buildPackingPlan(time.Now().Add(time.Second), 4096)
	if p == nil || !p.complete || p.parkings == 0 {
		t.Fatal("missing parking witness")
	}
	checkPackingWitness(t, s, p)
	d := s.compilePackingDAG(p)
	if d == nil {
		t.Fatalf("compilation failed after%d visits", p.causalVisits)
	}
	occ := make([]bool, len(s.occ))
	for i, ph := range p.phases {
		clear(occ)
		for _, b := range ph.required {
			occ[b] = true
		}
		if score := d.infer(occ); score < len(p.phases[0].required)+i {
			t.Fatalf("checkpoint%d score%d", i, score)
		}
	}
	if got := d.infer(occ); got != len(d.events) {
		t.Fatalf("complete witness%d/%d", got, len(d.events))
	}
	r := newPackingRoute(s)
	r.visits = packingRouteVisitCap - 1
	if r.compile(p.phases[1], p.phases[0]) {
		t.Fatal("route compiler ignored work cap")
	}
	t.Logf("%d events, %d dependencies, %d compile flood visits", len(d.events), d.edges, p.causalVisits)
}
