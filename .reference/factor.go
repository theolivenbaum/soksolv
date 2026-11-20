package main

// exp-645: factored component lane (-factor, default off; research/exp-645).
//
// Dead squares split some boards into box components: sets of live squares no
// box can leave (exact: lone-box push graph on the wall-only board). Boxes of
// different components never share a square; they interact only by blocking
// the player. With k >= 2 non-trivial components (some box off goal) the lane:
//
//  1. solves each component as its own level, the other components' boxes and
//     goals absent (trivial components as walls), with a ladder of existing
//     engines (macro, novelty, fa2) under escalating node caps;
//  2. schedules the component plans on the real board: a depth-first search
//     over (plan index per group, box set of each small group, player region);
//     a planned push is legal when the player reaches its push square with all
//     boxes present; small groups (few configurations) and trivial components
//     move freely inside their component;
//  3. when no schedule completes: re-plans a stuck group with every other box
//     frozen as walls, else merges the stuck group with the group whose boxes
//     block the player and starts again from the initial position.
//
// Every emitted move is a real move, so the result is verified like any other.
// The gate is structural and exact; with k < 2 the lane does not start.

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"soksolv/reverse"
)

var (
	FactorLane    bool
	FactorFreeMax = 300    // a group with at most this many box configurations is searched by the scheduler
	FactorPool    = 200000 // timed mode: first node pool; doubled per attempt
	FactorSched   = 300000 // scheduler states per call (split over component orders)
	FactorPlanCap = 0.02   // exp-645b: > 0: a sub-solve's planner may use this fraction of the lane's remaining time
	FactorAlt     = false  // exp-645: added no board at promotion; off by default
)

var BudgetF, TrackedF atomic.Uint64

var factorDebug = os.Getenv("SOKSOLV_FACTOR_DEBUG") != "" // exp-660: per sub-solve trace on stderr
var factorT0 = time.Now()

// factorGated: the current level has >= 2 non-trivial box components (set in main after parsing).
var factorGated bool

func init() {
	flag.BoolVar(&FactorLane, "factor", true, "exp-645 (promoted 2026-11-12): factored component lane on boards with >= 2 non-trivial static box components (-strategy factor runs it alone; -nodes = its node pool)")
	flag.IntVar(&FactorFreeMax, "factor-free", FactorFreeMax, "exp-645: groups with at most this many box configurations are searched exactly by the scheduler")
	flag.Float64Var(&FactorPlanCap, "factor-plancap", FactorPlanCap, "exp-645b: > 0: each sub-solve's packing planner is limited to this fraction of the remaining time (search unchanged)")
	flag.IntVar(&FactorSched, "factor-sched", FactorSched, "exp-645: scheduler state cap per call")
	flag.BoolVar(&FactorAlt, "factor-alt", FactorAlt, "exp-645: before merging, re-plan stuck groups with the other groups at their goals / at their starts as walls, or with another engine")
}

// fgroup is a set of components treated as one sub-level.
type fgroup struct {
	cells []int32 // squares (sorted)
	boxes []int32 // initial boxes (sorted)
	goals []int32 // goals (sorted)
}

// boxComponents returns the non-trivial and trivial box components of lv.
func boxComponents(lv *Level) (nt, triv []fgroup) {
	n := lv.W * lv.H
	d := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	live := make([]bool, n)
	q := make([]int32, 0, n)
	for c := range n {
		if lv.Goal[c] && !lv.Wall[c] {
			live[c] = true
			q = append(q, int32(c))
		}
	}
	for i := 0; i < len(q); i++ { // pull flood: box at c pulled to c+d by a player standing at c+2d
		c := q[i]
		for _, dd := range d {
			nb, p := c+dd, c+2*dd
			if !lv.Wall[nb] && !lv.Wall[p] && !live[nb] {
				live[nb] = true
				q = append(q, nb)
			}
		}
	}
	par := make([]int32, n)
	for i := range par {
		par[i] = int32(i)
	}
	var find func(int32) int32
	find = func(x int32) int32 {
		for par[x] != x {
			par[x] = par[par[x]]
			x = par[x]
		}
		return x
	}
	for c := range n {
		if !live[c] {
			continue
		}
		for _, dd := range d {
			nb, p := int32(c)+dd, int32(c)-dd
			if live[nb] && !lv.Wall[p] {
				if a, b := find(int32(c)), find(nb); a != b {
					par[a] = b
				}
			}
		}
	}
	isBox := make([]bool, n)
	for _, b := range lv.Boxes {
		isBox[b] = true
	}
	byRoot := map[int32]int{}
	var all []fgroup
	for c := range n {
		if !live[c] {
			continue
		}
		r := find(int32(c))
		k, ok := byRoot[r]
		if !ok {
			k = len(all)
			byRoot[r] = k
			all = append(all, fgroup{})
		}
		g := &all[k]
		g.cells = append(g.cells, int32(c))
		if isBox[c] {
			g.boxes = append(g.boxes, int32(c))
		}
		if lv.Goal[c] {
			g.goals = append(g.goals, int32(c))
		}
	}
	for _, g := range all {
		if len(g.boxes) == 0 {
			continue
		}
		if slices.Equal(g.boxes, g.goals) {
			triv = append(triv, g)
		} else {
			nt = append(nt, g)
		}
	}
	return nt, triv
}

// factorGate: the lane applies (exact structural test).
func factorGate(lv *Level) bool {
	nt, _ := boxComponents(lv)
	return len(nt) >= 2
}

type fpush struct {
	box int32
	dir int8
}

// fplan is a group's push list; pos[j*nb:(j+1)*nb] = its boxes (sorted) after j pushes.
type fplan struct {
	pushes []fpush
	pos    []int32
	nb     int
}

func newFPlan(start []int32, pushes []fpush, d [4]int32) *fplan {
	p := &fplan{pushes: pushes, nb: len(start)}
	cur := slices.Clone(start)
	slices.Sort(cur)
	p.pos = append(p.pos, cur...)
	for _, ps := range pushes {
		i := slices.Index(cur, ps.box)
		cur[i] = ps.box + d[ps.dir]
		slices.Sort(cur)
		p.pos = append(p.pos, cur...)
	}
	return p
}

type factor struct {
	lv       *Level
	d        [4]int32
	deadline time.Time
	memLimit uint64
	solo     bool
	pool     int64 // node pool of the current attempt
	used     int64
	reserve  int64
	capScale int64

	groups []fgroup // current non-trivial groups
	triv   []fgroup
	cache  map[string]*fplan // relaxed plans from the initial position, by group
	live   map[string]*fplan // this attempt's remaining plans

	// real-board state
	occ   []bool
	boxes []int32
	pl    int32
	lurd  []byte

	// scratch
	mark  []uint32
	stamp uint32
	q     []int32
	prev  []int32
	occ2  []bool

	subCalls, subSolved int
	altTried            map[string]int
	infeasible          map[string]bool // exp-660 (portfolio): sub-levels an engine proved unsolvable

	// exp-660 (portfolio): the current sub-level's FESS searches, kept so the
	// next, larger cap resumes them instead of starting over
	keepKey string
	keep    map[string]*solver
	keepN   map[string]int64 // nodes the engine has spent on keepKey
	keepFA2 *fa2Runner
	planEng map[string]map[string]bool // engines that planned (or were asked to plan) each group
}

// fa2Runner is an fa2 search paused between baton slices (engine 0; the
// factor lane holds slot 1 and passes the token back to let it run on).
type fa2Runner struct {
	s       *solver
	bat     *baton
	ch      chan Result
	holding bool // the lane holds the token: the engine is paused in yield
	done    bool
}

func startFA2(s *solver, deadline time.Time, memLimit uint64) *fa2Runner {
	r := &fa2Runner{s: s, bat: newBaton([3]bool{true, true, false}), ch: make(chan Result, 1)}
	go func() { r.ch <- fa2Run(s, deadline, memLimit, FA2Plan, r.bat, 0) }()
	r.bat.ch[0] <- struct{}{}
	return r
}

// runTo lets the engine expand until it has done target expansions in total
// (to the next 1024-expansion yield) or ends; finished reports the end.
func (r *fa2Runner) runTo(target int64) (Result, bool) {
	for {
		if r.holding {
			r.holding = false
			r.bat.ch[0] <- struct{}{}
		}
		<-r.bat.ch[1]
		r.bat.mu.Lock()
		alive := r.bat.alive[0]
		r.bat.mu.Unlock()
		if !alive {
			r.done = true
			return <-r.ch, true
		}
		r.holding = true
		if r.s.expandedN >= target {
			return Result{Reason: "timeout"}, false
		}
	}
}

// stop ends a paused engine and waits for it.
func (r *fa2Runner) stop() {
	if r == nil || r.done {
		return
	}
	r.bat.mu.Lock()
	r.bat.stop = true
	r.bat.mu.Unlock()
	if r.holding {
		r.holding = false
		r.bat.ch[0] <- struct{}{}
	}
	<-r.ch
	r.done = true
}

// keepFA2Runner keeps a paused fa2 engine of sub-level rkey (or stops it).
func (f *factor) keepFA2Runner(rkey string, fr *fa2Runner, done bool, n int64) {
	f.keepSolver(rkey, "fa2", nil, Result{}, 0)
	if done {
		return
	}
	f.keepFA2, f.keepN["fa2"] = fr, n
}

// dropKept stops and releases every kept sub-search.
func (f *factor) dropKept() {
	f.keepFA2.stop()
	f.keepFA2 = nil
	f.keepKey, f.keep, f.keepN = "", nil, nil
}

// keepSolver keeps a FESS sub-search that stopped on its node cap for resumption.
func (f *factor) keepSolver(rkey, lane string, s *solver, r Result, n int64) {
	if rkey != f.keepKey {
		f.dropKept()
		f.keepKey, f.keep, f.keepN = rkey, map[string]*solver{}, map[string]int64{}
	}
	delete(f.keep, lane)
	if lane == "fa2" {
		f.keepFA2.stop()
		f.keepFA2 = nil
		return
	}
	if r.Reason == "timeout" && s.resume != nil && s.nodeLimit > 0 && s.expandedN >= s.nodeLimit {
		f.keep[lane], f.keepN[lane] = s, n
	}
}

// resumeSub continues a kept search of this sub-level up to cap nodes in total
// (ok = false: nothing kept, start fresh).
func (f *factor) resumeSub(rows []string, lane string, cap int64) (string, string, bool) {
	rkey := strings.Join(rows, "\n")
	if rkey != f.keepKey || (lane != "fa2" && f.keep[lane] == nil) || (lane == "fa2" && f.keepFA2 == nil) {
		return "", "", false
	}
	if lane == "fa2" {
		return f.resumeFA2(rkey, cap)
	}
	if f.infeasible[rkey] {
		return "", "infeasible", true
	}
	s, spent := f.keep[lane], f.keepN[lane]
	delta := min(cap-spent, f.pool-f.used)
	if delta <= 0 || f.expired() {
		if delta <= 0 && cap-spent > 0 {
			return "", "pool", true
		}
		return "", "timeout", true
	}
	before := s.expandedN
	s.nodeLimit = before + delta
	r := s.solve(f.deadline, f.memLimit)
	n := s.expandedN - before
	TrackedF.Store(0)
	f.used += min(n, delta)
	f.subCalls++
	if factorDebug {
		fmt.Fprintf(os.Stderr, "factor: resume %s cap=%d nodes=%d solved=%v why=%s used=%d/%d t=%.1fs\n", lane, cap, n, r.Solved, r.Reason, f.used, f.pool, time.Since(factorT0).Seconds())
	}
	delete(f.keep, lane)
	if r.Solved && r.Solution != "" {
		f.subSolved++
		return r.Solution, "", true
	}
	if infeasibleWhy(r.Reason) {
		if f.infeasible == nil {
			f.infeasible = map[string]bool{}
		}
		f.infeasible[rkey] = true
		return "", "infeasible", true
	}
	if r.Reason == "timeout" && s.expandedN >= s.nodeLimit {
		f.keep[lane], f.keepN[lane] = s, spent+n
	}
	return "", r.Reason, true
}

// infeasibleWhy: an engine's end reason that proves its sub-level unsolvable.
func infeasibleWhy(why string) bool {
	switch why {
	case "exhausted", "box on dead square", "goal unreachable from start squares":
		return true
	}
	return false
}

var dirCh = [4]byte{'u', 'd', 'l', 'r'}

func (f *factor) expired() bool {
	if Stop.Load() {
		return true
	}
	return NodeBudget == 0 && time.Now().After(f.deadline)
}

// reach marks the player's region from p (occ = obstacles) and returns its minimum cell.
func (f *factor) reach(p int32, occ []bool) int32 {
	f.stamp++
	if f.stamp == 0 {
		clear(f.mark)
		f.stamp = 1
	}
	f.q = append(f.q[:0], p)
	f.mark[p] = f.stamp
	mn := p
	for i := 0; i < len(f.q); i++ {
		c := f.q[i]
		for _, dd := range f.d {
			nb := c + dd
			if !f.lv.Wall[nb] && !occ[nb] && f.mark[nb] != f.stamp {
				f.mark[nb] = f.stamp
				f.q = append(f.q, nb)
				mn = min(mn, nb)
			}
		}
	}
	return mn
}

// walk appends the player's moves from pl to t (occ = obstacles); false if unreachable.
func (f *factor) walk(t int32) bool {
	if f.pl == t {
		return true
	}
	f.stamp++
	f.q = append(f.q[:0], f.pl)
	f.mark[f.pl] = f.stamp
	for i := 0; i < len(f.q); i++ {
		c := f.q[i]
		for k, dd := range f.d {
			nb := c + dd
			if f.lv.Wall[nb] || f.occ[nb] || f.mark[nb] == f.stamp {
				continue
			}
			f.mark[nb] = f.stamp
			f.prev[nb] = c*4 + int32(k)
			if nb == t {
				var rev []byte
				for x := t; x != f.pl; x = f.prev[x] / 4 {
					rev = append(rev, dirCh[f.prev[x]%4])
				}
				for j := len(rev) - 1; j >= 0; j-- {
					f.lurd = append(f.lurd, rev[j])
				}
				f.pl = t
				return true
			}
			f.q = append(f.q, nb)
		}
	}
	return false
}

// push applies one push on the real board (walking to its push square first).
func (f *factor) push(p fpush) bool {
	dd := f.d[p.dir]
	if !f.occ[p.box] || f.lv.Wall[p.box+dd] || f.occ[p.box+dd] || !f.walk(p.box-dd) {
		return false
	}
	f.lurd = append(f.lurd, dirCh[p.dir]-'a'+'A')
	f.occ[p.box], f.occ[p.box+dd] = false, true
	i := slices.Index(f.boxes, p.box)
	f.boxes[i] = p.box + dd
	f.pl = p.box
	return true
}

// applyLURD replays a sub-solution (same coordinates) on the real board.
func (f *factor) applyLURD(s string) bool {
	for i := 0; i < len(s); i++ {
		k := strings.IndexByte("udlr", s[i]|0x20)
		if k < 0 {
			return false
		}
		dd := f.d[k]
		nb := f.pl + dd
		if f.lv.Wall[nb] {
			return false
		}
		if f.occ[nb] {
			if f.lv.Wall[nb+dd] || f.occ[nb+dd] {
				return false
			}
			f.occ[nb], f.occ[nb+dd] = false, true
			f.boxes[slices.Index(f.boxes, nb)] = nb + dd
		}
		f.pl = nb
	}
	f.lurd = append(f.lurd, s...)
	return true
}

// rows renders a sub-level in the original rows' coordinates (lv cell (y+1)*W+x+1).
func (f *factor) rows(walls, boxes, goals []bool, pl int32) []string {
	lv := f.lv
	out := make([]string, lv.H-2)
	b := make([]byte, lv.W-2)
	for y := 1; y < lv.H-1; y++ {
		for x := 1; x < lv.W-1; x++ {
			c := int32(y*lv.W + x)
			ch := byte(' ')
			switch {
			case lv.Wall[c] || walls[c]:
				ch = '#'
			case c == pl && goals[c]:
				ch = '+'
			case c == pl:
				ch = '@'
			case boxes[c] && goals[c]:
				ch = '*'
			case boxes[c]:
				ch = '$'
			case goals[c]:
				ch = '.'
			}
			b[x-1] = ch
		}
		out[y-1] = string(b)
	}
	return out
}

func cellSet(n int, cs ...[]int32) []bool {
	m := make([]bool, n)
	for _, c := range cs {
		for _, x := range c {
			m[x] = true
		}
	}
	return m
}

// subSolve runs one engine on a sub-level with a node cap; returns the LURD or "".
func (f *factor) subSolve(rows []string, lane string, cap int64) (string, string) {
	if lane == "macro" || lane == "novelty" || lane == "fa2" {
		if sol, why, ok := f.resumeSub(rows, lane, cap); ok {
			return sol, why
		}
	}
	cap = min(cap, f.pool-f.used)
	if cap < 200 || f.expired() {
		return "", "pool"
	}
	rkey := strings.Join(rows, "\n")
	if f.infeasible[rkey] {
		return "", "infeasible"
	}
	sub, err := parseLevelRaw(rows) // a sealed or unbalanced sub-level is infeasible
	if err != nil {
		return "", "parse"
	}
	f.subCalls++
	mk := func(id int) *solver {
		s := newSolverUntil(sub, f.deadline)
		s.laneID, s.rows, s.isolated = id, rows, true
		s.budgetP, s.trackedP = &BudgetF, &TrackedF
		s.nodeLimit = cap
		if NodeBudget == 0 && FactorPlanCap > 0 {
			s.planCap = max(time.Duration(float64(time.Until(f.deadline))*FactorPlanCap), time.Millisecond)
		}
		return s
	}
	var r Result
	var n int64
	switch lane {
	case "macro":
		s := mk(1)
		r = s.solve(f.deadline, f.memLimit)
		n = s.expandedN
		for r.Reason == "replan" && !f.expired() && n < cap { // as the macro lane (exp-563)
			pre, at := s.prePlan, s.prePlanAt
			s = mk(1)
			s.nodeLimit = cap - n
			s.prePlan, s.prePlanAt = pre, at
			r = s.solve(f.deadline, f.memLimit)
			n += s.expandedN
		}
		f.keepSolver(rkey, lane, s, r, n)
	case "novelty":
		s := mk(5)
		s.novelty = 100
		s.owner = 3
		r = s.solve(f.deadline, f.memLimit)
		n = s.expandedN
		f.keepSolver(rkey, lane, s, r, n)
	case "fa2":
		s := mk(12)
		s.fa2Lane = true // per-solver OOP tail, no process-wide switch
		// a pausable engine (baton slices), resumed by the next larger cap
		s.nodeLimit = 1 << 62
		fr := startFA2(s, f.deadline, f.memLimit)
		var done bool
		r, done = fr.runTo(cap)
		n = s.expandedN
		f.keepFA2Runner(rkey, fr, done, n)
	case "reverse": // exp-660: the backward engine, private (no perimeters), on the factor lane's memory slot
		rr, err := reverse.SolveLane(rows, f.deadline, f.memLimit, 0, reverse.Lane{Index: 3, Free: reverse.FreeStart, Cells: reverse.RevCells,
			Novelty: true, Types: true, Walks: true, Private: true, MaxNodes: cap * revNodeWeight, Mem: &BudgetF, Used: &TrackedF})
		if err != nil {
			return "", "parse"
		}
		r = Result{Solved: rr.Solved, Reason: rr.Reason, Solution: rr.Solution}
		n = (int64(rr.Nodes) + revNodeWeight - 1) / revNodeWeight
	}
	TrackedF.Store(0)
	f.used += min(n, cap)
	if factorDebug {
		fmt.Fprintf(os.Stderr, "factor: sub %s boxes=%d cap=%d nodes=%d solved=%v why=%s used=%d/%d t=%.1fs\n", lane, len(sub.Boxes), cap, n, r.Solved, r.Reason, f.used, f.pool, time.Since(factorT0).Seconds())
	}
	if r.Solved && r.Solution != "" {
		f.subSolved++
		return r.Solution, ""
	}
	if infeasibleWhy(r.Reason) {
		if f.infeasible == nil {
			f.infeasible = map[string]bool{}
		}
		f.infeasible[rkey] = true
		return "", "infeasible"
	}
	return "", r.Reason
}

// pushesOf replays a sub-solution on its sub-level and returns its pushes.
func (f *factor) pushesOf(lurd string, boxes []int32, walls []bool, pl int32) []fpush {
	occ := cellSet(len(f.lv.Wall), boxes)
	var out []fpush
	for i := 0; i < len(lurd); i++ {
		k := strings.IndexByte("udlr", lurd[i]|0x20)
		if k < 0 {
			return nil
		}
		dd := f.d[k]
		nb := pl + dd
		if f.lv.Wall[nb] || walls[nb] {
			return nil
		}
		if occ[nb] {
			if f.lv.Wall[nb+dd] || walls[nb+dd] || occ[nb+dd] {
				return nil
			}
			occ[nb], occ[nb+dd] = false, true
			out = append(out, fpush{nb, int8(k)})
		}
		pl = nb
	}
	return out
}

// factorSteps: the sub-solver ladder has this many steps (see stepCalls).
const factorSteps = 3

// revNodeWeight: reverse expansions per pool node (a backward expansion costs
// about 1/25 of a FESS one: exp-657's sub-levels, 500k vs 10-20k per second).
const revNodeWeight = 16

// factorEnginesP (exp-660): the sub-solver engines, time-sliced on the lane's
// core by node caps. Set from the exp-660 engine census on exp-657's
// component sub-levels (each engine alone, node-bounded): fa2, macro,
// reverse, novelty; pull was never the only solver, so it is left out.
var factorEnginesP = []string{"fa2", "macro", "reverse", "novelty"}

// subCall is one engine run with a node cap (before capScale).
type subCall struct {
	lane string
	cap  int64
}

// stepCalls lists ladder step k's engine runs: every engine at a
// doubling cap, round robin (step 0: 1k..8k, step 1: 16k, 32k, step 2: 64k,
// 128k), so the engine that solves a sub-level cheaply is reached cheaply.
func stepCalls(k int) []subCall {
	var out []subCall
	lo, hi := [3]int64{1000, 16000, 64000}[k], [3]int64{8000, 32000, 128000}[k]
	for c := lo; c <= hi; c *= 2 {
		for _, l := range factorEnginesP {
			out = append(out, subCall{l, c})
		}
	}
	return out
}

// replanCalls: the engine runs a frozen re-plan tries in order.
func replanCalls() []subCall {
	var out []subCall
	for c := int64(1000); c <= 32000; c *= 2 {
		for _, l := range factorEnginesP {
			out = append(out, subCall{l, c})
		}
	}
	return out
}

// relaxedPlan: group g alone from the initial position; trivial components as walls (then absent).
func (f *factor) relaxedPlan(g *fgroup, step int) *fplan {
	return f.relaxedPlanBy(g, step, "")
}

// relaxedPlanBy is relaxedPlan with one engine only (only != ""); it records
// which engine planned the group (exp-660).
func (f *factor) relaxedPlanBy(g *fgroup, step int, only string) *fplan {
	n := len(f.lv.Wall)
	var tw []int32
	for _, t := range f.triv {
		tw = append(tw, t.boxes...)
	}
	for _, walls := range [2]bool{true, false} {
		var w []bool
		if walls {
			w = cellSet(n, tw)
		} else {
			w = make([]bool, n)
		}
		rows := f.rows(w, cellSet(n, g.boxes), cellSet(n, g.goals), f.lv.Player)
		timedOut := false
		for _, c := range stepCalls(step) {
			if only != "" && c.lane != only {
				continue
			}
			sol, why := f.subSolve(rows, c.lane, c.cap*f.capScale)
			if sol != "" {
				if ps := f.pushesOf(sol, g.boxes, w, f.lv.Player); ps != nil {
					if f.planEng == nil {
						f.planEng = map[string]map[string]bool{}
					}
					if f.planEng[gkey(g)] == nil {
						f.planEng[gkey(g)] = map[string]bool{}
					}
					f.planEng[gkey(g)][c.lane] = true
					return newFPlan(g.boxes, ps, f.d)
				}
			}
			if why == "pool" {
				return nil
			}
			if why == "infeasible" {
				timedOut = false // proved unsolvable: try the next variant
				break
			}
			timedOut = timedOut || why == "timeout"
		}
		if walls && timedOut {
			break
		}
	}
	return nil
}

func gkey(g *fgroup) string {
	b := make([]byte, 0, 4*len(g.boxes))
	for _, x := range g.boxes {
		b = append(b, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
	}
	return string(b)
}

func combLE(n, k, lim int) bool {
	c := 1
	for i := 0; i < k; i++ {
		c = c * (n - i) / (i + 1)
		if c > lim {
			return false
		}
	}
	return true
}

func (f *factor) isFree(g *fgroup) bool { return combLE(len(g.cells), len(g.boxes), FactorFreeMax) }

func (f *factor) groupDone(g *fgroup) bool {
	for _, x := range g.goals {
		if !f.occ[x] {
			return false
		}
	}
	return true
}

func (f *factor) curBoxes(g *fgroup) []int32 {
	var out []int32
	for _, c := range g.cells {
		if f.occ[c] {
			out = append(out, c)
		}
	}
	return out
}

// sgroup is a group taking part in one scheduler call.
type sgroup struct {
	plan  *fplan // nil: free group
	g     *fgroup
	in    []bool // free group's squares
	goal  []bool
	off   int // offset of its slot(s) in the state vector
	width int
	rank  int
}

// schedule runs the DFS over joint states from the current real position and
// returns the complete schedule, or the path to the furthest state, and that state.
func (f *factor) schedule(sg []sgroup, cap int) (bool, []fpush, []int32) {
	w := 1 // slot 0: canonical player cell
	for i := range sg {
		sg[i].off = w
		if sg[i].plan != nil {
			sg[i].width = 1
		} else {
			sg[i].width = len(sg[i].g.boxes)
		}
		w += sg[i].width
	}
	static := slices.Clone(f.occ)
	for i := range sg {
		for _, c := range sg[i].g.cells {
			static[c] = false
		}
	}
	occ := f.occ2
	setOcc := func(st []int32) {
		copy(occ, static)
		for i := range sg {
			s := &sg[i]
			if s.plan != nil {
				j := int(st[s.off])
				for _, c := range s.plan.pos[j*s.plan.nb : (j+1)*s.plan.nb] {
					occ[c] = true
				}
			} else {
				for _, c := range st[s.off : s.off+s.width] {
					occ[c] = true
				}
			}
		}
	}
	// arena of states; parent index and the move into it
	arena := make([]int32, 0, w*1024)
	parent := []int32{-1}
	moveG := []int16{-1}
	moveP := []fpush{{}}
	index := map[uint64][]int32{}
	hash := func(st []int32) uint64 {
		h := uint64(1469598103934665603)
		for _, x := range st {
			h = (h ^ uint64(uint32(x))) * 1099511628211
		}
		return h
	}
	find := func(st []int32, h uint64) bool {
		for _, id := range index[h] {
			if slices.Equal(arena[int(id)*w:int(id+1)*w], st) {
				return true
			}
		}
		return false
	}
	start := make([]int32, w)
	for i := range sg {
		s := &sg[i]
		if s.plan == nil {
			copy(start[s.off:], f.curBoxes(s.g))
		}
	}
	setOcc(start)
	start[0] = f.reach(f.pl, occ)
	arena = append(arena, start...)
	index[hash(start)] = []int32{0}
	type item struct {
		id   int32
		last int16
	}
	stack := []item{{0, -1}}
	done := func(st []int32) bool {
		for i := range sg {
			s := &sg[i]
			if s.plan != nil {
				if int(st[s.off]) != len(s.plan.pushes) {
					return false
				}
			} else {
				for _, c := range st[s.off : s.off+s.width] {
					if !s.goal[c] {
						return false
					}
				}
			}
		}
		return true
	}
	score := func(st []int32) int {
		t := 0
		for i := range sg {
			s := &sg[i]
			if s.plan != nil {
				t += int(st[s.off])
			} else {
				for _, c := range st[s.off : s.off+s.width] {
					if s.goal[c] {
						t++
					}
				}
			}
		}
		return t
	}
	best, bestID, found := score(start), int32(0), int32(-1)
	type succ struct {
		pri float64
		g   int16
		mv  fpush
	}
	var succs []succ
	child := make([]int32, w)
	expanded := 0
	for len(stack) > 0 && expanded < cap {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		expanded++
		st := arena[int(it.id)*w : int(it.id+1)*w]
		if sc := score(st); sc > best {
			best, bestID = sc, it.id
		}
		if done(st) {
			found = it.id
			break
		}
		setOcc(st)
		f.reach(st[0], occ) // marks the region
		succs = succs[:0]
		for gi := range sg {
			s := &sg[gi]
			if s.plan != nil {
				j := int(st[s.off])
				if j >= len(s.plan.pushes) {
					continue
				}
				p := s.plan.pushes[j]
				dd := f.d[p.dir]
				if f.mark[p.box-dd] != f.stamp || occ[p.box+dd] {
					continue
				}
				pri := 1.0 + 0.01*float64(len(sg)-s.rank)
				if int16(gi) == it.last {
					pri += 1
				}
				succs = append(succs, succ{pri, int16(gi), p})
				continue
			}
			allHome := true
			for _, c := range st[s.off : s.off+s.width] {
				allHome = allHome && s.goal[c]
			}
			if allHome {
				continue
			}
			for _, b := range st[s.off : s.off+s.width] {
				for k, dd := range f.d {
					t := b + dd
					if !s.in[t] || occ[t] || f.mark[b-dd] != f.stamp {
						continue
					}
					pri := 0.0
					if s.goal[t] && !s.goal[b] {
						pri = 3
					}
					succs = append(succs, succ{pri, int16(gi), fpush{b, int8(k)}})
				}
			}
		}
		slices.SortStableFunc(succs, func(a, b succ) int {
			switch {
			case a.pri < b.pri:
				return -1
			case a.pri > b.pri:
				return 1
			}
			return 0
		})
		for _, sc := range succs {
			s := &sg[sc.g]
			copy(child, st)
			if s.plan != nil {
				child[s.off]++
			} else {
				seg := child[s.off : s.off+s.width]
				seg[slices.Index(seg, sc.mv.box)] = sc.mv.box + f.d[sc.mv.dir]
				slices.Sort(seg)
			}
			child[0] = 0
			setOcc(child)
			child[0] = f.reach(sc.mv.box, occ)
			h := hash(child)
			if find(child, h) {
				continue
			}
			id := int32(len(parent))
			arena = append(arena, child...)
			parent = append(parent, it.id)
			moveG = append(moveG, sc.g)
			moveP = append(moveP, sc.mv)
			index[h] = append(index[h], id)
			stack = append(stack, item{id, sc.g})
		}
	}
	end := bestID
	if found >= 0 {
		end = found
	}
	var path []fpush
	for id := end; parent[id] >= 0; id = parent[id] {
		path = append(path, moveP[id])
	}
	slices.Reverse(path)
	return found >= 0, path, slices.Clone(arena[int(end)*w : int(end+1)*w])
}

// trySchedule schedules every unfinished group (plans and free groups) over
// several component orders and applies the complete or the furthest result.
func (f *factor) trySchedule() (bool, int) {
	n := len(f.lv.Wall)
	var sg []sgroup
	need := false
	add := func(g *fgroup) {
		if p := f.live[gkey(g)]; p != nil {
			sg = append(sg, sgroup{plan: p, g: g})
			need = true
		} else if f.isFree(g) {
			sg = append(sg, sgroup{g: g, in: cellSet(n, g.cells), goal: cellSet(n, g.goals)})
			need = need || !f.groupDone(g)
		}
	}
	for i := range f.groups {
		if !f.groupDone(&f.groups[i]) {
			add(&f.groups[i])
		}
	}
	for i := range f.triv {
		if f.isFree(&f.triv[i]) {
			add(&f.triv[i])
		}
	}
	if !need {
		return false, 0
	}
	var pk []int
	for i := range sg {
		if sg[i].plan != nil {
			pk = append(pk, i)
		}
	}
	slices.SortStableFunc(pk, func(a, b int) int { return len(sg[a].plan.pushes) - len(sg[b].plan.pushes) })
	var prefs [][]int
	if len(pk) <= 4 {
		permute(pk, 0, &prefs)
	} else {
		for r := range pk {
			prefs = append(prefs, append(slices.Clone(pk[r:]), pk[:r]...))
		}
	}
	if len(prefs) == 0 {
		prefs = [][]int{nil}
	}
	cap := max(20000, FactorSched/len(prefs))
	var bOK bool
	var bPath []fpush
	var bEnd []int32
	first := true
	for _, pref := range prefs {
		for i := range sg {
			sg[i].rank = len(sg)
		}
		for r, i := range pref {
			sg[i].rank = r
		}
		ok, path, end := f.schedule(sg, cap)
		if first || ok || len(path) > len(bPath) {
			bOK, bPath, bEnd, first = ok, path, end, false
		}
		if ok || f.expired() {
			break
		}
	}
	for _, p := range bPath {
		if !f.push(p) {
			return false, 0 // cannot happen: every step was checked
		}
	}
	for i := range sg {
		s := &sg[i]
		if s.plan == nil {
			continue
		}
		key := gkey(s.g)
		switch j := int(bEnd[s.off]); {
		case j == len(s.plan.pushes):
			delete(f.live, key)
		case j > 0:
			f.live[key] = &fplan{pushes: s.plan.pushes[j:], pos: s.plan.pos[j*s.plan.nb:], nb: s.plan.nb}
		}
	}
	return bOK, len(bPath)
}

// permute lists the orders of xs[k:] (component orders, k <= 4 groups).
func permute(xs []int, k int, out *[][]int) {
	if k == len(xs) {
		*out = append(*out, slices.Clone(xs))
		return
	}
	for i := k; i < len(xs); i++ {
		xs[k], xs[i] = xs[i], xs[k]
		permute(xs, k+1, out)
		xs[k], xs[i] = xs[i], xs[k]
	}
}

func (f *factor) resetBoard() {
	clear(f.occ)
	f.boxes = slices.Clone(f.lv.Boxes)
	for _, b := range f.boxes {
		f.occ[b] = true
	}
	f.pl = f.lv.Player
	f.lurd = f.lurd[:0]
}

func (f *factor) allDone() bool {
	for _, b := range f.boxes {
		if !f.lv.Goal[b] {
			return false
		}
	}
	return true
}

// blocker: the other group or trivial component whose boxes keep the player from
// group g's next push (or from all of its boxes). Returns (index, trivial?) or -1.
func (f *factor) blocker(g *fgroup) (int, bool) {
	var want []int32
	if p := f.live[gkey(g)]; p != nil && len(p.pushes) > 0 {
		want = []int32{p.pushes[0].box - f.d[p.pushes[0].dir]}
	} else {
		for _, b := range f.curBoxes(g) {
			for _, dd := range f.d {
				want = append(want, b+dd)
			}
		}
	}
	type cand struct {
		i    int
		triv bool
		nb   int
	}
	var cs []cand
	for i := range f.groups {
		if &f.groups[i] != g {
			cs = append(cs, cand{i, false, len(f.groups[i].boxes)})
		}
	}
	for i := range f.triv {
		cs = append(cs, cand{i, true, len(f.triv[i].boxes)})
	}
	slices.SortStableFunc(cs, func(a, b cand) int { return a.nb - b.nb })
	occ := f.occ2
	for _, c := range cs {
		var h *fgroup
		if c.triv {
			h = &f.triv[c.i]
		} else {
			h = &f.groups[c.i]
		}
		copy(occ, f.occ)
		for _, x := range h.cells {
			occ[x] = false
		}
		f.reach(f.pl, occ)
		for _, x := range want {
			if !f.lv.Wall[x] && f.mark[x] == f.stamp {
				return c.i, c.triv
			}
		}
	}
	return -1, false
}

func mergeGroups(a, b *fgroup) fgroup {
	u := func(x, y []int32) []int32 {
		z := append(slices.Clone(x), y...)
		slices.Sort(z)
		return z
	}
	return fgroup{u(a.cells, b.cells), u(a.boxes, b.boxes), u(a.goals, b.goals)}
}

// attempt is one run with the current pool: plan, schedule, re-plan, merge.
func (f *factor) attempt(nt, triv []fgroup) bool {
	f.groups = slices.Clone(nt)
	f.triv = slices.Clone(triv)
	n := len(f.lv.Wall)
	for {
		f.resetBoard()
		f.live = map[string]*fplan{}
		for step := range factorSteps {
			order := make([]int, len(f.groups))
			for i := range order {
				order[i] = i
			}
			slices.SortStableFunc(order, func(a, b int) int { return len(f.groups[a].boxes) - len(f.groups[b].boxes) })
			for _, i := range order {
				g := &f.groups[i]
				if f.isFree(g) || f.cache[gkey(g)] != nil || f.pool-f.used < f.reserve+1000 || f.expired() {
					continue
				}
				if p := f.relaxedPlan(g, step); p != nil {
					f.cache[gkey(g)] = p
				}
			}
		}
		for i := range f.groups {
			g := &f.groups[i]
			if f.isFree(g) {
				continue
			}
			p := f.cache[gkey(g)]
			if p == nil {
				if factorDebug {
					fmt.Fprintf(os.Stderr, "factor: no plan for a %d-box group\n", len(g.boxes))
				}
				return false
			}
			f.live[gkey(g)] = p
		}
		tried := map[string]bool{}
		solved := false
		for round := 1; ; round++ {
			if f.allDone() {
				solved = true
				break
			}
			if round > 12 || f.pool-f.used < 1000 || f.expired() {
				break
			}
			ok, moved := f.trySchedule()
			if factorDebug {
				fmt.Fprintf(os.Stderr, "factor: schedule round %d ok=%v pushes=%d groups=%d\n", round, ok, moved, len(f.groups))
			}
			if ok {
				continue
			}
			progressed := false
			var left []int
			for i := range f.groups {
				if !f.groupDone(&f.groups[i]) {
					left = append(left, i)
				}
			}
			off := func(i int) int {
				c := 0
				for _, b := range f.curBoxes(&f.groups[i]) {
					if !f.lv.Goal[b] {
						c++
					}
				}
				return c
			}
			slices.SortStableFunc(left, func(a, b int) int { return off(a) - off(b) })
			for _, i := range left {
				g := &f.groups[i]
				if f.isFree(g) {
					continue
				}
				key := gkey(g) + string(f.lurdKey())
				if tried[key] {
					continue
				}
				tried[key] = true
				mine := f.curBoxes(g)
				walls := slices.Clone(f.occ)
				for _, b := range mine {
					walls[b] = false
				}
				rows := f.rows(walls, cellSet(n, mine), cellSet(n, g.goals), f.pl)
				for _, c := range replanCalls() {
					sol, why := f.subSolve(rows, c.lane, c.cap*f.capScale)
					if sol != "" && f.applyLURD(sol) {
						delete(f.live, gkey(g))
						progressed = true
						break
					}
					if why == "pool" || why == "parse" || why == "infeasible" {
						break
					}
				}
				if progressed {
					break
				}
			}
			if !progressed && moved == 0 {
				break
			}
		}
		if solved {
			return true
		}
		if f.pool-f.used < f.reserve/2 || f.expired() {
			return false
		}
		if f.altEngine() {
			continue // another engine's plan for an unfinished group: schedule again from the start
		}
		if FactorAlt && f.altPlan() {
			continue // a different plan for a stuck group: schedule again from the start
		}
		// merge the first stuck group with its blocker; restart from the initial position
		bi, bt, si := -1, false, -1
		for i := range f.groups {
			if f.groupDone(&f.groups[i]) {
				continue
			}
			if j, t := f.blocker(&f.groups[i]); j >= 0 {
				bi, bt, si = j, t, i
				break
			}
		}
		if si < 0 || (!bt && len(f.groups) == 2 && len(f.triv) == 0) {
			return false // no blocker, or the merge would be the whole board
		}
		var other fgroup
		if bt {
			other = f.triv[bi]
			f.triv = slices.Delete(f.triv, bi, bi+1)
		} else {
			other = f.groups[bi]
		}
		m := mergeGroups(&f.groups[si], &other)
		if factorDebug {
			fmt.Fprintf(os.Stderr, "factor: merge %d + %d boxes (trivial %v)\n", len(f.groups[si].boxes), len(other.boxes), bt)
		}
		var keep []fgroup
		for i := range f.groups {
			if i != si && (bt || i != bi) {
				keep = append(keep, f.groups[i])
			}
		}
		f.groups = append(keep, m)
	}
}

// lurdKey identifies the real position (boxes and player) for the tried set.
func (f *factor) lurdKey() []byte {
	b := make([]byte, 0, 4*len(f.boxes)+4)
	bx := slices.Clone(f.boxes)
	slices.Sort(bx)
	for _, x := range append(bx, f.pl) {
		b = append(b, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
	}
	return b
}

// factorRun is the lane: -strategy factor alone (-nodes N = one attempt with an N-node
// pool, as the Tier 0 prototype), or in the portfolio with doubling pools until the deadline.
func factorRun(lv *Level, deadline time.Time, memLimit uint64, solo bool) Result {
	nt, triv := boxComponents(lv)
	if len(nt) < 2 {
		return Result{Reason: "factor: fewer than 2 box components"}
	}
	n := len(lv.Wall)
	f := &factor{lv: lv, d: [4]int32{-int32(lv.W), int32(lv.W), -1, 1}, deadline: deadline, memLimit: memLimit, solo: solo,
		cache: map[string]*fplan{}, occ: make([]bool, n), occ2: make([]bool, n), mark: make([]uint32, n), prev: make([]int32, n)}
	defer f.dropKept() // exp-660: no paused sub-search outlives the lane
	pool := int64(FactorPool)
	if NodeBudget > 0 {
		pool = NodeBudget
	}
	for scale := int64(1); ; scale *= 2 {
		f.capScale, f.pool, f.used, f.reserve = scale, pool*scale, 0, 40000*scale
		if f.attempt(nt, triv) {
			return Result{Solved: true, Solution: string(f.lurd), Nodes: int(f.used)}
		}
		if solo && f.pool-f.used >= 1000 && !f.expired() {
			// last resort (alone only; in the portfolio other lanes own the real board)
			rows := f.rows(make([]bool, n), f.occ, lv.Goal, f.pl)
			if sol, _ := f.subSolve(rows, "macro", f.pool-f.used); sol != "" && f.applyLURD(sol) && f.allDone() {
				return Result{Solved: true, Solution: string(f.lurd), Nodes: int(f.used)}
			}
		}
		if NodeBudget > 0 || f.expired() {
			return Result{Reason: "factor: unsolved", Nodes: int(f.used)}
		}
	}
}

// altPlan gives the first unfinished group that still has untried variants a new
// relaxed plan from the initial position: the other groups' goals as walls (it
// goes last), their initial boxes as walls (it goes first), or another engine.
func (f *factor) altPlan() bool {
	n := len(f.lv.Wall)
	if f.altTried == nil {
		f.altTried = map[string]int{}
	}
	for i := range f.groups {
		g := &f.groups[i]
		if f.groupDone(g) || f.isFree(g) {
			continue
		}
		key := gkey(g)
		for f.altTried[key] < 4 {
			v := f.altTried[key]
			f.altTried[key]++
			w := make([]bool, n)
			for _, t := range f.triv {
				for _, b := range t.boxes {
					w[b] = true
				}
			}
			lanes := []string{"macro", "fa2"}
			switch v {
			case 0, 1:
				for j := range f.groups {
					if j == i {
						continue
					}
					src := f.groups[j].goals
					if v == 1 {
						src = f.groups[j].boxes
					}
					for _, c := range src {
						w[c] = true
					}
				}
			case 2:
				lanes = []string{"novelty"}
			case 3:
				lanes = []string{"fa2"}
			}
			rows := f.rows(w, cellSet(n, g.boxes), cellSet(n, g.goals), f.lv.Player)
			for _, lane := range lanes {
				sol, why := f.subSolve(rows, lane, 30000*f.capScale)
				if sol != "" {
					if ps := f.pushesOf(sol, g.boxes, w, f.lv.Player); ps != nil {
						old := f.cache[key]
						if old == nil || !slices.Equal(old.pushes, ps) {
							f.cache[key] = newFPlan(g.boxes, ps, f.d)
							return true
						}
					}
				}
				if why == "pool" {
					return false
				}
				if why == "parse" {
					break
				}
			}
		}
	}
	return false
}

// resumeFA2 continues the kept fa2 engine of sub-level rkey up to cap expansions in total.
func (f *factor) resumeFA2(rkey string, cap int64) (string, string, bool) {
	fr, spent := f.keepFA2, f.keepN["fa2"]
	delta := min(cap-spent, f.pool-f.used)
	if delta <= 0 || f.expired() {
		if delta <= 0 && cap-spent > 0 {
			return "", "pool", true
		}
		return "", "timeout", true
	}
	before := fr.s.expandedN
	r, done := fr.runTo(before + delta)
	n := fr.s.expandedN - before
	TrackedF.Store(0)
	f.used += min(n, delta)
	f.subCalls++
	if factorDebug {
		fmt.Fprintf(os.Stderr, "factor: resume fa2 cap=%d nodes=%d solved=%v why=%s used=%d/%d t=%.1fs\n", cap, n, r.Solved, r.Reason, f.used, f.pool, time.Since(factorT0).Seconds())
	}
	f.keepN["fa2"] = spent + n
	if done {
		f.keepFA2 = nil
		if r.Solved && r.Solution != "" {
			f.subSolved++
			return r.Solution, "", true
		}
		if infeasibleWhy(r.Reason) {
			if f.infeasible == nil {
				f.infeasible = map[string]bool{}
			}
			f.infeasible[rkey] = true
			return "", "infeasible", true
		}
		return "", r.Reason, true
	}
	return "", "timeout", true
}

// altEngine (exp-660, portfolio): plans compose or not depending on the
// engine that found them (exp-660: Pokorny #102's 18-box group schedules with
// macro's plan, not with fa2's). Before merging, give the first unfinished
// group an engine it has not been planned by yet; a new plan restarts the
// schedule from the initial position.
func (f *factor) altEngine() bool {
	for i := range f.groups {
		g := &f.groups[i]
		if f.groupDone(g) || f.isFree(g) {
			continue
		}
		key := gkey(g)
		for _, e := range factorEnginesP {
			if f.planEng[key][e] || f.expired() || f.pool-f.used < f.reserve/2 {
				continue
			}
			if f.planEng == nil {
				f.planEng = map[string]map[string]bool{}
			}
			if f.planEng[key] == nil {
				f.planEng[key] = map[string]bool{}
			}
			f.planEng[key][e] = true
			for step := range factorSteps {
				if p := f.relaxedPlanBy(g, step, e); p != nil {
					if old := f.cache[key]; old == nil || !slices.Equal(old.pushes, p.pushes) {
						f.cache[key] = p
						if factorDebug {
							fmt.Fprintf(os.Stderr, "factor: alt engine %s re-planned a %d-box group (%d pushes)\n", e, len(g.boxes), len(p.pushes))
						}
						return true
					}
					break
				}
			}
		}
	}
	return false
}
