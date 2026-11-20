package main

import (
	"flag"
	"slices"
	"sync/atomic"
)

// exp-681b -frozen-hall: a sound deadlock rule from the exp-681 refuter. When
// a push leaves its box frozen on a goal, the frozen-on-goal boxes are walls
// for good; then every loose box must still be matchable to a distinct open
// goal it can reach alone (side-aware lone-box relation: a box on a cut cell
// only from the side region the player is in). A deficit proves the child
// dead. The relation tables depend only on the frozen set, so they are cached
// per frozen set (exact key).

var FrozenHall bool
var FrozenHallCache = 128

// frozenHallChecks counts matchings run (tests: evidence the rule was exercised).
var frozenHallChecks atomic.Int64

func init() {
	flag.BoolVar(&FrozenHall, "frozen-hall", false, "exp-681b: prune a push that freezes its box on a goal when the frozen-walled, player-side lone-box matching has a deficit (sound; tables cached per frozen set)")
}

type frozenHallEntry struct {
	cells []int32
	h     *fa2Hall
	k     int
}

type frozenHallCache struct {
	m    map[uint64][]*frozenHallEntry
	n    int
	fz   []int32
	bx   []int32
	mm   hallM
	hits int64
	miss int64
}

func (s *solver) frozenHallDead(cur []int32, i int, t, side int32, inRegion bool) bool {
	if !s.lv.Goal[t] {
		return false
	}
	off := false
	if !s.frozen(t, &off) || off {
		return false // not frozen, or frozen off goal (freezeDeadlock's case)
	}
	c := s.fhc
	if c == nil {
		c = &frozenHallCache{m: map[uint64][]*frozenHallEntry{}}
		s.fhc = c
	}
	c.bx = append(c.bx[:0], cur...)
	c.bx[i] = t
	c.fz = c.fz[:0]
	for _, b := range c.bx {
		o := false
		if s.frozen(b, &o) {
			if o {
				return false // left to freezeDeadlock
			}
			c.fz = append(c.fz, b)
		}
	}
	slices.Sort(c.fz)
	var key uint64 = 0x9E3779B97F4A7C15
	for _, b := range c.fz {
		key = (key ^ uint64(b)) * 0x100000001B3
	}
	var e *frozenHallEntry
	for _, x := range c.m[key] {
		if slices.Equal(x.cells, c.fz) {
			e = x
			break
		}
	}
	if e == nil {
		c.miss++
		if c.n >= FrozenHallCache {
			clear(c.m)
			c.n = 0
		}
		fp := &festPlan{}
		for _, b := range c.fz {
			fp.e = append(fp.e, festEntry{from: -1, to: b, until: -1})
		}
		h := newFA2HallOpt(s, fp, true, false)
		e = &frozenHallEntry{cells: slices.Clone(c.fz), h: h, k: len(h.permTo)}
		c.m[key] = append(c.m[key], e)
		c.n++
	} else {
		c.hits++
	}
	if e.h.off {
		return false
	}
	if len(c.mm.mb) != len(c.bx) || len(c.mm.mg) != e.h.ng {
		c.mm = hallM{mb: make([]int32, len(c.bx)), mg: make([]int32, e.h.ng)}
	}
	c.mm.reset()
	frozenHallChecks.Add(1)
	p := int32(-1)
	if inRegion {
		p = side
	}
	return e.h.solve(e.k, c.bx, &c.mm, p) > 0
}
