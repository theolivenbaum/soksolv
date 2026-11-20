package main

// exp-586: exhaustive soundness audit of the deadlock and pruning checkers on
// random small boards. Every state reachable from the start is enumerated
// (normalized player), its solvability is computed exactly by a backward
// fixpoint over the push graph, and every checker is asked about every
// reachable state / push child. A checker that calls a solvable state dead is
// a wrong prune. The pull-side checkers are asked about forward-reachable
// states, which by reversibility are exactly the pull states that can reach
// the start.
//
// SOKSOLV_AUDIT_N (default 300, 60 with -short) sets the number of random boards; the seed is
// SOKSOLV_AUDIT_SEED (default 586); SOKSOLV_AUDIT_REACH=1 runs with -exact-reach tables.

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"soksolv/cluster"
	"soksolv/deadzone"
	"soksolv/holes"
	"soksolv/reach"
)

type bfGraph struct {
	lv       *Level
	keys     map[string]int32
	boxes    [][]int32 // sorted
	player   []int32   // normalized
	children [][]int32
	solvable []bool
}

func bfKey(boxes []int32, p int32) string {
	var sb strings.Builder
	for _, b := range boxes {
		sb.WriteByte(byte(b))
		sb.WriteByte(byte(b >> 8))
	}
	sb.WriteByte(byte(p))
	sb.WriteByte(byte(p >> 8))
	return sb.String()
}

// bfRegion floods from p over cells that are not walls or boxes; returns the
// region cells and their minimum.
func bfRegion(lv *Level, occ []bool, p int32, mark []bool) ([]int32, int32) {
	clear(mark)
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	q := []int32{p}
	mark[p] = true
	m := p
	for i := 0; i < len(q); i++ {
		for _, d := range dirs {
			if y := q[i] + d; !lv.Wall[y] && !occ[y] && !mark[y] {
				mark[y] = true
				q = append(q, y)
				m = min(m, y)
			}
		}
	}
	return q, m
}

// bruteForce enumerates the states reachable from lv's start (at most limit)
// and their exact solvability. ok=false: too many states.
func bruteForce(lv *Level, limit int) (*bfGraph, bool) {
	n := len(lv.Wall)
	g := &bfGraph{lv: lv, keys: map[string]int32{}}
	occ := make([]bool, n)
	mark := make([]bool, n)
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	add := func(boxes []int32, p int32) int32 {
		for _, b := range boxes {
			occ[b] = true
		}
		_, m := bfRegion(lv, occ, p, mark)
		for _, b := range boxes {
			occ[b] = false
		}
		k := bfKey(boxes, m)
		if id, ok := g.keys[k]; ok {
			return id
		}
		id := int32(len(g.boxes))
		g.keys[k] = id
		g.boxes = append(g.boxes, boxes)
		g.player = append(g.player, m)
		g.children = append(g.children, nil)
		return id
	}
	start := slices.Clone(lv.Boxes)
	slices.Sort(start)
	add(start, lv.Player)
	for i := 0; i < len(g.boxes); i++ {
		if len(g.boxes) > limit {
			return nil, false
		}
		cur := g.boxes[i]
		for _, b := range cur {
			occ[b] = true
		}
		_, _ = bfRegion(lv, occ, g.player[i], mark)
		reg := slices.Clone(mark)
		for _, b := range cur {
			occ[b] = false
		}
		for j, b := range cur {
			for _, d := range dirs {
				t := b + d
				if !reg[b-d] || lv.Wall[t] || slices.Contains(cur, t) {
					continue
				}
				ch := slices.Clone(cur)
				ch[j] = t
				slices.Sort(ch)
				c := add(ch, b)
				g.children[i] = append(g.children[i], c)
			}
		}
	}
	// Backward fixpoint: solved states, then anything with a solvable child.
	parents := make([][]int32, len(g.boxes))
	for i, cs := range g.children {
		for _, c := range cs {
			parents[c] = append(parents[c], int32(i))
		}
	}
	g.solvable = make([]bool, len(g.boxes))
	var q []int32
	for i, bx := range g.boxes {
		solved := true
		for _, b := range bx {
			if !lv.Goal[b] {
				solved = false
				break
			}
		}
		if solved {
			g.solvable[i] = true
			q = append(q, int32(i))
		}
	}
	for k := 0; k < len(q); k++ {
		for _, p := range parents[q[k]] {
			if !g.solvable[p] {
				g.solvable[p] = true
				q = append(q, p)
			}
		}
	}
	return g, true
}

// randomBoard returns the rows of a random small level (border included).
func randomBoard(rng *rand.Rand) []string {
	for {
		w, h := 4+rng.Intn(4), 4+rng.Intn(4)
		wallP := 0.12 + rng.Float64()*0.2
		grid := make([][]byte, h+2)
		for y := range grid {
			grid[y] = make([]byte, w+2)
			for x := range grid[y] {
				if y == 0 || x == 0 || y == h+1 || x == w+1 || rng.Float64() < wallP {
					grid[y][x] = '#'
				} else {
					grid[y][x] = ' '
				}
			}
		}
		var floor [][2]int
		for y := range grid {
			for x := range grid[y] {
				if grid[y][x] == ' ' {
					floor = append(floor, [2]int{x, y})
				}
			}
		}
		if len(floor) < 8 {
			continue
		}
		// connected component of a random floor cell
		p := floor[rng.Intn(len(floor))]
		seen := map[[2]int]bool{p: true}
		q := [][2]int{p}
		for i := 0; i < len(q); i++ {
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nb := [2]int{q[i][0] + d[0], q[i][1] + d[1]}
				if grid[nb[1]][nb[0]] == ' ' && !seen[nb] {
					seen[nb] = true
					q = append(q, nb)
				}
			}
		}
		nb := 1 + rng.Intn(4)
		if rng.Intn(4) == 0 {
			nb = 5
		}
		if len(q) < nb+4 {
			continue
		}
		rng.Shuffle(len(q), func(i, j int) { q[i], q[j] = q[j], q[i] })
		boxes := q[:nb]
		player := q[nb]
		goals := slices.Clone(q[1:])
		rng.Shuffle(len(goals), func(i, j int) { goals[i], goals[j] = goals[j], goals[i] })
		// goals: random cells of the component (may coincide with boxes or the player)
		gs := map[[2]int]bool{}
		for _, c := range append(slices.Clone(q), goals...) {
			if len(gs) == nb {
				break
			}
			if rng.Intn(3) == 0 {
				gs[c] = true
			}
		}
		for len(gs) < nb {
			gs[q[rng.Intn(len(q))]] = true
		}
		bs := map[[2]int]bool{}
		for _, b := range boxes {
			bs[b] = true
		}
		if rng.Intn(2) == 0 {
			// solvable start: random pulls from the solved position
			bs = map[[2]int]bool{}
			for c := range gs {
				bs[c] = true
			}
			var free [][2]int
			for _, c := range q {
				if !bs[c] {
					free = append(free, c)
				}
			}
			if len(free) == 0 {
				continue
			}
			pl := free[rng.Intn(len(free))]
			isFree := func(c [2]int) bool { return grid[c[1]][c[0]] != '#' && !bs[c] }
			for step := 0; step < 200; step++ {
				d := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}[rng.Intn(4)]
				nx := [2]int{pl[0] + d[0], pl[1] + d[1]}
				if !isFree(nx) {
					continue
				}
				back := [2]int{pl[0] - d[0], pl[1] - d[1]}
				if bs[back] && rng.Intn(3) > 0 { // pull the box behind
					delete(bs, back)
					bs[pl] = true
				}
				pl = nx
			}
			player = pl
		}
		for c := range seen {
			ch := byte(' ')
			switch {
			case bs[c] && gs[c]:
				ch = '*'
			case bs[c]:
				ch = '$'
			case c == player && gs[c]:
				ch = '+'
			case c == player:
				ch = '@'
			case gs[c]:
				ch = '.'
			}
			grid[c[1]][c[0]] = ch
		}
		rows := make([]string, len(grid))
		for y := range grid {
			rows[y] = string(grid[y])
		}
		return rows
	}
}

type auditFail struct {
	checker string
	rows    []string
	boxes   []int32
	player  int32
	t       int32
	note    string
}

func (f auditFail) String() string {
	return fmt.Sprintf("%s: boxes %v player %d t %d %s\n%s", f.checker, f.boxes, f.player, f.t, f.note, strings.Join(f.rows, "\n"))
}

// auditBoard runs every checker against the exact answers on one board.
func auditBoard(t *testing.T, rows []string, g *bfGraph, report func(auditFail)) map[string]int {
	lv := g.lv
	cnt := map[string]int{}
	// Production shares corral and cluster verdicts between searchers of a
	// level: give each board fresh shared tables so that path is exercised.
	deadzone.Shared.Store(deadzone.NewTable(14))
	cluster.Shared.Store(cluster.NewTable(14))
	defer deadzone.Shared.Store(nil)
	defer cluster.Shared.Store(nil)
	s := newSolver(lv)
	s.pairOffsets()
	s.dpair = make([]int8, len(lv.Wall)*len(s.poff))
	dzBig := deadzone.NewChecker(deadzone.NewBoard(lv.Wall, lv.Goal, deadOf(s.dist), lv.W), 20000)
	ps := newPullSolver(lv, time.Time{})
	pc := newLevelPullChecker(lv, 20000)
	n := len(lv.Wall)
	fail := func(name string, i int32, tt int32, note string) {
		cnt["FAIL "+name]++
		report(auditFail{name, rows, g.boxes[i], g.player[i], tt, note})
	}
	for i := range int32(len(g.boxes)) {
		cur := g.boxes[i]
		solv := g.solvable[i]
		clear(s.occ)
		for _, b := range cur {
			s.occ[b] = true
		}
		// --- whole-state checks on X
		if solv && !s.feasible(cur) {
			fail("feasible", i, -1, "")
		}
		for _, b := range cur {
			if solv && s.dist[b] < 0 {
				fail("dead-square", i, b, "")
			}
		}
		if solv && s.dz.Dead(s.occ, cur, g.player[i]) {
			fail("deadzone.Dead(cap200)", i, -1, "")
		}
		if solv && dzBig.Dead(s.occ, cur, g.player[i]) {
			fail("deadzone.Dead(cap20000)", i, -1, "")
		}
		if solv && s.sd != nil {
			for _, c := range cur {
				if !lv.Goal[c] && s.sd.Dead(c, g.player[i]) {
					fail("sidedead(add)", i, c, "")
				}
			}
		}
		// --- corral restrictions on X: some allowed box must lead to a solvable child
		if solv && len(g.children[i]) > 0 {
			solved := true
			for _, b := range cur {
				solved = solved && lv.Goal[b]
			}
			if !solved {
				for _, kind := range []string{"frontier", "pi"} {
					clear(s.occ)
					for _, b := range cur {
						s.occ[b] = true
					}
					s.reach(g.player[i], s.mark)
					st := s.stamp
					var corral uint32
					if kind == "frontier" {
						corral = s.firstPushCorral(g.player[i], st)
					} else {
						corral = s.piCorral(st)
					}
					if corral == 0 {
						continue
					}
					cnt["corral-"+kind]++
					okc := false
					for _, b := range cur {
						if s.cmark[b] != corral {
							continue
						}
						s.boxSearch(b, st, func(k, to int32) bool {
							ch := slices.Clone(cur)
							ch[slices.Index(cur, b)] = to
							slices.Sort(ch)
							if id, ok := g.keys[bfKey(ch, s.candNP)]; ok && g.solvable[id] {
								okc = true
								return false
							}
							return true
						})
						if okc {
							break
						}
					}
					if !okc {
						fail("corral-"+kind, i, -1, "")
					}
				}
			}
		}
		// --- push children of X
		clear(s.occ)
		for _, b := range cur {
			s.occ[b] = true
		}
		regMark := make([]bool, n)
		_, _ = bfRegion(lv, s.occ, g.player[i], regMark)
		for j, b := range cur {
			for di, d := range s.dirs {
				tt := b + d
				if !regMark[b-d] || lv.Wall[tt] || s.occ[tt] || s.dist[tt] < 0 {
					continue
				}
				ch := slices.Clone(cur)
				ch[j] = tt
				slices.Sort(ch)
				s.occ[b], s.occ[tt] = false, true
				// child region into mark3 / pstamp, as boxSearch leaves it
				s.pstamp++
				ps3 := s.pstamp
				q := []int32{b}
				s.mark3[b] = ps3
				minC := b
				for h := 0; h < len(q); h++ {
					for _, e := range s.dirs {
						if y := q[h] + e; s.mark3[y] != ps3 && !lv.Wall[y] && !s.occ[y] {
							s.mark3[y] = ps3
							q = append(q, y)
							minC = min(minC, y)
						}
					}
				}
				cid, ok := g.keys[bfKey(ch, minC)]
				if !ok {
					t.Fatalf("child not enumerated")
				}
				csolv := g.solvable[cid]
				canonical := int32(0)
				for side, e := range s.dirs {
					if s.mark3[tt-e] == ps3 {
						canonical = int32(side)
						break
					}
				}
				cnt["children"]++
				if r := s.pushDead(cur, j, tt, b*4+int32(di), canonical); r != alive {
					cnt[r.String()]++
					if csolv {
						fail("pushDead:"+r.String(), i, tt, fmt.Sprintf("child %v p %d", ch, minC))
					}
				}
				if s.pull {
					t.Fatal("pull")
				}
				// diagonal chains (default only in toward lanes): check it everywhere
				if s.dd != nil && s.dd.Dead(s.occ, tt, b) {
					cnt["diag"]++
					if csolv {
						fail("diagdead", i, tt, fmt.Sprintf("child %v", ch))
					}
				}
				if s.cl != nil && s.cl.Dead(s.occ, tt, b, func(y int32) bool { return s.mark3[y] == ps3 }) {
					cnt["cluster"]++
					if csolv {
						fail("cluster", i, tt, fmt.Sprintf("child %v", ch))
					}
				}
				if s.dz.DeadAfter(s.occ, ch, b, tt) {
					cnt["deadzone"]++
					if csolv {
						fail("deadzone.DeadAfter", i, tt, fmt.Sprintf("child %v", ch))
					}
				}
				if dzBig.DeadAfter(s.occ, ch, b, tt) && csolv {
					fail("deadzone.DeadAfter(cap20000)", i, tt, fmt.Sprintf("child %v", ch))
				}
				s.occ[b], s.occ[tt] = true, false
			}
		}
		// --- pull side: every forward-reachable state can be pulled back to the start
		clear(ps.occ)
		for _, b := range cur {
			ps.occ[b] = true
		}
		for _, b := range cur {
			if ps.dist[b] < 0 {
				fail("pull-dead-square", i, b, "")
			}
			if ps.pullFreezeDead(b) {
				fail("pullFreezeDead", i, b, "")
			}
		}
		if !ps.feasible(cur) {
			fail("pull-feasible", i, -1, "")
		}
		if pullDeadTarget(pc, lv, cur, g.player[i]) {
			fail("PullChecker.Dead", i, -1, "")
		}
		clear(ps.occ)
	}
	return cnt
}

func deadOf(dist []int32) []bool {
	d := make([]bool, len(dist))
	for c, v := range dist {
		d[c] = v < 0
	}
	return d
}

func TestDeadAudit586(t *testing.T) {
	N, seed := 300, int64(586)
	if testing.Short() {
		N = 60
	}
	if v := os.Getenv("SOKSOLV_AUDIT_N"); v != "" {
		N, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SOKSOLV_AUDIT_SEED"); v != "" {
		seed, _ = strconv.ParseInt(v, 10, 64)
	}
	if os.Getenv("SOKSOLV_AUDIT_REACH") == "1" {
		old := reach.Enabled
		reach.Enabled = true
		defer func() { reach.Enabled = old }()
	}
	rng := rand.New(rand.NewSource(seed))
	total := map[string]int{}
	fails := 0
	boards, solvableStarts, holesChecked := 0, 0, 0
	for boards < N {
		rows := randomBoard(rng)
		lv, err := parseLevel(rows)
		if err != nil {
			continue
		}
		g, ok := bruteForce(lv, 60000)
		if !ok {
			continue
		}
		boards++
		if g.solvable[0] {
			solvableStarts++
		}
		// holes: closing must keep a solvable start solvable
		if holes.Enabled {
			raw, err := parseLevelRaw(rows)
			if err == nil && !slices.Equal(raw.Wall, lv.Wall) {
				if gr, ok := bruteForce(raw, 60000); ok {
					holesChecked++
					if gr.solvable[0] && !g.solvable[0] {
						fails++
						t.Errorf("holes: closing made a solvable start unsolvable\n%s", strings.Join(rows, "\n"))
					}
				}
			}
		}
		cnt := auditBoard(t, rows, g, func(f auditFail) {
			fails++
			if fails <= 20 {
				t.Errorf("%v", f)
			}
		})
		for k, v := range cnt {
			total[k] += v
		}
		total["states"] += len(g.boxes)
		for _, v := range g.solvable {
			if v {
				total["solvable-states"]++
			}
		}
	}
	keys := make([]string, 0, len(total))
	for k := range total {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, " %s=%d", k, total[k])
	}
	t.Logf("boards=%d solvable-starts=%d holes-checked=%d fails=%d%s", boards, solvableStarts, holesChecked, fails, sb.String())
}
