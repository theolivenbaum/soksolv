package main

import (
	"encoding/binary"
	"slices"
)

// PFZoneDead (exp-609, 0: off): a Festival-ordered pull search (festx/festb)
// drops each start zone (pull root) that a bounded exhaustive pull search of
// at most this many positions proves dead, as Festival's
// start_zone_is_deadlocked does with its 1000-position wobblers search. The
// pull roots are the player zones of the solved position; a zone is dead when
// every pull sequence from it is exhausted without putting all boxes on start
// squares, so no solution can end with the player there. If every zone would
// be dropped, none is (the filter never empties the root set).
var PFZoneDead = 1000 // exp-619 promoted 2026-11-11

// pullZoneDead reports whether the pull view's position (boxes, player) can
// provably never put every box on a start square. It searches single pulls
// breadth-first over relaxed positions: a box with a reversible pull (after
// the pull the player can reach the box's old square and pull it back) is deleted
// instead of moved, as in Festival's wobblers search. Deleting boxes only
// removes obstacles and goal obligations, so every real pull sequence maps onto
// the relaxed search, and exhausting it without all remaining boxes on start
// squares proves the position dead. The only other pruning is dead squares
// (dist < 0: no start square is reachable by pulls). Exceeding limit relaxed
// positions answers "not dead".
func pullZoneDead(wall []bool, w int, start []bool, dist []int32, boxes []int32, player int32, limit int) bool {
	cells := len(wall)
	dirs := [4]int32{-int32(w), int32(w), -1, 1}
	occ := make([]bool, cells)
	mark := make([]int32, cells)
	stamp := int32(0)
	var q []int32
	// reach floods the player's region (occ holds the boxes) and returns its
	// smallest cell; mark[c] == stamp marks the region.
	reach := func(p int32) int32 {
		stamp++
		mark[p] = stamp
		q = append(q[:0], p)
		lo := p
		for i := 0; i < len(q); i++ {
			for _, d := range dirs {
				if y := q[i] + d; !wall[y] && !occ[y] && mark[y] != stamp {
					mark[y] = stamp
					q = append(q, y)
					lo = min(lo, y)
				}
			}
		}
		return lo
	}
	onStart := func(bx []int32) bool {
		for _, b := range bx {
			if !start[b] {
				return false
			}
		}
		return true
	}
	seen := map[string]struct{}{}
	var key []byte
	enc := func(bx []int32, p int32) string {
		key = key[:0]
		for _, b := range bx {
			key = binary.LittleEndian.AppendUint32(key, uint32(b))
		}
		key = binary.LittleEndian.AppendUint32(key, uint32(p))
		return string(key)
	}
	type pos struct {
		boxes  []int32 // sorted
		player int32   // smallest cell of the player's region
	}
	var fifo []pos
	// push records a new relaxed position; it reports a solved one.
	push := func(bx []int32, p int32) (solved bool) {
		k := enc(bx, p)
		if _, ok := seen[k]; ok {
			return false
		}
		if onStart(bx) {
			return true
		}
		seen[k] = struct{}{}
		fifo = append(fifo, pos{bx, p})
		return false
	}
	root := slices.Clone(boxes)
	slices.Sort(root)
	for _, b := range root {
		if dist[b] < 0 {
			return true // a box already on a dead square
		}
	}
	for _, b := range root {
		occ[b] = true
	}
	rp := reach(player)
	for _, b := range root {
		occ[b] = false
	}
	if push(root, rp) {
		return false
	}
	for head := 0; head < len(fifo); head++ {
		if len(seen) > limit {
			return false // not proven within the limit
		}
		cur := fifo[head]
		for _, b := range cur.boxes {
			occ[b] = true
		}
		reach(cur.player)
		region := stamp
		// Children: every pull, unless some box has a reversible pull; then
		// the only child deletes that box (a relaxation of every child).
		type pull struct {
			i      int
			to, np int32
		}
		var pulls []pull
		wobbler := -1
		for i, b := range cur.boxes {
			for _, d := range dirs {
				// The player on b+d steps to b+2d, pulling the box onto b+d.
				p1, p2 := b+d, b+2*d
				if mark[p1] != region || wall[p2] || occ[p2] || dist[p1] < 0 {
					continue
				}
				occ[b], occ[p1] = false, true
				np := reach(p2)
				back := mark[b] == stamp && !wall[b-d] && !occ[b-d] // the player can reach b and pull the box back onto it
				occ[p1], occ[b] = false, true
				reach(cur.player) // restore the parent's region marks
				region = stamp
				if back {
					wobbler = i
					break
				}
				pulls = append(pulls, pull{i, p1, np})
			}
			if wobbler >= 0 {
				break
			}
		}
		if wobbler >= 0 {
			bx := slices.Delete(slices.Clone(cur.boxes), wobbler, wobbler+1)
			occ[cur.boxes[wobbler]] = false
			np := reach(cur.player)
			for _, b := range cur.boxes {
				occ[b] = false
			}
			if push(bx, np) {
				return false
			}
			continue
		}
		for _, b := range cur.boxes {
			occ[b] = false
		}
		for _, pl := range pulls {
			bx := slices.Clone(cur.boxes)
			bx[pl.i] = pl.to
			slices.Sort(bx)
			if push(bx, pl.np) {
				return false
			}
		}
	}
	return true
}

// liveZoneRoots drops the pull roots whose zone pullZoneDead proves dead
// (PFZoneDead positions each), keeping all of them if every one is dead.
func (s *solver) liveZoneRoots(roots []int32) []int32 {
	start := slices.Clone(s.lv.Boxes)
	var live []int32
	for _, r := range roots {
		if !pullZoneDead(s.lv.Wall, s.lv.W, s.lv.Goal, s.dist, start, r, s.pfZoneDead()) {
			live = append(live, r)
		}
	}
	if s.tr != nil {
		s.tr.SetExtra("zones_dead", len(roots)-len(live))
	}
	if len(live) == 0 {
		return roots
	}
	return live
}
