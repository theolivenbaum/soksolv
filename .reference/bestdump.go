package main

// exp-716 Gate 0 diagnostic (-best-dump DIR, default off): a forward lane run
// alone (-strategy macro|novelty -nodes N) tracks its best expanded board
// (most boxes on goals, ties: smaller h; exp-678's rule) and at 100k, 200k and
// 400k lane expansions writes the residual level: boxes frozen on goals become
// walls, the player stands where the path leaves it. Files: DIR/LANE-N.xsb
// (residual), DIR/LANE-N.path (the LURD prefix), DIR/LANE-N.info (one line:
// frozen, left, live, prefix length, expansion of the best).

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var bestDumpDir string

// bdCount: lane expansions across the lane's solver instances (ring replans, revives).
var bdCount [16]atomic.Int64

func init() {
	flag.StringVar(&bestDumpDir, "best-dump", "", "exp-716 diagnostic: macro/novelty run alone write their best board's residual (frozen-on-goal boxes as walls) at 100k/200k/400k lane expansions into this directory")
}

// noteBestDump (macro/novelty, -best-dump): called at each expansion, s.occ holds cur.
func (s *solver) noteBestDump(id int32, cur []int32, h int32) {
	var on int32
	for _, b := range cur {
		if s.lv.Goal[b] {
			on++
		}
	}
	if s.bdID == 0 || on > s.bdOn || on == s.bdOn && h < s.bdH {
		s.bdOn, s.bdH, s.bdID, s.bdAt = on, h, id+1, bdCount[s.laneID].Load()
	}
	n := bdCount[s.laneID].Add(1)
	if n != 100000 && n != 200000 && n != 400000 {
		return
	}
	lane := "macro"
	if s.novelty > 0 {
		lane = "novelty"
	}
	for _, b := range cur {
		s.occ[b] = false
	}
	best := make([]int32, len(cur))
	s.unpack(s.bdID-1, best)
	for _, b := range best {
		s.occ[b] = true
	}
	var frozen []int32
	for _, b := range best {
		if s.lv.Goal[b] {
			off := false
			if s.frozen(b, &off) {
				frozen = append(frozen, b)
			}
		}
	}
	for _, b := range best {
		s.occ[b] = false
	}
	pm, pa := s.stats.PathMoves, s.stats.PathAdvisors
	path := s.reconstruct(s.bdID - 1) // clears s.occ
	s.stats.PathMoves, s.stats.PathAdvisors = pm, pa
	for _, b := range cur {
		s.occ[b] = true
	}
	pl := s.lv.Player
	for i := 0; i < len(path); i++ {
		pl += s.dirs[strings.IndexByte(dirChars, path[i]|0x20)]
	}
	lv := s.lv
	nc := len(lv.Wall)
	walls := cellSet(nc, frozen)
	var rb, rg []int32
	for _, x := range best {
		if !walls[x] {
			rb = append(rb, x)
		}
	}
	for c := range nc {
		if lv.Goal[c] && !walls[c] {
			rg = append(rg, int32(c))
		}
	}
	f := &factor{lv: lv}
	res := f.rows(walls, cellSet(nc, rb), cellSet(nc, rg), pl)
	live := residualLive(lv, walls, rb, rg)
	base := filepath.Join(bestDumpDir, fmt.Sprintf("%s-%d", lane, n))
	os.MkdirAll(bestDumpDir, 0o755)
	os.WriteFile(base+".xsb", []byte(strings.Join(res, "\n")+"\n"), 0o644)
	os.WriteFile(base+".path", []byte(path+"\n"), 0o644)
	os.WriteFile(base+".info", []byte(fmt.Sprintf("frozen %d left %d live %v prefix %d best_at %d on %d h %d\n", len(frozen), len(rb), live, len(path), s.bdAt, s.bdOn, s.bdH)), 0o644)
}

// residualLive: every residual box can reach a residual goal alone and a
// perfect matching exists (walls = level walls plus frozen boxes). exp-674.
func residualLive(lv *Level, walls []bool, boxes, goals []int32) bool {
	if len(boxes) != len(goals) {
		return false
	}
	n := len(lv.Wall)
	d := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	wall := func(c int32) bool { return lv.Wall[c] || walls[c] }
	idx := make([]int, n)
	for i := range idx {
		idx[i] = -1
	}
	for i, b := range boxes {
		idx[b] = i
	}
	adj := make([][]int, len(goals)) // goal -> boxes that can reach it alone
	seen := make([]bool, n)
	q := make([]int32, 0, n)
	for gi, g := range goals {
		clear(seen)
		q = append(q[:0], g)
		seen[g] = true
		for i := 0; i < len(q); i++ {
			c := q[i]
			if j := idx[c]; j >= 0 {
				adj[gi] = append(adj[gi], j)
			}
			for _, dd := range d { // pull: box at c came from c+dd, player at c+2dd
				nb, p := c+dd, c+2*dd
				if !wall(nb) && !wall(p) && !seen[nb] {
					seen[nb] = true
					q = append(q, nb)
				}
			}
		}
		if len(adj[gi]) == 0 {
			return false
		}
	}
	match := make([]int, len(boxes)) // box -> goal
	for i := range match {
		match[i] = -1
	}
	vis := make([]bool, len(boxes))
	var try func(g int) bool
	try = func(g int) bool {
		for _, b := range adj[g] {
			if vis[b] {
				continue
			}
			vis[b] = true
			if match[b] < 0 || try(match[b]) {
				match[b] = g
				return true
			}
		}
		return false
	}
	for g := range goals {
		clear(vis)
		if !try(g) {
			return false
		}
	}
	return true
}
