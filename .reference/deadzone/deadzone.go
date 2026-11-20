// Package deadzone proves corral deadlocks (exp-227).
//
// A corral is an area of empty floor the player cannot reach. Keep only the
// boxes that fence it (its zone), turn boxes that can never move into walls,
// remove every other box, and search the pushes of the zone boxes alone. A
// real solution, projected onto the zone boxes, is a legal push sequence of
// this smaller problem (fewer boxes only help the player, fixed boxes never
// move), and it ends with every zone box on a goal. So if the search
// exhausts without reaching that, the position is dead. The search stops
// early (not dead) as soon as the player can walk everywhere in the reduced
// board: the corral is open, and further proof would cost more than it buys.
//
// Fixed boxes are the greatest set of boxes each blocked on both axes by
// walls, other fixed boxes, or (along an axis) dead squares on both sides:
// none of them can be the first to move. A fixed box off a goal is itself a
// deadlock.
package deadzone

import (
	"math/bits"
	"slices"
	"sync/atomic"
)

// Board is the static level.
type Board struct {
	W                int32
	Wall, Goal, Dead []bool // Dead: a lone box there can never reach a goal
	dirs             [4]int32
	salt             uint64 // see Salt
}

// Salt (exp-688) folds a per-board hash of walls, goals and dead squares into
// every shared-table key (see cluster.Salt).
var Salt bool

func boardSalt(wall, goal, dead []bool) uint64 {
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

// NewBoard returns the static board (dead may be nil: none).
func NewBoard(wall, goal, dead []bool, w int) *Board {
	if dead == nil {
		dead = make([]bool, len(wall))
	}
	return &Board{W: int32(w), Wall: wall, Goal: goal, Dead: dead, dirs: [4]int32{-int32(w), int32(w), -1, 1}, salt: boardSalt(wall, goal, dead)}
}

// Stats counts what the checker did (diagnostics).
type Stats struct {
	Calls, Matching, Corrals, Searches, CacheHits, Rechecks, Dead, Frozen, Unknown, Opened, States int
}

// Checker is one searcher's prover (scratch and cache are private).
type Checker struct {
	Private    bool // do not exchange goal-dependent proofs with global tables
	ReadOnly   bool // exp-741: read the shared verdict table, never write it
	b          *Board
	Cap        int // states per zone search
	Stats      Stats
	fixed      []bool
	wall2      []bool // fixed boxes that are walls of the current reduced board
	occ        []bool // reduced board: zone boxes
	comp       []uint32
	cst        uint32
	rmark      []uint32
	rst        uint32
	pmark      []uint32 // the player's region on the real board
	pst        uint32
	cmark      []uint32 // child floods during the search
	cmst       uint32
	zmark      []uint32
	zst        uint32
	area       []uint32 // the corral's cells and their 8 neighbours (stamp zst)
	fz         []bool
	q, zq      []int32
	zone       []int32
	cells      []int32 // the corral's cells
	realMin    int32   // minimum cell of the player's real region (FastKey)
	bitsOK     bool    // bitset floods (board width <= 64)
	rowOf      []int32
	colOf      []uint8
	base       []uint64
	baseStatic []uint64 // the board's floor, no fixed walls
	free       []uint64
	rbits      []uint64 // the parent state's region
	cbits      []uint64 // the child state's region
	prs        uint32   // stamp of the parent flood (no bitsets)
	fm, hm     []uint64 // fence mask (8-neighbourhood of unreachable free cells)
	ain, bx    []uint64 // the corral's area; the live boxes during escape
	tb, rm     []uint64 // push targets (free, not dead, outside the area); boxes removed this pass
	deadB      []uint64 // static dead cells
	fmOK       bool
	pOK        bool  // pm, psz describe rbits
	pm         int32 // minimum cell and size of the parent region
	psz        int
	spn        int32 // seedFinal's result
	ssz        int
	allCells   []int32
	corrals    [][2]int32 // ranges into allCells
	zoneAll    []int32
	zones      [][2]int32 // ranges into zoneAll
	cellsTmp   []int32
	uf         []int32
	owner      []int32
	members    []int32
	floorN     int // non-wall cells
	// search
	arena  []int16 // states: len(zone) boxes then the normalized player
	slots  []int32 // open addressing over arena state indices (+1; 0 empty)
	cur    []int32
	nxt    []int32
	fixedN int
	// result cache: key | verdict (1 dead, 2 alive, 3 unknown) in the low 2 bits
	cache []uint64
	// matching with fixed boxes as walls: per fixed set, for every cell the
	// bitset of goals a lone box there can still reach
	goals    []int32
	gw       int
	reachFix map[uint64][]uint64
	mOf      []int32
	mSeen    []uint64 // goals visited by the current augmenting search (bitset)
	cnt      []uint16 // queries of an unknown entry: re-searched larger at 8, 64, 512
	zob      []uint64
}

// NewChecker returns a prover for b.
func NewChecker(b *Board, capStates int) *Checker {
	n := len(b.Wall)
	c := &Checker{b: b, Cap: capStates, fixed: make([]bool, n), wall2: make([]bool, n), occ: make([]bool, n), comp: make([]uint32, n),
		rmark: make([]uint32, n), cmark: make([]uint32, n), pmark: make([]uint32, n), zmark: make([]uint32, n), area: make([]uint32, n), fz: make([]bool, n), cache: make([]uint64, 1<<16), cnt: make([]uint16, 1<<16), zob: make([]uint64, 2*n)}
	if b.W <= 64 && BitFlood {
		H := len(b.Wall) / int(b.W)
		c.bitsOK = true
		c.rowOf, c.colOf = make([]int32, n), make([]uint8, n)
		for x := range n {
			c.rowOf[x], c.colOf[x] = int32(x)/b.W, uint8(int32(x)%b.W)
		}
		c.base, c.free, c.rbits, c.cbits = make([]uint64, H), make([]uint64, H), make([]uint64, H), make([]uint64, H)
		c.ain, c.bx, c.tb, c.rm, c.deadB = make([]uint64, H), make([]uint64, H), make([]uint64, H), make([]uint64, H), make([]uint64, H)
		c.baseStatic = make([]uint64, H)
		for x := range n {
			if !b.Wall[x] {
				c.baseStatic[c.rowOf[x]] |= 1 << c.colOf[x]
			}
		}
		for x := range n {
			if b.Dead[x] {
				c.deadB[c.rowOf[x]] |= 1 << c.colOf[x]
			}
		}
	}
	x := uint64(0x9E3779B97F4A7C15)
	for i := range c.zob {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		c.zob[i] = x
	}
	for _, w := range b.Wall {
		if !w {
			c.floorN++
		}
	}
	return c
}

func (c *Checker) stampR() uint32 {
	c.rst++
	if c.rst == 0 {
		clear(c.rmark)
		c.rst = 1
	}
	return c.rst
}

// fixedBoxes computes the fixed set (c.fixed) and reports whether a fixed
// box is off goal (a freeze deadlock).
func (c *Checker) fixedBoxes(occ []bool, boxes []int32) bool {
	b := c.b
	for _, x := range boxes {
		c.fixed[x] = true
	}
	blk := func(x int32) bool { return b.Wall[x] || c.fixed[x] }
	for changed := true; changed; {
		changed = false
		for _, x := range boxes {
			if !c.fixed[x] {
				continue
			}
			h := blk(x-1) || blk(x+1) || b.Dead[x-1] && b.Dead[x+1]
			v := blk(x-b.W) || blk(x+b.W) || b.Dead[x-b.W] && b.Dead[x+b.W]
			if !h || !v {
				c.fixed[x] = false
				changed = true
			}
		}
	}
	for _, x := range boxes {
		if c.fixed[x] && !b.Goal[x] {
			return true
		}
	}
	return false
}

// Dead reports whether the position (occ marks its boxes, listed in boxes)
// with the player on cell player is a proved corral or freeze deadlock.
func (c *Checker) Dead(occ []bool, boxes []int32, player int32) bool {
	return c.DeadAfter(occ, boxes, player, -1)
}

// DeadAfter is Dead for a position just reached by pushing a box onto t:
// only corrals within two cells of t are searched (others were there
// before the push). t < 0: every corral.
func (c *Checker) DeadAfter(occ []bool, boxes []int32, player, t int32) bool {
	c.Stats.Calls++
	defer func() {
		for _, x := range boxes {
			c.fixed[x] = false
		}
	}()
	if c.fixedBoxes(occ, boxes) {
		c.Stats.Frozen++
		return true
	}
	if Matching && !c.matchFixed(boxes) {
		c.Stats.Matching++
		return true
	}
	b := c.b
	// The player's region on the real board.
	c.pst++
	if c.pst == 0 {
		clear(c.pmark)
		c.pst = 1
	}
	rs := c.pst
	c.q = append(c.q[:0], player)
	c.pmark[player] = rs
	c.realMin = player
	for i := 0; i < len(c.q); i++ {
		for _, d := range b.dirs {
			if y := c.q[i] + d; c.pmark[y] != rs && !b.Wall[y] && !occ[y] {
				c.pmark[y] = rs
				c.q = append(c.q, y)
				c.realMin = min(c.realMin, y)
			}
		}
	}
	c.cst++
	if c.cst == 0 {
		clear(c.comp)
		c.cst = 1
	}
	cs0 := c.cst
	// Every corral (a free region the player can't reach) touches a box.
	c.allCells = c.allCells[:0]
	c.corrals = c.corrals[:0]
	for _, x0 := range boxes {
		for _, d0 := range b.dirs {
			start := x0 + d0
			if b.Wall[start] || occ[start] || c.pmark[start] == rs || c.comp[start] >= cs0 {
				continue
			}
			c.cst++
			cs := c.cst
			c.comp[start] = cs
			c0 := len(c.allCells)
			c.allCells = append(c.allCells, start)
			for i := c0; i < len(c.allCells); i++ {
				for _, d := range b.dirs {
					if y := c.allCells[i] + d; !b.Wall[y] && !occ[y] && c.comp[y] != cs {
						c.comp[y] = cs
						c.allCells = append(c.allCells, y)
					}
				}
			}
			c.corrals = append(c.corrals, [2]int32{int32(c0), int32(len(c.allCells))})
		}
	}
	// Single corrals, then pairs sharing a zone box (Festival checks both).
	c.zones = c.zones[:0]
	for i, cr := range c.corrals {
		work := c.buildZone(occ, cr, [2]int32{})
		c.zones = append(c.zones, [2]int32{int32(len(c.zoneAll)), 0})
		c.zoneAll = append(c.zoneAll[:c.zones[i][0]], c.zone...)
		c.zones[i][1] = int32(len(c.zoneAll))
		if Near && t >= 0 && !c.near(t) {
			continue
		}
		if c.tryZone(work, boxes, player) {
			return true
		}
	}
	if Pairs {
		for i := range c.corrals {
			c.zst++
			zs := c.zst
			for _, z := range c.zoneAll[c.zones[i][0]:c.zones[i][1]] {
				c.zmark[z] = zs
			}
			for j := i + 1; j < len(c.corrals); j++ {
				shared := false
				for _, z := range c.zoneAll[c.zones[j][0]:c.zones[j][1]] {
					if c.zmark[z] == zs {
						shared = true
						break
					}
				}
				if !shared {
					continue
				}
				work := c.buildZone(occ, c.corrals[i], c.corrals[j])
				if c.tryZone(work, boxes, player) {
					return true
				}
				c.zst++ // buildZone used new stamps: re-mark corral i's zone
				zs = c.zst
				for _, z := range c.zoneAll[c.zones[i][0]:c.zones[i][1]] {
					c.zmark[z] = zs
				}
			}
		}
	}
	if Groups && len(c.corrals) > 2 && c.groupsDead(occ, boxes, player) {
		return true
	}
	return false
}

// Groups (exp-230) also searches every group of three or more corrals
// connected through shared fence boxes (chains such as Festival's diagonal
// deadlocks) as one zone.
var Groups = true

func (c *Checker) groupsDead(occ []bool, boxes []int32, player int32) bool {
	n := len(c.corrals)
	if cap(c.uf) < n {
		c.uf = make([]int32, n)
	}
	uf := c.uf[:n]
	for i := range uf {
		uf[i] = int32(i)
	}
	find := func(x int32) int32 {
		for uf[x] != x {
			uf[x] = uf[uf[x]]
			x = uf[x]
		}
		return x
	}
	c.zst++
	zs := c.zst
	if len(c.owner) < len(c.b.Wall) {
		c.owner = make([]int32, len(c.b.Wall))
	}
	for i := range n {
		for _, z := range c.zoneAll[c.zones[i][0]:c.zones[i][1]] {
			if c.zmark[z] == zs {
				a, b := find(c.owner[z]), find(int32(i))
				if a != b {
					uf[a] = b
				}
			} else {
				c.zmark[z] = zs
				c.owner[z] = int32(i)
			}
		}
	}
	for root := range int32(n) {
		if find(root) != root {
			continue
		}
		members := c.members[:0]
		for i := range int32(n) {
			if find(i) == root {
				members = append(members, i)
			}
		}
		c.members = members
		if len(members) < 3 || len(members) > 64 {
			continue
		}
		c.cellsTmp = c.cellsTmp[:0]
		for _, i := range members {
			cr := c.corrals[i]
			c.cellsTmp = append(c.cellsTmp, c.allCells[cr[0]:cr[1]]...)
		}
		saved := c.Cap
		if GroupCap > 0 {
			c.Cap = GroupCap
		}
		dead := c.tryZone(c.buildZoneCells(occ, c.cellsTmp), boxes, player)
		c.Cap = saved
		if dead {
			return true
		}
	}
	return false
}

// LocalFixed keeps only fixed boxes near the corral as reduced-board walls.
var LocalFixed = true

// nearArea reports whether x or an 8-neighbour lies in the current area.
func (c *Checker) nearArea(x int32) bool {
	w := c.b.W
	for _, d := range [9]int32{-w - 1, -w, -w + 1, -1, 0, 1, w - 1, w, w + 1} {
		if c.area[x+d] == c.zst {
			return true
		}
	}
	return false
}

// Table is a result cache shared by every searcher of one level: key |
// verdict (1 dead, 2 alive); unknown results are not shared.
type Table struct{ slots []atomic.Uint64 }

// NewTable returns a shared table with 1<<log slots.
func NewTable(log int) *Table { return &Table{slots: make([]atomic.Uint64, 1<<log)} }

// Shared, when set, is consulted and filled by every checker.
var Shared atomic.Pointer[Table]

// Pairs also searches pairs of corrals that share a fence box.
var Pairs = true

func (c *Checker) tryZone(work bool, boxes []int32, player int32) bool {
	if !work || len(c.zone) == 0 || len(c.zone) > 32000 {
		return false
	}
	c.Stats.Corrals++
	if Debug != nil {
		Debug("corral: %d cells, zone %v", len(c.cells), c.zone)
	}
	if c.zoneDead(boxes, player) {
		c.Stats.Dead++
		return true
	}
	return false
}

// buildZone sets c.cells, c.zone and the area stamp (c.zst) for the union
// of corrals a and b (b empty: just a), and reports whether a zone box is
// off goal.
func (c *Checker) buildZone(occ []bool, a, bb [2]int32) bool {
	c.cellsTmp = append(append(c.cellsTmp[:0], c.allCells[a[0]:a[1]]...), c.allCells[bb[0]:bb[1]]...)
	return c.buildZoneCells(occ, c.cellsTmp)
}

func (c *Checker) buildZoneCells(occ []bool, cells []int32) bool {
	b := c.b
	c.zst++
	if c.zst == 0 {
		clear(c.zmark)
		clear(c.area)
		c.zst = 1
	}
	zs := c.zst
	c.cells = append(c.cells[:0], cells...)
	c.zone = c.zone[:0]
	work := false
	addZone := func(y int32) {
		if occ[y] && !c.fixed[y] && c.zmark[y] != zs {
			c.zmark[y] = zs
			c.zone = append(c.zone, y)
			work = work || !b.Goal[y]
		}
	}
	for _, x := range c.cells {
		for _, d := range b.dirs {
			y := x + d
			if occ[y] {
				addZone(y)
				// a second box in line fences the corral too unless the
				// first can step aside (Festival's zone)
				if Wide && occ[y+d] && !c.fixed[y+d] && !c.sideways(occ, y+d, d) {
					addZone(y + d)
				}
			}
		}
		if Wide {
			for _, d := range [4]int32{-b.W - 1, -b.W + 1, b.W - 1, b.W + 1} {
				if !b.Wall[x+d] {
					addZone(x + d)
				}
			}
		}
		for _, d := range [9]int32{-b.W - 1, -b.W, -b.W + 1, -1, 0, 1, b.W - 1, b.W, b.W + 1} {
			c.area[x+d] = zs
		}
	}
	for _, z := range c.zone {
		c.area[z] = zs
	}
	return work
}

// Wide uses Festival's zone: diagonal neighbours of the corral and boxes in
// line behind a fence box that can't be pushed sideways.
var Wide = true

// Kill removes a box pushed out of the corral's area (its cells and their
// 8 neighbours) from the zone search, as Festival's deadlock search does:
// fewer boxes only relax the problem.
var Kill = true

// Shrink also removes, after each push, every box that no longer touches
// (8-neighbourhood) a floor cell the player can't reach.
var Shrink = true

// ShrinkAll applies Shrink to every box, not only the pushed one.
var ShrinkAll = true

// Escape removes, before a state is stored, every box that sits outside
// the corral's area or has a legal push out of it (repeatedly): a box that
// can leave is not what traps the corral, and removal only relaxes.
var Escape = true

// escape applies Escape to nxt (c.occ holds its live boxes) with the player
// on p, and returns the final reach (c.cmark) and live box count.
func (c *Checker) escapeScan(nxt []int32, p int32) (int32, int, int) {
	b := c.b
	for {
		pn, size := c.childFlood(p, nxt)
		if c.bitsOK && ShrinkAll {
			c.fenceMask()
		}
		live, removed := 0, false
		for j, y := range nxt {
			if y == 0 {
				continue
			}
			out := c.area[y] != c.zst || ShrinkAll && !c.fencing(y)
			for _, d := range b.dirs {
				if out {
					break
				}
				t := y + d
				if !c.inChild(y-d) || b.Wall[t] || c.wall2[t] || c.occ[t] || b.Dead[t] {
					continue
				}
				out = c.area[t] != c.zst
			}
			if out {
				c.occ[y] = false
				nxt[j] = 0
				removed = true
				continue
			}
			live++
		}
		if !removed {
			return pn, size, live
		}
	}
}

// CheckEscape (tests) runs the per-box scan beside the bitset escape and
// panics on any difference.
var CheckEscape bool

func (c *Checker) escape(nxt []int32, p int32, seed int32) (int32, int, int) {
	if !c.bitsOK || !ShrinkAll {
		return c.escapeScan(nxt, p)
	}
	if !CheckEscape {
		return c.escapeBits(nxt, p, seed)
	}
	orig := slices.Clone(nxt)
	for x := range c.area {
		if c.inBits(c.ain, int32(x)) != (c.area[x] == c.zst) && !c.b.Wall[x] {
			panic("deadzone: area bitset differs")
		}
	}
	pn, size, live := c.escapeBits(nxt, p, seed)
	got := slices.Clone(nxt)
	occ := make([]bool, len(orig))
	for j, y := range orig {
		if y != 0 {
			occ[j] = c.occ[y]
			c.occ[y] = true
		}
	}
	n2 := slices.Clone(orig)
	pn2, size2, live2 := c.escapeScan(n2, p)
	for j, y := range orig {
		if y != 0 && c.occ[y] != occ[j] {
			panic("deadzone: escape occ differs")
		}
	}
	if pn != pn2 || size != size2 || live != live2 || !slices.Equal(got, n2) {
		panic("deadzone: escape bitset differs from scan")
	}
	return pn, size, live
}

// buildArea sets c.ain to the stamped area (the corral's cells and their 8
// neighbours, and the zone boxes) for the current zone.
func (c *Checker) buildArea() {
	b := c.b
	clear(c.ain)
	for _, x := range c.cells {
		for _, d := range [9]int32{-b.W - 1, -b.W, -b.W + 1, -1, 0, 1, b.W - 1, b.W, b.W + 1} {
			y := x + d
			c.ain[c.rowOf[y]] |= 1 << c.colOf[y]
		}
	}
	for _, z := range c.zone {
		c.ain[c.rowOf[z]] |= 1 << c.colOf[z]
	}
}

// escapeBits is escape with the box tests done on row bitsets: a box leaves
// when it is outside the area, touches no unreachable free cell, or has a
// legal push from a reached cell to a free, non-dead cell outside the area.
// Removal only relaxes, so removing every such box of a pass at once reaches
// the same fixpoint as one at a time; the flood after a pass resumes from
// the previous (smaller) reach.
func (c *Checker) escapeBits(nxt []int32, p int32, seed int32) (int32, int, int) {
	H := len(c.cbits)
	bx := c.bx
	clear(bx)
	live := 0
	blo, bhi := H, -1 // rows holding boxes
	for _, y := range nxt {
		if y != 0 {
			r := int(c.rowOf[y])
			bx[r] |= 1 << c.colOf[y]
			live++
			blo, bhi = min(blo, r), max(bhi, r)
		}
	}
	c.fmOK = false
	F := c.free
	copy(F, c.base)
	for r := blo; r <= bhi; r++ {
		F[r] &^= bx[r]
	}
	var pn int32
	var size int
	if seed >= 0 && c.seedChild(p, seed) {
		if c.seedFinal(p, seed) {
			pn, size = c.spn, c.ssz
		} else {
			pn, size = c.floodFreeMode(p, c.cbits, true, true)
		}
		if CheckEscape {
			got := slices.Clone(c.cbits)
			pn2, size2 := c.floodFree(p, c.cbits, false)
			if pn != pn2 || size != size2 {
				panic("deadzone: seeded child flood differs")
			}
			if !slices.Equal(got, c.cbits) {
				panic("deadzone: seeded child region differs")
			}
		}
	} else {
		pn, size = c.floodFree(p, c.cbits, false)
	}
	// Boxes only leave, so the rows [blo, bhi] bound every later pass. A box
	// row r reads the push targets of rows r-1..r+1 and the fence mask (the
	// 8-neighbourhood of unreachable free cells) of the same rows.
	deadB := c.deadB
	for {
		R, ain, rm := c.cbits, c.ain, c.rm
		any := false
		for r := max(blo, 1); r <= bhi && r < H-1; r++ {
			B := bx[r]
			rm[r] = 0
			if B == 0 {
				continue
			}
			uP, u0, uN := F[r-1]&^R[r-1], F[r]&^R[r], F[r+1]&^R[r+1]
			fm := uP | uP<<1 | uP>>1 | u0 | u0<<1 | u0>>1 | uN | uN<<1 | uN>>1
			tP, t0, tN := F[r-1]&^deadB[r-1]&^ain[r-1], F[r]&^deadB[r]&^ain[r], F[r+1]&^deadB[r+1]&^ain[r+1]
			push := R[r]<<1&(t0>>1) | R[r]>>1&(t0<<1) | R[r-1]&tN | R[r+1]&tP
			rem := B & (^ain[r] | ^fm | push)
			if rem != 0 {
				rm[r] = rem
				bx[r] = B &^ rem
				live -= bits.OnesCount64(rem)
				any = true
			}
		}
		if !any {
			c.fmOK = false // fm covers only the box rows
			return pn, size, live
		}
		for j, y := range nxt {
			if y != 0 && rm[c.rowOf[y]]>>c.colOf[y]&1 != 0 {
				c.occ[y] = false
				nxt[j] = 0
				F[c.rowOf[y]] |= c.base[c.rowOf[y]] & (1 << c.colOf[y])
			}
		}
		c.fmOK = false
		pn, size = c.floodFree(p, c.cbits, true)
	}
}

// ringOK[m] (m = the 8 cells around a cell, clockwise from north) is true
// when the set cells at the four edge positions all lie in one arc of
// consecutive set ring cells: removing the centre then cannot split a
// 4-connected region that contained it.
var ringOK = func() (t [256]bool) {
	for m := range 256 {
		start, ok := -1, true
		for i := 0; i < 8; i += 2 {
			if m>>i&1 == 0 {
				continue
			}
			j := i // walk back to the start of the arc holding i
			for k := 0; k < 8 && m>>((j+7)&7)&1 != 0; k++ {
				j = (j + 7) & 7
			}
			if m == 255 {
				j = 0
			}
			if start >= 0 && j != start {
				ok = false
			}
			start = j
		}
		t[m] = ok
	}
	return
}()

// seedChild seeds c.cbits with the parent's region c.rbits adjusted for the
// child (player at p, the box that stood there gone; seed = the cell the box
// moved to, 0 if it left). The seed is a subset of the child's region when
// removing seed from the parent region cannot disconnect it; otherwise it
// reports false.
func (c *Checker) seedChild(p, seed int32) bool {
	R := c.rbits
	if seed != 0 {
		r, col := int(c.rowOf[seed]), uint(c.colOf[seed])
		if r == 0 || r == len(R)-1 || col == 0 {
			return false
		}
		if R[r]>>col&1 != 0 {
			u, m, d := R[r-1]>>(col-1)&7, R[r]>>(col-1)&7, R[r+1]>>(col-1)&7
			ring := u>>1&1 | u>>2&1<<1 | m>>2&1<<2 | d>>2&1<<3 | d>>1&1<<4 | d&1<<5 | m&1<<6 | u&1<<7
			if !ringOK[ring] {
				return false
			}
		}
	}
	copy(c.cbits, R)
	if seed != 0 {
		c.cbits[c.rowOf[seed]] &^= 1 << c.colOf[seed]
	}
	c.cbits[c.rowOf[p]] |= 1 << c.colOf[p]
	return true
}

// seedFinal reports whether the seed set in c.cbits already is the child's
// region: no free cell next to the player lies outside it, so the seed is
// closed. Then c.spn, c.ssz are its minimum cell and size.
func (c *Checker) seedFinal(p, seed int32) bool {
	if !c.pOK {
		return false
	}
	F, R := c.free, c.cbits
	for _, d := range c.b.dirs {
		u := p + d
		r, col := c.rowOf[u], c.colOf[u]
		if F[r]>>col&1 != 0 && R[r]>>col&1 == 0 {
			return false
		}
	}
	size, m := c.psz+1, c.pm
	if seed != 0 && c.rbits[c.rowOf[seed]]>>c.colOf[seed]&1 != 0 {
		size--
		if seed == m {
			return false
		}
	}
	c.spn, c.ssz = min(m, p), size
	return true
}

// fencing reports whether the box at y touches an unreachable free cell
// (c.cmark holds the child's reach).
func (c *Checker) fencing(y int32) bool {
	if c.bitsOK && c.fmOK {
		return c.inBits(c.fm, y)
	}
	b := c.b
	for _, d := range [8]int32{-b.W - 1, -b.W, -b.W + 1, -1, 1, b.W - 1, b.W, b.W + 1} {
		u := y + d
		if !b.Wall[u] && !c.wall2[u] && !c.occ[u] && !c.inChild(u) {
			return true
		}
	}
	return false
}

// Debug, when set, traces corrals and verdicts.
var Debug func(format string, args ...any)

// GroupCap (exp-248; 0: the checker's cap) bounds group-zone searches.
var GroupCap = 0

// BitFlood (exp-248): word-parallel region floods in zone searches.
var BitFlood = true

// FastKey (exp-248) keys corral verdicts by the real player region.
var FastKey = true

// Near restricts DeadAfter to corrals within two cells of the pushed box.
var Near = false

// sideways reports whether the box at x (behind a fence box, along d) is
// free to move twice across d: it need not stay to fence the corral.
func (c *Checker) sideways(occ []bool, x, d int32) bool {
	b := c.b
	free := func(y int32) bool { return !b.Wall[y] && !occ[y] }
	for _, e := range b.dirs {
		if e == d || e == -d {
			continue
		}
		if free(x+e) && free(x+2*e) && free(x-e) {
			return true
		}
	}
	return false
}

// near reports whether a cell of the current corral lies within two cells
// (Chebyshev) of t.
func (c *Checker) near(t int32) bool {
	w := c.b.W
	tx, ty := t%w, t/w
	for _, x := range c.cells {
		dx, dy := x%w-tx, x/w-ty
		if dx >= -2 && dx <= 2 && dy >= -2 && dy <= 2 {
			return true
		}
	}
	return false
}

// key hashes the zone, the fixed boxes and the reduced player region.
func (c *Checker) key(boxes []int32, pnorm int32) uint64 {
	h := uint64(pnorm)*0xD6E8FEB86659FD93 + uint64(len(c.zone))
	for _, z := range c.zone {
		h ^= c.zob[z]
	}
	for _, x := range boxes {
		if c.wall2[x] {
			h ^= c.zob[len(c.b.Wall)+int(x)]
		}
	}
	return (h ^ c.b.salt) &^ 3
}

// Bitset floods (exp-248): one uint64 per board row (boards up to 64 wide).
// free = the zone's static floor (no walls, no fixed walls) minus the given
// boxes; the region grows by neighbouring rows and within-row smearing.
func (c *Checker) setBase(boxes []int32) {
	if !c.bitsOK {
		return
	}
	// wall2 holds only fixed boxes (set in zoneDead)
	copy(c.base, c.baseStatic)
	for _, x := range boxes {
		if c.wall2[x] {
			c.base[c.rowOf[x]] &^= 1 << c.colOf[x]
		}
	}
}

// parentFlood / childFlood flood the reduced board holding boxes (live
// entries; 0 = removed) from p; inParent / inChild test membership.
func (c *Checker) parentFlood(p int32, boxes []int32) (int32, int) {
	if c.bitsOK {
		return c.floodBits(p, boxes, c.rbits, false)
	}
	m, n, rs := c.reach(p)
	c.prs = rs
	return m, n
}

func (c *Checker) childFlood(p int32, boxes []int32) (int32, int) {
	c.fmOK = false
	if c.bitsOK {
		return c.floodBits(p, boxes, c.cbits, false)
	}
	return c.childReach(p)
}

func (c *Checker) inParent(x int32) bool {
	if c.bitsOK {
		return c.inBits(c.rbits, x)
	}
	return c.rmark[x] == c.prs
}

func (c *Checker) inChild(x int32) bool {
	if c.bitsOK {
		return c.inBits(c.cbits, x)
	}
	return c.cmark[x] == c.cmst
}

// fenceMask: cells 8-adjacent to a free cell the child flood didn't reach
// (c.free and c.cbits from the last floodBits).
func (c *Checker) fenceMask() {
	c.fenceMaskRows(0, len(c.cbits)-1)
	c.fmOK = true
}

// fenceMaskRows computes c.fm for rows lo..hi (0 <= lo, hi < H) only.
func (c *Checker) fenceMaskRows(lo, hi int) {
	H := len(c.cbits)
	if len(c.fm) != H {
		c.fm, c.hm = make([]uint64, H), make([]uint64, H)
	}
	hlo, hhi := max(lo-1, 0), min(hi+1, H-1)
	for r := hlo; r <= hhi; r++ {
		u := c.free[r] &^ c.cbits[r]
		c.hm[r] = u | u<<1 | u>>1
	}
	for r := lo; r <= hi; r++ {
		m := c.hm[r]
		if r > 0 {
			m |= c.hm[r-1]
		}
		if r < H-1 {
			m |= c.hm[r+1]
		}
		c.fm[r] = m
	}
}

func (c *Checker) inBits(bits []uint64, x int32) bool {
	return bits[c.rowOf[x]]>>c.colOf[x]&1 != 0
}

func (c *Checker) floodBits(p int32, boxes []int32, out []uint64, seeded bool) (int32, int) {
	F := c.free
	copy(F, c.base)
	for _, x := range boxes {
		if x != 0 {
			F[c.rowOf[x]] &^= 1 << c.colOf[x]
		}
	}
	return c.floodFree(p, out, seeded)
}

// fillRow grows y inside the free cells f along the row.
func fillRow(y, f uint64) uint64 {
	for {
		z := (y | y<<1 | y>>1) & f
		if z == y {
			return y
		}
		y = z
	}
}

// floodFree floods c.free from p into out. Only the rows between the
// outermost reached rows (grown as the flood spreads) are swept: the others
// hold no reach and no neighbour of one, so a sweep skips them anyway.
func (c *Checker) floodFree(p int32, out []uint64, seeded bool) (int32, int) {
	return c.floodFreeMode(p, out, seeded, false)
}

// floodFreeMode: closed means out is already row-filled within c.free except
// possibly at p's row (the parent's region with p added), so the initial
// row-fill pass is skipped.
func (c *Checker) floodFreeMode(p int32, out []uint64, seeded, closed bool) (int32, int) {
	F := c.free
	H := len(out)
	prow := int(c.rowOf[p])
	lo, hi := prow, prow
	flo, fhi := H, -1 // closed mode: the rows holding the seed
	if seeded {
		// out holds an earlier, smaller reach of the same player: only
		// boxes were removed, so grow it
		lo, hi = H, -1
		for r, w := range out {
			if w != 0 {
				if !closed {
					out[r] = fillRow(w, F[r])
				}
				if lo == H {
					lo = r
				}
				hi = r
			}
		}
		if closed {
			// only p's row can grow first; the sweeps spread from it
			flo, fhi = lo, hi
			lo, hi = prow, prow
		}
	} else {
		clear(out)
		out[prow] = 1 << c.colOf[p]
	}
	// A sweep that changes nothing proves the fixpoint, so the loop stops
	// after any such sweep (no separate confirming pass).
	for {
		changed := false
		for r := max(lo-1, 1); r <= hi+1 && r < H-1; r++ {
			y := out[r] | (out[r-1]|out[r+1])&F[r]
			if y == out[r] && (r != prow || out[r] == 0) {
				continue
			}
			y = fillRow(y, F[r])
			if y != out[r] {
				out[r] = y
				changed = true
				lo, hi = min(lo, r), max(hi, r)
			}
		}
		if !changed {
			break
		}
		changed = false
		for r := min(hi+1, H-2); r >= 1 && r >= lo-1; r-- {
			y := out[r] | (out[r-1]|out[r+1])&F[r]
			if y == out[r] {
				continue
			}
			y = fillRow(y, F[r])
			out[r] = y
			changed = true
			lo, hi = min(lo, r), max(hi, r)
		}
		if !changed {
			break
		}
	}
	m, size := int32(-1), 0
	lo, hi = min(lo, flo), max(hi, fhi)
	for r := max(lo, 0); r <= hi && r < H; r++ {
		if w := out[r]; w != 0 {
			if m < 0 {
				m = int32(r)*c.b.W + int32(bits.TrailingZeros64(w))
			}
			size += bits.OnesCount64(w)
		}
	}
	return m, size
}

// reach floods the reduced board (walls, fixed boxes, c.occ) from p and
// returns its minimum cell and size; c.rmark holds the returned stamp.
func (c *Checker) reach(p int32) (int32, int, uint32) {
	return c.flood(p, c.rmark, c.stampR())
}

// childReach floods into c.cmark (keeps the parent's c.rmark intact).
func (c *Checker) childReach(p int32) (int32, int) {
	c.cmst++
	if c.cmst == 0 {
		clear(c.cmark)
		c.cmst = 1
	}
	m, n, _ := c.flood(p, c.cmark, c.cmst)
	return m, n
}

func (c *Checker) flood(p int32, mark []uint32, rs uint32) (int32, int, uint32) {
	b := c.b
	c.zq = append(c.zq[:0], p)
	mark[p] = rs
	m := p
	for i := 0; i < len(c.zq); i++ {
		for _, d := range b.dirs {
			if y := c.zq[i] + d; mark[y] != rs && !b.Wall[y] && !c.wall2[y] && !c.occ[y] {
				mark[y] = rs
				c.zq = append(c.zq, y)
				m = min(m, y)
			}
		}
	}
	return m, len(c.zq), rs
}

// frozenOff reports whether the box at x is frozen in a set holding a box off
// goal. frozen's *off is read only when frozen returned true: on a false
// result it can be set by boxes frozen only under the hypothesis that x is a
// wall (exp-553's over-report; exp-586 shows frozenOff itself is sound).
func (c *Checker) frozenOff(x int32) bool {
	off := false
	return c.frozen(x, &off) && off
}

func (c *Checker) frozen(x int32, off *bool) bool {
	c.fz[x] = true
	ok := c.blockedAxis(x, 1, off) && c.blockedAxis(x, c.b.W, off)
	c.fz[x] = false
	if ok && !c.b.Goal[x] {
		*off = true
	}
	return ok
}

func (c *Checker) blockedAxis(x, d int32, off *bool) bool {
	b := c.b
	l, r := x-d, x+d
	if b.Wall[l] || b.Wall[r] || c.wall2[l] || c.wall2[r] || c.fz[l] || c.fz[r] {
		return true
	}
	if b.Dead[l] && b.Dead[r] {
		return true
	}
	return c.occ[l] && c.frozen(l, off) || c.occ[r] && c.frozen(r, off)
}

// zoneDead searches the reduced problem of c.zone (fixed boxes are walls).
func (c *Checker) zoneDead(boxes []int32, player int32) bool {
	k := len(c.zone)
	slices.Sort(c.zone)
	for _, z := range c.zone {
		c.occ[z] = true
	}
	// Fixed boxes are walls of the reduced board only near the corral's
	// area; farther ones are removed (a relaxation, as Festival does), so
	// the cache key stays local.
	fixedN := 0
	for _, x := range boxes {
		if c.fixed[x] && (!LocalFixed || c.nearArea(x)) {
			c.wall2[x] = true
			fixedN++
		}
	}
	defer func() {
		for _, x := range boxes {
			c.wall2[x] = false
		}
	}()
	c.fixedN = fixedN
	open := c.floorN - fixedN - k // free cells of the reduced board
	// lookup: the cached verdict for key (hit), or the budget to search with.
	var key uint64
	var i uint64
	var slot *uint64
	capN := c.Cap
	var sh *Table
	if !c.Private {
		sh = Shared.Load()
	}
	lookup := func() (hit, dead bool) {
		i = key >> 2 & uint64(len(c.cache)-1)
		slot = &c.cache[i]
		if sh != nil && *slot&^3 != key {
			if v := sh.slots[key>>2&uint64(len(sh.slots)-1)].Load(); v&^3 == key && v&3 != 0 {
				c.Stats.CacheHits++
				*slot = v
				return true, v&3 == 1
			}
		}
		if *slot&^3 != key {
			c.cnt[i] = 0
			return false, false
		}
		c.Stats.CacheHits++
		if *slot&3 != 3 {
			return true, *slot&3 == 1
		}
		c.cnt[i]++
		switch c.cnt[i] {
		case 8:
			capN *= 4
		case 64:
			capN *= 16
		case 512:
			capN *= 64
		default:
			return true, false
		}
		c.Stats.Rechecks++
		return false, false
	}
	if FastKey {
		// Keyed by the player's real region (its minimum cell): every cell
		// of it lies in one reduced-board region, so the verdict is the
		// same; a hit needs no flood (exp-248).
		key = c.key(boxes, c.realMin)
		if hit, dead := lookup(); hit {
			for _, z := range c.zone {
				c.occ[z] = false
			}
			return dead
		}
	}
	c.setBase(boxes)
	p0, size := c.parentFlood(player, c.zone)
	for _, z := range c.zone {
		c.occ[z] = false
	}
	if size == open {
		c.Stats.Opened++
		if FastKey {
			*slot = key | 2
		}
		return false // nothing is fenced off once the other boxes are gone
	}
	if !FastKey {
		key = c.key(boxes, p0)
		if hit, dead := lookup(); hit {
			return dead
		}
	}
	c.Stats.Searches++
	res := c.search(k, p0, open, capN)
	if Debug != nil {
		Debug("  zone %v player %d -> %d (states %d)", c.zone, p0, res, c.Stats.States)
	}
	*slot = key | uint64(res)
	if sh != nil && res != 3 && !c.ReadOnly {
		sh.slots[key>>2&uint64(len(sh.slots)-1)].Store(key | uint64(res))
	}
	switch res {
	case 3:
		c.Stats.Unknown++
	}
	return res == 1
}

// search: BFS over zone positions; 1 dead, 2 alive, 3 unknown (cap).
func (c *Checker) search(k int, p0 int32, open, capN int) uint8 {
	b := c.b
	stride := k + 1
	c.arena = c.arena[:0]
	if Escape {
		if c.bitsOK {
			c.buildArea()
		}
		nxt := append(c.nxt[:0], c.zone...)
		for _, z := range nxt {
			c.occ[z] = true
		}
		pn, size, live := c.escape(nxt, p0, -1)
		for _, z := range nxt {
			c.occ[z] = false
		}
		c.nxt = nxt
		if size == c.floorN-c.fixedN-live || c.allGoals(nxt, -1, 0) {
			c.Stats.Opened++
			return 2
		}
		slices.Sort(nxt)
		for _, z := range nxt {
			c.arena = append(c.arena, int16(z))
		}
		c.arena = append(c.arena, int16(pn))
	} else {
		for _, z := range c.zone {
			c.arena = append(c.arena, int16(z))
		}
		c.arena = append(c.arena, int16(p0))
	}
	need := 1
	for need < 4*capN {
		need <<= 1
	}
	if len(c.slots) < need {
		c.slots = make([]int32, need)
	} else {
		clear(c.slots[:need])
	}
	slots := c.slots[:need]
	mask := uint64(need - 1)
	hashAt := func(st []int16) uint64 {
		h := uint64(0xCBF29CE484222325)
		for _, v := range st {
			h = (h ^ uint64(uint16(v))) * 0x100000001B3
		}
		return h ^ h>>29
	}
	insert := func(st []int16) bool { // st is the last state in the arena
		n := int32(len(c.arena)/stride - 1)
		for i := hashAt(st) & mask; ; i = (i + 1) & mask {
			if slots[i] == 0 {
				slots[i] = n + 1
				return true
			}
			o := int(slots[i]-1) * stride
			if slices.Equal(c.arena[o:o+stride], st) {
				return false
			}
		}
	}
	insert(c.arena[:stride])
	states := 1
	if cap(c.cur) < k {
		c.cur = make([]int32, k)
	}
	cur := c.cur[:k]
	defer func() { c.Stats.States += states }()
	for head := 0; head*stride < len(c.arena); head++ {
		o := head * stride
		for i := range k {
			cur[i] = int32(c.arena[o+i])
			if cur[i] == 0 {
				continue
			}
			c.occ[cur[i]] = true
		}
		p := int32(c.arena[o+k])
		c.pm, c.psz = c.parentFlood(p, cur)
		c.pOK = c.bitsOK
		for i := range k {
			x := cur[i]
			for _, d := range b.dirs {
				t := x + d
				if x == 0 || !c.inParent(x-d) || b.Wall[t] || c.wall2[t] || c.occ[t] || b.Dead[t] {
					continue
				}
				nxt := append(c.nxt[:0], cur...)
				gone := Kill && c.area[t] != c.zst // left the zone: removed (a relaxation)
				c.occ[x] = false
				if gone {
					nxt[i] = 0
				} else {
					nxt[i] = t
					c.occ[t] = true
				}
				if !gone && c.frozenOff(t) {
					c.occ[t], c.occ[x] = false, true
					continue
				}
				var pn int32
				var size int
				if !Escape {
					pn, size = c.childFlood(x, nxt)
				}
				live := 0
				if Escape {
					seedT := t
					if gone {
						seedT = 0
					}
					pn, size, live = c.escape(nxt, x, seedT)
				} else {
					for _, y := range nxt {
						if y != 0 {
							live++
						}
					}
				}
				if !Escape && Shrink {
					// boxes no longer fencing an unreachable cell leave too
					killed := false
					for j, y := range nxt {
						if y != 0 && (j == i || ShrinkAll) && !c.fencing(y) {
							c.occ[y] = false
							nxt[j] = 0
							live--
							killed = true
						}
					}
					if killed {
						pn, size = c.childFlood(x, nxt)
					}
				}
				done := size == c.floorN-c.fixedN-live || c.allGoals(nxt, -1, 0)
				for _, y := range nxt {
					c.occ[y] = false
				}
				for _, y := range cur {
					if y != 0 {
						c.occ[y] = true
					}
				}
				c.nxt = nxt
				if done {
					if Debug != nil {
						Debug("  opened by pushing %d->%d (dir %d): child %v player %d", x, t, d, nxt, pn)
					}
					for j := range k {
						c.occ[cur[j]] = false
					}
					c.occ[0] = false
					c.Stats.Opened++
					return 2
				}
				base := len(c.arena)
				c.arena = slices.Grow(c.arena, stride)[:base+stride]
				sortedState(c.arena[base:base+k], nxt, i)
				c.arena[base+k] = int16(pn)
				if insert(c.arena[base:]) {
					states++
					if states > capN {
						for j := range k {
							c.occ[cur[j]] = false
						}
						c.occ[0] = false
						return 3
					}
				} else {
					c.arena = c.arena[:base]
				}
			}
		}
		for i := range k {
			c.occ[cur[i]] = false
		}
	}
	return 1
}

// sortedState writes the boxes of nxt into st in ascending order. nxt is
// the parent's state (sorted, removed boxes 0) with the box at index i moved
// and further boxes removed, so the other entries are already in order.
func sortedState(st []int16, nxt []int32, i int) {
	z := 0
	for _, v := range nxt {
		if v == 0 {
			z++
		}
	}
	clear(st[:z])
	pos := z
	tv := nxt[i]
	placed := tv == 0
	for j, v := range nxt {
		if j == i || v == 0 {
			continue
		}
		if !placed && tv < v {
			st[pos] = int16(tv)
			pos++
			placed = true
		}
		st[pos] = int16(v)
		pos++
	}
	if !placed {
		st[pos] = int16(tv)
	}
}

func (c *Checker) allGoals(cur []int32, i int, t int32) bool {
	for j, x := range cur {
		if j == i {
			x = t
		}
		if x != 0 && !c.b.Goal[x] {
			return false
		}
	}
	return true
}

// Matching (exp-229) proves a position dead when, with fixed boxes as walls,
// the other boxes can't be matched to distinct free goals they can reach.
var Matching = true

func (c *Checker) matchFixed(boxes []int32) bool {
	b := c.b
	var key uint64
	any := false
	for _, x := range boxes {
		if c.fixed[x] {
			key ^= c.zob[x]
			any = true
		}
	}
	if !any {
		return true // the static matching already covers this
	}
	if c.goals == nil {
		for x, g := range b.Goal {
			if g && !b.Wall[x] {
				c.goals = append(c.goals, int32(x))
			}
		}
		c.gw = (len(c.goals) + 63) / 64
		c.reachFix = map[uint64][]uint64{}
		c.mOf = make([]int32, len(c.goals))
		c.mSeen = make([]uint64, c.gw)
	}
	R, ok := c.reachFix[key]
	if !ok {
		if len(c.reachFix) > 4096 {
			clear(c.reachFix)
		}
		n := len(b.Wall)
		R = make([]uint64, n*c.gw)
		free := func(x int32) bool { return !b.Wall[x] && !c.fixed[x] }
		for gi, g := range c.goals {
			if c.fixed[g] {
				continue
			}
			rs := c.stampR()
			c.q = append(c.q[:0], g)
			c.rmark[g] = rs
			for i := 0; i < len(c.q); i++ {
				y := c.q[i]
				R[int(y)*c.gw+gi>>6] |= 1 << (gi & 63)
				for _, d := range b.dirs {
					// a box on y-d pushed by a player on y-2d reaches y
					if x := y - d; c.rmark[x] != rs && free(x) && free(x-d) {
						c.rmark[x] = rs
						c.q = append(c.q, x)
					}
				}
			}
		}
		c.reachFix[key] = R
	}
	for i := range c.mOf {
		c.mOf[i] = -1
	}
	for _, x := range boxes {
		if c.fixed[x] {
			continue
		}
		clear(c.mSeen)
		if !c.augment(R, x) {
			return false
		}
	}
	return true
}

// augment finds an augmenting path for box cell x in the reach rows R: goals
// are tried in ascending order, each at most once per search (mSeen).
func (c *Checker) augment(R []uint64, x int32) bool {
	gw := c.gw
	row := R[int(x)*gw : int(x+1)*gw]
	for w, rw := range row {
		for {
			cand := rw &^ c.mSeen[w]
			if cand == 0 {
				break
			}
			bit := cand & -cand
			c.mSeen[w] |= bit
			gi := w<<6 | bits.TrailingZeros64(cand)
			if c.mOf[gi] < 0 || c.augment(R, c.mOf[gi]) {
				c.mOf[gi] = x
				return true
			}
		}
	}
	return false
}
