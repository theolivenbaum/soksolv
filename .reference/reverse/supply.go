package reverse

import (
	"math/bits"
	"slices"
)

// Supply-aware reverse lane (exp-707, -rev-supply W; Stage A of exp-706 Design 1).
//
// A reverse state places boxes on start squares. Placing one can cut the only
// pull route of a box toward a start square still free, which the lane's key
// (distance to the nearest free start square) counts as progress until the
// last 1-3 squares turn out unsuppliable. The supply count U of a state is its
// off-start boxes minus a maximum matching "box -> free start square it can be
// pulled to", where the pull reach is exact for one box with the player
// tracked (from the node's player region), the boxes on start squares are
// obstacles and the other off-start boxes are lifted. U > 0: some start square
// cannot be supplied while the placed boxes stay where they are.
//
// U is evaluated when a node is first selected (deferred, so unselected
// children cost nothing). A node with U > 0 goes back into the greedy queue
// with W*8 per unit added to its key, and into the cell of (boxes on start
// squares - U). An expanded node with U = 0 also gets placement macros as
// children: each off-start box pulled, in the real state, along a fewest-pull
// path to the nearest free start square it reaches.
//
// Player tracking without floods: for a fixed obstacle set the free cells form
// a graph, and one DFS (Tarjan's lowpoints) says, for any blocked cell z and
// two cells u, w of its component, whether u and w stay connected without z
// (lab). A box on z with the player in piece l can be pulled to w = z+d iff w
// is in piece l and z+2d is free; the pull reach is a BFS over (box cell,
// player piece) with no flood at all. Per placed set (direct-mapped slots)
// the DFS, the pieces of each cell's neighbours and the reach sets per (box
// cell, player piece) are kept; a child that keeps the placed set changes
// only the pulled box's reach (the player never crosses another box, so its
// piece relative to every other box is the parent's).

// supMark tags a node id pushed back into the queues with its supply penalty.
const supMark = 1 << 30

type set256 [4]uint64

func (a *set256) set(j int32) { a[j>>6] |= 1 << (j & 63) }

// supGraph is the DFS forest of the free cells for one obstacle set: disc is
// the 1-based preorder (0: obstacle or not visited), end one past the
// subtree's last preorder number, low Tarjan's lowpoint, par the tree parent
// (-1 for roots), top the tree's root: the component's smallest cell, so the
// forest (and every piece code) is a function of the obstacle set alone. nl
// caches lab for each cell's four neighbours (-1: not free), valid where
// nlStamp equals epoch.
type supGraph struct {
	key                      set256
	epoch                    uint32
	disc, end, low, par, top []int32
	nl                       []int8
	nlStamp                  []uint32
	n                        int32
	// Pull-state closure (lazy Tarjan SCCs over states y*4+e, e the first
	// neighbour direction of the player's piece): sccOf per state where
	// sccStamp == epoch, sccR the start squares each SCC reaches.
	sccOf, sccStamp []int32
	sccR            []set256
}

// supEntry is a reach-cache entry: fp = fingerprint of (placed set, box
// cell, player piece) | 1, r the start squares reachable.
type supEntry struct {
	fp uint64
	r  set256
}

type supply struct {
	w        int32 // key penalty per unit of U (x8)
	cells    int
	epoch    uint32
	obst     []bool // obstacles of the graph being built
	slots    []supGraph
	scratch  supGraph   // the real state's graph (macros), rebuilt per node
	cache    []supEntry // direct-mapped reach cache
	stk      []int32
	order    []int32 // dfs: the cells of the tree just built
	it       []int8
	tIdx     []int32 // closure: Tarjan index / lowlink per state, valid where tStamp == tst
	tLow     []int32
	tStamp   []uint32
	tst      uint32
	tStk     []int32
	tFr      []int32
	seen     []uint32 // per cell*4+dir: BFS stamp
	prev     []int32  // per cell*4+dir: BFS parent state (macros)
	stamp    uint32
	q        []int32
	target   int32 // macro BFS: the state on the first free start square, -1
	reachOf  []set256
	off      []int32
	left     []int32 // matching: boxes the greedy pass left unmatched
	matchOf  []int32 // per start square: matched box (index into off), -1
	gs       []uint32
	gstamp   uint32
	pathBuf  []int32
	evals    int64
	deferred int64
	misses   int64
	graphs   int64
	macros   int64
	macroOK  int64
}

func newSupply(s *solver, w int32) *supply {
	cells := len(s.lv.Wall)
	nslots := 16
	for nslots < 1024 && 2*nslots*cells*supSlotBytes <= 8<<20 {
		nslots *= 2
	}
	sp := &supply{w: w, cells: cells, obst: make([]bool, cells), slots: make([]supGraph, nslots), cache: make([]supEntry, supCache),
		it: make([]int8, cells), tIdx: make([]int32, 4*cells), tLow: make([]int32, 4*cells), tStamp: make([]uint32, 4*cells), seen: make([]uint32, cells*4), prev: make([]int32, cells*4), matchOf: make([]int32, s.n), gs: make([]uint32, s.n)}
	sp.scratch = sp.newGraph()
	return sp
}

// supSlotBytes is a slot's memory per board cell.
const supSlotBytes = 5*4 + 4 + 4 + 32

// supCache is the reach cache's entry count (40 bytes each).
const supCache = 1 << 19

func (sp *supply) newGraph() supGraph {
	c := sp.cells
	return supGraph{disc: make([]int32, c), end: make([]int32, c), low: make([]int32, c), par: make([]int32, c), top: make([]int32, c),
		nl: make([]int8, 4*c), nlStamp: make([]uint32, c), sccOf: make([]int32, 4*c), sccStamp: make([]int32, 4*c)}
}

func (sp *supply) bytes() uint64 {
	if sp == nil {
		return 0
	}
	used := 1
	for i := range sp.slots {
		if sp.slots[i].disc != nil {
			used++
		}
	}
	return uint64(used)*uint64(sp.cells)*supSlotBytes + uint64(sp.cells)*(1+1+4+4+4)*4 + uint64(len(sp.cache))*40
}

// reset empties g for a new obstacle set.
func (sp *supply) reset(g *supGraph) {
	if g.disc == nil {
		*g = sp.newGraph()
	}
	clear(g.disc)
	g.n = 0
	g.sccR = g.sccR[:0]
	sp.epoch++
	g.epoch = sp.epoch
}

// tree extends g's forest with the component of p (obstacles in sp.obst),
// rooted at its smallest cell.
func (sp *supply) tree(s *solver, g *supGraph, p int32) {
	n0 := g.n
	m := sp.dfs(s, g, p)
	if m == p {
		return
	}
	for _, c := range sp.order {
		g.disc[c] = 0
	}
	g.n = n0
	sp.dfs(s, g, m)
}

// dfs extends g's forest with the component of root (obstacles in sp.obst)
// and returns its smallest cell; sp.order lists the component.
func (sp *supply) dfs(s *solver, g *supGraph, root int32) int32 {
	m := root
	order := append(sp.order[:0], root)
	g.n++
	g.disc[root], g.low[root], g.par[root], g.top[root] = g.n, g.n, -1, root
	sp.it[root] = 0
	stk := append(sp.stk[:0], root)
	for len(stk) > 0 {
		v := stk[len(stk)-1]
		if k := sp.it[v]; k < 4 {
			sp.it[v]++
			w := v + s.dirs[k]
			if sp.obst[w] {
				continue
			}
			if g.disc[w] == 0 {
				g.n++
				g.disc[w], g.low[w], g.par[w], g.top[w] = g.n, g.n, v, root
				sp.it[w] = 0
				stk = append(stk, w)
				order = append(order, w)
				m = min(m, w)
			} else if w != g.par[v] {
				g.low[v] = min(g.low[v], g.disc[w])
			}
			continue
		}
		stk = stk[:len(stk)-1]
		g.end[v] = g.n + 1
		if p := g.par[v]; p >= 0 {
			g.low[p] = min(g.low[p], g.low[v])
		}
	}
	sp.stk, sp.order = stk, order
	return m
}

// lab is the piece code of w (a cell of z's tree, w != z) in the graph
// without z: 0 for the piece above z in the tree, k+1 for the subtree of z's
// child in direction k. Two cells stay connected without z iff their codes
// are equal.
func (g *supGraph) lab(s *solver, z, w int32) int8 {
	dw, dz := g.disc[w], g.disc[z]
	if dw <= dz || dw >= g.end[z] {
		return 0
	}
	for k, d := range s.dirs {
		c := z + d
		if dc := g.disc[c]; dc > dz && dc <= dw && dw < g.end[c] && g.par[c] == z {
			if g.par[z] >= 0 && g.low[c] < dz {
				return 0
			}
			return int8(k + 1)
		}
	}
	return 0
}

// nlab is lab for z's four neighbours (-1: not a cell of the graph).
func (g *supGraph) nlab(s *solver, z int32) []int8 {
	nl := g.nl[z*4 : z*4+4]
	if g.nlStamp[z] != g.epoch {
		g.nlStamp[z] = g.epoch
		for k, d := range s.dirs {
			nl[k] = -1
			if g.disc[z+d] != 0 {
				nl[k] = g.lab(s, z, z+d)
			}
		}
	}
	return nl
}

// graphFor returns the slot for placed set f (sp.obst must hold walls + the
// boxes on f), its forest covering the component of p.
func (s *solver) graphFor(f set256, p int32) *supGraph {
	sp := s.sup
	g := &sp.slots[int(hash(f[:], 0)>>40)&(len(sp.slots)-1)]
	if g.disc == nil || g.key != f {
		sp.reset(g)
		g.key = f
		sp.graphs++
	}
	if g.disc[p] == 0 {
		sp.tree(s, g, p)
	}
	return g
}

// supplyU computes U for the boxes cur (ascending) with the player's
// normalized cell p, and the number of boxes on start squares k.
func (s *solver) supplyU(cur []int32, p int32) (u int32, k int) {
	sp := s.sup
	var f, free set256
	for j := range int32(s.n) {
		free.set(j)
	}
	sp.off = sp.off[:0]
	for _, b := range cur {
		if j := s.startIdx[b]; j >= 0 {
			f.set(j)
			free[j>>6] &^= 1 << (j & 63)
			k++
		} else {
			sp.off = append(sp.off, b)
		}
	}
	if len(sp.off) == 0 {
		return 0, k
	}
	sp.evals++
	hf := hash(f[:], 0)
	g := &sp.slots[int(hf>>40)&(len(sp.slots)-1)]
	if g.disc == nil || g.key != f || g.disc[p] == 0 {
		copy(sp.obst, s.lv.Wall)
		for _, b := range cur {
			if s.startIdx[b] >= 0 {
				sp.obst[b] = true
			}
		}
		g = s.graphFor(f, p)
	}
	sp.reachOf = sp.reachOf[:0]
	for _, b := range sp.off {
		var r set256
		if g.disc[b] != 0 && g.top[b] == g.top[p] { // b in the player's component
			l := g.lab(s, b, p)
			fp := (hf^uint64(b)<<3^uint64(l))*0x9E3779B97F4A7C15 | 1
			fp ^= fp >> 29
			fp |= 1
			e := &sp.cache[fp&(supCache-1)]
			if e.fp == fp {
				r = e.r
			} else {
				sp.misses++
				r = s.closure(g, g.canon(s, b, l))
				e.fp, e.r = fp, r
			}
		}
		for w := range r {
			r[w] &= free[w]
		}
		sp.reachOf = append(sp.reachOf, r)
	}
	for j := range sp.matchOf {
		sp.matchOf[j] = -1
	}
	// Greedy pass (a free square nobody holds yet), then augmenting paths.
	var taken set256
	sp.left = sp.left[:0]
	for i, r := range sp.reachOf {
		w := 0
		for w < 4 && r[w]&^taken[w] == 0 {
			w++
		}
		if w == 4 {
			sp.left = append(sp.left, int32(i))
			continue
		}
		j := int32(w*64 + bits.TrailingZeros64(r[w]&^taken[w]))
		taken.set(j)
		sp.matchOf[j] = int32(i)
	}
	for _, i := range sp.left {
		sp.gstamp++
		if sp.gstamp == 0 {
			clear(sp.gs)
			sp.gstamp = 1
		}
		if !sp.augment(i) {
			u++
		}
	}
	return u, k
}

func (sp *supply) augment(i int32) bool {
	r := sp.reachOf[i]
	for w, x := range r {
		for x != 0 {
			j := int32(w*64 + bits.TrailingZeros64(x))
			x &= x - 1
			if sp.gs[j] == sp.gstamp {
				continue
			}
			sp.gs[j] = sp.gstamp
			if sp.matchOf[j] < 0 || sp.augment(sp.matchOf[j]) {
				sp.matchOf[j] = i
				return true
			}
		}
	}
	return false
}

// supVisit enqueues state (box on y, player in y's piece l) once per piece.
func (s *solver) supVisit(g *supGraph, q []int32, y int32, l int8, from int32, st uint32) []int32 {
	sp := s.sup
	nl := g.nlab(s, y)
	e0 := int32(-1)
	for e := range int32(4) {
		if nl[e] != l {
			continue
		}
		if sp.seen[y*4+e] == st {
			return q
		}
		sp.seen[y*4+e] = st
		if e0 < 0 {
			e0 = e
		}
	}
	if e0 >= 0 {
		sp.prev[y*4+e0] = from
		q = append(q, y*4+e0)
	}
	return q
}

// pullReach is the set of start squares (by index) a box on b can be pulled
// onto, the player in piece l of b's component in g. With stop (macros: g is
// the real state's graph, b not a cell of it, l the player's tree root), it
// ends at the first start square, leaving the BFS parents in sp.prev and that
// state in sp.target.
func (s *solver) pullReach(g *supGraph, b int32, l int32, stop bool) set256 {
	sp := s.sup
	var r set256
	sp.stamp++
	if sp.stamp == 0 {
		clear(sp.seen)
		sp.stamp = 1
	}
	st := sp.stamp
	q := sp.q[:0]
	sp.target = -1
	if stop {
		// b is blocked in g: the player reaches b+d iff b+d is in his tree.
		for e, d := range s.dirs {
			w, w2 := b+d, b+2*d
			if g.disc[w] != 0 && g.top[w] == l && g.disc[w2] != 0 && s.dist[w] >= 0 {
				q = s.supVisit(g, q, w, g.nlab(s, w)[e], -1, st)
			}
		}
	} else {
		q = s.supVisit(g, q, b, int8(l), -1, st)
	}
	for i := 0; i < len(q); i++ {
		y := q[i] >> 2
		nl := g.nlab(s, y)
		l := nl[q[i]&3]
		if j := s.startIdx[y]; j >= 0 && y != b {
			r.set(j)
			if stop {
				sp.target = q[i]
				break
			}
		}
		for e, d := range s.dirs {
			if nl[e] != l {
				continue
			}
			w, w2 := y+d, y+2*d
			if g.disc[w2] == 0 || s.dist[w] < 0 {
				continue
			}
			q = s.supVisit(g, q, w, g.nlab(s, w)[e], q[i], st)
		}
	}
	sp.q = q
	return r
}

// macroPath finds the fewest-pull path of box b to a free start square in the
// real state's graph g (all boxes blocked, b's own cell included: a path that
// would bring the box back over its first cell is not found). It returns the
// pulls as (from cell, direction index) pairs in order, or none.
func (s *solver) macroPath(g *supGraph, b, p int32, out []int32) []int32 {
	sp := s.sup
	out = out[:0]
	s.pullReach(g, b, g.top[p], true)
	if sp.target < 0 {
		return out
	}
	// Walk back: each state's box cell is its parent's box cell + one
	// direction; the first state's parent is b.
	for x := sp.target; x >= 0; x = sp.prev[x] {
		y, from := x>>2, b
		if px := sp.prev[x]; px >= 0 {
			from = px >> 2
		}
		for di, d := range s.dirs {
			if from+d == y {
				out = append(out, from, int32(di))
			}
		}
	}
	for i, j := 0, len(out)-2; i < j; i, j = i+2, j-2 {
		out[i], out[j] = out[j], out[i]
		out[i+1], out[j+1] = out[j+1], out[i+1]
	}
	return out
}

// unexpand clears node id's "expanded" bit (cell scheduling dedupe) so its
// penalized entry can still be expanded.
func (s *solver) unexpand(id int32) {
	if w := int(id >> 6); w < len(s.expanded) {
		s.expanded[w] &^= 1 << (id & 63)
	}
}

// setSupU stores v+1 as node id's supply state (see solver.supU).
func (s *solver) setSupU(id, u int32) {
	if n := s.nodes.Len(); len(s.supU) < n {
		s.supU = slices.Grow(s.supU, n-len(s.supU))[:n]
	}
	s.supU[id] = uint8(u + 1)
}

// laneKey is the lane's ordering key of boxes from scratch (s.occ must hold them).
func (s *solver) laneKey(boxes []int32) int32 {
	switch {
	case s.ln.Match:
		return s.matchCost(boxes)
	case s.ln.Free:
		return s.stateCost(boxes)
	}
	var h int32
	for _, b := range boxes {
		h += s.key8[b]
	}
	return h
}

// placementMacros adds, for an expanded node id with U = 0 (s.occ and s.pkey
// hold cur), one child per off-start box: the box pulled along a fewest-pull
// path to the nearest free start square it reaches in the real state. The
// path's intermediate states are stored as plain node records (not hashed, not
// queued) so that the parent chain stays one pull per node.
func (s *solver) placementMacros(id int32, cur, child []int32) {
	sp := s.sup
	p := s.nodePlayer(id)
	g := &sp.scratch
	copy(sp.obst, s.lv.Wall)
	for _, b := range cur {
		sp.obst[b] = true
	}
	sp.reset(g)
	sp.dfs(s, g, p)
	for _, b := range cur {
		if s.startIdx[b] >= 0 {
			continue
		}
		path := s.macroPath(g, b, p, sp.pathBuf)
		sp.pathBuf = path
		if len(path) < 4 { // none, or a single pull: an ordinary child
			continue
		}
		sp.macros++
		lf, ld := path[len(path)-2], path[len(path)-1]
		t := lf + s.dirs[ld]
		j := 0
		for _, x := range cur {
			if x != b {
				child[j] = x
				j++
			}
		}
		child[j] = t
		slices.Sort(child)
		s.occ[b], s.occ[t] = false, true
		np := s.reach(t+s.dirs[ld], s.mark2)
		copy(s.key, s.pkey)
		fb, ft := s.fidx[b], s.fidx[t]
		s.key[fb>>6] &^= 1 << (fb & 63)
		s.key[ft>>6] |= 1 << (ft & 63)
		if _, old, _ := s.find(s.key, np); old >= 0 {
			s.occ[b], s.occ[t] = true, false
			continue
		}
		h := s.laneKey(child)
		parent := id
		for k := 0; k+2 < len(path); k += 2 {
			from, di := path[k], path[k+1]
			to := from + s.dirs[di]
			copy(s.key, s.pkey)
			s.key[fb>>6] &^= 1 << (fb & 63)
			ff := s.fidx[to]
			s.key[ff>>6] |= 1 << (ff & 63)
			s.nodes.Append(parent, to+s.dirs[di], s.key, s.fidx[from], int8(di))
			parent = int32(s.nodes.Len() - 1)
		}
		copy(s.key, s.pkey)
		s.key[fb>>6] &^= 1 << (fb & 63)
		s.key[ft>>6] |= 1 << (ft & 63)
		if s.addKeyed(child, np, parent, lf, int8(ld), h) >= 0 {
			sp.macroOK++
		}
		s.occ[b], s.occ[t] = true, false
	}
}

// canon is the state id of a box on y with the player in y's piece l: y*4+e
// for the first neighbour direction e in that piece, or -1 (no pull).
func (g *supGraph) canon(s *solver, y int32, l int8) int32 {
	nl := g.nlab(s, y)
	for e := range int32(4) {
		if nl[e] == l {
			return y*4 + e
		}
	}
	return -1
}

// succ is state v's successor by a pull in direction k, or -1.
func (s *solver) succ(g *supGraph, v int32, k int) int32 {
	y := v >> 2
	nl := g.nlab(s, y)
	if nl[k] != nl[v&3] {
		return -1
	}
	d := s.dirs[k]
	w, w2 := y+d, y+2*d
	if g.disc[w2] == 0 || s.dist[w] < 0 {
		return -1
	}
	return g.canon(s, w, g.nlab(s, w)[k])
}

// closure is the set of start squares reachable from pull state root (-1:
// none) in g: Tarjan's SCCs over the states reachable from root, each SCC's
// set the union of its own start squares and its successors' sets. SCCs stay
// in the slot, so later queries on the same placed set reuse them.
func (s *solver) closure(g *supGraph, root int32) set256 {
	if root < 0 {
		return set256{}
	}
	if g.sccStamp[root] == int32(g.epoch) {
		return g.sccR[g.sccOf[root]]
	}
	sp := s.sup
	sp.tst++
	if sp.tst == 0 {
		clear(sp.tStamp)
		sp.tst = 1
	}
	ts, ep := sp.tst, int32(g.epoch)
	counter := int32(0)
	stk, fr := sp.tStk[:0], sp.tFr[:0]
	visit := func(v int32) {
		sp.tStamp[v], sp.tIdx[v], sp.tLow[v] = ts, counter, counter
		counter++
		stk = append(stk, v)
		fr = append(fr, v, 0)
	}
	visit(root)
	for len(fr) > 0 {
		v, k := fr[len(fr)-2], fr[len(fr)-1]
		if k < 4 {
			fr[len(fr)-1]++
			w := s.succ(g, v, int(k))
			switch {
			case w < 0 || g.sccStamp[w] == ep: // no pull, or a finished SCC
			case sp.tStamp[w] != ts:
				visit(w)
			default: // on the stack
				sp.tLow[v] = min(sp.tLow[v], sp.tIdx[w])
			}
			continue
		}
		fr = fr[:len(fr)-2]
		if len(fr) > 0 {
			u := fr[len(fr)-2]
			sp.tLow[u] = min(sp.tLow[u], sp.tLow[v])
		}
		if sp.tLow[v] != sp.tIdx[v] {
			continue
		}
		i := len(stk) - 1
		for stk[i] != v {
			i--
		}
		id := int32(len(g.sccR))
		for _, m := range stk[i:] {
			g.sccOf[m], g.sccStamp[m] = id, ep
		}
		var r set256
		for _, m := range stk[i:] {
			if j := s.startIdx[m>>2]; j >= 0 {
				r.set(j)
			}
			for k := range 4 {
				if w := s.succ(g, m, k); w >= 0 && g.sccOf[w] != id {
					x := g.sccR[g.sccOf[w]]
					r[0], r[1], r[2], r[3] = r[0]|x[0], r[1]|x[1], r[2]|x[2], r[3]|x[3]
				}
			}
		}
		g.sccR = append(g.sccR, r)
		stk = stk[:i]
	}
	sp.tStk, sp.tFr = stk, fr
	return g.sccR[g.sccOf[root]]
}
