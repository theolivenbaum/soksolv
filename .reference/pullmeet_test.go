package main

import (
	"os"
	"sync"
	"testing"
	"time"

	"soksolv/meet"
	"soksolv/verify"
)

// TestPullMeetsForward: forward FESS and pull FESS alone, sharing the
// perimeters as in the portfolio. Meetings must happen on some levels and
// every returned solution must verify.
func TestPullMeetsForward(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: skipped in fast tier (go test -short); see research/exp-371")
	}
	data, err := os.ReadFile("levels/Large Test Suite/XSokoban_90.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	// The meet tables are process-global; a stale table made later tests' solvers "meet" a
	// foreign perimeter (TestRetargetMultipleEpochsWindowIdentity, exp-377).
	defer func() { meet.Back.Store(nil); meet.Fwd.Store(nil); Stop.Store(false) }()
	posted, confirmed, solved := int64(0), int64(0), 0
	for _, idx := range []int{4, 6, 8, 10, 13, 15, 16, 18, 20, 23} {
		rows := levels[idx-1]
		lv, _ := parseLevel(rows)
		back, fwd := meet.New(lv.W*lv.H, 20, 4), meet.New(lv.W*lv.H, 20, 3)
		meet.Back.Store(back)
		meet.Fwd.Store(fwd)
		deadline := time.Now().Add(4 * time.Second)
		Stop.Store(false)
		var wg sync.WaitGroup
		res := make([]Result, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			s := newSolverUntil(lv, deadline)
			s.rows = rows
			res[0] = s.solve(deadline, hugeMem)
			if res[0].Solved {
				Stop.Store(true)
			}
		}()
		go func() {
			defer wg.Done()
			s := newPullSolver(lv, deadline)
			s.budgetP, s.trackedP = &Budget4, &Tracked4
			res[1] = s.solve(deadline, hugeMem)
			if res[1].Solved {
				Stop.Store(true)
			}
		}()
		wg.Wait()
		Stop.Store(false)
		posted += back.Posted.Load() + fwd.Posted.Load()
		confirmed += back.Confirmed.Load() + fwd.Confirmed.Load()
		for k, r := range res {
			if r.Solved {
				if err := verify.Check(rows, r.Solution); err != nil {
					t.Fatalf("#%d searcher %d: solution does not verify: %v", idx, k, err)
				}
			}
		}
		if res[0].Solved || res[1].Solved {
			solved++
		}
		t.Logf("#%d: forward %v (%s), pull %v (%s); posted %d confirmed %d", idx, res[0].Solved, res[0].Reason, res[1].Solved, res[1].Reason, back.Posted.Load()+fwd.Posted.Load(), back.Confirmed.Load()+fwd.Confirmed.Load())
	}
	meet.Back.Store(nil)
	meet.Fwd.Store(nil)
	t.Logf("solved %d, meetings posted %d, confirmed %d", solved, posted, confirmed)
}
