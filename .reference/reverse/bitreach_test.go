package reverse

import (
	"os"
	"testing"
)

// TestRegionMinMatchesReach (exp-415): the bitset region minimum equals the
// BFS reach minimum for random pulls on random box placements.
func TestRegionMinMatchesReach(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/XSokoban_90.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := uint64(5)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	checked := 0
	for _, idx := range []int{1, 6, 13, 29, 54, 89} {
		lv, err := parseLevel(levels[idx-1])
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		if s.br == nil {
			continue
		}
		var floor []int32
		for c := range lv.Wall {
			if !lv.Wall[c] {
				floor = append(floor, int32(c))
			}
		}
		for trial := 0; trial < 300; trial++ {
			clear(s.occ)
			var boxes []int32
			for k := 1 + next(s.n); k > 0; k-- {
				c := floor[next(len(floor))]
				if !s.occ[c] {
					s.occ[c] = true
					boxes = append(boxes, c)
				}
			}
			s.br.armed, s.br.built = true, false
			for _, b := range boxes {
				for _, d := range s.dirs {
					p, p2 := b+d, b+2*d
					if lv.Wall[p] || s.occ[p] || lv.Wall[p2] || s.occ[p2] {
						continue
					}
					s.occ[b], s.occ[p] = false, true
					want := s.reach(p2, s.mark2)
					if got := s.regionMin(b, p); got != want {
						t.Fatalf("xsokoban %d: regionMin %d reach %d", idx, got, want)
					}
					s.occ[b], s.occ[p] = true, false
					checked++
				}
			}
			s.br.armed = false
		}
	}
	if checked == 0 {
		t.Fatal("nothing checked")
	}
}
