package main

// PackingObligations supplies advice only when the existing packing advisor
// has none. The incumbent's packed/OOP features and all legal moves stay intact.
var PackingObligations bool

func (s *solver) preparePackingObligations() {
	s.packPrepared = false
	if s.plan == nil || s.plan.dag == nil {
		return
	}
	d := s.plan.dag
	if s.packReady == nil {
		s.packReady = make([]bool, len(s.occ))
		s.packUse = make([]int, len(s.occ))
		s.packBlock = make([]int, len(s.occ))
	}
	clear(s.packReady)
	clear(s.packUse)
	clear(s.packBlock)
	d.infer(s.occ)
	for i, e := range d.events {
		if d.credit[i] {
			continue
		}
		ready := true
		for _, j := range d.dependencies[d.dependencyStart[i]:d.dependencyStart[i+1]] {
			if !d.credit[j] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		s.packReady[e.to] = true
		src := int32(-1)
		if e.prev >= 0 {
			src = d.events[e.prev].to
		}
		for _, c := range d.readCells[d.readStart[i]:d.readStart[i+1]] {
			if c != src {
				s.packUse[c]++
				if s.occ[c] {
					s.packBlock[c]++
				}
			}
		}
	}
	s.packPrepared = true
	s.stats.PackingPrepQueries++
}

// No child graph queries: root readiness is compiled into three per-cell arrays.
// First try an enabled placement, then a move that reduces blockage of the ready
// routes. Every suggestion is already a legal macro checked by the normal engine.
func (s *solver) packingObligationAdvice(moves []macroCandidate, choose func(int)) bool {
	if !s.packPrepared {
		return false
	}
	found := false
	for i, m := range moves {
		if s.packReady[m.t] {
			choose(i)
			found = true
		}
	}
	if found {
		return false
	}
	best := 0
	for _, m := range moves {
		best = max(best, s.packBlock[m.b]-s.packUse[m.t])
	}
	if best == 0 {
		return false
	}
	for i, m := range moves {
		if s.packBlock[m.b]-s.packUse[m.t] == best {
			choose(i)
		}
	}
	return true
}
