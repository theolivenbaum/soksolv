package main

import "math/bits"

type featureCell struct{ packed, connectivity, roomConnectivity, oop int }

// roomMap is the static decomposition used by FESS. Rooms are connected
// unions of empty 2x3 (or 3x2) rectangles, ignoring boxes. A cell shared only
// at a corner by two rooms is left in the corridor, as in the paper.
// nbEdge is one neighbour of a room cell: its board cell and kind (same-room local index, -1 corridor, -2-room).
type nbEdge struct{ cell, kind int32 }

type roomMap struct {
	share       []int
	parentTotal int
	cell        []int
	rooms       [][]int32
	edges       [][]bool
	seen        []uint32
	stamp       uint32
	queue       []int32
	covered     []uint32
	touched     []uint32
	coverStamp  uint32
	touchStamp  uint32

	// exp-394: corridor components are labelled once
	// per board and shared by all rooms, so no room re-floods the corridors.
	nw       int      // words per room bitset
	edgeBits []uint64 // room r's direct neighbours, bit-packed (edges)
	corr     []int32  // corridor cells (cell < 0, not wall)
	loc      []int32  // index of a room cell inside its room's cell list
	cur, par corrLab  // corridor labelling of the evaluated board / of the opened node
	lc       *corrLab // labelling roomBrokenFast reads
	pbuf     []uint64 // affected-room bitset scratch
	undo     []int32
	roomBase []int // index of a room's first cell in nbStart
	nbStart  []int32
	nbCell   []int32
	nbKind   []int32
	// Fast-path copy of the neighbour lists (exp-638): intra-room edges to a
	// later local index are dropped (the union is done from the later cell),
	// and cell/kind share one record.
	fbStart   []int32
	fbEdge    []nbEdge
	compRep   []int32
	compRepSt []uint32
	repStamp  uint32
	dsu       []int32
	fragTouch []uint64
	// exp-659 -lazy-child (at most 64 rooms): the opened node's fragments,
	// per room cell (global index roomBase[r]+i) its fragment's root local
	// index and, at the root's index, the fragment's touch row.
	in4      []bool // room cell whose non-wall 4-neighbours are all in its room
	fragRoot []int32
	fragRow  []uint64
	fxLost   []uint64 // exp-483/491 festRoomsFull scratch
}

func (s *solver) computeRooms() {
	lv := s.lv
	m := &roomMap{cell: make([]int, len(lv.Wall)), seen: make([]uint32, len(lv.Wall))}
	for i := range m.cell {
		m.cell[i] = -1
	}
	parent := make([]int, len(lv.Wall))
	for i := range parent {
		parent[i] = -1
	}
	for y := 0; y+1 < lv.H; y++ {
		for x := 0; x+1 < lv.W; x++ {
			c := y*lv.W + x
			if !lv.Wall[c] && !lv.Wall[c+1] && !lv.Wall[c+lv.W] && !lv.Wall[c+lv.W+1] {
				parent[c] = c
			}
		}
	}
	var root func(int) int
	root = func(c int) int {
		if parent[c] != c {
			parent[c] = root(parent[c])
		}
		return parent[c]
	}
	for c, p := range parent {
		if p >= 0 {
			for _, d := range []int{1, lv.W} {
				if c+d < len(parent) && parent[c+d] >= 0 {
					parent[root(c+d)] = root(c)
				}
			}
		}
	}
	count := make([]int, len(parent))
	for c, p := range parent {
		if p >= 0 {
			count[root(c)]++
		}
	}
	ids := map[int]int{}
	for c, p := range parent {
		if p >= 0 && count[root(c)] > 1 {
			r := root(c)
			id, ok := ids[r]
			if !ok {
				id = len(ids)
				ids[r] = id
				m.rooms = append(m.rooms, nil)
			}
			for _, d := range []int{0, 1, lv.W, lv.W + 1} {
				x := c + d
				if m.cell[x] == -1 {
					m.cell[x] = id
				} else if m.cell[x] != id {
					m.cell[x] = -2
				}
			}
		}
	}
	for c, r := range m.cell {
		if r >= 0 {
			m.rooms[r] = append(m.rooms[r], int32(c))
		}
	}
	m.edges = make([][]bool, len(m.rooms))
	for r := range m.rooms {
		m.edges[r] = make([]bool, len(m.rooms))
	}
	s.rooms = m
	m.covered = make([]uint32, len(lv.Wall))
	m.touched = make([]uint32, len(m.rooms))
	m.initFast(lv.Wall, s.dirs)
	// With no boxes, find direct room paths without passing through a third room.
	for r, cells := range m.rooms {
		if len(cells) > 0 {
			m.walk(s, r, cells[0], func(other int) { m.edges[r][other] = true })
		}
	}
	m.packEdges()
}

// corrLab labels the connected components of free corridor cells.
type corrLab struct {
	lab   []int32  // component of a free corridor cell
	touch []uint64 // per component: rooms with a free cell adjacent to it
	k     int      // number of components
}

func (m *roomMap) initFast(wall []bool, dirs [4]int32) {
	n := len(m.rooms)
	m.nw = (n + 63) / 64
	m.edgeBits = make([]uint64, n*m.nw)
	maxc := 0
	m.loc = make([]int32, len(m.cell))
	m.cur.lab = make([]int32, len(m.cell))
	m.par.lab = make([]int32, len(m.cell))
	m.lc = &m.cur
	for _, cells := range m.rooms {
		maxc = max(maxc, len(cells))
		for i, c := range cells {
			m.loc[c] = int32(i)
		}
	}
	for c, r := range m.cell {
		if r < 0 && !wall[c] {
			m.corr = append(m.corr, int32(c))
		}
	}
	m.cur.touch = make([]uint64, (len(m.corr)+16)*m.nw)
	m.par.touch = make([]uint64, (len(m.corr)+16)*m.nw)
	m.compRep = make([]int32, len(m.corr)+16)
	m.compRepSt = make([]uint32, len(m.corr)+16)
	m.pbuf = make([]uint64, m.nw)
	// Static neighbour lists of room cells (walls dropped): same-room local
	// index, -1 for a corridor cell, or -2-room for a cell of another room.
	for r, cells := range m.rooms {
		m.roomBase = append(m.roomBase, len(m.nbStart))
		for _, c := range cells {
			m.nbStart = append(m.nbStart, int32(len(m.nbCell)))
			for _, d := range dirs {
				y := c + d
				if wall[y] {
					continue
				}
				kd := int32(-1)
				if cy := m.cell[y]; cy == r {
					kd = m.loc[y]
				} else if cy >= 0 {
					kd = int32(-2 - cy)
				}
				m.nbCell = append(m.nbCell, y)
				m.nbKind = append(m.nbKind, kd)
			}
		}
	}
	m.nbStart = append(m.nbStart, int32(len(m.nbCell)))
	for r := range m.rooms {
		for i := range m.rooms[r] {
			g := m.roomBase[r] + i
			m.fbStart = append(m.fbStart, int32(len(m.fbEdge)))
			for e := m.nbStart[g]; e < m.nbStart[g+1]; e++ {
				if kd := m.nbKind[e]; kd < 0 || kd < int32(i) {
					m.fbEdge = append(m.fbEdge, nbEdge{m.nbCell[e], kd})
				}
			}
		}
	}
	m.fbStart = append(m.fbStart, int32(len(m.fbEdge)))
	m.dsu = make([]int32, maxc)
	m.fragTouch = make([]uint64, maxc*m.nw)
	m.in4 = make([]bool, len(m.cell))
	for c, r := range m.cell {
		if r < 0 {
			continue
		}
		m.in4[c] = true
		for _, d := range dirs {
			if y := int32(c) + d; !wall[y] && m.cell[y] != r {
				m.in4[c] = false
			}
		}
	}
	m.fragRoot = make([]int32, len(m.nbStart)-1)
	m.fragRow = make([]uint64, len(m.nbStart)-1)
}

// fastEdges packs edges once they are known (after the initial walks).
func (m *roomMap) packEdges() {
	for r, row := range m.edges {
		for o, e := range row {
			if e {
				m.edgeBits[r*m.nw+o>>6] |= 1 << (o & 63)
			}
		}
	}
}

// labelCorridors labels the free corridor cells of the current board into lc
// and records, per component, the rooms owning a free cell next to it. It
// makes lc the labelling roomBrokenFast reads.
func (s *solver) labelCorridors(lc *corrLab) { s.labelCorridorsT(lc, false) }

// labelCorridorsT with occT also records occupied room cells next to a
// component (PFRoomsOcc, Festival's rooms_bfs link rule).
func (s *solver) labelCorridorsT(lc *corrLab, occT bool) {
	m := s.rooms
	m.lc = lc
	nw := m.nw
	for _, c := range m.corr {
		lc.lab[c] = -1
	}
	k := 0
	st := m.queue[:0]
	for _, c := range m.corr {
		if s.occ[c] || lc.lab[c] >= 0 {
			continue
		}
		tb := lc.touch[k*nw : (k+1)*nw]
		clear(tb)
		lc.lab[c] = int32(k)
		st = append(st[:0], c)
		for len(st) > 0 {
			x := st[len(st)-1]
			st = st[:len(st)-1]
			for _, d := range s.dirs {
				y := x + d
				if s.lv.Wall[y] {
					continue
				}
				if cy := m.cell[y]; cy >= 0 {
					if occT || !s.occ[y] {
						tb[cy>>6] |= 1 << (cy & 63)
					}
				} else if !s.occ[y] && lc.lab[y] < 0 {
					lc.lab[y] = int32(k)
					st = append(st, y)
				}
			}
		}
		k++
	}
	lc.k = k
	m.queue = st
}

func (m *roomMap) find(x int32) int32 {
	for m.dsu[x] != x {
		m.dsu[x] = m.dsu[m.dsu[x]]
		x = m.dsu[x]
	}
	return x
}

// roomBrokenFast equals roomBroken(r, nil) given the corridor labelling m.lc
// of the current board: the fragments of room r are the components of its
// free cells joined by adjacency and by shared corridor components. One pass:
// each free cell unions with earlier same-room neighbours and with the first
// cell seen on each corridor component, and fragments merge their touch rows.
func (s *solver) roomBrokenFast(r int) int {
	m := s.rooms
	nw := m.nw
	if nw == 1 {
		return s.roomBrokenFast1(r)
	}
	return s.roomBrokenFastN(r)
}

// roomBrokenFastN is roomBrokenFast for any number of rooms.
func (s *solver) roomBrokenFastN(r int) int {
	m := s.rooms
	nw := m.nw
	cells := m.rooms[r]
	base := m.roomBase[r]
	edges := m.edgeBits[r*nw : (r+1)*nw]
	lc := m.lc
	dsu, rows := m.dsu, m.fragTouch
	m.repStamp++
	if m.repStamp == 0 {
		clear(m.compRepSt)
		m.repStamp = 1
	}
	rs := m.repStamp
	free := 0
	for i, c := range cells {
		if s.occ[c] {
			continue
		}
		free++
		ii := int32(i)
		dsu[i] = ii
		clear(rows[i*nw : (i+1)*nw])
		for _, nb := range m.fbEdge[m.fbStart[base+i]:m.fbStart[base+i+1]] {
			y := nb.cell
			if s.occ[y] {
				continue
			}
			switch kd := nb.kind; {
			case kd >= 0:
				if kd < ii {
					m.union(ii, kd)
				}
			case kd == -1:
				k := lc.lab[y]
				ri := int(m.find(ii)) * nw
				for w, v := range lc.touch[int(k)*nw : int(k+1)*nw] {
					rows[ri+w] |= v
				}
				if m.compRepSt[k] == rs {
					m.union(ii, m.compRep[k])
				} else {
					m.compRepSt[k] = rs
					m.compRep[k] = ii
				}
			default:
				o := int(-2 - kd)
				rows[int(m.find(ii))*nw+o>>6] |= 1 << (o & 63)
			}
		}
	}
	broken := 0
	if free == 0 {
		for _, w := range edges {
			broken += bits64(w)
		}
		return broken
	}
	for i, c := range cells {
		if s.occ[c] || dsu[i] != int32(i) {
			continue
		}
		tb := rows[i*nw : (i+1)*nw]
		for w, e := range edges {
			broken += bits64(e &^ tb[w])
		}
	}
	return broken
}

// roomBrokenFast1 is roomBrokenFast for at most 64 rooms (one word per touch
// row): scalar rows and inlined find/union (exp-649, identical result).
func (s *solver) roomBrokenFast1(r int) int {
	broken, _ := s.roomFrag1(r, false)
	return broken
}

// roomFrag1 also returns the union over room r's empty fragments of the edges
// the fragment cannot reach (all ones for a fully occupied room). With occT an
// occupied cell of another room next to a fragment still links it (PFRoomsOcc).
func (s *solver) roomFrag1(r int, occT bool) (int, uint64) {
	m := s.rooms
	cells := m.rooms[r]
	base := m.roomBase[r]
	edge := m.edgeBits[r]
	lab, touch := m.lc.lab, m.lc.touch
	dsu, rows := m.dsu[:len(cells)], m.fragTouch[:len(cells)]
	occ := s.occ
	fbStart := m.fbStart[base : base+len(cells)+1]
	m.repStamp++
	if m.repStamp == 0 {
		clear(m.compRepSt)
		m.repStamp = 1
	}
	rs := m.repStamp
	compRep, compRepSt := m.compRep, m.compRepSt
	free := 0
	for i, c := range cells {
		if occ[c] {
			continue
		}
		free++
		ii := int32(i)
		dsu[i] = ii
		rows[i] = 0
		for _, nb := range m.fbEdge[fbStart[i]:fbStart[i+1]] {
			y := nb.cell
			kd := nb.kind
			if occ[y] {
				if occT && kd < -1 {
					ri := ii
					for dsu[ri] != ri {
						dsu[ri] = dsu[dsu[ri]]
						ri = dsu[ri]
					}
					rows[ri] |= 1 << uint(-2-kd)
				}
				continue
			}
			if kd >= 0 {
				if kd < ii {
					ra, rb := ii, kd
					for dsu[ra] != ra {
						dsu[ra] = dsu[dsu[ra]]
						ra = dsu[ra]
					}
					for dsu[rb] != rb {
						dsu[rb] = dsu[dsu[rb]]
						rb = dsu[rb]
					}
					if ra != rb {
						dsu[ra] = rb
						rows[rb] |= rows[ra]
					}
				}
			} else if kd == -1 {
				k := lab[y]
				ri := ii
				for dsu[ri] != ri {
					dsu[ri] = dsu[dsu[ri]]
					ri = dsu[ri]
				}
				rows[ri] |= touch[k]
				if compRepSt[k] == rs {
					ra, rb := ii, compRep[k]
					for dsu[ra] != ra {
						dsu[ra] = dsu[dsu[ra]]
						ra = dsu[ra]
					}
					for dsu[rb] != rb {
						dsu[rb] = dsu[dsu[rb]]
						rb = dsu[rb]
					}
					if ra != rb {
						dsu[ra] = rb
						rows[rb] |= rows[ra]
					}
				} else {
					compRepSt[k] = rs
					compRep[k] = ii
				}
			} else {
				ri := ii
				for dsu[ri] != ri {
					dsu[ri] = dsu[dsu[ri]]
					ri = dsu[ri]
				}
				rows[ri] |= 1 << uint(-2-kd)
			}
		}
	}
	if free == 0 {
		return bits64(edge), ^uint64(0)
	}
	broken := 0
	var lost uint64
	for i, c := range cells {
		if !occ[c] && dsu[i] == int32(i) {
			l := edge &^ rows[i]
			broken += bits64(l)
			lost |= l
		}
	}
	return broken, lost
}

// union joins the fragments of local cells a and b, merging touch rows.
func (m *roomMap) union(a, b int32) {
	ra, rb := m.find(a), m.find(b)
	if ra == rb {
		return
	}
	m.dsu[ra] = rb
	nw := m.nw
	dst, src := m.fragTouch[int(rb)*nw:int(rb+1)*nw], m.fragTouch[int(ra)*nw:int(ra+1)*nw]
	for w, v := range src {
		dst[w] |= v
	}
}

func bits64(w uint64) int { return bits.OnesCount64(w) }

func (m *roomMap) walk(s *solver, room int, start int32, touch func(int)) {
	m.stamp++
	if m.stamp == 0 {
		clear(m.seen)
		m.stamp = 1
	}
	st := m.stamp
	q := append(m.queue[:0], start)
	m.seen[start] = st
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			x := q[i] + d
			if PFRoomsOcc && !s.lv.Wall[x] && s.occ[x] {
				// exp-696: Festival's rooms_bfs marks a neighbouring room's
				// cell seen before testing occupancy, so a box there still
				// links the rooms.
				if r := m.cell[x]; r >= 0 && r != room {
					touch(r)
				}
				continue
			}
			if s.lv.Wall[x] || s.occ[x] || m.seen[x] == st {
				continue
			}
			m.seen[x] = st
			if r := m.cell[x]; r >= 0 && r != room {
				touch(r)
				continue
			}
			q = append(q, x)
		}
	}
	m.queue = q
}

// roomConnectivity counts broken directed edges from each empty fragment of
// a room. All fragments matter: one connected corner cannot hide an isolated
// half of the same room. A completely occupied room has all its edges broken.
func (s *solver) roomConnectivity() int {
	m := s.rooms
	if m == nil || len(m.rooms) < 2 {
		return 0
	}
	s.labelCorridors(&m.cur)
	broken := 0
	for r := range m.rooms {
		broken += s.roomBrokenFast(r)
	}
	return broken
}

// roomConnParent computes roomConnectivity for an opened node (s.occ holds
// it) and remembers its corridor labelling and each room's share, so that
// roomConnChild can update it for a one-box move (exp-201, exp-394).
func (s *solver) roomConnParent() int {
	m := s.rooms
	if m == nil || len(m.rooms) < 2 {
		return 0
	}
	if len(m.share) != len(m.rooms) {
		m.share = make([]int, len(m.rooms))
	}
	s.labelCorridors(&m.par)
	total := 0
	keep := LazyChild && m.nw == 1
	for r := range m.rooms {
		m.share[r] = s.roomBrokenFast(r)
		total += m.share[r]
		if keep {
			m.keepFrags(s.occ, r)
		}
	}
	m.parentTotal = total
	return total
}

func (m *roomMap) bytes() int {
	n := cap(m.cell)*8 + cap(m.seen)*4 + cap(m.queue)*4 + cap(m.covered)*4 + cap(m.touched)*4 + cap(m.rooms)*24 + cap(m.edges)*24
	for _, r := range m.rooms {
		n += cap(r) * 4
	}
	for _, r := range m.edges {
		n += cap(r)
	}
	return n
}

// PFFestRooms (exp-285): Festival-ordered pull scores rooms as Festival's
// score_rooms: room pairs adjacent on the empty board that some empty
// fragment of either room no longer reaches, each pair counted once.
var PFFestRooms = true // exp-619 promoted 2026-11-11

// PFRoomsOcc (exp-696, festival-derived): festRooms follows Festival's
// rooms_bfs link rule: a box on the neighbouring room's boundary cell does not
// break the link (the cell is marked seen before the occupancy test).
var PFRoomsOcc = true // exp-696 promoted 2026-11-12 (default on)

func (s *solver) festRooms() int {
	m := s.rooms
	if m == nil || len(m.rooms) < 2 {
		return 0
	}
	if m.nw == 1 {
		return s.festRooms1()
	}
	return s.festRoomsFlood()
}

// festRoomsFlood is festRooms with one flood per empty fragment.
func (s *solver) festRoomsFlood() int {
	m := s.rooms
	n := len(m.rooms)
	if len(s.frLost) != n*n {
		s.frLost = make([]bool, n*n)
	}
	lost := s.frLost
	clear(lost)
	m.coverStamp++
	if m.coverStamp == 0 {
		clear(m.covered)
		m.coverStamp = 1
	}
	cs := m.coverStamp
	for r, cells := range m.rooms {
		any := false
		for _, c := range cells {
			if s.occ[c] || m.covered[c] == cs {
				continue
			}
			any = true
			m.touchStamp++
			if m.touchStamp == 0 {
				clear(m.touched)
				m.touchStamp = 1
			}
			ts := m.touchStamp
			m.walk(s, r, c, func(other int) { m.touched[other] = ts })
			for _, x := range cells {
				if m.seen[x] == m.stamp {
					m.covered[x] = cs
				}
			}
			for o, edge := range m.edges[r] {
				if edge && m.touched[o] != ts {
					lost[r*n+o] = true
				}
			}
		}
		if !any {
			for o := range n {
				lost[r*n+o] = true
			}
		}
	}
	k := 0
	for r := range n {
		for o := r + 1; o < n; o++ {
			if (m.edges[r][o] || m.edges[o][r]) && (lost[r*n+o] || lost[o*n+r]) {
				k++
			}
		}
	}
	return k
}

// festRooms1 is festRooms for at most 64 rooms: the fragments and what each
// reaches come from the union-find pass of roomFrag1 instead of a flood per
// fragment (exp-649, identical count). lost[r] has bit o set when some empty
// fragment of r has an edge to o it cannot reach.
func (s *solver) festRooms1() int {
	m := s.rooms
	n := len(m.rooms)
	if cap(s.frLost1) < n {
		s.frLost1 = make([]uint64, n)
	}
	lost := s.frLost1[:n]
	saved := m.lc
	s.labelCorridorsT(&m.cur, PFRoomsOcc)
	for r := range n {
		_, lost[r] = s.roomFrag1(r, PFRoomsOcc)
	}
	m.lc = saved
	k := 0
	for r := range n {
		for o := r + 1; o < n; o++ {
			if (m.edgeBits[r]>>uint(o)&1 != 0 || m.edgeBits[o]>>uint(r)&1 != 0) && (lost[r]>>uint(o)&1 != 0 || lost[o]>>uint(r)&1 != 0) {
				k++
			}
		}
	}
	return k
}

// roomConnChild is roomConnectivity after the box on b moved to t (s.occ
// holds the child), given roomConnParent for the parent. The child's
// corridor labelling is the parent's with only the components next to b or t
// re-flooded (an overlay undone before returning). Only rooms that can see the
// change are re-evaluated: the rooms of b and t, rooms with a cell next to
// either, and rooms adjacent to a corridor component next to either (in the
// parent or the child labelling, since components merge and split).
func (s *solver) roomConnChild(b, t int32) int {
	m := s.rooms
	if m == nil || len(m.rooms) < 2 {
		return 0
	}
	if b == t {
		return m.parentTotal // the box came back: the child is the parent
	}
	if LazyChild && m.nw == 1 && m.in4[b] && m.in4[t] && m.cell[b] == m.cell[t] {
		if v, ok := s.roomInteriorMove(b, t); ok {
			return v
		}
	}
	nw := m.nw
	pl := &m.par
	m.lc = pl
	aff := m.pbuf
	clear(aff)
	zs := [2]int32{b, t}
	for _, z := range zs {
		if r := m.cell[z]; r >= 0 {
			aff[r>>6] |= 1 << (r & 63)
		}
		for _, d := range s.dirs {
			n := z + d
			if s.lv.Wall[n] {
				continue
			}
			if r := m.cell[n]; r >= 0 {
				aff[r>>6] |= 1 << (r & 63)
			} else if k := pl.lab[n]; k >= 0 {
				for w, v := range pl.touch[int(k)*nw : int(k+1)*nw] {
					aff[w] |= v
				}
			}
		}
	}
	k0 := int32(pl.k)
	next := k0
	undo := m.undo[:0]
	st := m.queue[:0]
	flood := func(c int32) {
		tb := pl.touch[int(next)*nw : int(next+1)*nw]
		clear(tb)
		undo = append(undo, c, pl.lab[c])
		pl.lab[c] = next
		st = append(st[:0], c)
		for len(st) > 0 {
			x := st[len(st)-1]
			st = st[:len(st)-1]
			for _, d := range s.dirs {
				y := x + d
				if s.lv.Wall[y] || s.occ[y] {
					continue
				}
				if cy := m.cell[y]; cy >= 0 {
					tb[cy>>6] |= 1 << (cy & 63)
				} else if pl.lab[y] < k0 {
					undo = append(undo, y, pl.lab[y])
					pl.lab[y] = next
					st = append(st, y)
				}
			}
		}
		next++
	}
	if m.cell[b] < 0 {
		flood(b) // the box left b: it is a free corridor cell now
	}
	for _, z := range zs {
		for _, d := range s.dirs {
			n := z + d
			if !s.lv.Wall[n] && !s.occ[n] && m.cell[n] < 0 && pl.lab[n] < k0 {
				flood(n)
			}
		}
	}
	for _, z := range zs {
		for _, d := range s.dirs {
			n := z + d
			if !s.lv.Wall[n] && !s.occ[n] && m.cell[n] < 0 {
				k := pl.lab[n]
				for w, v := range pl.touch[int(k)*nw : int(k+1)*nw] {
					aff[w] |= v
				}
			}
		}
	}
	total := m.parentTotal
	for w, v := range aff {
		for ; v != 0; v &= v - 1 {
			r := w<<6 | bits.TrailingZeros64(v)
			total += s.roomBrokenFast(r) - m.share[r]
		}
	}
	for i := len(undo) - 2; i >= 0; i -= 2 {
		pl.lab[undo[i]] = undo[i+1]
	}
	m.undo, m.queue = undo[:0], st[:0]
	return total
}

// LazyChild (exp-659): roomConnChild settles a move inside one room between
// interior cells from the opened node's fragments instead of re-evaluating
// the room (identical result; see roomInteriorMove).
var LazyChild = false

// keepFrags records room r's fragments just computed by roomFrag1 (the
// opened node: s.occ holds it, m.dsu and m.fragTouch hold the pass).
func (m *roomMap) keepFrags(occ []bool, r int) {
	cells := m.rooms[r]
	base := m.roomBase[r]
	dsu, rows := m.dsu, m.fragTouch
	for i, c := range cells {
		if occ[c] {
			continue
		}
		x := int32(i)
		for dsu[x] != x {
			x = dsu[x]
		}
		m.fragRoot[base+i] = x
		if x == int32(i) {
			m.fragRow[base+i] = rows[i]
		}
	}
}

// roomInteriorMove is roomConnChild for the box on b moving to t, both cells
// of room r whose non-wall 4-neighbours lie in r (s.occ holds the child).
// Such cells touch no corridor and no other room, so only r's value changes,
// and they add nothing to a fragment's touch row. Occupying t (b still a
// box): with no free neighbour, t's single-cell fragment (empty row) goes;
// if its free neighbours are joined through free r-cells of its 8-ring, the
// fragments stay as they are; otherwise ok is false (a possible split).
// Freeing b then joins the fragments of its free neighbours (by their
// recorded roots) or, with none, adds a single-cell fragment.
func (s *solver) roomInteriorMove(b, t int32) (int, bool) {
	m := s.rooms
	r := m.cell[t]
	occ := s.occ
	edge := m.edgeBits[r]
	base := m.roomBase[r]
	W := int32(s.lv.W)
	free := func(c int32) bool { // parent occupancy around t: b is a box
		return c != b && !occ[c] && !s.lv.Wall[c] && m.cell[c] == r
	}
	// The 8-ring of t in order; even positions are the 4-neighbours.
	ring := [8]int32{t - W, t - W + 1, t + 1, t + W + 1, t + W, t + W - 1, t - 1, t - W - 1}
	var fr [8]bool
	nfree := 0
	for k, c := range ring {
		fr[k] = free(c)
		if k%2 == 0 && fr[k] {
			nfree++
		}
	}
	share := m.share[r]
	if nfree == 0 {
		share -= bits64(edge)
	} else if nfree > 1 {
		// all free 4-neighbours in one circular run of free ring cells
		start := -1
		for k := range 8 {
			if !fr[k] {
				start = k
				break
			}
		}
		if start < 0 {
			goto joined // the whole ring is free
		}
		runs := 0
		for j := 1; j <= 8; j++ {
			k := (start + j) % 8
			if !fr[k] {
				continue
			}
			if !fr[(k+7)%8] {
				// a new run: count it only if it holds a 4-neighbour
				has := false
				for q := k; fr[q]; q = (q + 1) % 8 {
					if q%2 == 0 {
						has = true
						break
					}
				}
				if has {
					runs++
				}
			}
		}
		if runs > 1 {
			return 0, false
		}
	}
joined:
	// Free b (s.occ: t is a box now).
	var roots [4]int32
	var rows uint64
	nr := 0
	contrib := 0
	for _, d := range s.dirs {
		c := b + d
		if occ[c] || s.lv.Wall[c] {
			continue
		}
		x := m.fragRoot[base+int(m.loc[c])]
		dup := false
		for _, y := range roots[:nr] {
			dup = dup || y == x
		}
		if dup {
			continue
		}
		roots[nr] = x
		nr++
		row := m.fragRow[base+int(x)]
		rows |= row
		contrib += bits64(edge &^ row)
	}
	switch nr {
	case 0:
		share += bits64(edge)
	case 1:
	default:
		share += bits64(edge&^rows) - contrib
	}
	return m.parentTotal - m.share[r] + share, true
}
