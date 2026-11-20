package main

import "math/bits"

// pairDeadFromBits (exp-419) is pairDeadFromScan with the player's region
// flooded as row bitsets over private rows (the two boxes are bits cleared
// from the static floor rows), which was 90% of the pair search. States,
// visit order and results are identical.
func (s *solver) pairDeadFromBits(a, b, from int32) bool {
	const capStates = 200
	lv, bb := s.lv, s.bb
	W := int32(bb.W)
	bb.scratch()
	free, reg := bb.pfree, bb.preg
	copy(free, bb.pfloor)
	flip := func(x int32) { free[int(x/W)+1] ^= 1 << uint(x%W) }
	// reach floods the player's region from p (the start cell counts as
	// reachable even when blocked, as in the queue flood) and returns the
	// region's minimum cell. reg holds the region until the next call.
	reach := func(p int32) int32 {
		rp, cp := int(p/W)+1, uint(p%W)
		saved := free[rp]
		free[rp] |= 1 << cp
		floodRows(free, reg, rp, cp)
		free[rp] = saved
		for r := 1; r <= bb.H; r++ {
			if w := reg[r]; w != 0 {
				return int32(r-1)*W + int32(bits.TrailingZeros64(w))
			}
		}
		return p
	}
	inReg := func(c int32) bool { return reg[int(c/W)+1]>>uint(c%W)&1 != 0 }
	flip(a)
	flip(b)
	var starts []int32
	if from >= 0 {
		starts = []int32{reach(from)}
	} else {
		// every region of the board holding a and b, by ascending minimum cell
		rem := bb.prem
		copy(rem, free)
		for r := 1; r <= bb.H; r++ {
			for rem[r] != 0 {
				c := int32(r-1)*W + int32(bits.TrailingZeros64(rem[r]))
				starts = append(starts, reach(c))
				for k := range rem {
					rem[k] &^= reg[k]
				}
			}
		}
	}
	if s.pvis == nil {
		s.pvis = make([]uint64, 1024)
	}
	mask := uint64(len(s.pvis) - 1)
	visit := func(x, y, p int32) bool { // reports whether newly added
		k := uint64(uint16(x)) | uint64(uint16(y))<<16 | uint64(uint16(p))<<32
		v := k | uint64(s.pgen)<<48
		for i := (k * 0x9E3779B97F4A7C15 >> 40) & mask; ; i = (i + 1) & mask {
			e := s.pvis[i]
			if uint16(e>>48) != s.pgen {
				s.pvis[i] = v
				return true
			}
			if e == v {
				return false
			}
		}
	}
	for _, p0 := range starts {
		s.pgen++
		if s.pgen == 0 {
			clear(s.pvis)
			s.pgen = 1
		}
		visit(a, b, p0)
		nseen := 1
		q := append(s.pq2[:0], st2{a, b, p0})
		for i := 0; i < len(q); i++ {
			if nseen > capStates {
				s.pq2 = q
				return false // unknown: assume not dead
			}
			cur := q[i]
			// free holds a and b as boxes; make it hold cur's boxes
			flip(a)
			flip(b)
			flip(cur.x)
			flip(cur.y)
			reach(cur.p)
			type mv struct{ box, t, other int32 }
			var moves [8]mv
			nm := 0
			for _, pr := range [2][2]int32{{cur.x, cur.y}, {cur.y, cur.x}} {
				bx, other := pr[0], pr[1]
				for _, d := range s.dirs {
					t := bx + d
					if inReg(bx-d) && !lv.Wall[t] && t != other && s.dist[t] >= 0 {
						moves[nm] = mv{bx, t, other}
						nm++
					}
				}
			}
			flip(cur.x)
			flip(cur.y)
			for _, m := range moves[:nm] {
				if lv.Goal[m.t] && lv.Goal[m.other] {
					s.pq2 = q
					return false
				}
				flip(m.t)
				flip(m.other)
				np := reach(m.box)
				flip(m.t)
				flip(m.other)
				x, y := min(m.t, m.other), max(m.t, m.other)
				if visit(x, y, np) {
					nseen++
					q = append(q, st2{x, y, np})
				}
			}
			flip(a)
			flip(b)
		}
		s.pq2 = q
	}
	return true
}
