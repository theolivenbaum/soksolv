package main

// PackedFest (exp-409): count "packed" the way Festival does over our own
// plan: the plan is a forward sequence of parking entries (initial boxes
// first, then each witnessed move: box arrives at `to`, leaving `from`),
// and packed is the length of the satisfied PREFIX, where an entry whose
// box has since moved on (until) does not break the prefix.
var PackedFest = false

// PackedFestMode: 1 Festival port (all entries), 2 goals-only prefix (transient
// staging entries ignored), 3 goals-only plain count (order-free ablation).
var PackedFestMode = 2

type festEntry struct {
	from, to int32 // from: -2 initial box, -1 arrives from a base/sink
	until    int
}

type festPlan struct {
	e       []festEntry
	initNum int
	mode    int        // PackedFestMode semantics of this plan
	festTo  []bool     // exp-472: plan parking cells (oracle plan file)
	oopPark [][2]int32 // exp-740: plan-file park cells [cell, step], OOP targets only
}

func buildFestPlan(p *packingPlan, goal []bool) *festPlan {
	if len(p.phases) == 0 {
		return &festPlan{}
	}
	if PackedFestMode >= 2 {
		fp := buildGoalsPlan(p, goal)
		fp.mode = PackedFestMode
		return fp
	}
	return buildFestEvents(p, -1)
}

// buildFestEvents turns the plan's witnessed backward moves into Festival's
// forward event list (mode 1, exp-409): INIT entries for the residual boxes of
// the endpoint, then each move (a removal is "ANY -> cell"), consecutive moves
// of one box collapsed, lifetimes recomputed.
func buildFestEvents(p *packingPlan, nBoxes int) *festPlan {
	if len(p.phases) == 0 {
		return &festPlan{mode: 1}
	}
	var e []festEntry
	for _, c := range p.phases[0].required {
		e = append(e, festEntry{from: -2, to: c})
	}
	initNum := len(e)
	for k := 0; k+1 < len(p.phases); k++ {
		m := p.phases[k].reverseMove
		from := m.to
		if m.remove {
			from = -1
		}
		if from == m.from {
			continue
		}
		e = append(e, festEntry{from: from, to: m.from})
	}
	// merge consecutive moves of one box (a->b, b->c becomes a->c)
	for i := initNum; i < len(e); {
		if e[i].to == e[i].from { // a->b, b->a: no move at all
			e = append(e[:i], e[i+1:]...)
			i = initNum
			continue
		}
		if i+1 < len(e) && e[i].to == e[i+1].from {
			e[i].to = e[i+1].to
			e = append(e[:i+1], e[i+2:]...)
			i = initNum
			continue
		}
		i++
	}
	fp := &festPlan{initNum: initNum, mode: 1}
	fp.e = e
	fp.recomputeUntil()
	if initNum == nBoxes && nBoxes > 0 {
		// no box was removed: forget the moves, keep the permanent targets
		// (Festival keep_only_perm)
		fp.keepOnlyPerm()
	}
	return fp
}

func (f *festPlan) keepOnlyPerm() {
	var e []festEntry
	for i, x := range f.e {
		if x.until == -1 {
			if i >= f.initNum {
				x.from = -1
			}
			e = append(e, x)
		}
	}
	n := 0
	for i := 0; i < f.initNum; i++ {
		if f.e[i].until == -1 {
			n++
		}
	}
	f.e, f.initNum = e, n
	f.recomputeUntil()
}

func (f *festPlan) recomputeUntil() {
	for i := range f.e {
		f.e[i].until = -1
	}
	for i := range f.e {
		for j := i + 1; j < len(f.e); j++ {
			if f.e[j].from == f.e[i].to {
				f.e[i].until = j
				break
			}
		}
	}
}

func (f *festPlan) packed(occ []bool) int {
	n := len(f.e)
	if f.mode == 3 {
		c := 0
		for _, x := range f.e {
			if occ[x.to] {
				c++
			}
		}
		return c
	}
	if f.mode == 2 {
		for i, x := range f.e {
			if !occ[x.to] {
				return i
			}
		}
		return n
	}
	for i := 0; i < f.initNum; i++ {
		if f.e[i].until == -1 && !occ[f.e[i].to] {
			// mandatory initial box missing: only count mandatory present
			c := 0
			for j := 0; j < f.initNum; j++ {
				if f.e[j].until == -1 && occ[f.e[j].to] {
					c++
				}
			}
			return c
		}
	}
	packed := n
	for i := n - 1; i >= f.initNum; i-- {
		if occ[f.e[i].to] {
			continue
		}
		if f.e[i].until == -1 || f.e[i].until >= packed {
			packed = i
		}
	}
	good := 0
	for i := 0; i < f.initNum; i++ {
		if occ[f.e[i].to] || f.e[i].until < packed {
			good++
		}
	}
	return packed - (f.initNum - good)
}

// buildGoalsPlan keeps only the final arrival of each goal, in plan order
// (goals already standing in the plan's first checkpoint come first). Staging
// parks and boxes that merely start where the plan puts them are ignored.
func buildGoalsPlan(p *packingPlan, goal []bool) *festPlan {
	type mv struct{ from, to int32 }
	var moves []mv
	for k := 0; k+1 < len(p.phases); k++ {
		m := p.phases[k].reverseMove
		from := m.to
		if m.remove {
			from = -1
		}
		if from != m.from {
			moves = append(moves, mv{from, m.from})
		}
	}
	leaves := func(c int32, after int) bool {
		for j := after + 1; j < len(moves); j++ {
			if moves[j].from == c {
				return true
			}
		}
		return false
	}
	var e []festEntry
	for _, c := range p.phases[0].required {
		if goal[c] {
			e = append(e, festEntry{from: -2, to: c, until: -1})
		}
	}
	for i, m := range moves {
		if goal[m.to] && !leaves(m.to, i) {
			e = append(e, festEntry{from: -1, to: m.to, until: -1})
		}
	}
	return &festPlan{e: e}
}
