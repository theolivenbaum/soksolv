package main

import (
	"slices"
	"testing"
	"time"

	"soksolv/verify"
)

func tinyPushSolvable(lv *Level) bool {
	type state struct{ a, b, p int32 }
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	flood := func(a, b, p int32) (int32, []bool) {
		seen := make([]bool, len(lv.Wall))
		q := []int32{p}
		seen[p] = true
		norm := p
		for i := 0; i < len(q); i++ {
			norm = min(norm, q[i])
			for _, d := range dirs {
				x := q[i] + d
				if !lv.Wall[x] && x != a && x != b && !seen[x] {
					seen[x] = true
					q = append(q, x)
				}
			}
		}
		return norm, seen
	}
	norm, _ := flood(lv.Boxes[0], lv.Boxes[1], lv.Player)
	root := state{lv.Boxes[0], lv.Boxes[1], norm}
	q := []state{root}
	seen := map[state]bool{root: true}
	for i := 0; i < len(q); i++ {
		st := q[i]
		if lv.Goal[st.a] && lv.Goal[st.b] {
			return true
		}
		_, reach := flood(st.a, st.b, st.p)
		for _, box := range []int32{st.a, st.b} {
			for _, d := range dirs {
				to := box + d
				if !reach[box-d] || lv.Wall[to] || to == st.a || to == st.b {
					continue
				}
				a, b := st.a, st.b
				if a == box {
					a = to
				} else {
					b = to
				}
				if a > b {
					a, b = b, a
				}
				norm, _ := flood(a, b, box)
				child := state{a, b, norm}
				if !seen[child] {
					seen[child] = true
					q = append(q, child)
				}
			}
		}
	}
	return false
}

func TestFESSAgainstExhaustiveTinyPushOracle(t *testing.T) {
	withNodeBudget(t, 200000)
	checked, solvable := 0, 0
	for g1 := 0; g1 < 9; g1++ {
		for g2 := g1 + 1; g2 < 9; g2++ {
			for b1 := 0; b1 < 9; b1++ {
				for b2 := b1 + 1; b2 < 9; b2++ {
					rows := []string{"#####", "#   #", "#   #", "#   #", "#####"}
					grid := make([][]byte, 5)
					for i, r := range rows {
						grid[i] = []byte(r)
					}
					for _, g := range []int{g1, g2} {
						grid[1+g/3][1+g%3] = '.'
					}
					for _, b := range []int{b1, b2} {
						c := &grid[1+b/3][1+b%3]
						if *c == '.' {
							*c = '*'
						} else {
							*c = '$'
						}
					}
					p := 0
					for p == b1 || p == b2 {
						p++
					}
					c := &grid[1+p/3][1+p%3]
					if *c == '.' {
						*c = '+'
					} else {
						*c = '@'
					}
					for i := range rows {
						rows[i] = string(grid[i])
					}
					lv, err := parseLevel(rows)
					if err != nil {
						t.Fatal(err)
					}
					want := tinyPushSolvable(lv)
					s := newSolver(lv)
					res := s.solve(time.Now().Add(time.Minute), hugeMem)
					checked++
					if want {
						solvable++
					}
					if res.Solved != want {
						t.Fatalf("oracle=%v FESS=%v (%s): %v", want, res.Solved, res.Reason, rows)
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
	if checked != 1296 || solvable == 0 {
		t.Fatalf("fixture enumeration failed: %d/%d", checked, solvable)
	}
}

func TestFESSExpiredPreprocessingAndPortfolioStop(t *testing.T) {
	lv, err := parseLevel([]string{"#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stop := range []bool{false, true} {
		Stop.Store(stop)
		deadline := time.Now().Add(-time.Second)
		if stop {
			deadline = time.Now().Add(time.Second)
		}
		s := newSolverUntil(lv, deadline)
		res := s.solve(deadline, hugeMem)
		Stop.Store(false)
		if res.Solved || res.Reason != "timeout" || !s.cancelled {
			t.Fatalf("expired search continued: %+v", res)
		}
		if !slices.Equal(lv.Boxes, []int32{int32(2*lv.W + 4), int32(3*lv.W + 6), int32(4*lv.W + 4)}) {
			t.Fatal("preprocessing mutated level boxes")
		}
	}
}
