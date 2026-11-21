// Package cluster proves small groups of boxes dead and shares what it
// learns between searchers.
//
// A cluster is the box just pushed plus the boxes touching it (8-neighbour
// adjacency, transitively, up to MaxBoxes). With every other box removed,
// an exhaustive search over the cluster's pushes, starting from the player's
// actual side, decides whether all cluster boxes can ever stand on goals at
// once. If not, any state containing the cluster with the player on that
// side is dead: the other boxes can only get in the way. The search gives
// up (alive) at a state cap, so a "dead" verdict is always a proof.
//
// Proven patterns are written to a lock-free table shared by every forward
// searcher of the process (Shared), so a pattern one searcher pays to prove
// is free for the others. Alive verdicts are cached per searcher.
package cluster

import (
	"slices"
	"sync/atomic"
)

// MaxBoxes bounds cluster size; StateCap bounds each proof search.
var (
	MaxBoxes = 5
	StateCap = 1000
)

// Board is the static level: walls, goals and the static dead squares.
type Board struct {
	Wall, Goal, Dead []bool
	Dist             []int32 // optional: push distance to the nearest goal (enables penalties)
	W                int
	dirs             [4]int32
	nFloor           int
	salt             uint64 // hash of walls, goals and dead squares (Salt on): keeps verdicts of different levels apart in the shared tables
}

// Salt (exp-688) folds a per-board hash of walls and goals into every table
// key, so verdicts proven on a sub-level (extra walls, fewer goals) never
// reach another level through the process-wide tables.
var Salt bool

// BoardSalt hashes the static board (0 when Salt is off).
func BoardSalt(wall, goal, dead []bool) uint64 {
	if !Salt {
		return 0
	}
	h := uint64(len(wall)) * 0x9E3779B97F4A7C15
	for i := range wall {
		var v uint64
		if wall[i] {
			v |= 1
		}
		if i < len(goal) && goal[i] {
			v |= 2
		}
		if i < len(dead) && dead[i] {
			v |= 4
		}
		h = (h ^ v) * 0xBF58476D1CE4E5B9
		h ^= h >> 29
	}
	return h
}

func (b *Board) floor() int {
	if b.nFloor == 0 {
		for _, w := range b.Wall {
			if !w {
				b.nFloor++
			}
		}
	}
	return b.nFloor
}

// NewBoard prepares a board; dead[c] marks squares no box can leave to a goal.
func NewBoard(wall, goal, dead []bool, w int) *Board {
	return &Board{Wall: wall, Goal: goal, Dead: dead, W: w, dirs: [4]int32{-int32(w), int32(w), -1, 1}, salt: BoardSalt(wall, goal, dead)}
}

// Table is the shared store of proven-dead patterns (64-bit keys of the
// cluster's cells plus the player's side cell), direct-mapped and lossy.
type Table struct {
	slots []atomic.Uint64
	Proven atomic.Int64 // patterns proven dead (diagnostics)
	Hits   atomic.Int64 // prunes answered from the table
}

// Shared is the process-wide table (nil: no sharing).
var Shared atomic.Pointer[Table]

// SharedPen is the process-wide penalty table: alive clusters whose true
// push cost (exhaustive search, other boxes removed) exceeds the sum of their
// boxes' goal distances. Slots hold key&^0xFF | penalty (penalty < 256).
var SharedPen atomic.Pointer[Table]

func (t *Table) penalty(k uint64) (int32, bool) {
	if t == nil {
		return 0, false
	}
	v := t.slots[k&uint64(len(t.slots)-1)].Load()
	if v&^0xFF != k&^0xFF {
		return 0, false
	}
	return int32(v & 0xFF), true
}

func (t *Table) addPenalty(k uint64, pen int32) {
	if t != nil {
		t.put(k&^0xFF|uint64(min(pen, 255)), k)
	}
}

// put stores slot value v for key k.
func (t *Table) put(v, k uint64) {
	slot := k & uint64(len(t.slots)-1)
	t.slots[slot].Store(v)
	t.Proven.Add(1)
}

// Penalty is the last Dead call's penalty for an alive cluster (0 if none).
func (c *Checker) Penalty() int32 { return c.pen }

// NewTable makes a table with 2^log slots.
func NewTable(log int) *Table { return &Table{slots: make([]atomic.Uint64, 1<<log)} }

func (t *Table) has(k uint64) bool {
	return t != nil && t.slots[k&uint64(len(t.slots)-1)].Load() == k
}
func (t *Table) add(k uint64) {
	if t != nil {
		t.put(k, k)
	}
}

// Checker is one searcher's view: scratch space and its alive cache.
type Checker struct {
	Private bool // do not exchange goal-dependent proofs with global tables
	b       *Board
	proven  []uint64 // alive patterns this searcher proved: direct-mapped, key&^0xFF | 0x80 | penalty (<128)
	occ     []bool
	mark    []uint32
	stamp   uint32
	queue   []int32
	Checked int // proof searches run (diagnostics)
	pen     int32
	seen    map[cstate]struct{} // proof search scratch, reused
	cq      []cstate
	cdepth  []int32
	moves   []cmove
}

// NewChecker returns a checker for one searcher.
func NewChecker(b *Board) *Checker {
	n := len(b.Wall)
	return &Checker{b: b, proven: make([]uint64, 1<<18), occ: make([]bool, n), mark: make([]uint32, n)}
}

func key(cells []int32, side int32) uint64 {
	h := uint64(side)*0x9E3779B97F4A7C15 + uint64(len(cells))
	for _, c := range cells {
		h = (h ^ uint64(c)) * 0xBF58476D1CE4E5B9
		h ^= h >> 31
	}
	return h | 1
}

// Dead reports whether the box just pushed to t, with the boxes touching it
// (occ marks every box of the state), is a dead cluster given that the player
// stands on side, a free cell next to t in the player's region.
func (c *Checker) Dead(occ []bool, t, side int32, inRegion func(int32) bool) bool {
	b := c.b
	c.pen = 0
	// Gather the cluster: 8-neighbour connected boxes from t.
	cl := []int32{t}
	w := int32(b.W)
	ring := [8]int32{-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1}
	for i := 0; i < len(cl) && len(cl) < MaxBoxes; i++ {
		for _, o := range ring {
			x := cl[i] + o
			if x >= 0 && int(x) < len(occ) && occ[x] && !slices.Contains(cl, x) && len(cl) < MaxBoxes {
				cl = append(cl, x)
			}
		}
	}
	if len(cl) < 3 {
		return false // pairs are handled by the pair checks
	}
	allOnGoals := true
	for _, x := range cl {
		if !b.Goal[x] {
			allOnGoals = false
			break
		}
	}
	if allOnGoals {
		return false
	}
	// Cheap filter: a cluster can only cut the player's region (and be dead
	// in a way the local freeze checks miss) if some free cell touching it
	// is outside the player's current region. Skipping is always safe.
	if inRegion != nil {
		cut := false
		for _, x := range cl {
			for _, d := range b.dirs {
				if y := x + d; !b.Wall[y] && !occ[y] && !inRegion(y) {
					cut = true
				}
			}
		}
		if !cut {
			return false
		}
	}
	slices.Sort(cl)
	k := key(cl, side) ^ b.salt&^0xFF
	var shared, penalties *Table
	if !c.Private {
		shared, penalties = Shared.Load(), SharedPen.Load()
	}
	if pen, ok := c.local(k); ok { // proven alive before, by this searcher
		c.pen = pen
		if pen == 0 && b.Dist != nil {
			if p, ok := penalties.penalty(k); ok {
				c.pen = p
			}
		}
		return false
	}
	if t := shared; t.has(k) {
		t.Hits.Add(1)
		return true
	}
	// Only a cluster that cuts the player's region can be dead in a way the
	// local freeze checks miss; one flood decides (cheap filter).
	for _, x := range cl {
		c.occ[x] = true
	}
	c.region(side)
	split := len(c.queue) < c.b.floor()-len(cl)
	for _, x := range cl {
		c.occ[x] = false
	}
	if !split {
		c.setLocal(k, 0)
		return false
	}
	dead, cost := c.search(cl, side)
	if dead {
		shared.add(k)
	} else {
		c.setLocal(k, 0)
		if b.Dist != nil && cost > 0 {
			est := int32(0)
			for _, x := range cl {
				est += b.Dist[x]
			}
			if p := cost - est; p > 0 {
				c.setLocal(k, p)
				c.pen = p
				penalties.addPenalty(k, p)
			}
		}
	}
	return dead
}

// region floods the player's region from p (per c.occ) and returns its
// minimum cell; c.mark holds stamp c.stamp for the region.
func (c *Checker) region(p int32) int32 {
	b := c.b
	c.stamp++
	st := c.stamp
	q := append(c.queue[:0], p)
	c.mark[p] = st
	m := p
	for i := 0; i < len(q); i++ {
		for _, d := range b.dirs {
			if y := q[i] + d; c.mark[y] != st && !b.Wall[y] && !c.occ[y] {
				c.mark[y] = st
				q = append(q, y)
				m = min(m, y)
			}
		}
	}
	c.queue = q
	return m
}

// search: exhaustive BFS over the cluster's positions (other boxes removed)
// and the player's region. Dead if no state with every cluster box on a goal
// is reachable before the search space is exhausted; alive at the cap.
// search also returns, when alive, the cluster's minimum push count (BFS
// depth of the first all-on-goals state), or 0 when the cap was hit.
// cstate is one proof-search state: up to 8 sorted box cells and the
// normalized player, as a fixed-size array so it can key a map without
// allocating.
type cstate [9]int16

func mkState(bx []int32, p int32) cstate {
	var k cstate
	for i := range k {
		k[i] = -1
	}
	for i, x := range bx {
		k[i] = int16(x)
	}
	k[8] = int16(p)
	return k
}

func (c *Checker) search(start []int32, side int32) (bool, int32) {
	return c.searchCap(start, side, StateCap)
}

// searchCap is search with an explicit state cap.
func (c *Checker) searchCap(start []int32, side int32, capStates int) (bool, int32) {
	b := c.b
	c.Checked++
	if len(start) > 8 {
		return false, 0 // cstate holds 8 boxes; MaxBoxes is at most 5 in practice
	}
	n := len(start)
	set := func(bx []int32, v bool) {
		for _, x := range bx {
			c.occ[x] = v
		}
	}
	set(start, true)
	p0 := c.region(side)
	set(start, false)
	if c.seen == nil {
		c.seen = map[cstate]struct{}{}
	}
	clear(c.seen)
	c.seen[mkState(start, p0)] = struct{}{}
	c.cq = append(c.cq[:0], mkState(start, p0))
	c.cdepth = append(c.cdepth[:0], 0)
	cur := make([]int32, n)
	nb := make([]int32, n)
	for head := 0; head < len(c.cq); head++ {
		if len(c.seen) > capStates {
			return false, 0
		}
		st, depth := c.cq[head], c.cdepth[head]
		for i := range n {
			cur[i] = int32(st[i])
		}
		stp := int32(st[8])
		set(cur, true)
		c.region(stp)
		rs := c.stamp
		c.moves = c.moves[:0]
		for i, x := range cur {
			for _, d := range b.dirs {
				to := x + d
				if c.mark[x-d] == rs && !b.Wall[to] && !c.occ[to] && !b.Dead[to] {
					c.moves = append(c.moves, cmove{i, to, x, c.fastPlayer(x, to, stp, rs)})
				}
			}
		}
		set(cur, false)
		for _, m := range c.moves {
			copy(nb, cur)
			nb[m.i] = m.to
			slices.Sort(nb)
			done := true
			for _, x := range nb {
				if !b.Goal[x] {
					done = false
					break
				}
			}
			if done {
				return false, depth + 1
			}
			np := m.np
			if np < 0 || CheckFast {
				set(nb, true)
				slow := c.region(m.from)
				set(nb, false)
				if CheckFast && np >= 0 && np != slow {
					panic("cluster: fast player normalization disagrees with flood")
				}
				np = slow
			}
			k := mkState(nb, np)
			if _, ok := c.seen[k]; !ok {
				c.seen[k] = struct{}{}
				c.cq = append(c.cq, k)
				c.cdepth = append(c.cdepth, depth+1)
			}
		}
	}
	return true, 0
}

type cmove struct {
	i        int
	to, from int32
	np       int32 // child's normalized player, -1: flood needed
}

// CheckFast (tests): verify every fast normalization against a flood.
var CheckFast = false

// fastPlayer returns the child's normalized player after a box moves from x
// to t with the player ending on x (c.occ holds the parent's boxes; the
// parent's region R is stamped rs, minimum minR), or -1 if a flood is
// needed. If x opens no new area (its free neighbors other than t are in R),
// t isn't R's minimum, and blocking t can't split R (t's free orthogonal
// neighbors lie in one run of free cells around its 8-cell ring), the child
// region is R - t + x with minimum min(minR, x).
func (c *Checker) fastPlayer(x, t, minR int32, rs uint32) int32 {
	b := c.b
	for _, d := range b.dirs {
		if y := x + d; y != t && !b.Wall[y] && !c.occ[y] && c.mark[y] != rs {
			return -1
		}
	}
	if t == minR {
		return -1
	}
	w := int32(b.W)
	ring := [8]int32{-w - 1, -w, -w + 1, 1, w + 1, w, w - 1, -1} // circular; odd indices are orthogonal
	var free [8]bool
	start := -1
	for k, o := range ring {
		y := t + o
		free[k] = !b.Wall[y] && (y == x || !c.occ[y])
		if !free[k] && start < 0 {
			start = k
		}
	}
	if start < 0 {
		return min(minR, x) // whole ring free: one run
	}
	runsWithOrth := 0
	inRun, hasOrth := false, false
	for i := 1; i <= 8; i++ {
		k := (start + i) % 8
		if free[k] {
			if !inRun {
				inRun, hasOrth = true, false
			}
			if k%2 == 1 {
				hasOrth = true
			}
		} else if inRun {
			if hasOrth {
				runsWithOrth++
			}
			inRun = false
		}
	}
	if inRun && hasOrth {
		runsWithOrth++
	}
	if runsWithOrth > 1 {
		return -1
	}
	return min(minR, x)
}

// local looks up an alive verdict this searcher proved: one slot, exact
// match on the key's upper 56 bits (lossy: a newer pattern may evict it).
func (c *Checker) local(k uint64) (int32, bool) {
	v := c.proven[k&uint64(len(c.proven)-1)]
	if v&0x80 == 0 || v&^0xFF != k&^0xFF {
		return 0, false
	}
	return int32(v & 0x7F), true
}

func (c *Checker) setLocal(k uint64, pen int32) {
	c.proven[k&uint64(len(c.proven)-1)] = k&^0xFF | 0x80 | uint64(min(pen, 127))
}

// SubsetDead reports whether boxes (at most 8, sorted or not), with every
// other box removed, can never all stand on goals at once when the player
// starts at player: an exhaustive proof capped at capStates states (alive at
// the cap). When dead, region is the player's region on that subset board
// (one bit per cell): the proof covers any position containing these boxes
// with the player anywhere in region.
func (c *Checker) SubsetDead(boxes []int32, player int32, capStates int) (dead bool, region []uint64) {
	if len(boxes) == 0 || len(boxes) > 8 {
		return false, nil
	}
	b := c.b
	allGoal := true
	for _, x := range boxes {
		if !b.Goal[x] {
			allGoal = false
		}
	}
	if allGoal {
		return false, nil
	}
	sorted := slices.Clone(boxes)
	slices.Sort(sorted)
	dead, _ = c.searchCap(sorted, player, capStates)
	if !dead {
		return false, nil
	}
	for _, x := range sorted {
		c.occ[x] = true
	}
	c.region(player)
	for _, x := range sorted {
		c.occ[x] = false
	}
	region = make([]uint64, (len(b.Wall)+63)/64)
	for _, y := range c.queue {
		region[y>>6] |= 1 << (y & 63)
	}
	return true, region
}
