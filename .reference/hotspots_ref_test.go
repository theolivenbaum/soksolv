package main

// Original hotspot implementations (before exp-399), kept as the test
// reference: one BFS per goal per blocker, and an O(boxes) scan per candidate.

type hotspotRefState struct {
	seen  []uint32
	stamp uint32
	queue []int32
	rows  [][]uint64
}

func (h *hotspotTable) rowRef(ref *hotspotRefState, s *solver, blocker int32) []uint64 {
	if ref.seen == nil {
		ref.seen = make([]uint32, len(s.lv.Wall))
		ref.rows = make([][]uint64, len(s.fcell))
	}
	if r := ref.rows[s.fidx[blocker]]; r != nil {
		return r
	}
	row := make([]uint64, s.nw)
	lv := s.lv
	for g, goal := range s.goals {
		if s.expired() {
			return row
		}
		ref.stamp++
		st := ref.stamp
		q := ref.queue[:0]
		if goal != blocker {
			q = append(q, goal)
			ref.seen[goal] = st
		}
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				b := q[i] - d
				support := s.supportCell(b, q[i], d)
				if b == blocker || lv.Wall[b] || ref.seen[b] == st || support == blocker || lv.Wall[support] || s.goalDist[g*len(lv.Wall)+int(b)] < 0 {
					continue
				}
				ref.seen[b] = st
				q = append(q, b)
			}
		}
		ref.queue = q
		base := s.goalDist[g*len(lv.Wall) : (g+1)*len(lv.Wall)]
		for fi, c := range s.fcell {
			if c != blocker && base[c] >= 0 && ref.seen[c] != st {
				row[fi/64] |= 1 << uint(fi%64)
			}
		}
	}
	ref.rows[s.fidx[blocker]] = row
	return row
}

// countsRef returns, per occupied blocker, the number of other boxes it affects.
func (h *hotspotTable) countsRef(ref *hotspotRefState, s *solver, boxes []int32) []int {
	counts := make([]int, len(boxes))
	for i, b := range boxes {
		row := h.rowRef(ref, s, b)
		for j, x := range boxes {
			if i != j && hotspotHits(row, s.fidx[x]) {
				counts[i]++
			}
		}
	}
	return counts
}

// afterRef returns the number of hotspot boxes after replacing boxes[moved]
// with target. Updating the fixed blockers takes O(boxes), not O(boxes²).
func (h *hotspotTable) afterRef(ref *hotspotRefState, s *solver, boxes []int32, counts []int, moved int, target int32) int {
	old, now := s.fidx[boxes[moved]], s.fidx[target]
	n := 0
	for i, b := range boxes {
		if i != moved {
			row := h.rowRef(ref, s, b)
			c := counts[i]
			if hotspotHits(row, old) {
				c--
			}
			if hotspotHits(row, now) {
				c++
			}
			if c > 0 {
				n++
			}
		}
	}
	row := h.rowRef(ref, s, target)
	for i, b := range boxes {
		if i != moved && hotspotHits(row, s.fidx[b]) {
			n++
			break
		}
	}
	return n
}
