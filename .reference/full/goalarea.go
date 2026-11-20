package full

// Goal-area deadlocks (Festival's try_pulling_all_zones; see
// research/WEB-RESEARCH-2026-10-31.md #3). Boxes frozen on goals never move
// again in any solution, so they are walls. From the solved board (boxes on
// every other goal), greedily pull each box one step and delete it, for each
// possible final player zone. Reverse any real solution from the current
// position: every box it moves is deleted by this saturation (by induction,
// the greedy board is always a subset of the real one), and every box it
// never moves sits on its goal in the current position. So if, in every zone,
// some surviving goal holds no box now, the position is dead. Sound whenever
// the freeze test is. Survivors are computed per call (no cache: a 64-bit hash
// of the frozen set keyed the old cache with no collision check).

type goalArea struct {
	wall     []bool // walls plus frozen goal boxes
	box      []bool // solved-board boxes not yet removed
	mark     []uint32
	stamp    uint32
	q        []int32
	fset     []int32
	wl       []int32  // survivors: boxes to re-check
	fz, gone []uint32 // survivors: frozen-this-call and removed-this-zone stamps
	fst, zst uint32
	isGoal   []bool
}

// goalAreaDeadlock is called with s.occ holding the child and t the square a
// box was just pushed to, after the freeze test passed. Only a push that
// leaves its box frozen on a goal can change the frozen goal set.
func (s *solver) goalAreaDeadlock(t int32) bool {
	lv := s.lv
	if !lv.Goal[t] {
		return false
	}
	off := false
	if !s.frozen(t, &off) {
		return false
	}
	ga := &s.ga
	if ga.wall == nil {
		n := len(lv.Wall)
		*ga = goalArea{wall: make([]bool, n), box: make([]bool, n), mark: make([]uint32, n)}
	}
	ga.fset = ga.fset[:0]
	for _, g := range s.goals {
		if s.occ[g] {
			off := false
			if s.frozen(g, &off) {
				ga.fset = append(ga.fset, g)
			}
		}
	}
	zones := ga.survivors(lv.Wall, s.goals, s.dirs)
	if checkSurvivors != nil {
		checkSurvivors(ga, zones, lv.Wall, s.goals, s.dirs)
	}
	for _, surv := range zones {
		covered := true
		for _, g := range surv {
			if !s.occ[g] {
				covered = false
				break
			}
		}
		if covered {
			return false
		}
	}
	return len(zones) > 0
}

// survivors returns, per zone of the solved board, the goal boxes that
// greedy pull-and-delete cannot remove. No zones (no free floor) means no
// claim.
func (ga *goalArea) survivors(wall []bool, goals []int32, dirs [4]int32) [][]int32 {
	// Generation stamps instead of per-call O(board) copies and clears:
	// frozen goal boxes are walls for this call (fz == fst), and a goal box
	// is present on the solved board unless pulled away in this zone
	// (gone == zst).
	if len(ga.fz) != len(wall) {
		ga.fz, ga.gone, ga.isGoal = make([]uint32, len(wall)), make([]uint32, len(wall)), make([]bool, len(wall))
		for _, g := range goals {
			ga.isGoal[g] = true
		}
	}
	ga.fst++
	fst := ga.fst
	for _, f := range ga.fset {
		ga.fz[f] = fst
	}
	isWall := func(c int32) bool { return wall[c] || ga.fz[c] == fst }
	var zst uint32
	hasBox := func(c int32) bool { return ga.isGoal[c] && ga.fz[c] != fst && ga.gone[c] != zst }
	free := func(c int32) bool { return !isWall(c) && !hasBox(c) }
	reset := func() int {
		ga.zst++
		zst = ga.zst
		return len(goals) - len(ga.fset)
	}
	if reset() == 0 {
		return nil
	}
	// Zones of the solved board: try each as the player's final region.
	ga.stamp++
	zoneStamp := ga.stamp
	var seeds []int32
	var zones [][]int32
	for c := range int32(len(wall)) {
		if free(c) && ga.mark[c] != zoneStamp {
			seeds = append(seeds, c)
			ga.flood(c, zoneStamp, free, dirs)
		}
	}
	for _, seed := range seeds {
		left := reset()
		// Worklist fixpoint (same result as repeated full passes): a goal
		// box b can be pulled away when, for some d, b+d is in the player's
		// region and b+2d is free. Removing b frees b and can grow the
		// region; only boxes beside newly reached cells, or two steps behind
		// the freed cell, can become removable.
		ga.stamp++
		st := ga.stamp
		ga.wl = ga.wl[:0]
		reach := func(c int32) { // incremental flood from a newly free cell c adjacent to the region
			q := append(ga.q[:0], c)
			ga.mark[c] = st
			for i := 0; i < len(q); i++ {
				x := q[i]
				for _, d := range dirs {
					if b := x - d; hasBox(b) {
						ga.wl = append(ga.wl, b) // x = b+d is reachable now
					}
					if y := x + d; free(y) && ga.mark[y] != st {
						ga.mark[y] = st
						q = append(q, y)
					}
				}
			}
			ga.q = q
		}
		reach(seed)
		for len(ga.wl) > 0 && left > 0 {
			b := ga.wl[len(ga.wl)-1]
			ga.wl = ga.wl[:len(ga.wl)-1]
			if !hasBox(b) {
				continue
			}
			removable := false
			for _, d := range dirs {
				if p := b + d; ga.mark[p] == st && free(p+d) {
					removable = true
					break
				}
			}
			if !removable {
				continue
			}
			ga.gone[b] = zst
			left--
			for _, d := range dirs {
				if c := b - 2*d; c >= 0 && int(c) < len(wall) && hasBox(c) {
					ga.wl = append(ga.wl, c) // c+2d = b is free now
				}
			}
			adjacent := false
			for _, d := range dirs {
				if ga.mark[b+d] == st {
					adjacent = true
					break
				}
			}
			if adjacent && ga.mark[b] != st {
				reach(b)
			}
		}
		surv := []int32{}
		for _, g := range goals {
			if hasBox(g) {
				surv = append(surv, g)
			}
		}
		if len(surv) == 0 {
			return [][]int32{surv} // always covered: never dead
		}
		zones = append(zones, surv)
	}
	return zones
}

// flood marks the free cells reachable from c with stamp st. Removing a box
// only enlarges the region, so the seed stays inside it.
func (ga *goalArea) flood(c int32, st uint32, free func(int32) bool, dirs [4]int32) {
	ga.q = append(ga.q[:0], c)
	ga.mark[c] = st
	for i := 0; i < len(ga.q); i++ {
		x := ga.q[i]
		for _, d := range dirs {
			if y := x + d; free(y) && ga.mark[y] != st {
				ga.mark[y] = st
				ga.q = append(ga.q, y)
			}
		}
	}
}

// checkSurvivors (tests) compares each result with the reference fixpoint.
var checkSurvivors func(ga *goalArea, zones [][]int32, wall []bool, goals []int32, dirs [4]int32)
