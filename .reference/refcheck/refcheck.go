// Package refcheck is a slow, obviously-correct reference for single-box move legality
// (exp-381). Test code only: it is not imported by the solver.
package refcheck

import "slices"

// Board is a padded grid (outer ring wall) with the other boxes fixed.
type Board struct {
	Wall []bool
	Occ  []bool
	Dirs [4]int32 // u d l r
}

func New(wall []bool, w int) *Board {
	return &Board{Wall: wall, Occ: make([]bool, len(wall)), Dirs: [4]int32{-int32(w), int32(w), -1, 1}}
}

// Flood returns the cells the player reaches from `from` (the single moving box sits on `box`).
func (r *Board) Flood(from, box int32) []bool {
	reach := make([]bool, len(r.Wall))
	if r.Wall[from] || r.Occ[from] || from == box {
		return reach
	}
	q := []int32{from}
	reach[from] = true
	for i := 0; i < len(q); i++ {
		for _, d := range r.Dirs {
			y := q[i] + d
			if !r.Wall[y] && !r.Occ[y] && y != box && !reach[y] {
				reach[y] = true
				q = append(q, y)
			}
		}
	}
	return reach
}

// Ends returns every reachable (box, player-cell) pair when only one box moves. Pushes: the
// player at b-d moves the box to b+d. Pulls: the player at p with the box at p+d steps to p-d
// and the box follows to p.
func (r *Board) Ends(b0, p0 int32, pull bool) map[[2]int32]bool {
	seen := map[[2]int32]bool{}
	type st struct{ b, p int32 }
	var q []st
	for c, ok := range r.Flood(p0, b0) {
		if ok {
			q = append(q, st{b0, int32(c)})
		}
	}
	add := func(nb, np int32) {
		for c, ok := range r.Flood(np, nb) {
			if ok && !seen[[2]int32{nb, int32(c)}] {
				q = append(q, st{nb, int32(c)})
			}
		}
	}
	for i := 0; i < len(q); i++ {
		s := q[i]
		k := [2]int32{s.b, s.p}
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, d := range r.Dirs {
			if !pull {
				if s.p == s.b-d && !r.Wall[s.b+d] && !r.Occ[s.b+d] {
					add(s.b+d, s.b)
				}
			} else if s.p+d == s.b {
				if np := s.p - d; !r.Wall[np] && !r.Occ[np] {
					add(s.p, np)
				}
			}
		}
	}
	return seen
}

// Edge describes the difference between a parent and a child state.
type Edge struct {
	From, To int32 // moved box (From == -1: no box left its square)
	Multi    bool  // more than one box moved
}

func Diff(pc, cc []int32) Edge {
	e := Edge{From: -1, To: -1}
	for _, x := range pc {
		if !slices.Contains(cc, x) {
			if e.From >= 0 {
				e.Multi = true
			}
			e.From = x
		}
	}
	for _, x := range cc {
		if !slices.Contains(pc, x) {
			e.To = x
		}
	}
	return e
}

// EdgeOK reports whether the reference can turn the parent (boxes pc, player region of pp) into
// the child (boxes cc, player in the region of cp) by moving the one box that differs.
// checked=false when no box changed square (nothing to verify).
func EdgeOK(wall []bool, w int, pc []int32, pp int32, cc []int32, cp int32, pull bool) (ok, checked bool, e Edge) {
	e = Diff(pc, cc)
	if e.Multi {
		return false, true, e
	}
	if e.From < 0 {
		return true, false, e
	}
	b := New(wall, w)
	for _, x := range pc {
		if x != e.From {
			b.Occ[x] = true
		}
	}
	ends := b.Ends(e.From, pp, pull)
	childReach := b.Flood(cp, e.To)
	for k := range ends {
		if k[0] == e.To && childReach[k[1]] {
			return true, true, e
		}
	}
	return false, true, e
}

// regionMin is the smallest cell of the player's region (box on `box`): a canonical region id.
func (r *Board) regionMin(from, box int32) int32 {
	for c, ok := range r.Flood(from, box) {
		if ok {
			return int32(c)
		}
	}
	return -1
}

// Alive returns the cells a lone box can be pushed from to some goal on the empty board with the
// player free to walk anywhere (simple deadlock squares are the complement).
func Alive(wall, goal []bool, w int) []bool {
	b := New(wall, w)
	alive := make([]bool, len(wall))
	for g, isGoal := range goal {
		if !isGoal || wall[g] {
			continue
		}
		alive[g] = true
		for _, d := range b.Dirs {
			if p := int32(g) + d; !wall[p] { // each neighbour: the goal can split the floor into regions
				for k := range b.Ends(int32(g), p, true) {
					alive[k[0]] = true
				}
			}
		}
	}
	return alive
}

// PushEndpoints is the macro-move endpoint set for the box on b0 with the player in the region
// of p0 and the other boxes in Occ: every (box cell, region id) state reachable by one or more
// pushes of that box alone, where the box may only stand on cells with alive[cell] (nil: all
// floor). A region id is the smallest cell of the player's region.
func (r *Board) PushEndpoints(b0, p0 int32, alive []bool) map[[2]int32]bool {
	type st struct{ b, reg int32 }
	res := map[[2]int32]bool{}
	start := st{b0, r.regionMin(p0, b0)}
	seen := map[st]bool{start: true}
	q := []st{start}
	for i := 0; i < len(q); i++ {
		s := q[i]
		reach := r.Flood(s.reg, s.b)
		for _, d := range r.Dirs {
			nb := s.b + d
			if !reach[s.b-d] || r.Wall[nb] || r.Occ[nb] || alive != nil && !alive[nb] {
				continue
			}
			ns := st{nb, r.regionMin(s.b, nb)}
			res[[2]int32{ns.b, ns.reg}] = true
			if !seen[ns] {
				seen[ns] = true
				q = append(q, ns)
			}
		}
	}
	return res
}

// PullEndpoints is PushEndpoints for pull moves: the player in the region stands on b+d, with
// b+2d free, and drags the box from b to b+d.
func (r *Board) PullEndpoints(b0, p0 int32, alive []bool) map[[2]int32]bool {
	type st struct{ b, reg int32 }
	res := map[[2]int32]bool{}
	start := st{b0, r.regionMin(p0, b0)}
	seen := map[st]bool{start: true}
	q := []st{start}
	for i := 0; i < len(q); i++ {
		s := q[i]
		reach := r.Flood(s.reg, s.b)
		for _, d := range r.Dirs {
			nb, back := s.b+d, s.b+2*d
			if !reach[nb] || r.Wall[back] || r.Occ[back] || alive != nil && !alive[nb] {
				continue
			}
			ns := st{nb, r.regionMin(back, nb)}
			res[[2]int32{ns.b, ns.reg}] = true
			if !seen[ns] {
				seen[ns] = true
				q = append(q, ns)
			}
		}
	}
	return res
}
