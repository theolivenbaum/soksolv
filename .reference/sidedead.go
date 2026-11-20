package main

// SideDead enables the player-side dead-square check in FESS.
var SideDead = true

// sideDeadAfter reports whether, after box i of cur moves to t with the
// player in the region touching t on side canonical, some box off a goal is
// side-dead. The caller has s.occ set for the child.
func (s *solver) sideDeadAfter(cur []int32, i int, t, canonical int32) bool {
	p := t - s.dirs[canonical]
	if s.mark3[p] != s.pstamp {
		return false // no player cell beside t known: skip the check
	}
	for j, c := range cur {
		if j == i {
			c = t
		}
		if !s.lv.Goal[c] && s.sd.Dead(c, p) {
			return true
		}
	}
	return false
}

// PairSide enables pair deadlocks evaluated from the player's actual side.
var PairSide = true

// CorralDead (exp-227; 0: off) proves corral deadlocks of each applied push
// with a zone search of at most this many states (package deadzone).
var CorralDead = 200

// SharedCorral shares corral verdicts between searchers (exp-228).
var SharedCorral = true

// ClusterDead enables cluster deadlocks (package cluster) in FESS and full.
var ClusterDead = true

// InefficientPrune skips pushes that park a box over a one-wide hole
// (Festival's "inefficient positions"; heuristic, not a deadlock proof).
var InefficientPrune = false

// inefficient reports whether pushing a box to t, with the player ending on
// player, leaves it on top of a one-wide hole: both lower diagonals walls,
// a wall or box on one side, and the box didn't arrive horizontally from the
// open side. Such a box blocks the hole's mouth and can only go back the way
// it came. Goals and plan squares are never pruned.
func (s *solver) inefficient(t, player int32) bool {
	lv := s.lv
	if lv.Goal[t] || s.inPlan(t) {
		return false
	}
	w := int32(lv.W)
	type rot struct{ d, p1, p2 int32 }
	for _, r := range [4]rot{{w, -1, 1}, {-w, -1, 1}, {1, -w, w}, {-1, -w, w}} {
		if !lv.Wall[t+r.d+r.p1] || !lv.Wall[t+r.d+r.p2] || lv.Wall[t+r.d] {
			continue // not above a one-wide hole in direction d
		}
		blocked1 := lv.Wall[t+r.p1] || s.occ[t+r.p1]
		blocked2 := lv.Wall[t+r.p2] || s.occ[t+r.p2]
		switch {
		case blocked1 && blocked2:
			return true
		case blocked1 && player != t+r.p2, blocked2 && player != t+r.p1:
			return true
		}
	}
	return false
}

// inPlan reports whether c is a required square of any packing-plan phase:
// one bit per cell, set from the plan's phases on first use (the plan is
// fixed for the search).
func (s *solver) inPlan(c int32) bool {
	if s.plan == nil {
		return false
	}
	if s.planBits == nil {
		s.planBits = make([]uint64, (len(s.lv.Wall)+63)/64)
		for _, ph := range s.plan.phases {
			for _, x := range ph.required {
				s.planBits[x>>6] |= 1 << (x & 63)
			}
		}
	}
	return s.planBits[c>>6]>>(c&63)&1 != 0
}
