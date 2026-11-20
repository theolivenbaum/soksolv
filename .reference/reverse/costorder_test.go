package reverse

import (
	"os"
	"testing"
)

// TestCostOrderMatchesScan (exp-408): the nearest-first cost equals the
// original scan of all free start squares, for random box placements.
func TestCostOrderMatchesScan(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/XSokoban_90.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := uint64(99)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	for _, idx := range []int{1, 6, 13, 29, 54, 89} {
		lv, err := parseLevel(levels[idx-1])
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		s.perturb(2)
		var floor []int32
		for c := range lv.Wall {
			if !lv.Wall[c] {
				floor = append(floor, int32(c))
			}
		}
		for trial := 0; trial < 50; trial++ {
			clear(s.occ)
			for k := next(s.n + 1); k > 0; k-- {
				s.occ[floor[next(len(floor))]] = true
			}
			for _, b := range lv.Boxes { // sometimes occupy start squares
				if next(2) == 0 {
					s.occ[b] = true
				}
			}
			s.setFree()
			for _, c := range floor {
				if a, b := s.cost(c), s.costScan(c); a != b {
					t.Fatalf("xsokoban %d cell %d: cost %d scan %d", idx, c, a, b)
				}
			}
		}
	}
}
