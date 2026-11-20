package main

import (
	"math/rand"
	"os"
	"testing"
)

// TestBitFloodMatchesBFS (exp-408): the bitset flood marks the same cells and
// returns the same minimum as the queue flood, with an extra blocked cell.
func TestBitFloodMatchesBFS(t *testing.T) {
	files := map[string][]int{
		"levels/Large Test Suite/Sven_1623.xsb":    {7, 295, 1001},
		"levels/Large Test Suite/Sasquatch_50.xsb": {29, 43},
		"levels/Large Test Suite/Holland_81.xsb":   {4, 81},
		"levels/Large Test Suite/XSokoban_90.xsb":  {1, 50},
	}
	rng := rand.New(rand.NewSource(7))
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
			bb := newBitBoard(lv)
			if bb == nil {
				continue
			}
			var floor []int32
			for c := range lv.Wall {
				if !lv.Wall[c] {
					floor = append(floor, int32(c))
				}
			}
			occ := make([]bool, len(lv.Wall))
			mark := make([]uint32, len(lv.Wall))
			ref := make([]bool, len(lv.Wall))
			dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
			for trial := 0; trial < 300; trial++ {
				clear(occ)
				for k := rng.Intn(len(floor)/2 + 1); k > 0; k-- {
					occ[floor[rng.Intn(len(floor))]] = true
				}
				bb.build(occ)
				pc := floor[rng.Intn(len(floor))]
				nc := floor[rng.Intn(len(floor))]
				if pc == nc {
					continue
				}
				occ[pc] = false // the player's square is free by construction
				bb.build(occ)
				occ[nc] = true
				// reference BFS
				clear(ref)
				q := []int32{pc}
				ref[pc] = true
				minC := pc
				for j := 0; j < len(q); j++ {
					for _, d := range dirs {
						y := q[j] + d
						if !ref[y] && !lv.Wall[y] && !occ[y] {
							ref[y] = true
							q = append(q, y)
							minC = min(minC, y)
						}
					}
				}
				occ[nc] = false
				ps := uint32(trial + 1)
				got := bb.flood(pc, nc, mark, ps)
				if got != minC {
					t.Fatalf("%s #%d: min %d want %d", path, i, got, minC)
				}
				for c := range mark {
					if (mark[c] == ps) != ref[c] {
						t.Fatalf("%s #%d: cell %d mark %v want %v", path, i, c, mark[c] == ps, ref[c])
					}
				}
			}
		}
	}
}
