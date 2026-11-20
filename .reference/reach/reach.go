// Package reach computes single-box push distances that respect the player's
// access (exp-464, -exact-reach).
//
// The solver's relaxed tables let a box at b move in direction d whenever b-d
// and b+d are not walls. That ignores WHERE the player can stand: with the box
// on b, the floor splits into up to four side components (the pieces of the
// walls-only board minus b that touch b's neighbours), and a push needs the
// player in the component of b-d. On Sasquatch VII #49 a tip box sits above a
// dead-end cell: the sideways push needs the player in that cell, but the box
// is standing on its only entrance. The relaxed table says the box reaches 449
// cells; with access it reaches 7.
//
// A state is (cell x, side label l): the box on x, the player somewhere in the
// side component with canonical label l. A push in direction d from (x, l)
// needs l to be the label of x-d's component, and x+d floor; the result is
// (x+d, label of x's component as seen from x+d). Real states project onto
// states of this graph (other boxes only shrink the player's region), so
// distances over it are a sound lower bound, and tight for one box.
//
// Which states can occur: those reachable from the start states, because a
// box's player side after its first push is always the cell it left, and
// before its first push it is the component of the player's start cell
// (other boxes never let the player cross this box's cell). Restricting to
// reachable states is what makes the #49 tips come out confined.
package reach

import "os"

// Enabled turns the access-aware tables on (the -exact-reach flag;
// SOKSOLV_EXACT_REACH=1 does the same for whole test binaries).
var Enabled = os.Getenv("SOKSOLV_EXACT_REACH") == "1"

// Graph is the side-component structure of a board plus the state set
// reachable from the start boxes.
type Graph struct {
	w     int
	dirs  [4]int32
	wall  []bool
	lab   []int8  // lab[x*4+i]: canonical label of x+dirs[i]'s component in the board minus x; -1 if that neighbour is wall (or x is)
	reach []uint8 // reach[x]: bit l set = state (x, l) is reachable from a start state; bit 4 = a start box that no push can ever move
	// scratch
	seen  []uint32
	stamp uint32
	queue []int32
	sd    []int32 // Backward's state-distance scratch
	sq    []int32
}

// New builds the graph for a padded board (outer ring wall) with the given
// start boxes and player.
func New(wall []bool, w int, boxes []int32, player int32) *Graph {
	n := len(wall)
	g := &Graph{
		w:     w,
		dirs:  [4]int32{-int32(w), int32(w), -1, 1},
		wall:  wall,
		lab:   make([]int8, 4*n),
		reach: make([]uint8, n),
		seen:  make([]uint32, n),
	}
	for i := range g.lab {
		g.lab[i] = -1
	}
	for x := range n {
		if !wall[x] {
			g.label(int32(x))
		}
	}
	g.forward(boxes, player)
	return g
}

// label fills lab for cell x: flood from each unlabelled floor neighbour in
// order; the label is the index of the neighbour the flood started from, which
// is the smallest index of its component.
func (g *Graph) label(x int32) {
	var nbr [4]int32
	want := 0
	for i, d := range g.dirs {
		nbr[i] = x + d
		if !g.wall[nbr[i]] {
			want++
		}
	}
	if want == 0 {
		return
	}
	g.stamp++
	got := 0
	base := int(x) * 4
	for i := range 4 {
		if g.wall[nbr[i]] || g.lab[base+i] >= 0 {
			continue
		}
		g.lab[base+i] = int8(i)
		got++
		if got == want {
			break
		}
		q := append(g.queue[:0], nbr[i])
		g.seen[nbr[i]] = g.stamp
	flood:
		for h := 0; h < len(q); h++ {
			c := q[h]
			for _, d := range g.dirs {
				nb := c + d
				if g.wall[nb] || nb == x || g.seen[nb] == g.stamp {
					continue
				}
				g.seen[nb] = g.stamp
				q = append(q, nb)
				for j := i + 1; j < 4; j++ {
					if nb == nbr[j] && g.lab[base+j] < 0 {
						g.lab[base+j] = int8(i)
						got++
						if got == want {
							break flood
						}
					}
				}
			}
		}
		g.queue = q
	}
}

// push returns the successor of state (x, l) pushing in direction index i, or
// ok=false if the push is not available.
func (g *Graph) push(x int32, l int8, i int) (y int32, yl int8, ok bool) {
	d := g.dirs[i]
	if g.lab[int(x)*4+(i^1)] != l || l < 0 { // player not on the x-d side
		return 0, 0, false
	}
	y = x + d
	if g.wall[y] {
		return 0, 0, false
	}
	return y, g.lab[int(y)*4+(i^1)], true
}

// startStates returns the start state (x*4+label) of each start box that can
// ever be pushed: the label of the component the player's start cell lies in
// once the box's cell is blocked. Boxes the player can never touch (no
// neighbour of theirs in his component) are returned in immobile.
func (g *Graph) startStates(boxes []int32, player int32) (states, immobile []int32) {
	for _, b := range boxes {
		g.stamp++
		pq := append(g.queue[:0], player)
		g.seen[player] = g.stamp
		for h := 0; h < len(pq); h++ {
			c := pq[h]
			for _, d := range g.dirs {
				nb := c + d
				if g.wall[nb] || nb == b || g.seen[nb] == g.stamp {
					continue
				}
				g.seen[nb] = g.stamp
				pq = append(pq, nb)
			}
		}
		g.queue = pq
		l := int8(-1)
		if player != b {
			for i, d := range g.dirs {
				if nb := b + d; !g.wall[nb] && g.seen[nb] == g.stamp {
					l = g.lab[int(b)*4+i]
					break
				}
			}
		}
		if l < 0 {
			immobile = append(immobile, b)
		} else {
			states = append(states, b*4+int32(l))
		}
	}
	return
}

// forward marks every state reachable from the start boxes with the player
// at player.
func (g *Graph) forward(boxes []int32, player int32) {
	states, immobile := g.startStates(boxes, player)
	for _, b := range immobile {
		g.reach[b] |= 1 << 4
	}
	q := make([]int32, 0, 64)
	mark := func(x int32, l int8) {
		if g.reach[x]&(1<<uint(l)) == 0 {
			g.reach[x] |= 1 << uint(l)
			q = append(q, x*4+int32(l))
		}
	}
	for _, s := range states {
		mark(s/4, int8(s%4))
	}
	for h := 0; h < len(q); h++ {
		x, l := q[h]/4, int8(q[h]%4)
		for i := range 4 {
			if y, yl, ok := g.push(x, l, i); ok {
				mark(y, yl)
			}
		}
	}
}

// Cells returns, per cell, whether some reachable state has the box there.
func (g *Graph) Cells() []bool {
	out := make([]bool, len(g.wall))
	for c, m := range g.reach {
		out[c] = m != 0
	}
	return out
}

// Backward returns, per cell, the fewest pushes that bring a box standing there
// (in any reachable state) onto one of the target cells, or -1 if none can.
// out must have len(wall) entries; it is overwritten.
func (g *Graph) Backward(targets []int32, out []int32) {
	n := len(g.wall)
	dist := g.stateDist(targets, true)
	for c := range n {
		out[c] = -1
		for l := range 4 {
			if d := dist[c*4+l]; d >= 0 && (out[c] < 0 || d < out[c]) {
				out[c] = d
			}
		}
	}
	// Immobile start boxes: distance 0 on a target, otherwise none.
	for _, t := range targets {
		if g.reach[t]&(1<<4) != 0 {
			out[t] = 0
		}
	}
}

// stateDist is the backward BFS over reachable states (index x*4+label).
func (g *Graph) stateDist(targets []int32, scratch bool) []int32 {
	n := len(g.wall)
	var dist []int32
	if scratch { // Backward consumes the result at once, so one buffer serves every call
		if g.sd == nil {
			g.sd = make([]int32, 4*n)
		}
		dist = g.sd
	} else {
		dist = make([]int32, 4*n)
	}
	for i := range dist {
		dist[i] = -1
	}
	q := g.sq[:0]
	for _, t := range targets {
		for l := range 4 {
			if g.reach[t]&(1<<uint(l)) != 0 && dist[int(t)*4+l] < 0 {
				dist[int(t)*4+l] = 0
				q = append(q, t*4+int32(l))
			}
		}
	}
	for h := 0; h < len(q); h++ {
		y, yl := q[h]/4, int8(q[h]%4)
		for i := range 4 {
			// predecessor: box on x = y-d pushed d. The player was on the x-d side of x.
			if g.lab[int(y)*4+(i^1)] != yl {
				continue
			}
			x := y - g.dirs[i]
			if g.wall[x] {
				continue
			}
			xl := g.lab[int(x)*4+(i^1)]
			if xl < 0 || g.reach[x]&(1<<uint(xl)) == 0 || dist[int(x)*4+int(xl)] >= 0 {
				continue
			}
			dist[int(x)*4+int(xl)] = dist[int(y)*4+int(yl)] + 1
			q = append(q, x*4+int32(xl))
		}
	}
	g.sq = q
	return dist
}

// Forward returns, per cell, the fewest pushes from any start box's start
// state to a state with the box there, or -1 if it cannot get there.
func (g *Graph) Forward(boxes []int32, player int32, out []int32) {
	n := len(g.wall)
	dist := make([]int32, 4*n)
	for i := range dist {
		dist[i] = -1
	}
	starts, _ := g.startStates(boxes, player)
	q := make([]int32, 0, 64)
	for _, s := range starts {
		dist[s] = 0
		q = append(q, s)
	}
	for h := 0; h < len(q); h++ {
		x, l := q[h]/4, int8(q[h]%4)
		for i := range 4 {
			if y, yl, ok := g.push(x, l, i); ok && dist[int(y)*4+int(yl)] < 0 {
				dist[int(y)*4+int(yl)] = dist[q[h]] + 1
				q = append(q, y*4+int32(yl))
			}
		}
	}
	for c := range n {
		out[c] = -1
		for l := range 4 {
			if d := dist[c*4+l]; d >= 0 && (out[c] < 0 || d < out[c]) {
				out[c] = d
			}
		}
	}
	for _, b := range boxes {
		if out[b] < 0 {
			out[b] = 0
		}
	}
}
