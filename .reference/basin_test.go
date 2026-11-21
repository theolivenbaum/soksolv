package main

import "testing"

// This independent push search tests the basin's pull search. Other goals
// are permanent walls; any starting player region is allowed.
func canSupplyGoal(s *solver, box, goal int32) bool {
	type state struct{ b, p int32 }
	free := func(c, b int32) bool { return !s.lv.Wall[c] && c != b && (!s.lv.Goal[c] || c == goal) }
	flood := func(b, p int32) (int32, []bool) {
		seen := make([]bool, len(s.lv.Wall))
		seen[p] = true
		q := []int32{p}
		norm := p
		for i := 0; i < len(q); i++ {
			norm = min(norm, q[i])
			for _, d := range s.dirs {
				x := q[i] + d
				if !seen[x] && free(x, b) {
					seen[x] = true
					q = append(q, x)
				}
			}
		}
		return norm, seen
	}
	visited := map[state]bool{}
	var q []state
	for _, p := range s.fcell {
		if free(p, box) {
			norm, _ := flood(box, p)
			st := state{box, norm}
			if !visited[st] {
				visited[st] = true
				q = append(q, st)
			}
		}
	}
	for i := 0; i < len(q); i++ {
		st := q[i]
		if st.b == goal {
			return true
		}
		_, reach := flood(st.b, st.p)
		for _, d := range s.dirs {
			to := st.b + d
			if reach[st.b-d] && free(to, st.b) {
				norm, _ := flood(to, st.b)
				child := state{to, norm}
				if !visited[child] {
					visited[child] = true
					q = append(q, child)
				}
			}
		}
	}
	return false
}

func TestBasinMatchesIndependentPushSearch(t *testing.T) {
	s := testSolver(t, "#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########")
	for _, goal := range s.goals {
		basin := s.targetBasin(goal)
		for _, box := range s.fcell {
			if s.lv.Goal[box] && box != goal {
				if basin[box] {
					t.Fatal("basin contains another final box")
				}
				continue
			}
			if want := canSupplyGoal(s, box, goal); basin[box] != want {
				t.Fatalf("goal=%d box=%d basin=%v want=%v", goal, box, basin[box], want)
			}
		}
	}
}

func TestFESSScoreOrdering(t *testing.T) {
	base := fessScore{2, 3, 4, 5, 6, 7}
	better := []fessScore{{1, 0, 999, 999, 99, 0}, {2, 4, 999, 999, 99, 0}, {2, 3, 3, 999, 99, 0}, {2, 3, 4, 4, 99, 0}, {2, 3, 4, 5, 5, 0}, {2, 3, 4, 5, 6, 8}}
	for _, score := range better {
		if score.rank() >= base.rank() {
			t.Fatalf("%+v must outrank %+v", score, base)
		}
	}
}
