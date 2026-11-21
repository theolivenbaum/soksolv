package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"soksolv/holes"
	"soksolv/reach"
)

// exp-464 census (SOKSOLV_EXP464_STATS=out.tsv [SOKSOLV_SHARD=i/n]): per LTS level, the cells
// -close-holes closes, and how much -exact-reach tightens the tables:
// start-box reach (sum over start boxes of cells a lone box can reach, relaxed vs exact),
// dead squares (cells with no push path to a goal), and the start matching distance h0.
func TestExp464Stats(t *testing.T) {
	out := os.Getenv("SOKSOLV_EXP464_STATS")
	if out == "" {
		t.Skip("set SOKSOLV_EXP464_STATS=out.tsv")
	}
	shard, nshard := 0, 1
	if s := os.Getenv("SOKSOLV_SHARD"); s != "" {
		a, b, _ := strings.Cut(s, "/")
		shard, _ = strconv.Atoi(a)
		nshard, _ = strconv.Atoi(b)
	}
	files, _ := filepath.Glob("levels/Large Test Suite/*.xsb")
	var sb strings.Builder
	sb.WriteString("level\tfloor\tclosed\tboxreach_rel\tboxreach_exact\tdead_rel\tdead_exact\th0_rel\th0_exact\tboxreach_closed\tboxreach_both\tdead_closed\tdead_both\th0_closed\th0_both\ttabdiff_exact\n")
	k := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, rows := range splitCollection(data) {
			k++
			if k%nshard != shard {
				continue
			}
			holes.Enabled = false
			lv, err := parseLevel(rows)
			if err != nil {
				continue
			}
			floor := 0
			for _, w := range lv.Wall {
				if !w {
					floor++
				}
			}
			lc := *lv
			lc.Wall = append([]bool(nil), lv.Wall...)
			closed := holes.Close(lc.Wall, lc.Goal, lc.W, lc.Boxes, lc.Player)
			var tabs [2]*solver
			stats := func(l *Level, exact bool) (br, dead, h0 int) {
				reach.Enabled = exact
				s := newSolver(l)
				if l == lv {
					if exact {
						tabs[1] = s
					} else {
						tabs[0] = s
					}
				}
				for c, d := range s.dist {
					if d < 0 && !l.Wall[c] {
						dead++
					}
				}
				for _, b := range l.Boxes {
					br += loneBoxCells(l, b, exact)
				}
				h0 = s.matchDist(l.Boxes)
				return
			}
			br0, d0, h00 := stats(lv, false)
			br1, d1, h01 := stats(lv, true)
			// the same on the closed board (closing alone, then both flags)
			br2, d2, h02 := stats(&lc, false)
			br3, d3, h03 := stats(&lc, true)
			tabdiff := 0
			if !slices.Equal(tabs[0].dist, tabs[1].dist) || !slices.Equal(tabs[0].goalDist, tabs[1].goalDist) {
				tabdiff = 1
			}
			fmt.Fprintf(&sb, "%s_%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", filepath.Base(f), i+1, floor, closed, br0, br1, d0, d1, h00, h01, br2, br3, d2, d3, h02, h03, tabdiff)
		}
	}
	reach.Enabled = false
	if err := os.WriteFile(out, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loneBoxCells counts the cells one box can reach alone from its start (relaxed: the player's cell
// must not be a wall; exact: the player-side graph restricted to this box).
func loneBoxCells(lv *Level, b int32, exact bool) int {
	if exact {
		g := reach.New(lv.Wall, lv.W, []int32{b}, lv.Player)
		n := 0
		for _, ok := range g.Cells() {
			if ok {
				n++
			}
		}
		return n
	}
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	seen := map[int32]bool{b: true}
	q := []int32{b}
	for len(q) > 0 {
		c := q[0]
		q = q[1:]
		for _, d := range dirs {
			if t := c + d; !lv.Wall[t] && !lv.Wall[c-d] && !seen[t] {
				seen[t] = true
				q = append(q, t)
			}
		}
	}
	return len(seen)
}
