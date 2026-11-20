package main

import (
	"math/rand"
	"testing"
)

func TestIncrementalMatchingAgreesWithFreshAssignment(t *testing.T) {
	previous := HFIncrementalMatch
	HFIncrementalMatch = true
	defer func() { HFIncrementalMatch = previous }()
	rng := rand.New(rand.NewSource(262))
	for sample := 0; sample < 160; sample++ {
		n := 1 + rng.Intn(9)
		cells := 3 * n
		s := newMatchingFixture(n, cells)
		for i := range s.goalDist {
			d := rng.Intn(12)
			s.goalDist[i] = int32(d)
			if d < 3 {
				s.goalDist[i] = -1
			}
		}
		boxes := make([]int32, n)
		for i := range boxes {
			boxes[i] = int32(i)
		}
		s.hfMatchParent(boxes)
		for i := range boxes {
			old := boxes[i]
			for b := n; b < cells; b++ {
				boxes[i] = int32(b)
				got := s.hfMatchChild(boxes, i, old)
				want := s.matchDist(boxes)
				if got != want {
					t.Fatalf("sample %d, row %d, square %d: incremental %d, fresh %d", sample, i, b, got, want)
				}
			}
			boxes[i] = old
		}
	}
}

func newMatchingFixture(n, cells int) *solver {
	m := n + 1
	return &solver{
		n: n, lv: &Level{Wall: make([]bool, cells)}, goalDist: make([]int32, n*cells),
		hu: make([]int32, m), hv: make([]int32, m), hp: make([]int32, m),
		hway: make([]int32, m), hminv: make([]int32, m), hused: make([]bool, m),
	}
}
