package main

// exp-686: the hub race (research/exp-683/REPORT.md 3.1). With -hub-race the
// hub-pull lane no longer aims one forward search at the pull tree's single
// best state. It keeps the best tip per stratum (hubstrata.go) and races them:
// each trial is a fresh toward search with an expansion budget (4k, doubled
// while the best (boxes on hub, matching distance) improved in the window's
// second half), never-tried strata first, then the most progress per
// expansion; pull and trial expansions alternate (pull gets 1/festbFwdRatio
// of the forward expansions). A failed stratum is the first to be replaced
// by a new signature (preference, not prune). Strata tried by either hub lane
// go into one shared table keyed by the v0 signature of the hub's boxes.
// Nothing depends on the time limit.

import (
	"math/bits"
	"sync"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/trace"
)

var (
	HubRace         = true        // -hub-race (exp-686 promoted 2026-11-12)
	HubNoSpin       = false       // -hub-nospin (exp-754)
	HubIdleRing     = true        // -hub-idle-ring (exp-758; default on since its promotion 2026-11-14; -hub-idle-ring=false = old path)
	HubIdleK        = 4           // -hub-idle-k: idle rounds at the back-off cap before an idle hub config ends
	HubRaceSig      = 0           // -hub-race-sig: strata signature for hub-pull (0 rooms/goals, 1 half-depth ancestor)
	HubRaceUnit     = int64(4096) // -hub-race-unit: first trial budget in forward expansions
	HubRaceTrials   = true        // -hub-race-trials: run the strata trials (false: base pursuit only, the ablation)
	HubRaceBase     = true        // -hub-race-base: half the forward expansions keep the old pursuit of frontier.best (one persistent forward tree, retargeted)
	HubRaceBaseFrac = 0.8         // -hub-race-base-frac (exp-686b, promoted 2026-11-12: 0.8; 0.5 was exp-686)
)

// TrackedRace: hub-pull's race trial tree (its base tree charges Tracked10).
var TrackedRace atomic.Uint64

// raceShared is the tried-strata table both hub lanes read and write.
type raceRec struct {
	tries int32
	best  float32 // best fraction of the start-to-hub gap closed
}

var raceShared = struct {
	sync.Mutex
	m map[uint64]raceRec
}{m: map[uint64]raceRec{}}

func raceSharedGet(k uint64) (raceRec, bool) {
	raceShared.Lock()
	defer raceShared.Unlock()
	r, ok := raceShared.m[k]
	return r, ok
}

func raceSharedMark(k uint64, frac float32) {
	raceShared.Lock()
	defer raceShared.Unlock()
	r := raceShared.m[k]
	r.tries++
	r.best = max(r.best, frac)
	raceShared.m[k] = r
}

// boxSig is the v0 signature of a box set (real goals and rooms of s, pull
// or push view: rooms depend only on walls).
func boxSig(goal []bool, room []int, cnt []int32, boxes []int32) uint64 {
	clear(cnt)
	on := 0
	for _, b := range boxes {
		if goal[b] {
			on++
		}
		r := -1
		if room != nil {
			r = room[b]
		}
		if r >= 0 {
			cnt[r+1]++
		} else {
			cnt[0]++
		}
	}
	h := uint64(14695981039346656037) ^ uint64(on)
	h *= 1099511628211
	for _, c := range cnt {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// trial state per stratum slot (parallel to strata.sig/best).
type raceSlot struct {
	sig    uint64 // the stratum this slot belongs to (strata slots are reused)
	tip    int32  // tip tried last (-1: never)
	tries  int32
	exp    int64   // forward expansions spent on this stratum
	budget int64   // next trial's budget
	prog   float32 // best fraction of the gap closed
}

type hubRaceStats struct {
	trials, retries, reached, extensions, sharedSkip, baseRuns int
	baseExp                                                    int64
	exp, backExp                                               int64
	maxStrata                                                  int
}

func raceAlternate(ps *solver, rows []string, lv *Level, deadline time.Time, memLimit uint64, tr *trace.Recorder, fwdTracked *atomic.Uint64, owner int) Result {
	ps.peerTracked = fwdTracked
	ps.peerTracked2 = &TrackedRace
	defer fwdTracked.Store(0)
	defer TrackedRace.Store(0)
	var bf *solver // the base forward tree (aims at frontier.best)
	var cur *meet.Target
	baseDone := false
	baseQuota := HubRaceUnit
	st := newStrata(ps, HubRaceSig)
	ps.strataT = []*strata{st}
	v0 := st
	if HubRaceSig != 0 {
		v0 = newStrata(ps, 0) // scratch for the shared key only
	}
	var slots []raceSlot
	var S hubRaceStats
	report := func() {
		tr.SetExtra("race_trials", S.trials)
		tr.SetExtra("race_retries", S.retries)
		tr.SetExtra("race_reached", S.reached)
		tr.SetExtra("race_ext", S.extensions)
		tr.SetExtra("race_shared_skip", S.sharedSkip)
		tr.SetExtra("race_fwd_exp", S.exp)
		tr.SetExtra("race_back_exp", ps.expandedN)
		tr.SetExtra("race_strata", len(st.sig))
		tr.SetExtra("race_strata_added", st.added)
		tr.SetExtra("race_base_runs", S.baseRuns)
		tr.SetExtra("race_base_exp", S.baseExp)
	}
	defer report()
	backDone := false
	// Pull and forward alternate in wall time, pull 1/festbFwdRatio of the
	// forward time (festbAlternate's split, without its doubling slices).
	pullSlice := festbUnit
	fwdT := time.Duration(0)
	nodes := 0
	tipBoxes := make([]int32, ps.n)
	var rback *relayBackState // exp-761 -relay-back
	if owner == FestXLane {
		rback = newRelayBack(ps, lv)
		ps.rback = rback
	}
	live := func() bool { return !Stop.Load() && (deadline.IsZero() || time.Now().Before(deadline)) }
	for live() {
		// 1. Pull slice.
		if !backDone && (ps.resume == nil || fwdT >= pullSlice*time.Duration(festbFwdRatio) || len(st.sig) == 0) {
			if ps.resume != nil {
				pullSlice = max(festbUnit, fwdT/time.Duration(festbFwdRatio))
			}
			r := ps.solve(roundEnd(pullSlice, deadline), memLimit)
			ps.tr.SetExtra("pull_exp_cum", ps.expandedN)
			if r.Solved {
				return r
			}
			backDone = r.Reason != "timeout"
			fwdT = 0
			if backDone {
				rback.step(ps, true)
			}
		}
		if ps.resume == nil {
			return Result{Reason: "festb: backward search did not start"}
		}
		if !live() {
			break
		}
		for len(slots) < len(st.sig) {
			slots = append(slots, raceSlot{tip: -1, budget: HubRaceUnit, sig: st.sig[len(slots)]})
		}
		for i := range slots {
			if slots[i].sig != st.sig[i] {
				slots[i] = raceSlot{tip: -1, budget: HubRaceUnit, sig: st.sig[i]}
			}
		}
		S.maxStrata = max(S.maxStrata, len(st.sig))
		// 2a. Base: the old hub-pull pursuit of the pull best, as many
		// expansions as the last strata trial.
		if HubRaceBase {
			if tgt := ps.bestTarget(); tgt != nil && (cur == nil || !sameTarget(tgt, cur)) {
				cur, baseDone = tgt, false
				S.baseRuns++
				if RetargetReuse && bf != nil && !bf.memFull && len(bf.player) > 0 && len(bf.player) <= 16384 {
					if sol, ok := bf.retargetTo(tgt); ok {
						return Result{Solved: true, Solution: sol, Nodes: nodes}
					}
				} else {
					fwdTracked.Store(0)
					bf = newTowardReal(rows, lv, tgt, deadline, tr, ps.budgetP, fwdTracked, owner, 0)
					bf.peerTracked, bf.peerTracked2 = ps.trackedP, &TrackedRace
					bf.laneID = 10
				}
			}
			if bf != nil && !baseDone {
				var met atomic.Bool
				drain := startDrain(func() (string, bool) {
					sol, ok := ps.pullCheckMail()
					return sol, ok && checkSol(rows, sol)
				}, &met)
				bf.stopCheck = drain.StopCheck()
				before, t0 := bf.expandedN, time.Now()
				bf.nodeLimit = bf.expandedN + baseQuota
				sol, r := bf.towardRun(deadline, memLimit)
				joined := drain.Stop()
				bf.stopCheck = nil
				used := bf.expandedN - before
				S.baseExp += used
				fwdT += time.Since(t0)
				nodes += int(used)
				if sol == "" {
					sol = joined
				}
				if sol != "" {
					report()
					tr.SetExtra("race_win", "base")
					return Result{Solved: true, Solution: sol, Nodes: nodes}
				}
				baseDone = r.Reason != "timeout"
				if !live() {
					break
				}
			}
		}
		// 2. Pick: never-tried first (best pull order), then progress per expansion.
		pick := -1
		for i := range st.sig {
			if slots[i].tip >= 0 || st.failed[i] {
				continue
			}
			ps.unpack(st.best[i], tipBoxes)
			if r, ok := raceSharedGet(boxSig(v0.goal, roomCells(ps), v0.cnt, tipBoxes)); ok && r.tries > 0 {
				// tried by the other lane: treat as tried with its progress
				slots[i] = raceSlot{sig: st.sig[i], tip: st.best[i], tries: r.tries, budget: HubRaceUnit * 2, prog: r.best, exp: HubRaceUnit}
				S.sharedSkip++
				continue
			}
			if pick < 0 || pfBetter(ps.pfBest[st.best[i]], ps.pfBest[st.best[pick]]) {
				pick = i
			}
		}
		if pick < 0 {
			bestScore := -1.0
			for i := range st.sig {
				sl := &slots[i]
				sc := (float64(sl.prog) + 0.01) / float64(bits.Len64(uint64(sl.exp+1))) / float64(sl.tries+1)
				if st.best[i] != sl.tip {
					sc *= 2 // a newer tip in a tried stratum
				}
				if sc > bestScore {
					bestScore, pick = sc, i
				}
			}
			if pick >= 0 {
				S.retries++
			}
		}
		if !HubRaceTrials {
			pick = -1
		}
		if pick < 0 {
			if backDone {
				break
			}
			fwdT = pullSlice * time.Duration(festbFwdRatio) // nothing to try: pull again
			continue
		}
		// 3. Trial.
		sl := &slots[pick]
		tip := st.best[pick]
		if sl.tip != tip {
			sl.budget = max(sl.budget, HubRaceUnit)
		}
		ps.unpack(tip, tipBoxes)
		key := boxSig(v0.goal, roomCells(ps), v0.cnt, tipBoxes)
		tgt := &meet.Target{Boxes: append([]int32(nil), tipBoxes...), Player: ps.player[tip], Tail: ps.pullTail(tip)}
		S.trials++
		dumpHub(lv, "hub-pull", tgt, ps.expandedN, S.trials, true)
		t0 := time.Now()
		sol, prog, used, reached, ext := raceTrial(ps, rows, lv, tgt, deadline, memLimit, tr, &TrackedRace, owner, sl.budget, sl.budget*int64(min(sl.tries, 1)*3+1))
		S.exp += used
		baseQuota = max(int64(float64(used)*HubRaceBaseFrac/(1-HubRaceBaseFrac)), HubRaceUnit) // exp-686b: base share of forward expansions
		S.extensions += ext
		fwdT += time.Since(t0)
		nodes += int(used)
		if sol != "" {
			report()
			tr.SetExtra("race_win", "trial")
			tr.SetExtra("race_win_trial", S.trials)
			return Result{Solved: true, Solution: sol, Nodes: nodes}
		}
		if reached {
			S.reached++
		}
		sl.tip, sl.tries, sl.exp = tip, sl.tries+1, sl.exp+used
		sl.prog = max(sl.prog, prog)
		sl.budget = max(sl.budget*2, used*2)
		st.failed[pick] = true
		raceSharedMark(key, prog)
		if S.trials%8 == 0 {
			report()
		}
	}
	if sol, ok := ps.pullCheckMail(); ok && checkSol(rows, sol) {
		return Result{Solved: true, Solution: sol, Nodes: nodes}
	}
	return Result{Reason: "festb ended", Nodes: nodes}
}

func roomCells(s *solver) []int {
	if s.rooms == nil {
		return nil
	}
	return s.rooms.cell
}

// raceTrial runs one forward trial at tgt with an expansion budget, doubled
// while the best (boxes on hub, matching distance) improved in the window's
// second half (at most maxExp in all). It returns the solution (if any), the fraction of the gap
// closed, the expansions used, whether the hub's boxes were all reached and
// the number of extensions.
func raceTrial(ps *solver, rows []string, lv *Level, tgt *meet.Target, deadline time.Time, memLimit uint64, tr *trace.Recorder, fwdTracked *atomic.Uint64, owner int, budget, maxExp int64) (string, float32, int64, bool, int) {
	fwdTracked.Store(0)
	f := newTowardReal(rows, lv, tgt, deadline, tr, ps.budgetP, fwdTracked, owner, budget)
	f.peerTracked = ps.trackedP
	if fwdTracked == &TrackedRace {
		f.peerTracked2 = &Tracked10 // the base tree
	}
	f.laneID = 10
	if owner == FestBLane {
		f.laneID = 8
	}
	defer fwdTracked.Store(0)
	startOn := int32(0)
	for _, b := range lv.Boxes {
		if f.tgt[b] {
			startOn++
		}
	}
	bestOn, bestDist := startOn, int32(1<<30)
	lastImpr := int64(0)
	scanned := 0
	win := budget
	ext := 0
	for {
		var met atomic.Bool
		drain := startDrain(func() (string, bool) {
			sol, ok := ps.pullCheckMail()
			return sol, ok && checkSol(rows, sol)
		}, &met)
		f.stopCheck = drain.StopCheck()
		sol, r := f.towardRun(deadline, memLimit)
		joined := drain.Stop()
		f.stopCheck = nil
		if sol == "" {
			sol = joined
		}
		for id := scanned; id < len(f.hfOwn); id++ {
			t := f.hfOwn[id]
			if t.on > bestOn || t.on == bestOn && !t.inf && t.dist < bestDist {
				bestOn, bestDist = t.on, t.dist
				lastImpr = f.expandedN
			}
		}
		scanned = len(f.hfOwn)
		n := int32(ps.n)
		frac := float32(0)
		if n > startOn {
			frac = float32(bestOn-startOn) / float32(n-startOn)
		}
		reached := bestOn >= n
		if sol != "" {
			return sol, 1, f.expandedN, true, ext
		}
		if r.Reason == "timeout" && f.expandedN >= f.nodeLimit && !f.memFull && lastImpr >= f.nodeLimit-win/2 && f.nodeLimit+2*win <= maxExp && !Stop.Load() && (deadline.IsZero() || time.Now().Before(deadline)) {
			win *= 2
			f.nodeLimit += win
			ext++
			continue
		}
		return "", frac, f.expandedN, reached, ext
	}
}
