package reverse

import "math/bits"

// bitReach (exp-415) answers normalize's "minimum cell of the player's region"
// with a row-bitset flood instead of a cell BFS. Rows are built lazily, once
// per expanded node, from the parent's occupancy; a candidate pull then
// toggles its two cells in the rows and undoes them. The BFS also stamped
// mark2, which nothing reads. Boards wider than 64 cells use the BFS.
type bitReach struct {
	W, H  int
	wall  []uint64 // wall[r+1]: bit x set for a wall at (r, x)
	free  []uint64 // free[r+1]: not wall and not occupied (the parent's boxes)
	reg   []uint64
	armed bool // a node is being expanded: rows may be used
	built bool // free holds that node's boxes
}

func newBitReach(lv *Level) *bitReach {
	if lv.W > 64 {
		return nil
	}
	br := &bitReach{W: lv.W, H: lv.H, wall: make([]uint64, lv.H+2), free: make([]uint64, lv.H+2), reg: make([]uint64, lv.H+2)}
	for r := 0; r < lv.H; r++ {
		for x := 0; x < lv.W; x++ {
			if lv.Wall[r*lv.W+x] {
				br.wall[r+1] |= 1 << uint(x)
			}
		}
	}
	return br
}

func (br *bitReach) build(occ []bool) {
	W := br.W
	all := ^uint64(0)
	if W < 64 {
		all = 1<<uint(W) - 1
	}
	for r := 0; r < br.H; r++ {
		var m uint64
		for x, o := range occ[r*W : r*W+W] {
			if o {
				m |= 1 << uint(x)
			}
		}
		br.free[r+1] = all &^ br.wall[r+1] &^ m
	}
	br.built = true
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

// minReach floods from start with cell b freed and cell p blocked (the rows
// hold the parent's boxes) and returns the region's minimum cell. start is
// always taken as reachable.
func (br *bitReach) minReach(start, b, p int32) int32 {
	W := int32(br.W)
	free, reg := br.free, br.reg
	rb, cb := int(b/W)+1, uint(b%W)
	rp, cp := int(p/W)+1, uint(p%W)
	rs, cs := int(start/W)+1, uint(start%W)
	s1, s2, s3 := free[rb], free[rp], free[rs]
	free[rb] |= 1 << cb
	free[rp] &^= 1 << cp
	free[rs] |= 1 << cs
	clear(reg)
	reg[rs] = fillRow(1<<cs, free[rs])
	last := len(free) - 2
	for changed := true; changed; {
		changed = false
		for r := 1; r <= last; r++ {
			y := reg[r] | (reg[r-1]|reg[r+1])&free[r]
			if y == reg[r] {
				continue
			}
			if y = fillRow(y, free[r]); y != reg[r] {
				reg[r] = y
				changed = true
			}
		}
		for r := last; r >= 1; r-- {
			y := reg[r] | (reg[r-1]|reg[r+1])&free[r]
			if y == reg[r] {
				continue
			}
			if y = fillRow(y, free[r]); y != reg[r] {
				reg[r] = y
				changed = true
			}
		}
	}
	free[rs], free[rp], free[rb] = s3, s2, s1
	for r := 1; r <= last; r++ {
		if w := reg[r]; w != 0 {
			return int32(r-1)*W + int32(bits.TrailingZeros64(w))
		}
	}
	return start
}
