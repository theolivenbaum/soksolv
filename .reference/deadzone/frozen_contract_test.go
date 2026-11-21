package deadzone

// exp-586: frozenOff (the zone search's child freeze prune) only reports a
// position whose greatest fixed set (fixedBoxes) holds a box off goal, even
// though frozen(x, &off) alone can over-report off when it returns false.

import (
	"math/rand"
	"testing"
)

func TestFrozenOffVsFixedSet(t *testing.T) {
	rng := rand.New(rand.NewSource(586))
	flagged, overReports := 0, 0
	for trial := 0; trial < 4000; trial++ {
		W, H := 6+rng.Intn(5), 6+rng.Intn(5)
		n := W * H
		wall, goal, dead := make([]bool, n), make([]bool, n), make([]bool, n)
		var floor []int32
		for c := range n {
			x, y := c%W, c/W
			if x == 0 || y == 0 || x == W-1 || y == H-1 || rng.Float64() < 0.2 {
				wall[c] = true
			} else {
				floor = append(floor, int32(c))
				goal[c] = rng.Float64() < 0.3
				dead[c] = rng.Float64() < 0.15
			}
		}
		b := NewBoard(wall, goal, dead, W)
		c := NewChecker(b, 100)
		var boxes []int32
		for _, f := range floor {
			if rng.Float64() < 0.45 {
				boxes = append(boxes, f)
				c.occ[f] = true
			}
		}
		occ := make([]bool, n)
		copy(occ, c.occ)
		c.fixedBoxes(occ, boxes)
		offFixed := false
		for _, x := range boxes {
			offFixed = offFixed || c.fixed[x] && !goal[x]
		}
		fixed := append([]bool(nil), c.fixed...)
		for _, x := range boxes {
			c.fixed[x] = false
		}
		for _, x := range boxes {
			if c.frozenOff(x) {
				flagged++
				if !offFixed || !fixed[x] {
					t.Fatalf("frozenOff(%d) without an off-goal fixed box (fixed[x]=%v)", x, fixed[x])
				}
			}
			off := false
			if !c.frozen(x, &off) && off {
				overReports++
			}
			for _, f := range c.fz {
				if f {
					t.Fatal("fz scratch left dirty")
				}
			}
		}
	}
	if overReports == 0 {
		t.Fatal("expected some false-result over-reports of off (the exp-553 pattern)")
	}
	t.Logf("frozenOff flagged %d, frozen=false with off=true %d (harmless: callers ignore off then)", flagged, overReports)
}
