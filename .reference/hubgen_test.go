package main

import (
	"os"
	"testing"
	"time"

	"soksolv/meet"
)

// TestHubGenTail: every generated node's tail, played from the node's board,
// takes the boxes to the goals (exp-453).
func TestHubGenTail(t *testing.T) {
	for _, rows := range [][]string{hubTestLevel, {
		"##########",
		"#        #",
		"# $$ $.. #",
		"#@  $ .. #",
		"#  $   . #",
		"##########",
	}} {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		ps := newPullSolver(lv, time.Now().Add(time.Minute))
		ps.pfOn = true
		g := newHubGen(ps)
		if g == nil {
			t.Fatal("no generator")
		}
		g.run(time.Now().Add(300*time.Millisecond), 3000)
		checked := 0
		for id := range g.nodes {
			if id%7 != 0 && id != len(g.nodes)-1 {
				continue
			}
			boxes := make([]int32, g.n)
			g.unpack(int32(id), boxes)
			tail := g.tail(int32(id))
			if g.nodes[id].parent >= 0 && len(tail) == 0 {
				t.Fatalf("node %d has no tail", id)
			}
			moves := meet.Complete(lv.Wall, lv.W, boxes, g.nodes[id].player, tail)
			if moves == "" && len(tail) > 0 {
				t.Fatalf("node %d: tail not playable", id)
			}
			occ := map[int32]bool{}
			for _, b := range boxes {
				occ[b] = true
			}
			p := g.nodes[id].player
			dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
			for i := 0; i < len(moves); i++ {
				c := moves[i]
				push := c >= 'A' && c <= 'Z'
				d := dirs[c|0x20]
				if push {
					if !occ[p+d] || occ[p+2*d] || lv.Wall[p+2*d] {
						t.Fatalf("node %d: illegal push at move %d", id, i)
					}
					delete(occ, p+d)
					occ[p+2*d] = true
				} else if occ[p+d] || lv.Wall[p+d] {
					t.Fatalf("node %d: illegal walk at move %d", id, i)
				}
				p += d
			}
			for b := range occ {
				if !ps.pfGoal(b) {
					t.Fatalf("node %d: box left off the goals at %d (%d nodes, depth %d)", id, b, len(g.nodes), g.nodes[id].depth)
				}
			}
			checked++
		}
		if checked < 5 {
			t.Fatalf("only %d nodes checked", checked)
		}
		t.Logf("%d nodes, %d checked, expansions %d", len(g.nodes), checked, g.expanded)
	}
}

// TestHubGenPFIneff (exp-696): with -pf-ineff no generated pull leaves a box
// in a forbidden pull tunnel (start squares exempt), and some pull is dropped.
func TestHubGenPFIneff(t *testing.T) {
	data, err := os.ReadFile("levels/Open Test Suite/Sokodeal.xsb")
	if err != nil {
		t.Skip(err)
	}
	lv, err := parseLevel(splitCollection(data)[71])
	if err != nil {
		t.Fatal(err)
	}
	count := func(on bool) (bad, nodes int) {
		svPFIneff := PFIneff
		PFIneff = on
		defer func() { PFIneff = svPFIneff }()
		ps := newPullSolver(lv, time.Now().Add(time.Minute))
		ps.pfOn = true
		g := newHubGen(ps)
		g.run(time.Now().Add(5*time.Second), 3000)
		tun := ps.festTunnels()
		for id := range g.nodes {
			nd := g.nodes[id]
			if nd.parent < 0 {
				continue
			}
			x := int32(nd.mv & 0xFFFF)
			if tun[x] && !ps.lv.Goal[x] {
				bad++
			}
		}
		return bad, len(g.nodes)
	}
	offBad, offN := count(false)
	onBad, onN := count(true)
	t.Logf("tunnel destinations: off %d of %d nodes, on %d of %d", offBad, offN, onBad, onN)
	if onBad != 0 {
		t.Fatalf("-pf-ineff: %d generated pulls end in a forbidden tunnel", onBad)
	}
}
