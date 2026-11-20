package main

import (
	"os"
	"slices"
	"strings"
	"time"

	"soksolv/meet"
)

// exp-453: the hub generator (-hub-back G). A backward best-first search over
// single pulls whose keys never refer to the start: player regions, boxes
// that cannot be pulled ("trapped"), free cells on no cycle (biconnectivity),
// then the summed goal distance of the boxes (larger is looser) and the total
// pull mobility. Festival's order (docs/FESTIVAL.md section 1) is the model, the
// implementation is ours and cheap: a child's key comes from the parent's
// exact region and cycle counts plus an O(1) local update of trapped / mobility
// / distance, so no matching, no per-child flood. A node's own regions and
// cycle count are computed once, when it is created (lazily, when its entry
// reaches the top of the heap). Only the best genTopK children of an expanded
// node are kept (a generator, not a prover).
//
// The one start-referring thing is a hard filter: a box never goes on a cell
// from which no start square can be reached (ps.dist < 0), so every hub is
// at least cell-feasible. It is not a key.

var (
	hubGenTopK    = 16   // children kept per expansion
	hubGenBic     = true // the biconnectivity key (Festival's bicon)
	hubGenKey     = "fm" // order of the last two key terms: far then mobility (fm), or mobility then far (mf)
	hubGenInit    = 0.15 // fraction of the hub phase spent generating before the first nomination
	hubGenMacro   = 48   // pull-walk states explored per box (1: single pulls)
	hubGenCells   = true // alternate best-first with the least-expanded (regions, trapped) cell
	hubGenPockets = 4    // also admit hubs with up to this many floor regions (the player's plus sealed pockets); 1: strict one-region hubs only
	hubGenOnly    bool   // diagnostics: run the generator alone, print the best hubs, return
)

const hubPocketMax = 60 // sealed pocket cells a hub may have (Festival's #49 hub: 3 small pockets)

const genExpand = ^uint32(0) // heap entry kind: expand node id

type genNode struct {
	parent                      int32
	mv                          uint32 // box index<<2 | dir: the box moved from (its cell now) - dirs[dir]
	player                      int32  // canonical: smallest reachable cell
	reg, trap, mob, bic, far, g int32  // regions, boxes with no pullable side, total pull mobility, off-cycle cells, summed goal distance, boxes on goals
	depth                       int32
	hash                        uint64 // zobrist of the box set
}

type genEnt struct {
	key uint64
	id  int32
	mv  uint32
}

type hubGen struct {
	ps       *solver
	n        int
	nodes    []genNode
	boxes    []uint16 // n per node, slot-consistent with the parent
	seen     map[uint64]int32
	cells    []genCell
	cellIdx  map[uint32]int32
	pending  int
	tick     int64
	z, zp    []uint64
	gd       []int32 // push distance to the nearest real goal
	cur      []int32 // boxes loaded in ps.occ
	curID    int32
	mark     []uint32
	vis      []uint32
	visStamp uint32
	st       []macroSt
	lab      []int32 // region number (1: the flooded player's region) of the free cells, valid where mark == stamp
	stamp    uint32
	q        []int32
	dd       []uint32 // dedupe stamps for the local update
	ddStamp  uint32
	top      []genEnt
	tys      [][2]int32
	base     uint64  // tracked bytes of the festb prefix tree
	cand     []int32 // one-region nodes, in creation order (hub candidates)
	best     int32   // node with the smallest key
	bestKey  uint64
	expanded int64
	created  int64
	dupes    int64
	capBytes uint64
	full     bool
	ns       int64     // time spent in run
	reach    *hubReach // exp-463 -hub-gen-reach: exact single-box reach of the start boxes (nil: off)
}

func newHubGen(ps *solver) *hubGen {
	cells := len(ps.lv.Wall)
	if cells >= 1<<16 || ps.n == 0 {
		return nil
	}
	g := &hubGen{ps: ps, n: ps.n, seen: map[uint64]int32{}, cellIdx: map[uint32]int32{}, curID: -2, best: -1, bestKey: ^uint64(0)}
	ps.pfInit()
	g.gd = make([]int32, cells)
	for c := range g.gd {
		g.gd[c] = -1
		for gi := 0; gi*cells < len(ps.pfDist); gi++ {
			if d := ps.pfDist[gi*cells+c]; d >= 0 && (g.gd[c] < 0 || d < g.gd[c]) {
				g.gd[c] = d
			}
		}
		g.gd[c] = max(g.gd[c], 0)
	}
	// Fixed pseudo-random zobrist (splitmix64): deterministic runs.
	g.z, g.zp = make([]uint64, cells), make([]uint64, cells)
	x := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
		z = (z ^ z>>27) * 0x94D049BB133111EB
		return z ^ z>>31
	}
	for c := range g.z {
		g.z[c], g.zp[c] = next(), next()
	}
	g.mark = make([]uint32, cells)
	g.lab = make([]int32, cells)
	g.vis = make([]uint32, cells*4)
	g.dd = make([]uint32, cells)
	g.cur = make([]int32, g.n)
	g.top = make([]genEnt, 0, hubGenTopK+1)
	if hubGenReach {
		if r := newHubReach(ps); r != nil {
			var cnt [64]int32
			goals := make([]uint16, 0, g.n)
			for _, b := range ps.lv.Boxes {
				goals = append(goals, uint16(b))
			}
			r.counts(goals, &cnt)
			sound := true
			for j := range r.nCls {
				sound = sound && cnt[j] >= r.need[j] // the solved position satisfies every class (the level is solvable)
			}
			if sound && r.feasible(ps.lv.Boxes) {
				g.reach = r
			}
		}
	}
	roots := ps.pullRoots
	if len(roots) == 0 {
		roots = []int32{ps.lv.Player}
	}
	for _, r := range roots {
		goals := make([]uint16, 0, g.n)
		for _, b := range ps.lv.Boxes {
			goals = append(goals, uint16(b))
		}
		g.addRoot(goals, r)
	}
	return g
}

func (g *hubGen) bytes() uint64 {
	return uint64(len(g.nodes))*72 + uint64(len(g.boxes))*2 + uint64(len(g.seen))*40 + uint64(g.pending)*16
}

// load sets ps.occ to the given boxes.
func (g *hubGen) load(bs []uint16) {
	occ := g.ps.occ
	if g.curID != -2 {
		for _, c := range g.cur {
			occ[c] = false
		}
	}
	for i, b := range bs {
		g.cur[i] = int32(b)
		occ[b] = true
	}
	g.curID = -1
}

func (g *hubGen) nodeBoxes(id int32) []uint16 { return g.boxes[int(id)*g.n : int(id+1)*g.n] }

func (g *hubGen) loadNode(id int32) {
	if g.curID == id {
		return
	}
	g.load(g.nodeBoxes(id))
	g.curID = id
}

// flood marks the free cells reachable from p with a fresh stamp and returns
// the smallest one; regions then counts the other free components.
func (g *hubGen) flood(p int32) (minC int32, regs int32) {
	ps := g.ps
	wall, occ := ps.lv.Wall, ps.occ
	g.stamp++
	st := g.stamp
	minC = p
	regNo := int32(0)
	fill := func(s int32) {
		regNo++
		q := append(g.q[:0], s)
		g.mark[s] = st
		g.lab[s] = regNo
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, d := range ps.dirs {
				nb := c + d
				if g.mark[nb] != st && !wall[nb] && !occ[nb] {
					g.mark[nb] = st
					g.lab[nb] = regNo
					q = append(q, nb)
					if nb < minC && s == p {
						minC = nb
					}
				}
			}
		}
		g.q = q
	}
	fill(p)
	regs = 1
	for c := range int32(len(wall)) {
		if !wall[c] && !occ[c] && g.mark[c] != st {
			regs++
			fill(c)
		}
	}
	return minC, regs
}

// mobOf is the number of directions in which the box on x can be pulled
// right now (the cell beside it and the one beyond are free).
func (g *hubGen) mobOf(x int32) (m int32) {
	wall, occ := g.ps.lv.Wall, g.ps.occ
	for _, d := range g.ps.dirs {
		a := x + d
		if wall[a] || occ[a] {
			continue
		}
		if b := a + d; !wall[b] && !occ[b] {
			m++
		}
	}
	return m
}

// local returns the (trapped, mobility) sums over the boxes whose pullability
// the move a -> b can change, before (occ as it is) and after it. ps.occ is
// restored.
func (g *hubGen) local(a, b int32) (trapB, mobB, trapA, mobA int32) {
	ps := g.ps
	wall, occ := ps.lv.Wall, ps.occ
	var cs [18]int32
	nc := 0
	g.ddStamp++
	st := g.ddStamp
	add := func(c int32) {
		if g.dd[c] != st {
			g.dd[c] = st
			cs[nc] = c
			nc++
		}
	}
	add(a)
	add(b)
	for _, d := range ps.dirs {
		for _, c := range [2]int32{a, b} {
			if c1 := c - d; !wall[c1] {
				add(c1)
				if c2 := c1 - d; !wall[c2] {
					add(c2)
				}
			}
		}
	}
	for _, c := range cs[:nc] {
		if occ[c] {
			m := g.mobOf(c)
			mobB += m
			if m == 0 {
				trapB++
			}
		}
	}
	occ[a], occ[b] = false, true
	for _, c := range cs[:nc] {
		if occ[c] {
			m := g.mobOf(c)
			mobA += m
			if m == 0 {
				trapA++
			}
		}
	}
	occ[a], occ[b] = true, false
	return
}

// fullEval computes the node-wide counts of the loaded boxes.
func (g *hubGen) fullEval(nd *genNode, bs []uint16) {
	ps := g.ps
	nd.trap, nd.mob, nd.far, nd.g = 0, 0, 0, 0
	for _, b := range bs {
		m := g.mobOf(int32(b))
		nd.mob += m
		if m == 0 {
			nd.trap++
		}
		nd.far += g.gd[b]
		if ps.pfGoal(int32(b)) {
			nd.g++
		}
	}
}

func (g *hubGen) key(reg, trap, bic, far, mob int32) uint64 {
	f := uint64((1<<19 - 1) - min(far, 1<<19-1))
	m := uint64((1<<19 - 1) - min(mob, 1<<19-1))
	k := uint64(min(reg, 15))<<60 | uint64(min(trap, 1023))<<50 | uint64(min(bic, 4095))<<38
	if hubGenKey == "mf" {
		return k | m<<19 | f
	}
	return k | f<<19 | m
}

func (g *hubGen) nodeKey(nd *genNode) uint64 { return g.key(nd.reg, nd.trap, nd.bic, nd.far, nd.mob) }

func (g *hubGen) addRoot(bs []uint16, player int32) {
	g.load(bs)
	var nd genNode
	nd.parent = -1
	for _, b := range bs {
		nd.hash ^= g.z[b]
	}
	minC, regs := g.flood(player)
	nd.player, nd.reg = minC, regs
	g.fullEval(&nd, bs)
	if hubGenBic {
		nd.bic = g.ps.pfBicon()
	}
	full := nd.hash ^ g.zp[minC]
	if _, ok := g.seen[full]; ok {
		return
	}
	id := int32(len(g.nodes))
	g.nodes = append(g.nodes, nd)
	g.boxes = append(g.boxes, bs...)
	g.seen[full] = id
	g.note(id)
	g.push(genEnt{key: g.nodeKey(&nd), id: id, mv: genExpand}, nd.reg, nd.trap)
}

func (g *hubGen) note(id int32) {
	if k := g.nodeKey(&g.nodes[id]); k < g.bestKey {
		g.bestKey, g.best = k, id
	}
}

// Binary min-heap on key (ties: smaller id first, so runs are deterministic).
func entLess(a, b genEnt) bool {
	if a.key != b.key {
		return a.key < b.key
	}
	if a.id != b.id {
		return a.id < b.id
	}
	return a.mv < b.mv
}

// Cells (FESS / type-based exploration): entries are bucketed by the type
// (regions, trapped boxes / 4) of the board they lead to. Pops alternate
// between the globally best entry and the best entry of the least-expanded
// cell, so a plateau of equal-key boards cannot starve the cells behind it.
type genCell struct {
	heap  []genEnt
	count int32
}

func (g *hubGen) cellOf(reg, trap int32) *genCell {
	ty := uint32(min(reg, 255))<<16 | uint32(min(trap>>2, 255))
	ci, ok := g.cellIdx[ty]
	if !ok {
		ci = int32(len(g.cells))
		g.cellIdx[ty] = ci
		g.cells = append(g.cells, genCell{})
	}
	return &g.cells[ci]
}

func (g *hubGen) push(e genEnt, reg, trap int32) {
	c := g.cellOf(reg, trap)
	h := append(c.heap, e)
	i := len(h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !entLess(h[i], h[p]) {
			break
		}
		h[i], h[p] = h[p], h[i]
		i = p
	}
	c.heap = h
	g.pending++
}

func (g *hubGen) pop() genEnt {
	g.tick++
	bi := -1
	if g.tick&1 == 0 || !hubGenCells {
		for i := range g.cells {
			if h := g.cells[i].heap; len(h) > 0 && (bi < 0 || entLess(h[0], g.cells[bi].heap[0])) {
				bi = i
			}
		}
	} else {
		for i := range g.cells {
			h := g.cells[i].heap
			if len(h) == 0 {
				continue
			}
			if bi < 0 || g.cells[i].count < g.cells[bi].count || g.cells[i].count == g.cells[bi].count && entLess(h[0], g.cells[bi].heap[0]) {
				bi = i
			}
		}
	}
	c := &g.cells[bi]
	c.count++
	h := c.heap
	e := h[0]
	n := len(h) - 1
	h[0] = h[n]
	h = h[:n]
	i := 0
	for {
		l := 2*i + 1
		if l >= n {
			break
		}
		if r := l + 1; r < n && entLess(h[r], h[l]) {
			l = r
		}
		if !entLess(h[l], h[i]) {
			break
		}
		h[i], h[l] = h[l], h[i]
		i = l
	}
	c.heap = h
	g.pending--
	return e
}

// run expands until the deadline (or maxExp more expansions, 0: no bound),
// the memory cap, or the heap empties. It reports whether the search is over.
func (g *hubGen) run(deadline time.Time, maxExp int64) (over bool) {
	t0 := time.Now()
	defer func() { g.ns += time.Since(t0).Nanoseconds() }()
	start := g.expanded
	for steps := 0; g.pending > 0; steps++ {
		if steps&31 == 0 {
			if Stop.Load() || !deadline.IsZero() && time.Now().After(deadline) {
				return false
			}
			if g.capBytes > 0 && g.bytes() > g.capBytes {
				g.full = true
				return true
			}
		}
		if maxExp > 0 && g.expanded-start >= maxExp {
			return false
		}
		e := g.pop()
		if e.mv == genExpand {
			g.expand(e.id)
		} else {
			g.create(e)
		}
	}
	return true
}

// create makes the child entry e describes (box i of node e.id pulled to x,
// the player left on side k of it), unless it was seen.
func (g *hubGen) create(e genEnt) {
	ps := g.ps
	p := e.id
	i, k, to := int(e.mv>>18), e.mv>>16&3, int32(e.mv&0xFFFF)
	pb := g.nodeBoxes(p)
	from := int32(pb[i])
	g.loadNode(p)
	pn := g.nodes[p]
	trapB, mobB, trapA, mobA := g.local(from, to)
	var nd genNode
	nd.parent = p
	nd.mv = e.mv
	nd.depth = pn.depth + 1
	nd.trap, nd.mob = pn.trap-trapB+trapA, pn.mob-mobB+mobA
	nd.far = pn.far - g.gd[from] + g.gd[to]
	nd.g = pn.g
	if ps.pfGoal(from) {
		nd.g--
	}
	if ps.pfGoal(to) {
		nd.g++
	}
	nd.hash = pn.hash ^ g.z[from] ^ g.z[to]
	// Move to the child: occ and the loaded list follow.
	ps.occ[from], ps.occ[to] = false, true
	g.cur[i] = to
	g.curID = -1
	minC, regs := g.flood(to + ps.dirs[k])
	nd.player, nd.reg = minC, regs
	full := nd.hash ^ g.zp[minC]
	if _, ok := g.seen[full]; ok {
		g.dupes++
		return
	}
	if hubGenBic {
		nd.bic = ps.pfBicon()
	}
	id := int32(len(g.nodes))
	g.nodes = append(g.nodes, nd)
	g.boxes = append(g.boxes, pb...)
	g.boxes[int(id)*g.n+i] = uint16(to)
	g.seen[full] = id
	g.curID = id
	g.created++
	if int(nd.reg) <= max(hubGenPockets, 1) {
		g.cand = append(g.cand, id)
	}
	g.note(id)
	g.push(genEnt{key: g.nodeKey(&nd), id: id, mv: genExpand}, nd.reg, nd.trap)
}

type macroSt struct {
	x   int32
	par int32
	k   int8
}

// macroBFS explores the pull-walks of the box on c: states (x, k) with the
// box on x and the player on its side k, reached by pulling straight on or
// turning the corner (one free diagonal) and pulling the other way. The
// player's flood labels (lab == 1) give the first pulls. ps.occ is restored.
func (g *hubGen) macroBFS(c int32, limit int) []macroSt {
	ps := g.ps
	wall, occ := ps.lv.Wall, ps.occ
	g.visStamp++
	vs := g.visStamp
	st := g.st[:0]
	occ[c] = false
	for k, d := range ps.dirs {
		p := c + d
		if g.mark[p] != g.stamp || g.lab[p] != 1 {
			continue
		}
		if q := p + d; !wall[q] && !occ[q] {
			g.vis[int(p)*4+k] = vs
			st = append(st, macroSt{x: p, par: -1, k: int8(k)})
		}
	}
	free := func(c int32) bool { return !wall[c] && !occ[c] }
	for idx := 0; idx < len(st) && len(st) < limit; idx++ {
		x, k := st[idx].x, int(st[idx].k)
		dk := ps.dirs[k]
		try := func(nx int32, nk int) {
			if v := int(nx)*4 + nk; g.vis[v] != vs {
				g.vis[v] = vs
				st = append(st, macroSt{x: nx, par: int32(idx), k: int8(nk)})
			}
		}
		if free(x + 2*dk) {
			try(x+dk, k)
		}
		for k2, d2 := range ps.dirs {
			if k2>>1 == k>>1 {
				continue
			}
			if free(x+dk+d2) && free(x+d2) && free(x+2*d2) {
				try(x+d2, k2)
			}
		}
	}
	occ[c] = true
	g.st = st
	return st
}

// expand pushes the best genTopK pull-walks of node id, keyed by the node's
// exact regions and cycle count and the children's locally updated counts.
func (g *hubGen) expand(id int32) {
	ps := g.ps
	g.loadNode(id)
	nd := g.nodes[id]
	g.expanded++
	_, _ = g.flood(nd.player)
	bs := g.nodeBoxes(id)
	var cnt [64]int32
	rc := g.reach
	if rc != nil {
		rc.counts(bs, &cnt)
	}
	top := g.top[:0]
	tys := g.tys[:0]
	for i, b16 := range bs {
		c := int32(b16)
		sts := g.macroBFS(c, hubGenMacro)
		if len(sts) == 0 {
			continue
		}
		for _, s := range sts {
			x := s.x
			if x == c || ps.dist[x] < 0 || rc != nil && (!rc.inU[x] || !rc.ok(c, x, &cnt)) {
				continue
			}
			if PFIneff && ps.pfInefficient(x, c) {
				continue // exp-696
			}
			trapB, mobB, trapA, mobA := g.local(c, x)
			er, et := max(nd.reg+g.dReg(c, x), 1), nd.trap-trapB+trapA
			key := g.key(er, et, nd.bic, nd.far-g.gd[c]+g.gd[x], nd.mob-mobB+mobA)
			ent := genEnt{key: key, id: id, mv: uint32(i)<<18 | uint32(s.k)<<16 | uint32(x)}
			if len(top) == hubGenTopK && !entLess(ent, top[len(top)-1]) {
				continue
			}
			ty := [2]int32{er, et}
			if len(top) < hubGenTopK {
				top = append(top, ent)
				tys = append(tys, ty)
			} else {
				top[len(top)-1], tys[len(top)-1] = ent, ty
			}
			for j := len(top) - 1; j > 0 && entLess(top[j], top[j-1]); j-- {
				top[j], top[j-1] = top[j-1], top[j]
				tys[j], tys[j-1] = tys[j-1], tys[j]
			}
		}
	}
	g.top, g.tys = top, tys
	for j, e := range top {
		g.push(e, tys[j][0], tys[j][1])
	}
}

// unpack writes node id's boxes (as cells) to cur.
func (g *hubGen) unpack(id int32, cur []int32) {
	for i, b := range g.nodeBoxes(id) {
		cur[i] = int32(b)
	}
}

// tuple is the node as a pull tuple (hot = trapped, dist = summed goal
// distance, bic = off-cycle cells).
func (g *hubGen) tuple(id int32) pfTuple {
	nd := &g.nodes[id]
	return pfTuple{reg: nd.reg, hot: nd.trap, dist: nd.far, bic: nd.bic}
}

// tail is the forward pushes from node id's position to the solved position:
// each pull-walk undone, newest first (the walks are found again by BFS on
// the parent's board).
func (g *hubGen) tail(id int32) []meet.Push {
	var out []meet.Push
	opp := [4]int8{1, 0, 3, 2}
	curID := g.curID
	_ = curID
	for ; g.nodes[id].parent >= 0; id = g.nodes[id].parent {
		nd := &g.nodes[id]
		par := nd.parent
		i, k, x := int(nd.mv>>18), int8(nd.mv>>16&3), int32(nd.mv&0xFFFF)
		c := int32(g.nodeBoxes(par)[i])
		g.loadNode(par)
		g.flood(g.nodes[par].player)
		sts := g.macroBFS(c, 1<<30)
		at := -1
		for j, s := range sts {
			if s.x == x && s.k == k {
				at = j
				break
			}
		}
		if at < 0 {
			return nil // cannot happen: the walk was found when the node was created
		}
		for j := at; j >= 0; j = int(sts[j].par) {
			out = append(out, meet.Push{From: sts[j].x, Dir: opp[sts[j].k]})
		}
	}
	return out
}

// nearDup reports whether nodes a and b differ in fewer than delta boxes.
func (g *hubGen) nearDup(a, b int32, delta int) bool {
	g.ddStamp++
	st := g.ddStamp
	for _, c := range g.nodeBoxes(a) {
		g.dd[c] = st
	}
	common := 0
	for _, c := range g.nodeBoxes(b) {
		if g.dd[c] == st {
			common++
		}
	}
	return g.n-common < delta
}

// ---- hub lane glue (-hub-back G) ----

func (h *hub) unpackNode(id int32, cur []int32) {
	if h.gen != nil {
		h.gen.unpack(id, cur)
		return
	}
	h.ps.unpack(id, cur)
}

func (h *hub) bestNode() int32 {
	if h.gen != nil {
		return h.gen.best
	}
	return h.ps.frontier.best
}

func (h *hub) tupleOf(id int32) pfTuple {
	if h.gen != nil {
		return h.gen.tuple(id)
	}
	return h.ps.pfOwn[id]
}

// genSlice runs the generator for d (or for -nodes expansions when set,
// whichever the harness bounds) and reports whether it is over.
func (h *hub) genSlice(d time.Duration) bool {
	g := h.gen
	if g.base == 0 {
		g.base = h.ps.trackedP.Load() // the festb prefix's pull tree stays allocated
	}
	over := g.run(h.sliceEnd(d), 0)
	h.ps.trackedP.Store(g.base + g.bytes())
	return over
}

// harvestG is harvest over the generator's one-region nodes (g.cand, in
// creation order), within about budget of wall time; a backlog beyond what
// fits is strided, re-estimated every 64 visits.
func (h *hub) harvestG(budget time.Duration) {
	g := h.gen
	t0 := time.Now()
	end := int32(len(g.cand))
	if h.scan >= end {
		h.rescanOn = false
		return
	}
	if HubIdleRing {
		for need := (int(end) + 63) >> 6; len(h.candSeen) < need; {
			h.candSeen = append(h.candSeen, 0)
		}
	}
	if h.rescanOn {
		// exp-758: revisit, stride 1, the candidates earlier strided scans skipped.
		visited := 0
		k := h.scan
		for ; k < end; k++ {
			if h.candSeen[k>>6]&(1<<(k&63)) != 0 {
				continue
			}
			if visited&63 == 63 && time.Since(t0) > budget {
				break
			}
			visited++
			h.harvestOne(k)
		}
		h.scan = k
		if k >= end {
			h.rescanOn = false
		}
		h.scanNs += time.Since(t0).Nanoseconds()
		return
	}
	stride := int32(1)
	visited := 0
	k := h.scan
	for ; k < end; k += stride {
		if visited&63 == 63 {
			el := time.Since(t0)
			if el > 3*budget {
				break
			}
			if per := float64(el) / float64(visited); per > 0 {
				if capN := float64(budget) / per; capN > 0 && float64(end-k) > capN {
					stride = int32(float64(end-k)/capN) + 1
				}
			}
		}
		visited++
		h.harvestOne(k)
	}
	h.scanSkipped += int(min(k, end)-h.scan) - visited
	h.scan = min(k, end)
	h.scanNs += time.Since(t0).Nanoseconds()
}

// harvestOne judges candidate g.cand[k] and admits it to the pool if it qualifies.
func (h *hub) harvestOne(k int32) {
	g := h.gen
	if HubIdleRing && h.candSeen[k>>6]&(1<<(k&63)) == 0 {
		h.candSeen[k>>6] |= 1 << (k & 63)
		h.nSeen++
	}
	h.scanned++
	id := g.cand[k]
	t := g.tuple(id)
	pocket := t.reg > 1
	t.reg = 1 // pocket hubs: judged on their player region (score recomputes the unpullable count)
	tier := hubTier(t)
	h.tierSeen[tier]++
	h.nReg1++
	h.minHot0 = min(h.minHot0, int(t.hot))
	h.minHot = min(h.minHot, int(t.hot))
	if tier > 3 {
		return
	}
	if t.dist > h.maxD[tier] {
		h.maxD[tier] = t.dist
	}
	if t.dist < h.maxD[tier]/2 {
		return
	}
	h.eligible++
	cut, pinned, trapped := h.score(id)
	if pocket {
		if h.lastPocket > hubPocketMax {
			return
		}
		t.hot = h.lastHot
		if tier = hubTier(t); tier > 3 {
			return
		}
	}
	h.admit(hubCand{id: id, cut: cut, pinned: pinned, trapped: trapped, dist: t.dist, tier: tier, pocket: pocket})
}

// genOnly is the generator-quality harness (-hub-gen-only): run the
// generator alone (for -t, or -nodes expansions), harvest the pool, print
// the hubs with their characterisation, and return.
func (h *hub) genOnly() Result {
	g := h.gen
	var dl time.Time
	t0 := time.Now()
	harvest := 3 * time.Second
	if !h.deadline.IsZero() {
		dl = t0.Add(time.Until(h.deadline) * 7 / 10) // the rest is for harvesting and the report
		harvest = time.Until(h.deadline) / 10
	}
	g.run(dl, NodeBudget)
	el := time.Since(t0)
	h.harvestG(harvest)
	// Pool order: the nomination order without the start (lb zero).
	var rows []hubCand
	rows = append(rows, h.pool[:h.nPool]...)
	slices.SortFunc(rows, func(a, b hubCand) int {
		if a.keyLess(&b) {
			return -1
		}
		if b.keyLess(&a) {
			return 1
		}
		return 0
	})
	ps := h.ps
	out := map[string]any{
		"gen_expanded": g.expanded, "gen_created": g.created, "gen_dupes": g.dupes, "gen_ms": el.Milliseconds(),
		"gen_rate": float64(g.expanded) / max(el.Seconds(), 1e-9), "gen_nodes": len(g.nodes), "gen_full": g.full, "gen_mb": g.bytes() >> 20,
		"gen_best": h.statsOf(g.best), "n": ps.n, "pool": h.nPool, "tier_seen": h.tierSeen[:], "scanned": h.scanned, "eligible": h.eligible,
	}
	var hubs []map[string]any
	rr := g.reach
	if rr == nil {
		rr = newHubReach(ps) // diagnostics with the flag off: report the same numbers
	}
	if rr != nil {
		out["reach_classes"], out["reach_need"], out["reach_on"] = rr.nCls, rr.need, g.reach != nil
	}
	for i, c := range rows {
		if i >= 5 {
			break
		}
		st := h.statsOf(c.id)
		if rr != nil {
			h.unpackNode(c.id, h.buf)
			st["reach_feasible"] = rr.feasible(h.buf)
		}
		hubs = append(hubs, st)
	}
	out["hubs"] = hubs
	if p := os.Getenv("HUBGEN_DUMP"); p != "" {
		var sb strings.Builder
		sb.WriteString("BEST\n" + g.dump(g.best))
		for i, c := range rows {
			if i < 5 {
				sb.WriteString("HUB\n" + g.dump(c.id))
			}
		}
		os.WriteFile(p, []byte(sb.String()), 0o644)
	}
	if h.tr != nil {
		for k, v := range out {
			h.tr.SetExtra("hubgen_"+k, v)
		}
	}
	return Result{Reason: "hub ended"}
}

// statsOf characterises generator node id the way exp-449 characterised
// Festival's hubs: regions, off-cycle cells, cut, pinned and trapped boxes.
func (h *hub) statsOf(id int32) map[string]any {
	if id < 0 {
		return nil
	}
	g, ps := h.gen, h.ps
	nd := g.nodes[id]
	cut, pinned, trapped := h.score(id)
	g.invalidate()
	h.unpackNode(id, h.buf)
	for _, b := range h.buf {
		ps.occ[b] = true
	}
	hot := ps.pfHot(h.buf)
	match := ps.pfMatch(h.buf)
	for _, b := range h.buf {
		ps.occ[b] = false
	}
	return map[string]any{"id": id, "depth": nd.depth, "regions": nd.reg, "trap_strict": nd.trap, "mob": nd.mob, "bic": nd.bic, "far": nd.far,
		"on_goal": nd.g, "cut": cut, "pinned": pinned, "trapped_goal": trapped, "pf_hot": hot, "match_dist": match}
}

// dReg estimates the change in floor regions when the box on a is pulled to
// b: a becomes floor and joins the distinct regions of its other neighbours
// (none: a is a new one-cell region). Splits caused by the box on b are not
// seen; the child's exact count replaces the estimate when it is created.
func (g *hubGen) dReg(a, b int32) int32 {
	wall, occ := g.ps.lv.Wall, g.ps.occ
	var seen [4]int32
	k := 0
	for _, d := range g.ps.dirs {
		c := a + d
		if c == b || wall[c] || occ[c] {
			continue
		}
		l := g.lab[c]
		dup := false
		for _, x := range seen[:k] {
			dup = dup || x == l
		}
		if !dup {
			seen[k] = l
			k++
		}
	}
	if k == 0 {
		return 1
	}
	return int32(1 - k)
}

// dumpBoxes renders a board (diagnostics): # wall, $ box, * box on a real goal, . free goal, a-z other free regions of the player's.
func dumpBoxes(ps *solver, boxes []int32, player int32) string {
	occ := make([]bool, len(ps.lv.Wall))
	for _, b := range boxes {
		occ[b] = true
	}
	lab := make([]int32, len(occ))
	n := int32(0)
	fill := func(c int32) {
		n++
		q := []int32{c}
		lab[c] = n
		for i := 0; i < len(q); i++ {
			for _, d := range ps.dirs {
				if y := q[i] + d; !ps.lv.Wall[y] && !occ[y] && lab[y] == 0 {
					lab[y] = n
					q = append(q, y)
				}
			}
		}
	}
	fill(player)
	for c := range int32(len(occ)) {
		if !ps.lv.Wall[c] && !occ[c] && lab[c] == 0 {
			fill(c)
		}
	}
	var sb []byte
	W := ps.lv.W
	for y := 0; y < ps.lv.H; y++ {
		for x := 0; x < W; x++ {
			c := int32(y*W + x)
			switch {
			case ps.lv.Wall[c]:
				sb = append(sb, '#')
			case occ[c] && ps.pfGoal(c):
				sb = append(sb, '*')
			case occ[c]:
				sb = append(sb, '$')
			case lab[c] == 1:
				if ps.pfGoal(c) {
					sb = append(sb, '.')
				} else {
					sb = append(sb, ' ')
				}
			default:
				sb = append(sb, byte('a'+(lab[c]-2)%26))
			}
		}
		sb = append(sb, '\n')
	}
	return string(sb)
}

func (g *hubGen) dump(id int32) string {
	boxes := make([]int32, g.n)
	g.unpack(id, boxes)
	return dumpBoxes(g.ps, boxes, g.nodes[id].player)
}

// invalidate clears the loaded boxes from ps.occ; the next use reloads.
func (g *hubGen) invalidate() {
	if g.curID != -2 {
		for _, c := range g.cur {
			g.ps.occ[c] = false
		}
		g.curID = -2
	}
}
