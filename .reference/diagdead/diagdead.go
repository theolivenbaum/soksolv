// Package diagdead: diagonal-chain deadlocks, shared by the forward searches (exp-459).
//
// A chain is a run of k >= 1 empty cells I_0..I_{k-1} along a diagonal, each with all four
// orthogonal neighbours occupied (wall or box), the player not standing in it. Number the
// occupied "pair" cells P_0..P_k: P_m is the two cells I_m has up and on the chain's upper
// side, and P_k the two cells I_{k-1} has below and on the lower side (every cell of P_m, 0<m<k,
// is shared by I_{m-1} and I_m). A chain is capped at each end by an occupied diagonal cell A
// (before I_0) and B (after I_{k-1}), or by a pair of walls that closes that end.
//
//	A  U0
//	L0 I0 U1        (R chain; the L chain is its mirror image)
//	   L1 I1 U2
//	      L2 .. B
//
// While the chain stays closed nothing can leave its pair or cap cells, because moving such a
// box along any axis needs the player on a chain cell or on the cell the box moves toward. The
// only first changes are inward pushes: a box of P_m or P_{m+1} into I_m. This package decides
// whether every such change, and not changing the chain at all, ends in a dead position:
//
//   - A push into I_0 from P_1 (into I_{k-1} from P_{k-1}) fills the 2x2 block A,U_0,L_0,I_0
//     (I_{k-1},U_k,L_k,B): frozen, and dead if any box in it is off a goal.
//   - A push into I_m from P_m leaves the lower sub-chain I_{m+1}.. closed with the pushed box as
//     its cap (that box is itself immovable while P_{m+1} stands); the upper part is opened and
//     is ignored (sound: fewer prunes). A push from P_{m+1} is symmetric.
//   - Leaving a closed (sub-)chain closed is dead when one of its boxes (pairs, caps) is off a
//     goal: it can never move.
//
// A chain is dead when it has such an obligation and every possible inward push is dead. The
// outside is treated as maximally permissive (any pair box may be pushed inward whenever it
// exists), so a "dead" verdict holds in every board with that chain. Festival's
// is_diag_deadlock detects the same family with a top-down scan; this is an independent
// derivation and is, on the cases it shares, at least as strong.
package diagdead

// MaxChain bounds the chain length analysed; longer chains are skipped (sound, fewer prunes).
const MaxChain = 24

// Checker holds the static level data. Dead allocates nothing.
type Checker struct {
	wall, goal []bool
	w          int32
}

// New returns a checker for a padded level (outer ring wall) of width w.
func New(wall, goal []bool, w int) *Checker {
	return &Checker{wall: wall, goal: goal, w: int32(w)}
}

func (c *Checker) blocked(occ []bool, x int32) bool { return c.wall[x] || occ[x] }

// inner: x is empty, not the player's cell, and has four occupied neighbours.
func (c *Checker) inner(occ []bool, x, player int32) bool {
	if x == player || c.wall[x] || occ[x] {
		return false
	}
	w := c.w
	return c.blocked(occ, x-w) && c.blocked(occ, x+w) && c.blocked(occ, x-1) && c.blocked(occ, x+1)
}

// Dead reports whether occ (box cells; walls are in the level) holds a diagonal-chain deadlock
// through the 8-neighbourhood of the box just pushed to t, with the player at player. Chains
// elsewhere are not examined: a push cannot create a deadlock away from its destination.
func (c *Checker) Dead(occ []bool, t, player int32) bool {
	w := c.w
	var seen [16]int32
	ns := 0
	for _, off := range [8]int32{-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1} {
		n := t + off
		if !c.inner(occ, n, player) {
			continue
		}
		for _, delta := range [2]int32{1, -1} {
			dd := w + delta
			c0, steps := n, 0
			for c.inner(occ, c0-dd, player) {
				c0 -= dd
				if steps++; steps > MaxChain {
					break
				}
			}
			if steps > MaxChain {
				continue
			}
			key := c0*2 + (delta+1)/2
			dup := false
			for _, s := range seen[:ns] {
				if s == key {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			if ns < len(seen) {
				seen[ns] = key
				ns++
			}
			if c.chainDead(occ, c0, delta, player) {
				return true
			}
		}
	}
	return false
}

type chain struct {
	k        int
	has, bad [MaxChain + 1]bool // pair P_m holds a box / a box off its goal
	ng       [MaxChain]bool     // I_m is not a goal
	cA, cB   bool               // the cap is a box off its goal
	memo     [MaxChain][MaxChain]uint8
}

// chainDead analyses the chain whose first inner cell is c0, running down-right (delta 1) or
// down-left (delta -1).
func (c *Checker) chainDead(occ []bool, c0, delta, player int32) bool {
	w := c.w
	dd := w + delta
	a, b := -w, -delta // pair offsets of an inner cell: up, and left (R) / right (L)
	var q chain
	for q.k < MaxChain && c.inner(occ, c0+int32(q.k)*dd, player) {
		q.k++
	}
	if q.k == 0 || q.k >= MaxChain && c.inner(occ, c0+int32(q.k)*dd, player) {
		return false
	}
	k := q.k
	last := c0 + int32(k-1)*dd
	// Caps.
	wallStart := c.wall[c0+a] && c.wall[c0+b]
	capA := c0 - dd
	if !wallStart && !c.blocked(occ, capA) {
		return false
	}
	wallEnd := c.wall[last-a] && c.wall[last-b]
	capB := last + dd
	if !wallEnd && !c.blocked(occ, capB) {
		return false
	}
	q.cA = !wallStart && occ[capA] && !c.goal[capA]
	q.cB = !wallEnd && occ[capB] && !c.goal[capB]
	// Pairs and inner goals.
	for m := 0; m < k; m++ {
		x := c0 + int32(m)*dd
		q.ng[m] = !c.goal[x]
		c.addPair(occ, &q, m, x+a)
		c.addPair(occ, &q, m, x+b)
	}
	c.addPair(occ, &q, k, last-a)
	c.addPair(occ, &q, k, last-b)
	return q.dead(0, k-1)
}

func (c *Checker) addPair(occ []bool, q *chain, m int, x int32) {
	if occ[x] {
		q.has[m] = true
		if !c.goal[x] {
			q.bad[m] = true
		}
	}
}

func (q *chain) sA(a int) bool {
	if a == 0 {
		return q.cA
	}
	return q.ng[a-1]
}

func (q *chain) sB(b int) bool {
	if b == q.k-1 {
		return q.cB
	}
	return q.ng[b+1]
}

// dead: the closed segment of inner cells a..b (with the caps it currently has) cannot be
// completed.
func (q *chain) dead(a, b int) bool {
	if v := q.memo[a][b]; v != 0 {
		return v == 1
	}
	r := q.compute(a, b)
	if r {
		q.memo[a][b] = 1
	} else {
		q.memo[a][b] = 2
	}
	return r
}

func (q *chain) compute(a, b int) bool {
	sa, sb := q.sA(a), q.sB(b)
	obl := sa || sb
	for m := a; m <= b+1 && !obl; m++ {
		obl = q.bad[m]
	}
	if !obl {
		return false // may stay closed forever
	}
	for m := a; m <= b; m++ {
		if q.has[m+1] { // a box of P_{m+1} pushed into I_m
			var d bool
			if m == a {
				d = sa || q.bad[a] || q.ng[a] // fills the block A, P_a, I_a
			} else {
				d = q.dead(a, m-1)
			}
			if !d {
				return false
			}
		}
		if q.has[m] { // a box of P_m pushed into I_m
			var d bool
			if m == b {
				d = sb || q.bad[b+1] || q.ng[b] // fills the block I_b, P_{b+1}, B
			} else {
				d = q.dead(m+1, b)
			}
			if !d {
				return false
			}
		}
	}
	return true
}
