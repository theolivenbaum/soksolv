package main

type fessEdge struct{ parent, next int32 }

// Retain incoming graph edges, including transpositions. A state is proved
// dead only after every macro is tried and every resulting state is proved
// dead. Cycles without an exit may stay unmarked; this conservative choice
// never turns a transposition or an unresolved cycle into a false deadlock.
func (s *solver) recordEdge(parent, child int32) {
	if child < 0 || s.dead[child] {
		return
	}
	s.edges = append(s.edges, fessEdge{parent, s.edgeHead[child]})
	s.edgeHead[child] = int32(len(s.edges) - 1)
	s.liveChildren[parent]++
}

func (s *solver) propagateDead(id int32) {
	if s.cancelled || s.dead[id] || s.mvStart[id] < 0 || s.mvLeft[id] > 0 || s.liveChildren[id] > 0 {
		return
	}
	s.deadFrom(id)
}

func (s *solver) deadFrom(id int32) {
	q := []int32{id}
	for len(q) > 0 {
		id = q[len(q)-1]
		q = q[:len(q)-1]
		if s.dead[id] {
			continue
		}
		s.dead[id] = true
		s.version[id]++
		s.stats.DeadEnds++
		for edge := s.edgeHead[id]; edge >= 0; edge = s.edges[edge].next {
			parent := s.edges[edge].parent
			s.liveChildren[parent]--
			if !s.dead[parent] && s.mvStart[parent] >= 0 && s.mvLeft[parent] == 0 && s.liveChildren[parent] == 0 {
				q = append(q, parent)
			}
		}
	}
}
