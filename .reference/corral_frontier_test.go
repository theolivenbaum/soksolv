package main

import (
	"slices"
	"soksolv/verify"
	"testing"
	"time"
)

func TestCorralFrontierAgainstTinyOracle(t *testing.T) {
	old := FrontierCorral
	FrontierCorral = true
	defer func() { FrontierCorral = old }()
	withNodeBudget(t, 200000)
	t.Run("all-two-box-configurations", TestFESSAgainstExhaustiveTinyPushOracle)
}

func TestCorralFrontierFenceAndOutsidePreparation(t *testing.T) {
	for _, rows := range [][]string{
		{"########", "#@ $  .#", "#  $  .#", "########"},
		{"#########", "#@$ $  .#", "#   $ ..#", "#########"},
	} {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		for _, b := range lv.Boxes {
			s.occ[b] = true
		}
		s.reach(lv.Player, s.mark)
		if s.firstPushCorral(lv.Player, s.stamp) == 0 {
			t.Fatalf("missed simple fence: %v", rows)
		}
	}
}

func TestCorralFrontierThreeBoxOracle(t *testing.T) {
	old := FrontierCorral
	FrontierCorral = true
	defer func() { FrontierCorral = old }()
	withNodeBudget(t, 200000)
	checked, certified := 0, 0
	for b1 := 0; b1 < 12; b1++ {
		for b2 := b1 + 1; b2 < 12; b2++ {
			for b3 := b2 + 1; b3 < 12; b3++ {
				for variant := 0; variant < 4; variant++ {
					grid := [][]byte{[]byte("######"), []byte("#    #"), []byte("#    #"), []byte("#    #"), []byte("######")}
					for _, g := range []int{variant, variant + 4, variant + 8} {
						grid[1+g/4][1+g%4] = '.'
					}
					for _, b := range []int{b1, b2, b3} {
						c := &grid[1+b/4][1+b%4]
						if *c == '.' {
							*c = '*'
						} else {
							*c = '$'
						}
					}
					p := (b1 + variant) % 12
					for p == b1 || p == b2 || p == b3 {
						p = (p + 1) % 12
					}
					c := &grid[1+p/4][1+p%4]
					if *c == '.' {
						*c = '+'
					} else {
						*c = '@'
					}
					rows := make([]string, len(grid))
					for i := range grid {
						rows[i] = string(grid[i])
					}
					lv, err := parseLevel(rows)
					if err != nil {
						t.Fatal(err)
					}
					want := threeBoxPushSolvable(lv)
					s := newSolver(lv)
					res := s.solve(time.Now().Add(time.Minute), hugeMem)
					checked++
					certified += res.Stats.FrontierCorrals
					if want != res.Solved {
						t.Fatalf("oracle=%v frontier=%v (%s): %v", want, res.Solved, res.Reason, rows)
					}
					if res.Solved {
						if err := verify.Check(rows, res.Solution); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
	}
	if checked != 880 || certified == 0 {
		t.Fatalf("fixture coverage %d cases %d certifications", checked, certified)
	}
	t.Logf("%d independently checked three-box positions, %d frontier certifications", checked, certified)
}

// Independent primitive-push BFS: no macros, deadlock tests or corral pruning.
func threeBoxPushSolvable(lv *Level) bool {
	type state struct {
		boxes  [3]int32
		player int32
	}
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	flood := func(b [3]int32, p int32) (int32, []bool) {
		r := make([]bool, len(lv.Wall))
		q := []int32{p}
		r[p] = true
		norm := p
		for head := 0; head < len(q); head++ {
			x := q[head]
			norm = min(norm, x)
			for _, d := range dirs {
				y := x + d
				if !lv.Wall[y] && y != b[0] && y != b[1] && y != b[2] && !r[y] {
					r[y] = true
					q = append(q, y)
				}
			}
		}
		return norm, r
	}
	var boxes [3]int32
	copy(boxes[:], lv.Boxes)
	slices.Sort(boxes[:])
	p, _ := flood(boxes, lv.Player)
	root := state{boxes, p}
	q := []state{root}
	seen := map[state]bool{root: true}
	for head := 0; head < len(q); head++ {
		st := q[head]
		if lv.Goal[st.boxes[0]] && lv.Goal[st.boxes[1]] && lv.Goal[st.boxes[2]] {
			return true
		}
		_, r := flood(st.boxes, st.player)
		for i, b := range st.boxes {
			for _, d := range dirs {
				t := b + d
				if !r[b-d] || lv.Wall[t] || t == st.boxes[0] || t == st.boxes[1] || t == st.boxes[2] {
					continue
				}
				child := st
				child.boxes[i] = t
				slices.Sort(child.boxes[:])
				child.player, _ = flood(child.boxes, b)
				if !seen[child] {
					seen[child] = true
					q = append(q, child)
				}
			}
		}
	}
	return false
}
