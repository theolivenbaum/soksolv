package main

import (
	"math/rand"
	"os"
	"testing"
)

// TestRoomFastPathsMatchGeneral (exp-649): the single-word room paths equal the
// general ones on random box placements.
func TestRoomFastPathsMatchGeneral(t *testing.T) {
	checked := 0
	defer func() { t.Logf("boards checked: %d", checked) }()
	for _, f := range []string{"levels/Large Test Suite/Sven_1623.xsb", "levels/Large Test Suite/Sasquatch_III_50.xsb", "levels/Large Test Suite/XSokoban_90.xsb"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Skip(err)
		}
		levels := splitCollection(b)
		rng := rand.New(rand.NewSource(7))
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
			for it := 0; it < 200; it++ {
				clear(s.occ)
				k := rng.Intn(len(free)/2 + 1)
				for i := 0; i < k; i++ {
					s.occ[free[rng.Intn(len(free))]] = true
				}
				checked++
				s.labelCorridors(&m.cur)
				for r := range m.rooms {
					if a, b := s.roomBrokenFast1(r), s.roomBrokenFastN(r); a != b {
						t.Fatalf("%s #%d room %d: fast1=%d general=%d", f, li, r, a, b)
					}
				}
				if a, b := s.festRooms1(), s.festRoomsFlood(); a != b {
					t.Fatalf("%s #%d: festRooms1=%d flood=%d", f, li, a, b)
				}
				svOcc := PFRoomsOcc
				PFRoomsOcc = true // exp-696: the rooms_bfs link rule
				a, b := s.festRooms1(), s.festRoomsFlood()
				PFRoomsOcc = svOcc
				if a != b {
					t.Fatalf("%s #%d: -pf-rooms-occ festRooms1=%d flood=%d", f, li, a, b)
				}
			}
		}
	}
}
