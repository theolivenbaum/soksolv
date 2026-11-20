package main

// exp-586: the freeze test's off-goal flag. frozen(x, &off) sets off for every
// box it found frozen while exploring, including boxes that were frozen only
// under the hypothesis "x is a wall". When frozen(x) itself returns false that
// hypothesis is void, so off is meaningless (the exp-553 over-report). The
// production callers (freezeDeadlock, deadzone.frozenOff) only read off when
// frozen returned true, and then every box that set it belongs to one closed,
// mutually blocking set: sound. These tests pin both facts.

import (
	"math/rand"
	"slices"
	"testing"
)

// TestFrozenOffOnlyWhenTrue: the over-report exists for a false result, and
// freezeDeadlock is not fooled by it.
func TestFrozenOffOnlyWhenTrue(t *testing.T) {
	// y (off goal) has a wall above and x to its right, floor to its left; x
	// can move vertically, so y is not frozen.
	// frozen(x): x's horizontal axis probes y, which is "frozen" given x as a
	// wall (sets off); x's vertical axis is open, so frozen(x) is false.
	rows := []string{
		"########",
		"#      #",
		"#  #   #",
		"#  $*. #",
		"#   @  #",
		"#      #",
		"########",
	}
	lv, err := parseLevelRaw(rows)
	if err != nil {
		t.Fatal(err)
	}
	s := newSolver(lv)
	for _, b := range lv.Boxes {
		s.occ[b] = true
	}
	x := int32(4*lv.W + 5) // the '*' (padded: row 3 col 4 -> (3+1)*W + 4+1)
	if !s.occ[x] {
		t.Fatal("bad board")
	}
	off := false
	if s.frozen(x, &off) {
		t.Fatal("x should not be frozen")
	}
	if !off {
		t.Fatal("expected the documented over-report: off set although frozen(x) is false")
	}
	if s.freezeDeadlock(x) {
		t.Fatal("freezeDeadlock must not report this position")
	}
	y := x - 1
	if s.freezeDeadlock(y) {
		t.Fatal("y alone is not frozen")
	}
}

// fixedSet is the greatest set of boxes each blocked on both axes by walls,
// members, or (along an axis) dead squares on both sides: boxes none of which
// can ever be the first to move.
func fixedSet(lv *Level, occ []bool, dist []int32, boxes []int32) []bool {
	fixed := make([]bool, len(lv.Wall))
	for _, b := range boxes {
		fixed[b] = true
	}
	blk := func(c int32) bool { return lv.Wall[c] || fixed[c] }
	w := int32(lv.W)
	for changed := true; changed; {
		changed = false
		for _, x := range boxes {
			if !fixed[x] {
				continue
			}
			h := blk(x-1) || blk(x+1) || dist[x-1] < 0 && dist[x+1] < 0
			v := blk(x-w) || blk(x+w) || dist[x-w] < 0 && dist[x+w] < 0
			if !h || !v {
				fixed[x] = false
				changed = true
			}
		}
	}
	return fixed
}

// TestFreezeDeadlockVsFixedSet: on random dense boards, freezeDeadlock(t)
// implies the greatest fixed set holds a box off goal.
func TestFreezeDeadlockVsFixedSet(t *testing.T) {
	rng := rand.New(rand.NewSource(586))
	checked, flagged := 0, 0
	for trial := 0; trial < 4000; trial++ {
		w, h := 4+rng.Intn(5), 4+rng.Intn(5)
		grid := make([][]byte, h+2)
		var floor [][2]int
		for y := range grid {
			grid[y] = make([]byte, w+2)
			for x := range grid[y] {
				if y == 0 || x == 0 || y == h+1 || x == w+1 || rng.Float64() < 0.2 {
					grid[y][x] = '#'
				} else {
					grid[y][x] = ' '
					floor = append(floor, [2]int{x, y})
				}
			}
		}
		if len(floor) < 6 {
			continue
		}
		rng.Shuffle(len(floor), func(i, j int) { floor[i], floor[j] = floor[j], floor[i] })
		nb := len(floor) * (3 + rng.Intn(4)) / 10
		if nb < 1 || nb+1 >= len(floor) {
			continue
		}
		boxes := floor[:nb]
		goals := slices.Clone(floor[1 : nb+1]) // overlap some boxes
		if rng.Intn(2) == 0 {
			goals = slices.Clone(floor[:nb])
			rng.Shuffle(len(floor)-1, func(i, j int) {})
			k := rng.Intn(nb)
			goals[k] = floor[nb]
		}
		isGoal := map[[2]int]bool{}
		for _, g := range goals {
			isGoal[g] = true
		}
		for _, b := range boxes {
			if isGoal[b] {
				grid[b[1]][b[0]] = '*'
			} else {
				grid[b[1]][b[0]] = '$'
			}
		}
		for _, g := range goals {
			if grid[g[1]][g[0]] == ' ' {
				grid[g[1]][g[0]] = '.'
			}
		}
		p := floor[len(floor)-1]
		if grid[p[1]][p[0]] == '.' {
			grid[p[1]][p[0]] = '+'
		} else if grid[p[1]][p[0]] == ' ' {
			grid[p[1]][p[0]] = '@'
		} else {
			continue
		}
		rows := make([]string, len(grid))
		for y := range grid {
			rows[y] = string(grid[y])
		}
		lv, err := parseLevelRaw(rows)
		if err != nil {
			continue
		}
		s := newSolver(lv)
		for _, b := range lv.Boxes {
			s.occ[b] = true
		}
		fixed := fixedSet(lv, s.occ, s.dist, lv.Boxes)
		offFixed := false
		for _, b := range lv.Boxes {
			offFixed = offFixed || fixed[b] && !lv.Goal[b]
		}
		for _, b := range lv.Boxes {
			checked++
			if s.freezeDeadlock(b) {
				flagged++
				if !offFixed {
					t.Fatalf("freezeDeadlock(%d) with no off-goal fixed box\n%s", b, joinRows(rows))
				}
				// the box itself must be in the fixed set
				if !fixed[b] {
					t.Fatalf("freezeDeadlock(%d): box not in the greatest fixed set\n%s", b, joinRows(rows))
				}
			}
			for _, f := range s.fz {
				if f {
					t.Fatal("fz scratch left dirty")
				}
			}
		}
	}
	t.Logf("boxes checked %d, flagged %d", checked, flagged)
}

func joinRows(rows []string) string {
	out := ""
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}
