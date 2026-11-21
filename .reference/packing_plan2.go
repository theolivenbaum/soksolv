package main

import (
	"math/bits"
	"slices"
	"time"
)

// PlanV2 (exp-411) selects a rebuilt packing planner: a best-first BACKWARD
// PULL search from the solved board in which a box is removed when it is
// pulled onto an unused START-box cell (a "base"; one box per base, consumed).
// The plan is the pull/remove path of the best node. Its forward event list
// (packing_fest.go, mode 1) gives the packed feature Festival-like prefix
// semantics. Needs no sink room. Everything is flat arrays; children are
// generated once per expanded node, only the best PlanV2Beam are kept, and a
// child's state is materialised only when it is popped.
var PlanV2 = false

// PlanV2Beam is the number of children kept per expanded node; PlanV2Exp
// overrides the caller's expansion cap when positive.
var PlanV2Beam = 48
var PlanV2Exp = 0
var PlanV2Match = true

// PlanV2Count scores the v2 plan with the checkpoint count (status) instead of
// the event prefix (diagnostic cross).
var PlanV2Count = false

// PlanV2Goals scores the v2 plan with the goals-only prefix (diagnostic cross).
var PlanV2Goals = false
var PlanV2NoGoals = false
var p2Debug func(p *planner2, best int32)
var PlanV2BaseDist = false

type p2Child struct {
	key              uint64
	hash             uint64
	from, to, player int32
	base             int32 // base consumed (cell is `to`), -1 for a plain pull
}

// p2Kid is a stored (not yet materialised) child: 16 bytes instead of
// p2Child's 32. The hash is recomputed from the parent (planner2.kid) and
// cells fit int16 (parseLevelRaw refuses larger boards).
type p2Kid struct {
	key                    uint64
	from, to, player, base int16
}

// p2Kids stores every node's children contiguously in chunks that are never
// copied or doubled (exp-583: the appended []p2Child arena was most of the
// planner's memory traffic and up to 2x its peak RSS). A node's children never
// straddle a chunk; off = chunk<<p2KidShift | index in chunk.
type p2Kids struct {
	chunks [][]p2Kid
	used   int // entries used in the last chunk
}

const p2KidShift = 18

// put stores a then b contiguously and returns their offset.
func (k *p2Kids) put(a, b []p2Child) int32 {
	n := len(a) + len(b)
	if n == 0 {
		return 0
	}
	if len(k.chunks) == 0 || k.used+n > len(k.chunks[len(k.chunks)-1]) {
		k.chunks = append(k.chunks, make([]p2Kid, max(n, 1024<<min(len(k.chunks), p2KidShift-10))))
		k.used = 0
	}
	c := k.chunks[len(k.chunks)-1][k.used : k.used+n]
	for i := range a {
		c[i] = p2Kid{a[i].key, int16(a[i].from), int16(a[i].to), int16(a[i].player), int16(a[i].base)}
	}
	for i := range b {
		c[len(a)+i] = p2Kid{b[i].key, int16(b[i].from), int16(b[i].to), int16(b[i].player), int16(b[i].base)}
	}
	off := int32(len(k.chunks)-1)<<p2KidShift | int32(k.used)
	k.used += n
	if k.used >= 1<<p2KidShift {
		k.used = len(k.chunks[len(k.chunks)-1]) // an oversized chunk is full: next put opens a new one
	}
	return off
}

func (k *p2Kids) at(off, i int32) *p2Kid {
	return &k.chunks[off>>p2KidShift][off&(1<<p2KidShift-1)+i]
}

// kidKey is node id's i-th stored child's key.
func (p *planner2) kidKey(id, i int32) uint64 {
	nd := &p.nodes[id]
	return p.kids.at(nd.cOff, i).key
}

// kid materialises node id's i-th stored child, hash included.
func (p *planner2) kid(id, i int32) p2Child {
	nd := &p.nodes[id]
	k := p.kids.at(nd.cOff, i)
	c := p2Child{key: k.key, from: int32(k.from), to: int32(k.to), player: int32(k.player), base: int32(k.base)}
	c.hash = nd.hash ^ p.zb[c.from] ^ p.zp[nd.player] ^ p.zp[c.player]
	if c.base >= 0 {
		c.hash ^= p.zu[c.base]
	} else {
		c.hash ^= p.zb[c.to]
	}
	return c
}

type p2Node struct {
	parent     int32
	bOff       int32 // boxes in boxArena
	nb         int32
	player     int32
	hash       uint64
	key        uint64
	goals      int32 // boxes standing on goals
	dist       int32 // sum of box distances to the nearest goal
	mfrom, mto int32 // move that produced the node from its parent
	mbase      int32
	cOff, cLen int32 // kept children in kids
	cNext      int32
}

type p2Entry struct {
	key  uint64
	seq  uint32
	node int32
	w    int32 // exp-610/612: plan4 path weight (Festival-style plan); always 0 elsewhere
}

type p2Cand struct {
	box, to, player int32 // box index in the node's box list
	pieces          int32
}

type planner2 struct {
	s        *solver
	cut      cutScan
	n        int // boxes
	nw       int // words of a used-base bitset
	baseAt   []int32
	baseCell []int32
	reach    [][]uint64 // per base: cells a box can be pulled from onto it (one box, empty board)
	distance []int32
	zb, zp   []uint64
	zu       []uint64

	nodes     []p2Node
	boxArena  []int32
	usedArena []uint64
	kids      p2Kids
	heap      []p2Entry
	seq       uint32
	seen      []uint64
	mask      uint64
	nseen     int

	mark    []uint32
	stamp   uint32
	queue   []int32
	cands   []p2Cand
	rem     []p2Child
	beam    []p2Child
	demand  []int32
	alive   []uint64
	region0 []int32
	elimFr  []int32 // computeElim4 scratch (exp-638)
	elimBlk []bool
	bseen   []uint32
	bstamp  uint32

	// bipartite matching scratch
	mBox   []int32 // base -> box cell or -1
	mSeen  []uint32
	mStamp uint32
	curBox []int32
	free   []int32 // unused base indices
	skipT  int32
	// matchSkip's per-expansion matching (exp-583)
	expGen, fmGen int
	fmDef, fmU0   int     // deficiency of the maximum matching; its first unmatched box
	fmBox, fmOf   []int32 // base -> box index or -1; box index -> base or -1
	fm1, fmQ      []int32 // matchSkipOK scratch: flipped matching, BFS queue
	fmPar, fmBy   []int32 // per box: base it was reached through; per base: box it was reached from

	deadline time.Time
	maxExp   int
	expanded int
	matchOn  bool
	stats    p2Stats
}

type p2Stats struct{ matchCalls, matchFails, generated, dupes int }

func (p *planner2) stopped() bool {
	return Stop.Load() || NodeBudget == 0 && time.Now().After(p.deadline)
}

func splitmix(x *uint64) uint64 {
	*x += 0x9e3779b97f4a7c15
	z := *x
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}

func (p *planner2) flood(player int32) (int32, uint32) {
	p.stamp++
	st := p.stamp
	q := append(p.queue[:0], player)
	p.mark[player] = st
	norm := player
	for i := 0; i < len(q); i++ {
		norm = min(norm, q[i])
		for _, d := range p.s.dirs {
			x := q[i] + d
			if !p.s.lv.Wall[x] && !p.s.occ[x] && p.mark[x] != st {
				p.mark[x] = st
				q = append(q, x)
			}
		}
	}
	p.queue = q
	return norm, st
}

func (p *planner2) key(rem, regions, goals int, dist int32, rooms int) uint64 {
	if PlanV2NoGoals {
		goals = 0
	}
	inv := int64(1<<24) - 1 - int64(dist)
	if inv < 0 {
		inv = 0
	}
	return uint64(rem)<<52 | uint64(min(regions, 1023))<<42 | uint64(min(goals, 255))<<34 | uint64(min(rooms, 255))<<26 | uint64(inv)
}

// usedBits returns the node's used-base bitset.
func (p *planner2) usedBits(id int32) []uint64 {
	return p.usedArena[int(id)*p.nw : int(id)*p.nw+p.nw]
}

func (p *planner2) boxes(id int32) []int32 {
	n := &p.nodes[id]
	return p.boxArena[n.bOff : n.bOff+n.nb]
}

func (p *planner2) setOcc(id int32) {
	clear(p.s.occ)
	for _, b := range p.boxes(id) {
		p.s.occ[b] = true
	}
}

// seenInsert reports false if h was already present.
func (p *planner2) seenInsert(h uint64) bool {
	if h == 0 {
		h = 1
	}
	if 2*(p.nseen+1) > len(p.seen) { // grow at half load (exp-527; v2 sizes its table to never reach it)
		old := p.seen
		p.seen = make([]uint64, 2*len(old))
		p.mask = uint64(len(p.seen) - 1)
		for _, x := range old {
			if x != 0 {
				j := x & p.mask
				for p.seen[j] != 0 {
					j = (j + 1) & p.mask
				}
				p.seen[j] = x
			}
		}
	}
	i := h & p.mask
	for p.seen[i] != 0 {
		if p.seen[i] == h {
			return false
		}
		i = (i + 1) & p.mask
	}
	p.seen[i] = h
	p.nseen++
	return true
}

func (p *planner2) push(key uint64, node int32) {
	p.seq++
	p.heap = append(p.heap, p2Entry{key, p.seq, node, 0})
	i := len(p.heap) - 1
	for i > 0 {
		par := (i - 1) / 2
		if !p2Less(p.heap[i], p.heap[par]) {
			break
		}
		p.heap[i], p.heap[par] = p.heap[par], p.heap[i]
		i = par
	}
}

func p2Less(a, b p2Entry) bool {
	if a.w != b.w {
		return a.w < b.w
	}
	if a.key != b.key {
		return a.key < b.key
	}
	return a.seq < b.seq
}

func (p *planner2) pop() p2Entry {
	h := p.heap
	top := h[0]
	last := len(h) - 1
	h[0] = h[last]
	h = h[:last]
	p.heap = h
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(h) && p2Less(h[l], h[m]) {
			m = l
		}
		if r < len(h) && p2Less(h[r], h[m]) {
			m = r
		}
		if m == i {
			break
		}
		h[i], h[m] = h[m], h[i]
		i = m
	}
	return top
}

// ---- bipartite matching: can the remaining boxes still reach the unused
// bases (one box per base)? A relaxed (empty board, one box) necessary
// condition; used to pick which base a removal consumes.

func (p *planner2) canReach(base int32, cell int32) bool {
	return p.reach[base][cell>>6]>>(uint(cell)&63)&1 != 0
}

func (p *planner2) augment(u int, seen *uint32) bool {
	cell := p.curBox[u]
	for _, t := range p.free {
		if t == p.skipT || !p.canReach(t, cell) || p.mSeen[t] == *seen {
			continue
		}
		p.mSeen[t] = *seen
		if p.mBox[t] < 0 || p.augment(int(p.mBox[t]), seen) {
			p.mBox[t] = int32(u)
			return true
		}
	}
	return false
}

// matchOK reports whether the boxes except index skipBox can be matched to
// the unused bases except skipBase.
func (p *planner2) matchOK(boxes []int32, skipBox int, skipBase int32) bool {
	p.stats.matchCalls++
	p.curBox = p.curBox[:0]
	for i, b := range boxes {
		if i != skipBox {
			p.curBox = append(p.curBox, b)
		}
	}
	p.skipT = skipBase
	for i := range p.mBox {
		p.mBox[i] = -1
	}
	for u := range p.curBox {
		p.mStamp++
		if !p.augment(u, &p.mStamp) {
			p.stats.matchFails++
			return false
		}
	}
	return true
}

// matchSkip is matchOK(boxes, skipBox, skipBase) for a real box and base
// (exp-583): same answers and stats, from one maximum matching M of all the
// node's boxes per expansion (expGen) instead of a full matching per call.
//   - |M| <= n-2: removing one box leaves a deficiency, so false.
//   - M perfect: skipBox's base is freed; at most one alternating-path search
//     from skipBase's box (Berge).
//   - |M| = n-1 (box u0 unmatched): skipBox must be exposable, i.e. reached
//     from u0 by an even alternating path (else every maximum matching covers
//     it and removing it drops the size below n-1); flipping that path gives
//     a maximum matching M1 perfect on the other boxes, then as above.
func (p *planner2) matchSkip(boxes []int32, skipBox int, skipBase int32) bool {
	if p.fmGen != p.expGen {
		p.fmGen = p.expGen
		p.fmPrep(boxes)
	}
	p.stats.matchCalls++
	if p.matchSkipOK(boxes, skipBox, skipBase) {
		return true
	}
	p.stats.matchFails++
	return false
}

// fmPrep computes the maximum matching of all boxes (Kuhn: a box that cannot
// augment now never can later, so one pass is maximum).
func (p *planner2) fmPrep(boxes []int32) {
	p.curBox = append(p.curBox[:0], boxes...)
	p.skipT = -1
	for i := range p.mBox {
		p.mBox[i] = -1
	}
	p.fmDef, p.fmU0 = 0, -1
	for u := range p.curBox {
		p.mStamp++
		if !p.augment(u, &p.mStamp) {
			p.fmDef++
			if p.fmU0 < 0 {
				p.fmU0 = u
			}
		}
	}
	p.fmBox = append(p.fmBox[:0], p.mBox...)
	p.fmOf = slices.Grow(p.fmOf[:0], len(boxes))[:len(boxes)]
	for i := range p.fmOf {
		p.fmOf[i] = -1
	}
	for t, u := range p.fmBox {
		if u >= 0 {
			p.fmOf[u] = int32(t)
		}
	}
}

func (p *planner2) matchSkipOK(boxes []int32, skipBox int, skipBase int32) bool {
	switch {
	case p.fmDef >= 2:
		return false
	case p.fmDef == 0:
		fb := p.fmOf[skipBox]
		if fb == skipBase || p.fmBox[skipBase] < 0 {
			return true // M minus skipBox's pair already avoids skipBase
		}
		p.mStamp++
		return p.altPath(boxes, p.fmBox, int(p.fmBox[skipBase]), skipBase, fb, p.mStamp)
	}
	m1 := append(p.fm1[:0], p.fmBox...)
	p.fm1 = m1
	if skipBox != p.fmU0 {
		// BFS over alternating paths from u0 (every base met is matched: M
		// is maximum); flip the path to skipBox.
		if len(p.fmPar) < len(boxes) {
			p.fmPar = make([]int32, len(boxes))
		}
		p.mStamp++
		st := p.mStamp
		q := append(p.fmQ[:0], int32(p.fmU0))
		found := false
		for qi := 0; qi < len(q) && !found; qi++ {
			cell := boxes[q[qi]]
			for _, t := range p.free {
				if !p.canReach(t, cell) || p.mSeen[t] == st {
					continue
				}
				p.mSeen[t] = st
				x := p.fmBox[t]
				p.fmPar[x] = t // x is reached through base t (from box fmBox... q[qi])
				p.fmBy[t] = q[qi]
				if int(x) == skipBox {
					found = true
					break
				}
				q = append(q, x)
			}
		}
		p.fmQ = q
		if !found {
			return false
		}
		for x := int32(skipBox); x != int32(p.fmU0); {
			t := p.fmPar[x]
			y := p.fmBy[t]
			m1[t] = y
			x = y
		}
	}
	// m1 leaves skipBox unmatched and is perfect on the other boxes.
	if m1[skipBase] < 0 {
		return true
	}
	p.mStamp++
	return p.altPath(boxes, m1, int(m1[skipBase]), skipBase, -1, p.mStamp)
}

// altPath: is there an alternating path (w.r.t. matching m, base -> box)
// from box u to a base that is free once skipBase's pair (and the base fb,
// freed by the removed box; -1: none) is dropped, avoiding skipBase? Then the
// remaining matching extends to a perfect one (Berge). Read-only.
func (p *planner2) altPath(boxes []int32, m []int32, u int, skipBase, fb int32, seen uint32) bool {
	cell := boxes[u]
	for _, t := range p.free {
		if t == skipBase || !p.canReach(t, cell) || p.mSeen[t] == seen {
			continue
		}
		p.mSeen[t] = seen
		if t == fb || m[t] < 0 || p.altPath(boxes, m, int(m[t]), skipBase, fb, seen) {
			return true
		}
	}
	return false
}

// expand generates the node's children: removals when any box can reach an
// unused base (only those), else the best PlanV2Beam plain pulls.
func (p *planner2) expand(id int32) {
	p.expGen++
	s := p.s
	nd := &p.nodes[id]
	boxes := p.boxes(id)
	p.setOcc(id)
	used := p.usedBits(id)
	cs := &p.cut
	cs.init(s)
	_, parentStamp := p.flood(nd.player)
	pg := cs.newNode()
	for _, c := range p.queue {
		if p.mark[c] == parentStamp {
			cs.pgen[c] = pg
		}
	}
	p.free = p.free[:0]
	for t := range p.baseCell {
		if used[t>>6]>>(uint(t)&63)&1 == 0 {
			p.free = append(p.free, int32(t))
		}
	}
	p.cands = p.cands[:0]
	p.rem = p.rem[:0]
	p.region0 = p.region0[:0]
	// demand[t]: remaining boxes that can still reach base t
	for i := range p.demand {
		p.demand[i] = 0
	}
	for _, t := range p.free {
		for _, b := range boxes {
			if p.canReach(t, b) {
				p.demand[t]++
			}
		}
	}
	// cells from which some unused base is still reachable; a box pulled
	// anywhere else could never return to a start cell
	if len(p.alive) < len(p.reach[0]) {
		p.alive = make([]uint64, len(p.reach[0]))
	}
	clear(p.alive)
	for _, t := range p.free {
		for w, v := range p.reach[t] {
			p.alive[w] |= v
		}
	}
	isFree := func(t int32) bool { return t >= 0 && used[t>>6]>>(uint(t)&63)&1 == 0 }
	var reachBases []int32
	for bi, b := range boxes {
		if bi&7 == 7 && p.stopped() {
			break
		}
		reachBases = reachBases[:0]
		if t := p.baseAt[b]; isFree(t) {
			reachBases = append(reachBases, t)
		}
		cs.begin(b)
		g := cs.gen
		q := cs.q[:0]
		for di, d := range s.dirs {
			if cs.pgen[b+d] == pg {
				k := b*4 + int32(di)
				q = append(q, k)
				cs.seen[k] = g
			}
		}
		s.occ[b] = false
		for qi := 0; qi < len(q); qi++ {
			k := q[qi]
			c, d := k/4, s.dirs[k%4]
			to, player := c+d, c+2*d
			if s.lv.Wall[to] || s.occ[to] || s.lv.Wall[player] || s.occ[player] {
				continue
			}
			np, sides := cs.query(to, player)
			for di := range s.dirs {
				k2 := to*4 + int32(di)
				if sides&(1<<uint(di)) != 0 && cs.seen[k2] != g {
					cs.seen[k2] = g
					q = append(q, k2)
				}
			}
			if t := p.baseAt[to]; isFree(t) {
				reachBases = append(reachBases, t)
				continue
			}
			if p.alive[to>>6]>>(uint(to)&63)&1 == 0 {
				continue
			}
			if !cs.newEnd(to, np) {
				continue
			}
			p.cands = append(p.cands, p2Cand{box: int32(bi), to: to, player: np, pieces: cs.pieces(to)})
		}
		cs.q = q
		if len(reachBases) > 0 {
			// choose the consumed base: feasible matching, then the one the
			// fewest other boxes need
			slices.SortFunc(reachBases, func(a, b int32) int {
				if p.demand[a] != p.demand[b] {
					return int(p.demand[a] - p.demand[b])
				}
				return int(a - b)
			})
			chosen := int32(-1)
			for ti, t := range reachBases {
				if ti >= 64 {
					break
				}
				if !p.matchOn || p.matchSkip(boxes, bi, t) {
					chosen = t
					break
				}
			}
			if chosen >= 0 {
				// the box is gone: the player's region is its component in
				// the board without the box (a pull's path stays inside it)
				np := p.compMinAt(nd.player)
				regions := s.regions() // box b is off the board
				goals := nd.goals
				if s.lv.Goal[b] {
					goals--
				}
				dist := nd.dist - p.distance[b]
				ch := p2Child{from: b, to: p.baseCell[chosen], player: np, base: chosen}
				ch.hash = nd.hash ^ p.zb[b] ^ p.zp[nd.player] ^ p.zp[np] ^ p.zu[chosen]
				ch.key = p.key(int(nd.nb)-1, regions, int(goals), dist, 0)
				p.rem = append(p.rem, ch)
			}
		}
		p.region0 = append(p.region0, int32(s.regions()))
		s.occ[b] = true
	}
	nd = &p.nodes[id]
	if len(p.rem) > 0 {
		slices.SortStableFunc(p.rem, func(a, b p2Child) int { return cmpU64(a.key, b.key) })
		nd.cLen = int32(len(p.rem))
		p.stats.generated += len(p.rem)
		nd.cOff = p.kids.put(p.rem, nil)
		return
	}
	// plain pulls: keep the best PlanV2Beam
	p.beam = p.beam[:0]
	K := PlanV2Beam
	for _, c := range p.cands {
		b := boxes[c.box]
		regions := int(p.region0[c.box]) - 1 + int(c.pieces)
		goals := nd.goals
		if s.lv.Goal[b] {
			goals--
		}
		if s.lv.Goal[c.to] {
			goals++
		}
		dist := nd.dist - p.distance[b] + p.distance[c.to]
		key := p.key(int(nd.nb), regions, int(goals), dist, 0)
		if len(p.beam) >= K && key >= p.beam[len(p.beam)-1].key {
			continue
		}
		ch := p2Child{key: key, from: b, to: c.to, player: c.player, base: -1}
		ch.hash = nd.hash ^ p.zb[b] ^ p.zb[c.to] ^ p.zp[nd.player] ^ p.zp[c.player]
		i := len(p.beam)
		if i < K {
			p.beam = append(p.beam, ch)
		} else {
			i--
		}
		for i > 0 && p.beam[i-1].key > key {
			p.beam[i] = p.beam[i-1]
			i--
		}
		p.beam[i] = ch
	}
	nd.cOff = p.kids.put(p.rem, p.beam) // rem is empty here
	nd.cLen += int32(len(p.beam))
	p.stats.generated += len(p.beam)
}

func cmpU64(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// compMinAt is the smallest cell of the component of v in the current
// occupancy (box at v already removed).
func (p *planner2) compMinAt(v int32) int32 {
	n, _ := p.flood(v)
	return n
}

// addNode materialises child c of parent id; false when its state was seen.
func (p *planner2) addNode(parent int32, c *p2Child) int32 {
	if !p.seenInsert(c.hash) {
		p.stats.dupes++
		return -1
	}
	pn := p.nodes[parent]
	pb := p.boxArena[pn.bOff : pn.bOff+pn.nb]
	off := int32(len(p.boxArena))
	removed := c.base >= 0
	for _, b := range pb {
		if b == c.from {
			if !removed {
				p.boxArena = append(p.boxArena, c.to)
			}
			continue
		}
		p.boxArena = append(p.boxArena, b)
	}
	id := int32(len(p.nodes))
	n := p2Node{parent: parent, bOff: off, nb: pn.nb, player: c.player, hash: c.hash, key: c.key,
		goals: pn.goals, dist: pn.dist, mfrom: c.from, mto: c.to, mbase: c.base}
	if removed {
		n.nb--
	}
	if p.s.lv.Goal[c.from] {
		n.goals--
	}
	n.dist -= p.distance[c.from]
	if !removed {
		if p.s.lv.Goal[c.to] {
			n.goals++
		}
		n.dist += p.distance[c.to]
	}
	p.nodes = append(p.nodes, n)
	pu := p.usedBits(parent)
	p.usedArena = append(p.usedArena, pu...)
	if removed {
		u := p.usedBits(id)
		u[c.base>>6] |= 1 << (uint(c.base) & 63)
	}
	return id
}

func (p *planner2) run() *packingPlan {
	s := p.s
	// roots: the solved board, one per final player component
	clear(s.occ)
	for _, g := range s.goals {
		s.occ[g] = true
	}
	var roots []int32
	covered := make([]bool, len(s.lv.Wall))
	for _, c := range s.fcell {
		if !s.occ[c] && !covered[c] {
			norm, st := p.flood(c)
			roots = append(roots, norm)
			for _, x := range p.queue {
				if p.mark[x] == st {
					covered[x] = true
				}
			}
		}
	}
	if len(roots) == 0 {
		return nil
	}
	var gsum int32
	for _, g := range s.goals {
		gsum += p.distance[g]
	}
	for _, r := range roots {
		h := p.zp[r]
		for _, g := range s.goals {
			h ^= p.zb[g]
		}
		id := int32(len(p.nodes))
		p.boxArena = append(p.boxArena, s.goals...)
		p.nodes = append(p.nodes, p2Node{parent: -1, bOff: int32(len(p.boxArena) - len(s.goals)), nb: int32(len(s.goals)), player: r, hash: h,
			goals: int32(len(s.goals)), dist: gsum, mbase: -1, key: p.key(len(s.goals), 1, len(s.goals), gsum, 0)})
		p.usedArena = append(p.usedArena, make([]uint64, p.nw)...)
		p.seenInsert(h)
		if p.matchOn {
			p.setOcc(id)
		}
	}
	// the matching must exist at the root, else the relaxed check would block
	// every removal: switch it off then.
	if p.matchOn {
		p.free = p.free[:0]
		for t := range p.baseCell {
			p.free = append(p.free, int32(t))
		}
		if !p.matchOK(p.boxes(0), -1, -1) {
			p.matchOn = false
		}
	}
	best := int32(0)
	for id := range p.nodes {
		if p.nodes[id].key < p.nodes[best].key {
			best = int32(id)
		}
		p.expand(int32(id))
		p.expanded++
		if nd := &p.nodes[id]; nd.cLen > 0 {
			p.push(p.kidKey(int32(id), 0), int32(id))
		}
	}
	for !p.stopped() && p.nodes[best].nb > 0 && p.expanded < p.maxExp && len(p.heap) > 0 {
		e := p.pop()
		pn := &p.nodes[e.node]
		c := p.kid(e.node, pn.cNext)
		pn.cNext++
		if pn.cNext < pn.cLen {
			p.push(p.kidKey(e.node, pn.cNext), e.node)
		}
		id := p.addNode(e.node, &c)
		if id < 0 {
			continue
		}
		if p.nodes[id].key < p.nodes[best].key {
			best = id
		}
		if p.nodes[id].nb == 0 {
			best = id
			break
		}
		p.expand(id)
		p.expanded++
		if nd := &p.nodes[id]; nd.cLen > 0 {
			p.push(p.kidKey(id, 0), id)
		}
	}
	if p2Debug != nil {
		p2Debug(p, best)
	}
	return p.result(best)
}

// result turns the path root -> best into packing phases (latest backward
// state first). A removal is a pull onto its base followed by the deletion.
func (p *planner2) result(best int32) *packingPlan {
	var path []int32
	for id := best; id >= 0; id = p.nodes[id].parent {
		path = append(path, id)
	}
	slices.Reverse(path) // root first
	res := &packingPlan{complete: p.nodes[best].nb == 0, expanded: p.expanded, remaining: int(p.nodes[best].nb)}
	cfg := slices.Clone(p.boxes(path[0]))
	var phases []packingPhase // backward order, root first
	snap := func(player int32, mv packingMove) {
		r := slices.Clone(cfg)
		slices.Sort(r)
		phases = append(phases, packingPhase{required: r, player: player, reverseMove: mv})
	}
	snap(p.nodes[path[0]].player, packingMove{})
	for _, id := range path[1:] {
		n := &p.nodes[id]
		i := slices.Index(cfg, n.mfrom)
		if n.mbase < 0 {
			cfg[i] = n.mto
			snap(n.player, packingMove{from: n.mfrom, to: n.mto, player: n.player})
			res.parkings++
			continue
		}
		if n.mfrom != n.mto {
			cfg[i] = n.mto
			snap(n.player, packingMove{from: n.mfrom, to: n.mto, player: n.player})
			res.parkings++
		}
		cfg = slices.Delete(cfg, i, i+1)
		snap(n.player, packingMove{from: n.mto, to: n.mto, player: n.player, remove: true})
	}
	slices.Reverse(phases)
	res.phases = phases
	res.fest = buildFestEvents(res, p.n)
	if PlanV2Goals {
		res.fest = buildGoalsPlan(res, p.s.lv.Goal)
		res.fest.mode = 2
	}
	return res
}

func (s *solver) buildPlanV2(deadline time.Time, maxNodes int) *packingPlan {
	if Plan4 {
		return s.buildPlan4(deadline, maxNodes)
	}
	return s.buildPlanV2Core(deadline, maxNodes)
}

func (s *solver) buildPlanV2Core(deadline time.Time, maxNodes int) *packingPlan {
	size := len(s.lv.Wall)
	work := *s
	work.occ = make([]bool, size)
	work.rmark = nil
	work.rqueue = nil
	work.rstamp = 0
	n := len(s.lv.Boxes)
	p := &planner2{s: &work, n: n, nw: (n + 63) / 64, deadline: deadline, maxExp: maxNodes, matchOn: PlanV2Match,
		baseAt: make([]int32, size), mark: make([]uint32, size), distance: make([]int32, size),
		zb: make([]uint64, size), zp: make([]uint64, size), zu: make([]uint64, n), demand: make([]int32, n),
		mBox: make([]int32, n), mSeen: make([]uint32, n), fmBy: make([]int32, n)}
	if PlanV2Exp > 0 {
		p.maxExp = PlanV2Exp
	}
	seed := uint64(0x5eed)
	for i := range p.zb {
		p.zb[i], p.zp[i] = splitmix(&seed), splitmix(&seed)
	}
	for i := range p.zu {
		p.zu[i] = splitmix(&seed)
	}
	for i := range p.baseAt {
		p.baseAt[i] = -1
	}
	p.baseCell = slices.Clone(s.lv.Boxes)
	slices.Sort(p.baseCell)
	for i, c := range p.baseCell {
		p.baseAt[c] = int32(i)
	}
	// static: cells from which one box can be pulled onto each base
	words := (size + 63) / 64
	p.reach = make([][]uint64, n)
	var q []int32
	for t, bc := range p.baseCell {
		r := make([]uint64, words)
		r[bc>>6] |= 1 << (uint(bc) & 63)
		q = append(q[:0], bc)
		for i := 0; i < len(q); i++ {
			x := q[i]
			for _, d := range s.dirs {
				c := x - d
				if s.lv.Wall[c] || s.lv.Wall[x] || s.lv.Wall[x+d] || r[c>>6]>>(uint(c)&63)&1 != 0 {
					continue
				}
				r[c>>6] |= 1 << (uint(c) & 63)
				q = append(q, c)
			}
		}
		p.reach[t] = r
	}
	for i := range p.distance {
		p.distance[i] = -1
	}
	q = q[:0]
	for _, g := range s.goals {
		p.distance[g] = 0
		q = append(q, g)
	}
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			x := q[i] + d
			if !s.lv.Wall[x] && p.distance[x] < 0 {
				p.distance[x] = p.distance[q[i]] + 1
				q = append(q, x)
			}
		}
	}
	for i := range p.distance {
		p.distance[i] = max(p.distance[i], 0)
	}
	if PlanV2BaseDist {
		// guide boxes toward the bases: score = 255 - pull distance to the
		// nearest base (bigger is better, like the goal distance it replaces)
		for i := range p.distance {
			p.distance[i] = 0
		}
		dd := make([]int32, size)
		for i := range dd {
			dd[i] = -1
		}
		q = q[:0]
		for _, bc := range p.baseCell {
			dd[bc] = 0
			q = append(q, bc)
		}
		for i := 0; i < len(q); i++ {
			x := q[i]
			for _, d := range s.dirs {
				c := x - d
				if s.lv.Wall[c] || s.lv.Wall[x] || s.lv.Wall[x+d] || dd[c] >= 0 {
					continue
				}
				dd[c] = dd[x] + 1
				q = append(q, c)
			}
		}
		for i := range dd {
			if dd[i] >= 0 {
				p.distance[i] = max(255-dd[i], 1)
			}
		}
	}
	cap2 := 1 << bits.Len(uint(max(p.maxExp, 16)*(PlanV2Beam+1)*2))
	p.seen = make([]uint64, cap2)
	p.mask = uint64(cap2 - 1)
	return p.run()
}
