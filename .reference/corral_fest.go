package main

import "slices"

// Festival's PI-corral detection (exp-269, corral.cpp: detect_corral). A
// corral grows from one floor component behind a box that touches the
// player's component (or from two touching components), adding a component
// only when a corral box can be pushed into it or aside into it. The pushes
// are judged on a board with only the boxes around the corral. It is a
// PI-corral when no such box can be pushed outside the corral and every push
// into it starts from a cell the player reaches now. Smaller corrals than our
// maximal piCorral pass the test more often, so more nodes get the restriction.

// FestCorral uses festCorral instead of piCorral in forward FESS.
var FestCorral = false

type festCorralScratch struct {
	comp    []int32 // floor component per cell (-1: wall or box), stamped by gen
	gen     []uint32
	stamp   uint32
	n       int32
	used    []bool
	cand    []bool
	inStamp uint32
	cb      []uint32  // corral boxes (8-neighbourhood), stamped by inStamp
	goal    []bool    // component holds a goal cell (exp-457)
	bl      [][]int32 // boxes in the 8-neighbourhood of each component, built lazily (exp-457)
	bng     []bool    // bl[i] holds a non-goal box
	bdone   []uint32  // bl[i] valid when == stamp
	bseen   []uint32  // box dedupe while building a list, stamped by bctr
	bctr    uint32
	gval    []int32 // grow(b) cache, valid when gok == inStamp (exp-457)
	gok     []uint32
	cbl     []int32  // corral boxes of the current used set (exp-457)
	pairs   []uint64 // candidate two-room pairs i<<32|j, sorted (exp-457)
	q       []int32
	boxes   []int32
	cl      []int32 // floor cells grouped by component (exp-415), so fcMark visits only the corral
	cs      []int32 // cs[i]:cs[i+1] bounds component i in cl
}

// festCorral returns a stamp in s.cmark marking the boxes whose pushes the
// node may use, or 0 when no PI-corral is found. st stamps the player's
// reach in s.mark.
func (s *solver) festCorral(player int32, st uint32) uint32 {
	lv := s.lv
	f := &s.fc
	cells := len(lv.Wall)
	if f.comp == nil {
		f.comp = make([]int32, cells)
		f.gen = make([]uint32, cells)
		f.bseen = make([]uint32, cells)
		f.gval = make([]int32, cells)
		f.gok = make([]uint32, cells)
		f.cb = make([]uint32, cells)
	}
	f.stamp++
	if f.stamp == 0 {
		clear(f.gen)
		f.stamp = 1
	}
	comp := func(c int32) int32 {
		if f.gen[c] != f.stamp {
			return -1
		}
		return f.comp[c]
	}
	// Floor components.
	f.n = 0
	f.boxes = f.boxes[:0]
	f.cl, f.cs = f.cl[:0], append(f.cs[:0], 0)
	f.goal = f.goal[:0]
	for c := range int32(cells) {
		if s.occ[c] {
			f.boxes = append(f.boxes, c)
		}
		if lv.Wall[c] || s.occ[c] || f.gen[c] == f.stamp {
			continue
		}
		q := append(f.q[:0], c)
		f.gen[c], f.comp[c] = f.stamp, f.n
		hasGoal := lv.Goal[c]
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; !lv.Wall[y] && !s.occ[y] && f.gen[y] != f.stamp {
					f.gen[y], f.comp[y] = f.stamp, f.n
					hasGoal = hasGoal || lv.Goal[y]
					q = append(q, y)
				}
			}
		}
		f.q = q
		f.goal = append(f.goal, hasGoal)
		f.cl = append(f.cl, q...)
		f.cs = append(f.cs, int32(len(f.cl)))
		f.n++
	}
	if f.n > 250 {
		return 0
	}
	for len(f.bl) < int(f.n) {
		f.bl = append(f.bl, nil)
		f.bng = append(f.bng, false)
		f.bdone = append(f.bdone, 0)
	}
	pc := comp(player)
	if cap(f.used) < int(f.n) {
		f.used = make([]bool, f.n)
		f.cand = make([]bool, f.n)
	}
	f.used, f.cand = f.used[:f.n], f.cand[:f.n]
	clear(f.cand)
	for _, b := range f.boxes {
		for _, d := range s.dirs {
			if comp(b+d) == pc {
				if o := comp(b - d); o >= 0 && o != pc {
					f.cand[o] = true
				}
			}
		}
	}
	try := func(c1, c2 int32) uint32 {
		clear(f.used)
		f.used[c1], f.used[c2] = true, true
		if !s.fcActive() {
			return 0
		}
		return s.fcExpand(pc, st, comp)
	}
	for i := int32(0); i < f.n; i++ {
		if f.cand[i] {
			if r := try(i, i); r != 0 {
				return r
			}
		}
	}
	// Two-room corrals: components touching the same box, tried in (i, j) order.
	// The pairs come from the boxes (exp-457) instead of testing every i<j.
	f.pairs = f.pairs[:0]
	for _, b := range f.boxes {
		var nb [4]int32
		k := 0
	next:
		for _, d := range s.dirs {
			c := comp(b + d)
			if c < 0 || c == pc {
				continue
			}
			for _, o := range nb[:k] {
				if o == c {
					continue next
				}
			}
			nb[k] = c
			k++
		}
		for x := 0; x < k; x++ {
			for y := x + 1; y < k; y++ {
				i, j := nb[x], nb[y]
				if i > j {
					i, j = j, i
				}
				if f.cand[i] || f.cand[j] {
					f.pairs = append(f.pairs, uint64(i)<<32|uint64(j))
				}
			}
		}
	}
	slices.Sort(f.pairs)
	for k, p := range f.pairs {
		if k > 0 && p == f.pairs[k-1] {
			continue
		}
		if r := try(int32(p>>32), int32(p&0xffffffff)); r != 0 {
			return r
		}
	}
	return 0
}

// fcIn reports whether c is a cell of a used component.
func (f *festCorralScratch) fcIn(c int32) bool {
	return f.gen[c] == f.stamp && f.used[f.comp[c]]
}

// fcBoxes lists the boxes in the 8-neighbourhood of component i's cells.
func (s *solver) fcBoxes(i int32) []int32 {
	f := &s.fc
	if f.bdone[i] == f.stamp {
		return f.bl[i]
	}
	f.bdone[i] = f.stamp
	f.bctr++
	if f.bctr == 0 {
		clear(f.bseen)
		f.bctr = 1
	}
	w := int32(s.lv.W)
	n8 := [8]int32{1, -1, w, -w, w + 1, w - 1, -w + 1, -w - 1}
	list, ng := f.bl[i][:0], false
	for _, c := range f.cl[f.cs[i]:f.cs[i+1]] {
		for _, d := range n8 {
			if y := c + d; s.occ[y] && f.bseen[y] != f.bctr {
				f.bseen[y] = f.bctr
				list = append(list, y)
				ng = ng || !s.lv.Goal[y]
			}
		}
	}
	f.bl[i], f.bng[i] = list, ng
	return list
}

// fcMarkComp stamps the boxes around component i as corral boxes.
func (s *solver) fcMarkComp(i int32) {
	f := &s.fc
	for _, b := range s.fcBoxes(i) {
		if f.cb[b] != f.inStamp {
			f.cb[b] = f.inStamp
			f.cbl = append(f.cbl, b)
		}
	}
}

// fcMark stamps the corral boxes (8-neighbourhood of the components in used).
// Corral cells are not stamped: fcIn reads them from the component labels
// (exp-457).
func (s *solver) fcMark() {
	f := &s.fc
	f.inStamp++
	if f.inStamp == 0 {
		clear(f.cb)
		clear(f.gok)
		f.inStamp = 1
	}
	f.cbl = f.cbl[:0]
	for i, u := range f.used {
		if u {
			s.fcMarkComp(int32(i))
		}
	}
	if fcCheck != nil {
		fcCheck(s, 0, false)
	}
}

// fcCheck, when set by a test, receives every fcMark (kind 0) and fcActive
// (kind 1, with its result) so a scan-over-the-board reference can be compared.
var fcCheck func(s *solver, kind int, active bool)

func (s *solver) fcActive() bool {
	r := s.fcActive1()
	if fcCheck != nil {
		fcCheck(s, 1, r)
	}
	return r
}

func (s *solver) fcActive1() bool {
	s.fcMark()
	f := &s.fc
	for i, u := range f.used {
		if u && f.goal[i] {
			return true
		}
	}
	for i, u := range f.used {
		if u && f.bng[i] {
			return true
		}
	}
	return false
}

// fcExpand grows the corral and tests it (corral cells stamped by fcMark).
func (s *solver) fcExpand(pc int32, st uint32, comp func(int32) int32) uint32 {
	lv := s.lv
	f := &s.fc
	isIn := f.fcIn
	occ := func(c int32) bool { return lv.Wall[c] || f.cb[c] == f.inStamp } // relaxed board
	// fcActive just stamped the boxes for the current used set (exp-457).
	// grow returns the component box b would add (-1: none). It reads only b's
	// four neighbours (their cells, labels and corral-box stamps), so after an
	// add only the boxes next to the new component or to a newly stamped box
	// are re-evaluated; the first box in board order still decides.
	grow := func(b int32) int32 {
		touchIn, touchOut := false, false
		for _, d := range s.dirs {
			if isIn(b + d) {
				touchIn = true
			}
			if !occ(b+d) && !isIn(b+d) {
				touchOut = true
			}
		}
		if !touchIn || !touchOut {
			return -1
		}
		for _, d := range s.dirs {
			pu, de := b+d, b-d // pusher, destination
			if lv.Wall[pu] || occ(de) || s.dist[de] < 0 {
				continue
			}
			inside := !occ(pu) && !isIn(pu) && isIn(de)
			aside := !occ(pu) && !isIn(pu) && !isIn(de)
			if inside {
				if c := comp(pu); c >= 0 && c != pc && !f.used[c] {
					return c
				}
			}
			if aside {
				if c := comp(de); c >= 0 && c != pc && !f.used[c] {
					return c
				}
			}
		}
		return -1
	}
	for {
		add, first := int32(-1), int32(-1)
		for _, b := range f.cbl {
			if first >= 0 && b > first {
				continue
			}
			if f.gok[b] != f.inStamp {
				f.gval[b], f.gok[b] = grow(b), f.inStamp
			}
			if f.gval[b] >= 0 {
				add, first = f.gval[b], b
			}
		}
		if add < 0 {
			break
		}
		f.used[add] = true
		n0 := len(f.cbl)
		s.fcMarkComp(add)
		for _, b := range f.bl[add] {
			f.gok[b] = 0
		}
		for _, n := range f.cbl[n0:] {
			for _, d := range s.dirs {
				f.gok[n+d] = 0
			}
		}
		if fcCheck != nil {
			fcCheck(s, 0, false)
		}
	}
	// No corral box can be pushed outside the corral (relaxed board).
	for _, b := range f.cbl {
		for _, d := range s.dirs {
			pu, de := b+d, b-d
			if !occ(pu) && !occ(de) && !isIn(pu) && !isIn(de) && s.dist[de] >= 0 {
				return 0
			}
		}
	}
	// Every push into the corral starts where the player is now.
	s.cstamp++
	opt := s.cstamp
	anyOpt := false
	for _, b := range f.cbl {
		for _, d := range s.dirs {
			pu, de := b+d, b-d
			if lv.Wall[pu] || occ(de) || isIn(pu) || !isIn(de) || s.dist[de] < 0 || occ(pu) {
				continue
			}
			if s.mark[pu] != st {
				return 0
			}
			s.cmark[b] = opt
			anyOpt = true
		}
	}
	if !anyOpt {
		return 0
	}
	return opt
}
