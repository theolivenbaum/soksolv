package main

import "time"

// packingBasin is computed in the final board, with all the other goals
// occupied. A box can be pulled from target to a basin square with valid
// player routing. Thus reversing that route can supply the target even after
// the other final targets have become permanent obstacles.
type packingBasin struct {
	inside   []bool
	distance []int32 // walking distance to the basin, for the OOP advisor
	sink     int     // room intersecting the selected basin, or -1
	goal     int32
}

func (s *solver) targetBasin(goal int32) []bool {
	lv := s.lv
	inside := make([]bool, len(lv.Wall))
	inside[goal] = true
	seen := make([]bool, 4*len(lv.Wall))
	mark := make([]uint32, len(lv.Wall))
	stamp := uint32(0)
	// Each item is a box square and an available pulling direction. The
	// player is on box+direction, ready to step back one square and pull.
	var q, pq []int32
	free := func(c, box int32) bool { return !lv.Wall[c] && c != box && (!lv.Goal[c] || c == goal) }
	for di, d := range s.dirs {
		if free(goal+d, goal) {
			k := goal*4 + int32(di)
			seen[k] = true
			q = append(q, k)
		}
	}
	for i := 0; i < len(q); i++ {
		if i&63 == 0 && s.setupExpired() {
			return nil
		}
		k := q[i]
		b, d := k/4, s.dirs[k%4]
		to, p := b+d, b+2*d
		if !free(to, b) || !free(p, b) {
			continue
		}
		inside[to] = true
		stamp++
		pq = append(pq[:0], p)
		mark[p] = stamp
		for j := 0; j < len(pq); j++ {
			for _, off := range s.dirs {
				x := pq[j] + off
				if mark[x] != stamp && free(x, to) {
					mark[x] = stamp
					pq = append(pq, x)
				}
			}
		}
		for di, off := range s.dirs {
			k2 := to*4 + int32(di)
			if mark[to+off] == stamp && !seen[k2] {
				seen[k2] = true
				q = append(q, k2)
			}
		}
	}
	return inside
}

func (s *solver) computeBasin() *packingBasin {
	if len(s.rooms.rooms) == 0 {
		return nil
	}
	var best *packingBasin
	bestBoxes, bestRoom := -1, -1
	for _, g := range s.goals {
		inside := s.targetBasin(g)
		if inside == nil {
			break
		}
		boxes := 0
		for _, b := range s.lv.Boxes {
			if inside[b] {
				boxes++
			}
		}
		rcounts := make([]int, len(s.rooms.rooms))
		for c, in := range inside {
			if in && s.rooms.cell[c] >= 0 {
				rcounts[s.rooms.cell[c]]++
			}
		}
		sink, roomSize := -1, 0
		for r, n := range rcounts {
			if n > roomSize {
				sink, roomSize = r, n
			}
		}
		if sink < 0 {
			continue
		}
		if boxes > bestBoxes || boxes == bestBoxes && roomSize > bestRoom {
			bestBoxes, bestRoom = boxes, roomSize
			best = &packingBasin{inside: inside, sink: sink, goal: g}
		}
	}
	if best == nil {
		return nil
	}
	best.distance = make([]int32, len(s.lv.Wall))
	for i := range best.distance {
		best.distance[i] = -1
	}
	var q []int32
	for c, in := range best.inside {
		if in {
			best.distance[c] = 0
			q = append(q, int32(c))
		}
	}
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			x := q[i] + d
			if !s.lv.Wall[x] && best.distance[x] < 0 {
				best.distance[x] = best.distance[q[i]] + 1
				q = append(q, x)
			}
		}
	}
	return best
}

func (s *solver) setupExpired() bool {
	return s.expired() || NodeBudget == 0 && !s.setupDeadline.IsZero() && time.Now().After(s.setupDeadline)
}

// outOfPlan uses the selected final-board basin, rather than a distance or
// goal-round penalty. Already packed goals are exempt. Parking checkpoints
// will also be exempt when the backward packing planner supplies them.
// OOPGoalFree: boxes on goals never count as out of plan (exp-187).
var OOPGoalFree = true

func (s *solver) outOfPlan(boxes []int32) int { return s.outOfPlanPhase(boxes, -2) }

func (s *solver) outOfPlanPhase(boxes []int32, phase int) int {
	return s.outOfPlanStep(boxes, phase, -1)
}

// outOfPlanPhase is outOfPlan given the plan phase of s.occ (-2: not known
// yet, looked up when a box needs it).
func (s *solver) outOfPlanStep(boxes []int32, phase, packed int) int {
	if s.basin == nil || FXNoOOP {
		return 0
	}
	// The plan's phase depends only on s.occ: find it once, not per box
	// (per box it was a quarter of FESS's time on 60-box levels).
	n := 0
	var row []uint64 // the plan's placed bits of the phase, once known
	for _, b := range boxes {
		if s.basin.inside[b] {
			continue
		}
		var ok bool
		if s.plan != nil && s.plan.fest != nil && s.plan.fest.festTo != nil && PlanFileOOP {
			ok = s.plan.fest.festTo[b]
		} else if s.plan != nil {
			if row == nil {
				if phase == -2 {
					phase, _ = s.plan.status(s.occ)
				}
				row = s.plan.placedRow(phase)
			}
			w := int(b >> 6)
			ok = w < len(row) && row[w]>>(uint(b)&63)&1 != 0
		} else {
			ok = s.lv.Goal[b] && s.occ[b]
		}
		// A box on a goal is never out of plan: the plan fills that goal
		// later, and the box already supplies it (Festival: a box that can
		// still reach a remaining target is not OOP). Counting it made FESS
		// pull correct boxes off their goals (the "never" group of the
		// failure taxonomy: levels that start mostly packed).
		if !ok && !(OOPGoalFree && s.lv.Goal[b]) {
			n++
		}
	}
	return n
}

// oopOne is box c's term of outOfPlanStep at a known phase (>= -1, or any
// value without a plan).
func (s *solver) oopOne(c int32, phase int) int {
	if s.basin.inside[c] {
		return 0
	}
	var ok bool
	if s.plan != nil && s.plan.fest != nil && s.plan.fest.festTo != nil && PlanFileOOP {
		ok = s.plan.fest.festTo[c]
	} else if s.plan != nil {
		row := s.plan.placedRow(phase)
		w := int(c >> 6)
		ok = w < len(row) && row[w]>>(uint(c)&63)&1 != 0
	} else {
		ok = s.lv.Goal[c] && s.occ[c]
	}
	if !ok && !(OOPGoalFree && s.lv.Goal[c]) {
		return 1
	}
	return 0
}

// oopChild is outOfPlanStep for the child of the opened node in which box i
// moved from b to boxes[i] (exp-659 -lazy-child): per phase, the parent's
// count is found once per opened node (s.oopSt marks it) and each child
// adjusts it by the moved box's two terms.
func (s *solver) oopChild(boxes []int32, i int, b int32, phase, packed int) int {
	if s.basin == nil || FXNoOOP {
		return 0
	}
	if s.plan == nil {
		if !OOPGoalFree {
			return s.outOfPlanStep(boxes, phase, packed) // the term reads s.occ
		}
		phase = -1
	}
	k := phase + 1
	if k < 0 {
		return s.outOfPlanStep(boxes, phase, packed)
	}
	for len(s.oopBase) <= k {
		s.oopBase = append(s.oopBase, 0)
		s.oopBaseSt = append(s.oopBaseSt, 0)
	}
	t := boxes[i]
	if s.oopBaseSt[k] != s.oopSt {
		boxes[i] = b
		s.oopBase[k] = int32(s.outOfPlanStep(boxes, phase, packed))
		boxes[i] = t
		s.oopBaseSt[k] = s.oopSt
	}
	return int(s.oopBase[k]) - s.oopOne(b, phase) + s.oopOne(t, phase)
}

func (s *solver) placed(b int32) bool {
	if s.plan != nil {
		phase, _ := s.plan.status(s.occ)
		return s.plan.placed(b, phase)
	}
	return s.lv.Goal[b] && s.occ[b]
}
