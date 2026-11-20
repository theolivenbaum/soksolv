package main

import (
	"math/bits"
	"slices"
)

// exp-463: -hub-gen-reach. Start-feasibility of generator hubs by EXACT
// single-box reach. For every start box b, R_b is the set of cells the box can
// be pushed to on the board with only walls (all other boxes removed, so the
// player has at least as much room as in the real game) with the player's
// access checked: a state is (box cell, the component of the player in the
// board minus the box cell), and a push needs the cell behind the box in the
// player's component. R_b is a superset of what the real game allows, so
// "hub square not in R_b" and "no perfect matching boxes -> hub squares over
// R_b" are NECESSARY conditions for forward reachability: they never reject
// a reachable hub. The start is used only as this filter, never as a key.
//
// Boxes with equal R_b form classes. A class set S (other than the union of
// all R_b) with k boxes whose R_b lies inside S needs >= k hub boxes inside
// S. The generator keeps, per expansion, the count of the node's boxes in each
// S (at most 64 classes, smallest first) and skips pull-walks c -> x that
// drop a class below its requirement. Hall's condition across overlapping
// classes is not covered by the counts; the exact matching at nomination is.

var hubGenReach = true // exp-463: start-feasibility of generator hubs by exact single-box reach (-hub-back G)

type hubReach struct {
	cellMask []uint64 // per cell: bit j set if the cell is in class j's set
	need     []int32  // per class: boxes of the start that must end inside it
	inU      []bool   // per cell: some start box can reach it
	words    int
	rb       [][]uint64 // per start box: reach bitset over cells
	nCls     int
	// matching scratch
	matchOf []int32 // hub square index -> box index, -1 free
	seen    []uint32
	stamp   uint32
	adj     [][]int32
}

// newHubReach computes the reach sets of the start boxes of pull-view solver
// ps (its lv.Goal marks the start squares, realPlayer is the start player).
// It returns nil when the level has no start boxes.
func newHubReach(ps *solver) *hubReach {
	lv := ps.lv
	cells := len(lv.Wall)
	var start []int32
	for c := range int32(cells) {
		if lv.Goal[c] {
			start = append(start, c)
		}
	}
	if len(start) == 0 || len(start) != ps.n {
		return nil
	}
	dirs := ps.dirs
	opp := [4]int{1, 0, 3, 2}
	// comp[x*4+k]: canonical (smallest) side index of the component, in the
	// board minus x, holding the cell beside x on side k; -1 if that is a wall.
	comp := make([]int8, cells*4)
	lab := make([]uint32, cells)
	var stamp uint32
	var q []int32
	flood := func(from, block int32) {
		stamp++
		q = append(q[:0], from)
		lab[from] = stamp
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, d := range dirs {
				if y := c + d; y != block && !lv.Wall[y] && lab[y] != stamp {
					lab[y] = stamp
					q = append(q, y)
				}
			}
		}
	}
	for x := range int32(cells) {
		for k := range 4 {
			comp[int(x)*4+k] = -1
		}
		if lv.Wall[x] {
			continue
		}
		for k := range 4 {
			nb := x + dirs[k]
			if lv.Wall[nb] {
				continue
			}
			if k > 0 && comp[int(x)*4+k] >= 0 {
				continue
			}
			flood(nb, x)
			for j := k; j < 4; j++ {
				if y := x + dirs[j]; !lv.Wall[y] && lab[y] == stamp {
					comp[int(x)*4+j] = int8(k)
				}
			}
		}
	}
	words := (cells + 63) / 64
	r := &hubReach{words: words, rb: make([][]uint64, len(start))}
	vis := make([]uint64, (cells*4+63)/64)
	var bfs []int32
	for bi, b := range start {
		// the player's component in the board minus b
		pk := int8(-1)
		flood(ps.realPlayer, b)
		for k := range 4 {
			if nb := b + dirs[k]; !lv.Wall[nb] && lab[nb] == stamp {
				pk = comp[int(b)*4+k]
				break
			}
		}
		set := make([]uint64, words)
		set[b>>6] |= 1 << (b & 63)
		r.rb[bi] = set
		if pk < 0 {
			continue
		}
		clear(vis)
		bfs = append(bfs[:0], b*4+int32(pk))
		vis[(int(b)*4+int(pk))>>6] |= 1 << ((int(b)*4 + int(pk)) & 63)
		for i := 0; i < len(bfs); i++ {
			x, k := bfs[i]>>2, int8(bfs[i]&3)
			for di, d := range dirs {
				y := x + d
				if lv.Wall[y] || comp[int(x)*4+opp[di]] != k {
					continue
				}
				ny := int8(comp[int(y)*4+opp[di]])
				if ny < 0 {
					continue
				}
				s := int(y)*4 + int(ny)
				if vis[s>>6]&(1<<(s&63)) != 0 {
					continue
				}
				vis[s>>6] |= 1 << (s & 63)
				set[y>>6] |= 1 << (y & 63)
				bfs = append(bfs, int32(s))
			}
		}
	}
	// union and classes
	union := make([]uint64, words)
	for _, s := range r.rb {
		for w := range s {
			union[w] |= s[w]
		}
	}
	r.inU = make([]bool, cells)
	for c := range cells {
		r.inU[c] = union[c>>6]&(1<<(c&63)) != 0
	}
	var sets [][]uint64
	for _, s := range r.rb {
		if !slices.Equal(s, union) && !slices.ContainsFunc(sets, func(t []uint64) bool { return slices.Equal(s, t) }) {
			sets = append(sets, s)
		}
	}
	card := func(s []uint64) (n int) {
		for _, w := range s {
			n += bits.OnesCount64(w)
		}
		return
	}
	slices.SortStableFunc(sets, func(a, b []uint64) int { return card(a) - card(b) })
	if len(sets) > 64 {
		sets = sets[:64]
	}
	r.nCls = len(sets)
	r.cellMask = make([]uint64, cells)
	r.need = make([]int32, len(sets))
	for j, s := range sets {
		for c := range cells {
			if s[c>>6]&(1<<(c&63)) != 0 {
				r.cellMask[c] |= 1 << j
			}
		}
		for _, t := range r.rb {
			sub := true
			for w := range t {
				if t[w]&^s[w] != 0 {
					sub = false
					break
				}
			}
			if sub {
				r.need[j]++
			}
		}
	}
	n := len(start)
	r.matchOf = make([]int32, n)
	r.seen = make([]uint32, n)
	r.adj = make([][]int32, n)
	return r
}

// counts fills cnt[j] with the number of boxes of bs inside class j's set.
func (r *hubReach) counts(bs []uint16, cnt *[64]int32) {
	*cnt = [64]int32{}
	for _, b := range bs {
		for m := r.cellMask[b]; m != 0; m &= m - 1 {
			cnt[bits.TrailingZeros64(m)]++
		}
	}
}

// ok reports whether pulling a box from c to x keeps every class's
// requirement, given the node's counts.
func (r *hubReach) ok(c, x int32, cnt *[64]int32) bool {
	for m := r.cellMask[c] &^ r.cellMask[x]; m != 0; m &= m - 1 {
		j := bits.TrailingZeros64(m)
		if cnt[j]-1 < r.need[j] {
			return false
		}
	}
	return true
}

// feasible is the exact perfect-matching test: can the start boxes be matched
// to distinct squares of the hub (boxes as cells), each within its reach set?
func (r *hubReach) feasible(hub []int32) bool {
	n := len(r.rb)
	if len(hub) != n {
		return true
	}
	for i := range n {
		a := r.adj[i][:0]
		for j, h := range hub {
			if r.rb[i][h>>6]&(1<<(h&63)) != 0 {
				a = append(a, int32(j))
			}
		}
		if len(a) == 0 {
			return false
		}
		r.adj[i] = a
	}
	for j := range r.matchOf {
		r.matchOf[j] = -1
	}
	var try func(i int32) bool
	try = func(i int32) bool {
		for _, j := range r.adj[i] {
			if r.seen[j] == r.stamp {
				continue
			}
			r.seen[j] = r.stamp
			if r.matchOf[j] < 0 || try(r.matchOf[j]) {
				r.matchOf[j] = i
				return true
			}
		}
		return false
	}
	for i := range n {
		r.stamp++
		if !try(int32(i)) {
			return false
		}
	}
	return true
}
