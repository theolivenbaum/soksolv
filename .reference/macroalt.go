package main

import (
	"flag"
	"sync/atomic"
	"time"

	"soksolv/trace"
)

// exp-769 (-macro-alt-plan; exp-766 pitch P3; provenance hybrid): the macro
// lane runs a second macro engine beside the default one. The alt engine
// plans plan4 with exp-742's settings (festsink + roomsep + keep-parks 1)
// for itself only (planAlt): it never publishes its list to fa2, and the
// default engine, every other lane and -plan4-scope are unchanged. The two
// engines alternate on the lane's core in doubling baton slices (1024
// expansions << min(k, -macro-alt-shift)) and split the lane's memory share
// between the live engines. Each engine runs its own ring (replan carries
// its own plan; revive on memory death). Deterministic under -nodes: each
// engine's configs are bounded by -nodes/2, so -nodes 2N gives the default
// engine exactly the default lane's N.
var (
	MacroAltPlan    bool
	MacroAltShift   = 4
	TrackedMacroAlt atomic.Uint64
	macroAltOwner   = -1 // the alt engine's forward-perimeter mailbox (-1: shares the default's)
)

func init() {
	flag.BoolVar(&MacroAltPlan, "macro-alt-plan", true, "exp-769 (default on since its promotion 2026-11-14; -macro-alt-plan=false = old path): the macro lane runs a second macro engine on exp-742's plan4 config (festsink + roomsep + keep-parks 1, this engine only, not published to fa2), alternating with the default engine in doubling baton slices with the lane's memory split")
	flag.IntVar(&MacroAltShift, "macro-alt-shift", MacroAltShift, "exp-769: -macro-alt-plan slices double up to 1024 << shift expansions")
}

// macroAlt is the running alt engine (nil: off).
type macroAlt struct {
	bat   *baton
	done  chan struct{}
	res   Result
	tr    *trace.Recorder
	nodes int64
	cfgs  int
}

// macroAltStart starts the alt engine (waiting for the token); the caller's
// goroutine is engine 0 and holds the token.
func macroAltStart(lv *Level, rows []string, deadline time.Time, memLimit uint64) *macroAlt {
	if !MacroAltPlan {
		return nil
	}
	b := newBaton([3]bool{true, true, false})
	b.dbl = max(MacroAltShift, 1)
	b.quota, b.turnK = []int64{1, 1, 0}, make([]int, 3)
	m := &macroAlt{bat: b, done: make(chan struct{}), tr: trace.New()}
	go m.run(lv, rows, deadline, memLimit)
	return m
}

// attach puts a config's solver on the baton as engine me.
func (m *macroAlt) attach(s *solver, me int) {
	if m == nil {
		return
	}
	s.bat, s.batMe, s.batDyn = m.bat, me, true
	if NodeBudget > 0 {
		s.nodeLimit = max(NodeBudget/2, 1)
	}
}

func (m *macroAlt) run(lv *Level, rows []string, deadline time.Time, memLimit uint64) {
	defer close(m.done)
	m.bat.wait(1)
	var s *solver
	var root *rootBoard
	revs, revived := 0, false
	m.res = ringRun(deadline, slots(&TrackedMacroAlt), func(k int) Result {
		prev := s
		if revived {
			root, revived = relayPick("macro-alt", revs), false
			prev = nil
		}
		lvk, rowsk := rootOf(root, lv, rows)
		s = newSolverUntil(lvk, deadline)
		s.laneID, s.tr, s.rows = 1, m.tr, rowsk
		s.trackedP, s.planAlt = &TrackedMacroAlt, true
		if macroAltOwner >= 0 {
			s.owner = macroAltOwner
		}
		m.attach(s, 1)
		if prev != nil {
			s.prePlan, s.prePlanAt = prev.prePlan, prev.prePlanAt
		}
		r := s.solve(deadline, memLimit)
		m.nodes += s.expandedN
		m.cfgs++
		if ReviveRoot && !r.Solved {
			s.relayFlush()
		}
		return relayFix(r)
	}, func(r Result) restart {
		if r.Reason == "replan" && time.Now().Before(deadline) {
			return replan
		}
		if ReviveRoot && reviveRule(r, deadline) == revive {
			return revive
		}
		return stop
	}, func(int) { revs, revived = revs+1, true })
	m.bat.done(1, m.res.Solved)
}

// finish ends engine 0 (result r), waits for the alt engine and returns the
// lane's result.
func (m *macroAlt) finish(r Result, tr *trace.Recorder) Result {
	if m == nil {
		return r
	}
	m.bat.done(0, r.Solved)
	<-m.done
	tr.SetExtra("macro_alt_end", m.res.Reason)
	tr.SetExtra("macro_alt_nodes", m.nodes)
	tr.SetExtra("macro_alt_cfgs", m.cfgs)
	if !r.Solved && m.res.Solved {
		tr.SetExtra("macro_alt_won", true)
		m.res.Nodes += r.Nodes
		return m.res
	}
	r.Nodes += m.res.Nodes
	return r
}
