package main

import "slices"

// pullMovesRef is the original flood-per-pull implementation, kept as the test reference for pullMoves.
// pullMoves retains distinct final player regions and checks player routing
// after every pull. Other boxes stay fixed throughout each macro.
func (p *packingPlanner) pullMovesRef(boxes []int32, player int32) []packingMove {
	s := p.s
	_, parentStamp := p.flood(player)
	parentReach := make([]bool, len(s.lv.Wall))
	for _, c := range p.queue {
		parentReach[c] = p.mark[c] == parentStamp
	}
	var moves, removals []packingMove
	for _, b := range boxes {
		if p.stopped() {
			break
		}
		if p.sink[b] {
			s.occ[b] = false
			np, _ := p.flood(player)
			s.occ[b] = true
			removals = append(removals, packingMove{from: b, to: b, player: np, remove: true})
			continue
		}
		seen := make([]bool, 4*len(s.lv.Wall))
		endSeen := map[[2]int32]bool{}
		var q []int32
		for di, d := range s.dirs {
			if parentReach[b+d] {
				k := b*4 + int32(di)
				q = append(q, k)
				seen[k] = true
			}
		}
		s.occ[b] = false
		for qi := 0; qi < len(q); qi++ {
			if qi&31 == 0 && p.stopped() {
				break
			}
			k := q[qi]
			c, d := k/4, s.dirs[k%4]
			to, player := c+d, c+2*d
			if s.lv.Wall[to] || s.occ[to] || s.lv.Wall[player] || s.occ[player] {
				continue
			}
			s.occ[to] = true
			np, st := p.flood(player)
			// Save next pulling sides before removal changes the player flood.
			for di, d2 := range s.dirs {
				k2 := to*4 + int32(di)
				if p.mark[to+d2] == st && !seen[k2] {
					seen[k2] = true
					q = append(q, k2)
				}
			}
			s.occ[to] = false
			remove := p.sink[to]
			if remove {
				np, _ = p.flood(player)
			}
			key := [2]int32{to, np}
			if endSeen[key] {
				continue
			}
			endSeen[key] = true
			m := packingMove{from: b, to: to, player: np, remove: remove, weight: 1, score: p.distance[to] - p.distance[b]}
			if remove {
				m.weight = 0
				m.score = -p.distance[b]
				removals = append(removals, m)
			} else {
				moves = append(moves, m)
			}
		}
		s.occ[b] = true
	}
	// Infinite-capacity removal cannot crowd any other box, so it has
	// precedence over parking. It is not forward-search pruning.
	if len(removals) > 0 {
		slices.SortStableFunc(removals, func(a, b packingMove) int { return b.score - a.score })
		return removals
	}
	parentRegions := s.regions()
	advisor := -1
	bestScore := -int(^uint(0) >> 1)
	for i, m := range moves {
		if !s.lv.Goal[m.from] || !(p.pushable[m.to] || p.start[m.to]) {
			continue
		}
		s.occ[m.from] = false
		s.occ[m.to] = true
		regions := s.regions()
		s.occ[m.to] = false
		s.occ[m.from] = true
		if regions <= parentRegions && m.score > bestScore {
			advisor, bestScore = i, m.score
		}
	}
	if advisor >= 0 {
		moves[advisor].weight = 0
	}
	slices.SortStableFunc(moves, func(a, b packingMove) int {
		if a.weight != b.weight {
			return int(a.weight - b.weight)
		}
		return b.score - a.score
	})
	return moves
}
