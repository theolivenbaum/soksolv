package main

import "math/bits"

// boxSearch (exp-408) computes the player's reach after each
// push with a row-bitset flood (one uint64 per board row) instead of a
// cell-queue BFS, then stamps mark3 from the result so every reader of mark3
// is unchanged. Region, minimum cell and stamps are identical to the queue
// flood, which stays for boards wider than 64 cells and as the test
// reference.

// boxBitAfter is how many pushes of one boxSearch use the queue flood before
// the occupancy rows are built (the build is O(cells), the flood saves more
// than that only on longer searches). Short searches (most boxes have few
// reachable pushes) keep the queue flood so they never pay the build.
const boxBitAfter = 3

// bitBoard holds the static wall rows and the per-search free rows. Rows are
// shifted by one so rows r-1 and r+1 always exist.
type bitBoard struct {
	W, H int
	wall []uint64 // wall[r+1]: bit x set for a wall at (r, x)
	free []uint64 // free[r+1]: not wall and not occupied, built per search
	reg  []uint64

	// moveAccess rows (exp-419): par is the node's reach, alive the cells
	// with a finite push distance, geo the per-direction offsets.
	par, alive  []uint64
	geo         [4]accessGeo
	parentBuilt bool

	// pairDeadFromBits scratch (exp-419): static floor rows, the working
	// free rows, the region rows and the unvisited-region rows.
	pfloor, pfree, preg, prem []uint64

	// node: open() is looping over one node's boxes (every boxSearch sees
	// the same other boxes); nodeBuilt: free holds that node's boxes.
	node, nodeBuilt bool
}

// newBitBoard returns nil for boards wider than 64 cells.
func newBitBoard(lv *Level) *bitBoard {
	if lv.W > 64 {
		return nil
	}
	bb := &bitBoard{W: lv.W, H: lv.H, wall: make([]uint64, lv.H+2), free: make([]uint64, lv.H+2), reg: make([]uint64, lv.H+2)}
	for r := 0; r < lv.H; r++ {
		for x := 0; x < lv.W; x++ {
			if lv.Wall[r*lv.W+x] {
				bb.wall[r+1] |= 1 << uint(x)
			}
		}
	}
	return bb
}

// build fills the free rows from the occupancy.
func (bb *bitBoard) build(occ []bool) {
	W := bb.W
	all := ^uint64(0)
	if W < 64 {
		all = 1<<uint(W) - 1
	}
	for r := 0; r < bb.H; r++ {
		row := occ[r*W : r*W+W]
		var m uint64
		for x, o := range row {
			if o {
				m |= 1 << uint(x)
			}
		}
		bb.free[r+1] = all &^ bb.wall[r+1] &^ m
	}
}

func fillRow(y, f uint64) uint64 {
	for {
		z := (y | y<<1 | y>>1) & f
		if z == y {
			return y
		}
		y = z
	}
}

// flood marks the cells reachable from pc with ps in mark3, treating nc as
// occupied, and returns the minimum reachable cell. pc is always taken as
// reachable (as the queue flood does).
func (bb *bitBoard) flood(pc, nc int32, mark3 []uint32, ps uint32) int32 {
	W := int32(bb.W)
	free, reg := bb.free, bb.reg
	rn, cn := int(nc/W)+1, uint(nc%W)
	rp, cp := int(pc/W)+1, uint(pc%W)
	s1, s2 := free[rn], free[rp]
	free[rn] &^= 1 << cn
	free[rp] |= 1 << cp
	floodRows(free, reg, rp, cp)
	free[rp], free[rn] = s2, s1
	last := len(free) - 2
	minC := int32(-1)
	for r := 1; r <= last; r++ {
		w := reg[r]
		if w == 0 {
			continue
		}
		base := int32(r-1) * W
		if minC < 0 {
			minC = base + int32(bits.TrailingZeros64(w))
		}
		for ; w != 0; w &= w - 1 {
			mark3[base+int32(bits.TrailingZeros64(w))] = ps
		}
	}
	return minC
}

// floodRows fills reg with the cells reachable from (row rp, column cp)
// through free (both shifted by one row; free must hold the start cell).
func floodRows(free, reg []uint64, rp int, cp uint) {
	clear(reg)
	reg[rp] = fillRow(1<<cp, free[rp])
	last := len(free) - 2
	// A sweep that changes nothing proves the fixpoint: stop there.
	for {
		changed := false
		for r := 1; r <= last; r++ {
			y := reg[r] | (reg[r-1]|reg[r+1])&free[r]
			if y == reg[r] {
				continue
			}
			y = fillRow(y, free[r])
			if y != reg[r] {
				reg[r] = y
				changed = true
			}
		}
		if !changed {
			return
		}
		changed = false
		for r := last; r >= 1; r-- {
			y := reg[r] | (reg[r-1]|reg[r+1])&free[r]
			if y == reg[r] {
				continue
			}
			y = fillRow(y, free[r])
			if y != reg[r] {
				reg[r] = y
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// scratch allocates the private rows of the pair search and regionsChild.
func (bb *bitBoard) scratch() {
	if bb.pfree == nil {
		bb.pfloor, bb.pfree = make([]uint64, bb.H+2), make([]uint64, bb.H+2)
		bb.preg, bb.prem = make([]uint64, bb.H+2), make([]uint64, bb.H+2)
		all := ^uint64(0)
		if bb.W < 64 {
			all = 1<<uint(bb.W) - 1
		}
		for r := 0; r < bb.H; r++ {
			bb.pfloor[r+1] = all &^ bb.wall[r+1]
		}
	}
}

// regionsChild is regions() for the child being scored in open() (node
// mode: bb.free holds the node's boxes with the searched box lifted, t is
// the child's box): the components of empty floor are peeled off the free
// rows one flood at a time.
func (s *solver) regionsChild(t int32) int {
	bb := s.bb
	if bb == nil || !bb.node {
		return s.regions()
	}
	bb.scratch()
	W := int32(bb.W)
	rem, reg := bb.prem, bb.preg
	copy(rem, bb.free)
	rem[int(t/W)+1] &^= 1 << uint(t%W)
	n := 0
	for r := 1; r <= bb.H; r++ {
		for rem[r] != 0 {
			n++
			floodRows(rem, reg, r, uint(bits.TrailingZeros64(rem[r])))
			for k := range rem {
				rem[k] &^= reg[k]
			}
		}
	}
	return n
}
