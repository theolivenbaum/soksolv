package main

import (
	"flag"
	"sync"
	"sync/atomic"
)

// exp-694 -hot-sides: Festival's player-side definition of the
// hotspot relation (graph.cpp init_push, hotspot.cpp), on our tables.
//
// The push graph has one state per (box cell c, side j): the player stands in
// the component of floor\{c} that contains neighbour c+dirs[j] (zone[c*4+j],
// 0 = that neighbour is a wall). Edges: switch sides inside one component
// (free), push the box from c to c-dirs[j] (the player ends on side j of the
// new cell). A lone box at x reaches goal g iff some state (x,*) reaches some
// (g,*). Blocker y hits x when y, a wall for box and player alike, removes a
// goal x reached without it. Rows are lazy and shared read-only across the
// fa2 engines of a level: the base reach sets (per goal) are built once.
var HotSides bool

func init() {
	flag.BoolVar(&HotSides, "hot-sides", true, "exp-694: fa2 engines use the player-side hotspot relation (Festival's (cell, side) push graph; rows lazy, shared per level); false restores the player-blind relation")
}

type hotSidesShared struct {
	lv   *Level
	base [][]uint64 // per goal: floor-index bitset of cells that reach it
	rows []atomic.Pointer[[]uint64]
}

var (
	hotSidesMu   sync.Mutex
	hotSidesLast *hotSidesShared
)

// sharedHotSides returns the per-level shared tables, building them once.
func sharedHotSides(s *solver) *hotSidesShared {
	hotSidesMu.Lock()
	defer hotSidesMu.Unlock()
	if hs := hotSidesLast; hs != nil && hs.lv == s.lv {
		return hs
	}
	hs := &hotSidesShared{lv: s.lv, rows: make([]atomic.Pointer[[]uint64], len(s.fcell))}
	sc := newSideScratch(s)
	zone := make([]int32, len(s.lv.Wall)*4)
	sc.zones(s, -1, zone)
	hs.base = make([][]uint64, len(s.goals))
	for g, goal := range s.goals {
		hs.base[g] = make([]uint64, s.nw)
		sc.reachGoal(s, zone, goal, hs.base[g])
	}
	hotSidesLast = hs
	return hs
}

// sideScratch is per-caller BFS scratch (the shared tables stay read-only).
type sideScratch struct {
	comp   []int32
	cstamp []uint32
	cs     uint32
	seen   []uint32
	ss     uint32
	q      []int32
}

func newSideScratch(s *solver) *sideScratch {
	nc := len(s.lv.Wall)
	return &sideScratch{comp: make([]int32, nc), cstamp: make([]uint32, nc), seen: make([]uint32, nc*4)}
}

func hsWall(lv *Level, blocker, c int32) bool {
	return c < 0 || int(c) >= len(lv.Wall) || lv.Wall[c] || c == blocker
}

// zones fills zone[c*4+j] for every non-wall c (blocker counts as a wall).
func (sc *sideScratch) zones(s *solver, blocker int32, zone []int32) {
	lv := s.lv
	clear(zone)
	for _, c := range s.fcell {
		if c == blocker {
			continue
		}
		sc.cs++
		lab := int32(0)
		for j, d := range s.dirs {
			n := c + d
			if hsWall(lv, blocker, n) {
				continue
			}
			if sc.cstamp[n] != sc.cs {
				lab++
				sc.cstamp[n], sc.comp[n] = sc.cs, lab
				q := append(sc.q[:0], n)
				for k := 0; k < len(q); k++ {
					for _, d2 := range s.dirs {
						m := q[k] + d2
						if hsWall(lv, blocker, m) || m == c || sc.cstamp[m] == sc.cs {
							continue
						}
						sc.cstamp[m], sc.comp[m] = sc.cs, lab
						q = append(q, m)
					}
				}
				sc.q = q
			}
			zone[int(c)*4+j] = sc.comp[n]
		}
	}
}

// reachGoal sets in out the floor index of every cell a lone box reaches
// goal from (reverse search from all of the goal's states).
func (sc *sideScratch) reachGoal(s *solver, zone []int32, goal int32, out []uint64) {
	sc.ss++
	st := sc.ss
	q := sc.q[:0]
	for j := range s.dirs {
		if zone[int(goal)*4+j] != 0 {
			sc.seen[int(goal)*4+j] = st
			q = append(q, goal*4+int32(j))
		}
	}
	for k := 0; k < len(q); k++ {
		c, j := q[k]/4, int(q[k]%4)
		fi := s.fidx[c]
		out[fi/64] |= 1 << uint(fi%64)
		zc := zone[int(c)*4+j]
		for t := range s.dirs {
			if t != j && zone[int(c)*4+t] == zc && sc.seen[int(c)*4+t] != st {
				sc.seen[int(c)*4+t] = st
				q = append(q, c*4+int32(t))
			}
		}
		// predecessor: the box was pushed here from c+dirs[j]
		p := c + s.dirs[j]
		if p >= 0 && int(p) < len(zone)/4 && zone[int(p)*4+j] != 0 && sc.seen[int(p)*4+j] != st {
			sc.seen[int(p)*4+j] = st
			q = append(q, p*4+int32(j))
		}
	}
	sc.q = q
}

// rowSides is hotspotTable.row under -hot-sides.
func (h *hotspotTable) rowSides(s *solver, blocker int32) []uint64 {
	hs := sharedHotSides(s)
	idx := s.fidx[blocker]
	if p := hs.rows[idx].Load(); p != nil {
		return *p
	}
	row := make([]uint64, s.nw)
	if !s.lv.Goal[blocker] {
		if h.sc == nil {
			h.sc = newSideScratch(s)
			h.zone = make([]int32, len(s.lv.Wall)*4)
			h.with = make([]uint64, s.nw)
		}
		h.sc.zones(s, blocker, h.zone)
		for g, goal := range s.goals {
			clear(h.with)
			h.sc.reachGoal(s, h.zone, goal, h.with)
			for k, w := range hs.base[g] {
				row[k] |= w &^ h.with[k]
			}
		}
		row[idx/64] &^= 1 << uint(idx%64)
		for _, g := range s.goals {
			fi := s.fidx[g]
			row[fi/64] &^= 1 << uint(fi%64)
		}
		for fi, c := range s.fcell {
			if s.dist[c] < 0 {
				row[fi/64] &^= 1 << uint(fi%64)
			}
		}
	}
	hs.rows[idx].Store(&row)
	return row
}
