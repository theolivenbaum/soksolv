package main

import (
	"math/rand"
	"slices"
	"testing"
)

// exp-583: matchSkip answers exactly what a from-scratch matchOK does, for
// every (box, base) pair, on random bipartite reach graphs of every density.
func TestMatchSkipEqualsMatchOK(t *testing.T) {
	rng := rand.New(rand.NewSource(583))
	for iter := 0; iter < 4000; iter++ {
		n := 1 + rng.Intn(12)
		cells := 64
		p := &planner2{n: n, mBox: make([]int32, n), mSeen: make([]uint32, n), fmBy: make([]int32, n)}
		p.reach = make([][]uint64, n)
		dens := rng.Float64()
		for b := range p.reach {
			p.reach[b] = make([]uint64, 1)
			for c := 0; c < cells; c++ {
				if rng.Float64() < dens*0.5 {
					p.reach[b][0] |= 1 << uint(c)
				}
			}
		}
		p.free = p.free[:0]
		for b := 0; b < n; b++ {
			if rng.Intn(4) != 0 {
				p.free = append(p.free, int32(b))
			}
		}
		nb := len(p.free)
		if rng.Intn(3) == 0 && nb > 0 {
			nb = rng.Intn(nb) + 1
		}
		boxes := make([]int32, nb)
		for i := range boxes {
			boxes[i] = int32(rng.Intn(cells))
		}
		p.expGen++
		for bi := range boxes {
			for _, base := range p.free {
				got := p.matchSkip(boxes, bi, base)
				want := p.matchOK(boxes, bi, base)
				if got != want {
					t.Fatalf("iter %d n %d free %v boxes %v skip box %d base %d: matchSkip %v matchOK %v", iter, n, p.free, boxes, bi, base, got, want)
				}
			}
		}
		if p.stats.matchCalls%2 != 0 || p.stats.matchFails%2 != 0 {
			t.Fatalf("stats not doubled: %+v", p.stats)
		}
	}
}

// exp-583: sortKids is SortStableFunc by key (ties keep enumeration order).
func TestSortKidsStable(t *testing.T) {
	rng := rand.New(rand.NewSource(5831))
	var tmp []p2Child
	for iter := 0; iter < 3000; iter++ {
		n := rng.Intn(1500)
		a := make([]p2Child, n)
		spread := uint64(1) << uint(rng.Intn(64))
		for i := range a {
			a[i] = p2Child{key: (rng.Uint64() % (spread | 1)) << uint(rng.Intn(3)*20), from: int32(i)}
			if rng.Intn(2) == 0 {
				a[i].key |= 0xAB << 56
			}
		}
		want := append([]p2Child(nil), a...)
		slices.SortStableFunc(want, func(x, y p2Child) int { return cmpU64(x.key, y.key) })
		sortKids(a, &tmp)
		if !slices.Equal(a, want) {
			t.Fatalf("iter %d n %d: sortKids differs", iter, n)
		}
	}
}
