package main

import (
	"sync/atomic"
	"time"

	"soksolv/full"
	"soksolv/reverse"
	"soksolv/trace"
)

// LaneRevive (exp-588, -lane-revive): a forward lane (pull, novelty, festb,
// festx) whose search stops on memory frees its tree and starts again
// instead of exiting. In 142/142 unsolved 600 s runs every forward lane
// died on memory by ~215 s and the last half of the run was reverse lanes
// only (exp-581). The restart is fresh (exp-531: best-board finishers don't
// help) but not a replay: each revival seeds the lane's tie-breaking
// generator differently and turns on type-based exploration for lanes that
// lack it, so it is another search of the same kind. The lane keeps its
// memory share (it never leaves the portfolio, so its budget is not handed
// to the survivors). Meet-table entries it published stay in the tables.
// Nothing depends on the time limit.
var LaneRevive bool

// reviveSeed is the generator state for revival k (k >= 1); never zero.
func reviveSeed(k int) uint64 {
	return 0x2545f4914f6cdd1d + uint64(k)*0x9E3779B97F4A7C15
}

// reviveDiversify makes revival k of a lane's solver a different search.
func reviveDiversify(s *solver, k int) {
	if k == 0 {
		return
	}
	s.rng = reviveSeed(k)
	if !s.pfOn {
		s.reviveTypes = PFTypes
	}
}

// reviveLoop runs one lane's ring of configs k = 0, 1, ... (ringRun),
// reviving it while it stops on memory with time left. tracked are the
// slots the lane charges (cleared before a revive).
func reviveLoop(tr *trace.Recorder, deadline time.Time, tracked []*atomic.Uint64, run func(k int) Result) Result {
	var at []int64
	var why []string // exp-686: why each config ended (a revive restarts the lane's per-solve counters)
	start := time.Now()
	return ringRun(deadline, tracked, run, func(r Result) restart {
		if len(why) < 64 {
			why = append(why, r.Reason)
			tr.SetExtra("cfg_end", why)
		}
		return reviveRule(r, deadline)
	}, func(k int) {
		at = append(at, time.Since(start).Milliseconds())
		tr.SetExtra("revives", k+1)
		if len(at) <= 64 {
			tr.SetExtra("revive_ms", at)
		}
	})
}

// reviveRule: revive a lane whose config ended on memory (or a finished
// hub-gen/hub-pull alternation) while the portfolio runs with time left.
func reviveRule(r Result, deadline time.Time) restart {
	if r.Solved || !LaneRevive || !reviveReason(r.Reason) || Stop.Load() || full.Stop.Load() || reverse.Stop.Load() || time.Until(deadline) < time.Second {
		return stop
	}
	return revive
}

func reviveReason(reason string) bool {
	return reason == "memory" || reason == "hub idle" || reason == "festb ended" || reason == "hub ended" || reason == "reroot-invalid" // exp-744: a dropped re-rooted solution
}
