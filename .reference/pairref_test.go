package main

// pairDeadFromOld is the pre-exp-174 pair search (map-based), the reference
// for TestPairDeadAgree.
func (s *solver) pairDeadFromOld(a, b, from int32) bool {
	const capStates = 200
	lv := s.lv
	if s.pocc == nil {
		s.pocc = make([]bool, len(lv.Wall))
		s.pmark = make([]uint32, len(lv.Wall))
		s.pseen = make([]uint32, len(lv.Wall))
	}
	occ := s.pocc
	// reach marks the player's region from p (stamp returned) and returns its minimum cell.
	reach := func(p int32) (int32, uint32) {
		s.pst++
		st := s.pst
		q := append(s.pq[:0], p)
		s.pmark[p] = st
		m := p
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; s.pmark[y] != st && !lv.Wall[y] && !occ[y] {
					s.pmark[y] = st
					q = append(q, y)
					m = min(m, y)
				}
			}
		}
		s.pq = q
		return m, st
	}
	type st2o struct{ x, y, p int32 }
	occ[a], occ[b] = true, true
	defer func() { occ[a], occ[b] = false, false }()
	s.pst2++
	seenStamp := s.pst2
	var starts []int32
	if from >= 0 {
		m, _ := reach(from)
		starts = []int32{m}
	}
	for _, c := range s.floorCells() {
		if from >= 0 {
			break
		}
		if occ[c] || s.pseen[c] == seenStamp {
			continue
		}
		m, _ := reach(c)
		starts = append(starts, m)
		for _, x := range s.pq { // the region just flooded
			s.pseen[x] = seenStamp
		}
	}
	for _, p0 := range starts {
		seen := map[st2o]bool{{a, b, p0}: true}
		q := []st2o{{a, b, p0}}
		for i := 0; i < len(q); i++ {
			if len(seen) > capStates {
				return false // unknown: assume not dead
			}
			cur := q[i]
			occ[a], occ[b] = false, false
			occ[cur.x], occ[cur.y] = true, true
			_, st := reach(cur.p)
			type mv struct{ box, t, other int32 }
			var moves []mv
			for _, pr := range [2][2]int32{{cur.x, cur.y}, {cur.y, cur.x}} {
				bx, other := pr[0], pr[1]
				for _, d := range s.dirs {
					t := bx + d
					if s.pmark[bx-d] == st && !lv.Wall[t] && t != other && s.dist[t] >= 0 {
						moves = append(moves, mv{bx, t, other})
					}
				}
			}
			occ[cur.x], occ[cur.y] = false, false
			for _, m := range moves {
				if lv.Goal[m.t] && lv.Goal[m.other] {
					occ[a], occ[b] = true, true
					return false
				}
				occ[m.t], occ[m.other] = true, true
				np, _ := reach(m.box)
				occ[m.t], occ[m.other] = false, false
				n := st2o{min(m.t, m.other), max(m.t, m.other), np}
				if !seen[n] {
					seen[n] = true
					q = append(q, n)
				}
			}
			occ[a], occ[b] = true, true
		}
	}
	return true
}
