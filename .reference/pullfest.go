package main

import "slices"

// Festival's backward packing search order (exp-275, strategy B's
// MAX_DIST_SEARCH2): pull states rank by fewer floor regions, fewer broken
// room connections, then (with one region) fewer boxes that cannot be pulled
// from any side, then the largest min-cost matching distance from the boxes
// back to the real goals. One advisor (the best child better than its parent)
// costs 0, every other move 1. Cells are (regions, rooms) of the best score
// on the path; the best-so-far node compares those scores. Equal children
// keep generation order (box, then destination, in board order).

// pfStyle (exp-283): a Festival-ordered pull search's variant, per solver so
// lanes with different styles can run together.
type pfStyle struct {
	spread, bicon bool // Festival D/E distance key; E/F biconnectivity key
	types         int  // type-based exploration period (0: off)
	// rootBare (exp-285): root moves are scored with no other boxes on the
	// board (regions, rooms, hotspots of an empty floor: in effect ranked by
	// distance alone). Before exp-285 every Festival-ordered pull search did
	// this by accident; it solves a different set (Sven-172 vs Sasq III-36).
	rootBare bool
}

func (s *solver) pfAdvFix() bool    { return PFAdvFix }
func (s *solver) pfFestAdv() bool   { return PFFestAdv }
func (s *solver) pfExactDist() bool { return PFExactDist }
func (s *solver) pfFestRooms() bool { return PFFestRooms }
func (s *solver) pfZoneDead() int   { return PFZoneDead }

type pfTuple struct{ reg, rooms, hot, dist, bic int32 } // bic: PFBicon's count (0 unless one region)

// pfBetter: Festival's pull HF order (dist: larger is better; -1 = no matching).
func pfBetter(a, b pfTuple) bool {
	if a.reg != b.reg {
		return a.reg < b.reg
	}
	if a.rooms != b.rooms {
		return a.rooms < b.rooms
	}
	if a.reg == 1 && a.hot != b.hot {
		return a.hot < b.hot
	}
	if a.bic != b.bic {
		return a.bic < b.bic
	}
	return a.dist > b.dist
}

// pfInit builds the push distances from every cell to every real goal (the
// pull view's boxes at the root) used for the matching distance.
func (s *solver) pfInit() {
	if s.pfDist != nil {
		return
	}
	lv := s.lv
	cells := len(lv.Wall)
	goals := slices.Clone(lv.Boxes) // pull view: boxes start on the real goals
	slices.Sort(goals)
	s.pfDist = make([]int32, len(goals)*cells)
	for i := range s.pfDist {
		s.pfDist[i] = -1
	}
	if s.pfExactDist() {
		// exp-285: Festival's distance_from_to: pushes of one box on the
		// empty board, the player walking around the box between pushes.
		gi := make([]int32, cells)
		for i := range gi {
			gi[i] = -1
		}
		for g, goal := range goals {
			gi[goal] = int32(g)
		}
		pushDistances(lv.Wall, s.dirs[:], func(src, c, d int32) {
			if g := gi[c]; g >= 0 {
				s.pfDist[int(g)*cells+int(src)] = d
			}
		})
		return
	}
	for g, goal := range goals {
		d := s.pfDist[g*cells : (g+1)*cells]
		d[goal] = 0
		q := []int32{goal}
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, dir := range s.dirs {
				b := c - dir // a box on b pushed by a player on b-dir lands on c
				if !lv.Wall[b] && !lv.Wall[b-dir] && d[b] < 0 {
					d[b] = d[c] + 1
					q = append(q, b)
				}
			}
		}
	}
}

// pfHot counts boxes no player can pull: no side with a floor neighbour and a
// free cell beyond it (Festival's can_be_accessed_from_some_direction).
func (s *solver) pfHot(boxes []int32) int32 {
	n := int32(0)
	for _, b := range boxes {
		ok := false
		for _, d := range s.dirs {
			if !s.lv.Wall[b+d] && !s.lv.Wall[b+2*d] && !s.occ[b+2*d] {
				ok = true
				break
			}
		}
		if !ok {
			n++
		}
	}
	return n
}

func (s *solver) pfMatch(boxes []int32) int32 {
	s.pfInit()
	if d := s.assignTo(boxes, s.pfDist); d >= 0 {
		return d
	}
	return -1
}

// pfScore turns a tuple into the fessScore FESS sorts by (lower first).
// The mobility key packs the biconnectivity count (fewer first) above the
// matching distance (larger first), shifted by one so no matching (-1) is 0.
func pfScore(t pfTuple) fessScore {
	return fessScore{oop: int(t.reg), room: int(t.rooms), hot: int(t.hot), mobility: (4095-int(min(t.bic, 4095)))<<16 | int(min(t.dist+1, 65535))}
}

// pfFromMove inverts pfScore for a stored move score.
func pfFromMove(ms moveScore) pfTuple {
	return pfTuple{int32(ms.oop), int32(ms.room), int32(ms.hot), int32(ms.mobility&0xffff) - 1, 4095 - int32(ms.mobility>>16)}
}

// pfFill adds the hotspot and biconnectivity counts of a one-region state
// (s.occ holds its boxes).
func (s *solver) pfFill(pt *pfTuple, boxes []int32) {
	if s.pfFestRooms() {
		pt.rooms = int32(s.festRooms())
	}
	if s.pfSty.spread && pt.dist >= 0 {
		pt.dist = s.pfSpreadSum(boxes)
	}
	if pt.reg != 1 {
		return
	}
	pt.hot = s.pfHot(boxes)
	if s.pfSty.bicon {
		pt.bic = s.pfBicon()
	}
}

// pfAdd records a new pull node's tuple and returns its bucket.
func (s *solver) pfAdd(boxes []int32, parent int32, reg int) int {
	var own pfTuple
	if s.pfHave {
		own = s.pfNext
	} else {
		// Score this board alone, then restore the caller's occupancy
		// (exp-285: clearing it scored every root's moves on an empty board).
		saved := slices.Clone(s.occ)
		clear(s.occ)
		for _, b := range boxes {
			s.occ[b] = true
		}
		own = pfTuple{reg: int32(reg), rooms: int32(s.roomConnectivity()), dist: s.pfMatch(boxes)}
		s.pfFill(&own, boxes)
		copy(s.occ, saved)
		if !RootBareScore && s.pfSty.rootBare {
			// pfStyle.rootBare (buggy exp-285): the caller's board loses these boxes, so a
			// root's moves are ranked on the board without boxes.
			for _, b := range boxes {
				s.occ[b] = false
			}
		}
	}
	best := own
	if parent >= 0 && !pfBetter(own, s.pfBest[parent]) {
		best = s.pfBest[parent]
	}
	s.pfOwn = append(s.pfOwn, own)
	s.pfBest = append(s.pfBest, best)
	return s.project(featureCell{oop: int(best.reg), connectivity: int(best.rooms)}, -1)
}

func (s *solver) pfReparent(id, parent int32) int {
	best := s.pfOwn[id]
	if parent >= 0 && !pfBetter(best, s.pfBest[parent]) {
		best = s.pfBest[parent]
	}
	s.pfBest[id] = best
	return s.project(featureCell{oop: int(best.reg), connectivity: int(best.rooms)}, -1)
}

// pfBicon (exp-281, Festival strategies E/F) counts free floor squares on no cycle of the free-floor graph
// (s.occ holds the boxes), skipping free real goals (Festival's
// biconnect_score).
func (s *solver) pfBicon() int32 {
	cells := len(s.lv.Wall)
	if s.bcDisc == nil {
		s.bcDisc, s.bcLow, s.bcOn = make([]int32, cells), make([]int32, cells), make([]uint32, cells)
	}
	s.bcStamp += 2
	vis, on := s.bcStamp, s.bcStamp+1 // bcOn: vis = visited, on = on a cycle
	disc, low := s.bcDisc, s.bcLow
	t := int32(0)
	for root := range int32(cells) {
		if s.lv.Wall[root] || s.occ[root] || s.bcOn[root] >= vis {
			continue
		}
		// Iterative DFS: the stack holds cell, parent and next direction.
		st := append(s.bcStack[:0], root, -1, 0)
		s.bcOn[root] = vis
		disc[root], low[root] = t, t
		t++
		for len(st) > 0 {
			k := len(st) - 3
			u, par, d := st[k], st[k+1], st[k+2]
			if d < 4 {
				st[k+2]++
				v := u + s.dirs[d]
				if s.lv.Wall[v] || s.occ[v] || v == par {
					continue
				}
				if s.bcOn[v] >= vis {
					low[u] = min(low[u], disc[v]) // back edge: both on a cycle
					s.bcOn[u], s.bcOn[v] = on, on
					continue
				}
				s.bcOn[v] = vis
				disc[v], low[v] = t, t
				t++
				st = append(st, v, u, 0)
				continue
			}
			st = st[:k]
			if par >= 0 {
				low[par] = min(low[par], low[u])
				if low[u] <= disc[par] { // tree edge on a cycle
					s.bcOn[u], s.bcOn[par] = on, on
				}
			}
		}
		s.bcStack = st
	}
	n := int32(0)
	for c := range cells {
		if !s.lv.Wall[c] && !s.occ[c] && s.bcOn[c] != on && !s.pfGoal(int32(c)) {
			n++
		}
	}
	return n
}

// pfGoal: c is a real goal (a box square of the pull view's root).
func (s *solver) pfGoal(c int32) bool {
	if s.pfGoals == nil {
		s.pfGoals = make([]bool, len(s.lv.Wall))
		for _, g := range s.lv.Boxes {
			s.pfGoals[g] = true
		}
	}
	return s.pfGoals[c]
}

// pfSpreadSum is the scored distance of boxes (a cell's distances to every
// real goal, summed, computed once).
func (s *solver) pfSpreadSum(boxes []int32) int32 {
	if s.pfScored == nil {
		s.pfInit()
		cells := len(s.lv.Wall)
		s.pfScored = make([]int32, cells)
		for g := 0; g*cells < len(s.pfDist); g++ {
			for c, d := range s.pfDist[g*cells : (g+1)*cells] {
				if d > 0 {
					s.pfScored[c] += d
				}
			}
		}
	}
	sum := int32(0)
	for _, b := range boxes {
		sum += s.pfScored[b]
	}
	return sum
}

// PFAdvFix (exp-605): the Festival-ordered pull search's parent score carries
// its region count, so its one advisor (the best child better than the parent)
// exists without a region merge, as in Festival's set_default_advisor.
var PFAdvFix = true // exp-619 promoted 2026-11-11

// PFFestAdv (exp-609): the Festival-ordered pull advisor follows Festival's
// set_default_advisor exactly: equal-scored children tie in board order (box,
// then destination) rather than our flood order, and children already in the
// tree are never the advisor (their zero weight would be spent on a duplicate).
var PFFestAdv = true // exp-619 promoted 2026-11-11

// PFExactDist (exp-285): Festival-ordered pull uses Festival's push
// distances (player access around the box) instead of the box-only BFS.
var PFExactDist = true // exp-619 promoted 2026-11-11

// pushDistances calls visit(src, c, d) for every floor cell src and every
// cell c a lone box on src can be pushed to, d pushes away: a BFS over (box
// cell, player side) on the empty board; the player starts on any side and
// walks around the box between pushes.
func pushDistances(wall []bool, dirs []int32, visit func(src, c, d int32)) {
	cells := int32(len(wall))
	// side[c*4+k]: region label of the floor neighbour c+dirs[k] with a box on
	// c (-1: wall); sides with equal labels are mutually reachable.
	side := make([]int32, cells*4)
	mark := make([]int32, cells)
	stamp := int32(0)
	var q []int32
	for c := range cells {
		for k := range 4 {
			side[c*4+int32(k)] = -1
		}
		if wall[c] {
			continue
		}
		for k, d := range dirs {
			n := c + d
			if wall[n] || side[c*4+int32(k)] >= 0 {
				continue
			}
			stamp++
			mark[n], mark[c] = stamp, stamp // the box blocks c
			q = append(q[:0], n)
			for i := 0; i < len(q); i++ {
				for _, d2 := range dirs {
					if m := q[i] + d2; !wall[m] && mark[m] != stamp {
						mark[m] = stamp
						q = append(q, m)
					}
				}
			}
			for k2, d2 := range dirs {
				if n2 := c + d2; !wall[n2] && mark[n2] == stamp && n2 != c {
					side[c*4+int32(k2)] = int32(k)
				}
			}
		}
	}
	dist := make([]int32, cells*4)
	best := make([]int32, cells)
	var bq []int32
	for src := range cells {
		if wall[src] {
			continue
		}
		for i := range dist {
			dist[i] = -1
		}
		for i := range best {
			best[i] = -1
		}
		bq = bq[:0]
		for k := range 4 {
			if side[src*4+int32(k)] >= 0 {
				dist[src*4+int32(k)] = 0
				bq = append(bq, src*4+int32(k))
			}
		}
		best[src] = 0
		for i := 0; i < len(bq); i++ {
			st := bq[i]
			c, k := st/4, st%4
			r := side[st]
			for j, d := range dirs {
				// The player on side j of c pushes the box away from j.
				if side[c*4+int32(j)] != r {
					continue
				}
				nc := c - d
				if wall[nc] {
					continue
				}
				ns := nc*4 + int32(j) // the player now stands on c: side j of nc
				if dist[ns] >= 0 {
					continue
				}
				dist[ns] = dist[st] + 1
				if best[nc] < 0 {
					best[nc] = dist[ns]
				}
				bq = append(bq, ns)
			}
			_ = k
		}
		for c := range cells {
			if best[c] >= 0 {
				visit(src, c, best[c])
			}
		}
	}
}
