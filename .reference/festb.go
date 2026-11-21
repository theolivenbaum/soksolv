package main

import (
	"slices"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/trace"
)

// exp-278: the festb lane alternates its backward and forward
// phases in rounds that double (festbUnit backward, festbFwdRatio units
// forward, then twice that each, ...) instead of splitting a deadline: the pull tree is
// kept across rounds, and the forward search continues while the backward
// best is unchanged and restarts toward a newer one. Both trees share the
// lane's memory budget; a phase that runs out of memory or work stops and the
// other keeps the core. Open-ended: no deadline needed.
var festbUnit = 250 * time.Millisecond
var festbFwdRatio = 2 // forward slice = this many backward slices

// festX (exp-283): an extra Festival-style lane by Festival's strategy letter.
var festX = "D"                       // exp-283: Festival strategy D in the full lane's slot
var macroLane, fullLane = true, false // exp-625 retired reverse-b and bridge-m; exp-640 deleted them

// festXRootBare (exp-285): the festx lane scores root moves on a bare board
// (pfStyle.rootBare); disabled in exp-286 (retires rootBare in favor of search invariants).
var festXRootBare = false

// FestXLane is the festx lane's backward and forward mailbox and Best slot.
const FestXLane = 5

// Budget9, Tracked9, Tracked10: the festx lane's memory (pull, forward).
var Budget9, Tracked9, Tracked10 atomic.Uint64

func festXStyle() pfStyle {
	st := pfStyle{types: PFTypes, rootBare: festXRootBare}
	switch festX {
	case "D":
		st.spread = true
	case "E":
		st.spread, st.bicon = true, true
	case "F":
		st.bicon = true
	}
	return st
}

// Tracked8: festb's forward tree (its budget is festb's, Budget7).
var Tracked8 atomic.Uint64

func roundEnd(slice time.Duration, deadline time.Time) time.Time {
	end := time.Now().Add(slice)
	if !deadline.IsZero() && end.After(deadline) {
		return deadline
	}
	return end
}

func sameTarget(a, b *meet.Target) bool {
	return a.Player == b.Player && slices.Equal(a.Boxes, b.Boxes)
}

func festbAlternate(ps *solver, rows []string, lv *Level, deadline time.Time, memLimit uint64, tr *trace.Recorder, fwdTracked *atomic.Uint64, owner int) Result {
	ps.peerTracked = fwdTracked
	defer fwdTracked.Store(0)
	var fwd *solver
	var cur *meet.Target
	backDone, fwdDone := false, false
	runs, nodes := 0, 0
	var rback *relayBackState // exp-761 -relay-back
	if owner == FestXLane {
		rback = newRelayBack(ps, lv)
		ps.rback = rback
	}
	for round := 0; !Stop.Load() && (deadline.IsZero() || time.Now().Before(deadline)); round++ {
		slice := doubling(festbUnit, round, 30)
		if !backDone {
			var metFwd atomic.Bool
			var drainFwd *mailDrain
			if RetainFwdMail && fwd != nil {
				fwdCur := fwd
				drainFwd = startDrain(func() (string, bool) {
					sol, ok := fwdCur.checkMail()
					return sol, ok && checkSol(rows, sol)
				}, &metFwd)
				ps.stopCheck = drainFwd.StopCheck()
			}
			r := ps.solve(roundEnd(slice, deadline), memLimit)
			// exp-686: the trace's "nodes" is the last slice's count; record the tree's total.
			ps.tr.SetExtra("pull_exp_cum", ps.expandedN)
			ps.tr.SetExtra("pull_stored", len(ps.player))
			if drainFwd != nil {
				joinedFwd := drainFwd.Stop()
				ps.stopCheck = nil
				if joinedFwd != "" {
					return Result{Solved: true, Solution: joinedFwd, Nodes: nodes + r.Nodes}
				}
				if sol, ok := fwd.checkMail(); ok && checkSol(rows, sol) {
					return Result{Solved: true, Solution: sol, Nodes: nodes + r.Nodes}
				}
			}
			if r.Solved {
				return r
			}
			backDone = r.Reason != "timeout"
			if backDone {
				rback.step(ps, true)
			}
		}
		if ps.resume == nil {
			return Result{Reason: "festb: backward search did not start"}
		}
		if Stop.Load() || !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		if tgt := ps.bestTarget(); tgt != nil && (cur == nil || !sameTarget(tgt, cur)) {
			oldFwd := fwd
			cur, fwd, fwdDone = tgt, nil, false
			runs++
			dumpHub(lv, map[int]string{FestBLane: "hub-gen", FestXLane: "hub-pull"}[owner], tgt, ps.expandedN, runs, false)
			tr.SetExtra("runs", runs)
			if RetargetReuse && oldFwd != nil && !oldFwd.memFull && len(oldFwd.player) > 0 && len(oldFwd.player) <= 16384 {
				t0 := time.Now()
				sol, ok := oldFwd.retargetTo(tgt)
				setupMs := float64(time.Since(t0).Microseconds()) / 1000.0
				oldFwd.stats.RetargetSetupMs += setupMs
				oldFwd.stats.RetargetRuns = runs
				oldFwd.stats.RetargetActiveNodes = len(oldFwd.player)
				oldFwd.stats.RetargetPrevNodes = len(oldFwd.player)
				fwd = oldFwd
				tr.SetExtra("retarget_runs", runs)
				tr.SetExtra("retarget_active_nodes", len(fwd.player))
				tr.SetExtra("retarget_prev_nodes", len(fwd.player))
				tr.SetExtra("retarget_setup_ms", oldFwd.stats.RetargetSetupMs)
				tr.SetExtra("retarget_regen_added", oldFwd.stats.RetargetRegenAdded)
				tr.SetExtra("retarget_total_added", oldFwd.stats.RetargetTotalAdded)
				tr.SetExtra("retarget_regen_expanded", oldFwd.stats.RetargetRegenExpanded)
				tr.SetExtra("retarget_total_expanded", oldFwd.stats.RetargetTotalExpanded)
				if ok {
					ps.stopCheck = nil
					return Result{Solved: true, Solution: sol, Nodes: nodes}
				}
			} else {
				fwdTracked.Store(0)
				fwd = newTowardReal(rows, lv, tgt, deadline, tr, ps.budgetP, fwdTracked, owner, 0)
				fwd.peerTracked = ps.trackedP
				if owner == FestBLane {
					fwd.laneID = 8
				} else {
					fwd.laneID = 10
				}
				if RetainFwdMail && oldFwd != nil {
					fwd.prevTrees = slices.Clone(oldFwd.prevTrees)
					fwd.addPrevTree(oldFwd)
				}
				if oldFwd != nil {
					fwd.stats.RetargetRuns = runs
					fwd.stats.RetargetRegenAdded += oldFwd.stats.RetargetRegenAdded
					fwd.stats.RetargetTotalAdded += oldFwd.stats.RetargetTotalAdded
					fwd.stats.RetargetRegenExpanded += oldFwd.stats.RetargetRegenExpanded
					fwd.stats.RetargetTotalExpanded += oldFwd.stats.RetargetTotalExpanded
					fwd.stats.RetargetSetupMs = oldFwd.stats.RetargetSetupMs + fwd.stats.RetargetSetupMs
				} else {
					fwd.stats.RetargetRuns = runs
				}
			}
		}
		if fwd != nil && !fwdDone {
			// The pull solver is idle: drain meetings posted to it.
			var met atomic.Bool // stops this lane's forward search only (not the portfolio: -all-finish)
			drain := startDrain(func() (string, bool) {
				sol, ok := ps.pullCheckMail()
				return sol, ok && checkSol(rows, sol)
			}, &met)
			fwd.stopCheck = drain.StopCheck()
			sol, r := fwd.towardRun(roundEnd(slice*time.Duration(festbFwdRatio), deadline), memLimit)
			joined := drain.Stop()
			nodes += r.Nodes
			prevNodes := 0
			for _, p := range fwd.prevTrees {
				prevNodes += len(p.player)
			}
			tr.SetExtra("retarget_runs", runs)
			tr.SetExtra("retarget_regen_added", fwd.stats.RetargetRegenAdded)
			tr.SetExtra("retarget_total_added", fwd.stats.RetargetTotalAdded)
			tr.SetExtra("retarget_regen_expanded", fwd.stats.RetargetRegenExpanded)
			tr.SetExtra("retarget_total_expanded", fwd.stats.RetargetTotalExpanded)
			tr.SetExtra("retarget_prev_nodes", prevNodes)
			tr.SetExtra("retarget_active_nodes", len(fwd.player))
			tr.SetExtra("retarget_setup_ms", fwd.stats.RetargetSetupMs)
			if sol != "" {
				return Result{Solved: true, Solution: sol, Nodes: nodes}
			}
			if joined != "" {
				return Result{Solved: true, Solution: joined, Nodes: nodes}
			}
			fwdDone = r.Reason != "timeout"
		}
		if backDone && (fwd == nil || fwdDone) {
			break
		}
	}
	tr.SetExtra("rounds_back_done", backDone)
	if sol, ok := ps.pullCheckMail(); ok && checkSol(rows, sol) {
		return Result{Solved: true, Solution: sol, Nodes: nodes}
	}
	if RetainFwdMail && fwd != nil {
		if sol, ok := fwd.checkMail(); ok && checkSol(rows, sol) {
			return Result{Solved: true, Solution: sol, Nodes: nodes}
		}
	}
	return Result{Reason: "festb ended", Nodes: nodes}
}
