package main

import "math/bits"

// exp-483 (copied for exp-491 fa2): Festival's score_rooms (rooms.cpp): one point per broken
// UNORDERED baseline room pair (a fragment of either room failing to reach
// the other breaks both directions, however many fragments fail), and a
// neighbouring cell of another room counts as touched even when a box stands
// on it. A full room breaks every incident pair. Full recompute per board.

// labelCorridorsFX is labelCorridors with the boundary rule: a room cell
// next to the corridor component is touched whether or not it holds a box.
func (s *solver) labelCorridorsFX(lc *corrLab) {
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
					tb[cy>>6] |= 1 << (cy & 63)
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

// roomBrokenFX marks, in lost (n rows of nw words, symmetric), the baseline
// pairs of room r that one of its free fragments no longer reaches.
func (s *solver) roomBrokenFX(r int, lost []uint64) {
	m := s.rooms
	nw := m.nw
	if nw == 1 {
		s.roomBrokenFX1(r, lost)
		return
	}
	s.roomBrokenFXN(r, lost)
}

// roomBrokenFXN is roomBrokenFX for any number of rooms.
func (s *solver) roomBrokenFXN(r int, lost []uint64) {
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
	mark := func(o int) {
		lost[r*nw+o>>6] |= 1 << (o & 63)
		lost[o*nw+r>>6] |= 1 << (r & 63)
	}
	for i, c := range cells {
		if s.occ[c] {
			continue
		}
		free++
		ii := int32(i)
		dsu[i] = ii
		clear(rows[i*nw : (i+1)*nw])
		for _, nb := range m.fbEdge[m.fbStart[base+i]:m.fbStart[base+i+1]] {
			y, kd := nb.cell, nb.kind
			if kd <= -2 { // another room: touched even when occupied
				o := int(-2 - kd)
				rows[int(m.find(ii))*nw+o>>6] |= 1 << (o & 63)
				continue
			}
			if s.occ[y] {
				continue
			}
			if kd >= 0 {
				if kd < ii {
					m.union(ii, kd)
				}
			} else {
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
			}
		}
	}
	if free == 0 {
		for w, e := range edges {
			for ; e != 0; e &= e - 1 {
				mark(w<<6 | bits.TrailingZeros64(e))
			}
		}
		return
	}
	for i, c := range cells {
		if s.occ[c] || dsu[i] != int32(i) {
			continue
		}
		tb := rows[i*nw : (i+1)*nw]
		for w, e := range edges {
			for miss := e &^ tb[w]; miss != 0; miss &= miss - 1 {
				mark(w<<6 | bits.TrailingZeros64(miss))
			}
		}
	}
}

// festRoomsFull is the exp-483 room score of s.occ.
func (s *solver) festRoomsFull() int {
	m := s.rooms
	if m == nil || len(m.rooms) < 2 {
		return 0
	}
	n := len(m.rooms)
	if len(m.fxLost) != n*m.nw {
		m.fxLost = make([]uint64, n*m.nw)
	}
	clear(m.fxLost)
	s.labelCorridorsFX(&m.cur)
	for r := range n {
		s.roomBrokenFX(r, m.fxLost)
	}
	k := 0
	for _, w := range m.fxLost {
		k += bits.OnesCount64(w)
	}
	return k / 2
}

// roomBrokenFX1 is roomBrokenFX for at most 64 rooms (one word per touch
// row): scalar rows and inlined find/union (exp-655, identical result).
func (s *solver) roomBrokenFX1(r int, lost []uint64) {
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
			kd := nb.kind
			if kd <= -2 { // another room: touched even when occupied
				ri := ii
				for dsu[ri] != ri {
					dsu[ri] = dsu[dsu[ri]]
					ri = dsu[ri]
				}
				rows[ri] |= 1 << uint(-2-kd)
				continue
			}
			y := nb.cell
			if occ[y] {
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
			} else {
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
			}
		}
	}
	var miss uint64
	if free == 0 {
		miss = edge
	} else {
		for i, c := range cells {
			if !occ[c] && dsu[i] == int32(i) {
				miss |= edge &^ rows[i]
			}
		}
	}
	if miss != 0 {
		lost[r] |= miss
		for ; miss != 0; miss &= miss - 1 {
			lost[bits.TrailingZeros64(miss)] |= 1 << uint(r)
		}
	}
}
