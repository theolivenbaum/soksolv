package main

import (
	"slices"
	"time"
)

// Packing compilation has a deterministic work cap in addition to the solve's
// wall deadline. It keeps no alive/dead result cache and never prunes a move.
const packingRouteVisitCap = 8_000_000

type packingRoute struct {
	s                     *solver
	fixed, touched        []bool
	mark, seen            []uint32
	stamp, generation     uint32
	prev, walkPrev        []int32
	q, states, path, used []int32
	visits                int
}

func newPackingRoute(s *solver) *packingRoute {
	n := len(s.lv.Wall)
	return &packingRoute{s: s, fixed: make([]bool, n), touched: make([]bool, n), mark: make([]uint32, n), seen: make([]uint32, 4*n), prev: make([]int32, 4*n), walkPrev: make([]int32, n)}
}

func (r *packingRoute) flood(box, player int32) (int32, bool) {
	if player < 0 || r.s.lv.Wall[player] || r.fixed[player] || player == box {
		return 0, false
	}
	r.stamp++
	st := r.stamp
	r.mark[player] = st
	r.q = append(r.q[:0], player)
	norm := player
	for head := 0; head < len(r.q); head++ {
		x := r.q[head]
		norm = min(norm, x)
		r.visits++
		if r.visits >= packingRouteVisitCap {
			return 0, false
		}
		if r.visits&1023 == 0 && (Stop.Load() || NodeBudget == 0 && !r.s.deadline.IsZero() && time.Now().After(r.s.deadline)) {
			return 0, false
		}
		for _, d := range r.s.dirs {
			y := x + d
			if !r.s.lv.Wall[y] && !r.fixed[y] && y != box && r.mark[y] != st {
				r.mark[y] = st
				r.walkPrev[y] = x
				r.q = append(r.q, y)
			}
		}
	}
	return norm, true
}

func (r *packingRoute) note(c int32) {
	if !r.touched[c] {
		r.touched[c] = true
		r.used = append(r.used, c)
	}
}

func (r *packingRoute) walk(box, from, to int32) bool {
	if _, ok := r.flood(box, from); !ok || r.mark[to] != r.stamp {
		return false
	}
	for x := to; ; x = r.walkPrev[x] {
		r.note(x)
		if x == from {
			break
		}
	}
	return true
}

// compile reconstructs a primitive pull witness with all other boxes fixed,
// then records its swept box/support/walking cells. Reversing these routes
// supplies the forward resource dependencies; sink supply stays a relaxation.
func (r *packingRoute) compile(parent, child packingPhase) bool {
	clear(r.fixed)
	clear(r.touched)
	r.used = r.used[:0]
	m := child.reverseMove
	for _, b := range parent.required {
		if b != m.from {
			r.fixed[b] = true
		}
	}
	if _, ok := r.flood(m.from, parent.player); !ok {
		return false
	}
	r.generation++
	g := r.generation
	r.states = r.states[:0]
	for di, d := range r.s.dirs {
		if r.mark[m.from+d] == r.stamp {
			k := m.from*4 + int32(di)
			r.seen[k] = g
			r.prev[k] = -1
			r.states = append(r.states, k)
		}
	}
	last := int32(-2)
	if m.from == m.to {
		box := m.from
		if m.remove {
			box = -1
		}
		if norm, ok := r.flood(box, parent.player); !ok {
			return false
		} else if norm == child.player {
			last = -1
		}
	}
	for head := 0; last == -2 && head < len(r.states); head++ {
		k := r.states[head]
		b, d := k/4, r.s.dirs[k%4]
		to, player := b+d, b+2*d
		if r.s.lv.Wall[to] || r.fixed[to] || r.s.lv.Wall[player] || r.fixed[player] {
			continue
		}
		norm, ok := r.flood(to, player)
		if !ok {
			return false
		}
		if to == m.to {
			if m.remove {
				norm, ok = r.flood(-1, player)
				if !ok {
					return false
				}
			}
			if norm == child.player {
				last = k
				break
			}
			if m.remove {
				if _, ok = r.flood(to, player); !ok {
					return false
				}
			}
		}
		for di, off := range r.s.dirs {
			kk := to*4 + int32(di)
			if r.mark[to+off] == r.stamp && r.seen[kk] != g {
				r.seen[kk] = g
				r.prev[kk] = k
				r.states = append(r.states, kk)
			}
		}
	}
	if last == -2 {
		return false
	}
	r.path = r.path[:0]
	for k := last; k >= 0; k = r.prev[k] {
		r.path = append(r.path, k)
	}
	slices.Reverse(r.path)
	player, box := parent.player, m.from
	r.note(box)
	for _, k := range r.path {
		if box != k/4 {
			return false
		}
		d := r.s.dirs[k%4]
		if !r.walk(box, player, box+d) {
			return false
		}
		r.note(box + d)
		r.note(box + 2*d)
		player = box + 2*d
		box += d
	}
	if box != m.to {
		return false
	}
	if m.remove {
		box = -1
	}
	return r.walk(box, player, child.player)
}
