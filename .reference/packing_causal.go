package main

import "math/bits"

// The packing graph infers an order ideal of route-conditioned packing events.
// Dependencies are guidance inferred from one relaxed witness, not universal
// landmarks or a proof that every permitted reordering is executable.
type packingEvent struct {
	to         int32
	prev, next int // the placement this event retires / its later retirement
}

type packingDAG struct {
	events                        []packingEvent
	start, consumers              []int // CSR: prerequisite -> dependent events
	dependencyStart, dependencies []int
	readStart                     []int
	readCells                     []int32
	credit, live                  []bool
	queue                         []int
	edges                         int
}

func (s *solver) packingDAGBytes() int {
	if s.plan == nil || s.plan.dag == nil {
		return 0
	}
	d := s.plan.dag
	return cap(d.events)*24 + (cap(d.start)+cap(d.consumers)+cap(d.queue)+cap(d.dependencyStart)+cap(d.dependencies)+cap(d.readStart))*8 + cap(d.readCells)*4 + cap(d.credit) + cap(d.live)
}

// infer computes the greatest prerequisite-closed event set consistent with
// occupancy. Missing temporary placements may be justified by a credited later
// movement of that box. Failures propagate along both kinds of edge in O(V+E).
func (d *packingDAG) infer(occ []bool) int {
	for i := range d.credit {
		d.credit[i] = true
	}
	clear(d.live)
	d.queue = d.queue[:0]
	fail := func(i int) {
		if d.credit[i] {
			d.credit[i] = false
			d.queue = append(d.queue, i)
		}
	}
	for i, e := range d.events {
		if !occ[e.to] && e.next < 0 {
			fail(i)
		}
	}
	for head := 0; head < len(d.queue); head++ {
		i := d.queue[head]
		for _, j := range d.consumers[d.start[i]:d.start[i+1]] {
			fail(j)
		}
		if p := d.events[i].prev; p >= 0 && !occ[d.events[p].to] {
			fail(p)
		}
	}
	for i, e := range d.events {
		if d.credit[i] && occ[e.to] && (e.next < 0 || !d.credit[e.next]) {
			d.live[e.to] = true
		}
	}
	return len(d.events) - len(d.queue)
}

// compilePackingDAG converts the witness's box lifetimes and swept resources
// into a partial order. Atomic routes read empty squares; placing a box closes
// those resources until that box moves again. The route compiler is bounded;
// an incomplete compilation falls back to the incumbent's checkpoint scoring.
func (s *solver) compilePackingDAG(p *packingPlan) *packingDAG {
	if len(p.phases) < 2 || p.expanded == 0 {
		return nil
	}
	n := len(p.phases[0].required) + len(p.phases) - 1
	if n > 512 {
		return nil
	}
	cells := len(s.lv.Wall)
	words := (n + 63) / 64
	deps := make([]uint64, n*words)
	readers := make([]uint64, cells*words)
	owner, cleared := make([]int, cells), make([]int, cells)
	for c := range owner {
		owner[c] = -1
		cleared[c] = -1
	}
	d := &packingDAG{credit: make([]bool, n), live: make([]bool, cells), readStart: []int{0}}
	for _, c := range p.phases[0].required {
		owner[c] = len(d.events)
		d.events = append(d.events, packingEvent{to: c, prev: -1, next: -1})
		d.readCells = append(d.readCells, c)
		d.readStart = append(d.readStart, len(d.readCells))
	}
	r := newPackingRoute(s)
	for phase := 0; phase+1 < len(p.phases); phase++ {
		if !r.compile(p.phases[phase+1], p.phases[phase]) {
			p.causalVisits = r.visits
			return nil
		}
		i := len(d.events)
		d.readCells = append(d.readCells, r.used...)
		if len(d.readCells) > 1_000_000 {
			p.causalVisits = r.visits
			return nil
		}
		d.readStart = append(d.readStart, len(d.readCells))
		m := p.phases[phase].reverseMove
		src := -int32(1)
		if !m.remove {
			src = m.to
		}
		dst := m.from
		row := deps[i*words : (i+1)*words]
		add := func(j int) {
			if j >= 0 && j != i {
				row[j>>6] |= 1 << uint(j&63)
			}
		}
		prev := -1
		if src >= 0 {
			prev = owner[src]
			if prev < 0 {
				return nil
			}
			add(prev)
		}
		if owner[dst] >= 0 && dst != src {
			return nil
		}
		for _, c := range r.used {
			if c == src {
				continue
			}
			add(cleared[c])
		}
		// Every earlier empty-resource reader must precede its next writer.
		for w, v := range readers[int(dst)*words : (int(dst)+1)*words] {
			row[w] |= v
		}
		for _, c := range r.used {
			if c != src && c != dst {
				readers[int(c)*words+i/64] |= 1 << uint(i&63)
			}
		}
		if src >= 0 {
			d.events[prev].next = i
			owner[src] = -1
			cleared[src] = i
			clear(readers[int(src)*words : (int(src)+1)*words])
		}
		owner[dst] = i
		clear(readers[int(dst)*words : (int(dst)+1)*words])
		d.events = append(d.events, packingEvent{to: dst, prev: prev, next: -1})
	}
	p.causalVisits = r.visits
	d.start = make([]int, n+1)
	d.dependencyStart = make([]int, n+1)
	for i := 0; i < n; i++ {
		d.dependencyStart[i] = len(d.dependencies)
		for w, v := range deps[i*words : (i+1)*words] {
			for v != 0 {
				j := w*64 + bits.TrailingZeros64(v)
				v &= v - 1
				if j >= i {
					return nil
				}
				d.start[j+1]++
				d.dependencies = append(d.dependencies, j)
			}
		}
	}
	d.dependencyStart[n] = len(d.dependencies)
	for i := 1; i <= n; i++ {
		d.start[i] += d.start[i-1]
	}
	d.edges = d.start[n]
	d.consumers = make([]int, d.edges)
	next := append([]int(nil), d.start[:n]...)
	for i := 0; i < n; i++ {
		for w, v := range deps[i*words : (i+1)*words] {
			for v != 0 {
				j := w*64 + bits.TrailingZeros64(v)
				v &= v - 1
				d.consumers[next[j]] = i
				next[j]++
			}
		}
	}
	return d
}
