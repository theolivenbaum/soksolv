package reverse

import (
	"os"
	"testing"
	"time"

	"soksolv/verify"
)

// refReach is the flood-per-state reference for pullReach (exp-706 probe):
// start squares a box on b can be pulled onto, obstacles obst (b excluded),
// the player starting on p.
func refReach(s *solver, obst []bool, b, p int32) set256 {
	cells := len(obst)
	flood := func(from, box int32) []bool {
		m := make([]bool, cells)
		if obst[from] || from == box {
			return m
		}
		q := []int32{from}
		m[from] = true
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; y != box && !obst[y] && !m[y] {
					m[y] = true
					q = append(q, y)
				}
			}
		}
		return m
	}
	var r set256
	type st struct{ y, pl int32 }
	seen := map[st]bool{}
	q := []st{}
	reg := flood(p, b)
	for _, d := range s.dirs {
		if w := b + d; !obst[w] && reg[w] && !obst[b+2*d] && s.dist[w] >= 0 {
			x := st{w, w + d}
			if !seen[x] {
				seen[x] = true
				q = append(q, x)
			}
		}
	}
	for i := 0; i < len(q); i++ {
		y := q[i].y
		if j := s.startIdx[y]; j >= 0 {
			r.set(j)
		}
		reg := flood(q[i].pl, y)
		for _, d := range s.dirs {
			if w, w2 := y+d, y+2*d; !obst[w] && reg[w] && !obst[w2] && s.dist[w] >= 0 {
				x := st{w, w2}
				if !seen[x] {
					seen[x] = true
					q = append(q, x)
				}
			}
		}
	}
	return r
}

// The DFS-lowpoint pull reach (BFS and SCC closure) must equal the flood reference on random
// placed sets and box cells.
func TestSupplyReachMatchesFloods(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/XSokoban_90.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := splitCollection(data)
	rng := uint64(777)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	compared := 0
	for _, idx := range []int{1, 6, 13, 29, 54, 89} {
		lv, err := parseLevel(levels[idx-1])
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		s.sup = newSupply(s, 1)
		var floor []int32
		for c := range lv.Wall {
			if !lv.Wall[c] && s.dist[c] >= 0 {
				floor = append(floor, int32(c))
			}
		}
		for trial := 0; trial < 300; trial++ {
			var f set256
			copy(s.sup.obst, lv.Wall)
			for j, c := range lv.Boxes {
				if next(3) == 0 {
					f.set(int32(j))
					s.sup.obst[c] = true
				}
			}
			p := floor[next(len(floor))]
			b := floor[next(len(floor))]
			if s.sup.obst[p] || s.sup.obst[b] || p == b || s.startIdx[b] >= 0 { // boxes in the lane's U are off-start
				continue
			}
			g := s.graphFor(f, p)
			var got set256
			if g.top[b] == g.top[p] && g.disc[b] != 0 {
				got = s.pullReach(g, b, int32(g.lab(s, b, p)), false)
			}
			want := refReach(s, s.sup.obst, b, p)
			if g.top[b] == g.top[p] && g.disc[b] != 0 {
				if c := s.closure(g, g.canon(s, b, g.lab(s, b, p))); c != want {
					t.Fatalf("level %d trial %d: closure %x want %x", idx, trial, c, want)
				}
			}
			if got != want {
				for y := 0; y < lv.H; y++ {
					line := []byte{}
					for x := 0; x < lv.W; x++ {
						c := int32(y*lv.W + x)
						ch := byte(' ')
						switch {
						case lv.Wall[c]:
							ch = '#'
						case c == b:
							ch = 'B'
						case c == p:
							ch = '@'
						case s.sup.obst[c]:
							ch = 'X'
						case s.startIdx[c] >= 0:
							ch = '.'
						}
						line = append(line, ch)
					}
					t.Log(string(line))
				}

				t.Fatalf("level %d trial %d: box %d player %d: got %x want %x", idx, trial, b, p, got, want)
			}
			compared++
		}
	}
	if compared < 100 {
		t.Fatalf("only %d comparisons", compared)
	}
}

// With -rev-supply the lane still solves Microban levels with valid solutions
// (placement macros keep the parent chain one pull per node).
func TestSupplyMicroban(t *testing.T) {
	data, err := os.ReadFile("../levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip(err)
	}
	levels := verify.SplitLevels(data)
	solved := 0
	for i, rows := range levels[:30] {
		r, err := SolveLane(rows, time.Now().Add(time.Second), 512<<20, 0, Lane{Free: true, Cells: true, Private: true, Supply: 4})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Solved {
			continue
		}
		if err := verify.Check(rows, r.Solution); err != nil {
			t.Fatalf("level %d: invalid solution: %v", i+1, err)
		}
		solved++
	}
	if solved < 22 {
		t.Errorf("only %d/30 solved", solved)
	}
}
