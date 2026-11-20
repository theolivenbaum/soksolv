package main

import (
	"math/rand"
	"os"
	"slices"
	"testing"
)

// TestRoomFXFastMatchesGeneral (exp-655): roomBrokenFX1 equals the general
// roomBrokenFXN on random box placements.
func TestRoomFXFastMatchesGeneral(t *testing.T) {
	checked := 0
	defer func() { t.Logf("boards checked: %d", checked) }()
	for _, f := range []string{"levels/Large Test Suite/Sven_1623.xsb", "levels/Large Test Suite/Sasquatch_III_50.xsb", "levels/Large Test Suite/XSokoban_90.xsb"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Skip(err)
		}
		levels := splitCollection(b)
		rng := rand.New(rand.NewSource(11))
		for li := 0; li < len(levels) && li < 60; li += 3 {
			lv, err := parseLevel(levels[li])
			if err != nil {
				continue
			}
			s := newSolver(lv)
			m := s.rooms
			if m == nil || len(m.rooms) < 2 || m.nw != 1 {
				continue
			}
			var free []int32
			for c, w := range lv.Wall {
				if !w {
					free = append(free, int32(c))
				}
			}
			n := len(m.rooms)
			for it := 0; it < 200; it++ {
				clear(s.occ)
				k := rng.Intn(len(free)/2 + 1)
				for i := 0; i < k; i++ {
					s.occ[free[rng.Intn(len(free))]] = true
				}
				checked++
				s.labelCorridorsFX(&m.cur)
				a, c := make([]uint64, n), make([]uint64, n)
				for r := range m.rooms {
					s.roomBrokenFX1(r, a)
					s.roomBrokenFXN(r, c)
				}
				if !slices.Equal(a, c) {
					t.Fatalf("%s #%d: fx1=%v general=%v", f, li, a, c)
				}
			}
		}
	}
}
