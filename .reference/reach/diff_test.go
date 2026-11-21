package reach

import (
	"math/rand"
	"slices"
	"testing"

	"soksolv/holes"
)

// A tiny brute-force Sokoban solver used as the oracle for both packages.

type board struct {
	w, h   int
	wall   []bool
	goal   []bool
	boxes  []int32
	player int32
}

func parse(rows []string) *board {
	h := len(rows) + 2
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	w += 2
	b := &board{w: w, h: h, wall: make([]bool, w*h), goal: make([]bool, w*h), player: -1}
	for i := range b.wall {
		b.wall[i] = true
	}
	for y, r := range rows {
		for x := 0; x < len(r); x++ {
			c := int32((y+1)*w + x + 1)
			b.wall[c] = r[x] == '#'
			switch r[x] {
			case '@':
				b.player = c
			case '+':
				b.player = c
				b.goal[c] = true
			case '$':
				b.boxes = append(b.boxes, c)
			case '*':
				b.boxes = append(b.boxes, c)
				b.goal[c] = true
			case '.':
				b.goal[c] = true
			}
		}
	}
	return b
}

func (b *board) clone() *board {
	c := *b
	c.wall = slices.Clone(b.wall)
	c.goal = slices.Clone(b.goal)
	c.boxes = slices.Clone(b.boxes)
	return &c
}

// solve returns the minimum number of pushes (-1 if unsolvable). visit, if
// non-nil, is called for every reached state with the boxes and player.
func (b *board) solve(visit func(boxes []int32, player int32)) int {
	dirs := [4]int32{-int32(b.w), int32(b.w), -1, 1}
	type key struct {
		boxes string
		p     int32
	}
	norm := func(boxes []int32, p int32) ([]int32, int32) {
		bs := slices.Clone(boxes)
		slices.Sort(bs)
		occ := map[int32]bool{}
		for _, x := range bs {
			occ[x] = true
		}
		// smallest reachable cell
		seen := map[int32]bool{p: true}
		q := []int32{p}
		best := p
		for len(q) > 0 {
			c := q[0]
			q = q[1:]
			best = min(best, c)
			for _, d := range dirs {
				n := c + d
				if !b.wall[n] && !occ[n] && !seen[n] {
					seen[n] = true
					q = append(q, n)
				}
			}
		}
		return bs, best
	}
	mk := func(bs []int32, p int32) key {
		buf := make([]byte, 0, 2*len(bs)+2)
		for _, x := range bs {
			buf = append(buf, byte(x), byte(x>>8))
		}
		return key{string(buf), p}
	}
	type node struct {
		boxes []int32
		p     int32
		d     int
	}
	solved := func(bs []int32) bool {
		for _, x := range bs {
			if !b.goal[x] {
				return false
			}
		}
		return true
	}
	bs, p := norm(b.boxes, b.player)
	seen := map[key]bool{mk(bs, p): true}
	queue := []node{{bs, p, 0}}
	for len(queue) > 0 {
		nd := queue[0]
		queue = queue[1:]
		if visit != nil {
			visit(nd.boxes, nd.p)
		}
		if solved(nd.boxes) {
			return nd.d
		}
		occ := map[int32]bool{}
		for _, x := range nd.boxes {
			occ[x] = true
		}
		// player region
		reg := map[int32]bool{nd.p: true}
		q := []int32{nd.p}
		for len(q) > 0 {
			c := q[0]
			q = q[1:]
			for _, d := range dirs {
				n := c + d
				if !b.wall[n] && !occ[n] && !reg[n] {
					reg[n] = true
					q = append(q, n)
				}
			}
		}
		for i, x := range nd.boxes {
			for _, d := range dirs {
				if !reg[x-d] {
					continue
				}
				t := x + d
				if b.wall[t] || occ[t] {
					continue
				}
				nb := slices.Clone(nd.boxes)
				nb[i] = t
				nbs, np := norm(nb, x)
				k := mk(nbs, np)
				if !seen[k] {
					seen[k] = true
					queue = append(queue, node{nbs, np, nd.d + 1})
				}
			}
		}
	}
	return -1
}

// randomBoard makes a small random board: boxes goals and player on floor.
func randomBoard(r *rand.Rand, nbox int) *board {
	w, h := 4+r.Intn(4), 4+r.Intn(3)
	for {
		b := &board{w: w + 2, h: h + 2, player: -1}
		b.wall = make([]bool, b.w*b.h)
		b.goal = make([]bool, b.w*b.h)
		var floor []int32
		for y := 0; y < b.h; y++ {
			for x := 0; x < b.w; x++ {
				c := int32(y*b.w + x)
				if x == 0 || y == 0 || x == b.w-1 || y == b.h-1 || r.Intn(100) < 28 {
					b.wall[c] = true
				} else {
					floor = append(floor, c)
				}
			}
		}
		if len(floor) < 2*nbox+2 {
			continue
		}
		r.Shuffle(len(floor), func(i, j int) { floor[i], floor[j] = floor[j], floor[i] })
		b.player = floor[0]
		b.boxes = slices.Clone(floor[1 : 1+nbox])
		gs := floor[1+nbox:]
		r.Shuffle(len(gs), func(i, j int) { gs[i], gs[j] = gs[j], gs[i] })
		// goals: avoid a box already on a goal half of the time
		for _, g := range gs[:nbox] {
			b.goal[g] = true
		}
		return b
	}
}

func TestClosingPreservesOptimum(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	closedAny, solvable := 0, 0
	for it := 0; it < 3000; it++ {
		b := randomBoard(r, 1+r.Intn(3))
		c := b.clone()
		n := holes.Close(c.wall, c.goal, c.w, c.boxes, c.player)
		if n > 0 {
			closedAny++
		}
		a, bb := b.solve(nil), c.solve(nil)
		if a != bb {
			t.Fatalf("iteration %d: optimum changed %d -> %d after closing %d cells\nboard %+v\nclosed %+v", it, a, bb, n, b, c)
		}
		if a >= 0 {
			solvable++
		}
	}
	t.Logf("%d boards closed something, %d solvable", closedAny, solvable)
	if closedAny < 200 || solvable < 200 {
		t.Fatalf("test not exercising enough: closed %d solvable %d", closedAny, solvable)
	}
}

// For every state the brute force reaches, every box's player side must be one
// of the graph's reachable states, and with one box the table is exact.
func TestReachInvariantAndBound(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	tight, looser := 0, 0
	for it := 0; it < 3000; it++ {
		nbox := 1 + r.Intn(3)
		b := randomBoard(r, nbox)
		g := New(b.wall, b.w, b.boxes, b.player)
		// invariant on visited states
		bad := ""
		opt := b.solve(func(boxes []int32, p int32) {
			for _, x := range boxes {
				l := int8(-1)
				// player's component in the board minus x
				seen := map[int32]bool{p: true}
				q := []int32{p}
				for len(q) > 0 {
					c := q[0]
					q = q[1:]
					for _, d := range g.dirs {
						n := c + d
						if !b.wall[n] && n != x && !seen[n] {
							seen[n] = true
							q = append(q, n)
						}
					}
				}
				for i, d := range g.dirs {
					if !b.wall[x+d] && seen[x+d] {
						l = g.lab[int(x)*4+i]
						break
					}
				}
				if l < 0 {
					if g.reach[x]&16 == 0 && bad == "" {
						bad = "immobile box without bit 4"
					}
				} else if g.reach[x]&(1<<uint(l)) == 0 && bad == "" {
					bad = "visited state not in reach set"
				}
			}
		})
		if bad != "" {
			t.Fatalf("iteration %d: %s\n%+v", it, bad, b)
		}
		// lower bound: min over assignments of summed per-goal exact distances
		var goals []int32
		for c, ok := range b.goal {
			if ok {
				goals = append(goals, int32(c))
			}
		}
		d := make([][]int32, len(goals))
		for gi, gc := range goals {
			d[gi] = make([]int32, len(b.wall))
			g.Backward([]int32{gc}, d[gi])
		}
		best := int32(-1)
		perm := make([]int, len(goals))
		for i := range perm {
			perm[i] = i
		}
		var rec func(k int, sum int32, used uint)
		rec = func(k int, sum int32, used uint) {
			if k == len(b.boxes) {
				if best < 0 || sum < best {
					best = sum
				}
				return
			}
			for gi := range goals {
				if used&(1<<uint(gi)) == 0 {
					if v := d[gi][b.boxes[k]]; v >= 0 {
						rec(k+1, sum+v, used|1<<uint(gi))
					}
				}
			}
		}
		rec(0, 0, 0)
		if opt >= 0 {
			if best < 0 || int(best) > opt {
				t.Fatalf("iteration %d: exact bound %d exceeds optimum %d\n%+v", it, best, opt, b)
			}
		}
		if nbox == 1 {
			// exact at the start state (the per-cell table takes the min over all reachable sides)
			starts, _ := g.startStates(b.boxes, b.player)
			ex := int32(-1)
			if len(starts) == 1 {
				for gi, gc := range goals {
					_ = gi
					sd := g.stateDist([]int32{gc}, false)
					if v := sd[starts[0]]; v >= 0 && (ex < 0 || v < ex) {
						ex = v
					}
				}
			} else if b.goal[b.boxes[0]] {
				ex = 0
			}
			if int(ex) != opt {
				t.Fatalf("iteration %d: single-box start-state distance %d != optimum %d\n%+v", it, ex, opt, b)
			}
		}
		// relaxed bound (no player sides) is never above the exact one
		rel := relaxedMatch(b, goals)
		if best >= 0 && rel > best {
			t.Fatalf("iteration %d: relaxed %d above exact %d", it, rel, best)
		}
		if best < 0 && rel >= 0 {
			tight++
		} else if best > rel {
			looser++
		}
	}
	t.Logf("exact made infinite %d boards, finite-tighter %d", tight, looser)
}

// relaxedMatch is the solver's old table: only the player's cell must not be
// a wall.
func relaxedMatch(b *board, goals []int32) int32 {
	dirs := [4]int32{-int32(b.w), int32(b.w), -1, 1}
	d := make([][]int32, len(goals))
	for gi, gc := range goals {
		d[gi] = make([]int32, len(b.wall))
		for i := range d[gi] {
			d[gi][i] = -1
		}
		d[gi][gc] = 0
		q := []int32{gc}
		for len(q) > 0 {
			c := q[0]
			q = q[1:]
			for _, dd := range dirs {
				x := c - dd
				if !b.wall[x] && !b.wall[x-dd] && d[gi][x] < 0 {
					d[gi][x] = d[gi][c] + 1
					q = append(q, x)
				}
			}
		}
	}
	best := int32(-1)
	var rec func(k int, sum int32, used uint)
	rec = func(k int, sum int32, used uint) {
		if k == len(b.boxes) {
			if best < 0 || sum < best {
				best = sum
			}
			return
		}
		for gi := range goals {
			if used&(1<<uint(gi)) == 0 {
				if v := d[gi][b.boxes[k]]; v >= 0 {
					rec(k+1, sum+v, used|1<<uint(gi))
				}
			}
		}
	}
	rec(0, 0, 0)
	return best
}

func TestHandMadeBoards(t *testing.T) {
	// 1. Dead-end pocket beside a corridor: the pocket cell (row 1, col 4) has
	// three walls and no goal, so it must close; solvability is unchanged.
	var pocket []string
	pocket = []string{
		"#######",
		"### ###",
		"#@$ . #",
		"#######",
	}
	b := parse(pocket)
	c := b.clone()
	if n := holes.Close(c.wall, c.goal, c.w, c.boxes, c.player); n != 2 {
		t.Fatalf("pocket: closed %d cells, want 2 (above the corridor and its right end)", n)
	}
	if b.solve(nil) != c.solve(nil) || b.solve(nil) != 2 {
		t.Fatalf("pocket: optimum %d vs %d, want 2", b.solve(nil), c.solve(nil))
	}
	// 2. The player starts in the dead end with the box right in front of it:
	// the only way to move the box is from inside the pocket, so it must stay.
	inside := []string{
		"#######",
		"#######",
		"##@$ .#",
		"#######",
	}
	// "@" has walls above, below and left: a 3-wall cell holding the player.
	b = parse(inside)
	c = b.clone()
	if n := holes.Close(c.wall, c.goal, c.w, c.boxes, c.player); n != 0 {
		t.Fatalf("player start in pocket: closed %d cells, want 0", n)
	}
	if b.solve(nil) != 2 {
		t.Fatalf("player start in pocket: optimum %d, want 2", b.solve(nil))
	}
	// 3. A dead-end corridor of length 2 closes completely (cascade) while the
	// goal cell at the end of another one stays.
	corr := []string{
		"#########",
		"#@$   . #",
		"##### ###",
		"##### ###",
		"#########",
	}
	b = parse(corr)
	c = b.clone()
	if n := holes.Close(c.wall, c.goal, c.w, c.boxes, c.player); n != 3 {
		t.Fatalf("corridor: closed %d cells, want 3 (two below, one at the right end)", n)
	}
	if b.solve(nil) != c.solve(nil) {
		t.Fatalf("corridor: optimum changed")
	}
	// 4. Exact reach: the box in a one-wide vertical corridor has dead-end cells
	// to its left and right; a sideways push would need the player in one, but
	// the box is standing on their only entrance.
	conf := []string{
		"#####",
		"##.##",
		"# $ #",
		"## ##",
		"##@##",
		"#####",
	}
	b = parse(conf)
	g := New(b.wall, b.w, b.boxes, b.player)
	d := make([]int32, len(b.wall))
	g.Backward([]int32{b.boxes[0] - int32(b.w)}, d)
	if d[b.boxes[0]] != 1 {
		t.Fatalf("straight push: dist %d, want 1", d[b.boxes[0]])
	}
	g.Backward([]int32{b.boxes[0] + 1}, d) // the dead-end cell to the right of the box
	if d[b.boxes[0]] >= 0 {
		t.Fatalf("sideways push into the pocket should be impossible, dist %d", d[b.boxes[0]])
	}
	cells := 0
	for _, ok := range g.Cells() {
		if ok {
			cells++
		}
	}
	if cells > 4 {
		t.Fatalf("confined box reaches %d cells, want <= 4", cells)
	}
}

func (b *board) String() string {
	s := "\n"
	for y := 0; y < b.h; y++ {
		for x := 0; x < b.w; x++ {
			c := int32(y*b.w + x)
			ch := byte(' ')
			switch {
			case b.wall[c]:
				ch = '#'
			case slices.Contains(b.boxes, c) && b.goal[c]:
				ch = '*'
			case slices.Contains(b.boxes, c):
				ch = '$'
			case c == b.player && b.goal[c]:
				ch = '+'
			case c == b.player:
				ch = '@'
			case b.goal[c]:
				ch = '.'
			}
			s += string(ch)
		}
		s += "\n"
	}
	return s
}
