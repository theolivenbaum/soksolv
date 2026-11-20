package main

import (
	"flag"
	"math/bits"
)

// exp-641 (research/exp-637/IDEAS.md §3 E1): Hall credit gate
// for the fa2 engine. fa2's packed prefix never unpacks, so it treats
// the plan's credited targets as permanent. Under that same assumption
// (credited permanent targets = walls) the loose boxes must still match the
// open goals, each loose box serving the goals a lone box can be pushed to.
//
//   - -fa2-hall: a node's packed credit is the largest prefix whose walls
//     leave a perfect matching (deficit 0), scanning down from the raw prefix.
//   - -fa2-hall-diag: compute and count only (no key change).
//
// Levels: level k walls the first k permanent plan entries (until == -1).
// Per level and cell, a goal bitset of the goals a lone box on that cell can
// be pushed to with the level's walls (the sourcesOf relation, one pull
// search per goal over side-component labels). Levels are built lazily.
// Walls only shrink going down a level, so the relation only grows and a
// matching at level k stays valid at k-1; every solve is a warm start
// (repair the given matching, then one augmenting attempt per free box,
// which is exact by Kuhn's lemma).

var (
	FA2Hall     bool
	FA2HallDiag bool
	FA2HallCap  = 16 << 20 // per-level build work cap (free cells^2 + 4*goals*free cells)
	FA2HallSup  bool
	FA2HallSide bool
	FA2HallSink bool
)

func init() {
	flag.BoolVar(&FA2Hall, "fa2-hall", true, "exp-641 (promoted 2026-11-12) fa2: Hall credit gate: packed credit = largest plan prefix whose permanent targets as walls leave the loose boxes perfectly matchable to the open goals")
	flag.BoolVar(&FA2HallSup, "fa2-hall-sup", false, "exp-681 fa2: supply-staged Hall relation: a box serves open goal g at level k only if it can reach g through the plan's wall stages k, k+1, ..., L(g) (L(g) = permanent entries before g), so a box must reach a region before the plan seals it")
	flag.BoolVar(&FA2HallSide, "fa2-hall-side", true, "exp-681 fa2: player-side Hall relation: a box on a cut cell serves only the goals reachable from the side region the player is in (walls + that box), not from every side")
	flag.BoolVar(&FA2HallSink, "fa2-hall-sink", true, "exp-681 fa2: a box on a sink goal (no push possible with walls alone) is a wall for the Hall relation at every level, credited or not")
	flag.BoolVar(&FA2HallDiag, "fa2-hall-diag", false, "exp-641 fa2: compute the Hall credit and count inconsistent expansions (no key change; counters with -fa2-debug)")
}

type hallLevel struct {
	wall []uint64 // cells walled at this level (the permanent targets)
	open []uint64 // goals not walled
	adj  []uint64 // cell*gw: goals a lone box on the cell can be pushed to
	// exp-681 -fa2-hall-side: for a cut cell c (>= 2 side regions), reg[c]
	// labels every free cell by its region of (free cells minus c), -1
	// elsewhere; sadj[(c*4+lab)*gw] is the goal row of a box on c with the
	// player in region lab. nil rows: one region (use adj).
	reg  [][]int8
	sadj []uint64
}

type hallM struct {
	mb []int32 // per box index: matched goal index or -1
	mg []int32 // per goal index: matched box index or -1
}

type fa2Hall struct {
	s         *solver
	ng, gw    int
	cw        int     // words per cell bitset
	gidx      []int32 // cell -> goal index or -1
	goalAt    []int32 // goal index -> cell
	permAt    []int32 // entry prefix length p -> permanent entries in e[:p]
	permPos   []int32 // k -> entry index of the k-th permanent entry
	permTo    []int32 // k -> its target cell
	lv        []*hallLevel
	off       bool     // board over the work cap
	vis       []uint64 // goals visited by the current augmenting attempt
	m0        hallM    // parent: matching at its raw level
	mc        hallM    // child scratch
	parentOK  bool
	pl        []hallPL // per level: the expanded parent's data (valid when st == pst)
	pst       uint32
	pc        []int32 // the parent's box cells
	lastLoose int32
	side      bool  // exp-681b: build the player-side tables
	pp, cp    int32 // exp-681: parent and child player cells (-1: unknown)
	// exp-681 -fa2-hall-sink: sinkBit[c] is the bit of sink goal c (-1: not
	// a sink); mask = occupied sinks the current tables wall; lvm holds the
	// levels of nonzero masks.
	sinkBit     []int8
	mask, pmask uint64
	lvm         map[uint64][]*hallLevel
	mcReady     bool
	mw          hallM // box-structure scratch
	fw          []uint64
	skipped     int64
	lazy        int64

	// build scratch
	comp []int8
	mark []uint32
	mst  uint32
	q    []int32

	// counters
	nodes, incons, gated, levels int64
	dead0                        int64
	defSum                       int64
}

func newFA2Hall(s *solver, fp *festPlan) *fa2Hall {
	return newFA2HallOpt(s, fp, FA2HallSide, FA2HallSink)
}

// newFA2HallOpt builds the Hall tables with explicit side/sink options
// (exp-681b: the frozen-wall deadlock rule always uses the side relation).
func newFA2HallOpt(s *solver, fp *festPlan, side, sink bool) *fa2Hall {
	n := len(s.lv.Wall)
	h := &fa2Hall{s: s, cw: (n + 63) >> 6, gidx: make([]int32, n)}
	for c := range h.gidx {
		h.gidx[c] = -1
		if s.lv.Goal[c] {
			h.gidx[c] = int32(len(h.goalAt))
			h.goalAt = append(h.goalAt, int32(c))
		}
	}
	h.ng = len(h.goalAt)
	h.gw = (h.ng + 63) >> 6
	h.permAt = make([]int32, len(fp.e)+1)
	for i, e := range fp.e {
		h.permAt[i+1] = h.permAt[i]
		if e.until == -1 && e.to >= 0 && int(e.to) < n {
			h.permAt[i+1]++
			h.permPos = append(h.permPos, int32(i))
			h.permTo = append(h.permTo, e.to)
		}
	}
	h.lv = make([]*hallLevel, len(h.permTo)+1)
	h.side = side
	if sink {
		h.sinkBit = make([]int8, n)
		nb := 0
		for c := range h.sinkBit {
			h.sinkBit[c] = -1
			if s.lv.Wall[c] || !s.lv.Goal[c] || nb >= 64 {
				continue
			}
			can := false
			for _, d := range s.dirs {
				if !s.lv.Wall[int32(c)+d] && !s.lv.Wall[int32(c)-d] {
					can = true
				}
			}
			if !can {
				h.sinkBit[c] = int8(nb)
				nb++
			}
		}
		h.lvm = map[uint64][]*hallLevel{}
	}
	h.pl = make([]hallPL, len(h.permTo)+1)
	h.pc = make([]int32, s.n)
	h.fw = make([]uint64, h.gw)
	free := 0
	for _, w := range s.lv.Wall {
		if !w {
			free++
		}
	}
	if free*free+4*h.ng*free > FA2HallCap {
		h.off = true
	}
	h.vis = make([]uint64, h.gw)
	for _, m := range []*hallM{&h.m0, &h.mc, &h.mw} {
		m.mb = make([]int32, s.n)
		m.mg = make([]int32, h.ng)
		m.reset()
	}
	return h
}

func (m *hallM) reset() {
	for i := range m.mb {
		m.mb[i] = -1
	}
	for i := range m.mg {
		m.mg[i] = -1
	}
}

func (m *hallM) copyFrom(o *hallM) {
	copy(m.mb, o.mb)
	copy(m.mg, o.mg)
}

// level returns level k's tables, building them on first use.
func (h *fa2Hall) level(k int) *hallLevel {
	lv := h.lv
	if h.mask != 0 {
		lv = h.lvm[h.mask]
		if lv == nil {
			lv = make([]*hallLevel, len(h.lv))
			h.lvm[h.mask] = lv
		}
	}
	if l := lv[k]; l != nil {
		return l
	}
	if FA2HallSup {
		// exp-681: build every level top-down; level k seeds each goal's pull
		// search with the (cell, side) states it reached at level k+1.
		var prev [][]uint64
		for j := len(lv) - 1; j >= 0; j-- {
			lv[j], prev = h.build(j, prev)
		}
		return lv[k]
	}
	lv[k], _ = h.build(k, nil)
	return lv[k]
}

// sinkMask is the set of occupied sink goals of cur.
func (h *fa2Hall) sinkMask(cur []int32) uint64 {
	var m uint64
	if h.sinkBit != nil {
		for _, c := range cur {
			if b := h.sinkBit[c]; b >= 0 {
				m |= 1 << uint(b)
			}
		}
	}
	return m
}

// build makes level k's tables. With prev (exp-681 -fa2-hall-sup), prev[gi]
// holds the pull states of goal gi at level k+1 (nil: gi walled there or no
// staging); those states seed gi's search here, and the states reached are
// returned per goal when staging (prev != nil or FA2HallSup).
func (h *fa2Hall) build(k int, prev [][]uint64) (*hallLevel, [][]uint64) {
	s := h.s
	n := len(s.lv.Wall)
	l := &hallLevel{wall: make([]uint64, h.cw), adj: make([]uint64, n*h.gw)}
	h.levels++
	var lab []int8 // per flood cell: its region label for the current b
	var flooded []int32
	regBytes := 0
	if h.side {
		l.reg = make([][]int8, n)
		l.sadj = make([]uint64, 4*n*h.gw)
		lab = make([]int8, n)
		for i := range lab {
			lab[i] = -1
		}
	}
	blocked := func(c int32) bool {
		return s.lv.Wall[c] || l.wall[c>>6]>>(uint(c)&63)&1 != 0
	}
	for _, c := range h.permTo[:k] {
		l.wall[c>>6] |= 1 << (uint(c) & 63)
	}
	if h.mask != 0 {
		for c, b := range h.sinkBit {
			if b >= 0 && h.mask>>uint(b)&1 != 0 {
				l.wall[c>>6] |= 1 << (uint(c) & 63)
			}
		}
	}
	l.open = make([]uint64, h.gw)
	for gi, g := range h.goalAt {
		if !blocked(g) {
			l.open[gi>>6] |= 1 << (uint(gi) & 63)
		}
	}
	// Side components: comp[b*4+d] labels the region (free cells minus b)
	// of b's neighbour in direction d (the lowest direction of the region),
	// -1 when that neighbour is blocked.
	if h.comp == nil {
		h.comp = make([]int8, 4*n)
		h.mark = make([]uint32, n)
	}
	for b := int32(0); b < int32(n); b++ {
		for d := 0; d < 4; d++ {
			h.comp[b*4+int32(d)] = -1
		}
		if blocked(b) {
			continue
		}
		var done [4]bool
		nreg := 0
		flooded = flooded[:0]
		for d, off := range s.dirs {
			x := b + off
			if done[d] || blocked(x) {
				continue
			}
			h.mst++
			h.mark[x] = h.mst
			q := append(h.q[:0], x)
			for i := 0; i < len(q); i++ {
				for _, o := range s.dirs {
					y := q[i] + o
					if y != b && h.mark[y] != h.mst && !blocked(y) {
						h.mark[y] = h.mst
						q = append(q, y)
					}
				}
			}
			h.q = q
			for d2, o2 := range s.dirs {
				if y := b + o2; !blocked(y) && h.mark[y] == h.mst {
					done[d2] = true
					h.comp[b*4+int32(d2)] = int8(d)
				}
			}
			nreg++
			if lab != nil {
				for _, y := range q {
					lab[y] = int8(d)
				}
				flooded = append(flooded, q...)
			}
		}
		if lab != nil {
			if nreg >= 2 && regBytes+n <= FA2HallCap/4 {
				regBytes += n
				r := make([]int8, n)
				copy(r, lab)
				l.reg[b] = r
			}
			for _, y := range flooded {
				lab[y] = -1
			}
		}
	}
	// One pull search per open goal: state (box cell, player side).
	seen := make([]uint32, 4*n)
	var st uint32
	var q []int32
	var out [][]uint64
	sw := (4*n + 63) >> 6
	if FA2HallSup {
		out = make([][]uint64, h.ng)
	}
	for gi, g := range h.goalAt {
		if blocked(g) {
			continue
		}
		st++
		gw, gb := gi>>6, uint64(1)<<(uint(gi)&63)
		l.adj[int(g)*h.gw+gw] |= gb
		q = q[:0]
		for d := 0; d < 4; d++ {
			if h.comp[g*4+int32(d)] >= 0 {
				seen[g*4+int32(d)] = st
				q = append(q, g*4+int32(d))
			}
		}
		if prev != nil && prev[gi] != nil {
			// states reachable at level k+1 (walls shrink: still valid here),
			// closed over the sides sharing a region at this level
			for w, x := range prev[gi] {
				for ; x != 0; x &= x - 1 {
					sid := int32(w<<6 + bits.TrailingZeros64(x))
					b := sid / 4
					l.adj[int(b)*h.gw+gw] |= gb
					lab := h.comp[sid]
					if lab < 0 {
						continue
					}
					for d2 := int32(0); d2 < 4; d2++ {
						if k2 := b*4 + d2; h.comp[k2] == lab && seen[k2] != st {
							seen[k2] = st
							q = append(q, k2)
						}
					}
				}
			}
		}
		for i := 0; i < len(q); i++ {
			b, d := q[i]/4, q[i]%4
			off := s.dirs[d]
			to, p := b+off, b+2*off
			if blocked(to) || blocked(p) {
				continue
			}
			l.adj[int(to)*h.gw+gw] |= gb
			lab := h.comp[to*4+d] // the player stands on p, to's neighbour in direction d
			for d2 := int32(0); d2 < 4; d2++ {
				if k2 := to*4 + d2; h.comp[k2] == lab && seen[k2] != st {
					seen[k2] = st
					q = append(q, k2)
				}
			}
		}
		if l.sadj != nil {
			for _, sid := range q {
				l.sadj[int(sid)*h.gw+gw] |= gb
			}
		}
		if out != nil {
			bs := make([]uint64, sw)
			for _, sid := range q {
				bs[sid>>6] |= 1 << (uint(sid) & 63)
			}
			out[gi] = bs
		}
	}
	if l.sadj != nil {
		// fold each cut cell's side rows into its region's lowest direction
		for b := int32(0); b < int32(n); b++ {
			if l.reg[b] == nil {
				continue
			}
			for d := int32(0); d < 4; d++ {
				lb := h.comp[b*4+d]
				if lb < 0 || int32(lb) == d {
					continue
				}
				dst := l.sadj[int(b*4+int32(lb))*h.gw:][:h.gw]
				for w, x := range l.sadj[int(b*4+d)*h.gw:][:h.gw] {
					dst[w] |= x
				}
			}
		}
	}
	return l, out
}

// row is the goal row of a box on c with the player on cell p (exp-681
// -fa2-hall-side: only the player's side region on a cut cell).
func (h *fa2Hall) row(l *hallLevel, c, p int32) []uint64 {
	if l.reg != nil && p >= 0 {
		if r := l.reg[c]; r != nil {
			if lb := r[p]; lb >= 0 {
				return l.sadj[int(c*4+int32(lb))*h.gw:][:h.gw]
			}
		}
	}
	return l.adj[int(c)*h.gw:][:h.gw]
}

func (h *fa2Hall) augment(l *hallLevel, cur []int32, i int32, m *hallM, p int32) bool {
	row := h.row(l, cur[i], p)
	for w, rw := range row {
		// recomputed after each recursion, which marks more goals visited
		for word := rw &^ h.vis[w]; word != 0; word = rw &^ h.vis[w] {
			b := uint(bits.TrailingZeros64(word))
			h.vis[w] |= 1 << b
			g := int32(w<<6) + int32(b)
			if j := m.mg[g]; j < 0 || h.augment(l, cur, j, m, p) {
				m.mg[g], m.mb[i] = i, g
				return true
			}
		}
	}
	return false
}

// solve turns m (any matching, possibly stale) into a maximum matching of
// the loose boxes of cur at level k and returns the deficit.
func (h *fa2Hall) solve(k int, cur []int32, m *hallM, p int32) int32 {
	l := h.level(k)
	loose := int32(0)
	for i, g := range m.mb {
		c := cur[i]
		isLoose := l.wall[c>>6]>>(uint(c)&63)&1 == 0
		if isLoose {
			loose++
		}
		if g >= 0 && (!isLoose || m.mg[g] != int32(i) || h.row(l, c, p)[g>>6]>>(uint(g)&63)&1 == 0) {
			if m.mg[g] == int32(i) {
				m.mg[g] = -1
			}
			m.mb[i] = -1
		}
	}
	for g, i := range m.mg {
		if i >= 0 && m.mb[i] != int32(g) {
			m.mg[g] = -1
		}
	}
	size := int32(0)
	for i, g := range m.mb {
		if g >= 0 {
			size++
			continue
		}
		c := cur[i]
		if l.wall[c>>6]>>(uint(c)&63)&1 != 0 {
			continue
		}
		clear(h.vis)
		if h.augment(l, cur, int32(i), m, p) {
			size++
		}
	}
	h.lastLoose = loose
	return loose - size
}

// hallPL is the expanded parent's data at one level: its maximum matching
// and, per moved box i (lazily), the maximum matching size s_i of the
// relation without box i and the goals F_i from which an alternating path
// reaches a free goal in that matching. A child moves box i to t, so its
// deficit is exact from these: loose' - (s_i + [t loose and row(t) meets F_i]).
type hallPL struct {
	st         uint32
	m          hallM
	loose, def int32
	bst        []uint32
	bs         []int32
	bf         []uint64
}

func (h *fa2Hall) walled(l *hallLevel, c int32) bool {
	return l.wall[c>>6]>>(uint(c)&63)&1 != 0
}

// psolve is solve on the parent, recorded at level k.
func (h *fa2Hall) psolve(k int, cur []int32, m *hallM) int32 {
	d := h.solve(k, cur, m, h.pp)
	pl := &h.pl[k]
	if pl.m.mb == nil {
		pl.m = hallM{mb: make([]int32, h.s.n), mg: make([]int32, h.ng)}
		pl.bst = make([]uint32, h.s.n)
		pl.bs = make([]int32, h.s.n)
		pl.bf = make([]uint64, h.s.n*h.gw)
	}
	pl.m.copyFrom(m)
	pl.loose, pl.def, pl.st = h.lastLoose, d, h.pst
	return d
}

// boxStruct fills s_i and F_i of level k for box i (parent cells h.pc).
func (h *fa2Hall) boxStruct(k int, i int) {
	pl, l, gw := &h.pl[k], h.level(k), h.gw
	m := &h.mw
	m.copyFrom(&pl.m)
	if g := m.mb[i]; g >= 0 {
		m.mg[g], m.mb[i] = -1, -1
	}
	size := int32(0)
	for _, g := range m.mb {
		if g >= 0 {
			size++
		}
	}
	if pl.def > 0 || pl.m.mb[i] < 0 {
		// free boxes other than i may now augment (Kuhn: one try each)
		for j, g := range m.mb {
			if g >= 0 || j == i || h.walled(l, h.pc[j]) {
				continue
			}
			clear(h.vis)
			if h.augment(l, h.pc, int32(j), m, h.pp) {
				size++
			}
		}
	}
	f := pl.bf[i*gw : i*gw+gw]
	for w := range f {
		f[w] = l.open[w]
	}
	for g, j := range m.mg {
		if j >= 0 {
			f[g>>6] &^= 1 << (uint(g) & 63)
		}
	}
	for changed := true; changed; {
		changed = false
		for j, g := range m.mb {
			if g < 0 || f[g>>6]>>(uint(g)&63)&1 != 0 {
				continue
			}
			row := h.row(l, h.pc[j], h.pp)
			for w, x := range row {
				if x&f[w] != 0 {
					f[g>>6] |= 1 << (uint(g) & 63)
					changed = true
					break
				}
			}
		}
	}
	pl.bs[i], pl.bst[i] = size, h.pst
}

// dAt is the child's deficit at level k (cur: child cells, box i moved from
// b). It may answer 1 for any positive deficit.
func (h *fa2Hall) dAt(k int, cur []int32, i int, b int32) int32 {
	pl := &h.pl[k]
	if h.sinkBit != nil {
		cm := h.sinkMask(cur)
		if cm != h.pmask {
			// the child changed the walled sinks: no parent fast path
			h.mask = cm
			if !h.mcReady {
				h.mc.reset()
				h.mcReady = true
			}
			d := h.solve(k, cur, &h.mc, h.cp)
			h.mask = h.pmask
			return d
		}
		h.mask = h.pmask
	}
	if h.parentOK && i >= 0 && pl.st != h.pst {
		// the parent at this level, on first use (warm from its credited level)
		h.mc.copyFrom(&h.m0)
		h.mcReady = false
		h.psolve(k, h.pc, &h.mc)
		h.lazy++
	}
	if h.parentOK && i >= 0 && pl.st == h.pst {
		if pl.def >= 2 {
			h.skipped++ // one box moved: the deficit changes by at most one
			return 1
		}
		if pl.bst[i] != h.pst {
			h.boxStruct(k, i)
		}
		l, gw := h.level(k), h.gw
		t := cur[i]
		loose, size := pl.loose, pl.bs[i]
		if !h.walled(l, b) {
			loose--
		}
		if !h.walled(l, t) {
			loose++
			f := pl.bf[i*gw : i*gw+gw]
			row := h.row(l, t, h.cp)
			for w, x := range row {
				if x&f[w] != 0 {
					size++
					break
				}
			}
		}
		return loose - size
	}
	if !h.mcReady {
		if h.parentOK {
			h.mc.copyFrom(&h.m0)
		} else {
			h.mc.reset()
		}
		h.mcReady = true
	}
	return h.solve(k, cur, &h.mc, h.cp)
}

// parent records the expanded node's matchings (s.occ holds cur, p its raw
// prefix) at every level its credit scan visits.
func (h *fa2Hall) parent(cur []int32, p int32, player int32) {
	h.parentOK = false
	h.pp = player
	if h.off {
		return
	}
	h.nodes++
	h.pst++
	if h.pst == 0 {
		for k := range h.pl {
			h.pl[k].st = 0
			clear(h.pl[k].bst)
		}
		h.pst = 1
	}
	copy(h.pc, cur)
	h.m0.reset()
	h.pmask = h.sinkMask(cur)
	h.mask = h.pmask
	p = min(max(p, 0), int32(len(h.permAt)-1))
	k := int(h.permAt[p])
	d := h.psolve(k, cur, &h.m0)
	if d > 0 {
		h.incons++
		h.defSum += int64(d)
	}
	for d > 0 && k > 0 {
		k--
		d = h.psolve(k, cur, &h.m0)
	}
	h.parentOK = true
}

// child returns the credited prefix of a child: cur
// holds its cells (box i moved from b; i < 0: no parent, e.g. the root),
// s.occ the child, p its raw prefix.
func (h *fa2Hall) child(cur []int32, p int32, i int, b int32, player int32) (cp int32) {
	if h.off {
		return p
	}
	h.cp = player
	h.mcReady = false
	p = min(max(p, 0), int32(len(h.permAt)-1))
	k := int(h.permAt[p])
	d := h.dAt(k, cur, i, b)
	for d > 0 && k > 0 {
		k--
		d = h.dAt(k, cur, i, b)
	}
	cp = p
	if k != int(h.permAt[p]) {
		cp = h.permPos[k]
		h.gated++
	}
	if d > 0 && (FA2HallSink || FA2HallSide) {
		// exp-681: inconsistent with no plan walls at all (only frozen sinks):
		// the child is dead under the lone-box relaxation; no credit.
		cp = 0
		h.dead0++
	}
	return cp
}
