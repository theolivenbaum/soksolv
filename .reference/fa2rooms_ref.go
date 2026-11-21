package main

import "math/bits"

// exp-530 test reference for the incremental room score: festRoomsFull of
// s.occ on private labels (the parent's labels m.cur stay valid). expand
// compares every child with it when fa2RoomsCheck is set (tests only).
var fa2RoomsCheck = false
var refLab corrLab
var refLost []uint64

func (f *fa2) roomsChildRef() int {
	m := f.s.rooms
	if !f.rm.on {
		return 0
	}
	if len(refLab.lab) != len(m.cell) || len(refLab.touch) != len(m.cur.touch) || len(refLost) != len(m.rooms)*m.nw {
		refLab.lab, refLab.touch = make([]int32, len(m.cell)), make([]uint64, len(m.cur.touch))
		refLost = make([]uint64, len(m.rooms)*m.nw)
	}
	clear(refLost)
	f.s.labelCorridorsFX(&refLab)
	for r := range m.rooms {
		f.s.roomBrokenFX(r, refLost)
	}
	m.lc = &m.cur
	k := 0
	for _, w := range refLost {
		k += bits.OnesCount64(w)
	}
	return k / 2
}
