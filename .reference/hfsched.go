package main

// Festival's forward HF scheduling (exp-271). Every node keeps its own HF
// score and the best score on its path (Festival's best_past, compared in
// HF order: a matching to the target first, fewer regions, more boxes on
// target squares, fewer hotspots, smaller matching distance). The node's
// cell is the best score's (regions, boxes on target); the best-so-far node
// is the one with the best such score.

// HFFestSched enables Festival's HF cells and best-so-far in toward-hf.
var HFFestSched = false

type hfTuple struct {
	inf                bool
	reg, on, hot, dist int32
}

func hfBetter(a, b hfTuple) bool {
	if a.inf != b.inf {
		return !a.inf
	}
	if a.reg != b.reg {
		return a.reg < b.reg
	}
	if a.on != b.on {
		return a.on > b.on
	}
	if a.hot != b.hot {
		return a.hot < b.hot
	}
	return a.dist < b.dist
}

// hfSchedAdd records a new node's scores (s.occ need not hold boxes: the
// features come from cell) and returns its bucket.
func (s *solver) hfSchedAdd(boxes []int32, cell featureCell, parent int32, reg int) int {
	dist := s.addHF
	if dist < 0 {
		dist = int32(min(s.matchDist(boxes), 65535))
	}
	own := hfTuple{inf: dist >= 65535, reg: int32(reg), on: int32(cell.packed), hot: int32(cell.connectivity), dist: dist}
	best := own
	if parent >= 0 && int(parent) < len(s.hfBest) && !hfBetter(own, s.hfBest[parent]) {
		best = s.hfBest[parent]
	}
	s.hfOwn = append(s.hfOwn, own)
	s.hfBest = append(s.hfBest, best)
	return s.project(featureCell{packed: int(best.on), oop: int(best.reg)}, -1)
}

func (s *solver) hfSchedReparent(id, parent int32) int {
	best := s.hfOwn[id]
	if parent >= 0 && int(parent) < len(s.hfBest) && !hfBetter(best, s.hfBest[parent]) {
		best = s.hfBest[parent]
	}
	s.hfBest[id] = best
	return s.project(featureCell{packed: int(best.on), oop: int(best.reg)}, -1)
}
