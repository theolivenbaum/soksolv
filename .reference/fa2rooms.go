package main

import "math/bits"

// Incremental room-pair score for fa2 (exp-491, identical to festRoomsFull).
//
// exp-530: the score of a child is counted from the parent's broken pairs.
// Room k's evaluation (roomBrokenFX) only marks pairs (k, o), so the
// parent's broken-pair matrix is P = own | oth, where own(x) is room x's own
// row and oth(x) holds bit k when room k's evaluation broke (k, x). A child
// re-evaluates a set S of rooms (rows E'x); every pair with no end in S
// keeps its parent value, and a pair with an end x in S is broken iff
// oth(x) has it from a room outside S, E'x has it, or E'y (y in S) has x.
// A room's evaluation reads only the boxes on its own cells, the boxes on
// the corridor cells next to it and the corridor components next to it, so
// the rooms that can change are:
//   - box b -> t between room cells: rb and rt (the corridor labels are the
//     parent's); "rb without b" is the same row for every child of box b
//     (cached per box), "rt with a box on t" the same for every box outside
//     rt (cached per target cell);
//   - room cell b -> corridor cell t: rb, and the rooms touched by t's
//     parent component (only that component splits). Those rooms (other
//     than rb) depend on t alone and are cached per (t, room);
//   - corridor cell b -> room cell t: rt, and the rooms next to b or touched
//     by a component next to b (they merge through b). Those rooms (other
//     than rt) depend on b alone and are cached per box;
//   - corridor -> corridor: the rooms of both of the above, per child.
//
// Rooms evaluated on other corridor labels than the parent's use private
// labellings (the parent's stays valid for the next child).
type fa2Rooms struct {
	on      bool
	n, nw   int
	own     []uint64 // row x: the pairs room x's evaluation breaks on the parent
	par     []uint64 // the parent's broken pairs (n rows, symmetric)
	oth     []uint64 // row x: bit k when room k's evaluation breaks (k, x), k != x
	parBits int      // set bits of par
	full    int      // parent's score
	one     []uint64 // single-room evaluation scratch (only the evaluated row is read)
	sm      []uint64 // the re-evaluated rooms S (bitset)
	xs      []int    // S as a list
	ev      []uint64 // row j: E' of xs[j]
	nn      []uint64 // scratch row
	aff     []uint64 // scratch room set
	ns      uint32   // parent stamp

	alt  corrLab // private labels of the current corridor child
	altT int32   // room -> corridor: the target whose labels alt holds (if altS == ns)
	altS uint32

	bBox int32 // the box the per-box caches belong to (if bSt == ns)
	bSt  uint32
	bRow []uint64 // room rb without b (parent labels); empty until computed
	bRw  []uint64
	blab corrLab  // corridor box b lifted: labels
	bl   bool     // blab and ab are valid for bBox
	ab   []uint64 // rooms next to the lifted corridor box b or touched by its neighbours' components
	bcSt []uint32 // per room: stamp (bcS) of bc's row under blab
	bc   []uint64
	bcS  uint32

	tSt  []uint32 // per cell: stamp of tRow's row
	tRow []uint64 // room rt with a box on room cell t (parent labels)
	atSt []uint32 // per corridor cell t: stamp of at
	at   []uint64 // rooms touched by t's parent component
	tcSt []uint32 // per (cell, room): stamp of tc's row (nil: no cache)
	tc   []uint64 // room x under the labels with a box on corridor cell t

	art fa2Art // same-room children of box bBox
}

// fa2Art answers same-room children (b -> t inside room r) without a room
// evaluation. G is room r without b on the parent's labels: free cells and
// the corridor components next to them, joined as roomBrokenFX joins them;
// its connected components are r's fragments. A box on t only removes t from
// its fragment F: if t is not an articulation point of G, F minus t stays one
// fragment with all of F's components and loses only t's own adjacencies to
// other rooms (per-fragment counts), and every other fragment is unchanged.
// An articulation point falls back to the evaluation.
type fa2Art struct {
	ok        bool
	seen      uint32  // bcS of the box whose first same-room child was evaluated
	builds    []int32 // per room: builds, answers from builds, skipped builds
	fast      []int32
	skip      []int32
	room      int
	nc        int     // cells of the room (node i = local cell index)
	free      int     // free cells of G
	adjS, adj []int32 // CSR adjacency over nodes (cells, then components)
	el        [][2]int32
	disc, low []int32
	art       []bool
	frag      []int32 // per node: fragment (-1: occupied cell)
	stack     []artFrame
	nf        int
	ncell     []int32
	cnt       []int32  // fragment f, room o: free cells of f next to room o
	ct        []uint64 // fragment f: OR of its components' touch sets
	miss      []uint64 // fragment f: edges it does not touch
	missCnt   []int32  // room o: fragments missing o
	cnode     []int32  // label -> node (stamp cst)
	cst       []uint32
	cs        uint32
	nadj      []int32 // scratch: other rooms next to a cell
}

func (f *fa2) roomsSetup() {
	m := f.s.rooms
	r := &f.rm
	if !FA2Rooms || m == nil || len(m.rooms) < 2 {
		return
	}
	r.on = true
	n, nw, cells := len(m.rooms), m.nw, len(m.cell)
	r.n, r.nw = n, nw
	r.own, r.par, r.oth, r.one = make([]uint64, n*nw), make([]uint64, n*nw), make([]uint64, n*nw), make([]uint64, n*nw)
	r.sm, r.nn, r.aff, r.bRw, r.ab = make([]uint64, nw), make([]uint64, nw), make([]uint64, nw), make([]uint64, nw), make([]uint64, nw)
	r.xs, r.ev = make([]int, 0, n), make([]uint64, n*nw)
	r.alt.lab, r.alt.touch = make([]int32, cells), make([]uint64, len(m.cur.touch))
	r.blab.lab, r.blab.touch = make([]int32, cells), make([]uint64, len(m.cur.touch))
	r.bcSt, r.bc = make([]uint32, n), make([]uint64, n*nw)
	r.tSt, r.tRow = make([]uint32, cells), make([]uint64, cells*nw)
	r.atSt, r.at = make([]uint32, cells), make([]uint64, cells*nw)
	if cells*n*nw <= 1<<22 {
		r.tcSt, r.tc = make([]uint32, cells*n), make([]uint64, cells*n*nw)
	}
	if len(m.fxLost) != n*nw {
		m.fxLost = make([]uint64, n*nw)
	}
}

// evalRow is room k's row of roomBrokenFX on s.occ (corridor labels m.lc).
func (f *fa2) evalRow(k int, dst []uint64) {
	r := &f.rm
	row := r.one[k*r.nw : (k+1)*r.nw]
	clear(row)
	f.s.roomBrokenFX(k, r.one)
	copy(dst, row)
}

func hasBit(v []uint64, k int) bool { return v[k>>6]>>(uint(k)&63)&1 != 0 }
func setBit(v []uint64, k int)      { v[k>>6] |= 1 << (uint(k) & 63) }

// addRoom puts room x in S and returns its E' row (to be filled).
func (r *fa2Rooms) addRoom(x int) []uint64 {
	j := len(r.xs)
	r.xs = append(r.xs, x)
	setBit(r.sm, x)
	return r.ev[j*r.nw : (j+1)*r.nw]
}

// count is the child's score from the parent's pairs and the E' rows of S.
func (r *fa2Rooms) count() int {
	nw := r.nw
	total := r.parBits
	for j, x := range r.xs {
		nx := r.nn
		for w := range nw {
			nx[w] = r.oth[x*nw+w]&^r.sm[w] | r.ev[j*nw+w]
		}
		for i, y := range r.xs {
			if i != j && hasBit(r.ev[i*nw:(i+1)*nw], x) {
				setBit(nx, y)
			}
		}
		bx := r.par[x*nw : (x+1)*nw]
		for w := range nw {
			total += 2*(bits.OnesCount64(nx[w]&^r.sm[w])-bits.OnesCount64(bx[w]&^r.sm[w])) +
				bits.OnesCount64(nx[w]&r.sm[w]) - bits.OnesCount64(bx[w]&r.sm[w])
		}
	}
	return total / 2
}

// roomsParent labels the parent's corridors and records each room's marks.
func (f *fa2) roomsParent() int {
	r := &f.rm
	if !r.on {
		return 0
	}
	s := f.s
	nw := r.nw
	s.labelCorridorsFX(&s.rooms.cur)
	for k := range r.n {
		f.evalRow(k, r.own[k*nw:(k+1)*nw])
	}
	clear(r.oth)
	for k := range r.n {
		for w, v := range r.own[k*nw : (k+1)*nw] {
			for ; v != 0; v &= v - 1 {
				if o := w<<6 | bits.TrailingZeros64(v); o != k {
					setBit(r.oth[o*nw:(o+1)*nw], k)
				}
			}
		}
	}
	n := 0
	for i := range r.par {
		r.par[i] = r.own[i] | r.oth[i]
		n += bits.OnesCount64(r.par[i])
	}
	r.full = n / 2
	r.parBits = n
	r.ns++
	if r.ns == 0 {
		clear(r.tSt)
		clear(r.atSt)
		clear(r.tcSt)
		r.bSt, r.altS = 0, 0
		r.ns = 1
	}
	return r.full
}

// boxRows makes the per-box caches belong to box b.
func (r *fa2Rooms) boxRows(b int32) {
	if r.bSt == r.ns && r.bBox == b {
		return
	}
	r.bSt, r.bBox = r.ns, b
	r.bRow = r.bRw[:0]
	r.bl = false
	r.art.ok = false
	r.bcS++
	if r.bcS == 0 {
		clear(r.bcSt)
		r.bcS = 1
	}
}

// touchOf ORs into dst the room of cell c, or the rooms touched by the
// component of corridor cell c under labels lc.
func (f *fa2) touchOf(dst []uint64, lc *corrLab, c int32) {
	m := f.s.rooms
	if k := m.cell[c]; k >= 0 {
		setBit(dst, k)
		return
	}
	if f.s.lv.Wall[c] {
		return
	}
	if l := lc.lab[c]; l >= 0 {
		for i, w := range lc.touch[int(l)*f.rm.nw : int(l+1)*f.rm.nw] {
			dst[i] |= w
		}
	}
}

// roomsChild is festRoomsFull for the child (s.occ) of moving a box b -> t.
func (f *fa2) roomsChild(b, t int32) int {
	r := &f.rm
	if !r.on {
		return 0
	}
	if b == t {
		return r.full
	}
	s := f.s
	m := s.rooms
	nw := r.nw
	rb, rt := m.cell[b], m.cell[t]
	clear(r.sm)
	r.xs = r.xs[:0]
	r.boxRows(b)
	// withoutB: room rb without b, on the parent's labels.
	withoutB := func(dst []uint64) {
		if len(r.bRow) == 0 {
			r.bRow = r.bRw[:nw]
			f.evalRow(rb, r.bRow)
		}
		copy(dst, r.bRow)
	}
	// withT: room rt with a box on t, on the parent's labels.
	withT := func(dst []uint64) {
		tr := r.tRow[int(t)*nw : (int(t)+1)*nw]
		if r.tSt[t] != r.ns {
			f.evalRow(rt, tr)
			r.tSt[t] = r.ns
		}
		copy(dst, tr)
	}
	switch {
	case rb >= 0 && rt >= 0:
		if rb == rt {
			f.sameRoom(rb, b, t, r.addRoom(rb))
			break
		}
		withoutB(r.addRoom(rb))
		withT(r.addRoom(rt))
	case rb >= 0: // room -> corridor
		at := r.at[int(t)*nw : (int(t)+1)*nw]
		if r.atSt[t] != r.ns {
			clear(at)
			f.touchOf(at, &m.cur, t)
			r.atSt[t] = r.ns
		}
		labT := func() {
			if r.altS != r.ns || r.altT != t {
				s.labelCorridorsFX(&r.alt)
				r.altS, r.altT = r.ns, t
			}
			m.lc = &r.alt
		}
		if hasBit(at, rb) {
			labT()
			f.evalRow(rb, r.addRoom(rb))
		} else {
			withoutB(r.addRoom(rb))
		}
		for w, v := range at {
			for ; v != 0; v &= v - 1 {
				x := w<<6 | bits.TrailingZeros64(v)
				if x == rb {
					continue
				}
				dst := r.addRoom(x)
				if r.tcSt == nil {
					labT()
					f.evalRow(x, dst)
					continue
				}
				i := int(t)*r.n + x
				row := r.tc[i*nw : (i+1)*nw]
				if r.tcSt[i] != r.ns {
					labT()
					f.evalRow(x, row)
					r.tcSt[i] = r.ns
				}
				copy(dst, row)
			}
		}
	case rt >= 0: // corridor -> room
		if !r.bl {
			clear(r.ab)
			for _, d := range s.dirs {
				f.touchOf(r.ab, &m.cur, b+d)
			}
			s.labelCorridorsFX(&r.blab)
			r.bl = true
		}
		if hasBit(r.ab, rt) {
			m.lc = &r.blab
			f.evalRow(rt, r.addRoom(rt))
		} else {
			withT(r.addRoom(rt))
		}
		for w, v := range r.ab {
			for ; v != 0; v &= v - 1 {
				x := w<<6 | bits.TrailingZeros64(v)
				if x == rt {
					continue
				}
				dst := r.addRoom(x)
				row := r.bc[x*nw : (x+1)*nw]
				if r.bcSt[x] != r.bcS {
					m.lc = &r.blab
					f.evalRow(x, row)
					r.bcSt[x] = r.bcS
				}
				copy(dst, row)
			}
		}
	default: // corridor -> corridor
		clear(r.aff)
		f.touchOf(r.aff, &m.cur, t)
		for _, d := range s.dirs {
			f.touchOf(r.aff, &m.cur, b+d)
		}
		s.labelCorridorsFX(&r.alt)
		r.altS = 0
		for w, v := range r.aff {
			for ; v != 0; v &= v - 1 {
				x := w<<6 | bits.TrailingZeros64(v)
				f.evalRow(x, r.addRoom(x))
			}
		}
	}
	m.lc = &m.cur
	return r.count()
}

// cellRooms lists the other rooms next to room r's cell i (deduplicated).
func (f *fa2) cellRooms(base, i int, out []int32) []int32 {
	m := f.s.rooms
	out = out[:0]
	for e := m.nbStart[base+i]; e < m.nbStart[base+i+1]; e++ {
		if kd := m.nbKind[e]; kd <= -2 {
			o := -2 - kd
			found := false
			for _, x := range out {
				found = found || x == o
			}
			if !found {
				out = append(out, o)
			}
		}
	}
	return out
}

// buildArt computes fa2Art for room r on s.occ with t lifted (b is lifted).
func (f *fa2) buildArt(r int) {
	s, m, R := f.s, f.s.rooms, &f.rm
	a := &R.art
	n, nw := R.n, R.nw
	cells := m.rooms[r]
	base := m.roomBase[r]
	lc := &m.cur
	nc := len(cells)
	a.ok, a.room, a.nc, a.free = true, r, nc, 0
	if len(a.cst) < len(lc.touch)/max(nw, 1)+1 {
		a.cst, a.cnode = make([]uint32, len(lc.touch)/max(nw, 1)+1), make([]int32, len(lc.touch)/max(nw, 1)+1)
	}
	a.cs++
	if a.cs == 0 {
		clear(a.cst)
		a.cs = 1
	}
	nodes := nc
	a.el = a.el[:0]
	var compLab []int32
	for i, c := range cells {
		if s.occ[c] {
			continue
		}
		a.free++
		for e := m.nbStart[base+i]; e < m.nbStart[base+i+1]; e++ {
			kd := m.nbKind[e]
			if kd <= -2 {
				continue
			}
			y := m.nbCell[e]
			if s.occ[y] {
				continue
			}
			if kd >= 0 {
				a.el = append(a.el, [2]int32{int32(i), kd})
				continue
			}
			k := lc.lab[y]
			if a.cst[k] != a.cs {
				a.cst[k] = a.cs
				a.cnode[k] = int32(nodes)
				compLab = append(compLab, k)
				nodes++
			}
			a.el = append(a.el, [2]int32{int32(i), a.cnode[k]}, [2]int32{a.cnode[k], int32(i)})
		}
	}
	grow := func(v []int32, k int) []int32 {
		if cap(v) < k {
			return make([]int32, k)
		}
		return v[:k]
	}
	a.adjS = grow(a.adjS, nodes+1)
	clear(a.adjS)
	for _, e := range a.el {
		a.adjS[e[0]+1]++
	}
	for i := 1; i <= nodes; i++ {
		a.adjS[i] += a.adjS[i-1]
	}
	a.adj = grow(a.adj, len(a.el))
	pos := grow(a.disc, nodes) // scratch, overwritten below
	copy(pos, a.adjS[:nodes])
	for _, e := range a.el {
		a.adj[pos[e[0]]] = e[1]
		pos[e[0]]++
	}
	a.disc, a.low, a.frag = grow(pos, nodes), grow(a.low, nodes), grow(a.frag, nodes)
	if cap(a.art) < nodes {
		a.art = make([]bool, nodes)
	}
	a.art = a.art[:nodes]
	clear(a.art)
	for i := range nodes {
		a.disc[i], a.frag[i] = 0, -1
	}
	// Iterative Tarjan over the free cells (components are reached from them).
	timer := int32(0)
	a.nf = 0
	for root := range nc {
		if s.occ[cells[root]] || a.disc[root] != 0 {
			continue
		}
		fid := int32(a.nf)
		a.nf++
		timer++
		a.disc[root], a.low[root], a.frag[root] = timer, timer, fid
		rootKids := 0
		a.stack = append(a.stack[:0], artFrame{int32(root), -1, 0})
		for len(a.stack) > 0 {
			fr := &a.stack[len(a.stack)-1]
			c := fr.c
			if e := a.adjS[c] + int32(fr.next); e < a.adjS[c+1] {
				fr.next++
				y := a.adj[e]
				if a.disc[y] == 0 {
					timer++
					a.disc[y], a.low[y], a.frag[y] = timer, timer, fid
					if c == int32(root) {
						rootKids++
					}
					a.stack = append(a.stack, artFrame{y, c, 0})
				} else if y != fr.parent {
					a.low[c] = min(a.low[c], a.disc[y])
				}
				continue
			}
			p := fr.parent
			a.stack = a.stack[:len(a.stack)-1]
			if p < 0 {
				continue
			}
			a.low[p] = min(a.low[p], a.low[c])
			if p != int32(root) && a.low[c] >= a.disc[p] {
				a.art[p] = true
			}
		}
		if rootKids >= 2 {
			a.art[root] = true
		}
	}
	nf := a.nf
	a.ncell = grow(a.ncell, nf)
	clear(a.ncell)
	a.cnt = grow(a.cnt, nf*n)
	clear(a.cnt)
	if cap(a.ct) < nf*nw {
		a.ct, a.miss = make([]uint64, nf*nw), make([]uint64, nf*nw)
	}
	a.ct, a.miss = a.ct[:nf*nw], a.miss[:nf*nw]
	clear(a.ct)
	for i, c := range cells {
		if s.occ[c] {
			continue
		}
		fi := int(a.frag[i])
		a.ncell[fi]++
		a.nadj = f.cellRooms(base, i, a.nadj)
		for _, o := range a.nadj {
			a.cnt[fi*n+int(o)]++
		}
	}
	for _, k := range compLab {
		fi := int(a.frag[a.cnode[k]])
		for w, v := range lc.touch[int(k)*nw : int(k+1)*nw] {
			a.ct[fi*nw+w] |= v
		}
	}
	a.missCnt = grow(a.missCnt, n)
	clear(a.missCnt)
	edges := m.edgeBits[r*nw : (r+1)*nw]
	for fi := range nf {
		for w, e := range edges {
			v := e &^ a.ct[fi*nw+w]
			for x := e; x != 0; x &= x - 1 {
				o := w<<6 | bits.TrailingZeros64(x)
				if a.cnt[fi*n+o] > 0 {
					v &^= 1 << (uint(o) & 63)
				}
			}
			a.miss[fi*nw+w] = v
			for ; v != 0; v &= v - 1 {
				a.missCnt[w<<6|bits.TrailingZeros64(v)]++
			}
		}
	}
}

// sameRoom fills dst with room r's row for the child b -> t inside r.
func (f *fa2) sameRoom(r int, b, t int32, dst []uint64) {
	s, m, R := f.s, f.s.rooms, &f.rm
	a := &R.art
	if !a.ok || a.room != r {
		// Build on the box's second same-room child: a lone one is cheaper
		// to evaluate. Rooms whose builds have not paid (fewer than two
		// answers per build) are evaluated, with one build in 32 as a probe.
		if a.seen != R.bcS {
			a.seen = R.bcS
			f.evalRow(r, dst)
			return
		}
		if len(a.builds) != R.n {
			a.builds, a.fast, a.skip = make([]int32, R.n), make([]int32, R.n), make([]int32, R.n)
		}
		if a.builds[r] >= 8 && a.fast[r] < 2*a.builds[r] {
			if a.skip[r]++; a.skip[r]%32 != 0 {
				f.evalRow(r, dst)
				return
			}
		}
		a.builds[r]++
		s.occ[t] = false
		f.buildArt(r)
		s.occ[t] = true
	}
	ti := int(m.loc[t])
	fi := int(a.frag[ti])
	if fi < 0 || a.art[ti] && a.ncell[fi] > 1 {
		f.evalRow(r, dst)
		return
	}
	a.fast[r]++
	n, nw := R.n, R.nw
	edges := m.edgeBits[r*nw : (r+1)*nw]
	if a.free == 1 {
		copy(dst, edges)
		return
	}
	a.nadj = f.cellRooms(m.roomBase[r], ti, a.nadj)
	for w, e := range edges {
		v := uint64(0)
		for x := e; x != 0; x &= x - 1 {
			o := w<<6 | bits.TrailingZeros64(x)
			bit := uint64(1) << (uint(o) & 63)
			own := a.miss[fi*nw+w]&bit != 0
			others := a.missCnt[o]
			if own {
				others--
			}
			if others > 0 {
				v |= bit
				continue
			}
			if a.ncell[fi] == 1 || a.ct[fi*nw+w]&bit != 0 {
				continue // the fragment vanishes, or a component touches o
			}
			c := a.cnt[fi*n+o]
			for _, x := range a.nadj {
				if int(x) == o {
					c--
				}
			}
			if c <= 0 {
				v |= bit
			}
		}
		dst[w] = v
	}
}
