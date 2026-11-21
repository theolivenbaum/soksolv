package main

import (
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"soksolv/full"
	"soksolv/meet"
	"soksolv/reverse"
	"soksolv/verify"
)

// Every state on a backward solution's path, replayed forwards with the
// forward searches' normalization, must be found in the perimeter.
func TestPerimeterKeysAgree(t *testing.T) {
	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	for _, idx := range []int{1, 5, 20, 60} {
		rows := levels[idx-1]
		lv, _ := parseLevel(rows)
		mt := meet.New(lv.W*lv.H, 20, 1)
		meet.Back.Store(mt)
		rr, err := reverse.Solve(rows, time.Now().Add(5*time.Second), 1<<30)
		meet.Back.Store(nil)
		if err != nil || !rr.Solved {
			t.Fatalf("level %d: reverse did not solve: %v %v", idx, err, rr.Reason)
		}
		s := newSolver(lv)
		boxes := slices.Clone(lv.Boxes)
		p := lv.Player
		misses, states := 0, 0
		check := func() {
			slices.Sort(boxes)
			for _, b := range boxes {
				s.occ[b] = true
			}
			np := s.reach(p, s.mark)
			for _, b := range boxes {
				s.occ[b] = false
			}
			states++
			if !mt.Has(mt.Key(boxes, np)) {
				misses++
			}
		}
		check()
		for i := 0; i < len(rr.Solution); i++ {
			c := rr.Solution[i]
			var d int32
			switch c | 0x20 {
			case 'u':
				d = -int32(lv.W)
			case 'd':
				d = int32(lv.W)
			case 'l':
				d = -1
			case 'r':
				d = 1
			}
			p += d
			if c >= 'A' && c <= 'Z' {
				boxes[slices.Index(boxes, p)] = p + d
				check()
			}
		}
		if misses > 0 {
			t.Errorf("level %d: %d of %d path states missing from the perimeter", idx, misses, states)
		}
	}
}

// Perturbed forward restarts still return valid solutions.
func TestPerturbedForwardRestarts(t *testing.T) {
	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	for _, idx := range []int{3, 12, 40} {
		for seed, free := range []bool{true, false, true, false} {
			var budget, tracked atomic.Uint64
			r, err := full.SolveWith(levels[idx-1], time.Now().Add(3*time.Second), 1<<30, full.Slot{Budget: &budget, Tracked: &tracked}, free, seed)
			if err != nil || !r.Solved {
				t.Errorf("level %d seed %d: not solved (%v %s)", idx, seed, err, r.Reason)
				continue
			}
			if err := verify.Check(levels[idx-1], r.Solution); err != nil {
				t.Errorf("level %d seed %d: invalid solution: %v", idx, seed, err)
			}
		}
	}
}
