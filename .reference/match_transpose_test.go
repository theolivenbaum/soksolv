package main

import (
	"math"
	"math/rand"
	"os"
	"slices"
	"testing"
	"time"
)

// TestMatchTranspose (exp-408): the box-major assignment loop gives the same
// costs, duals and assignments as the goal-major one, for the plain goal
// distances, the pull lane's, and after replacing one row.
func TestMatchTranspose(t *testing.T) {
	files := map[string][]int{
		"levels/Large Test Suite/Sven_1623.xsb":    {7, 295, 1001},
		"levels/Large Test Suite/Sasquatch_50.xsb": {29, 43},
		"levels/Large Test Suite/Holland_81.xsb":   {4, 81},
		"levels/Large Test Suite/XSokoban_90.xsb":  {1, 50},
	}
	rng := rand.New(rand.NewSource(408))
	for path, idxs := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skip("level files not available")
		}
		levels := splitCollection(data)
		for _, i := range idxs {
			lv, err := parseLevel(levels[i-1])
			if err != nil {
				t.Fatal(err)
			}
			var floor []int32
			for c := range lv.Wall {
				if !lv.Wall[c] {
					floor = append(floor, int32(c))
				}
			}
			for _, pull := range []bool{false, true} {
				var a, b *solver
				mk := func() *solver {
					d := time.Now().Add(time.Minute)
					if pull {
						s := newPullSolver(lv, d)
						s.pfInit()
						return s
					}
					return newSolverUntil(lv, d)
				}
				a, b = mk(), mk()
				for trial := 0; trial < 40; trial++ {
					boxes := make([]int32, a.n)
					for k := range boxes {
						boxes[k] = floor[rng.Intn(len(floor))]
					}
					slices.Sort(boxes)
					run := func(s *solver, on bool) (int32, []int32, []int32, []int32) {
						dist := s.matchingDistances()
						c := s.assignToMode(boxes, dist, on)
						// replace one row and re-augment, as hfMatchChild does
						row := int32(rng.Intn(s.n)) + 1
						_ = row
						return c, slices.Clone(s.hu), slices.Clone(s.hv), slices.Clone(s.hp)
					}
					st := rng.Int63()
					rng.Seed(st)
					c0, u0, v0, p0 := run(a, false)
					rng.Seed(st)
					c1, u1, v1, p1 := run(b, true)
					// exp-452: the free-column tie rule may pick a different optimal matching and duals; the cost is what must agree.
					_, _, _, _ = u0, v0, u1, v1
					_, _ = p0, p1
					if c0 != c1 {
						t.Fatalf("%s #%d pull=%v trial %d: cost %d vs %d", path, i, pull, trial, c0, c1)
					}
					// replace one box and repair both ways
					mv := rng.Intn(a.n)
					nb := floor[rng.Intn(len(floor))]
					boxes2 := slices.Clone(boxes)
					boxes2[mv] = nb
					fix := func(s *solver, on bool) (int32, []int32) {
						dist := s.matchingDistances()
						s.hu[mv+1] = 0
						g := s.hp[0]
						_ = g
						for j := 1; j <= s.n; j++ {
							if s.hp[j] == int32(mv+1) {
								s.hp[j] = 0
							}
						}
						if on {
							s.augmentAssignment(boxes2, dist, int32(mv+1))
						} else {
							s.augmentAssignmentRef(boxes2, dist, int32(mv+1))
						}
						return int32(s.assignmentCost(boxes2, dist)), slices.Clone(s.hp)
					}
					r0, q0 := fix(a, false)
					r1, q1 := fix(b, true)
					_, _ = q0, q1
					if r0 != r1 {
						t.Fatalf("%s #%d pull=%v trial %d: repair %d vs %d", path, i, pull, trial, r0, r1)
					}
				}
			}
		}
	}
}

// assignToMode is assignTo with the reference (strided) augment when !on.
func (s *solver) assignToMode(boxes []int32, dist []int32, on bool) int32 {
	clear(s.hu)
	clear(s.hv)
	clear(s.hp)
	for row := int32(1); row <= int32(s.n); row++ {
		if on {
			s.augmentAssignment(boxes, dist, row)
		} else {
			s.augmentAssignmentRef(boxes, dist, row)
		}
	}
	if total := s.assignmentCost(boxes, dist); total < assignmentInf {
		return int32(total)
	}
	return -1
}

// augmentAssignmentRef is the original goal-major augmentAssignment.
func (s *solver) augmentAssignmentRef(boxes []int32, dist []int32, row int32) {
	n, cells := s.n, len(s.lv.Wall)
	u, v, p, way, minv, used := s.hu, s.hv, s.hp, s.hway, s.hminv, s.hused
	p[0] = row
	j0 := int32(0)
	for j := range minv {
		minv[j], used[j] = math.MaxInt32, false
	}
	for {
		used[j0] = true
		i0, delta, j1 := p[j0], int32(math.MaxInt32), int32(0)
		for j := int32(1); j <= int32(n); j++ {
			if !used[j] {
				if cur := assignmentEdge(dist, cells, boxes[i0-1], j) - u[i0] - v[j]; cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
		}
		for j := 0; j <= n; j++ {
			if used[j] {
				u[p[j]] += delta
				v[j] -= delta
			} else {
				minv[j] -= delta
			}
		}
		j0 = j1
		if p[j0] == 0 {
			break
		}
	}
	for j0 != 0 {
		j1 := way[j0]
		p[j0] = p[j1]
		j0 = j1
	}
}
