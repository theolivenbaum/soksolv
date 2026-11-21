package main

import (
	"math/rand"
	"os"
	"testing"
)

// TestFcMarkMatchesScan (exp-415): fcMark/fcActive over per-component cell
// lists equal the original full-board scans, on random box placements.
func TestFcMarkMatchesScan(t *testing.T) {
	files := map[string][]int{
		"levels/Large Test Suite/Sasquatch_50.xsb": {29, 43},
		"levels/Large Test Suite/Holland_81.xsb":   {4, 81},
		"levels/Large Test Suite/XSokoban_90.xsb":  {1, 50},
	}
	rng := rand.New(rand.NewSource(3))
	var marks, actives int
	fcCheck = func(s *solver, kind int, active bool) {
		f := &s.fc
		w := int32(s.lv.W)
		n8 := [8]int32{1, -1, w, -w, w + 1, w - 1, -w + 1, -w - 1}
		if kind == 0 {
			marks++
			for c := range int32(len(s.lv.Wall)) {
				cb := false
				for _, d := range n8 {
					y := c + d
					if y >= 0 && y < int32(len(s.lv.Wall)) && f.gen[y] == f.stamp && f.used[f.comp[y]] && s.occ[c] {
						cb = true
					}
				}
				if (f.cb[c] == f.inStamp) != cb {
					t.Fatalf("cb[%d]: got %v want %v", c, f.cb[c] == f.inStamp, cb)
				}
			}
			return
		}
		actives++
		want := false
		for c := range int32(len(s.lv.Wall)) {
			if f.fcIn(c) && s.lv.Goal[c] || f.cb[c] == f.inStamp && !s.lv.Goal[c] {
				want = true
				break
			}
		}
		if active != want {
			t.Fatalf("fcActive: got %v want %v", active, want)
		}
	}
	defer func() { fcCheck = nil }()
	for path, idxs := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skip("level files not available")
		}
		levels := splitCollection(data)
		for _, i := range idxs {
			lv, err := parseLevel(levels[i-1])
			if err != nil {
				t.Fatal(err)
			}
			s := newSolver(lv)
			var floor []int32
			for c := range lv.Wall {
				if !lv.Wall[c] {
					floor = append(floor, int32(c))
				}
			}
			for trial := 0; trial < 400; trial++ {
				clear(s.occ)
				ctr := floor[rng.Intn(len(floor))]
				for k := 0; k < len(lv.Boxes)+rng.Intn(4); k++ {
					c := floor[rng.Intn(len(floor))]
					if trial%2 == 0 { // cluster boxes near one cell to make corrals
						c = ctr + int32(rng.Intn(7)-3) + int32(rng.Intn(7)-3)*int32(lv.W)
						if c < 0 || int(c) >= len(lv.Wall) || lv.Wall[c] {
							continue
						}
					}
					s.occ[c] = true
				}
				p := floor[rng.Intn(len(floor))]
				if s.occ[p] {
					continue
				}
				s.reach(p, s.mark)
				s.festCorral(p, s.stamp)
			}
		}
	}
	if marks == 0 || actives == 0 {
		t.Fatalf("hook never ran: marks %d actives %d", marks, actives)
	}
	t.Logf("checked %d marks, %d actives", marks, actives)
}
