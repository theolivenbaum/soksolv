package main

import (
	"math/bits"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/trace"
)

// exp-451: the hub lane (design: research/exp-450/DESIGN.md). Festival makes
// one blind hub per strategy; this lane keeps ONE growing festb pull tree and
// harvests many loose hub candidates from it, picks the one the start can
// plausibly reach, and commits a forward context to it for a window that
// doubles while it keeps closing (Luby-style restarts). exp-640 removed the
// T-gate -hub-from and the festb prefix -hub-prefix (both 0 since exp-502).

var (
	hubOn            = true // exp-480: the festb slot runs hubLane
	hubGenCap        = 0.75 // exp-469: >0: generator cap as a fraction of the lane memory budget (0: budget/2 - min(tracked, budget/4))
	hubCtxN          = 2    // live forward contexts (1: single-context ablation)
	hubRegRel        = true // exp-480: hub forward contexts count player regions relative to their target (exp-460 -hf-reg-rel, scoped to the hub closer)
	HubFallbackCheck = true // exp-680: the tier-4 fallback gets the pool's start checks (exp-686 promoted 2026-11-12)
	hubInject        string // diagnostics: boards (Festival's hubs, exp-449 format) committed first, as a positive control
)

const (
	hubPoolMax  = 64 // array size; poolCap() is the live cap (32; -hub-race: strataMax)
	hubTriedMax = 64
	hubRecMax   = 32
	hubSlots    = 2

	hubFree       = 0
	hubInfeasible = 1
)

// hubCand is a pool entry: a node of the pull tree.
type hubCand struct {
	id, cut, pinned, dist, lb, trapped int32 // trapped: boxes on goals no player can pull (Festival's hubs: 0-2)
	tier, state                        uint8
	pocket                             bool // exp-453: the hub has sealed pockets besides its player region (-hub-gen-pockets)
}

// keyLess is the admission order (smaller is better): tier, log2 of the cut,
// pinned boxes, then deeper (larger matching distance from the goals).
func (c *hubCand) keyLess(o *hubCand) bool {
	if c.tier != o.tier {
		return c.tier < o.tier
	}
	if c.trapped != o.trapped { // exp-453: trapped goal boxes (Festival's hubs: 0-2) before the cut
		return c.trapped < o.trapped
	}
	if a, b := bits.Len32(uint32(c.cut)), bits.Len32(uint32(o.cut)); a != b {
		return a < b
	}
	if c.pinned != o.pinned {
		return c.pinned < o.pinned
	}
	if c.dist != o.dist {
		return c.dist > o.dist
	}
	return c.id < o.id
}

// hubCtx is one committed forward context.
type hubCtx struct {
	fwd         *solver
	tracked     *atomic.Uint64
	cand        hubCand
	tailLen     int
	start       time.Time     // wall time of the commitment
	fwdTime     time.Duration // forward time spent so far (its own)
	window      time.Duration // current window; ends when fwdTime reaches it
	winExp      int64         // exp-686 -hub-race: window in forward expansions
	lastImprExp int64
	lastImpr    time.Duration // fwdTime at the last progress
	scanned     int32         // forward nodes already read for progress
	bestOn      int32
	bestDist    int32
	reached     bool
	firstOn     time.Duration
	rec         int // index of its record, -1 if over the cap
}

type hubRec struct {
	TMs     int64  `json:"t_ms"`
	Tier    uint8  `json:"tier"`
	Cut     int32  `json:"cut"`
	Pinned  int32  `json:"pinned"`
	Trapped int32  `json:"trapped_goal"`
	Hot     int32  `json:"hot"`
	Dist    int32  `json:"dist"`
	LB      int32  `json:"lb"`
	TailLen int    `json:"tail_len"`
	BestOn  int32  `json:"best_on"`
	N       int    `json:"n"`
	Pocket  bool   `json:"pocket"`
	FwdExp  int64  `json:"fwd_exp"`
	FwdMs   int64  `json:"fwd_ms"`
	End     string `json:"end"`
}

type hub struct {
	ps       *solver
	rows     []string
	lv       *Level
	deadline time.Time
	memLimit uint64
	tr       *trace.Recorder
	owner    int
	trackeds [hubSlots]*atomic.Uint64
	t0       time.Time

	pool    [hubPoolMax]hubCand
	sigBuf  []int32 // exp-686 -hub-race: signature scratch
	sigCnt  []int32
	sigGoal []bool
	nPool   int
	tried   [hubTriedMax]int32
	nTried  int
	scan    int32
	delta   int
	maxD    [4]int32
	buf     []int32
	ctxs    [hubSlots]*hubCtx

	injects []*meet.Target // -hub-inject boards, committed first (no tail: reach is measured, not solved)
	injCur  *meet.Target
	U       time.Duration

	// counters
	nominated, committed, reached, abandoned, extensions int
	memfull, exhausted, tailFail                         int
	infeasible, scanSkipped, scanned, eligible, reachRej int
	nominateNs, scanNs                                   int64
	firstReachMs                                         int64
	tierCount                                            [5]int
	tierSeen                                             [5]int
	nReg1, minRooms, minHot0, minHot                     int
	bestOnAll                                            int32
	recs                                                 []hubRec
	lastHot, lastPocket                                  int32    // set by score: unpullable boxes on the player region, sealed pocket cells
	gen                                                  *hubGen  // -hub-back G: the start-free generator replaces the pull tree as the candidate source
	fallbackRej                                          int      // exp-680 -hub-fallback-check: empty-pool fallbacks rejected
	rounds, idle                                         int      // exp-754: rounds; idle rounds (no forward slice, no generator expansion, scan caught up)
	idleFirstMs                                          int64    // exp-754: lane time of the first idle round
	idleDone, idlePaused                                 int      // exp-754: idle rounds with the pull tree finished / paused on memory
	idleSleepMs                                          int64    // exp-754 -hub-nospin: time slept in idle rounds
	idleCapRun                                           int      // exp-758 -hub-idle-ring: consecutive idle rounds at the back-off cap
	candSeen                                             []uint64 // exp-758 -hub-idle-ring: g.cand indices the harvest has visited (bitset)
	nSeen, rescans                                       int      // exp-758: visited candidates; rescan passes started
	rescanOn                                             bool     // exp-758: the harvest is revisiting the candidates a strided scan skipped
	idleEnd                                              string   // exp-758: why the config ended idle ("zero-cand", "idle")
}

// hubIdleSleepMs: time the hub-gen lane slept in idle rounds, over all its configs (exp-758).
var hubIdleSleepMs atomic.Int64

// hubRingEnds: hub-gen configs that ended idle in this run (exp-758).
var hubRingEnds atomic.Int64

func (h *hub) ms() int64 { return time.Since(h.t0).Milliseconds() }

// hubLane is the festb slot's lane when -hub is on.
func hubLane(ps *solver, rows []string, lv *Level, deadline time.Time, memLimit uint64, tr *trace.Recorder, fwdTracked *atomic.Uint64, owner int) Result {
	t0 := time.Now()
	total := time.Duration(0) // T (0: no limit)
	if !deadline.IsZero() {
		total = time.Until(deadline)
	}
	r := Result{Reason: "festb ended"} // the generator starts at once (exp-640: no festb prefix)
	if Stop.Load() || !deadline.IsZero() && !time.Now().Before(deadline) {
		return r
	}
	h := &hub{ps: ps, rows: rows, lv: lv, deadline: deadline, memLimit: memLimit, tr: tr, owner: owner, t0: t0}
	h.trackeds[0] = fwdTracked
	h.trackeds[1] = &Tracked11
	ps.peerTracked2 = &Tracked11
	h.delta = max(2, ps.n/8)
	h.buf = make([]int32, ps.n)
	if h.gen = newHubGen(ps); h.gen == nil {
		return r
	}
	if b := ps.budgetP.Load(); b > 0 {
		h.gen.capBytes = b/2 - min(ps.trackedP.Load(), b/4)
		if hubGenCap > 0 {
			h.gen.capBytes = uint64(hubGenCap * float64(b))
		}
	}
	h.minRooms, h.minHot0, h.minHot = 1<<30, 1<<30, 1<<30
	if hubInject != "" {
		h.injects = loadHubBoards(hubInject, lv)
	}
	defer func() {
		fwdTracked.Store(0)
		Tracked11.Store(0)
		h.report("end")
	}()
	if hubGenOnly {
		return h.genOnly()
	}
	return h.run(total)
}

// hubUnit is the commitment unit U.
func hubUnit(total time.Duration) time.Duration {
	if total <= 0 {
		return 10 * time.Second
	}
	return min(max(total/12, 2*time.Second), 30*time.Second)
}

func (h *hub) run(total time.Duration) Result {
	ps := h.ps
	U := hubUnit(total)
	h.U = U
	q := U / 4
	nodes := 0
	backDone := false
	lastRound := time.Second // first scan may take up to 1.5 s (the prefix tree)
	firstScan := true
	h.report("start")
	// Festival's backward phase first: the generator runs alone before the first nomination.
	burst := time.Duration(float64(max(total, 0)) * hubGenInit)
	if total <= 0 {
		burst = 10 * time.Second
	}
	backDone = h.genSlice(burst)
	if HubIdleRing && len(h.gen.cand) == 0 && !Stop.Load() && (h.deadline.IsZero() || time.Now().Before(h.deadline)) {
		// exp-758 class I: the burst left no one-region candidate, so the
		// lane would only nominate a fallback and idle; end the config.
		h.idleEnd = "zero-cand"
		h.report("idle-end")
		return Result{Reason: "hub idle", Nodes: nodes}
	}
	idleWait := time.Duration(0) // exp-754 -hub-nospin: back-off after idle rounds
	for round := 0; !Stop.Load() && (h.deadline.IsZero() || time.Now().Before(h.deadline)); round++ {
		roundStart := time.Now()
		genBefore, ranFwd, paused := h.gen.expanded, false, false
		// 1. Harvest and nominate.
		budget := lastRound / 20
		if firstScan {
			budget, firstScan = 1500*time.Millisecond, false
		}
		if h.rescanOn {
			budget = 500 * time.Millisecond // exp-758: the lane is otherwise idle
		}
		h.harvest(budget)
		live := 0
		for i := range hubSlots {
			if h.ctxs[i] != nil {
				live++
			}
		}
		for i := 0; i < min(hubCtxN, hubSlots); i++ {
			if h.ctxs[i] != nil {
				continue
			}
			// The second context waits until the first has survived a window.
			if i == 1 && (h.ctxs[0] == nil || !HubRace && h.ctxs[0].window == h.U || HubRace && h.ctxs[0].winExp == HubRaceUnit) {
				continue
			}
			if c := h.nominate(); c != nil {
				h.commit(i, c)
				live++
			}
		}
		// 2. Forward slices.
		sumFwd := time.Duration(0)
		for i := range hubSlots {
			c := h.ctxs[i]
			if c == nil {
				continue
			}
			slice := q
			t0 := time.Now()
			ranFwd = true
			sol, r, joined := h.forward(c, slice)
			if HubRace {
				sumFwd += time.Since(t0) // exp-686: an expansion-budget trial may end early
			} else {
				sumFwd += slice
			}
			nodes += r.Nodes
			if sol != "" {
				h.recordEnd(c, "solved")
				return Result{Solved: true, Solution: sol, Nodes: nodes}
			}
			if joined != "" {
				h.recordEnd(c, "joined")
				return Result{Solved: true, Solution: joined, Nodes: nodes}
			}
			h.windowCheck(i, r, U)
			if Stop.Load() || !h.deadline.IsZero() && !time.Now().Before(h.deadline) {
				break
			}
		}
		// 3. Backward slice: half the forward time, until the pull tree has
		// its half of the memory budget.
		if !backDone && !Stop.Load() && (h.deadline.IsZero() || time.Now().Before(h.deadline)) {
			bslice := sumFwd / 2
			if sumFwd == 0 {
				bslice = q
			}
			if b := ps.budgetP.Load(); b > 0 && ps.trackedP.Load() >= b/2 {
				h.report("pull-paused")
				paused = true
			} else {
				sol, br, ok := h.backward(bslice)
				nodes += br.Nodes
				if ok {
					return Result{Solved: true, Solution: sol, Nodes: nodes}
				}
				backDone = br.Reason != "timeout"
			}
		}
		lastRound = time.Since(roundStart)
		h.rounds++
		if !ranFwd && h.gen.expanded == genBefore && h.scan >= int32(len(h.gen.cand)) {
			h.idle++
			if h.idle == 1 {
				h.idleFirstMs = h.ms()
				h.report("idle")
			}
			if backDone {
				h.idleDone++
			}
			if paused {
				h.idlePaused++
			}
			if HubIdleRing && h.nSeen < len(h.gen.cand) {
				// exp-758 class II: the strided harvest skipped candidates;
				// revisit them (stride 1, seen ones skipped) before idling.
				h.scan, h.rescanOn = 0, true
				h.rescans++
				idleWait, h.idleCapRun = 0, 0
				continue
			}
			if HubNoSpin || HubIdleRing {
				// Nothing ran: wait (1 ms doubling to 64 ms) instead of
				// re-entering at once, so the core goes to the other lanes.
				idleWait = min(max(2*idleWait, time.Millisecond), 64*time.Millisecond)
				w := idleWait
				if !h.deadline.IsZero() {
					w = min(w, time.Until(h.deadline))
				}
				if w > 0 {
					time.Sleep(w)
					h.idleSleepMs += w.Milliseconds()
				}
				if HubIdleRing {
					hubIdleSleepMs.Add(w.Milliseconds())
					if idleWait == 64*time.Millisecond {
						h.idleCapRun++
					}
					if h.idleCapRun >= HubIdleK {
						// exp-758: paused or finished generator, every
						// candidate visited, empty pool, no live context:
						// end the config so the lane's ring starts another.
						h.idleEnd = "idle"
						h.report("idle-end")
						return Result{Reason: "hub idle", Nodes: nodes}
					}
				}
			}
		} else {
			idleWait, h.idleCapRun = 0, 0
		}
		if backDone && h.liveCount() == 0 && h.nFreeCand() == 0 {
			// Nothing left to try on a finished pull tree.
			if h.scan >= int32(len(ps.player)) {
				break
			}
		}
	}
	return Result{Reason: "hub ended", Nodes: nodes}
}

func (h *hub) liveCount() int {
	n := 0
	for _, c := range h.ctxs {
		if c != nil {
			n++
		}
	}
	return n
}

func (h *hub) nFreeCand() int {
	n := 0
	for i := range h.nPool {
		if h.pool[i].state == hubFree {
			n++
		}
	}
	return n
}

// sliceEnd is the end of a slice of d, clipped to the lane deadline.
func (h *hub) sliceEnd(d time.Duration) time.Time { return roundEnd(d, h.deadline) }

// forward runs one forward slice of c, draining meetings posted to the idle
// pull solver, and updates the context's progress. A forward node equal to a
// pull node (the exact own-tree probe) ends the slice and is joined here.
func (h *hub) forward(c *hubCtx, d time.Duration) (string, Result, string) {
	ps := h.ps
	var met atomic.Bool
	drain := startDrain(func() (string, bool) {
		sol, ok := ps.pullCheckMail()
		return sol, ok && checkSol(h.rows, sol)
	}, &met)
	f := c.fwd
	stop := drain.StopCheck()
	f.stopCheck = stop
	t := time.Now()
	sol, r := f.towardRun(h.sliceEnd(d), h.memLimit)
	c.fwdTime += time.Since(t)
	joined := drain.Stop() // ps is ours again
	f.stopCheck = nil
	h.progress(c)
	return sol, r, joined
}

// backward runs one slice of the pull search, draining meetings posted to
// the forward contexts (their trees are idle meanwhile).
func (h *hub) backward(d time.Duration) (string, Result, bool) {
	over := h.genSlice(d)
	r := Result{Reason: "timeout"}
	if over {
		r.Reason = "exhausted"
	}
	return "", r, false
}

// progress reads the forward nodes added since the last slice for the best
// (boxes on hub, matching distance).
func (h *hub) progress(c *hubCtx) {
	f := c.fwd
	for id := int(c.scanned); id < len(f.hfOwn); id++ {
		t := f.hfOwn[id]
		if t.on > c.bestOn || t.on == c.bestOn && !t.inf && t.dist < c.bestDist {
			c.bestOn, c.bestDist = t.on, t.dist
			c.lastImpr = c.fwdTime
			c.lastImprExp = f.expandedN
		}
	}
	if n := len(f.hfOwn); n > int(c.scanned) {
		c.scanned = int32(n)
	}
	if !c.reached && int(c.bestOn) >= h.ps.n {
		c.reached = true
		c.firstOn = c.fwdTime
		h.reached++
		if h.firstReachMs == 0 {
			h.firstReachMs = h.ms()
		}
	}
	h.bestOnAll = max(h.bestOnAll, c.bestOn)
}

// windowCheck extends a context whose window ended while it was still
// improving in the window's second half; otherwise abandons it.
func (h *hub) windowCheck(i int, r Result, U time.Duration) {
	c := h.ctxs[i]
	if c == nil {
		return
	}
	end := ""
	switch {
	case c.fwd.memFull:
		h.memfull++
		end = "memfull"
	case r.Reason != "timeout" && r.Reason != "retarget" && r.Reason != "":
		if r.Reason == "target region unreachable" {
			h.tailFail++
			end = "tail"
		} else {
			h.exhausted++
			end = "exhausted"
		}
	case HubRace && c.fwd.expandedN >= c.winExp:
		half := c.winExp / 2 // second half of the current window ([winExp/2, winExp] after a doubling)
		if c.winExp > HubRaceUnit {
			half = c.winExp - c.winExp/4
		}
		if c.lastImprExp >= half {
			c.winExp *= 2
			c.fwd.nodeLimit = c.winExp
			h.extensions++
		} else {
			end = "stalled"
		}
	case !HubRace && c.fwdTime >= c.window:
		if c.lastImpr >= c.window/2 {
			c.window *= 2
			h.extensions++
		} else {
			end = "stalled"
		}
	}
	if end != "" {
		h.drop(i, end)
	}
}

func (h *hub) recordEnd(c *hubCtx, end string) {
	if c.rec >= 0 && c.rec < len(h.recs) {
		r := &h.recs[c.rec]
		r.BestOn, r.FwdExp, r.FwdMs, r.End = c.bestOn, c.fwd.expandedN, c.fwdTime.Milliseconds(), end
	}
}

// drop abandons context i: its hub joins the tried list.
func (h *hub) drop(i int, end string) {
	c := h.ctxs[i]
	h.recordEnd(c, end)
	h.abandoned++
	if HubRace && c.cand.id >= 0 {
		frac := float32(0)
		if n := int32(h.ps.n); n > 0 {
			frac = float32(c.bestOn) / float32(n)
		}
		raceSharedMark(h.sigOf(c.cand.id), frac)
	}
	if c.cand.id >= 0 {
		h.tried[h.nTried%hubTriedMax] = c.cand.id
		h.nTried++
	}
	c.tracked.Store(0)
	h.ctxs[i] = nil
	h.report("drop")
}

// nearDup reports whether node a differs from node b in fewer than delta
// boxes (popcount of the xor of the box bitsets, halved).
func (h *hub) nearDup(a, b int32) bool {
	return h.gen.nearDup(a, b, h.delta)
}

// dupLive: id is a near-duplicate of a live context's hub.
func (h *hub) dupLive(id int32) bool {
	for _, c := range h.ctxs {
		if c != nil && c.cand.id >= 0 && h.nearDup(id, c.cand.id) {
			return true
		}
	}
	return false
}

func (h *hub) inTried(id int32) bool {
	if h.dupLive(id) {
		return true
	}
	for k := range min(h.nTried, hubTriedMax) {
		if h.tried[k] == id || h.nearDup(id, h.tried[k]) {
			return true
		}
	}
	return false
}

// hubTier classifies a pull tuple: 0 is Festival's endpoint signature (one
// region, no broken rooms, no hotspots), 1 allows one hotspot, 2 one broken
// room link; 3 keeps one broken room link and any hotspots (relative: boards
// far from the goals never reach 0-2); 4 is none of those (the fallback).
func hubTier(t pfTuple) uint8 {
	switch {
	case t.reg != 1 || t.dist < 0:
		return 4
	case t.rooms == 0 && t.hot == 0:
		return 0
	case t.rooms == 0 && t.hot <= 1:
		return 1
	case t.rooms <= 1 && t.hot <= 1:
		return 2
	case t.rooms <= 1:
		return 3 // relative tier: connected, at most one broken room link, any hotspots (boards far from the goals never reach tiers 0-2)
	}
	return 4
}

// harvest scans the pull nodes added since the last call (ps is idle) for
// hub candidates, within about budget of wall time; a backlog beyond what
// fits is strided.
func (h *hub) harvest(budget time.Duration) { h.harvestG(budget) }

// score computes the cut proxy (free squares on no cycle: pfBicon) and the
// pinned boxes (no last push-in) of pull node id.
func (h *hub) score(id int32) (cut, pinned, trapped int32) {
	ps := h.ps
	h.gen.invalidate() // scoring borrows ps.occ
	h.unpackNode(id, h.buf)
	for _, b := range h.buf {
		ps.occ[b] = true
	}
	// With a generator: looseness is judged on the player's region; floor cells of other
	// regions (sealed pockets) count as blocked for the cut and the pull tests.
	var pocketCells []int32
	h.lastHot, h.lastPocket = 0, 0
	reach := func(c int32) bool { return true }
	if g := h.gen; g != nil {
		g.flood(g.nodes[id].player)
		for c := range int32(len(ps.lv.Wall)) {
			if !ps.lv.Wall[c] && !ps.occ[c] && g.lab[c] != 1 {
				pocketCells = append(pocketCells, c)
			}
		}
		h.lastPocket = int32(len(pocketCells))
		for _, c := range pocketCells {
			ps.occ[c] = true
		}
		reach = func(c int32) bool { return g.mark[c] == g.stamp && g.lab[c] == 1 }
	}
	cut = ps.pfBicon()
	wall, occ := ps.lv.Wall, ps.occ
	for _, b := range h.buf {
		ok := false
		for _, d := range ps.dirs {
			if !wall[b+d] && !wall[b+2*d] && !occ[b+2*d] && reach(b+d) {
				ok = true
				break
			}
		}
		if !ok {
			h.lastHot++
		}
	}
	for _, q := range h.buf {
		ok := false
		for _, d := range ps.dirs {
			if a, b := q-d, q-2*d; !wall[a] && !occ[a] && !wall[b] && !occ[b] {
				ok = true
				break
			}
		}
		if !ok {
			pinned++
		}
	}
	for _, b := range h.buf {
		if !ps.pfGoal(b) {
			continue
		}
		ok := false
		for _, d := range ps.dirs {
			if !wall[b+d] && !wall[b+2*d] && !occ[b+2*d] && reach(b+d) {
				ok = true
				break
			}
		}
		if !ok {
			trapped++
		}
	}
	for _, c := range pocketCells {
		ps.occ[c] = false
	}
	for _, b := range h.buf {
		ps.occ[b] = false
	}
	return cut, pinned, trapped
}

// admit puts c into the pool: near-duplicates of a member compete with it
// (the better key stays), near-duplicates of a tried hub are dropped.
func (h *hub) admit(c hubCand) {
	if h.inTried(c.id) {
		return
	}
	dup := -1
	var csig uint64
	if HubRace {
		csig = h.sigOf(c.id)
	}
	for i := range h.nPool {
		if HubRace && h.sigOf(h.pool[i].id) == csig || !HubRace && h.nearDup(c.id, h.pool[i].id) {
			if !c.keyLess(&h.pool[i]) {
				return
			}
			dup = i
			break
		}
	}
	if dup >= 0 {
		h.pool[dup] = c
		return
	}
	if h.nPool < h.poolCap() {
		h.pool[h.nPool] = c
		h.nPool++
		return
	}
	// Full pool: the victim is the worst member of the kind (strict one-region / pockets)
	// that holds more than half the pool, or c's own kind.
	nPk := 0
	for i := range h.nPool {
		if h.pool[i].pocket {
			nPk++
		}
	}
	victimPocket := c.pocket
	if nPk > h.poolCap()/2 && !c.pocket {
		victimPocket = true
	} else if h.nPool-nPk > h.poolCap()/2 && c.pocket {
		victimPocket = false
	}
	worst := -1
	for i := range h.nPool {
		if h.pool[i].pocket != victimPocket {
			continue
		}
		if worst < 0 || h.pool[worst].keyLess(&h.pool[i]) {
			worst = i
		}
	}
	if worst >= 0 && (c.keyLess(&h.pool[worst]) || c.pocket != victimPocket) {
		h.pool[worst] = c
	}
}

// removePool deletes pool member i.
func (h *hub) removePool(i int) {
	h.nPool--
	h.pool[i] = h.pool[h.nPool]
}

// nominate picks the next hub: the best 4 free pool members by key are scored
// against the start (min-cost matching of the hub's boxes to the start
// squares, in the pull view's distances); among them the best by
// (tier, cut bucket, pinned, lb, -dist) wins. nil if there is none.
func (h *hub) nominate() *hubCand {
	ps := h.ps
	t0 := time.Now()
	defer func() { h.nominateNs += time.Since(t0).Nanoseconds() }()
	if len(h.injects) > 0 {
		h.injCur, h.injects = h.injects[0], h.injects[1:]
		h.nominated++
		return &hubCand{id: -1}
	}
	// The best 4 free members by admission key are scored against the start.
	var top [4]int
	nTop := 0
	// Kinds alternate (strict one-region first): the top 4 come from the wanted kind,
	// the other kind fills the rest only when the wanted one has none free.
	wantPocket := hubGenPockets > 1 && h.nominated%2 == 1
	haveWanted := false
	for i := range h.nPool {
		if h.pool[i].state == hubFree && h.pool[i].pocket == wantPocket {
			haveWanted = true
		}
	}
	if !haveWanted {
		wantPocket = !wantPocket
	}
	for k := 0; k < 4; k++ {
		bi := -1
		for i := range h.nPool {
			p := &h.pool[i]
			if p.state != hubFree || slices.Contains(top[:nTop], i) || h.gen != nil && p.pocket != wantPocket {
				continue
			}
			if h.dupLive(p.id) {
				p.state = hubInfeasible // dropped (its twin is live); not counted as infeasible
				continue
			}
			if HubRace && bi >= 0 {
				// exp-686: strata no hub lane has tried come first
				pt, bt := h.sharedTried(p.id), h.sharedTried(h.pool[bi].id)
				if pt != bt {
					if !pt {
						bi = i
					}
					continue
				}
			}
			if bi < 0 || p.keyLess(&h.pool[bi]) {
				bi = i
			}
		}
		if bi < 0 {
			break
		}
		top[nTop] = bi
		nTop++
	}
	for _, i := range top[:nTop] {
		p := &h.pool[i]
		if p.lb != 0 {
			continue
		}
		h.unpackNode(p.id, h.buf)
		if h.gen != nil && h.gen.reach != nil && !h.gen.reach.feasible(h.buf) {
			p.state = hubInfeasible // exp-463: no perfect matching of the start boxes onto the hub within their exact reach
			h.reachRej++
			continue
		}
		lb := ps.assign(h.buf)
		if lb < 0 {
			p.state = hubInfeasible
			h.infeasible++
			continue
		}
		p.lb = lb + 1 // stored plus one: 0 means not evaluated
	}
	var best *hubCand
	bestIdx := -1
	for _, i := range top[:nTop] {
		p := &h.pool[i]
		if p.state != hubFree {
			continue
		}
		if best == nil || hubPickLess(p, best) {
			best, bestIdx = p, i
		}
	}
	var picked hubCand
	if best != nil {
		picked = *best
	}
	// compact away infeasible members
	for i := 0; i < h.nPool; {
		if h.pool[i].state == hubInfeasible {
			h.removePool(i)
			if bestIdx == h.nPool { // the moved last member
				bestIdx = i
			}
			continue
		}
		i++
	}
	if best != nil {
		c := picked
		c.lb--
		for i := range h.nPool {
			if h.pool[i].id == c.id {
				h.removePool(i)
				break
			}
		}
		h.tierCount[c.tier]++
		h.nominated++
		return &c
	}
	// Empty pool: festb-commit's choice, the pull tree's best node.
	id := h.bestNode()
	if id < 0 || h.inTriedExact(id) || h.inTried(id) {
		return nil
	}
	if HubFallbackCheck {
		// exp-680: the pool's start checks (exact single-box reach matching, assignment) for the fallback too
		h.unpackNode(id, h.buf)
		if h.gen != nil && h.gen.reach != nil && !h.gen.reach.feasible(h.buf) || ps.assign(h.buf) < 0 {
			h.fallbackRej++
			h.tried[h.nTried%hubTriedMax] = id
			h.nTried++
			return nil
		}
	}
	t := h.tupleOf(id)
	c := &hubCand{id: id, tier: 4, dist: t.dist}
	h.unpackNode(id, h.buf)
	c.lb = ps.assign(h.buf)
	c.cut, c.pinned, c.trapped = h.score(id)
	h.tierCount[4]++
	h.nominated++
	return c
}

func (h *hub) inTriedExact(id int32) bool {
	for k := range min(h.nTried, hubTriedMax) {
		if h.tried[k] == id {
			return true
		}
	}
	return false
}

// hubPickLess is the nomination order over evaluated members (lb is stored
// plus one).
func hubPickLess(a, b *hubCand) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if a.trapped != b.trapped {
		return a.trapped < b.trapped
	}
	if x, y := bits.Len32(uint32(a.cut)), bits.Len32(uint32(b.cut)); x != y {
		return x < y
	}
	if a.pinned != b.pinned {
		return a.pinned < b.pinned
	}
	if a.lb != b.lb {
		return a.lb < b.lb
	}
	if a.dist != b.dist {
		return a.dist > b.dist
	}
	return a.id < b.id
}

// targetOf is the forward target of pull node id.
func (h *hub) targetOf(id int32) *meet.Target {
	ps := h.ps
	if g := h.gen; g != nil {
		boxes := make([]int32, ps.n)
		g.unpack(id, boxes)
		slices.Sort(boxes)
		return &meet.Target{Boxes: boxes, Player: g.nodes[id].player, Tail: g.tail(id)}
	}
	boxes := make([]int32, ps.n)
	ps.unpack(id, boxes)
	return &meet.Target{Boxes: boxes, Player: ps.player[id], Tail: ps.pullTail(id)}
}

// commit starts a forward context on hub c in slot i.
func (h *hub) commit(i int, c *hubCand) {
	ps := h.ps
	var tgt *meet.Target
	if c.id < 0 {
		tgt = h.injCur
	} else {
		tgt = h.targetOf(c.id)
	}
	tracked := h.trackeds[i]
	tracked.Store(0)
	f := newTowardReal(h.rows, h.lv, tgt, h.deadline, h.tr, ps.budgetP, tracked, h.owner, 0)
	if hubRegRel {
		f.regRel = true
		f.setTargetPockets(tgt)
	}
	f.peerTracked = ps.trackedP
	f.peerTracked2 = h.trackeds[1-i]
	if h.owner == FestBLane {
		f.laneID = 8
	} else {
		f.laneID = 10
	}
	ctx := &hubCtx{fwd: f, tracked: tracked, cand: *c, tailLen: len(tgt.Tail), start: time.Now(), window: h.U, bestDist: 1 << 30, rec: -1}
	if HubRace {
		ctx.winExp = HubRaceUnit
		f.nodeLimit = HubRaceUnit
	}
	hot := int32(-1)
	if c.id >= 0 {
		hot = h.tupleOf(c.id).hot
	}
	if len(h.recs) < hubRecMax {
		ctx.rec = len(h.recs)
		h.recs = append(h.recs, hubRec{TMs: h.ms(), Tier: c.tier, Cut: c.cut, Pinned: c.pinned, Trapped: c.trapped, Hot: hot, Dist: c.dist, LB: c.lb, TailLen: len(tgt.Tail), N: ps.n, Pocket: c.pocket})
	}
	if p := os.Getenv("HUBGEN_COMMIT_DUMP"); p != "" && c.id >= 0 {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		bx := make([]int32, ps.n)
		pl := int32(0)
		if h.gen != nil { // exp-461: the generator's committed hubs too
			h.gen.invalidate()
			h.gen.unpack(c.id, bx)
			pl = h.gen.nodes[c.id].player
		} else {
			ps.unpack(c.id, bx)
			pl = ps.player[c.id]
		}
		f.WriteString("COMMIT tier " + string(rune('0'+c.tier)) + " id " + strconv.Itoa(int(c.id)) + " t_ms " + strconv.FormatInt(h.ms(), 10) + "\n" + dumpBoxes(ps, bx, pl))
		f.Close()
	}
	h.ctxs[i] = ctx
	h.committed++
	h.report("commit")
}

// report writes the Tier 0 counters to the forward recorder.
func (h *hub) report(ev string) {
	tr := h.tr
	if tr == nil {
		return
	}
	tr.SetExtra("hub_mode", true)
	tr.SetExtra("hubs_nominated", h.nominated)
	tr.SetExtra("hubs_committed", h.committed)
	tr.SetExtra("hub_fallback_rej", h.fallbackRej)
	tr.SetExtra("hubs_reached", h.reached)
	tr.SetExtra("hub_first_reach_ms", h.firstReachMs)
	tr.SetExtra("hub_abandoned", h.abandoned)
	tr.SetExtra("hub_extensions", h.extensions)
	tr.SetExtra("hub_memfull", h.memfull)
	tr.SetExtra("hub_exhausted", h.exhausted)
	tr.SetExtra("hub_tail_fail", h.tailFail)
	tr.SetExtra("hub_infeasible", h.infeasible)
	if h.gen != nil && h.gen.reach != nil {
		tr.SetExtra("hub_reach", []int{h.gen.reach.nCls, h.reachRej})
	}
	if h.gen != nil {
		tr.SetExtra("hub_gen", []any{h.gen.expanded, h.gen.full, h.gen.bytes() >> 20, h.gen.capBytes >> 20})
	}
	tr.SetExtra("hub_tier", h.tierCount[:])
	tr.SetExtra("hub_tier_seen", h.tierSeen[:])
	tr.SetExtra("hub_reg1", []int{h.nReg1, h.minRooms, h.minHot0, h.minHot})
	tr.SetExtra("hub_pool", h.nPool)
	tr.SetExtra("hub_eligible", h.eligible)
	tr.SetExtra("hub_scanned", h.scanned)
	tr.SetExtra("hub_scan_skipped", h.scanSkipped)
	tr.SetExtra("hub_scan_ms", h.scanNs/1e6)
	tr.SetExtra("hub_nominate_ms", h.nominateNs/1e6)
	tr.SetExtra("hub_best_on", h.bestOnAll)
	if h.gen != nil {
		tr.SetExtra("hub_rounds", []any{h.rounds, h.idle, h.idleFirstMs, h.idleDone, h.idlePaused, h.idleSleepMs, len(h.ps.player), h.scan, len(h.gen.cand), h.ps.trackedP.Load() >> 20, h.ps.budgetP.Load() >> 20, h.gen.base >> 20})
		if HubIdleRing {
			tr.SetExtra("hub_idle_ring", []any{h.nSeen, h.rescans, h.idleEnd, h.ms()}) // exp-758
		}
	}
	if ev == "end" || ev == "drop" {
		for _, c := range h.ctxs {
			if c != nil && ev == "end" {
				h.recordEnd(c, "end")
			}
		}
	}
	tr.SetExtra("hub_list", slices.Clone(h.recs))
}

// loadHubBoards reads boards in exp-449's format (an optional "HUB name"
// header line, then rows with '$' boxes and '@' player cells) as targets
// without tails. Diagnostics only.
func loadHubBoards(path string, lv *Level) []*meet.Target {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []*meet.Target
	var rows []string
	flush := func() {
		var boxes []int32
		player := int32(-1)
		for y, r := range rows {
			for x, c := range r {
				cell := int32((y+1)*lv.W + x + 1)
				switch c {
				case '$', '*':
					boxes = append(boxes, cell)
				case '@', '+':
					if player < 0 {
						player = cell
					}
				}
			}
		}
		if len(boxes) == len(lv.Boxes) && player >= 0 {
			slices.Sort(boxes)
			out = append(out, &meet.Target{Boxes: boxes, Player: player})
		}
		rows = nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "HUB") || strings.HasPrefix(line, "IMAGINED") || strings.HasPrefix(line, "["):
			if len(rows) > 0 {
				flush()
			}
		case strings.TrimSpace(line) == "":
			if len(rows) > 0 {
				flush()
			}
		default:
			rows = append(rows, line)
		}
	}
	if len(rows) > 0 {
		flush()
	}
	return out
}

// poolCap is the live pool size (exp-686 -hub-race: one member per stratum, strataMax).
func (h *hub) poolCap() int {
	if HubRace {
		return min(strataMax, hubPoolMax)
	}
	return 32
}

// sigOf is the v0 stratum signature of node id (exp-686).
func (h *hub) sigOf(id int32) uint64 {
	ps := h.ps
	if h.sigBuf == nil {
		h.sigBuf = make([]int32, ps.n)
		n := 1
		if ps.rooms != nil {
			n += len(ps.rooms.rooms)
		}
		h.sigCnt = make([]int32, n)
	}
	h.unpackNode(id, h.sigBuf)
	if h.sigGoal == nil {
		h.sigGoal = make([]bool, len(ps.lv.Wall))
		for _, g := range ps.lv.Boxes {
			h.sigGoal[g] = true
		}
	}
	return boxSig(h.sigGoal, roomCells(ps), h.sigCnt, h.sigBuf)
}

// sharedTried: a hub lane already tried node id's stratum.
func (h *hub) sharedTried(id int32) bool {
	r, ok := raceSharedGet(h.sigOf(id))
	return ok && r.tries > 0
}
