package main

import (
	"fmt"
	"math/bits"
	"slices"
)

// moveAccess (exp-419) answers, for the child just flooded by boxSearch, how
// many (box, direction) pairs have a reachable support (mobility), whether an
// unchanged box gained a legal push from newly reachable support (explorer),
// and the access facts when AccessNovelty records them.
//
// In node mode (boards up to 64 wide) every term is a row-bitset expression
// over the rows the search already holds: the child's reach (bb.reg, left by
// the last flood), the child's free cells (bb.free with the lifted box free),
// the parent's reach (bb.par, built once per node from mark) and the static
// alive rows. Only rows holding a box are visited; no per-box loads or
// branches on reachability. Facts are emitted in cell order, which the
// consumers do not depend on (set membership only). Wider boards use
// moveAccessScan, which is also the test reference (checkAccess).
func (s *solver) moveAccess(cur []int32, moved int, origB, target int32, st uint32, cmdMask, unpinMask uint64, unpackedRegime bool) (mobility int, explorer bool, accStart, accLen int32) {
	bb := s.bb
	if bb == nil || !bb.node {
		return s.moveAccessScan(cur, moved, origB, target, st, cmdMask, unpinMask, unpackedRegime)
	}
	mobility, explorer, accStart, accLen = s.moveAccessBits(origB, target, st, cmdMask, unpinMask, unpackedRegime)
	if checkAccess {
		got := slices.Clone(s.accBuf[accStart : accStart+accLen])
		s.accBuf = s.accBuf[:accStart]
		m2, e2, _, l2 := s.moveAccessScan(cur, moved, origB, target, st, cmdMask, unpinMask, unpackedRegime)
		want := slices.Clone(s.accBuf[accStart : accStart+l2])
		slices.Sort(got)
		slices.Sort(want)
		if m2 != mobility || e2 != explorer || !slices.Equal(got, want) {
			panic(fmt.Sprintf("moveAccessBits disagrees with scan: mobility %d/%d explorer %v/%v facts %d/%d", mobility, m2, explorer, e2, len(got), len(want)))
		}
		s.accBuf = append(s.accBuf[:accStart], got...)
		accLen = int32(len(got))
	}
	return
}

// checkAccess (tests): compare moveAccessBits with moveAccessScan on every call.
var checkAccess = false

// setAccessGeometry fills the static rows moveAccess needs: cells with a
// finite push distance, and per direction the (row, column) offsets of the
// support, the box's landing cell (alive test) and the cell that must be free.
func (bb *bitBoard) setAccessGeometry(dist []int32, dirs [4]int32, W int, pull bool) {
	bb.alive = make([]uint64, bb.H+2)
	for r := 0; r < bb.H; r++ {
		for x := 0; x < bb.W; x++ {
			if dist[r*W+x] >= 0 {
				bb.alive[r+1] |= 1 << uint(x)
			}
		}
	}
	for k, d := range dirs {
		var dr, dx int
		switch d {
		case -int32(W):
			dr = -1
		case int32(W):
			dr = 1
		case -1:
			dx = -1
		default:
			dx = 1
		}
		g := &bb.geo[k]
		g.landR, g.landX = dr, dx
		if pull {
			g.supR, g.supX = dr, dx
			g.freeR, g.freeX = 2*dr, 2*dx
		} else {
			g.supR, g.supX = -dr, -dx
			g.freeR, g.freeX = dr, dx
		}
	}
}

// accessGeo holds one direction's offsets for moveAccessBits.
type accessGeo struct{ supR, supX, landR, landX, freeR, freeX int }

// buildParent fills the parent's reach rows from the stamp array.
func (bb *bitBoard) buildParent(mark []uint32, st uint32) {
	if bb.par == nil {
		bb.par = make([]uint64, bb.H+2)
	}
	for r := 0; r < bb.H; r++ {
		var m uint64
		for x, v := range mark[r*bb.W : r*bb.W+bb.W] {
			if v == st {
				m |= 1 << uint(x)
			}
		}
		bb.par[r+1] = m
	}
	bb.parentBuilt = true
}

// rowAt returns rows[i], or 0 outside the slice.
func rowAt(rows []uint64, i int) uint64 {
	if uint(i) >= uint(len(rows)) {
		return 0
	}
	return rows[i]
}

// alignX moves the cells x+dx of a row onto position x.
func alignX(v uint64, dx int) uint64 {
	if dx >= 0 {
		return v >> uint(dx)
	}
	return v << uint(-dx)
}

func (s *solver) moveAccessBits(origB, target int32, st uint32, cmdMask, unpinMask uint64, unpackedRegime bool) (mobility int, explorer bool, accStart, accLen int32) {
	bb := s.bb
	if !bb.parentBuilt {
		bb.buildParent(s.mark, st)
	}
	recordAcc := AccessNovelty && s.noveltyBudget() > 0
	if recordAcc {
		accStart = int32(len(s.accBuf))
	}
	displaced := recordAcc && (unpackedRegime || (cmdMask|unpinMask) != 0 || (s.lv.Goal[origB] && !s.lv.Goal[target]))
	goalVacated := recordAcc && ((cmdMask|unpinMask) != 0 || (s.lv.Goal[origB] && !s.lv.Goal[target]))
	W := int32(bb.W)
	all := ^uint64(0)
	if bb.W < 64 {
		all = 1<<uint(bb.W) - 1
	}
	tr, tbit := int(target/W), uint64(1)<<uint(target%W)
	free, wall, reg, par, alive := bb.free, bb.wall, bb.reg, bb.par, bb.alive
	for r := 0; r < bb.H; r++ {
		boxes := all &^ free[r+1] &^ wall[r+1]
		var tm uint64
		if r == tr {
			boxes |= tbit
			tm = tbit
		}
		if boxes == 0 {
			continue
		}
		for k := range bb.geo {
			g := &bb.geo[k]
			reach := alignX(rowAt(reg, r+g.supR+1), g.supX)
			sup := boxes & reach
			if sup == 0 {
				continue
			}
			mobility += bits.OnesCount64(sup)
			if explorer && !recordAcc {
				continue // legality only matters for the first explorer or for recorded facts
			}
			fresh := sup &^ alignX(rowAt(par, r+g.supR+1), g.supX)
			if fresh == 0 && !goalVacated {
				continue
			}
			legal := sup & alignX(rowAt(free, r+g.freeR+1)&^tmAt(r+g.freeR, tr, tbit), g.freeX) & alignX(rowAt(alive, r+g.landR+1), g.landX)
			if legal == 0 {
				continue
			}
			if x := fresh & legal &^ tm; x != 0 {
				explorer = true
				if recordAcc {
					for ; x != 0; x &= x - 1 {
						b := int32(r)*W + int32(bits.TrailingZeros64(x))
						psi := uint32(b*4 + int32(k))
						s.accBuf = append(s.accBuf, uint64(psi), (uint64(1)<<40)|(uint64(psi)<<20)|uint64(target))
						if displaced {
							s.accBuf = append(s.accBuf, (uint64(2)<<40)|(uint64(psi)<<20)|uint64(origB))
						}
					}
				}
			}
			if goalVacated {
				for x := legal & (fresh | tm); x != 0; x &= x - 1 {
					b := int32(r)*W + int32(bits.TrailingZeros64(x))
					psi := uint32(b*4 + int32(k))
					s.accBuf = append(s.accBuf, (uint64(4)<<40)|(uint64(origB)<<20)|uint64(psi))
				}
			}
		}
	}
	if recordAcc {
		if s.candNP >= 0 && s.mark[s.candNP] != st {
			s.accBuf = append(s.accBuf, (uint64(3)<<40)|(uint64(s.candNP)<<20)|uint64(target))
		}
		accLen = int32(len(s.accBuf)) - accStart
	}
	return
}

// tmAt is the target's bit when row r is the target's row (the child's box
// stands there although the shared free rows still show the cell free).
func tmAt(r, tr int, tbit uint64) uint64 {
	if r == tr {
		return tbit
	}
	return 0
}
