package main

import "math/bits"

// hotspotTable caches the relaxed pair relation: row[y] contains every box
// square x whose reachable-goal set shrinks if y is made a permanent wall.
// Rows are built on demand, amortizing O(goals * floor) over all visits to y.
// This is advisor information only; it never proves a deadlock.
type hotspotTable struct {
	rows  [][]uint64
	seen  []uint32
	stamp uint32
	queue []int32

	// Per-goal pull-reachability trees (built once, see buildTrees), indexed
	// g*nf+fi: parent floor index, Euler-tour entry and subtree size
	// (tin -1 = not reachable), and byTin the floor index at each entry.
	trees               bool
	par, tin, size, byT []int32
	rs                  []uint32 // per floor index: reached-again stamp
	rstamp              uint32
	rq                  []int32

	// Per-node scratch for after(), filled by counts(): one row per box,
	// the box floor indices, and lazily built hit columns.
	nodeRows [][]uint64
	nodeIdx  []int32
	bw       int      // words per box bitset
	colGen   []uint32 // per floor index: node generation of col
	col      []uint64 // per floor index, bw words: boxes whose row hits it
	gen      uint32
	pos      []uint64 // bitset of box floor indices (nw words)
	mgen     int      // moved box prepared in mask, -1 none
	n0       int
	lmask    []uint64 // boxes (excluding moved) whose count is 0 without moved
	mpos     []uint64 // pos without the moved box

	// goalFree (exp-491 fa2): goals are neither blockers nor victims
	// (Festival's hotspot relation). Set before the first row is built.
	goalFree bool

	// -hot-sides scratch (exp-694); the tables themselves are shared per level.
	sc   *sideScratch
	zone []int32
	with []uint64
}

func newHotspotTable(s *solver) *hotspotTable {
	return &hotspotTable{rows: make([][]uint64, len(s.fcell)), seen: make([]uint32, len(s.lv.Wall))}
}

// buildTrees records, per goal, the BFS tree of the pull reachability used by
// row (box squares from which the goal is reachable) with an Euler tour, so a
// blocker's effect can be found by re-searching only the subtrees it cuts.
func (h *hotspotTable) buildTrees(s *solver) {
	h.trees = true
	nf, ng := len(s.fcell), len(s.goals)
	h.par = make([]int32, ng*nf)
	h.tin = make([]int32, ng*nf)
	h.size = make([]int32, ng*nf)
	h.byT = make([]int32, ng*nf)
	h.rs = make([]uint32, nf)
	lv := s.lv
	for i := range h.tin {
		h.tin[i] = -1
	}
	next := make([]int32, nf)
	for g, goal := range s.goals {
		base := g * nf
		par, tin, size, byT := h.par[base:base+nf], h.tin[base:base+nf], h.size[base:base+nf], h.byT[base:base+nf]
		gd := s.goalDist[g*len(lv.Wall) : (g+1)*len(lv.Wall)]
		h.stamp++
		st := h.stamp
		q := append(h.queue[:0], goal)
		h.seen[goal] = st
		par[s.fidx[goal]] = -1
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				b := q[i] - d
				if lv.Wall[b] || h.seen[b] == st || lv.Wall[s.supportCell(b, q[i], d)] || gd[b] < 0 {
					continue // exp-464: with -exact-reach the tree keeps to cells that reach this goal exactly
				}
				h.seen[b] = st
				par[s.fidx[b]] = q[i]
				q = append(q, b)
			}
		}
		h.queue = q
		for i := len(q) - 1; i >= 0; i-- {
			fi := s.fidx[q[i]]
			size[fi]++
			if p := par[fi]; p >= 0 && i > 0 {
				size[s.fidx[p]] += size[fi]
			}
		}
		tin[s.fidx[goal]] = 0
		byT[0] = s.fidx[goal]
		next[s.fidx[goal]] = 1
		for _, c := range q[1:] {
			fi, pf := s.fidx[c], s.fidx[par[s.fidx[c]]]
			tin[fi] = next[pf]
			next[pf] += size[fi]
			next[fi] = tin[fi] + 1
			byT[tin[fi]] = fi
		}
	}
}

func (h *hotspotTable) row(s *solver, blocker int32) []uint64 {
	idx := s.fidx[blocker]
	if h.rows[idx] != nil {
		return h.rows[idx]
	}
	if s.hotSides {
		row := h.rowSides(s, blocker)
		h.rows[idx] = row
		return row
	}
	if !h.trees {
		h.buildTrees(s)
	}
	row := make([]uint64, s.nw)
	nf := len(s.fcell)
	var rg [10][2]int32 // merged subtree ranges [lo, hi) of the cut roots
	for g, goal := range s.goals {
		if s.expired() {
			return row
		}
		base := g * nf
		tin, size, byT, par := h.tin[base:base+nf], h.size[base:base+nf], h.byT[base:base+nf], h.par[base:base+nf]
		nr := 0
		if goal == blocker {
			for _, fi := range byT[1:size[s.fidx[goal]]] {
				row[fi/64] |= 1 << uint(fi%64)
			}
			continue
		}
		add := func(fi int32) {
			rg[nr] = [2]int32{tin[fi], tin[fi] + size[fi]}
			nr++
		}
		if tin[idx] >= 0 {
			add(idx)
		}
		// Tree edges whose support square is the blocker are cut too: the
		// child is one step or two from it, whichever way supports run.
		for _, d := range s.dirs {
			for _, b := range [2]int32{blocker + d, blocker - 2*d} {
				if uint32(b) >= uint32(len(s.lv.Wall)) {
					continue
				}
				fi := s.fidx[b]
				if fi < 0 || tin[fi] < 0 || par[fi] < 0 {
					continue
				}
				if q := par[fi]; s.supportCell(b, q, q-b) == blocker {
					add(fi)
				}
			}
		}
		if nr == 0 {
			continue
		}
		// Merge nested/duplicate ranges (nr is tiny).
		for i := 1; i < nr; i++ {
			for j := i; j > 0 && rg[j][0] < rg[j-1][0]; j-- {
				rg[j], rg[j-1] = rg[j-1], rg[j]
			}
		}
		m := 0
		for i := 0; i < nr; i++ {
			if m > 0 && rg[i][0] < rg[m-1][1] {
				rg[m-1][1] = max(rg[m-1][1], rg[i][1])
			} else {
				rg[m] = rg[i]
				m++
			}
		}
		inS := func(fi int32) bool {
			t := tin[fi]
			for i := 0; i < m; i++ {
				if t >= rg[i][0] && t < rg[i][1] {
					return true
				}
			}
			return false
		}
		h.rstamp++
		rs := h.rstamp
		q := h.rq[:0]
		reach := func(b, from int32, d int32) bool {
			sup := s.supportCell(b, from, d)
			return b != blocker && sup != blocker && !s.lv.Wall[b] && !s.lv.Wall[sup]
		}
		for i := 0; i < m; i++ {
			for _, fi := range byT[rg[i][0]:rg[i][1]] {
				b := s.fcell[fi]
				if b == blocker {
					continue
				}
				for _, d := range s.dirs {
					from := b + d
					if ff := s.fidx[from]; ff >= 0 && tin[ff] >= 0 && !inS(ff) && reach(b, from, d) {
						h.rs[fi] = rs
						q = append(q, b)
						break
					}
				}
			}
		}
		for k := 0; k < len(q); k++ {
			for _, d := range s.dirs {
				b := q[k] - d
				if fb := s.fidx[b]; fb >= 0 && tin[fb] >= 0 && h.rs[fb] != rs && inS(fb) && reach(b, q[k], d) {
					h.rs[fb] = rs
					q = append(q, b)
				}
			}
		}
		h.rq = q
		for i := 0; i < m; i++ {
			for _, fi := range byT[rg[i][0]:rg[i][1]] {
				if h.rs[fi] != rs && s.fcell[fi] != blocker {
					row[fi/64] |= 1 << uint(fi%64)
				}
			}
		}
	}
	if h.goalFree {
		if s.lv.Goal[blocker] {
			clear(row)
		} else {
			for _, g := range s.goals {
				fi := s.fidx[g]
				row[fi/64] &^= 1 << uint(fi%64)
			}
		}
	}
	h.rows[idx] = row
	return row
}

func hotspotHits(row []uint64, index int32) bool {
	return row[index/64]&(1<<uint(index%64)) != 0
}

// counts returns, per occupied blocker, the number of other boxes it affects.
// It also readies the per-node scratch that after() uses: after() must be
// called for the same boxes, until the next counts().
func (h *hotspotTable) counts(s *solver, boxes []int32) []int {
	counts := make([]int, len(boxes))
	h.nodeRows = h.nodeRows[:0]
	h.nodeIdx = h.nodeIdx[:0]
	if h.col == nil {
		h.pos = make([]uint64, s.nw)
		h.mpos = make([]uint64, s.nw)
		h.colGen = make([]uint32, len(s.fcell))
	}
	clear(h.pos)
	for _, b := range boxes {
		h.nodeRows = append(h.nodeRows, h.row(s, b))
		h.nodeIdx = append(h.nodeIdx, s.fidx[b])
		h.pos[s.fidx[b]/64] |= 1 << uint(s.fidx[b]%64)
	}
	for i, row := range h.nodeRows {
		for j, x := range h.nodeIdx {
			if i != j && hotspotHits(row, x) {
				counts[i]++
			}
		}
	}
	// New node: columns are rebuilt on first use; any moved box is re-prepared.
	bw := (len(boxes) + 63) / 64
	if bw != h.bw || h.col == nil {
		h.bw = bw
		h.col = make([]uint64, len(s.fcell)*max(bw, 1))
		h.lmask = make([]uint64, max(bw, 1))
		clear(h.colGen)
		h.gen = 0
	}
	h.gen++
	h.mgen = -1
	return counts
}

// column returns the set of boxes (by index) whose row hits floor index x.
func (h *hotspotTable) column(x int32) []uint64 {
	w := h.col[int(x)*h.bw : int(x)*h.bw+h.bw]
	if h.colGen[x] != h.gen {
		h.colGen[x] = h.gen
		clear(w)
		for i, row := range h.nodeRows {
			if hotspotHits(row, x) {
				w[i/64] |= 1 << uint(i%64)
			}
		}
	}
	return w
}

// after returns the number of hotspot boxes after replacing boxes[moved]
// with target. For a fixed moved box, each other box's count without it is
// fixed, so a candidate only needs the boxes whose count would drop to zero
// (lmask) that the target hits, and whether the target's row hits any box.
func (h *hotspotTable) after(s *solver, boxes []int32, counts []int, moved int, target int32) int {
	old, now := h.nodeIdx[moved], s.fidx[target]
	if h.mgen != moved {
		h.mgen = moved
		h.n0 = 0
		clear(h.lmask)
		oc := h.column(old)
		for i := range boxes {
			if i == moved {
				continue
			}
			c := counts[i]
			if oc[i/64]&(1<<uint(i%64)) != 0 {
				c--
			}
			if c > 0 {
				h.n0++
			} else if c == 0 {
				h.lmask[i/64] |= 1 << uint(i%64)
			}
		}
		copy(h.mpos, h.pos)
		h.mpos[old/64] &^= 1 << uint(old%64)
	}
	n := h.n0
	nc := h.column(now)
	for k, w := range h.lmask {
		n += bits.OnesCount64(w & nc[k])
	}
	row := h.row(s, target)
	for k, w := range h.mpos {
		if w&row[k] != 0 {
			n++
			break
		}
	}
	return n
}

func (h *hotspotTable) bytes() int {
	n := cap(h.rows)*24 + cap(h.seen)*4 + cap(h.queue)*4 + (cap(h.par)+cap(h.tin)+cap(h.size)+cap(h.byT))*4 + cap(h.rs)*4 + cap(h.col)*8 + cap(h.colGen)*4
	for _, row := range h.rows {
		n += cap(row) * 8
	}
	return n
}

func hotspotCount(counts []int) int {
	n := 0
	for _, c := range counts {
		if c > 0 {
			n++
		}
	}
	return n
}
