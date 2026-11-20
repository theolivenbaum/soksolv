package main

import (
	"container/heap"
	"fmt"
	"slices"
	"soksolv/meet"
	"soksolv/memtrace"
	"soksolv/reverse"
	"time"

	"soksolv/trace"
)

type fessEntry struct {
	node, move, weight int32
	version            uint32
	score              moveScore
}
type fessHeap []fessEntry

func (h fessHeap) Len() int { return len(h) }
func (h fessHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.weight != b.weight {
		return a.weight < b.weight
	}
	if c := a.score.full().compare(b.score.full()); c != 0 {
		return c < 0
	}
	if a.node != b.node {
		if Seed != 0 { // seeded tie-breaking (exp-195): another run of the same search
			return seedHash(a.node) < seedHash(b.node)
		}
		return a.node < b.node
	}
	return a.move < b.move
}

// Seed (exp-195, 0: off) perturbs FESS's tie-breaking among equally ranked
// nodes and moves: each seed is a different deterministic run of the same
// search (restarts against heavy-tailed run times, Gomes et al.).
var Seed uint64

func seedHash(x int32) uint64 {
	z := uint64(x) + Seed*0x9E3779B97F4A7C15
	z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
	z = (z ^ z>>27) * 0x94D049BB133111EB
	return z ^ z>>31
}
func (h fessHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *fessHeap) Push(x any)   { *h = append(*h, x.(fessEntry)) }
func (h *fessHeap) Pop() any     { a := *h; x := a[len(a)-1]; *h = a[:len(a)-1]; return x }

type fessFrontier struct {
	cells  []featureCell
	queues []fessHeap
	next   int
	turn   int
	best   int32 // best-so-far node (progressBetter), -1 before the root
	// lastTurn (exp-477 trace only): which schedule picked the last move:
	// 'T' type-based, 'B' best cell, 'R' round robin.
	lastTurn byte
	retry    bool // exp-477 -fx-dupskip: the last pick was a duplicate; redo its turn
	lastCell int
}

func (f *fessFrontier) bytes() int {
	n := cap(f.cells)*32 + cap(f.queues)*24
	for _, q := range f.queues {
		n += cap(q) * 32
	}
	return n
}

func cellBetter(a, b featureCell) bool {
	if FXFestOrder {
		return progressBetter(a, b) // exp-477: Festival's best_past order
	}
	if a.oop != b.oop {
		return a.oop < b.oop
	}
	if a.packed != b.packed {
		return a.packed > b.packed
	}
	if a.connectivity != b.connectivity {
		return a.connectivity < b.connectivity
	}
	return a.roomConnectivity < b.roomConnectivity
}

// A worse descendant competes in its best ancestor's cell, rather than
// obtaining a permanent allocation of computation for regression.
func (s *solver) project(cell featureCell, parent int32) int {
	better := cellBetter
	if parent >= 0 {
		best := s.frontier.cells[s.bucket[parent]]
		if !better(cell, best) {
			return int(s.bucket[parent])
		}
	}
	if s.cellIDs == nil {
		s.cellIDs = map[featureCell]int{}
	}
	if id, ok := s.cellIDs[cell]; ok {
		return id
	}
	id := len(s.frontier.cells)
	s.cellIDs[cell] = id
	s.frontier.cells = append(s.frontier.cells, cell)
	s.frontier.queues = append(s.frontier.queues, nil)
	if NewCellJump {
		// Festival: a newly created cell is the next one the round robin
		// visits, so a move that reaches new feature values is followed up
		// at once (the dive that solves easy levels in a few expansions).
		s.frontier.next = id
	}
	return id
}

// NewCellJump moves the round-robin pointer to each newly created cell.
var NewCellJump = true

func (s *solver) enqueue(id int32) {
	if s.dead[id] || s.mvStart[id] < 0 || s.mvLeft[id] == 0 {
		return
	}
	start, end := s.mvStart[id], s.mvStart[id]+int32(s.mvLen[id])
	k := start + int32(s.mvNext[id])
	for k < end && s.mv[k]&moveUsed != 0 {
		k++
	}
	if k == end {
		if s.mvLeft[id] > 0 {
			s.reopen = id // the window ran out: the caller re-opens the node
		}
		return
	}
	s.mvNext[id] = uint32(k - start)
	_, _, wd, _ := decodeMove(s.mv[k])
	e := fessEntry{id, k, s.w[id] + wd, s.version[id], s.mvScore[k]}
	heap.Push(&s.frontier.queues[s.bucket[id]], e)
}

var ResumeWindowFix = true

// deferReopen preserves an exhausted window across a cancelled slice. The
// version and bucket fields invalidate it on reparenting or retargeting.
func (s *solver) deferReopen(id int32) {
	if !ResumeWindowFix || s.reopen != id || s.dead[id] {
		return
	}
	heap.Push(&s.frontier.queues[s.bucket[id]], fessEntry{node: id, move: -2, weight: s.w[id], version: s.version[id], score: s.lazyScore(id)})
	s.reopen = -1
}

// resumeWindow regenerates the next window using the node's current state.
func (s *solver) resumeWindow(id int32) {
	if s.dead[id] || s.mvStart[id] < 0 || s.mvLeft[id] == 0 {
		return
	}
	s.mvOff[id] += s.mvLen[id]
	s.mvStart[id] = -1
	s.lazyOpenNode(id)
}

func (s *solver) lazyOpenNode(id int32) {
	if s.mvStart[id] >= 0 || s.dead[id] {
		return
	}
	if len(s.lazyBoxes) != int(s.n) {
		s.lazyBoxes = make([]int32, s.n)
	}
	boxes := s.lazyBoxes
	s.unpack(id, boxes)
	for _, b := range boxes {
		s.occ[b] = true
	}
	var ch int32
	for _, b := range boxes {
		ch += s.dist[b]
	}
	s.reach(s.player[id], s.mark)
	s.open(id, boxes, ch, s.stamp)
	s.propagateDead(id)
	s.enqueue(id)
	for s.reopen == id && !s.dead[id] && !s.expired() {
		s.reopen = -1
		s.mvOff[id] += s.mvLen[id]
		s.reach(s.player[id], s.mark)
		s.open(id, boxes, ch, s.stamp)
		s.propagateDead(id)
		s.enqueue(id)
	}
	s.deferReopen(id)
	for _, b := range boxes {
		s.occ[b] = false
	}
}

// openFrontierEntry handles both retargeted unopened nodes and deferred windows.
// It reports slice expiry so selection does not misreport exhaustion.
func (s *solver) openFrontierEntry(e fessEntry) bool {
	if e.move == -2 {
		s.resumeWindow(e.node)
	} else if s.mvStart[e.node] < 0 && !s.dead[e.node] {
		s.lazyOpenNode(e.node)
	}
	return s.expired()
}

// Each node contributes precisely its best unexpanded move. Because its
// moves are sorted by (delta weight, child features), the cell minimum over
// these entries equals the minimum over every unexpanded move in the cell.
func (s *solver) selectMove() (fessEntry, bool) {
	f := &s.frontier
	if f.retry {
		f.retry = false
		f.turn--
		if f.lastTurn == 'R' {
			f.next = f.lastCell
		}
	}
	// Festival alternates: one selection from the best non-empty cell (so a
	// line of progress is followed at once), then one in round-robin order
	// (so every cell keeps getting coverage).
	f.turn++
	if k := s.typesEvery(); k > 0 && f.turn%k == 1 && len(f.queues) > 0 {
		// exp-279: type-based exploration (Xie et al.): every PFTypes-th
		// selection takes a random move of a random non-empty cell.
		for range 8 {
			cell := int(s.rnd() % uint64(len(f.queues)))
			q := &f.queues[cell]
			if len(*q) == 0 {
				continue
			}
			e := heap.Remove(q, int(s.rnd()%uint64(len(*q)))).(fessEntry)
			if e.version == s.version[e.node] && uint32(cell) == s.bucket[e.node] {
				if e.move < 0 {
					if s.openFrontierEntry(e) {
						return fessEntry{}, false
					}
					continue
				}
				if s.mv[e.move]&moveUsed == 0 {
					f.lastTurn = 'T'
					return e, true
				}
			}
		}
	}
	if BestCellTurns && f.turn%2 == 0 && f.best >= 0 {
		// Every other selection: the cell of the best-so-far node (in
		// Festival's push-mode order: packed, connectivity, rooms, OOP).
		cell := s.bucket[f.best]
		if int(cell) < len(f.queues) {
			q := &f.queues[cell]
			for len(*q) > 0 {
				e := heap.Pop(q).(fessEntry)
				if e.version == s.version[e.node] && cell == s.bucket[e.node] {
					if e.move < 0 {
						if s.openFrontierEntry(e) {
							return fessEntry{}, false
						}
						continue
					}
					if s.mv[e.move]&moveUsed == 0 {
						f.lastTurn = 'B'
						return e, true
					}
				}
			}
		}
	}
	for range len(f.queues) {
		cell := f.next
		f.lastCell = cell
		f.next = (f.next + 1) % len(f.queues)
		q := &f.queues[cell]
		for len(*q) > 0 {
			e := heap.Pop(q).(fessEntry)
			if e.version == s.version[e.node] && uint32(cell) == s.bucket[e.node] {
				if e.move < 0 {
					if s.openFrontierEntry(e) {
						return fessEntry{}, false
					}
					continue
				}
				if s.mv[e.move]&moveUsed == 0 {
					f.lastTurn = 'R'
					return e, true
				}
			}
		}
	}
	return fessEntry{}, false
}

// progressBetter is Festival's push-mode order for the best-so-far node:
// more packed boxes, then lower connectivity, room connectivity, OOP.
func progressBetter(a, b featureCell) bool {
	if a.packed != b.packed {
		return a.packed > b.packed
	}
	if a.connectivity != b.connectivity {
		return a.connectivity < b.connectivity
	}
	if a.roomConnectivity != b.roomConnectivity {
		return a.roomConnectivity < b.roomConnectivity
	}
	return a.oop < b.oop
}

// PullBestFeasible (exp-252): the pull lane's best state (the forward
// target) must pass the box-to-start-square matching. Nodes are checked when
// opened, but the best is chosen when added: unopened infeasible states were
// targets the start provably cannot reach.
var PullBestFeasible = true

func (s *solver) feasibleNode(id int32) bool {
	boxes := make([]int32, s.n)
	s.unpack(id, boxes)
	return s.feasible(boxes)
}

// DebugApplied (tests only) sees every applied move and the new child (-1:
// rejected or already stored).
var DebugApplied func(s *solver, e fessEntry, cid int32)

// DebugExpand (tests only) sees every selected move before it is applied.
var DebugExpand func(s *solver, e fessEntry)

// Festival-HF experiments on the toward search (exp-254).
var (
	HFRegRel     = false // exp-460: connectivity counts only regions meeting the target's player region
	HFMatchFirst = false // states with no box-to-target matching rank last (Festival's first HF key)
	HFBestFest   = false // best-so-far in Festival's HF order: connectivity, imagine, hotspots
)

const hfNoMatch = 1000

// better is the best-so-far order.
func (s *solver) better(a, b featureCell) bool {
	if s.hfRank && HFBestFest {
		if a.oop != b.oop {
			return a.oop < b.oop
		}
		if a.packed != b.packed {
			return a.packed > b.packed
		}
		return a.connectivity < b.connectivity
	}
	return progressBetter(a, b)
}

// noteBest updates the best-so-far node with a newly added node.
func (s *solver) noteBest(id int32) {
	f := &s.frontier
	if id < 0 || s.dead[id] {
		return
	}
	for _, t := range s.strataT {
		t.offer(s, id)
	}
	if f.best < 0 || s.dead[f.best] || s.pfOn && pfBetter(s.pfBest[id], s.pfBest[f.best]) || !(s.pfOn) && s.hfRank && HFFestSched && hfBetter(s.hfBest[id], s.hfBest[f.best]) || !(s.hfRank && HFFestSched) && !(s.pfOn) && s.better(s.actual[id], s.actual[f.best]) {
		if s.pull && PullBestFeasible && !s.feasibleNode(id) {
			return // a backward state the start can't reach: no target
		}
		f.best = id
		s.lastImprove = time.Now()
	}
}

// StallFinish: when FESS's best state stops improving, try to finish from
// it with a short private backward search aimed at it.
var StallFinish = true

// StallScale scales StallFinish's wall-clock constants (1 s cap, 1 s stall
// floor) by budget/600 s (exp-349 -stall-scale); 1 keeps them unchanged.
var StallScale = 1.0

// finishFromBest runs a backward search from the solved position toward the
// best-so-far node; on success the solution is the path to that node plus
// the reversed pulls (the caller's solution is verified by the portfolio).
func (s *solver) finishFromBest(deadline time.Time) (string, bool) {
	best := s.frontier.best
	pm, pa := s.stats.PathMoves, s.stats.PathAdvisors
	prefix := s.reconstruct(best)
	s.stats.PathMoves, s.stats.PathAdvisors = pm, pa
	boxes := make([]int32, s.n)
	s.unpack(best, boxes)
	player := meet.EndCell(prefix, s.lv.Player, s.lv.W)
	until := time.Now().Add(min(time.Duration(float64(time.Second)*StallScale), time.Until(deadline)/10))
	rr, err := reverse.SolveToward(s.rows, boxes, player, prefix, until, 1<<62, reverse.Lane{Index: 3, Cells: reverse.RevCells, Private: true, Share: 0.1})
	s.stallRuns++
	s.tr.SetExtra("stall_finishes", s.stallRuns)
	if err == nil && rr.Solved {
		s.tr.SetExtra("finished_from_best", true)
		return rr.Solution, true
	}
	return "", false
}

// BestCellTurns alternates best-cell and round-robin selection.
var BestCellTurns = true

func (s *solver) expansionLimit() int64 {
	if s.nodeLimit > 0 {
		return s.nodeLimit
	}
	return NodeBudget
}

func (s *solver) expired() bool {
	if s.cancelled {
		return true
	}
	if limit := s.expansionLimit(); limit > 0 && (s.expandedN >= limit || Stop.Load()) {
		s.cancelled = true
		return true
	}
	if NodeBudget > 0 {
		return false // deterministic runs ignore wall time
	}
	if !s.deadline.IsZero() && (Stop.Load() || time.Now().After(s.deadline)) {
		s.cancelled = true
		return true
	}
	return false
}

func (s *solver) solve(deadline time.Time, memLimit uint64) (res Result) {
	s.memLimit = memLimit
	defer func() {
		if s.elasticHold > 0 {
			releaseElastic(s.laneID, s.elasticHold)
			s.elasticHold = 0
		}
	}()
	defer func() { s.finishTrace(res, int(s.expandedN)) }()
	s.deadline = deadline
	if s.resume != nil {
		// exp-278: a later round of the same search (Resumable).
		s.cancelled = false
		return s.loop(deadline, memLimit)
	}
	if s.expired() {
		return Result{Reason: "timeout", Stats: s.finalStats()}
	}
	planBudget := min(250*time.Millisecond, time.Until(deadline)/10)
	if Plan4 && PlanV2 || Plan4Macro && s.laneID == 1 {
		planBudget = time.Duration(float64(time.Until(deadline)) * Plan4Frac) // exp-527: a fraction, no fixed cap
	}
	if NodeBudget > 0 {
		planBudget = time.Hour // bounded by the planner's node caps only
	}
	if s.planCap > 0 && planBudget > s.planCap {
		planBudget = s.planCap // exp-645b: a factor sub-solve's planner gets a small slice of the lane's time
	}
	if s.prePlan != nil {
		s.plan = s.prePlan
		s.stats.Plan4Complete, s.stats.Plan4Swap = true, s.prePlanAt
		planBudget = 0
	}
	if planBudget > 0 && !s.pull && !s.noPlan { // pull mode has no packing plan (as in Festival)
		if s.plan == nil {
			if Plan4Macro && s.laneID == 1 && !PlanV2 {
				// exp-527: the macro lane plans with plan4 (Festival prefix packed)
				// an incomplete witness falls back to the lane's own planner
				// with its own packed semantics (exp-527 Tier 2 losses)
				t4 := time.Now()
				if Plan4Anytime && !Plan4MacroOwn {
					return Result{Reason: "plan4-anytime needs -plan4-macro-own", Stats: s.finalStats()}
				}
				if Plan4Anytime {
					s.plan = s.planAnytime(time.Now().Add(planBudget*4/5), 4096)
				} else {
					s.plan = s.buildPlan4x(time.Now().Add(planBudget*4/5), 4096, !Plan4MacroOwn)
				}
				s.stats.Plan4Ms = int(time.Since(t4).Milliseconds())
				if s.plan != nil {
					s.stats.Plan4Exp, s.stats.Plan4Complete = s.plan.expanded, s.plan.complete
				}
				if s.plan != nil && (s.plan.complete || !Plan4MacroOwn) {
					s.plan.festPacked = true
				} else {
					s.plan = s.buildPackingPlan(time.Now().Add(min(250*time.Millisecond, planBudget/5)), 4096)
				}
			} else {
				s.plan = s.buildPackingPlan(time.Now().Add(planBudget), 4096)
			}
		}
	}
	if PlanFile != "" {
		fp, err := loadPlanFile(PlanFile, s.lv)
		if err != nil {
			return Result{Reason: "plan-file: " + err.Error(), Stats: s.finalStats()}
		}
		if s.plan == nil {
			return Result{Reason: "plan-file: no own plan to carry it", Stats: s.finalStats()}
		}
		s.plan.fest = fp
	}
	s.installPlanStats()
	s.fa2PublishPlan() // exp-594
	s.pairOffsets()
	s.buildWalk() // static: walking distances between all floor cells
	s.dpair = make([]int8, len(s.lv.Wall)*len(s.poff))
	s.back, s.fwd = meet.Back.Load(), meet.Fwd.Load()
	if s.isolated {
		s.back, s.fwd = nil, nil // exp-645: a sub-level's states are not the real level's
	} else if s.pull {
		// A backward searcher: it publishes to the backward perimeter and
		// probes the forward one.
		s.back, s.fwd = meet.Fwd.Load(), meet.Back.Load()
	}
	start := slices.Clone(s.lv.Boxes)
	slices.Sort(start)
	var h0 int32
	for _, b := range start {
		if s.dist[b] < 0 {
			return Result{Reason: "box on dead square"}
		}
		h0 += s.dist[b]
		s.occ[b] = true
	}
	s.tr.Root(int(h0))
	if h0 == 0 && (s.targetRows == nil || s.completeRootTarget()) {
		clear(s.occ)
		return Result{Solved: true, Stats: s.finalStats()}
	}
	roots := []int32{s.lv.Player}
	if s.pull {
		roots = s.pullRoots // one root per player zone of the solved position
		if (s.pfOn || s.dive) && PFReach {
			s.pfReachInit() // exp-619: exact single-box reach in the start-square matching
		}
		if (s.pfOn || s.dive) && s.pfZoneDead() > 0 && len(roots) > 1 {
			roots = s.liveZoneRoots(roots) // exp-609: Festival drops start zones proven dead
		}
	}
	for _, rp := range roots {
		p := s.reach(rp, s.mark)
		st := s.stamp
		if id := s.add(start, p, -1, -1, -1, -1, nil, 0, h0, 0, -1); id >= 0 && !s.dive {
			s.open(id, start, h0, st)
			s.propagateDead(id)
			s.enqueue(id)
			s.deferReopen(id)
		}
	}
	s.frontier.best = 0
	s.searchStart, s.lastImprove = time.Now(), time.Now()
	clear(s.occ)
	s.resume = &resumeState{cur: make([]int32, s.n), child: make([]int32, s.n)}
	if s.dive {
		return s.diveLoop(deadline, memLimit) // exp-691
	}
	return s.loop(deadline, memLimit)
}

// resumeState holds the search loop's scratch between rounds (exp-278): a
// solver whose solve returned on its deadline continues where it stopped
// when solve is called again with a later one.
type resumeState struct{ cur, child []int32 }

func (s *solver) loop(deadline time.Time, memLimit uint64) (res Result) {
	s.memLimit = memLimit
	expanded := 0
	cur, child := s.resume.cur, s.resume.child
	var lastMem time.Time
	for {
		if s.expired() {
			return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
		}
		if s.memFull {
			return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
		}
		periodic := false
		if now := time.Now(); now.Sub(lastMem) >= 50*time.Millisecond {
			if s.stopCheck != nil && s.stopCheck() {
				return Result{Reason: "retarget", Nodes: expanded, Stats: s.finalStats()}
			}
			lastMem, periodic = now, true
		}
		if periodic {
			if s.pull {
				s.publishPullBest()
				if s.rback != nil {
					s.rback.step(s, false) // exp-761
				}
			}
			if sol, ok := s.checkMail(); ok {
				s.tr.SetExtra("met", "reverse")
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
			if trace.Detail {
				s.tr.Sample(expanded, len(s.player), s.trackedBytes())
			}
			tb := s.trackedBytes()
			s.trackedP.Store(tb)
			if memtrace.Enabled {
				memtrace.Publish(fmt.Sprintf("macro-o%d", s.owner), len(s.player), s.memBreakdown(), s.memLen())
			}
			backstopPct := uint64(88)
			if ElasticMem {
				backstopPct = 76
			}
			if s.overBudget(tb+s.peerBytes()) || s.backstopHit(memLimit, backstopPct, tb+s.peerBytes()) {
				return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
			}
		}
		if StallFinish && NodeBudget == 0 && s.rows != nil && s.stallTries < 3 && s.frontier.best > 0 {
			stalled := time.Since(s.lastImprove)
			if stalled > max(time.Duration(float64(time.Second)*StallScale), time.Since(s.searchStart)*15/100) {
				s.stallTries++
				s.lastImprove = time.Now()
				if sol, ok := s.finishFromBest(deadline); ok {
					return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
				}
			}
		}
		e, ok := s.selectMove()
		if !ok {
			if s.expired() {
				return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
			}
			return Result{Reason: "exhausted", Nodes: expanded, Stats: s.finalStats()}
		}
		if s.p4pend != nil && s.stepPlan4() {
			return Result{Reason: "replan", Nodes: expanded, Stats: s.finalStats()}
		}
		id := e.node
		if DebugExpand != nil {
			DebugExpand(s, e)
		}
		s.unpack(id, cur)
		var h int32
		for _, b := range cur {
			s.occ[b] = true
			h += s.dist[b]
		}
		expanded++
		s.expandedN++
		if s.bat != nil && s.expandedN&DiveShareMask == 0 && !s.bat.yield(s.batMe) {
			return Result{Reason: "stopped", Nodes: expanded, Stats: s.finalStats()} // exp-693: the shared lane stops
		}
		if len(s.prevTrees) > 0 {
			s.stats.RetargetTotalExpanded++
			packed := s.boxes[int(id)*s.nw : int(id+1)*s.nw]
			for _, prev := range s.prevTrees {
				if _, pid, _ := prev.find(packed, s.player[id]); pid >= 0 {
					s.stats.RetargetRegenExpanded++
					break
				}
			}
		}
		s.tr.Step(expanded, int(h), s.actual[id].packed)
		if bestDumpDir != "" && (s.laneID == 1 || s.laneID == 5) && !s.pull && !s.isolated {
			s.noteBestDump(id, cur, h) // exp-716 Gate 0 diagnostic
		}
		if relayPostOn && s.relayPoster() {
			s.relayNote(id, cur) // exp-744
		}
		s.reach(s.player[id], s.mark)
		st := s.stamp
		s.mv[e.move] |= moveUsed
		s.mvLeft[id]--
		s.addHF = int32(s.mvScore[e.move].hot)
		if s.pfOn {
			ms := s.mvScore[e.move]
			s.pfNext, s.pfHave = pfFromMove(ms), true
		}
		cid, ch := s.apply(id, s.mv[e.move]&^moveUsed, cur, child, h, st)
		if FXDupSkip && cid < 0 && s.lastAdded >= 0 {
			// exp-477: Festival never selects a move into an existing tree
			// node (set_best_move skips it): no node count, no schedule turn.
			expanded--
			s.expandedN--
			s.frontier.retry = true
		}
		s.addHF = -1
		s.pfHave = false
		s.recordEdge(id, s.lastAdded)
		if DebugApplied != nil {
			DebugApplied(s, e, cid)
		}
		s.propagateDead(id)
		s.reopen = -1
		s.enqueue(id)
		for s.reopen == id && !s.dead[id] && !s.expired() {
			// Next window of id's moves: the same deterministic order, from
			// the first move not stored before.
			s.mvOff[id] += s.mvLen[id]
			s.reach(s.player[id], s.mark)
			s.open(id, cur, h, s.stamp)
			s.reopen = -1
			s.enqueue(id)
		}
		s.deferReopen(id)
		for _, b := range cur {
			s.occ[b] = false
		}
		if len(s.meetPending) > 0 || len(s.pendCands) > 0 {
			s.postMeetings(cur)
		}
		if cid < 0 {
			continue
		}
		s.noteBest(cid)
		if s.tgt != nil && ch != 0 && s.onTarget(child) {
			if s.tgtFull == nil {
				ch = 0 // legacy box-only target
			} else if sol, ok := s.completeTarget(cid); ok {
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
		}
		if ch == 0 && s.targetRows != nil {
			if sol, ok := s.completeTarget(cid); ok {
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
		}
		if ch == 0 && s.pull {
			// All boxes on start squares: solved if the real player is in
			// this state's region too.
			for _, b := range child {
				s.occ[b] = true
			}
			s.reach(s.player[cid], s.mark)
			home := s.mark[s.realPlayer] == s.stamp
			for _, b := range child {
				s.occ[b] = false
			}
			if !home {
				ch = -1 // not the start position; keep searching
			} else if sol := s.pullSolution(cid); sol != "" {
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
		}
		if ch == 0 && s.targetRows == nil {
			solution := s.reconstruct(cid)
			if solution != "" {
				return Result{Solved: true, Solution: solution, Nodes: expanded, Stats: s.finalStats()}
			}
			if s.expired() {
				return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
			}
			return Result{Solved: true, Solution: solution, Nodes: expanded, Stats: s.finalStats()}
		}
		// Open a child before publishing its frontier entry: no optimistic
		// parent-weight keys or stale alternate queues affect move choice.
		if s.mvStart[cid] < 0 {
			for _, b := range child {
				s.occ[b] = true
			}
			s.reach(s.player[cid], s.mark)
			s.open(cid, child, ch, s.stamp)
			s.propagateDead(cid)
			for _, b := range child {
				s.occ[b] = false
			}
		}
		s.enqueue(cid)
		s.deferReopen(cid)
	}
}

// finishTrace reports how the search ended and what its frontier looked like.
func (s *solver) finishTrace(res Result, expanded int) {
	if s.tr == nil {
		return
	}
	f := &s.frontier
	open, maxQ := 0, 0
	for _, q := range f.queues {
		if len(q) > 0 {
			open++
		}
		maxQ = max(maxQ, len(q))
	}
	if s.dz != nil {
		s.tr.SetExtra("dz", s.dz.Stats)
	}
	s.tr.SetExtra("cells", len(f.cells))
	s.tr.SetExtra("open_cells", open)
	if s.refN > 0 {
		s.tr.SetExtra("refs_n", s.refN) // exp-659: candidate references held
		s.tr.SetExtra("refs_mb", len(s.refs)>>17)
	}
	s.tr.SetExtra("max_cell_queue", maxQ)
	if len(f.cells) > 0 {
		best := f.cells[0]
		for _, c := range f.cells[1:] {
			if cellBetter(c, best) {
				best = c
			}
		}
		s.tr.SetExtra("best_cell", [4]int{best.oop, best.packed, best.connectivity, best.roomConnectivity})
	}
	if s.plan != nil {
		s.tr.SetExtra("plan_complete", s.plan.complete)
		s.tr.SetExtra("plan_obligations", PackingObligations && s.plan.dag != nil)
		if s.plan.dag != nil {
			s.tr.SetExtra("plan_events", len(s.plan.dag.events))
			s.tr.SetExtra("plan_dependencies", s.plan.dag.edges)
		}
	}
	if res.Solved {
		s.tr.SetExtra("path_advisors", res.Stats.PathAdvisors)
		s.tr.SetExtra("path_moves", res.Stats.PathMoves)
	}
	s.tr.Finish(trace.Ended(res.Solved, res.Reason), expanded, len(s.player), s.trackedBytes())
}

// NodeBudget (> 0) makes a single searcher deterministic for screening:
// it stops after this many expansions and ignores wall-clock limits,
// stall finishing and the learner (-nodes; never in timed benchmarks).
var NodeBudget int64

// PFTypes (exp-279): one selection in this many is type-based exploration in
// Festival-ordered pull search (0: off). Plain pull search has none.
var PFTypes = 8

func (s *solver) typesEvery() int {
	switch {
	case s.reviveTypes > 0 && !s.pfOn:
		return s.reviveTypes // exp-588: a revived lane explores differently from its first run
	case !s.pull:
		return 0
	case s.pfOn:
		return s.pfSty.types
	}
	return 0
}

// rnd is a deterministic xorshift generator.
func (s *solver) rnd() uint64 {
	if s.rng == 0 {
		s.rng = 0x9e3779b97f4a7c15
	}
	s.rng ^= s.rng << 13
	s.rng ^= s.rng >> 7
	s.rng ^= s.rng << 17
	return s.rng
}

// installPlanStats compiles the plan's obligation DAG (when enabled) and records plan stats.
func (s *solver) installPlanStats() {
	if s.plan != nil {
		if PackingObligations {
			s.plan.dag = s.compilePackingDAG(s.plan)
			s.stats.PlanCompileVisits = s.plan.causalVisits
			if s.plan.dag != nil {
				s.stats.PlanEvents = len(s.plan.dag.events)
				s.stats.PlanDependencies = s.plan.dag.edges
			}
		}
		s.stats.PlanExpanded = s.plan.expanded
		s.stats.PlanPhases = len(s.plan.phases)
		s.stats.PlanParkings = s.plan.parkings
		s.stats.PlanRemaining = s.plan.remaining
		s.stats.PlanComplete = s.plan.complete
	}
}
