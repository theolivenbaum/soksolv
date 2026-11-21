package main

import "math"

// HFIncrementalMatch reuses the exact Hungarian solution of the open node.
var HFIncrementalMatch = true

func (s *solver) hfMatchParent(boxes []int32) int {
	score := s.matchDist(boxes)
	if !HFIncrementalMatch {
		return score
	}
	m := s.n + 1
	if s.hfDual == nil {
		s.hfDual = make([]int32, 4*m)
	}
	copy(s.hfDual[:m], s.hu)
	copy(s.hfDual[m:2*m], s.hv)
	copy(s.hfDual[2*m:3*m], s.hp)
	for g := 1; g < m; g++ {
		s.hfDual[3*m+int(s.hp[g])] = int32(g)
	}
	s.hfCost = int64(score)
	if score == assignmentInf {
		s.hfCost = s.assignmentCost(boxes, s.matchingDistances())
	}
	return score
}

func (s *solver) hfMatchChild(boxes []int32, moved int, old int32) int {
	if !HFIncrementalMatch {
		return s.matchDist(boxes)
	}
	m, cells := s.n+1, len(s.lv.Wall)
	row := int32(moved + 1)
	goal := s.hfDual[3*m+int(row)]
	dist := s.matchingDistances()
	v := s.hfDual[m : 2*m]
	box := boxes[moved]

	// Replacing one row leaves the rest of the dual feasible. Its new
	// potential is the least reduced cost among its outgoing edges.
	u := int32(math.MaxInt32)
	if len(dist) == s.n*cells {
		// Box-major row (one box's distances to every goal, contiguous).
		er := s.distT(dist)[int(box)*s.n : int(box)*s.n+s.n]
		for j := 1; j < m; j++ {
			e := er[j-1]
			if e < 0 {
				e = assignmentInf
			}
			u = min(u, e-v[j])
		}
	} else {
		for j := int32(1); j < int32(m); j++ {
			u = min(u, assignmentEdge(dist, cells, box, j)-v[j])
		}
	}
	newCost := assignmentEdge(dist, cells, box, goal)
	if u == newCost-v[goal] {
		// The old assignment is still tight, so it is still optimal.
		oldCost := assignmentEdge(dist, cells, old, goal)
		return int(min(assignmentInf, s.hfCost+int64(newCost)-int64(oldCost)))
	}

	copy(s.hu, s.hfDual[:m])
	copy(s.hv, v)
	copy(s.hp, s.hfDual[2*m:3*m])
	s.hu[row], s.hp[goal], s.hv[0] = u, 0, 0
	s.augmentAssignment(boxes, dist, row)
	return int(min(assignmentInf, s.assignmentCost(boxes, dist)))
}
