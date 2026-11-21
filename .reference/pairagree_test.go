package main

import (
	"os"
	"testing"
	"time"
)

// The allocation-free pair search must agree with the reference on every
// (pair, side) key, and the eager build time is reported.
func TestPairDeadAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: skipped in fast tier (go test -short); see research/exp-371")
	}
	for f, idxs := range map[string][]int{
		"levels/Large Test Suite/Sven_1623.xsb":    {196, 1244},
		"levels/Large Test Suite/XSokoban_90.xsb":  {39, 75},
		"levels/Large Test Suite/SokHard_163.xsb":  {100},
		"levels/Large Test Suite/Microban_155.xsb": {60},
	} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Skip(err)
		}
		levels := splitCollection(data)
		for _, i := range idxs {
			lv, _ := parseLevel(levels[i-1])
			s := newSolver(lv)
			s.pairOffsets()
			var newT, oldT time.Duration
			n, bad := 0, 0
			for _, c := range s.floorCells() {
				if s.dist[c] < 0 || lv.Goal[c] {
					continue
				}
				for _, o := range s.poff {
					x := c + o
					if x < 0 || int(x) >= len(lv.Wall) || lv.Wall[x] {
						continue
					}
					froms := []int32{-1}
					for _, d := range s.dirs {
						if f := c + d; !lv.Wall[f] && f != x {
							froms = append(froms, f)
						}
					}
					for _, from := range froms {
						if from >= 0 && !s.pairSplits(c, x, from) {
							continue
						}
						n++
						t0 := time.Now()
						a := s.pairDeadFrom(c, x, from)
						newT += time.Since(t0)
						t0 = time.Now()
						b := s.pairDeadFromOld(c, x, from)
						oldT += time.Since(t0)
						if a != b {
							bad++
						}
					}
				}
			}
			t.Logf("%s %d: %d keys, %d differ, new %v old %v", f[len(f)-14:], i, n, bad, newT, oldT)
			if bad > 0 {
				t.Fail()
			}
		}
	}
}
