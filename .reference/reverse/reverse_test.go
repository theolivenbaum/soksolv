package reverse

import (
	"os"
	"slices"
	"testing"
	"time"

	"soksolv/verify"
)

// TestMicroban solves the first Microban levels backwards and replays every
// solution with the frozen verifier.
func TestMicroban(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := verify.SplitLevels(data)
	solved := 0
	for i, rows := range levels[:20] {
		r, err := Solve(rows, time.Now().Add(time.Second), 512<<20)
		if err != nil {
			t.Fatalf("level %d: %v", i+1, err)
		}
		if !r.Solved {
			continue
		}
		if err := verify.Check(rows, r.Solution); err != nil {
			t.Fatalf("level %d: invalid solution: %v", i+1, err)
		}
		solved++
	}
	t.Logf("solved %d/20", solved)
	if solved < 15 {
		t.Errorf("only %d/20 solved", solved)
	}
}

// matchChild (incremental repair) must equal matchCost (from scratch) for
// every one-box move from random states.
func TestMatchChildAgreesWithMatchCost(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/XSokoban_90.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := uint64(12345)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	compared := 0
	for _, idx := range []int{1, 6, 13, 29, 54, 89} {
		lv, err := parseLevel(levels[idx-1])
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		s.perturb(2)
		var floor []int32
		for c := range lv.Wall {
			if s.dist[c] >= 0 {
				floor = append(floor, int32(c))
			}
		}
		for trial := 0; trial < 30; trial++ {
			occ := map[int32]bool{}
			var cur []int32
			for len(cur) < s.n {
				c := floor[next(len(floor))]
				if !occ[c] {
					occ[c] = true
					cur = append(cur, c)
				}
			}
			slices.Sort(cur)
			s.matchCost(cur)
			s.saveMatch()
			for i, b := range cur {
				for _, d := range s.dirs {
					to := b + d
					if lv.Wall[to] || occ[to] || s.dist[to] < 0 {
						continue
					}
					got := s.matchChild(cur, i, to)
					child := slices.Clone(cur)
					child[i] = to
					slices.Sort(child)
					want := s.matchCost(child)
					if got != want {
						t.Fatalf("level %d: matchChild %d != matchCost %d", idx, got, want)
					}
					compared++
				}
			}
		}
	}
	t.Logf("%d child matchings compared", compared)
}
