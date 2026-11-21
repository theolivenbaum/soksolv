package main

// exp-479: Festival's out-of-plan semantics (oop.cpp), used by the fa2
// engine (the macro lane's -oop-fest switch was removed in exp-639 after the
// exp-486 STOP). A box is out of plan at step k only if it stands where no
// still-unplaced plan target can be reached once the permanently parked boxes
// are walls (and no other box stands in the way) and the cell is not dead
// anyway. One zone bitset per plan step is precomputed and smoothed to the
// next step where the zone changes ("clear boxes as soon as possible"), so
// counting is one bit test per box.

type oopZones struct {
	plan   *packingPlan
	byPack bool // zones indexed by Festival packed count (plan file), else by plan phase
	w      int
	z      []uint64 // step*w words
	n      int
}

// sourcesOf marks every cell from which a box can be pushed onto one of the
// targets on a board with only blocked cells as obstacles (pull search from
// the targets; the player is free to walk around the box).
func (s *solver) sourcesOf(blocked []bool, targets []int32) []bool {
	lv := s.lv
	inside := make([]bool, len(lv.Wall))
	seen := make([]bool, 4*len(lv.Wall))
	mark := make([]uint32, len(lv.Wall))
	stamp := uint32(0)
	var q, pq []int32
	free := func(c, box int32) bool { return !blocked[c] && c != box }
	for _, g := range targets {
		if blocked[g] {
			continue
		}
		inside[g] = true
		for di, d := range s.dirs {
			if free(g+d, g) {
				k := g*4 + int32(di)
				if !seen[k] {
					seen[k] = true
					q = append(q, k)
				}
			}
		}
	}
	for i := 0; i < len(q); i++ {
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

// buildOOPZones precomputes the per-step zones. Raw zone i (Festival
// get_oop_zone_for_step): parked = permanent targets of steps <= i, targets =
// all steps > i. Steps are the Festival plan entries (plan file) or the
// packing plan's phases (own plan, parked = cells of phase i that stay
// required in every later phase).
func (s *solver) buildOOPZones() *oopZones {
	lv := s.lv
	n := len(lv.Wall)
	z := &oopZones{plan: s.plan, w: (n + 63) >> 6}
	var goals []int32
	for c, g := range lv.Goal {
		if g {
			goals = append(goals, int32(c))
		}
	}
	possible := s.sourcesOf(lv.Wall, goals) // not a dead square
	var parked, targets [][]int32
	if fp := s.plan.fest; fp != nil && fp.festTo != nil {
		z.byPack = true
		z.n = len(fp.e)
		for i := 0; i < z.n; i++ {
			var pk, tg []int32
			for j := 0; j <= i; j++ {
				if fp.e[j].until == -1 {
					pk = append(pk, fp.e[j].to)
				}
			}
			for j := i + 1; j < z.n; j++ {
				tg = append(tg, fp.e[j].to)
			}
			for _, p := range fp.oopPark {
				if p[1] < 0 || int32(i) < p[1] {
					tg = append(tg, p[0])
				}
			}
			parked, targets = append(parked, pk), append(targets, tg)
		}
	} else {
		ph := s.plan.phases
		z.n = len(ph)
		parked = make([][]int32, len(ph))
		targets = make([][]int32, len(ph))
		cnt := make(map[int32]int) // cells required in every phase >= k
		var acc []int32
		for k := len(ph) - 1; k >= 0; k-- {
			targets[k] = append([]int32(nil), acc...) // targets of steps > k
			for _, c := range ph[k].required {
				cnt[c]++
			}
			for _, c := range ph[k].required {
				if cnt[c] == len(ph)-k {
					parked[k] = append(parked[k], c)
				}
			}
			acc = append(acc, ph[k].required...)
		}
	}
	if z.n < 2 {
		z.n = 0
		return z
	}
	raw := make([][]uint64, z.n)
	for i := 0; i < z.n-1; i++ {
		blocked := make([]bool, n)
		copy(blocked, lv.Wall)
		for _, c := range parked[i] {
			blocked[c] = true
		}
		var tg []int32
		for _, c := range targets[i] {
			if !blocked[c] {
				tg = append(tg, c)
			}
		}
		src := s.sourcesOf(blocked, tg)
		row := make([]uint64, z.w)
		for c := 0; c < n; c++ {
			if !blocked[c] && possible[c] && !src[c] {
				row[c>>6] |= 1 << (uint(c) & 63)
			}
		}
		raw[i] = row
	}
	// smoothing (prepare_oop_zones): step j in [lastModified, i] gets raw[i],
	// i the next step whose raw zone differs from the current one.
	z.z = make([]uint64, z.n*z.w)
	cur := make([]uint64, z.w)
	last := 0
	for i := 0; i < z.n-1; i++ {
		same := true
		for k := range cur {
			if raw[i][k] != cur[k] {
				same = false
				break
			}
		}
		if same {
			continue
		}
		copy(cur, raw[i])
		for j := last; j <= i; j++ {
			copy(z.z[j*z.w:(j+1)*z.w], cur)
		}
		last = i
	}
	if OOPTailFix || s.oopTail {
		// exp-491: steps after the last change keep the raw zone (Festival
		// smooths only ranges ending at a change; the tail stays raw).
		for j := last + 1; j < z.n-1; j++ {
			copy(z.z[j*z.w:(j+1)*z.w], raw[j])
		}
	}
	return z
}

// OOPTailFix (exp-491): see buildOOPZones. Set by the fa2 lane only.
var OOPTailFix = false

// oopFestCount counts boxes in the zone of the plan's current step. phase is
// the plan phase of s.occ (-2: look up); packed the Festival packed count
// (-1: compute; only used with a plan file).
func (s *solver) oopFestCount(boxes []int32, phase, packed int) int {
	if s.oopz == nil || s.oopz.plan != s.plan {
		s.oopz = s.buildOOPZones()
	}
	z := s.oopz
	if z.n == 0 {
		return 0
	}
	var step int
	if z.byPack {
		if packed < 0 {
			packed = s.plan.fest.packed(s.occ)
		}
		step = packed
	} else {
		if phase == -2 {
			phase, _ = s.plan.status(s.occ)
		}
		step = max(phase, 0) + 1 // the next step to be placed
	}
	if step >= z.n-1 {
		return 0
	}
	row := z.z[step*z.w : (step+1)*z.w]
	cnt := 0
	for _, b := range boxes {
		cnt += int(row[b>>6] >> (uint(b) & 63) & 1)
	}
	return cnt
}
