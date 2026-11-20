package main

import (
	"slices"
	"testing"
	"time"
)

// TestHubReachSound (exp-463): every box set reachable from the start by real
// play (brute force over a small level) passes the exact-reach matching, and
// the class counts hold on it, so the necessary conditions never reject a
// reachable position. A generator run with the constraint keeps the counts.
func TestHubReachSound(t *testing.T) {
	for _, rows := range [][]string{{
		"##########",
		"#  #     #",
		"#@ $  $ ##",
		"#  # #   #",
		"## .  .  #",
		"##########",
	}, {
		"##########",
		"#   $ .  #",
		"#@  ######",
		"#   $ .  #",
		"##########",
	}} {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		ps := newPullSolver(lv, time.Now().Add(time.Minute))
		ps.pfOn = true
		r := newHubReach(ps)
		if r == nil {
			t.Fatal("no reach")
		}
		type st struct {
			boxes [8]int32
			p     int32
		}
		cells := len(lv.Wall)
		dirs := ps.dirs
		norm := func(boxes []int32, p int32) st {
			occ := make([]bool, cells)
			for _, b := range boxes {
				occ[b] = true
			}
			seen := make([]bool, cells)
			q := []int32{p}
			seen[p] = true
			minC := p
			for i := 0; i < len(q); i++ {
				for _, d := range dirs {
					y := q[i] + d
					if !lv.Wall[y] && !occ[y] && !seen[y] {
						seen[y] = true
						q = append(q, y)
						minC = min(minC, y)
					}
				}
			}
			var s st
			sb := slices.Clone(boxes)
			slices.Sort(sb)
			copy(s.boxes[:], sb)
			s.p = minC
			return s
		}
		start := slices.Clone(lv.Boxes)
		seen := map[st]bool{norm(start, lv.Player): true}
		queue := []st{norm(start, lv.Player)}
		for i := 0; i < len(queue) && i < 200000; i++ {
			s := queue[i]
			boxes := s.boxes[:len(start)]
			if !r.feasible(boxes) {
				t.Fatalf("reachable position rejected by the matching: %v", boxes)
			}
			var cnt [64]int32
			u16 := make([]uint16, len(boxes))
			for j, b := range boxes {
				u16[j] = uint16(b)
			}
			r.counts(u16, &cnt)
			for j := range r.nCls {
				if cnt[j] < r.need[j] {
					t.Fatalf("reachable position breaks class %d: %v", j, boxes)
				}
			}
			occ := make([]bool, cells)
			for _, b := range boxes {
				occ[b] = true
			}
			// player region of s
			reach := make([]bool, cells)
			q := []int32{s.p}
			reach[s.p] = true
			for k := 0; k < len(q); k++ {
				for _, d := range dirs {
					y := q[k] + d
					if !lv.Wall[y] && !occ[y] && !reach[y] {
						reach[y] = true
						q = append(q, y)
					}
				}
			}
			for bi, b := range boxes {
				for _, d := range dirs {
					if !reach[b-d] || lv.Wall[b+d] || occ[b+d] {
						continue
					}
					nb := slices.Clone(boxes)
					nb[bi] = b + d
					n := norm(nb, b)
					if !seen[n] {
						seen[n] = true
						queue = append(queue, n)
					}
				}
			}
		}
		t.Logf("%d positions, %d classes", len(queue), r.nCls)
		// the generator with the constraint keeps the counts on every node
		defer func(v bool) { hubGenReach = v }(hubGenReach)
		hubGenReach = true
		g := newHubGen(ps)
		g.run(time.Now().Add(300*time.Millisecond), 3000)
		if g.reach == nil {
			t.Fatal("reach off in the generator")
		}
		for id := range g.nodes {
			var cnt [64]int32
			g.reach.counts(g.nodeBoxes(int32(id)), &cnt)
			for j := range r.nCls {
				if cnt[j] < r.need[j] {
					t.Fatalf("node %d breaks class %d", id, j)
				}
			}
		}
	}
}
