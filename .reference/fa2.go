package main

import (
	"flag"
	"slices"
	"soksolv/trace"
	"sync/atomic"
	"time"
)

// exp-491 -strategy fa2: a plan-guided forward FESS engine (research/exp-491/DESIGN.md).
// Our own design for the job Festival's strategy A does: a base plan (the
// exp-411 planner, or an oracle -plan-file) guides a forward FESS whose
// features, cells, advisors and schedule are defined together. It reuses the
// macro move generator, the deadlock tests, the corral restriction and the
// solver's node arrays (so reconstruct works unchanged), and owns everything
// else: children are scored once and stored as edges, duplicates are skipped
// inside the cell turn by one hash probe, the first expansion owns a state.

var Budget12, Tracked12 atomic.Uint64

var (
	FA2SmallFree = true // a node with <= 2 children gets its first child free
	FA2Opener    = true // plan opener advisor (next parking square)
	FA2OOPAdv    = 2    // OOP advisor
	FA2NoReturn  = true // no macro that returns its box to its own square
	FA2VerifyAdv = true // advisor picks pass the apply-time deadlock tests at generation
	FA2OOPTail   = true // OOP zones: tail steps keep the raw zone
	FA2Explorer  = true // explorer advisor (newly reachable push of an unchanged box, reversible move)
	FA2Rooms     = true // room-pair score (else 0)
	FA2GoalHot   = true // goal-free hotspot relation
	FA2Dist      = true // last key term: larger goal-distance sum first
	FA2PlanExp   = 4096 // own planner expansion cap
	FA2Debug     = false
	FA2EdgeWin   = 15    // exp-711 (-fa2-edge-win; promoted)
	FA2Plan      = "v2"  // own planner: v2, v2ng, old
	FA2Corral    = true  // PI-corral restriction
	FA2Lane      = false // portfolio: run fa2 as an extra lane
	FA2Mobility  = false // exp-512: last key = box sides the player reaches (larger first) instead of goal distance
)

func init() {
	flag.BoolVar(&FA2SmallFree, "fa2-smallfree", true, "exp-491 fa2: first child of a node with <= 2 children is free")
	flag.BoolVar(&FA2Opener, "fa2-opener", true, "exp-491 fa2: plan opener advisor")
	flag.IntVar(&FA2OOPAdv, "fa2-oopadv", 2, "exp-491 fa2: OOP advisor: 0 off, 1 OOP decreases, 2 clear a box away from the step zone (push distance)")
	flag.BoolVar(&FA2NoReturn, "fa2-noreturn", true, "exp-491 fa2: drop macros returning the box to its square (player relocation only)")
	flag.BoolVar(&FA2VerifyAdv, "fa2-verifyadv", true, "exp-491 fa2: run apply-time deadlock tests on advisor picks at generation (re-pick if dead)")
	flag.BoolVar(&FA2OOPTail, "fa2-ooptail", true, "exp-491 fa2: OOP zone steps after the last change keep the raw zone")
	flag.BoolVar(&FA2Explorer, "fa2-explorer", true, "exp-491 fa2: explorer advisor")
	flag.BoolVar(&FA2Rooms, "fa2-rooms", true, "exp-491 fa2: room-pair feature")
	flag.BoolVar(&FA2GoalHot, "fa2-goalhot", true, "exp-491 fa2: goal-free hotspots")
	flag.BoolVar(&FA2Dist, "fa2-dist", true, "exp-491 fa2: goal-distance sum (larger first) as the last key term")
	flag.IntVar(&FA2PlanExp, "fa2-plan-exp", 4096, "exp-491 fa2: own planner (exp-411 v2) expansion cap")
	flag.StringVar(&FA2DZ, "fa2-dz", "read", "exp-741 (default read since its promotion, 2026-11-13; share = old path): the fa2 lane's use of the shared corral-verdict table (deadzone.Shared): share (read and write), read (read only: its verdicts stay in its own cache), private (neither). exp-737/741: fa2's verdicts in the shared table changed the forward lanes' pruning and cost SokoStation #198 (pull meets novelty at 4 s with fa2 off or read-only, 0/3 at 30 s otherwise)")
	flag.BoolVar(&FA2Lane, "fa2-lane", true, "exp-491/594 (default on since exp-594, 2026-11-10): run the fa2 plan-guided FESS lane")
	flag.StringVar(&fa2LastKey, "fa2-lastkey", "mobility", "exp-512 fa2: last key term: dist (3.1) or mobility (paper)")
	flag.IntVar(&PlanV2Beam, "plan3-beam", 48, "exp-512: planner plain-pull beam width")
	flag.BoolVar(&FA2Corral, "fa2-corral", true, "exp-491 fa2: PI-corral move restriction")
	flag.BoolVar(&fa2Wide, "fa2-wide", false, "exp-527 fa2: 12-bit packed key field (clamp 4095, not 255); forced on by -plan4")
	flag.StringVar(&FA2Plan, "fa2-plan", "v2", "exp-491 fa2: own planner: v2 (exp-411), v2ng (v2 without the goals key), old (macro sink planner as an event list)")
	flag.BoolVar(&FA2Debug, "fa2-debug", false, "exp-491 fa2: print engine counters to stderr")
	flag.IntVar(&FA2EdgeWin, "fa2-edge-win", FA2EdgeWin, "exp-711 (default 15): fa2 nodes store only their next K+1 children (0: all); consuming the last re-expands the node (same order). Measured: 1.4% of stored children are ever used")
}

// fa2Edge is one scored child of an expanded node, 16 bytes: the child's
// state key is rebuilt from the parent's (box Zobrist ^ moved box ^ player
// region), so it is not stored.
type fa2Edge struct {
	key  uint64 // feature key (smaller is better)
	b, t uint16 // moved box: origin and destination cells
	np   uint16 // the child's normalised player cell
	side uint8  // direction of the last push
	wadv uint8  // bit 0: weight (0 advised, 1 otherwise); bits 1..7: advisors
}

func (e *fa2Edge) w() int32   { return int32(e.wadv & 1) }
func (e *fa2Edge) adv() uint8 { return e.wadv >> 1 }

// hashOf is the state key of node id's child along e.
func (f *fa2) hashOf(id int32, e *fa2Edge) uint64 {
	return f.boxH[id] ^ f.zb[e.b] ^ f.zb[e.t] ^ f.zp[e.np]
}

// idxOf is the index of e's moved box in the parent's sorted boxes.
func idxOf(cur []int32, e *fa2Edge) int {
	i, _ := slices.BinarySearch(cur, int32(e.b))
	return i
}

type fa2Cand struct {
	key                           uint64
	hash                          uint64
	t                             int32
	i                             uint16
	side                          uint8
	w                             uint8
	adv                           uint8
	np                            int32
	b                             int32
	packed, conn, rooms, oop, hot int32
	explorer                      bool
	checked                       bool
}

type fa2Ord struct {
	key uint64
	wi  uint32 // weight << 31 | index in cand
}

type fa2Cell struct {
	key  uint32
	heap []fa2HE
}

type fa2 struct {
	s         *solver
	fp        *festPlan
	score     []uint64 // per node: own key
	best      []uint64 // per node: best-past key
	w         []int32  // per node: accumulated weight
	boxH      []uint64 // per node: Zobrist key of the boxes
	eOff      []int32
	eLen      []int32
	eCur      []int32
	seen      u64set // expanded (or dead) state keys
	cells     []fa2Cell
	cellIdx   map[uint32]int32
	bestCell  int32
	rr        int
	turn      int
	zb, zp    []uint64
	cand      []fa2Cand
	ord       []fa2Ord
	rm        fa2Rooms
	planKind  string
	ed        fa2Arena
	revReach  [][]uint64      // per cell: cells a lone box can be pushed to from it (lazy)
	zdist     map[int][]int32 // per plan step: push distance from the OOP zone (-1: never)
	scratch   []int32
	lateDead2 int64
	dups      int64
	lateDead  int64
	solvedAt  int32
	maxPk     int32    // exp-690: best packed credit any node reached (plan-stream scheduling)
	hall      *fa2Hall // exp-641: Hall credit gate (nil: off)
	impKey    uint64   // exp-711: best (smallest) ring key a child reached
	// exp-711 (-fa2-edge-win K): a node keeps a window of its K+1 next
	// children; the last is a peek (its order key sits in the cell heap).
	// Consuming the peek re-expands the node and refills the window.
	eBase   []int32 // children consumed before the window
	eTot    []int32 // children in all (0: unwindowed)
	rebase  int32   // >= 0 while expand refills a window from this index
	refills int64
	impAt   int64 // exp-711: expansions when impKey last fell
}

const (
	fa2AdvPack = iota
	fa2AdvConn
	fa2AdvRooms
	fa2AdvHot
	fa2AdvOOP
	fa2AdvOpener
	fa2AdvSmall
	fa2AdvExplorer
	fa2NAdv
)

var fa2LastKey = "dist"

// FA2DZ (exp-741, -fa2-dz): share | read | private.
var FA2DZ = "share"

// fa2Wide (exp-527, -fa2-wide; forced by -plan4): packed gets 12 bits
// (clamp 4095 instead of 255) and the distance field 20 bits, so plans with
// more than 255 events keep their prefix distinctions.
var fa2Wide bool

func fa2Key(packed, conn, rooms, oop, hot, dist int32) uint64 {
	c8 := func(x int32) uint64 { return uint64(min(max(x, 0), 255)) }
	if fa2Wide {
		d := uint64(0)
		if FA2Dist {
			d = uint64(0xFFFFF - min(max(dist, 0), 0xFFFFF))
		}
		return (4095-uint64(min(max(packed, 0), 4095)))<<52 | c8(conn)<<44 | c8(rooms)<<36 | c8(oop)<<28 | c8(hot)<<20 | d
	}
	d := uint64(0)
	if FA2Dist {
		d = uint64(0xFFFFFF - min(max(dist, 0), 0xFFFFFF))
	}
	return (255-c8(packed))<<56 | c8(conn)<<48 | c8(rooms)<<40 | c8(oop)<<32 | c8(hot)<<24 | d
}

// fa2Cell projects a key on (packed, connectivity, OOP).
func fa2CellKey(k uint64) uint32 {
	if fa2Wide {
		return uint32(k>>52)<<16 | uint32(k>>44&255)<<8 | uint32(k>>28&255)
	}
	return uint32(k>>56)<<16 | uint32(k>>48&255)<<8 | uint32(k>>32&255)
}

func fa2Fields(k uint64) (packed, conn, rooms, oop, hot int32) {
	if fa2Wide {
		return 4095 - int32(k>>52), int32(k >> 44 & 255), int32(k >> 36 & 255), int32(k >> 28 & 255), int32(k >> 20 & 255)
	}
	return 255 - int32(k>>56), int32(k >> 48 & 255), int32(k >> 40 & 255), int32(k >> 32 & 255), int32(k >> 24 & 255)
}

func (f *fa2) packedNow() int32 {
	if f.fp == nil {
		n := int32(0)
		for _, g := range f.s.goals {
			if f.s.occ[g] {
				n++
			}
		}
		return n
	}
	return int32(f.fp.packed(f.s.occ))
}

func (f *fa2) roomsNow() int32 {
	if !FA2Rooms || f.s.rooms == nil || len(f.s.rooms.rooms) < 2 {
		return 0
	}
	return int32(f.s.festRoomsFull())
}

func (f *fa2) oopNow(boxes []int32, packed int32) int32 {
	if f.fp == nil || f.s.plan == nil {
		return 0
	}
	return int32(f.s.oopFestCount(boxes, -2, int(packed)))
}

// fa2HE is a cell heap entry: a node and the order of its next move
// (accumulated weight, key), copied in so comparisons stay in the heap.
type fa2HE struct {
	w   int32
	id  int32
	key uint64
}

// heLess: accumulated weight of the next move, its key, then node id.
func heLess(a, b fa2HE) bool {
	if a.w != b.w {
		return a.w < b.w
	}
	if a.key != b.key {
		return a.key < b.key
	}
	return a.id < b.id
}

func (f *fa2) push(c int32, id int32) {
	e := f.ed.at(f.eOff[id] + f.eCur[id])
	h := append(f.cells[c].heap, fa2HE{w: f.w[id] + e.w(), id: id, key: e.key})
	i := len(h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !heLess(h[i], h[p]) {
			break
		}
		h[i], h[p] = h[p], h[i]
		i = p
	}
	f.cells[c].heap = h
	if f.bestCell < 0 || f.cells[c].key < f.cells[f.bestCell].key {
		f.bestCell = c
	}
}

func (f *fa2) pop(c int32) int32 {
	h := f.cells[c].heap
	top := h[0].id
	last := len(h) - 1
	h[0] = h[last]
	h = h[:last]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(h) && heLess(h[l], h[m]) {
			m = l
		}
		if r < len(h) && heLess(h[r], h[m]) {
			m = r
		}
		if m == i {
			break
		}
		h[i], h[m] = h[m], h[i]
		i = m
	}
	f.cells[c].heap = h
	return top
}

func (f *fa2) cellFor(key uint64) int32 {
	ck := fa2CellKey(key)
	if c, ok := f.cellIdx[ck]; ok {
		return c
	}
	c := int32(len(f.cells))
	f.cells = append(f.cells, fa2Cell{key: ck})
	f.cellIdx[ck] = c
	return c
}

// pickCell alternates the best non-empty cell (B) and a round robin over
// cells in creation order (R). An empty B falls through to R.
func (f *fa2) pickCell() int32 {
	if f.turn%2 == 0 {
		if f.bestCell >= 0 && len(f.cells[f.bestCell].heap) == 0 {
			f.bestCell = -1
			for c := range f.cells {
				if len(f.cells[c].heap) > 0 && (f.bestCell < 0 || f.cells[c].key < f.cells[f.bestCell].key) {
					f.bestCell = int32(c)
				}
			}
		}
		if f.bestCell >= 0 {
			return f.bestCell
		}
	}
	for k := 0; k < len(f.cells); k++ {
		c := (f.rr + k) % len(f.cells)
		if len(f.cells[c].heap) > 0 {
			f.rr = c + 1
			return int32(c)
		}
	}
	return -1
}

// addNode appends a node to the solver's arrays (reconstruct reads them).
func (f *fa2) addNode(boxes []int32, player, parent, from int32, side int8, adv uint8, key, best uint64, w int32, boxH uint64) int32 {
	s := f.s
	id := int32(len(s.player))
	s.boxes = append(s.boxes, s.pack(boxes)...)
	s.player = append(s.player, player)
	s.parent = append(s.parent, parent)
	s.pushFrom = append(s.pushFrom, from)
	s.pushDir = append(s.pushDir, side)
	s.adv = append(s.adv, adv)
	f.score = append(f.score, key)
	f.best = append(f.best, best)
	f.w = append(f.w, w)
	f.boxH = append(f.boxH, boxH)
	f.eOff = append(f.eOff, 0)
	f.eLen = append(f.eLen, 0)
	f.eCur = append(f.eCur, 0)
	if FA2EdgeWin > 0 {
		f.eBase = append(f.eBase, 0)
		f.eTot = append(f.eTot, 0)
	}
	return id
}

// expand scores every legal macro child of node id (s.occ holds its boxes,
// s.mark stamped st) and stores them as sorted edges. It returns the index
// of a child with every box on a goal, or -1.
func (f *fa2) expand(id int32, cur []int32, st uint32) int {
	s := f.s
	if !s.feasible(cur) {
		return -1
	}
	pPacked, pConn, pRooms, pOOP, pHot := fa2Fields(f.score[id])
	var h int32
	for _, b := range cur {
		h += s.dist[b]
	}
	counts := s.hot.counts(s, cur)
	openSq := int32(-1)
	if FA2Opener && f.fp != nil {
		openSq = f.fp.nextParking(s.occ)
	}
	f.roomsParent()
	if f.hall != nil {
		f.hall.parent(cur, f.packedNow(), s.player[id])
	}
	corral := uint32(0)
	if FA2Corral {
		corral = s.piCorral(st)
	}
	if s.tseen == nil {
		s.tseen = make([]uint32, 4*len(s.lv.Wall))
	}
	if s.bb != nil {
		s.bb.node, s.bb.nodeBuilt, s.bb.parentBuilt = true, false, false
	}
	cand := f.cand[:0]
	solved := -1
	bh := f.boxH[id]
	for i, b := range cur {
		if corral != 0 && s.cmark[b] != corral {
			continue
		}
		s.tst++
		ts := s.tst
		cands := 0
		s.boxSearch(b, st, func(k, t int32) bool {
			canonical := int32(0)
			for side, d := range s.dirs {
				if s.mark3[t-d] == s.pstamp {
					canonical = int32(side)
					break
				}
			}
			if t == b && FA2NoReturn {
				return true
			}
			endpoint := t*4 + canonical
			if s.tseen[endpoint] == ts {
				return true
			}
			s.tseen[endpoint] = ts
			s.occ[t] = true
			defer func() { s.occ[t] = false }()
			if s.pushDead(cur, i, t, k, canonical) != alive {
				return true
			}
			np := s.candNP
			c := fa2Cand{b: b}
			c.t, c.i, c.side = t, uint16(i), uint8(k%4)
			cands++
			switch cands {
			case 1:
				c.conn = int32(s.regionsChild(t))
			case 2:
				s.occ[t] = false
				s.computeArticulation()
				s.occ[t] = true
				fallthrough
			default:
				c.conn = int32(s.childRegions(t))
			}
			c.packed = f.packedNow()
			c.rooms = int32(f.roomsChild(b, t))
			if fa2RoomsCheck && int32(f.roomsChildRef()) != c.rooms {
				panic("fa2: incremental rooms disagree")
			}
			c.hot = int32(s.hot.after(s, cur, counts, i, t))
			cur[i] = t
			c.oop = f.oopNow(cur, c.packed)
			if f.hall != nil {
				cp := f.hall.child(cur, c.packed, i, b, np)
				if FA2Hall {
					c.packed = cp
				}
			}
			cur[i] = b
			if FA2Explorer && c.packed >= pPacked && f.reversible(t, b) {
				c.explorer = f.explores(cur, i, st)
			}
			ch := h - s.dist[b] + s.dist[t]
			last := ch
			if FA2Mobility {
				last = f.mobility(cur, i, t, s.mark3, s.pstamp)
			}
			c.key = fa2Key(c.packed, c.conn, c.rooms, c.oop, c.hot, last)
			c.hash = bh ^ f.zb[b] ^ f.zb[t] ^ f.zp[np]
			c.np = np
			c.w = 1
			if ch == 0 && solved < 0 {
				solved = len(cand)
			}
			cand = append(cand, c)
			return true
		})
	}
	if s.bb != nil {
		s.bb.node = false
	}
	// Advisors: each picks at most one child, the best key it accepts. A
	// pick that fails the apply-time deadlock tests is dropped and the
	// advisors run again (FA2VerifyAdv), so a free move is never a dead one.
	var chosen [fa2NAdv]int
again:
	for k := range chosen {
		chosen[k] = -1
	}
	pick := func(a, k int) {
		if chosen[a] < 0 || cand[k].key < cand[chosen[a]].key {
			chosen[a] = k
		}
	}
	raises := false
	for k := range cand {
		c := &cand[k]
		if c.packed > pPacked {
			pick(fa2AdvPack, k)
			raises = true
		}
		if c.packed < pPacked {
			continue
		}
		if c.conn < pConn {
			pick(fa2AdvConn, k)
		}
		if c.rooms < pRooms {
			pick(fa2AdvRooms, k)
		}
		if c.hot < pHot {
			pick(fa2AdvHot, k)
		}
		if c.explorer {
			pick(fa2AdvExplorer, k)
		}
		if FA2OOPAdv == 1 && pOOP > 0 && c.oop < pOOP && c.conn <= pConn && c.rooms <= pRooms {
			pick(fa2AdvOOP, k)
		}

	}
	if FA2OOPAdv == 2 && pOOP > 0 {
		// Clear a box away from the current step's out-of-plan zone: a
		// reversible move that keeps packed, conn, rooms and OOP and raises
		// the box's push distance from the zone; nearest box first, then the
		// farthest destination.
		zd := f.zoneDist(int(pPacked))
		bestFrom, bestTo := int32(1<<30), int32(-1)
		for k := range cand {
			c := &cand[k]
			if zd == nil || c.packed < pPacked || c.conn > pConn || c.rooms > pRooms || c.oop > pOOP || !f.reversible(c.t, c.b) {
				continue
			}
			d0, d1 := zd[c.b], zd[c.t]
			if d0 < 0 || d1 < 0 || d1 <= d0 || d0 > bestFrom {
				continue
			}
			if d0 < bestFrom || d1 > bestTo {
				bestFrom, bestTo = d0, d1
				chosen[fa2AdvOOP] = k
			}
		}
	}
	if openSq >= 0 && !raises {
		bestD, bestFar := int32(1<<30), int32(-1)
		for k := range cand {
			c := &cand[k]
			if c.packed != pPacked || c.conn > pConn || c.rooms > pRooms || c.b == openSq && c.t == openSq {
				continue
			}
			d0, d1 := s.walk(c.b, openSq), s.walk(c.t, openSq)
			if d0 < 0 || d1 < d0 {
				continue
			}
			if d0 < bestD || d0 == bestD && d1 > bestFar {
				bestD, bestFar = d0, d1
				chosen[fa2AdvOpener] = k
			}
		}
	}
	if FA2SmallFree && len(cand) > 0 && len(cand) <= 2 {
		chosen[fa2AdvSmall] = 0
	}
	if FA2VerifyAdv {
		drop := -1
		for _, k := range chosen {
			if k >= 0 && !cand[k].checked {
				cand[k].checked = true
				if f.deadLate(cur, int(cand[k].i), cand[k].t, cand[k].side, f.scratch) {
					drop = k
					break
				}
			}
		}
		if drop >= 0 {
			f.lateDead2++
			cand = slices.Delete(cand, drop, drop+1)
			if solved > drop {
				solved--
			}
			goto again
		}
	}
	for a, k := range chosen {
		if k >= 0 {
			cand[k].w = 0
			cand[k].adv |= 1 << a
		}
	}
	if solved >= 0 && f.rebase < 0 {
		e := cand[solved].edge()
		f.cand = cand[:0]
		f.eOff[id] = f.ed.begin(1)
		f.ed.add(e)
		f.eLen[id] = 1
		return 0
	}
	// Children in (weight, key, generation index) order: the stable sort
	// by (weight, key), done on 16-byte entries (exp-530).
	ord := f.ord[:0]
	for i := range cand {
		ord = append(ord, fa2Ord{key: cand[i].key, wi: uint32(cand[i].w)<<31 | uint32(i)})
	}
	slices.SortFunc(ord, func(a, b fa2Ord) int {
		if a.wi>>31 != b.wi>>31 {
			return int(a.wi>>31) - int(b.wi>>31)
		}
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return int(a.wi&(1<<31-1)) - int(b.wi&(1<<31-1))
	})
	if len(ord) > fa2Chunk {
		ord = ord[:fa2Chunk] // (never in practice) one node's children fit one chunk
	}
	if FA2EdgeWin > 0 && (f.rebase >= 0 || len(ord) > FA2EdgeWin+1) {
		f.storeWindow(id, ord, cand)
		f.ord = ord[:0]
		f.cand = cand[:0]
		return -1
	}
	f.eOff[id] = f.ed.begin(len(ord))
	for _, o := range ord {
		f.ed.add(cand[o.wi&(1<<31-1)].edge())
	}
	f.ord = ord[:0]
	cand = cand[:len(ord)]
	f.eLen[id] = int32(len(cand))
	f.cand = cand[:0]
	return -1
}

func (f *fa2) bytes() uint64 {
	s := f.s
	return f.ed.bytes() + uint64(cap(s.boxes))*8 + uint64(len(s.player))*(4*4+1+8*3+4*4) + uint64(len(f.seen.slots))*8 + uint64(len(f.eTot))*8
}

// storeWindow (exp-711) stores children ord[base:base+K+1] of node id: base
// 0 in a new window, f.rebase in place when refilling (same slots).
func (f *fa2) storeWindow(id int32, ord []fa2Ord, cand []fa2Cand) {
	base := max(f.rebase, 0)
	n := min(int32(len(ord))-base, int32(FA2EdgeWin+1))
	if n < 0 {
		n = 0
	}
	if f.rebase < 0 {
		f.eOff[id] = f.ed.begin(int(n))
		for _, o := range ord[base : base+n] {
			f.ed.add(cand[o.wi&(1<<31-1)].edge())
		}
	} else {
		n = min(n, f.eLen[id]) // never past the window's slots
		for j, o := range ord[base : base+n] {
			*f.ed.at(f.eOff[id] + int32(j)) = cand[o.wi&(1<<31-1)].edge()
		}
	}
	f.eBase[id], f.eTot[id], f.eLen[id], f.eCur[id] = base, int32(len(ord)), n, 0
}

// refill (exp-711) re-expands node id when its window's peek child is next
// (the expansion is a function of the node, so the order is the same).
func (f *fa2) refill(id int32, cur []int32) {
	s := f.s
	s.unpack(id, cur)
	for _, b := range cur {
		s.occ[b] = true
	}
	s.reach(s.player[id], s.mark)
	f.rebase = f.eBase[id] + f.eLen[id] - 1
	f.expand(id, cur, s.stamp)
	f.rebase = -1
	f.refills++
	clear(s.occ)
}

// fa2Solve runs the lane on a fresh forward solver.
func fa2Solve(s *solver, deadline time.Time, memLimit uint64) Result {
	return fa2Run(s, deadline, memLimit, FA2Plan, nil, 0)
}

// fa2Run is one engine. With a baton (two-plan mode) it shares the lane with
// a second engine, alternating 1024-expansion slices; each gets half the
// node budget and half the memory.
func fa2Run(s *solver, deadline time.Time, memLimit uint64, planKind string, bat *baton, me int) (out Result) {
	if bat != nil {
		bat.wait(me)
		defer func() { bat.done(me, out.Solved) }()
	}
	tStart, tSearch := time.Now(), time.Time{}
	if s.dz != nil && s.fa2Lane { // exp-741 (-fa2-dz): the anytime lane's use of the shared corral-verdict table
		s.dz.ReadOnly = FA2DZ == "read"
		s.dz.Private = FA2DZ == "private"
	}
	s.deadline = deadline
	s.memLimit = memLimit
	if fa2LastKey == "mobility" {
		FA2Mobility = true
	}
	if FA2GoalHot {
		s.hot.goalFree = true
	}
	s.hotSides = HotSides // exp-694: fa2 engines only; the macro and fess lanes keep the player-blind relation
	if s.fa2Preset != nil || s.fa2Lane {
		s.oopTail = FA2OOPTail // exp-549: embedded in the macro lane (exp-594: or the anytime lane): no process-wide switch
	} else {
		OOPTailFix = FA2OOPTail
	}
	if Plan4 {
		fa2Wide = true
	}
	if len(s.lv.Wall) > 1<<16 {
		return Result{Reason: "fa2: board too large for 16-bit cells"}
	}
	f := &fa2{s: s, cellIdx: map[uint32]int32{}, bestCell: -1, solvedAt: -1, impKey: ^uint64(0), rebase: -1, scratch: make([]int32, s.n), planKind: planKind}
	memDiv := uint64(1)
	if bat != nil {
		memDiv = 2
	}
	// The plan (with bases).
	if s.fa2Preset != nil {
		cp := *s.fa2Preset // exp-549: the macro lane's plan4 event list (private copy)
		cp.e = slices.Clone(cp.e)
		cp.festTo = nil
		f.fp = &cp
	} else if PlanFile != "" {
		fp, err := loadPlanFile(PlanFile, s.lv)
		if err != nil {
			return Result{Reason: "plan-file: " + err.Error()}
		}
		f.fp = fp
	} else {
		budget := min(time.Second, time.Until(deadline)/10)
		if Plan4 {
			budget = time.Duration(float64(time.Until(deadline)) * Plan4Frac)
		}
		if NodeBudget > 0 {
			budget = time.Hour
		}
		if s.planCap > 0 && budget > s.planCap {
			budget = s.planCap // exp-645b
		}
		if Plan4Replay != "" {
			return plan4ReplayRun(s)
		}
		if Plan4FestAll != "" {
			return plan4FestAllRun(s)
		}
		if Plan4Cells != "" {
			return plan4CellsRun(s)
		}
		var pl *packingPlan
		switch f.planKind {
		case "old": // the macro lane's sink planner, as a Festival event list
			if old := s.buildPackingPlan(time.Now().Add(budget), FA2PlanExp); old != nil {
				old.fest = buildFestEvents(old, s.n)
				pl = old
			}
		case "v2ng": // v2 without the boxes-on-goals key term
			PlanV2NoGoals = true
			pl = s.buildPlanV2(time.Now().Add(budget), FA2PlanExp)
			PlanV2NoGoals = false
		case "fest": // exp-615: Festival-style plan4, planned in this engine's own slices
			pl = s.fa2FestPlan(deadline, bat, me)
			if pl == nil {
				return Result{Reason: "fa2: festival plan incomplete"}
			}
			if sh := s.fa2Peer; sh != nil && pl.fest != nil {
				sh.fest.Store(pl.fest)
				if FA2FestDedup && samePlan(pl.fest, sh.p4.Load()) {
					return Result{Reason: "fa2: festival plan duplicate"} // exp-618
				}
			}
		case "p4own": // exp-618 (-fa2-lane-solo): engine 1's default plan4 in its own slices
			pl = s.fa2OwnPlan4(deadline, bat, me, false)
			if pl == nil {
				return Result{Reason: "fa2: plan4 incomplete"}
			}
			if sh := s.fa2Peer; sh != nil && pl.fest != nil {
				sh.p4.Store(pl.fest)
			}
		default:
			if src, ok := fa2StreamSource(f.planKind); ok {
				pl = s.fa2StreamPlan(src, deadline, bat, me)
				if pl == nil {
					return Result{Reason: "fa2: stream plan " + src.name + " incomplete or duplicate"}
				}
			} else {
				pl = s.buildPlanV2(time.Now().Add(budget), FA2PlanExp)
			}
		}
		if pl != nil && pl.fest != nil && len(pl.fest.e) > 0 {
			f.fp = pl.fest
			s.plan = pl
			s.stats.PlanPhases = len(pl.phases)
			s.stats.PlanComplete = pl.complete
			s.stats.PlanExpanded = pl.expanded
		}
	}
	if f.fp != nil && FA2Debug {
		for _, e := range f.fp.e {
			print(" ", e.from, "->", e.to, "/", e.until)
		}
		println(" plan init", f.fp.initNum)
	}
	if f.fp != nil {
		f.fp.mode = 1
		if f.fp.festTo == nil {
			f.fp.festTo = make([]bool, len(s.lv.Wall))
			for _, e := range f.fp.e {
				if e.to >= 0 && int(e.to) < len(f.fp.festTo) {
					f.fp.festTo[e.to] = true
				}
			}
		}
		if s.plan == nil {
			s.plan = &packingPlan{}
		}
		s.plan.fest = f.fp
	}
	if f.fp != nil && (FA2Hall || FA2HallDiag) {
		f.hall = newFA2Hall(s, f.fp)
	}
	f.roomsSetup()
	s.pairOffsets()
	s.buildWalk()
	s.dpair = make([]int8, len(s.lv.Wall)*len(s.poff))
	seed := uint64(0x491fa2)
	f.zb, f.zp = make([]uint64, len(s.lv.Wall)), make([]uint64, len(s.lv.Wall))
	for i := range f.zb {
		f.zb[i], f.zp[i] = splitmix(&seed), splitmix(&seed)
	}
	if FA2Replay != "" {
		return fa2ReplayRun(f)
	}
	n := s.n
	cur, child := make([]int32, n), make([]int32, n)
	copy(cur, s.lv.Boxes)
	slices.Sort(cur)
	var bh uint64
	allGoal := true
	for _, b := range cur {
		if s.dist[b] < 0 {
			return Result{Reason: "box on dead square"}
		}
		bh ^= f.zb[b]
		s.occ[b] = true
		allGoal = allGoal && s.lv.Goal[b]
	}
	if allGoal {
		clear(s.occ)
		return Result{Solved: true}
	}
	np := s.reach(s.lv.Player, s.mark)
	st := s.stamp
	var h int32
	for _, b := range cur {
		h += s.dist[b]
	}
	pk := f.packedNow()
	rootOOP := f.oopNow(cur, pk)
	if f.hall != nil {
		cp := f.hall.child(cur, pk, -1, -1, np)
		if FA2Hall {
			pk = cp
		}
	}
	rootLast := h
	if FA2Mobility {
		rootLast = f.mobility(cur, -1, 0, s.mark, st)
	}
	rootKey := fa2Key(pk, int32(s.regions()), f.roomsNow(), rootOOP, int32(hotspotCount(s.hot.counts(s, cur))), rootLast)
	root := f.addNode(cur, np, -1, -1, -1, 0, rootKey, rootKey, 0, bh)
	f.seen.insert(bh ^ f.zp[np])
	res := func(r Result) Result {
		clear(s.occ)
		if s.elasticHold > 0 { // exp-710: return the borrowed bytes
			releaseElastic(s.laneID, s.elasticHold)
			s.elasticHold = 0
		}
		if FA2Debug {
			bk := f.best[0]
			for _, k := range f.best {
				bk = min(bk, k)
			}
			pk, cn, rm, oo, ht := fa2Fields(bk)
			pe := 0
			if f.fp != nil {
				pe = len(f.fp.e)
			}
			println("fa2: plan_ms", tSearch.Sub(tStart).Milliseconds(), "search_ms", time.Since(tSearch).Milliseconds())
			if hl := f.hall; hl != nil {
				println("fa2: hall off", hl.off, "nodes", hl.nodes, "incons", hl.incons, "defsum", hl.defSum, "gated", hl.gated, "skipped", hl.skipped, "levels", hl.levels, "of", len(hl.lv), "dead0", hl.dead0)
			}
			println("fa2: engine", f.planKind, "me", me, "imp_at", f.impAt, "imp_key", f.impKey)
			println("fa2: refills", f.refills, "edge bytes", f.ed.bytes(), "tracked", f.bytes())
			println("fa2: nodes", s.expandedN, "edges", f.ed.n, "cells", len(f.cells), "dups", f.dups, "lateDead", f.lateDead, "advDead", f.lateDead2, "plan", pe, "init", f.fp.initNumOr0(), "best packed/conn/rooms/oop/hot", pk, cn, rm, oo, ht)
		}
		r.Nodes = int(s.expandedN)
		r.Stats = s.finalStats()
		s.tr.Finish(trace.Ended(r.Solved, r.Reason), int(s.expandedN), len(s.player), f.bytes())
		return r
	}
	solvedChild := func(id int32, cur []int32) Result {
		e := *f.ed.at(f.eOff[id])
		b := int32(e.b)
		copy(child, cur)
		child[idxOf(cur, &e)] = int32(e.t)
		slices.Sort(child)
		clear(s.occ)
		cid := f.addNode(child, int32(e.t), id, b, int8(e.side), e.adv(), e.key, min(f.best[id], e.key), f.w[id]+e.w(), 0)
		s.expandedN++
		sol := s.reconstruct(cid)
		r := Result{Solved: sol != "", Solution: sol}
		if sol == "" {
			r.Reason = "reconstruct failed"
		}
		return res(r)
	}
	tSearch = time.Now()
	s.stats.PlanMs = tSearch.Sub(tStart).Milliseconds()
	if f.expand(root, cur, st) == 0 {
		return solvedChild(root, cur)
	}
	if f.eLen[root] > 0 {
		f.push(f.cellFor(f.best[root]), root)
	}
	for _, b := range cur {
		s.occ[b] = false
	}
	for {
		if s.expired() {
			return res(Result{Reason: "timeout"})
		}
		if s.expandedN&1023 == 0 {
			tb := f.bytes()
			if bat != nil && bat.ring {
				// exp-711: one pool for the lane (it borrows elastic bytes itself); exp-710's 76% mapped backstop still applies
				if (ElasticPoolPct > 0 && ElasticMem && memLimit > 0 && mappedOver(memLimit, 76)) || bat.ringOver(me, tb, s.expandedN-f.impAt, s.budgetP.Load()) {
					return res(Result{Reason: "memory"})
				}
			} else {
				s.trackedP.Store(tb)
				if bat != nil && bat.dynMem {
					memDiv = bat.live() // exp-594: the live engines split the lane's share
				}
				lim := s.budgetP.Load() / memDiv
				if ElasticPoolPct > 0 && ElasticMem && memLimit > 0 {
					// exp-710: borrow from the elastic pool above the lane share; the 76% mapped backstop replaces the 70% hard cap.
					if mappedOver(memLimit, 76) || (lim > 0 && !s.fa2Borrow(tb, lim)) {
						return res(Result{Reason: "memory"})
					}
				} else if (lim > 0 && tb > lim) || (memLimit > 0 && tb > memLimit/100*70/memDiv) {
					return res(Result{Reason: "memory"})
				}
			}
		}
		c := f.pickCell()
		if c < 0 {
			return res(Result{Reason: "exhausted"})
		}
		id := f.pop(c)
		if FA2EdgeWin > 0 && f.eCur[id] == f.eLen[id]-1 && f.eBase[id]+f.eLen[id] < f.eTot[id] {
			f.refill(id, cur)
		}
		e := *f.ed.at(f.eOff[id] + f.eCur[id])
		f.eCur[id]++
		if f.eCur[id] < f.eLen[id] {
			f.push(c, id)
		}
		eh := f.hashOf(id, &e)
		if f.seen.has(eh) {
			f.dups++
			continue // a move into an expanded state: no expansion, no turn
		}
		s.unpack(id, cur)
		for _, b := range cur {
			s.occ[b] = true
		}
		b, et := int32(e.b), int32(e.t)
		copy(child, cur)
		child[idxOf(cur, &e)] = et
		slices.Sort(child)
		s.occ[b] = false
		s.occ[et] = true
		side := s.playerAfter(et, s.dirs[e.side])
		np := s.reach(side, s.mark)
		st := s.stamp
		dead := false
		if LazyCluster && s.cl != nil && s.cl.Dead(s.occ, et, side, func(y int32) bool { return s.mark[y] == st }) {
			dead = true
		} else if s.dz != nil && s.dz.DeadAfter(s.occ, child, side, et) {
			dead = true
		}
		if dead {
			f.lateDead++
			f.seen.insert(eh)
			clear(s.occ)
			continue
		}
		if s.stamp != st {
			s.reach(side, s.mark)
			st = s.stamp
		}
		s.expandedN++
		f.turn++
		if bat != nil && s.expandedN&1023 == 0 {
			if me >= batonOld {
				bat.report(me, f.maxPk)
			}
			if !bat.yield(me) {
				clear(s.occ)
				return res(Result{Reason: "timeout"})
			}
		}
		cbh := f.boxH[id] ^ f.zb[b] ^ f.zb[et]
		if checkRegions && cbh^f.zp[np] != eh {
			panic("fa2: child key disagrees with its edge")
		}
		f.seen.insert(eh)
		cid := f.addNode(child, np, id, b, int8(e.side), e.adv(), e.key, min(f.best[id], e.key), f.w[id]+e.w(), cbh)
		if (ReviveRoot || RelayFA2) && s.relayPoster() {
			s.relayNote(cid, child) // exp-744 (exp-767 -relay-fa2)
		}
		if me >= batonOld {
			f.maxPk = max(f.maxPk, 4095-int32(f.best[cid]>>52))
		}
		if k := uint64(fa2CellKey(e.key)); k < f.impKey {
			if FA2Debug && fa2CellKey(e.key)>>16 != uint32(f.impKey>>16) {
				println("fa2: imp", f.planKind, me, s.expandedN, 4095-int32(e.key>>52))
			}
			f.impKey, f.impAt = k, s.expandedN // exp-711: the ring's staleness clock
		}
		if r := f.expand(cid, child, st); r == 0 {
			return solvedChild(cid, child)
		}
		if f.eLen[cid] > 0 {
			f.push(f.cellFor(f.best[cid]), cid)
		}
		clear(s.occ)
	}
}

// nextParking is Festival's next_parking_place semantics (exp-483): the first
// plan entry still missing by the backward prefix scan, or -1 when a
// permanent initial entry is missing or the plan is complete.
func (f *festPlan) nextParking(occ []bool) int32 {
	n := len(f.e)
	for i := 0; i < f.initNum; i++ {
		if f.e[i].until == -1 && !occ[f.e[i].to] {
			return -1
		}
	}
	packed := n
	for i := n - 1; i >= f.initNum; i-- {
		if occ[f.e[i].to] {
			continue
		}
		if f.e[i].until == -1 || f.e[i].until >= packed {
			packed = i
		}
	}
	if packed >= n {
		return -1
	}
	return f.e[packed].to
}

func (f *festPlan) initNumOr0() int {
	if f == nil {
		return 0
	}
	return f.initNum
}

// reversible reports whether a lone box on an empty board can be pushed from
// t back to b (Festival's explorer rejects irreversible moves).
func (f *fa2) reversible(t, b int32) bool {
	s := f.s
	if t == b {
		return true
	}
	if f.revReach == nil {
		f.revReach = make([][]uint64, len(s.lv.Wall))
	}
	r := f.revReach[t]
	if r == nil {
		r = make([]uint64, (len(s.lv.Wall)+63)/64)
		r[t>>6] |= 1 << (uint(t) & 63)
		q := []int32{t}
		for i := 0; i < len(q); i++ {
			x := q[i]
			for _, d := range s.dirs {
				y := x + d
				if s.lv.Wall[y] || s.lv.Wall[x-d] || r[y>>6]>>(uint(y)&63)&1 != 0 {
					continue
				}
				r[y>>6] |= 1 << (uint(y) & 63)
				q = append(q, y)
			}
		}
		f.revReach[t] = r
	}
	return r[b>>6]>>(uint(b)&63)&1 != 0
}

// explores reports whether the child (s.occ; s.mark3 == s.pstamp is its
// player region, s.mark == st the parent's) lets the player stand, for the
// first time, beside an unchanged box with that push free.
func (f *fa2) explores(cur []int32, moved int, st uint32) bool {
	s := f.s
	b := cur[moved]
	wall, occ, mark, mark3, ps := s.lv.Wall, s.occ, s.mark, s.mark3, s.pstamp
	for j, x := range cur {
		if j == moved {
			continue
		}
		for _, d := range s.dirs {
			sq := x + d
			if mark3[sq] != ps || sq == b || mark[sq] == st {
				continue
			}
			if dst := x - d; !wall[dst] && !occ[dst] {
				return true
			}
		}
	}
	return false
}

// lateDead runs the apply-time deadlock tests (lazy cluster, corral) on the
// child of moving cur[i] to t through the last push side. s.occ holds the
// parent; it is restored. s.mark is left alone (mark2 is used).
func (f *fa2) deadLate(cur []int32, i int, t int32, side uint8, child []int32) bool {
	s := f.s
	b := cur[i]
	copy(child, cur)
	child[i] = t
	slices.Sort(child)
	s.occ[b] = false
	s.occ[t] = true
	defer func() {
		s.occ[t] = false
		s.occ[b] = true
	}()
	ps := s.playerAfter(t, s.dirs[side])
	if LazyCluster && s.cl != nil {
		s.reach(ps, s.mark2)
		st := s.stamp
		if s.cl.Dead(s.occ, t, ps, func(y int32) bool { return s.mark2[y] == st }) {
			return true
		}
	}
	return s.dz != nil && s.dz.DeadAfter(s.occ, child, ps, t)
}

// zoneDist is the push distance of a lone box from plan step k's OOP zone,
// on the board whose walls are the walls plus the step's parked targets.
func (f *fa2) zoneDist(k int) []int32 {
	s := f.s
	if f.fp == nil || s.oopz == nil || s.oopz.n == 0 || k >= s.oopz.n-1 {
		return nil
	}
	if d, ok := f.zdist[k]; ok {
		return d
	}
	if f.zdist == nil {
		f.zdist = map[int][]int32{}
	}
	z := s.oopz
	row := z.z[k*z.w : (k+1)*z.w]
	n := len(s.lv.Wall)
	blocked := slices.Clone(s.lv.Wall)
	for j := 0; j <= k && j < len(f.fp.e); j++ {
		if e := f.fp.e[j]; e.until == -1 && e.to >= 0 {
			blocked[e.to] = true
		}
	}
	d := make([]int32, n)
	var q []int32
	for c := range d {
		d[c] = -1
		if row[c>>6]>>(uint(c)&63)&1 != 0 {
			d[c] = 0
			q = append(q, int32(c))
		}
	}
	for i := 0; i < len(q); i++ {
		x := q[i]
		for _, dd := range s.dirs {
			y := x + dd
			if blocked[y] || blocked[x-dd] || d[y] >= 0 {
				continue
			}
			d[y] = d[x] + 1
			q = append(q, y)
		}
	}
	f.zdist[k] = d
	return d
}

func (c *fa2Cand) edge() fa2Edge {
	return fa2Edge{key: c.key, b: uint16(c.b), t: uint16(c.t), np: uint16(c.np), side: c.side, wadv: c.w&1 | c.adv<<1}
}

// fa2Arena stores edges in chunks of fa2Chunk, so growth never copies (or
// leaves garbage for) the edges stored so far. A node's edges are contiguous
// inside one chunk; index = chunk<<fa2Shift | offset.
const fa2Shift = 20
const fa2Chunk = 1 << fa2Shift

type fa2Arena struct {
	ch [][]fa2Edge
	n  int
}

// begin returns the index the next need edges will get, opening a chunk if
// the current one cannot hold them all.
func (a *fa2Arena) begin(need int) int32 {
	if k := len(a.ch); k == 0 || len(a.ch[k-1])+need > fa2Chunk {
		c := 1024
		if k > 0 {
			c = fa2Chunk
		}
		a.ch = append(a.ch, make([]fa2Edge, 0, max(c, need)))
	}
	k := len(a.ch) - 1
	return int32(k<<fa2Shift | len(a.ch[k]))
}

func (a *fa2Arena) add(e fa2Edge) {
	k := len(a.ch) - 1
	a.ch[k] = append(a.ch[k], e)
	a.n++
}

func (a *fa2Arena) at(i int32) *fa2Edge { return &a.ch[i>>fa2Shift][i&(fa2Chunk-1)] }

func (a *fa2Arena) bytes() uint64 {
	n := 0
	for _, c := range a.ch {
		n += cap(c)
	}
	return uint64(n) * 16
}

// mobility counts the box sides the player can stand on (mk == stamp marks
// the player's region), with box `moved` standing on t (moved < 0: none moved).
func (f *fa2) mobility(cur []int32, moved int, t int32, mk []uint32, stamp uint32) int32 {
	s := f.s
	n := int32(0)
	for i, b := range cur {
		if i == moved {
			b = t
		}
		for _, d := range s.dirs {
			if mk[s.front(b, d)] == stamp {
				n++
			}
		}
	}
	return n
}

// fa2Borrow (exp-710) keeps the engine's elastic hold at tb-lim (acquiring or
// releasing the difference) and reports whether tb fits the share plus pool.
func (s *solver) fa2Borrow(tb, lim uint64) bool {
	var need uint64
	if tb > lim {
		need = tb - lim
	}
	if need > s.elasticHold {
		if mappedOver(s.memLimit, 75) || !acquireElastic(s.laneID, need-s.elasticHold) {
			return false
		}
	} else if need < s.elasticHold {
		releaseElastic(s.laneID, s.elasticHold-need)
	}
	s.elasticHold = need
	return true
}
