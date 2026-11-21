package main

import (
	"math/rand"
	"os"
	"slices"
	"testing"
)

// exp-641: hall tables match sourcesOf; warm-started matchings match a
// plain recomputation.
func TestHallTablesAndMatching(t *testing.T) {
	data, err := os.ReadFile("levels/Open Test Suite/beear.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := rand.New(rand.NewSource(641))
	for _, idx := range []int{82, 83, 67, 152} {
		lv, err := parseLevel(levels[idx-1])
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		var goals []int32
		for c, g := range lv.Goal {
			if g {
				goals = append(goals, int32(c))
			}
		}
		rng.Shuffle(len(goals), func(i, j int) { goals[i], goals[j] = goals[j], goals[i] })
		fp := &festPlan{mode: 1}
		for _, g := range goals {
			fp.e = append(fp.e, festEntry{from: -1, to: g, until: -1})
		}
		h := newFA2Hall(s, fp)
		var floor []int32
		for c, w := range lv.Wall {
			if !w && s.dist[c] >= 0 {
				floor = append(floor, int32(c))
			}
		}
		for k := 0; k <= len(goals); k += max(1, len(goals)/5) {
			l := h.level(k)
			blocked := append([]bool(nil), lv.Wall...)
			for _, g := range goals[:k] {
				blocked[g] = true
			}
			for gi, g := range h.goalAt {
				if blocked[g] {
					continue
				}
				src := s.sourcesOf(blocked, []int32{g})
				for c := range src {
					got := l.adj[c*h.gw+gi>>6]>>(uint(gi)&63)&1 != 0
					if got != src[c] {
						t.Fatalf("level %d k %d goal %d cell %d: got %v want %v", idx, k, g, c, got, src[c])
					}
				}
			}
			// random box sets: warm (stale) vs fresh deficit
			for trial := 0; trial < 30; trial++ {
				rng.Shuffle(len(floor), func(i, j int) { floor[i], floor[j] = floor[j], floor[i] })
				cur := append([]int32(nil), floor[:s.n]...)
				fresh := hallM{mb: make([]int32, s.n), mg: make([]int32, h.ng)}
				fresh.reset()
				d1 := h.solve(k, cur, &fresh, -1)
				// brute: simple Kuhn on bool adjacency
				d2 := bruteDeficit(h, l, cur)
				if d1 != d2 {
					t.Fatalf("deficit fresh %d brute %d", d1, d2)
				}
				// warm from the previous trial's (stale) matching
				d3 := h.solve(k, cur, &h.mc, -1)
				if d3 != d1 {
					t.Fatalf("deficit warm %d fresh %d", d3, d1)
				}
				// scan down: monotone warm start
				if k > 0 {
					d4 := h.solve(k-1, cur, &fresh, -1)
					d5 := bruteDeficit(h, h.level(k-1), cur)
					if d4 != d5 {
						t.Fatalf("scan-down deficit %d brute %d", d4, d5)
					}
				}
			}
		}
	}
}

func bruteDeficit(h *fa2Hall, l *hallLevel, cur []int32) int32 {
	mg := make([]int, h.ng)
	for i := range mg {
		mg[i] = -1
	}
	var try func(i int, vis []bool) bool
	try = func(i int, vis []bool) bool {
		for g := 0; g < h.ng; g++ {
			if l.adj[int(cur[i])*h.gw+g>>6]>>(uint(g)&63)&1 == 0 || vis[g] {
				continue
			}
			vis[g] = true
			if mg[g] < 0 || try(mg[g], vis) {
				mg[g] = i
				return true
			}
		}
		return false
	}
	d := int32(0)
	for i, c := range cur {
		if l.wall[c>>6]>>(uint(c)&63)&1 != 0 {
			continue
		}
		if !try(i, make([]bool, h.ng)) {
			d++
		}
	}
	return d
}

// exp-641: the parent/child fast path (per-box structure, level skip) gives
// the same credit as solving the child from scratch.
func TestHallChildFastPath(t *testing.T) {
	data, err := os.ReadFile("levels/Open Test Suite/beear.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := rand.New(rand.NewSource(6411))
	checked, gatedSeen := 0, 0
	for _, idx := range []int{82, 83, 67, 152} {
		lv, _ := parseLevel(levels[idx-1])
		s := newSolver(lv)
		var goals []int32
		for c, g := range lv.Goal {
			if g {
				goals = append(goals, int32(c))
			}
		}
		rng.Shuffle(len(goals), func(i, j int) { goals[i], goals[j] = goals[j], goals[i] })
		fp := &festPlan{mode: 1}
		for _, g := range goals {
			fp.e = append(fp.e, festEntry{from: -1, to: g, until: -1})
		}
		h := newFA2Hall(s, fp)
		var floor []int32
		for c, w := range lv.Wall {
			if !w && s.dist[c] >= 0 {
				floor = append(floor, int32(c))
			}
		}
		for trial := 0; trial < 200; trial++ {
			// parent: the first p plan goals filled, the other boxes random
			p := rng.Intn(len(goals) + 1)
			clear(s.occ)
			cur := append([]int32(nil), goals[:p]...)
			rng.Shuffle(len(floor), func(i, j int) { floor[i], floor[j] = floor[j], floor[i] })
			for _, c := range floor {
				if len(cur) == s.n {
					break
				}
				if !slices.Contains(cur, c) {
					cur = append(cur, c)
				}
			}
			for _, c := range cur {
				s.occ[c] = true
			}
			h.parent(cur, int32(p), -1)
			for ch := 0; ch < 20; ch++ {
				i := rng.Intn(s.n)
				b := cur[i]
				tc := floor[rng.Intn(len(floor))]
				if s.occ[tc] {
					continue
				}
				s.occ[b], s.occ[tc] = false, true
				cur[i] = tc
				pc := int32(p)
				if i < p {
					pc = int32(i) // leaving a prefix target breaks the prefix there
				}
				cp1 := h.child(cur, pc, i, b, -1)
				ok := h.parentOK
				h.parentOK = false
				cp2 := h.child(cur, pc, -1, -1, -1)
				h.parentOK = ok
				if cp1 != cp2 {
					t.Fatalf("board %d p %d child %d: fast %d scratch %d", idx, p, i, cp1, cp2)
				}
				if cp1 < pc {
					gatedSeen++
				}
				checked++
				cur[i] = b
				s.occ[b], s.occ[tc] = true, false
			}
		}
	}
	t.Logf("checked %d children, %d gated", checked, gatedSeen)
}
