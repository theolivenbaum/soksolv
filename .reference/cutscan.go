package main

// exp-673: one low-link DFS per planner node over G0 (every box placed)
// answers the queries of every lifted box b: G_b = G0 plus b, so the pieces
// of G_b minus `to` are the pieces of G0 minus `to`, merged through b with
// the G0 pieces/components that touch b. (exp-677 removed the per-box DFS
// path and its flag -cut-shared.)

// cutScan answers "which cells can the player reach if one free cell `to` is
// additionally blocked?" for many `to` over one fixed free graph, from a single
// depth-first search with low-links per graph (articulation-point pieces)
// instead of one flood per query. Everything is flat arrays reused across
// calls (stamped by gen); nothing is allocated on the hot path after warm-up.
// cutNode is one cell's DFS record (array of structs: a visit and a query touch
// several fields of the same cell, which were seven separate arrays).
type cutNode struct {
	vgen uint32 // gen when visited by this box's DFS
	tin  int32
	tout int32
	low  int32
	par  int32
	sub  int32 // minimum cell id in the DFS subtree
	cid  int32 // component index
	_    int32
}

type cutScan struct {
	s    *solver
	n    int
	gen  uint32
	dgen uint32 // gen of the DFS graph (cutNode.vgen stamps)
	lift int32  // the lifted box (free, but not in the DFS graph G0); -1 none
	// Per-box data (valid when bgen == gen): the lifted box's free
	// neighbours, their G0 components (distinct) and the minimum cell of the
	// lifted box's own component (the box plus every component it touches).
	bgen  uint32
	nbn   int
	nbc   int
	bn    [4]int32
	bnc   [4]int32
	bcm   [4]int32
	ball  int32
	qpc   []uint8   // cell -> pieces count when `to` is blocked
	nd    []cutNode // per-cell DFS data, one cache line per two cells
	qgen  []uint32  // cell -> gen when its query table was filled
	qnp   []int32   // cell*4+di -> region minimum when the neighbour in direction di is the player
	qsd   []uint8   // cell*4+di -> side set for that player
	pgen  []uint32  // cell -> gen when reachable by the node's player
	seen  []uint32  // pull state (cell*4+dir) -> gen when queued
	egen  []uint32  // cell -> gen when it has end entries
	ecnt  []uint8
	eval  []int32 // cell*4+i -> normalised player of the i-th end at that cell
	pre   []int32 // by DFS position: min cell id in component order up to here
	suf   []int32 // by DFS position: min cell id in component order from here
	order []int32
	cs    []int32 // component start / end positions in order
	ce    []int32
	stack []int32
	sdir  []uint8
	q     []int32
}

func (c *cutScan) init(s *solver) {
	if c.s == s {
		return
	}
	n := len(s.lv.Wall)
	*c = cutScan{s: s, n: n, pgen: make([]uint32, n), qgen: make([]uint32, n), qnp: make([]int32, 4*n), qsd: make([]uint8, 4*n), seen: make([]uint32, 4*n),
		egen: make([]uint32, n), ecnt: make([]uint8, n), eval: make([]int32, 4*n),
		nd: make([]cutNode, n), pre: make([]int32, n), suf: make([]int32, n), qpc: make([]uint8, n), lift: -1}
}

// newNode starts a planner node (s.occ holds every box) and returns the stamp
// the caller uses for the node's player reach (pgen).
func (c *cutScan) newNode() uint32 {
	c.gen++
	c.dgen = c.gen
	c.lift = -1
	c.order = c.order[:0]
	c.cs = c.cs[:0]
	c.ce = c.ce[:0]
	return c.gen
}

// begin starts a new free graph: box b is lifted (the caller clears s.occ[b]).
func (c *cutScan) begin(b int32) {
	c.gen++
	c.lift = b
}

// newEnd records that a pull ended with box `to` and normalised player np, and
// reports false if that pair was already seen for this box. At most four pull
// states reach one `to`, so at most four entries.
func (c *cutScan) newEnd(to, np int32) bool {
	if c.egen[to] != c.gen {
		c.egen[to] = c.gen
		c.ecnt[to] = 0
	}
	for i := uint8(0); i < c.ecnt[to]; i++ {
		if c.eval[to*4+int32(i)] == np {
			return false
		}
	}
	c.eval[to*4+int32(c.ecnt[to])] = np
	c.ecnt[to]++
	return true
}

func (c *cutScan) free(x int32) bool { return !c.s.lv.Wall[x] && !c.s.occ[x] }

// dfs scans the component of root (a free cell) with low-links. Iterative,
// with the per-cell array and the wall/occupancy slices held in locals
// (exp-638; a closure and appends through c made this the plan4 hot loop).
func (c *cutScan) dfs(root int32) {
	s := c.s
	wall, occ, dirs := s.lv.Wall, s.occ, s.dirs
	gen, lift := c.dgen, c.lift
	nd := c.nd
	comp := int32(len(c.cs))
	start := int32(len(c.order))
	c.cs = append(c.cs, start)
	if cap(c.stack) < c.n+1 {
		c.stack, c.sdir = make([]int32, 0, c.n+1), make([]uint8, 0, c.n+1)
	}
	stack, sdir := c.stack[:c.n+1], c.sdir[:c.n+1]
	order := c.order

	nd[root] = cutNode{vgen: gen, tin: int32(len(order)), low: int32(len(order)), par: -1, sub: root, cid: comp}
	order = append(order, root)
	stack[0], sdir[0] = root, 0
	sp := 1
	for sp > 0 {
		top := sp - 1
		v := stack[top]
		nv := &nd[v]
		pushed := false
		for di := sdir[top]; di < 4; di++ {
			w := v + dirs[di]
			if wall[w] || occ[w] || w == lift {
				continue
			}
			nw := &nd[w]
			if nw.vgen != gen {
				t := int32(len(order))
				*nw = cutNode{vgen: gen, tin: t, low: t, par: v, sub: w, cid: comp}
				order = append(order, w)
				sdir[top] = di + 1
				stack[sp], sdir[sp] = w, 0
				sp++
				pushed = true
				break
			} else if w != nv.par && nw.tin < nv.low {
				nv.low = nw.tin
			}
		}
		if pushed {
			continue
		}
		nv.tout = int32(len(order))
		sp = top
		if p := nv.par; p >= 0 {
			np := &nd[p]
			np.low = min(np.low, nv.low)
			np.sub = min(np.sub, nv.sub)
		}
	}
	c.order = order
	end := int32(len(order))
	c.ce = append(c.ce, end)
	m := int32(1 << 30)
	for i := start; i < end; i++ {
		m = min(m, order[i])
		c.pre[i] = m
	}
	m = 1 << 30
	for i := end - 1; i >= start; i-- {
		m = min(m, order[i])
		c.suf[i] = m
	}
}

func (c *cutScan) ensure(v int32) {
	if c.nd[v].vgen != c.dgen {
		c.dfs(v)
	}
}

// boxInfo collects the lifted box's free neighbours and the G0 components
// they lie in.
func (c *cutScan) boxInfo() {
	c.bgen = c.gen
	c.nbn, c.nbc = 0, 0
	b := c.lift
	m := b
	wall, occ := c.s.lv.Wall, c.s.occ
	for _, d := range c.s.dirs {
		y := b + d
		if wall[y] || occ[y] {
			continue
		}
		c.ensure(y)
		cid := c.nd[y].cid
		c.bn[c.nbn], c.bnc[c.nbn] = y, cid
		c.nbn++
		dup := false
		for i := 0; i < c.nbc; i++ {
			dup = dup || c.bcm[i] == cid
		}
		if !dup {
			c.bcm[c.nbc] = cid
			c.nbc++
			m = min(m, c.suf[c.cs[cid]])
		}
	}
	c.ball = m
}

// compMin is the smallest cell id in v's component of the free graph.
func (c *cutScan) compMin(v int32) int32 {
	if c.bgen != c.gen {
		c.boxInfo()
	}
	if v == c.lift {
		return c.ball
	}
	c.ensure(v)
	cid := c.nd[v].cid
	for i := 0; i < c.nbc; i++ {
		if c.bcm[i] == cid {
			return c.ball
		}
	}
	return c.suf[c.cs[cid]]
}

// query returns, for box on `to` and player on its free neighbour `player`,
// the smallest cell of the player's region and the set of directions di such
// that to+dirs[di] lies in that region. Both cells must be free in the graph.
// The answer is the same for every free neighbour in the same piece, and up to
// four pull states of one box end on the same `to`, so the answers for all four
// neighbours of `to` are computed once per box graph (queryFill).
func (c *cutScan) query(to, player int32) (int32, uint8) {
	if c.qgen[to] != c.gen {
		c.queryFill(to)
	}
	i := to * 4
	for di, d := range c.s.dirs {
		if to+d == player {
			return c.qnp[i+int32(di)], c.qsd[i+int32(di)]
		}
	}
	panic("cutScan.query: player is not adjacent to to")
}

// pieces is the number of pieces the component of v falls into when v is
// blocked, in the current cutScan graph (the box on v splits the free space).
func (c *cutScan) pieces(v int32) int32 {
	if c.qgen[v] != c.gen {
		c.queryFill(v)
	}
	return int32(c.qpc[v])
}

// queryFill is queryFill for G_b = G0 plus the lifted box b.
// Blocking `to` != b splits G0's component of `to` into the pieces read off
// the G0 DFS (separated child subtrees, else the parent side); b then joins
// every piece holding a free neighbour of b, and every other G0 component b
// touches, into one merged piece. Blocking b itself leaves G0: the pieces are
// G0's components.
func (c *cutScan) queryFill(to int32) {
	c.qgen[to] = c.gen
	if c.bgen != c.gen {
		c.boxInfo()
	}
	s := c.s
	wall, occ, dirs := s.lv.Wall, s.occ, s.dirs
	lift := c.lift
	base := to * 4
	var lab [4]int32
	var fr [4]bool
	if to == lift {
		for di, d := range dirs {
			w := to + d
			if wall[w] || occ[w] {
				continue
			}
			fr[di], lab[di] = true, c.nd[w].cid // ensured by boxInfo
		}
		np := uint8(0)
		for di := range dirs {
			if !fr[di] {
				continue
			}
			var sides uint8
			first := true
			for dj := range dirs {
				if fr[dj] && lab[dj] == lab[di] {
					sides |= 1 << uint(dj)
					first = first && dj >= di
				}
			}
			if first {
				np++
			}
			c.qsd[base+int32(di)] = sides
			c.qnp[base+int32(di)] = c.suf[c.cs[lab[di]]]
		}
		c.qpc[to] = np
		return
	}
	c.ensure(to)
	gen := c.dgen
	nt := &c.nd[to]
	tinTo, toutTo, cx := nt.tin, nt.tout, nt.cid
	isRoot := nt.par < 0
	var cu, clo, chi [4]int32 // child cell, subtree start, subtree end
	var csep [4]bool          // the child's subtree is cut off when `to` is blocked
	nc := 0
	for _, d := range dirs {
		u := to + d
		if wall[u] || occ[u] || u == lift {
			continue
		}
		if nu := &c.nd[u]; nu.vgen == gen && nu.par == to {
			cu[nc], clo[nc], chi[nc] = u, nu.tin, nu.tout
			csep[nc] = isRoot || nu.low >= tinTo
			nc++
		}
	}
	// pieceOf: index of the separated child subtree holding DFS time tw, else
	// -1 (the parent side).
	pieceOf := func(tw int32) int32 {
		if tw > tinTo && tw < toutTo {
			for i := 0; i < nc; i++ {
				if clo[i] <= tw && tw < chi[i] {
					if csep[i] {
						return int32(i)
					}
					break
				}
			}
		}
		return -1
	}
	// pieces merged through the lifted box
	var inM [4]bool
	parentInM := false
	mm := lift
	for i := 0; i < c.nbn; i++ {
		y := c.bn[i]
		if y == to {
			continue
		}
		if c.bnc[i] != cx {
			mm = min(mm, c.suf[c.cs[c.bnc[i]]])
			continue
		}
		if j := pieceOf(c.nd[y].tin); j >= 0 {
			inM[j] = true
		} else {
			parentInM = true
		}
	}
	const merged = -2
	needParent := parentInM
	for di, d := range dirs {
		w := to + d
		if w == lift {
			fr[di], lab[di] = true, merged
			continue
		}
		if wall[w] || occ[w] {
			continue
		}
		fr[di] = true
		j := pieceOf(c.nd[w].tin)
		switch {
		case j >= 0 && inM[j], j < 0 && parentInM:
			lab[di] = merged
		default:
			lab[di] = j
			needParent = needParent || j < 0
		}
	}
	parentMin := int32(0)
	if needParent {
		m := int32(1 << 30)
		if tinTo > c.cs[cx] {
			m = c.pre[tinTo-1]
		}
		if toutTo < c.ce[cx] {
			m = min(m, c.suf[toutTo])
		}
		for i := 0; i < nc; i++ {
			if nu := &c.nd[cu[i]]; nu.low < tinTo {
				m = min(m, nu.sub)
			}
		}
		parentMin = m
	}
	if parentInM {
		mm = min(mm, parentMin)
	}
	for i := 0; i < nc; i++ {
		if inM[i] {
			mm = min(mm, c.nd[cu[i]].sub)
		}
	}
	np := uint8(0)
	for di := range dirs {
		if !fr[di] {
			continue
		}
		var sides uint8
		first := true
		for dj := range dirs {
			if fr[dj] && lab[dj] == lab[di] {
				sides |= 1 << uint(dj)
				first = first && dj >= di
			}
		}
		if first {
			np++
		}
		c.qsd[base+int32(di)] = sides
		switch l := lab[di]; {
		case l == merged:
			c.qnp[base+int32(di)] = mm
		case l >= 0:
			c.qnp[base+int32(di)] = c.nd[cu[l]].sub
		default:
			c.qnp[base+int32(di)] = parentMin
		}
	}
	c.qpc[to] = np
}
