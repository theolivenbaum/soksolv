package main

// Festival's inefficient positions (exp-272, deadlock.cpp
// is_inefficient_position, push mode): a box pushed into the inside of a
// one-wide tunnel, or parked beside a one-wide hole in a way that only blocks
// it, is pruned unless it lands on a goal or a target square. A heuristic
// (not a deadlock proof) that keeps Festival's forward search narrow.

// FestIneff enables the rule in the toward-hf search. Note (exp-273):
// Festival's own HF forward search never prunes with it (its imagined-board
// exemption tests the cell, not the box bit, so every square is exempt).
var FestIneff = false

// festTunnels marks Festival's forbidden push-tunnel squares.
func (s *solver) festTunnels() []bool {
	if s.festTun != nil {
		return s.festTun
	}
	lv := s.lv
	w := int32(lv.W)
	cells := int32(len(lv.Wall))
	tun := make([]bool, cells)
	wall := func(c int32) bool { return c < 0 || c >= cells || lv.Wall[c] }
	for _, ax := range [2][2]int32{{1, w}, {w, 1}} { // along, across
		along, across := ax[0], ax[1]
		for c := int32(0); c < cells; c++ {
			if wall(c) {
				continue
			}
			if p := c - along; wall(p-across) && !wall(p) && wall(p+across) {
				continue // middle of a tunnel
			}
			n := int32(0)
			for x := c; wall(x-across) && !wall(x) && wall(x+across); x += along {
				n++
			}
			if n < 2 {
				continue
			}
			goal := false
			for k := int32(0); k < n; k++ {
				goal = goal || lv.Goal[c+k*along]
				tun[c+k*along] = true
			}
			if !goal {
				tun[c] = false
			}
		}
	}
	s.festTun = tun
	return tun
}

// festInefficient: box just pushed to t (s.occ holds the child, the player's
// region is s.mark3 == s.pstamp).
func (s *solver) festInefficient(t int32) bool {
	lv := s.lv
	if lv.Goal[t] || s.tgt != nil && s.tgt[t] {
		return false // goals and target squares
	}
	if s.festTunnels()[t] {
		return true
	}
	w := int32(lv.W)
	d8 := [8]int32{-w, -w + 1, 1, w + 1, w, w - 1, -1, -w - 1}
	var c [8]int32
	for i, d := range d8 {
		c[i] = t + d
	}
	wall := func(i int) bool { return lv.Wall[c[i&7]] }
	reach := func(i int) bool { return s.mark3[c[i&7]] == s.pstamp }
	box := func(i int) bool { return s.occ[c[i&7]] }
	for j := 0; j < 8; j += 2 {
		if wall(j+2) && wall(j+3) && wall(j+5) && !wall(j+6) && (!reach(j+6) || reach(j)) {
			return true // types 1 and 3
		}
		if wall(j+3) && wall(j+5) && wall(j+6) && !wall(j+2) && (!reach(j+2) || reach(j)) {
			return true // types 2 and 4
		}
		if wall(j+3) && wall(j+5) && box(j+2) && !reach(j+6) {
			return true // type 5
		}
		if wall(j+3) && wall(j+5) && box(j+6) && !reach(j+2) {
			return true // type 6
		}
	}
	return false
}

// PFIneff (exp-696, festival-derived): Festival's is_inefficient_position in
// pull mode (deadlock.cpp) for the Festival-ordered pull solvers (pfOn:
// hub-pull, hub-gen's generator). A box may not come to rest inside a
// one-wide tunnel of length >= 2 (forbidden_pull_tunnel; a tunnel holding no
// start square keeps its first cell) or in one of two walled nooks; start
// squares are exempt. A heuristic prune, not a deadlock proof. DiveIneff
// applies the same rule to the dive lane.
var PFIneff = true // exp-696 promoted 2026-11-12 (default on)
var DiveIneff bool

// pfInefficient: a box just pulled to t, with s.occ holding the other boxes
// (from, the box's old cell, counts as free). s.lv is the pull view: lv.Goal
// marks the real start squares.
func (s *solver) pfInefficient(t, from int32) bool {
	lv := s.lv
	if lv.Goal[t] {
		return false
	}
	if s.festTunnels()[t] {
		return true
	}
	w := int32(lv.W)
	d8 := [8]int32{-w, -w + 1, 1, w + 1, w, w - 1, -1, -w - 1}
	wall := func(k int) bool { return lv.Wall[t+d8[k&7]] }
	for j := 0; j < 8; j += 2 {
		f := t + d8[j]
		if lv.Wall[f] || s.occ[f] && f != from {
			continue
		}
		f2 := f + d8[j]
		if f2 < 0 || int(f2) >= len(lv.Wall) || !lv.Wall[f2] {
			continue
		}
		if wall(j+1) && wall(j+3) && wall(j+4) {
			return true
		}
		if wall(j+4) && wall(j+5) && wall(j+7) {
			return true
		}
	}
	return false
}
