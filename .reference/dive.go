package main

import (
	"fmt"
	"math"
	"os"
	"slices"
	"sync/atomic"
	"time"
)

// dive (exp-691; design research/exp-685/REPORT.md, Design 1): a
// landmark-phased greedy backward search. Provenance: hybrid. The greedy
// maximum-distance dive and the chain-prefix return order are Festival's
// snail mode (festival-derived); the landmark term E, the start-overlap return
// order, both orders as cell axes and the portfolio rule are ours.
//
// The pull view starts from the solved board. A landmark is a real goal that
// is not a start square: every backward solution must vacate it. While some
// landmark holds a box (E > 0) the search is one zero-weight greedy
// best-first queue on (fewer E, fewer U, larger D): U counts boxes with no
// neighbour in the player's region, D is the min-cost matching distance from
// the boxes back to the real goals (Festival's push distances). The dive walks
// away from the start, every box kept touchable, until the landmarks fall.
// Once E = 0 a node joins two FESS-style cell axes (best cell and round robin
// alternate inside an axis):
//   - R1: cell (U, boxes on start squares), then the least matching distance
//     to the start squares;
//   - R2: cell (U, prefix), then the largest D. The prefix is Festival's chain
//     prefix (goal squares in BFS order from the first landmark). exp-693
//     deleted the deepest-peel-first variant (-dive-r2 peel, 0/9 at 300k).
// Every fourth selection goes to the dive queue while return nodes exist.
// Goal test, meetings, pruning (pull freeze, -pf-reach, zone-dead roots) are
// the pull lane's.

// DiveLane (-dive) adds the dive lane to the portfolio on boards where some
// start-empty goal has solved-board peel depth >= DiveMinPeel.
var DiveLane = true // exp-691 promoted 2026-11-12 (default on; static peel>=10 rule)

// DiveMinPeel is the static activation rule's peel depth (exp-520's SnailMin).
var DiveMinPeel = 10

// DiveRet selects the return axes: both, r1 or r2.
var DiveRet = "both"

// DiveShare (exp-693, "": off) runs the dive inside another lane's goroutine
// on gated boards, alternating fixed expansion slices on one baton (no 9th
// thread): "pull" (the backward FESS lane). DiveReplace drops a lane on gated
// boards instead ("reverse", "pull", ...), the dive keeping its own lane.
var DiveShare string

// DiveReplace: exp-693 promoted 2026-11-12 ("reverse"; "" restores exp-691's 9th thread).
var DiveReplace = "reverse"

// DiveA (exp-693, Festival strategy A's dive terms, festival-derived): in the
// dive, after U, fewer crowded boxes (>= 6 of 8 neighbours wall or box), then
// more box-gap-box triples.
var DiveA bool

// diveA is strategy A's term for a board in s.occ (smaller is better; 0 when off).
func (s *solver) diveA(boxes []int32) int32 {
	if !(DiveA || s.diveAOn) {
		return 0
	}
	w := int32(s.lv.W)
	full := func(c int32) bool { return s.lv.Wall[c] || s.occ[c] }
	crowd, gap := int32(0), int32(0)
	for _, b := range boxes {
		n := 0
		for _, o := range [8]int32{-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1} {
			if c := b + o; c >= 0 && int(c) < len(s.occ) && full(c) {
				n++
			}
		}
		if n >= 6 {
			crowd++
		}
		for _, d := range [2]int32{1, w} {
			if c := b + 2*d; int(c) < len(s.occ) && s.occ[c] && !full(b+d) {
				gap++
			}
		}
	}
	return crowd<<12 - min(gap, 4095)
}

// DivePair (exp-693): the dive lane runs two engines on one baton (one core):
// the base dive and the -dive-a dive, each with half the lane's memory (and
// half of -nodes when run alone).
var DivePair = true // exp-693 promoted 2026-11-12

// TrackedDiveA: the paired A engine's tree.
var TrackedDiveA atomic.Uint64

// DiveFastApply (exp-693) stores children without re-running the box search.
var DiveFastApply = true

// DiveMemW (exp-693) is the dive's memory weight in the lane split (reverse
// has 3, other lanes 1); it only applies on gated boards.
var DiveMemW uint64 = 4 // exp-693 promoted 2026-11-12

// DiveShareMask: a sharing engine yields every DiveShareMask+1 expansions.
var DiveShareMask int64 = 1023

// diveGated: the board passed the activation rule (main sets it).
var diveGated bool

// BudgetDive, TrackedDive: the dive lane's memory slots.
var BudgetDive, TrackedDive atomic.Uint64

// DiveOwner is the dive lane's backward mailbox and Best slot.
const DiveOwner = 6

// diveMaxPeel returns the deepest solved-board peel depth of a start-empty
// goal (0 if none): boxes on all goals are pulled off layer by layer (a box
// leaves when some side has two free floor cells; the player is ignored).
func diveMaxPeel(lv *Level) int {
	peel := divePeel(lv.Wall, lv.Goal, lv.W, lv.Player)
	start := make([]bool, len(lv.Wall))
	for _, b := range lv.Boxes {
		start[b] = true
	}
	best := 0
	for c, g := range lv.Goal {
		if g && !start[c] && peel[c] > best {
			best = peel[c]
		}
	}
	return best
}

// divePeel peels a board with boxes on every occ cell; it returns each
// occupied cell's layer (cells never peeled get the last layer + 5; -1 for
// unoccupied cells). Floor is what the player can reach ignoring boxes.
func divePeel(wall, occ0 []bool, w int, player int32) []int {
	n := len(wall)
	dirs := [4]int32{-int32(w), int32(w), -1, 1}
	floor := make([]bool, n)
	q := []int32{player}
	floor[player] = true
	for i := 0; i < len(q); i++ {
		for _, d := range dirs {
			if y := q[i] + d; y >= 0 && int(y) < n && !wall[y] && !floor[y] {
				floor[y] = true
				q = append(q, y)
			}
		}
	}
	occ := slices.Clone(occ0)
	lay := make([]int, n)
	left := 0
	for c := range lay {
		lay[c] = -1
		if occ[c] {
			left++
		}
	}
	free := func(c int32) bool { return c >= 0 && int(c) < n && floor[c] && !occ[c] }
	L := 0
	var cur []int32
	for left > 0 {
		cur = cur[:0]
		for c := range int32(n) {
			if !occ[c] {
				continue
			}
			for _, d := range dirs {
				if free(c+d) && free(c+2*d) {
					cur = append(cur, c)
					break
				}
			}
		}
		if len(cur) == 0 {
			break
		}
		for _, c := range cur {
			lay[c] = L
			occ[c] = false
			left--
		}
		L++
	}
	for c := range occ {
		if occ[c] {
			lay[c] = L + 5
		}
	}
	return lay
}

// diveItem is a queue entry: lexicographic key (smaller first), newest first
// on ties.
type diveItem struct {
	k   [4]int32
	seq uint32
	id  int32
}

func diveLess(a, b diveItem) bool {
	if a.k != b.k {
		for i := range a.k {
			if a.k[i] != b.k[i] {
				return a.k[i] < b.k[i]
			}
		}
	}
	return a.seq > b.seq
}

type diveHeap []diveItem

func (h *diveHeap) push(x diveItem) {
	*h = append(*h, x)
	a := *h
	i := len(a) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !diveLess(a[i], a[p]) {
			break
		}
		a[i], a[p] = a[p], a[i]
		i = p
	}
}

func (h *diveHeap) pop() diveItem {
	a := *h
	top := a[0]
	n := len(a) - 1
	a[0] = a[n]
	a = a[:n]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && diveLess(a[l], a[m]) {
			m = l
		}
		if r < n && diveLess(a[r], a[m]) {
			m = r
		}
		if m == i {
			break
		}
		a[i], a[m] = a[m], a[i]
		i = m
	}
	*h = a
	return top
}

// diveAxis is one return order's FESS cells: cell key (U, -R), sorted.
type diveAxis struct {
	ids   map[[2]int32]int
	keys  [][2]int32
	heaps []diveHeap
	order []int // cell indices by key
	turn  int
	rr    int
	size  int
}

func (a *diveAxis) add(cell [2]int32, it diveItem) {
	if a.ids == nil {
		a.ids = map[[2]int32]int{}
	}
	c, ok := a.ids[cell]
	if !ok {
		c = len(a.keys)
		a.ids[cell] = c
		a.keys = append(a.keys, cell)
		a.heaps = append(a.heaps, nil)
		pos, _ := slices.BinarySearchFunc(a.order, cell, func(x int, k [2]int32) int {
			kx := a.keys[x]
			if kx[0] != k[0] {
				return int(kx[0] - k[0])
			}
			return int(kx[1] - k[1])
		})
		a.order = slices.Insert(a.order, pos, c)
	}
	a.heaps[c].push(it)
	a.size++
}

// pop alternates the best non-empty cell and round robin over the non-empty
// cells (the prototype's schedule).
func (a *diveAxis) pop() (diveItem, bool) {
	if a.size == 0 {
		return diveItem{}, false
	}
	a.turn++
	pick := -1
	if a.turn%2 == 1 {
		for _, c := range a.order {
			if len(a.heaps[c]) > 0 {
				pick = c
				break
			}
		}
	} else {
		live := 0
		for _, c := range a.order {
			if len(a.heaps[c]) > 0 {
				live++
			}
		}
		a.rr++
		k := a.rr % live
		for _, c := range a.order {
			if len(a.heaps[c]) > 0 {
				if k == 0 {
					pick = c
					break
				}
				k--
			}
		}
	}
	a.size--
	return a.heaps[pick].pop(), true
}

func (a *diveAxis) bytes() int {
	n := cap(a.keys)*8 + cap(a.order)*8 + len(a.ids)*48
	for _, h := range a.heaps {
		n += cap(h) * 24
	}
	return n
}

// diveDual is a saved Hungarian solution for one-row child updates.
type diveDual struct {
	dual []int32
	cost int64
}

type diveState struct {
	land, start []bool // pull view: landmark (real goal, not start square), start square
	chain       []int32
	chainLen    int32
	dive        diveHeap
	axes        [2]diveAxis
	axisOn      [2]bool
	done        []uint64
	depth       []int32
	seq         uint32
	turn, ret   int
	dD, dR      diveDual
	// diagnostics
	freeExp, freeDepth, freeDist int32
	bestE                        int32
	popsDive, popsRet            int
}

func (d *diveState) bytes() int {
	return cap(d.dive)*24 + d.axes[0].bytes() + d.axes[1].bytes() + cap(d.done)*8 + cap(d.depth)*4
}

// matchPrime solves the assignment of boxes to dist's goals and saves the
// dual for matchChild (the hfMatchParent/hfMatchChild scheme with its own
// buffers). Infeasible: assignmentInf.
func (s *solver) matchPrime(boxes, dist []int32, dd *diveDual) int32 {
	score := s.assignTo(boxes, dist)
	if score < 0 {
		score = assignmentInf
	}
	m := s.n + 1
	if len(dd.dual) != 4*m {
		dd.dual = make([]int32, 4*m)
	}
	copy(dd.dual[:m], s.hu)
	copy(dd.dual[m:2*m], s.hv)
	copy(dd.dual[2*m:3*m], s.hp)
	for g := 1; g < m; g++ {
		dd.dual[3*m+int(s.hp[g])] = int32(g)
	}
	dd.cost = int64(score)
	if score == assignmentInf {
		dd.cost = s.assignmentCost(boxes, dist)
	}
	return score
}

// matchChild is the matching cost with boxes[moved] replaced (old was there).
func (s *solver) matchChild(boxes []int32, moved int, old int32, dist []int32, dd *diveDual) int32 {
	m, cells := s.n+1, len(s.lv.Wall)
	row := int32(moved + 1)
	goal := dd.dual[3*m+int(row)]
	v := dd.dual[m : 2*m]
	box := boxes[moved]
	u := int32(math.MaxInt32)
	for j := int32(1); j < int32(m); j++ {
		u = min(u, assignmentEdge(dist, cells, box, j)-v[j])
	}
	newCost := assignmentEdge(dist, cells, box, goal)
	if u == newCost-v[goal] {
		oldCost := assignmentEdge(dist, cells, old, goal)
		return int32(min(assignmentInf, dd.cost+int64(newCost)-int64(oldCost)))
	}
	copy(s.hu, dd.dual[:m])
	copy(s.hv, v)
	copy(s.hp, dd.dual[2*m:3*m])
	s.hu[row], s.hp[goal], s.hv[0] = u, 0, 0
	s.augmentAssignment(boxes, dist, row)
	return int32(min(assignmentInf, s.assignmentCost(boxes, dist)))
}

func (s *solver) diveSetup() {
	lv := s.lv // pull view: Boxes = real goals, Goal = start squares
	n := len(lv.Wall)
	if s.tseen == nil {
		s.tseen = make([]uint32, 4*n)
	}
	d := &diveState{land: make([]bool, n), start: slices.Clone(lv.Goal), freeExp: -1}
	switch DiveRet {
	case "r1":
		d.axisOn[0] = true
	case "r2":
		d.axisOn[1] = true
	default:
		d.axisOn = [2]bool{true, true}
	}
	realGoal := make([]bool, n)
	for _, g := range lv.Boxes {
		realGoal[g] = true
		if !lv.Goal[g] {
			d.land[g] = true
		}
	}
	// Chain: BFS over real goal squares from the first landmark.
	d.chain = make([]int32, n)
	for i := range d.chain {
		d.chain[i] = -1
	}
	for _, g := range lv.Boxes {
		if !d.land[g] {
			continue
		}
		d.chain[g] = 0
		q := []int32{g}
		for i := 0; i < len(q); i++ {
			for _, dir := range s.dirs {
				if y := q[i] + dir; realGoal[y] && d.chain[y] < 0 {
					d.chain[y] = d.chain[q[i]] + 1
					d.chainLen = max(d.chainLen, d.chain[y]+1)
					q = append(q, y)
				}
			}
		}
		break
	}
	d.bestE = math.MaxInt32
	s.dv = d
}

// diveFeat computes the child-only features of a stored node (s.occ holds its
// boxes): U, prefix and whether the real player is in its region.
func (s *solver) diveFeat(id int32, boxes []int32) (u, prefix int32, home bool) {
	d := s.dv
	s.reach(s.player[id], s.mark2)
	st := s.stamp
	for _, b := range boxes {
		touched := false
		for _, dir := range s.dirs {
			if s.mark2[b+dir] == st {
				touched = true
				break
			}
		}
		if !touched {
			u++
		}
	}
	home = s.mark2[s.realPlayer] == st
	if d.chainLen > 0 {
		prefix = d.chainLen
		for _, g := range s.lv.Boxes {
			if v := d.chain[g]; v >= 1 && !s.occ[g] && v < prefix {
				prefix = v
			}
		}
	}
	return
}

// divePush files a new node by its features.
func (s *solver) divePush(id, e, u, a, dist, onStart, revh, prefix, depth int32) {
	d := s.dv
	for int(id) >= len(d.depth) {
		d.depth = append(d.depth, 0)
	}
	d.depth[id] = depth
	d.seq++
	if dist >= assignmentInf {
		dist = -1
	}
	if e > 0 {
		d.dive.push(diveItem{k: [4]int32{e, u, a, -dist}, seq: d.seq, id: id})
		return
	}
	if d.freeExp < 0 {
		d.freeExp, d.freeDepth, d.freeDist = int32(s.expandedN), depth, dist
	}
	if diveDebug && d.popsRet < 400 {
		fmt.Fprintf(os.Stderr, "free id=%d u=%d on=%d revh=%d pre=%d dist=%d depth=%d exp=%d\n", id, u, onStart, revh, prefix, dist, depth, s.expandedN)
	}
	if d.axisOn[0] {
		d.axes[0].add([2]int32{u, -onStart}, diveItem{k: [4]int32{revh}, seq: d.seq, id: id})
	}
	if d.axisOn[1] {
		d.axes[1].add([2]int32{u, -prefix}, diveItem{k: [4]int32{-dist}, seq: d.seq, id: id})
	}
}

// divePop picks the next node to expand (never one expanded before).
func (s *solver) divePop() (int32, bool) {
	d := s.dv
	for {
		ret := d.axes[0].size+d.axes[1].size > 0
		d.turn++
		var it diveItem
		ok := false
		if len(d.dive) > 0 && (!ret || d.turn%4 == 0) {
			it, ok = d.dive.pop(), true
			d.popsDive++
		} else if ret {
			d.ret++
			ax := d.ret % 2
			if d.axes[ax].size == 0 {
				ax = 1 - ax
			}
			it, ok = d.axes[ax].pop()
			d.popsRet++
			if diveDebug && d.popsRet < 400 {
				fmt.Fprintf(os.Stderr, "ret ax=%d key=%v id=%d depth=%d cells=%d\n", ax, it.k, it.id, d.depth[it.id], len(d.axes[ax].keys))
			}
		}
		if !ok {
			return -1, false
		}
		w := int(it.id) >> 6
		for w >= len(d.done) {
			d.done = append(d.done, 0)
		}
		if d.done[w]&(1<<(it.id&63)) != 0 {
			continue
		}
		d.done[w] |= 1 << (it.id & 63)
		return it.id, true
	}
}

var diveDebug = os.Getenv("DIVE_DEBUG") != ""

type diveCand struct{ i, t, side, plen int32 }

// diveApply is apply without the second box search (exp-693): the candidate's
// final push direction and length come from move generation.
func (s *solver) diveApply(id int32, c diveCand, cur, child []int32) int32 {
	s.lastAdded = -1
	b := cur[c.i]
	copy(child, cur)
	child[c.i] = c.t
	slices.Sort(child)
	s.occ[b] = false
	s.occ[c.t] = true
	np := s.reach(s.playerAfter(c.t, s.dirs[c.side]), s.mark2)
	cid := s.diveAdd(child, np, id, b, c.t, int8(c.side), s.g[id]+c.plen)
	s.occ[c.t] = false
	s.occ[b] = true
	return cid
}

// diveLoop runs the dive from the roots solve() stored.
func (s *solver) diveLoop(deadline time.Time, memLimit uint64) (res Result) {
	s.memLimit = memLimit
	s.pfInit() // the matching to the real goals (Festival's push distances)
	s.diveSetup()
	d := s.dv
	defer func() {
		if s.tr != nil {
			s.tr.SetExtra("dive_free_exp", d.freeExp)
			s.tr.SetExtra("dive_free_depth", d.freeDepth)
			s.tr.SetExtra("dive_free_dist", d.freeDist)
			s.tr.SetExtra("dive_best_e", d.bestE)
			s.tr.SetExtra("dive_cells", [2]int{len(d.axes[0].keys), len(d.axes[1].keys)})
			s.tr.SetExtra("dive_pops", [2]int{d.popsDive, d.popsRet})
			s.tr.SetExtra("dive_nodes", len(s.player))
		}
	}()
	cur, child := make([]int32, s.n), make([]int32, s.n)
	// Roots.
	for id := range int32(len(s.player)) {
		s.unpack(id, cur)
		for _, b := range cur {
			s.occ[b] = true
		}
		e, on := int32(0), int32(0)
		for _, b := range cur {
			if d.land[b] {
				e++
			}
			if d.start[b] {
				on++
			}
		}
		u, pre, _ := s.diveFeat(id, cur)
		dist := s.assignTo(cur, s.pfDist)
		if dist < 0 {
			dist = assignmentInf
		}
		revh := s.assignTo(cur, s.goalDist)
		if revh < 0 {
			revh = assignmentInf
		}
		for _, b := range cur {
			s.occ[b] = false
		}
		s.divePush(id, e, u, s.diveA(cur), dist, on, revh, pre, 0)
	}
	expanded := 0
	var lastMem time.Time
	var cands []diveCand
	for {
		if s.expired() {
			return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
		}
		if s.memFull {
			return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
		}
		if now := time.Now(); now.Sub(lastMem) >= 50*time.Millisecond {
			lastMem = now
			if s.stopCheck != nil && s.stopCheck() {
				return Result{Reason: "retarget", Nodes: expanded, Stats: s.finalStats()}
			}
			if sol, ok := s.checkMail(); ok {
				s.tr.SetExtra("met", "reverse")
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
			tb := s.trackedBytes()
			s.trackedP.Store(tb)
			backstopPct := uint64(88)
			if ElasticMem {
				backstopPct = 76
			}
			if s.overBudget(tb+s.peerBytes()) || s.backstopHit(memLimit, backstopPct, tb+s.peerBytes()) {
				return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
			}
		}
		id, ok := s.divePop()
		if !ok {
			return Result{Reason: "exhausted", Nodes: expanded, Stats: s.finalStats()}
		}
		expanded++
		s.expandedN++
		if s.bat != nil && s.expandedN&DiveShareMask == 0 && !s.bat.yield(s.batMe) {
			return Result{Reason: "stopped", Nodes: expanded, Stats: s.finalStats()} // exp-693
		}
		s.unpack(id, cur)
		var h, pE, pOn int32
		for _, b := range cur {
			s.occ[b] = true
			h += s.dist[b]
			if d.land[b] {
				pE++
			}
			if d.start[b] {
				pOn++
			}
		}
		if !s.feasible(cur) {
			for _, b := range cur {
				s.occ[b] = false
			}
			continue
		}
		s.reach(s.player[id], s.mark)
		st := s.stamp
		cands = cands[:0]
		for i, b := range cur {
			s.tst++
			ts := s.tst
			s.boxSearch(b, st, func(k, t int32) bool {
				canonical := int32(0)
				for side, dir := range s.dirs {
					if s.mark3[t-dir] == s.pstamp {
						canonical = int32(side)
						break
					}
				}
				endpoint := t*4 + canonical
				if s.tseen[endpoint] == ts {
					return true
				}
				s.tseen[endpoint] = ts
				s.occ[t] = true
				if s.pushDead(cur, i, t, k, canonical) == alive {
					plen := int32(1)
					for x := s.bprev[k]; x >= 0; x = s.bprev[x] {
						plen++
					}
					cands = append(cands, diveCand{int32(i), t, k % 4, plen})
					s.stats.Generated++
				} else {
					s.stats.Freeze++
				}
				s.occ[t] = false
				return true
			})
		}
		if len(cands) > 0 {
			s.matchPrime(cur, s.pfDist, &d.dD)
			primedR := false
			pDepth := d.depth[id]
			var solved int32 = -1
			for _, c := range cands {
				b := cur[c.i]
				cur[c.i] = c.t
				dist := s.matchChild(cur, int(c.i), b, s.pfDist, &d.dD)
				revh := int32(0)
				if d.axisOn[0] && pE-b2i(d.land[b])+b2i(d.land[c.t]) == 0 {
					if !primedR {
						cur[c.i] = b
						s.matchPrime(cur, s.goalDist, &d.dR)
						cur[c.i] = c.t
						primedR = true
					}
					revh = s.matchChild(cur, int(c.i), b, s.goalDist, &d.dR)
				}
				cur[c.i] = b
				e := pE
				on := pOn
				if d.land[b] {
					e--
				}
				if d.land[c.t] {
					e++
				}
				if d.start[b] {
					on--
				}
				if d.start[c.t] {
					on++
				}
				var cid int32
				if DiveFastApply {
					cid = s.diveApply(id, c, cur, child)
				} else {
					cid, _ = s.apply(id, encodeMove(c.i, c.t, 0, 1, c.side), cur, child, h, st)
				}
				if cid < 0 || int(cid) < len(d.depth) {
					continue // stored before (a reparent keeps its filing)
				}
				s.occ[b], s.occ[c.t] = false, true
				u, pre, home := s.diveFeat(cid, child)
				s.occ[c.t] = false
				s.occ[b] = true // in this order: a macro may end on its own square
				d.bestE = min(d.bestE, e)
				var a int32
				if (DiveA || s.diveAOn) && e > 0 {
					s.occ[b], s.occ[c.t] = false, true
					a = s.diveA(child)
					s.occ[c.t] = false
					s.occ[b] = true
				}
				s.divePush(cid, e, u, a, dist, on, revh, pre, pDepth+1)
				if diveDebug && on == int32(s.n) {
					fmt.Fprintf(os.Stderr, "allstart id=%d home=%v player=%d real=%d exp=%d\n", cid, home, s.player[cid], s.realPlayer, s.expandedN)
				}
				if on == int32(s.n) && home {
					solved = cid
					break
				}
			}
			if solved >= 0 {
				for _, b := range cur {
					s.occ[b] = false
				}
				if sol := s.pullSolution(solved); sol != "" {
					s.tr.SetExtra("dive_solved_depth", d.depth[solved])
					return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
				}
				continue
			}
		}
		for _, b := range cur {
			s.occ[b] = false
		}
		if len(s.meetPending) > 0 || len(s.pendCands) > 0 {
			s.postMeetings(cur)
		}
	}
}

func (d *diveState) bytesOrZero() int {
	if d == nil {
		return 0
	}
	return d.bytes()
}

func b2i(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// diveAdd is add for the dive: it keeps only the per-node arrays the pull
// path (transposition, pullTail, meetings) reads, about half of FESS's bytes
// per node. A duplicate reached by a shorter path is reparented, as in add.
func (s *solver) diveAdd(boxes []int32, player, parent, from, to int32, dir int8, g int32) int32 {
	packed := s.pack(boxes)
	slot, id, fp := s.find(packed, player)
	if id >= 0 {
		s.stats.Duplicates++
		s.lastAdded = id
		if g >= s.g[id] || ReparentCycleGuard && s.createsCycle(id, parent) {
			return -1
		}
		s.parent[id], s.pushFrom[id], s.pushTo[id], s.pushDir[id], s.g[id] = parent, from, to, dir, g
		return -1
	}
	if s.memFull {
		s.lastAdded = -1
		return -1
	}
	id = int32(len(s.player))
	s.lastAdded = id
	s.table[slot], s.tfp[slot] = id, fp
	s.boxes = append(s.boxes, packed...)
	s.player = append(s.player, player)
	s.parent = append(s.parent, parent)
	s.pushFrom = append(s.pushFrom, from)
	s.pushTo = append(s.pushTo, to)
	s.pushDir = append(s.pushDir, dir)
	s.g = append(s.g, g)
	if s.fwd != nil {
		k := s.fwd.Key(boxes, player)
		s.fwd.Add(k)
		if s.back != nil && s.meetPosted < maxMeetPosts && s.back.Has(k) {
			s.meetPending = append(s.meetPending, id)
			s.noteMeet()
		}
	}
	if len(s.player)*4 >= len(s.table)*3 {
		needed := s.trackedBytes() + uint64(len(s.table))*16 + s.peerBytes()
		if s.overBudget(needed) {
			s.memFull = true
		} else {
			s.grow()
		}
	}
	return id
}
