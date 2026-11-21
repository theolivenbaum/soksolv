package main

import (
	"maps"
	"testing"
)

func TestMacroRoutesMatchIndependentPushBFS(t *testing.T) {
	fixtures := [][]string{
		{"#############", "#     #     #", "# @$      . #", "#     #     #", "#############"},
		{"#########", "#@ $    #", "#  # $ .#", "#  $ .. #", "#########"},
		{"##########", "#@ $$  ..#", "#  #  #  #", "#        #", "##########"},
	}
	for _, rows := range fixtures {
		s := testSolver(t, rows...)
		for _, moved := range s.lv.Boxes {
			fixed := make([]bool, len(s.occ))
			for _, b := range s.lv.Boxes {
				if b != moved {
					fixed[b] = true
				}
				s.occ[b] = true
			}
			flood := func(box, player int32) (int32, []bool) {
				seen := make([]bool, len(fixed))
				q := []int32{player}
				seen[player] = true
				norm := player
				for i := 0; i < len(q); i++ {
					norm = min(norm, q[i])
					for _, d := range s.dirs {
						x := q[i] + d
						if !s.lv.Wall[x] && !fixed[x] && x != box && !seen[x] {
							seen[x] = true
							q = append(q, x)
						}
					}
				}
				return norm, seen
			}
			type state struct{ box, player int32 }
			norm, _ := flood(moved, s.lv.Player)
			root := state{moved, norm}
			q := []state{root}
			seen := map[state]bool{root: true}
			want := map[state]bool{}
			for i := 0; i < len(q); i++ {
				st := q[i]
				_, reach := flood(st.box, st.player)
				for _, d := range s.dirs {
					to := st.box + d
					if !reach[st.box-d] || s.lv.Wall[to] || fixed[to] || s.dist[to] < 0 {
						continue
					}
					np, _ := flood(to, st.box)
					child := state{to, np}
					want[child] = true
					if !seen[child] {
						seen[child] = true
						q = append(q, child)
					}
				}
			}
			got := map[state]bool{}
			s.reach(s.lv.Player, s.mark)
			s.boxSearch(moved, s.stamp, func(k, to int32) bool { np, _ := flood(to, k/4); got[state{to, np}] = true; return true })
			if !maps.Equal(got, want) {
				t.Fatalf("macro endpoints differ for box %d: got %v want %v", moved, got, want)
			}
			clear(s.occ)
		}
	}
}
