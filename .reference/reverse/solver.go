// Package reverse searches backwards: from the solved position (boxes on
// goals) it pulls boxes until they stand on their start squares with the
// player able to reach its start square. The pulls, reversed, are the pushes
// of a forward solution. Goal rooms that are hard to pack forward are usually
// easy to unpack backward, so this complements the forward strategies.
package reverse

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"runtime/metrics"
	"slices"
	"soksolv/explore"
	"soksolv/memtrace"
	"soksolv/soloracle"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/reach"
	"soksolv/trace"
)

const dirChars = "udlr"

// Stop asks a running search to give up at its next time check.
var Stop atomic.Bool

// LivePercent is the share of the memory limit this search may let the
// process's live heap reach before stopping. Portfolio guests use a lower
// share so they can't starve the main strategy.
var LivePercent = 70

// Budget is this search's memory budget in bytes, measured on its own data
// structures (trackedBytes). The portfolio sets it and raises it when other
// strategies finish. 0 means no budget (process-wide backstop only).
var Budget atomic.Uint64

// Tracked is this search's latest trackedBytes, for the portfolio's memory
// governor.
var Tracked atomic.Uint64

// ElasticMem, when set, allows reverse lanes to borrow from the centralized headroom token pool (exp-327).
var ElasticMem bool

// AcquireElastic, if non-nil, acquires tokens from the centralized headroom pool.
var AcquireElastic func(laneID int, bytes uint64) bool

// ReleaseElastic, if non-nil, releases tokens back to the centralized headroom pool.
var ReleaseElastic func(laneID int, bytes uint64)

// Result mirrors the forward solvers' result.
type Result struct {
	Solved   bool
	Reason   string
	Solution string
	Nodes    int
	Best     *meet.Target // with Lane.KeepBest: the state closest to the start, with its tail
	// With Lane.Closest: the most of those boxes' squares any stored state
	// covers, and whether some stored state has exactly those boxes.
	ClosestOn    int
	ClosestExact bool
}

type solver struct {
	br    *bitReach // nil for boards wider than 64 cells
	lv    *Level
	n     int
	dirs  [4]int32
	dist  []int32 // pushes needed to bring a box from some start square to the cell; -1 = unreachable
	start []bool  // start squares of the boxes
	// canReach[j*cells+c]: a box on start square j can be pushed (forwards) to
	// c, i.e. a box on c can be pulled back to start square j.
	canReach []bool
	// distStart[j*cells+c]: pushes from start square j to c (-1 = never);
	// startIdx[c]: j for start square c, else -1. With FreeStart, a box's
	// heuristic is its distance to the nearest start square no box holds,
	// rather than to the nearest start square (which another box may hold).
	distStart               []int32
	startIdx                []int32
	freeJ                   []int32 // scratch: unoccupied start squares
	isFree                  []bool  // CostOrder: isFree[j] iff freeJ holds j
	ordJ                    []int32 // CostOrder: ordJ[c*n:c*n+ordN[c]] start squares that reach c, nearest first
	ordN                    []int32
	boxCost                 []int32 // scratch: per box index of the expanded node
	hu, hv, hp, hway, hminv []int32 // matchCost scratch
	hused                   []bool
	pu, pv, pp              []int32 // saveMatch: the parent's duals and assignment
	matchOf                 []int32
	gseen                   []uint32
	gstamp                  uint32
	pruned                  int

	occ   []bool
	mark  []uint32
	mark2 []uint32
	stamp uint32
	queue []int32

	// node arena: states stored as bitsets over floor cells
	floorN        int        // floor cells (free cells = floorN - boxes)
	expandedTotal int64      // expansions so far (lockstep cell schedule)
	nodes         *nodeStore // node records: parent, player, box bitset (plain, or delta-coded with Compact)
	nw            int
	fidx          []int32 // cell -> floor index (-1 for walls)
	fcell         []int32 // floor index -> cell
	key, pkey     []uint64
	table         []uint64    // open addressing: 0 empty, else node id+1 (low 32) | hash fingerprint (high 32)
	q             bucketQueue // open nodes by h; FIFO per h = the old (h, id) heap order
	nq            bucketQueue // novel open nodes by h (exp-196)
	lastImpExp    int64       // expandedTotal at the last best-h improvement (exp-198)
	walkNext      int64
	wrng          uint64
	walkSteps     int64
	types         *explore.Types // type-based exploration (exp-197)
	typeTurn      int
	novSeen       [][]uint64
	novTurn       int
	cells         *cellQueue // with Lane.Cells: FESS-style scheduling over cells
	expanded      []uint64   // with cells: nodes already expanded (bitset)
	started       time.Time
	memLimit      uint64
	elasticHold   uint64 // exp-327: currently borrowed elastic bytes
	// memFull: the table cannot grow within the budget, so stop.
	memFull     bool
	key8        []int32     // per cell ordering key (see perturb); -1 = unreachable
	back, fwd   *meet.Table // perimeters: ours, the forward searches'
	meetPending []int32     // new nodes that hit the forward perimeter
	meetPosted  int
	metForward  bool // the solution came from a forward-posted meeting
	ln          Lane
	tr          *trace.Recorder
	// exp-707 (-rev-supply): the supply count's state; supU[id] = 0 not yet
	// evaluated, 1..127 U+1, 128+p not yet evaluated with provisional U = p
	// in its keys (-rev-supply-inherit); popMarked: the last pop was an entry
	// pushed back with its supply penalty (supMark).
	sup       *supply
	supU      []uint8
	popMarked bool
	provU     int32 // -rev-supply-inherit: the provisional U of the child being added
}

// Solve runs the backward search on one level's rows.
func Solve(rows []string, deadline time.Time, memLimit uint64) (Result, error) {
	return SolvePhase(rows, deadline, memLimit, 0)
}

// SolvePhase runs one search phase. Phase 0 is the plain greedy search;
// later phases (restarts after the previous phase filled its memory budget)
// perturb the heuristic per cell so that the search explores differently.
func SolvePhase(rows []string, deadline time.Time, memLimit uint64, phase int) (Result, error) {
	return SolveLane(rows, deadline, memLimit, phase, Lane{Free: FreeStart, Cells: RevCells, Trace: Trace})
}

// Lane configures one of several parallel backward searches, each with its
// own heuristic and trace.
type Lane struct {
	Index     int     // 0 or 1: its mailbox and memory slot
	Free      bool    // free-start-square heuristic (else nearest start square)
	Cells     bool    // FESS-style cell scheduling (cells = boxes on start squares)
	Match     bool    // exact min-cost box-to-start-square matching heuristic
	Share     float64 // wide mode: this lane's fixed share of the reverse budget
	KeepBest  bool    // return the best state with its tail in Result.Best
	Connected bool    // best state: lowest h among states whose player reaches every free cell
	Private   bool    // a helper search: no perimeters, no mailbox
	Novelty   bool    // a third queue of novel states (exp-196)
	Types     bool    // type-based exploration (exp-197)
	Walks     bool    // random-walk escape from plateaus (exp-198)
	MaxNodes  int64   // > 0: stop after this many expansions (a bounded probe, exp-252)
	Closest   []int32 // diagnostic (exp-253): report the stored state closest to these boxes in Result
	Root      int32   // > 0: only the solved position's player region holding cell Root-1 is a root
	Supply    int32   // exp-707: > 0 = supply-aware ordering with this weight (see supply.go)
	SupplyInh bool    // exp-707: children inherit the parent's U-1 as a provisional penalty
	NoMacros  bool    // exp-707 ablation: no placement macros at U = 0
	Live      *Live
	Trace     *trace.Recorder
	// exp-660: an outside lane's memory slot (factor sub-solves): the budget
	// comes from Mem and tracked bytes go to Used, not to the reverse lanes'.
	Mem, Used *atomic.Uint64
	// exp-744 -revive-root: the phase starts from these boxes and player cell (nil: the level's start).
	RootBoxes  []int32
	RootPlayer int32
}

// SolveLane runs one search phase in a lane.
func SolveLane(rows []string, deadline time.Time, memLimit uint64, phase int, ln Lane) (Result, error) {
	lv, err := parseLevel(rows)
	if err != nil {
		return Result{}, err
	}
	if ln.RootBoxes != nil {
		lv.Boxes, lv.Player = slices.Clone(ln.RootBoxes), ln.RootPlayer // exp-744: the phase's start is a posted board (same walls)
	}
	s := newSolver(lv)
	s.ln = ln
	s.tr = ln.Trace
	if ln.Supply > 0 && s.n <= 256 {
		s.sup = newSupply(s, ln.Supply)
	}
	defer func() {
		if ReleaseElastic != nil && s.elasticHold > 0 {
			ReleaseElastic(12+s.ln.Index, s.elasticHold)
			s.elasticHold = 0
		}
	}()
	if ln.Cells {
		s.cells = &cellQueue{}
	}
	s.started = time.Now()
	s.back, s.fwd = meet.Back.Load(), meet.Fwd.Load()
	if ln.Private {
		s.back, s.fwd = nil, nil
	}
	s.perturb(phase)
	return s.solve(deadline, memLimit), nil
}

// SolveToward runs a backward search whose "start" is a forward searcher's
// state (boxes, the player's cell, and the moves that reached it from the
// real start): it pulls from the solved position toward that state. The
// solution is the prefix plus the reversed pulls. Its states are ordinary
// backward states, so they also feed the meeting perimeter.
func SolveToward(rows []string, boxes []int32, player int32, prefix string, deadline time.Time, memLimit uint64, ln Lane) (Result, error) {
	lv, err := parseLevel(rows)
	if err != nil {
		return Result{}, err
	}
	lv.Boxes, lv.Player = slices.Clone(boxes), player
	s := newSolver(lv)
	s.ln = ln
	s.tr = ln.Trace
	if ln.Supply > 0 && s.n <= 256 {
		s.sup = newSupply(s, ln.Supply)
	}
	defer func() {
		if ReleaseElastic != nil && s.elasticHold > 0 {
			ReleaseElastic(12+s.ln.Index, s.elasticHold)
			s.elasticHold = 0
		}
	}()
	if ln.Cells {
		s.cells = &cellQueue{}
	}
	s.started = time.Now()
	s.back, s.fwd = meet.Back.Load(), meet.Fwd.Load()
	if ln.Private {
		s.back, s.fwd = nil, nil
	}
	s.perturb(0)
	res := s.solve(deadline, memLimit)
	if res.Solved && !s.metForward {
		res.Solution = prefix + res.Solution
	}
	return res, nil
}

// Live tracks which lanes of a multi-lane reverse portfolio are running (they
// run in parallel on their own cores; the count divides the memory budget).
type Live struct {
	mu     sync.Mutex
	joined []bool
	done   []bool
}

// NewLive makes a tracker for n lanes.
func NewLive(n int) *Live { return &Live{joined: make([]bool, n), done: make([]bool, n)} }

// Join marks lane l as running.
func (b *Live) Join(l int) {
	b.mu.Lock()
	b.joined[l] = true
	b.mu.Unlock()
}

// Done marks lane l finished for good.
func (b *Live) Done(l int) {
	b.mu.Lock()
	b.done[l] = true
	b.mu.Unlock()
}

// Active is the number of joined lanes still running (at least 1).
func (b *Live) Active() uint64 {
	if b == nil {
		return 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := uint64(0)
	for i := range b.done {
		if b.joined[i] && !b.done[i] {
			n++
		}
	}
	return max(n, 1)
}

// laneTracked holds each lane's tracked bytes; Tracked is their sum.
var laneTracked [4]atomic.Uint64 // lanes 0-2, 3: a private helper search

// budget is this lane's share of the strategy's memory budget.
func (s *solver) budget() uint64 {
	if s.ln.Mem != nil {
		return s.ln.Mem.Load()
	}
	b := Budget.Load()
	if s.ln.Share > 0 && b > 0 {
		return uint64(float64(b) * s.ln.Share) // wide mode: lanes on their own cores
	}
	if s.ln.Live == nil {
		return b
	}
	// Lanes share the budget: each gets what the others don't hold, extra
	// lanes at most a quarter (they run small phases so lane 0, usually far
	// larger, isn't pushed into a restart).
	others := laneTracked[0].Load() + laneTracked[1].Load() + laneTracked[2].Load() + laneTracked[3].Load() - laneTracked[s.ln.Index].Load()
	if !ElasticMem {
		if others >= b {
			return 1
		}
		if s.ln.Index > 0 {
			return min(b/4, b-others)
		}
		return b - others
	}

	// ElasticMem is true (exp-327):
	// reverse-b has base budget min(b/4, b-others) and can borrow extra bytes
	// above its base budget ONLY through acquireElastic.
	base := uint64(0)
	if others < b {
		if s.ln.Index > 0 {
			base = min(b/4, b-others)
		} else {
			base = b - others
		}
	}
	avail := base + s.elasticHold
	if avail == 0 {
		return 1
	}
	return avail
}

// matchCost is the minimum over assignments of boxes to distinct start
// squares of the summed ordering keys (8 x push distance + the phase's
// perturbation), by the Hungarian algorithm in O(n^3). Infeasible
// assignments cost unreachableCost per box.
func (s *solver) matchCost(boxes []int32) int32 {
	n, cells := s.n, int32(len(s.lv.Wall))
	if len(s.hu) != n+1 {
		s.hu, s.hv, s.hp, s.hway, s.hminv = make([]int32, n+1), make([]int32, n+1), make([]int32, n+1), make([]int32, n+1), make([]int32, n+1)
		s.hused = make([]bool, n+1)
	}
	const inf = 1 << 24
	u, v, p, way, minv, used := s.hu, s.hv, s.hp, s.hway, s.hminv, s.hused
	for i := range u {
		u[i], v[i], p[i], way[i] = 0, 0, 0, 0
	}
	cost := func(i, j int32) int32 {
		b := boxes[i-1]
		if d := s.distStart[(j-1)*cells+b]; d >= 0 {
			return d*8 + s.key8[b] - s.dist[b]*8
		}
		return inf
	}
	for i := int32(1); i <= int32(n); i++ {
		p[0] = i
		j0 := int32(0)
		for j := range minv {
			minv[j], used[j] = math.MaxInt32, false
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], int32(math.MaxInt32), int32(0)
			for j := int32(1); j <= int32(n); j++ {
				if !used[j] {
					if c := cost(i0, j) - u[i0] - v[j]; c < minv[j] {
						minv[j], way[j] = c, j0
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
	if t := -v[0]; t < inf {
		return t
	}
	return unreachableCost * int32(n)
}

// perturb sets the ordering key per cell: 8 x pull distance, plus for
// phase p > 0 a pseudo-random offset in [0, 8p) on cells off the start
// squares. A key of 0 still means "on a start square", so h == 0 keeps its
// meaning; phase 0 orders exactly like the unscaled distance.
func (s *solver) perturb(phase int) {
	s.key8 = make([]int32, len(s.dist))
	x := uint64(phase)*0x9E3779B97F4A7C15 + 0x2545F4914F6CDD1D
	for c, d := range s.dist {
		if d < 0 {
			s.key8[c] = -1
			continue
		}
		s.key8[c] = d * 8
		if phase > 0 && d > 0 {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			s.key8[c] += int32(x % uint64(8*phase))
		}
	}
}

func newSolver(lv *Level) *solver {
	size := len(lv.Wall)
	s := &solver{
		lv:    lv,
		n:     len(lv.Boxes),
		dirs:  [4]int32{-int32(lv.W), int32(lv.W), -1, 1},
		occ:   make([]bool, size),
		mark:  make([]uint32, size),
		mark2: make([]uint32, size),
		start: make([]bool, size),
		fidx:  make([]int32, size),
		table: make([]uint64, 1<<12),
	}
	s.br = newBitReach(lv)
	for _, b := range lv.Boxes {
		s.start[b] = true
	}
	// dist: forward push BFS from all start squares (a box at c can be pushed
	// to c+d if c-d and c+d are floor). Cells never reached can't hold a box
	// in any reverse state that leads back to the start.
	s.dist = make([]int32, size)
	for i := range s.dist {
		s.dist[i] = -1
	}
	if reach.Enabled {
		// Exact single-box reach with the player's access (exp-464).
		reach.New(lv.Wall, lv.W, lv.Boxes, lv.Player).Forward(lv.Boxes, lv.Player, s.dist)
	} else {
		q := []int32{}
		for _, b := range lv.Boxes {
			s.dist[b] = 0
			q = append(q, b)
		}
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, d := range s.dirs {
				if t := c + d; !lv.Wall[c-d] && !lv.Wall[t] && s.dist[t] < 0 {
					s.dist[t] = s.dist[c] + 1
					q = append(q, t)
				}
			}
		}
	}
	// Box bitsets index only cells a box can occupy in some reverse state
	// (dist >= 0): fewer words per stored state.
	for c := range lv.Wall {
		s.fidx[c] = -1
		if s.dist[c] >= 0 {
			s.fidx[c] = int32(len(s.fcell))
			s.fcell = append(s.fcell, int32(c))
		}
	}
	s.nw = (len(s.fcell) + 63) / 64
	for _, w := range lv.Wall {
		if !w {
			s.floorN++
		}
	}
	s.key = make([]uint64, s.nw)
	s.pkey = make([]uint64, s.nw)
	nb := make([]int32, 4*len(s.fcell))
	for f, c := range s.fcell {
		for d, dd := range s.dirs {
			nb[f*4+d] = -1
			if t := c + dd; t >= 0 && int(t) < len(lv.Wall) {
				nb[f*4+d] = s.fidx[t]
			}
		}
	}
	s.nodes = newNodeStore(s.nw, Compact, nb, len(s.fcell))
	cells := len(lv.Wall)
	s.canReach = make([]bool, s.n*cells)
	s.distStart = make([]int32, s.n*cells)
	s.startIdx = make([]int32, cells)
	for i := range s.startIdx {
		s.startIdx[i] = -1
	}
	for j, b := range lv.Boxes {
		s.startIdx[b] = int32(j)
		r := s.canReach[j*cells : (j+1)*cells]
		ds := s.distStart[j*cells : (j+1)*cells]
		for i := range ds {
			ds[i] = -1
		}
		r[b] = true
		ds[b] = 0
		q := []int32{b}
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, d := range s.dirs {
				if t := c + d; !lv.Wall[c-d] && !lv.Wall[t] && !r[t] {
					r[t] = true
					ds[t] = ds[c] + 1
					q = append(q, t)
				}
			}
		}
	}
	s.matchOf = make([]int32, s.n)
	s.gseen = make([]uint32, s.n)
	s.initCostOrder()
	return s
}

// saveMatch keeps the duals and assignment of the last matchCost (the
// expanded node's) for matchChild.
func (s *solver) saveMatch() {
	s.pu = append(s.pu[:0], s.hu...)
	s.pv = append(s.pv[:0], s.hv...)
	s.pp = append(s.pp[:0], s.hp...)
}

// matchChild is matchCost for the child where box i of cur moved to t,
// repaired from the parent's saved solution: unassign row i, reset its
// potential to its minimum reduced cost (the other rows' duals stay
// feasible and tight), and run one augmenting path: O(n^2), not O(n^3).
func (s *solver) matchChild(cur []int32, i int, t int32) int32 {
	n, cells := s.n, int32(len(s.lv.Wall))
	const inf = 1 << 24
	u, v, p := s.hu, s.hv, s.hp
	copy(u, s.pu)
	copy(v, s.pv)
	copy(p, s.pp)
	way, minv, used := s.hway, s.hminv, s.hused
	row := int32(i + 1)
	cell := func(r int32) int32 {
		if r == row {
			return t
		}
		return cur[r-1]
	}
	cost := func(r, j int32) int32 {
		b := cell(r)
		if d := s.distStart[(j-1)*cells+b]; d >= 0 {
			return d*8 + s.key8[b] - s.dist[b]*8
		}
		return inf
	}
	for j := 1; j <= n; j++ {
		if p[j] == row {
			p[j] = 0
		}
	}
	best := int32(math.MaxInt32)
	for j := int32(1); j <= int32(n); j++ {
		best = min(best, cost(row, j)-v[j])
	}
	u[row] = best
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
				if c := cost(i0, j) - u[i0] - v[j]; c < minv[j] {
					minv[j], way[j] = c, j0
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
	var total int32
	for j := int32(1); j <= int32(n); j++ {
		c := cost(p[j], j)
		if c >= inf {
			return unreachableCost * int32(n)
		}
		total += c
	}
	return total
}

// FreeStart selects the free-start-square heuristic (see distStart).
var FreeStart = false

// unreachableCost is a box's cost when no free start square can be reached
// (the matching check prunes such states when they are expanded).
const unreachableCost = 8 * 4096

// cost() scans each cell's start squares nearest first and stops at the
// first free one (exp-408), instead of every free start square across a
// cells-strided table (costScan, kept as the test reference).
// initCostOrder sorts, per cell, the start squares that reach it by distance.
func (s *solver) initCostOrder() {
	cells, n := len(s.lv.Wall), s.n
	s.isFree = make([]bool, n)
	s.ordJ = make([]int32, cells*n)
	s.ordN = make([]int32, cells)
	for c := range cells {
		row := s.ordJ[c*n : c*n : (c+1)*n]
		for j := range n {
			if s.distStart[j*cells+c] >= 0 {
				row = append(row, int32(j))
			}
		}
		slices.SortStableFunc(row, func(a, b int32) int {
			return int(s.distStart[int(a)*cells+c] - s.distStart[int(b)*cells+c])
		})
		s.ordN[c] = int32(len(row))
	}
}

// setFree lists the start squares not holding a box (per s.occ).
func (s *solver) setFree() {
	s.freeJ = s.freeJ[:0]
	clear(s.isFree)
	for j, b := range s.lv.Boxes {
		if !s.occ[b] {
			s.freeJ = append(s.freeJ, int32(j))
			s.isFree[j] = true
		}
	}
}

// cost is the ordering key of a box on c given the free start squares in
// s.freeJ: 0 on a start square, else 8 x the distance to the nearest free
// start square plus the phase's perturbation for c.
func (s *solver) cost(c int32) int32 {
	if s.startIdx[c] >= 0 {
		return 0
	}
	cells := int32(len(s.lv.Wall))
	best := int32(-1)
	for _, j := range s.ordJ[int(c)*s.n : int(c)*s.n+int(s.ordN[c])] {
		if s.isFree[j] {
			best = s.distStart[j*cells+c]
			break
		}
	}
	if best < 0 {
		return unreachableCost
	}
	return best*8 + s.key8[c] - s.dist[c]*8
}

// costScan is cost's original form: the nearest of the free start squares
// by a scan of all of them. Test reference.
func (s *solver) costScan(c int32) int32 {
	if s.startIdx[c] >= 0 {
		return 0
	}
	cells := int32(len(s.lv.Wall))
	best := int32(-1)
	for _, j := range s.freeJ {
		if d := s.distStart[j*cells+c]; d >= 0 && (best < 0 || d < best) {
			best = d
		}
	}
	if best < 0 {
		return unreachableCost
	}
	return best*8 + s.key8[c] - s.dist[c]*8
}

// stateCost sums cost over boxes (s.occ must hold them).
func (s *solver) stateCost(boxes []int32) int32 {
	s.setFree()
	var h int32
	for _, b := range boxes {
		h += s.cost(b)
	}
	return h
}

// pulled returns the pull that produced node id from its parent: the box
// moved from b to t. Derived from the two box bitsets (one bit differs each
// way), so no per-node move fields are stored.
func (s *solver) pulled(id int32) (b, t int32) {
	nw := s.nw
	p := s.nodeParent(id)
	if f, g, ok := s.nodes.Pulled(id); ok {
		return s.fcell[f], s.fcell[g]
	}
	cw, pw := s.nodes.Boxes(id, s.nodes.bufA), s.nodes.Boxes(p, s.nodes.bufB)
	for w := range nw {
		if x := pw[w] &^ cw[w]; x != 0 {
			b = s.fcell[w*64+bits.TrailingZeros64(x)]
		}
		if x := cw[w] &^ pw[w]; x != 0 {
			t = s.fcell[w*64+bits.TrailingZeros64(x)]
		}
	}
	return b, t
}

// inherits reports whether node id is certainly matchable because its parent
// was (parents are only expanded after passing the check): the pulled box
// moved from b to b+d, and every start square reachable from b is also
// reachable from b+d, so the parent's matching still works.
func (s *solver) inherits(id int32) bool {
	if s.nodeParent(id) < 0 {
		return false
	}
	cells := len(s.lv.Wall)
	b, t := s.pulled(id)
	for j := range s.n {
		if s.canReach[j*cells+int(b)] && !s.canReach[j*cells+int(t)] {
			return false
		}
	}
	return true
}

// feasible reports whether the boxes can be matched to distinct start squares
// each can be pulled back to (bipartite matching by augmenting paths). If not,
// the state can never lead back to the start position.
func (s *solver) feasible(boxes []int32) bool {
	cells := len(s.lv.Wall)
	for j := range s.matchOf {
		s.matchOf[j] = -1
	}
	var try func(i int32) bool
	try = func(i int32) bool {
		b := int(boxes[i])
		for j := range s.n {
			if s.gseen[j] == s.gstamp || !s.canReach[j*cells+b] {
				continue
			}
			s.gseen[j] = s.gstamp
			if s.matchOf[j] < 0 || try(s.matchOf[j]) {
				s.matchOf[j] = i
				return true
			}
		}
		return false
	}
	for i := range int32(len(boxes)) {
		s.gstamp++
		if s.gstamp == 0 {
			clear(s.gseen)
			s.gstamp = 1
		}
		if !try(i) {
			return false
		}
	}
	return true
}

// normalize returns the child's normalized player after pulling a box from b
// to p (s.occ holds the child; the parent's region R is stamped st in s.mark
// with minimum minR, and the player now stands in R). If b adds no new area
// (its other neighbors are walls, boxes or in R) and blocking p can't split R
// (p's free neighbors stay connected through its 8-cell ring), the region is
// R + b - p and its minimum is min(minR, b) without a flood fill.
func (s *solver) normalize(b, p, minR int32, st uint32) int32 {
	lv := s.lv
	for _, d := range s.dirs {
		if x := b + d; x != p && !lv.Wall[x] && !s.occ[x] && s.mark[x] != st {
			return s.regionMin(b, p)
		}
	}
	if p == minR || !s.ringConnected(p) {
		return s.regionMin(b, p)
	}
	return min(minR, b)
}

// regionMin is the minimum cell of the player's region after pulling a box
// from b to p, the player now standing at p+(p-b).
func (s *solver) regionMin(b, p int32) int32 {
	if br := s.br; br != nil && br.armed {
		if !br.built {
			// The rows hold the parent's boxes: undo this pull's two toggles.
			s.occ[b], s.occ[p] = true, false
			br.build(s.occ)
			s.occ[b], s.occ[p] = false, true
		}
		return br.minReach(p+(p-b), b, p)
	}
	return s.reach(p+(p-b), s.mark2)
}

// ringConnected reports whether all free orthogonal neighbors of c lie in one
// contiguous run of free cells on the 8-cell ring around c.
func (s *solver) ringConnected(c int32) bool {
	w := int32(s.lv.W)
	ring := [8]int32{c - w, c - w + 1, c + 1, c + w + 1, c + w, c + w - 1, c - 1, c - w - 1}
	free := func(x int32) bool { return !s.lv.Wall[x] && !s.occ[x] }
	start := -1
	for i, x := range ring {
		if !free(x) {
			start = i
			break
		}
	}
	if start < 0 {
		return true
	}
	runs := 0
	inRun, runHasOrth := false, false
	for k := 1; k <= 8; k++ {
		i := (start + k) % 8
		if free(ring[i]) {
			inRun = true
			runHasOrth = runHasOrth || i%2 == 0
			continue
		}
		if inRun && runHasOrth {
			runs++
		}
		inRun, runHasOrth = false, false
	}
	return runs <= 1
}

// reach marks the player's region from p (per s.occ) and returns its minimum cell.
func (s *solver) reach(p int32, mark []uint32) int32 {
	s.stamp++
	st := s.stamp
	wall, occ, dirs := s.lv.Wall, s.occ, s.dirs
	q := append(s.queue[:0], p)
	mark[p] = st
	m := p
	for i := 0; i < len(q); i++ {
		x := q[i]
		for _, d := range dirs {
			if y := x + d; mark[y] != st && !wall[y] && !occ[y] {
				mark[y] = st
				q = append(q, y)
				m = min(m, y)
			}
		}
	}
	s.queue = q
	return m
}

func hash(boxes []uint64, player int32) uint64 {
	h := uint64(player)*0x9E3779B97F4A7C15 + 1
	for _, b := range boxes {
		h = (h ^ b) * 0xBF58476D1CE4E5B9
		h ^= h >> 31
	}
	return h
}

func (s *solver) find(boxes []uint64, player int32) (int, int32, uint32) {
	mask := len(s.table) - 1
	fp := uint32(hash(boxes, player) >> 32)
	// The slot is a function of the fingerprint, so grow rehashes from the
	// table entries without reading the node store.
	for i := int(fp) & mask; ; i = (i + 1) & mask {
		e := s.table[i]
		if e == 0 {
			return i, -1, fp
		}
		if uint32(e>>32) == fp {
			// One record holds the player and the boxes.
			id := int32(uint32(e)) - 1
			if s.nodes.Player(id) == player && slices.Equal(s.nodes.Boxes(id, s.nodes.bufA), boxes) {
				return i, id, fp
			}
		}
	}
}

func (s *solver) grow() {
	old := s.table
	s.table = make([]uint64, 2*len(old))
	mask := len(s.table) - 1
	for _, e := range old {
		if e == 0 {
			continue
		}
		i := int(uint32(e>>32)) & mask
		for s.table[i] != 0 {
			i = (i + 1) & mask
		}
		s.table[i] = e
	}
}

// Node record accessors (one record per node: see solver.nodes).
func (s *solver) nodePlayer(id int32) int32 { return s.nodes.Player(id) }
func (s *solver) nodeParent(id int32) int32 { return s.nodes.Parent(id) }

// unpack fills cur with node id's boxes in ascending cell order.
func (s *solver) unpack(id int32, cur []int32) {
	i := 0
	for w, x := range s.nodes.Boxes(id, s.nodes.bufU) {
		for x != 0 {
			cur[i] = s.fcell[w*64+bits.TrailingZeros64(x)]
			i++
			x &= x - 1
		}
	}
}

func (s *solver) add(boxes []int32, player, parent, from int32, dir int8, h int32) int32 {
	clear(s.key)
	for _, b := range boxes {
		f := s.fidx[b]
		s.key[f>>6] |= 1 << (f & 63)
	}
	return s.addKeyed(boxes, player, parent, from, dir, h)
}

// addKeyed is add with s.key already holding the bitset of boxes.
func (s *solver) addKeyed(boxes []int32, player, parent, from int32, dir int8, h int32) int32 {
	slot, id, fp := s.find(s.key, player)
	if id >= 0 || s.memFull {
		return id
	}
	id = int32(s.nodes.Len())
	if soloracle.On {
		soloracle.Probe("B-rev"+strconv.Itoa(s.ln.Index), boxes, player)
	}
	s.table[slot] = uint64(id+1) | uint64(fp)<<32
	fromF := int32(0)
	if parent >= 0 {
		fromF = s.fidx[from]
	}
	s.nodes.Append(parent, player, s.key, fromF, dir)
	s.q.push(h, id)
	k := 0 // boxes on start squares, counted once for the queues below
	if Types || s.ln.Types || Novelty || s.ln.Novelty || s.cells != nil {
		for _, b := range boxes {
			if s.start[b] {
				k++
			}
		}
	}
	if Types || s.ln.Types {
		if s.types == nil {
			s.types = explore.NewTypes()
		}
		s.types.Push(uint64(h)<<16|uint64(k), id, h)
	}
	if Novelty || s.ln.Novelty {
		// Best-first width search's novelty (exp-196): a state with a box on
		// a square no box has stood on before, among states with as many
		// boxes back home, also enters the novelty queue (every third pop).
		for len(s.novSeen) <= k {
			s.novSeen = append(s.novSeen, nil)
		}
		if s.novSeen[k] == nil {
			s.novSeen[k] = make([]uint64, (len(s.lv.Wall)+63)/64)
		}
		seen, novel := s.novSeen[k], false
		for _, b := range boxes {
			if w, bit := b/64, uint64(1)<<(b%64); seen[w]&bit == 0 {
				seen[w] |= bit
				novel = true
			}
		}
		if novel {
			s.nq.push(h, id)
		}
	}
	if s.provU > 0 {
		s.setSupU(id, s.provU+127)
		k = max(0, k-int(s.provU))
	}
	if s.cells != nil {
		s.cells.push(k, h, id)
	}
	if s.back != nil {
		k := s.back.Key(boxes, player)
		s.back.Add(k)
		if s.fwd != nil && s.meetPosted < maxMeetPosts && s.fwd.Has(k) {
			s.meetPending = append(s.meetPending, id)
		}
	}
	if s.nodes.Len()*4 >= len(s.table)*3 {
		// Doubling allocates the new table while the old one is live. If that
		// would pass the budget, keep filling the current table up to 7/8
		// load (fingerprints keep probes cheap), then stop at the next check:
		// an RSS overshoot gets the whole process (every strategy) killed.
		needed := s.trackedBytes() + uint64(len(s.table))*16
		b := s.budget()
		if b > 0 && needed > b {
			if ElasticMem && s.ln.Index > 0 && AcquireElastic != nil && !mappedOver(s.memLimit, 75) {
				delta := needed - b
				if AcquireElastic(12+s.ln.Index, delta) {
					s.elasticHold += delta
					s.grow()
					return id
				}
			}
			s.memFull = s.nodes.Len()*8 >= len(s.table)*7
		} else {
			s.grow()
		}
	}
	return id
}

// Trace, if set, receives this search's progress (diagnostics only). Its h
// is the pull distance back to the start squares; packed counts boxes on
// start squares.
var Trace *trace.Recorder

func (s *solver) solve(deadline time.Time, memLimit uint64) (res Result) {
	s.memLimit = memLimit
	expanded := 0
	if len(s.ln.Closest) > 0 {
		defer func() {
			on := make([]bool, len(s.lv.Wall))
			for _, b := range s.ln.Closest {
				on[b] = true
			}
			cur := make([]int32, s.n)
			for id := range int32(s.nodes.Len()) {
				s.unpack(id, cur)
				k := 0
				for _, b := range cur {
					if on[b] {
						k++
					}
				}
				res.ClosestOn = max(res.ClosestOn, k)
				res.ClosestExact = res.ClosestExact || k == len(cur)
			}
		}()
	}
	defer func() {
		s.tr.SetExtra("pruned_matching", s.pruned)
		if sp := s.sup; sp != nil {
			s.tr.SetExtra("supply_evals", sp.evals)
			s.tr.SetExtra("supply_deferred", sp.deferred)
			s.tr.SetExtra("supply_misses", sp.misses)
			s.tr.SetExtra("supply_macros", sp.macros)
			s.tr.SetExtra("supply_macro_nodes", sp.macroOK)
			s.tr.SetExtra("supply_graphs", sp.graphs)
		}
		s.tr.Finish(trace.Ended(res.Solved, res.Reason), expanded, s.nodes.Len(), s.trackedBytes())
		if s.ln.Used != nil {
			s.ln.Used.Store(0)
		} else {
			laneTracked[s.ln.Index].Store(0)
		}
	}()
	lv := s.lv
	goals := []int32{}
	for c, g := range lv.Goal {
		if g {
			goals = append(goals, int32(c))
		}
	}
	var h0 int32
	for _, g := range goals {
		if s.dist[g] < 0 {
			return Result{Reason: "goal unreachable from start squares"}
		}
		h0 += s.key8[g]
	}
	// Roots: boxes on goals, one per player region.
	for _, g := range goals {
		s.occ[g] = true
	}
	if s.ln.Match {
		h0 = s.matchCost(goals)
	} else if s.ln.Free {
		h0 = s.stateCost(goals)
	}
	s.tr.Root(int(h0))
	s.stamp++
	seenRegion := s.stamp
	regionMark := make([]uint32, len(lv.Wall))
	for c := range int32(len(lv.Wall)) {
		if lv.Wall[c] || s.occ[c] || regionMark[c] == seenRegion {
			continue
		}
		p := s.reach(c, s.mark)
		for _, x := range s.queue {
			regionMark[x] = seenRegion
		}
		if s.ln.Root > 0 && s.mark[s.ln.Root-1] != s.stamp {
			continue
		}
		s.add(goals, p, -1, -1, -1, h0)
	}
	for _, g := range goals {
		s.occ[g] = false
	}

	cur := make([]int32, s.n)
	child := make([]int32, s.n)
	var lastMem time.Time
	var lastPub time.Time
	pubID := int32(-1)
	bestID, bestH := int32(-1), int32(0)
	plainID, plainH := int32(-1), int32(0) // lowest key among expanded states (debug dump; exp-707)
	if s.ln.KeepBest {
		defer func() {
			if !res.Solved && bestID >= 0 {
				boxes := make([]int32, s.n)
				s.unpack(bestID, boxes)
				res.Best = &meet.Target{Boxes: boxes, Player: s.nodePlayer(bestID), Tail: s.tail(bestID), H: bestH}
			}
		}()
	}
	if FXEnd != nil {
		defer func() {
			FXEnd(s.ln.Index, s.lv, s.nodes.Len(), s.unpack, s.n, bestID, bestH, s.expandedTotal)
		}()
	}
	if debugBest {
		defer func() { s.dumpBest(plainID, plainH) }()
	}
	for s.q.n > 0 {
		if s.memFull {
			return Result{Reason: "memory", Nodes: expanded}
		}
		if (Walks || s.ln.Walks) && bestID >= 0 && s.expandedTotal-s.lastImpExp > WalkAfter && s.expandedTotal >= s.walkNext {
			s.walkNext = s.expandedTotal + WalkAfter
			s.randomWalks(bestID)
		}
		id, h := s.popNext()
		if id < 0 {
			break
		}
		supEval := false // exp-707: evaluate U at this first selection
		prov, curU := int32(0), int32(0)
		if s.sup != nil {
			u := int32(0)
			if int(id) < len(s.supU) {
				u = int32(s.supU[id])
			}
			switch {
			case s.popMarked: // the penalized entry: back to the plain key
				curU = u - 1
				if h < maxBuckets-2 {
					h -= curU * s.sup.w * 8
				} else {
					h = maxBuckets - 1 // clamped: recompute below
				}
			case u >= 128: // provisional penalty in the key
				prov, supEval = u-128, true
				if h < maxBuckets-2 {
					h -= prov * s.sup.w * 8
				} else {
					h = maxBuckets - 1
				}
			case u > 1: // a stale entry of a node pushed back with its penalty
				s.unexpand(id)
				continue
			case u == 0:
				supEval = true
			}
		}
		s.unpack(id, cur)
		for _, b := range cur {
			s.occ[b] = true
		}
		if h == maxBuckets-1 { // the shared overflow bucket: recompute h exactly
			if s.ln.Match {
				h = s.matchCost(cur)
			} else if s.ln.Free {
				h = s.stateCost(cur)
			} else {
				h = 0
				for _, b := range cur {
					h += s.key8[b]
				}
			}
		}
		s.reach(s.nodePlayer(id), s.mark)
		st := s.stamp
		if h == 0 && s.mark[lv.Player] == st {
			for _, b := range cur {
				s.occ[b] = false
			}
			return Result{Solved: true, Solution: s.reconstruct(id), Nodes: expanded}
		}
		if h > 0 && !s.inherits(id) && !s.feasible(cur) {
			s.pruned++
			for _, b := range cur {
				s.occ[b] = false
			}
			continue
		}
		supZero := false // exp-707: U = 0 with boxes off: add placement macros
		if supEval && h > 0 && h < maxBuckets-1 {
			u, k := s.supplyU(cur, s.nodePlayer(id))
			u = min(u, 126)
			s.setSupU(id, u)
			curU = u
			if u > prov {
				s.sup.deferred++
				nh := min(h+u*s.sup.w*8, maxBuckets-2)
				s.q.push(nh, id|supMark)
				if s.cells != nil {
					s.cells.push(max(0, k-int(u)), nh, id|supMark)
				}
				s.unexpand(id)
				for _, b := range cur {
					s.occ[b] = false
				}
				continue
			}
			supZero = u == 0 && k < s.n
		}
		if s.sup == nil || !s.ln.SupplyInh {
			curU = 0
		}
		expanded++
		if plainID < 0 || h < plainH {
			plainID, plainH = id, h
		}
		// With Lane.Connected, only states whose player can reach every free
		// cell count as best: the lowest-h states are often traps with the
		// player walled off, useless as a forward target (exp-177).
		if (bestID < 0 || h < bestH) && (!s.ln.Connected || len(s.queue) == s.floorN-s.n) {
			bestID, bestH = id, h
			s.lastImpExp = s.expandedTotal
		}
		if s.tr != nil {
			packed := 0
			for _, b := range cur {
				if s.start[b] {
					packed++
				}
			}
			s.tr.Step(expanded, int(h), packed)
		}
		s.expandedTotal++
		if NodeBudget > 0 && s.expandedTotal >= NodeBudget || s.ln.MaxNodes > 0 && s.expandedTotal >= s.ln.MaxNodes {
			return Result{Reason: "timeout", Nodes: expanded} // deterministic screening
		}
		periodic := expanded&255 == 0
		if periodic {
			now := time.Now()
			if now.After(deadline) || Stop.Load() {
				return Result{Reason: "timeout", Nodes: expanded}
			}
			if sol, ok := s.checkMail(); ok {
				return Result{Solved: true, Solution: sol, Nodes: expanded}
			}
			if bestID != pubID && now.Sub(lastPub) > 100*time.Millisecond && s.back != nil {
				// Offer the state closest to the start as a bridge target.
				lastPub, pubID = now, bestID
				boxes := make([]int32, s.n)
				s.unpack(bestID, boxes)
				meet.StoreBest(s.ln.Index, &meet.Target{Boxes: boxes, Player: s.nodePlayer(bestID), Tail: s.tail(bestID), H: bestH})
			}
			if now.Sub(lastMem) > 50*time.Millisecond {
				lastMem = now
				if trace.Detail {
					s.tr.Sample(expanded, s.nodes.Len(), s.trackedBytes())
				}
				tb := s.trackedBytes()
				if s.ln.Used != nil {
					s.ln.Used.Store(tb)
				} else {
					laneTracked[s.ln.Index].Store(tb)
				}
				if memtrace.Enabled {
					memtrace.Publish(fmt.Sprintf("reverse-L%d", s.ln.Index), s.nodes.Len(), s.memBreakdown(), nil)
				}
				if s.ln.Used == nil {
					Tracked.Store(laneTracked[0].Load() + laneTracked[1].Load() + laneTracked[2].Load() + laneTracked[3].Load())
				}
				b := s.budget()
				backstopPct := uint64(92)
				if ElasticMem {
					backstopPct = 76
				}
				if b > 0 && tb > b || mappedOver(memLimit, backstopPct) {
					if ElasticMem && s.ln.Index > 0 && s.ln.Mem == nil && AcquireElastic != nil && tb > b && !mappedOver(memLimit, 75) {
						delta := tb - b
						if AcquireElastic(12+s.ln.Index, delta) {
							s.elasticHold += delta
							goto memOk
						}
					}
					return Result{Reason: "memory", Nodes: expanded}
				}
			memOk:
			}
		}
		if s.ln.Match {
			s.matchCost(cur) // the parent's assignment and duals, for matchChild
			s.saveMatch()
		}
		if s.ln.Free && !s.ln.Match {
			s.setFree()
			s.boxCost = s.boxCost[:0]
			for _, b := range cur {
				s.boxCost = append(s.boxCost, s.cost(b))
			}
		}
		if s.br != nil {
			s.br.armed, s.br.built = true, false
		}
		// Pull: player on b+d (reachable) steps to b+2d (free), box b -> b+d.
		// A child's key is the parent's with one bit moved.
		clear(s.pkey)
		for _, b := range cur {
			f := s.fidx[b]
			s.pkey[f>>6] |= 1 << (f & 63)
		}
		for i, b := range cur {
			for di, d := range s.dirs {
				p, p2 := b+d, b+2*d
				if s.mark[p] != st || lv.Wall[p2] || s.occ[p2] || s.dist[p] < 0 {
					continue
				}
				s.occ[b], s.occ[p] = false, true
				np := s.normalize(b, p, s.nodePlayer(id), st)
				// child = cur with box i moved to p, kept ascending: shift
				// the entries between its old and new positions.
				copy(child, cur)
				j := i
				for j+1 < len(child) && child[j+1] < p {
					child[j] = child[j+1]
					j++
				}
				for j > 0 && child[j-1] > p {
					child[j] = child[j-1]
					j--
				}
				child[j] = p
				copy(s.key, s.pkey)
				fb, fp := s.fidx[b], s.fidx[p]
				s.key[fb>>6] &^= 1 << (fb & 63)
				s.key[fp>>6] |= 1 << (fp & 63)
				ch := h - s.key8[b] + s.key8[p]
				if s.ln.Match {
					ch = s.matchChild(cur, i, p)
				} else if s.ln.Free {
					if s.startIdx[b] < 0 && s.startIdx[p] < 0 {
						ch = h - s.boxCost[i] + s.cost(p) // same free start squares
					} else {
						// The child's free start squares are the parent's with
						// b's square freed and p's occupied.
						jb, jp := s.startIdx[b], s.startIdx[p]
						if jb >= 0 {
							s.isFree[jb] = true
						}
						if jp >= 0 {
							s.isFree[jp] = false
						}
						ch = 0
						for _, x := range child {
							ch += s.cost(x)
						}
						if jb >= 0 {
							s.isFree[jb] = false
						}
						if jp >= 0 {
							s.isFree[jp] = true
						}
					}
				}
				if curU > 1 && s.startIdx[b] < 0 {
					// Pulling an off-start box lowers U by at most 1.
					s.provU = curU - 1
					ch += s.provU * s.sup.w * 8
				}
				s.addKeyed(child, np, id, b, int8(di), ch)
				s.provU = 0
				s.occ[b], s.occ[p] = true, false
			}
		}
		if s.br != nil {
			s.br.armed = false
		}
		if supZero && !s.ln.NoMacros {
			s.placementMacros(id, cur, child)
		}
		for _, b := range cur {
			s.occ[b] = false
		}
		if len(s.meetPending) > 0 {
			s.postMeetings(cur)
		}
	}
	return Result{Reason: "exhausted", Nodes: expanded}
}

// reconstruct turns the pulls from a root (solved position) to node id (the
// start position) into a forward LURD solution: walking the chain from id to
// the root lists the pulls in reverse, and each pull of a box from b to b+d
// is, forwards, a push from b+d in direction -d.
func (s *solver) reconstruct(id int32) string {
	return s.reconstructFrom(id, s.lv.Boxes, s.lv.Player)
}

// maxMeetPosts caps the meetings this search posts.
const maxMeetPosts = 2000

// tail lists the pushes from node id's state to the solved position: the
// pulls on its chain to the root, reversed.
func (s *solver) tail(id int32) []meet.Push {
	opposite := [4]int8{1, 0, 3, 2}
	var out []meet.Push
	for ; s.nodeParent(id) >= 0; id = s.nodeParent(id) {
		b, t := s.pulled(id)
		for k, dd := range s.dirs {
			if b+dd == t {
				out = append(out, meet.Push{From: t, Dir: opposite[k]})
			}
		}
	}
	return out
}

// postMeetings sends new nodes that hit the forward perimeter, with their
// tails, to the forward searches.
func (s *solver) postMeetings(cur []int32) {
	for _, id := range s.meetPending {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		s.meetPosted++
		s.unpack(id, cur)
		s.fwd.Post(meet.Meeting{Boxes: slices.Clone(cur), Player: s.nodePlayer(id), Tail: s.tail(id), From: "reverse"})
	}
	s.meetPending = s.meetPending[:0]
}

// checkMail resolves meetings posted by the forward searches: a posted state
// that is in this search's table joins the forward path to this search's
// pushes from that state to the solved position.
func (s *solver) checkMail() (string, bool) {
	if s.back == nil {
		return "", false
	}
	for {
		m, ok := s.back.Receive(s.ln.Index)
		if !ok {
			return "", false
		}
		if len(m.Boxes) != s.n {
			continue
		}
		key := make([]uint64, s.nw)
		for _, b := range m.Boxes {
			f := s.fidx[b]
			if f < 0 {
				ok = false
				break
			}
			key[f>>6] |= 1 << (f & 63)
		}
		if !ok {
			continue
		}
		if _, id, _ := s.find(key, m.Player); id >= 0 {
			s.back.Confirmed.Add(1)
			s.metForward = true
			s.tr.SetExtra("met", m.From)
			s.tr.SetExtra("met_lane", m.Lane) // exp-545 diagnostic
			s.tr.SetExtra("met_owner", m.Owner)
			tl := s.tail(id)
			if tail := meet.Complete(s.lv.Wall, s.lv.W, m.Boxes, m.At, tl); tail != "" || len(tl) == 0 {
				return m.Prefix + tail, true
			}
		}
	}
}

// reconstructFrom turns the pulls from a root (solved position) to node id
// into forward LURD moves starting from the given boxes and player cell
// (node id's state, with the player anywhere in its region).
func (s *solver) reconstructFrom(id int32, startBoxes []int32, startPlayer int32) string {
	opposite := [4]int32{1, 0, 3, 2}
	type push struct{ from, dir int32 }
	var pushes []push
	for ; s.nodeParent(id) >= 0; id = s.nodeParent(id) {
		b, t := s.pulled(id)
		d := int32(0)
		for k, dd := range s.dirs {
			if b+dd == t {
				d = int32(k)
			}
		}
		pushes = append(pushes, push{t, opposite[d]})
	}
	lv := s.lv
	for i := range s.occ {
		s.occ[i] = false
	}
	for _, b := range startBoxes {
		s.occ[b] = true
	}
	prev := make([]int8, len(lv.Wall))
	var out []byte
	p := startPlayer
	for _, pu := range pushes {
		target := pu.from - s.dirs[pu.dir]
		s.stamp++
		st := s.stamp
		q := append(s.queue[:0], p)
		s.mark[p] = st
		for i := 0; i < len(q) && s.mark[target] != st; i++ {
			c := q[i]
			for k, d := range s.dirs {
				if nb := c + d; s.mark[nb] != st && !lv.Wall[nb] && !s.occ[nb] {
					s.mark[nb] = st
					prev[nb] = int8(k)
					q = append(q, nb)
				}
			}
		}
		s.queue = q
		var walk []byte
		for c := target; c != p; c -= s.dirs[prev[c]] {
			walk = append(walk, dirChars[prev[c]])
		}
		slices.Reverse(walk)
		out = append(out, walk...)
		out = append(out, dirChars[pu.dir]-'a'+'A')
		s.occ[pu.from], s.occ[pu.from+s.dirs[pu.dir]] = false, true
		p = pu.from
	}
	for i := range s.occ {
		s.occ[i] = false
	}
	return string(out)
}

// bucketQueue pops open nodes by smallest h, and within one h in the order
// they were pushed. Node ids are assigned in increasing order as nodes are
// pushed, so this is exactly the order of a binary heap on (h << 32 | id),
// with O(1) operations and 4 bytes per entry instead of 8.
type bucketQueue struct {
	b    [][]int32 // per h (h >= maxBuckets shares the last bucket)
	head []int32   // per bucket: next entry to pop
	min  int       // no bucket below min holds entries
	n    int
}

const maxBuckets = 1 << 16

func (q *bucketQueue) push(h, id int32) {
	k := min(int(h), maxBuckets-1)
	if k >= len(q.b) {
		nb := max(k+1, 2*len(q.b))
		q.b = append(q.b, make([][]int32, nb-len(q.b))...)
		q.head = append(q.head, make([]int32, nb-len(q.head))...)
	}
	q.b[k] = append(q.b[k], id)
	if q.n == 0 || k < q.min {
		q.min = k
	}
	q.n++
}

func (q *bucketQueue) pop() (int32, int32) {
	for int(q.head[q.min]) == len(q.b[q.min]) {
		q.b[q.min], q.head[q.min] = q.b[q.min][:0], 0
		q.min++
	}
	k := q.min
	id := q.b[k][q.head[k]]
	q.head[k]++
	q.n--
	if h := int(q.head[k]); h >= 1<<16 && h*2 >= len(q.b[k]) {
		// Drop the popped prefix so a long-lived bucket doesn't keep it.
		rest := copy(q.b[k], q.b[k][h:])
		q.b[k], q.head[k] = q.b[k][:rest], 0
	}
	return id, int32(k)
}

// bytes is the queue's memory.
func (q *bucketQueue) bytes() uint64 {
	t := uint64(cap(q.b))*24 + uint64(cap(q.head))*4
	for _, x := range q.b {
		t += uint64(cap(x)) * 4
	}
	return t
}

// mappedOver reports whether the process's mapped memory is above pct% of
// the limit: a hard backstop shared by all strategies.
func mappedOver(limit uint64, pct uint64) bool {
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()-sample[1].Value.Uint64() > limit/100*pct
}

// trackedBytes is the capacity held by this search's large structures.
func (s *solver) trackedBytes() uint64 {
	return uint64(int(s.nodes.Bytes()) +
		cap(s.table)*8 + int(s.q.bytes()) + int(s.cells.bytes()) + cap(s.expanded)*8 + int(s.back.Bytes()) +
		int(s.sup.bytes()) + cap(s.supU))
}

var debugBest = os.Getenv("SOKSOLV_DEBUG_BEST") != ""

func (s *solver) dumpBest(id, h int32) {
	if id < 0 {
		return
	}
	cur := make([]int32, s.n)
	s.unpack(id, cur)
	occ := map[int32]bool{}
	for _, b := range cur {
		occ[b] = true
	}
	for i := range s.occ {
		s.occ[i] = false
	}
	for _, b := range cur {
		s.occ[b] = true
	}
	s.reach(s.nodePlayer(id), s.mark)
	st := s.stamp
	fmt.Fprintf(os.Stderr, "best h=%d nodes=%d\n", h, s.nodes.Len())
	for y := range s.lv.H {
		line := []byte{}
		for x := range s.lv.W {
			c := int32(y*s.lv.W + x)
			ch := byte(' ')
			switch {
			case s.lv.Wall[c]:
				ch = '#'
			case occ[c] && s.start[c]:
				ch = '*'
			case occ[c]:
				ch = '$'
			case s.start[c]:
				ch = '.'
			case s.mark[c] == st:
				ch = '-'
			}
			if c == s.lv.Player {
				ch = '@'
			}
			line = append(line, ch)
		}
		fmt.Fprintln(os.Stderr, string(line))
	}
}

// RevCells puts FESS-style cell scheduling on lane 0.
var RevCells = true

// cellQueue schedules the backward search the way exp-118 schedules FESS:
// open nodes live in per-cell queues (cell = boxes on start squares, each
// ordered by h); selections alternate between the best non-empty cell
// (most boxes on start squares) and a round robin over cells, and a newly
// created cell is visited next. A pure greedy search follows one line of
// progress; this keeps alternatives alive at every level of progress.
type cellQueue struct {
	q     []bucketQueue // by cell
	order []int         // cells in creation order (round robin)
	next  int           // round-robin position in order
	turn  int
	n     int
}

func (c *cellQueue) push(k int, h, id int32) {
	for k >= len(c.q) {
		c.q = append(c.q, bucketQueue{})
	}
	if c.q[k].b == nil && c.q[k].n == 0 && !c.seen(k) {
		c.order = append(c.order, k)
		c.next = len(c.order) - 1 // visit the new cell next
	}
	c.q[k].push(h, id)
	c.n++
}

func (c *cellQueue) seen(k int) bool {
	for _, x := range c.order {
		if x == k {
			return true
		}
	}
	return false
}

// popCell pops from a cell queue: round robin (with new-cell jumps) or the
// best cell (most boxes on start squares). Returns -1 when empty.
func (c *cellQueue) popCell(best bool) (int32, int32) {
	cell := -1
	if best {
		for k := len(c.q) - 1; k >= 0; k-- {
			if c.q[k].n > 0 {
				cell = k
				break
			}
		}
	} else {
		for range len(c.order) {
			k := c.order[c.next%len(c.order)]
			c.next = (c.next + 1) % len(c.order)
			if c.q[k].n > 0 {
				cell = k
				break
			}
		}
	}
	if cell < 0 {
		return -1, 0
	}
	c.n--
	return c.q[cell].pop()
}

func (c *cellQueue) bytes() uint64 {
	if c == nil {
		return 0
	}
	var t uint64
	for i := range c.q {
		t += c.q[i].bytes()
	}
	return t
}

// NodeBudget (> 0): a lane stops after this many expansions (deterministic
// screening).
var NodeBudget int64

// Types (exp-197): type-based exploration for every lane.
var Types = false

// TypesEvery: one pop in this many comes from the type queue.
var TypesEvery = 4

// Novelty (exp-196): a third queue of novel states (best-first width search).
var Novelty = false

// CellsAfter is when a lane with Cells starts diversifying: before it, the
// search is exactly the greedy one (fast levels are unaffected).
var CellsAfter = 2 * time.Second

// CellsAfterExp is CellsAfter in -nodes mode, as expansions (about what
// lane 0 does in 2 s).
var CellsAfterExp int64 = 40000

// popNext returns the next node to expand: greedy (lowest h) until
// CellsAfter, then 1/2 greedy, 1/4 round-robin cell, 1/4 best cell. Each
// node sits in both the greedy queue and its cell's queue; entries for
// nodes already expanded are skipped.
func (s *solver) popNext() (int32, int32) {
	for {
		var id, h int32
		fromCells := false
		if s.cells != nil && (NodeBudget == 0 && time.Since(s.started) > CellsAfter || NodeBudget > 0 && s.expandedTotal > CellsAfterExp) {
			s.cells.turn++
			switch s.cells.turn % 4 {
			case 1:
				id, h = s.cells.popCell(false)
				fromCells = id >= 0
			case 3:
				id, h = s.cells.popCell(true)
				fromCells = id >= 0
			}
		}
		if !fromCells && s.types != nil {
			s.typeTurn++
			if s.typeTurn%TypesEvery == 0 {
				if tid, th, ok := s.types.Pop(); ok {
					id, h, fromCells = tid, th, true
				}
			}
		}
		if !fromCells && (Novelty || s.ln.Novelty) && s.nq.n > 0 {
			s.novTurn++
			if s.novTurn%3 == 0 {
				id, h = s.nq.pop()
				fromCells = true
			}
		}
		if !fromCells {
			if s.q.n == 0 {
				return -1, 0
			}
			id, h = s.q.pop()
		}
		s.popMarked = id&supMark != 0
		id &^= supMark
		if s.cells == nil && !(Novelty || s.ln.Novelty) && s.types == nil {
			return id, h
		}
		w, bit := id>>6, uint64(1)<<(id&63)
		for int(w) >= len(s.expanded) {
			s.expanded = append(s.expanded, 0)
		}
		if s.expanded[w]&bit != 0 {
			continue // already expanded through the other queue
		}
		s.expanded[w] |= bit
		return id, h
	}
}

// memBreakdown is trackedBytes by structure, in MB (memtrace).
func (s *solver) memBreakdown() map[string]float64 {
	mb := func(b int) float64 { return float64(b) / (1 << 20) }
	return map[string]float64{
		"nodes":     mb(int(s.nodes.Bytes())),
		"table":     mb(cap(s.table) * 8),
		"queue":     mb(int(s.q.bytes())),
		"cells":     mb(int(s.cells.bytes())),
		"expanded":  mb(cap(s.expanded) * 8),
		"perimeter": mb(int(s.back.Bytes())),
	}
}

// runLaneForTest runs one lane privately and returns the solver (tests).
func runLaneForTest(rows []string, ln Lane, d time.Duration) (*solver, Result) {
	lv, err := parseLevel(rows)
	if err != nil {
		return nil, Result{}
	}
	s := newSolver(lv)
	ln.Private = true
	s.ln = ln
	if ln.Cells {
		s.cells = &cellQueue{}
	}
	s.started = time.Now()
	s.perturb(0)
	r := s.solve(time.Now().Add(d), 1<<31)
	return s, r
}

// storedPos reports whether the position (boxes, any player cell) is stored (tests).
func (s *solver) storedPos(boxes []int32, player int32) bool {
	for _, b := range boxes {
		if s.fidx[b] < 0 {
			return false
		}
	}
	for _, b := range boxes {
		s.occ[b] = true
	}
	np := s.reach(player, s.mark)
	for _, b := range boxes {
		s.occ[b] = false
	}
	clear(s.key)
	for _, b := range boxes {
		f := s.fidx[b]
		s.key[f>>6] |= 1 << (f & 63)
	}
	_, id, _ := s.find(s.key, np)
	return id >= 0
}

// Random-walk escape (exp-198; Arvand, Nakhost & Müller): when the best h
// has not improved for WalkAfter expansions, WalkCount random pull walks of
// WalkLen steps start from the best state. Every walk state is added like
// any child (parent links keep tails and meetings intact) and joins the
// queues with its h, so a walk that leaves the plateau is picked up by the
// greedy search. Each step counts as an expansion. Fixed-seed: deterministic.
var (
	Walks           = false
	WalkAfter int64 = 5000
	WalkCount       = 16
	WalkLen         = 50
)

func (s *solver) walkRand() uint64 {
	if s.wrng == 0 {
		s.wrng = 0x2545F4914F6CDD1D
	}
	s.wrng ^= s.wrng << 13
	s.wrng ^= s.wrng >> 7
	s.wrng ^= s.wrng << 17
	return s.wrng
}

func (s *solver) randomWalks(from int32) {
	lv := s.lv
	cur := make([]int32, s.n)
	child := make([]int32, s.n)
	type pull struct {
		i  int
		di int8
	}
	var opts []pull
	for range WalkCount {
		id := from
		for range WalkLen {
			s.unpack(id, cur)
			for _, b := range cur {
				s.occ[b] = true
			}
			s.reach(s.nodePlayer(id), s.mark)
			st := s.stamp
			opts = opts[:0]
			for i, b := range cur {
				for di, d := range s.dirs {
					p, p2 := b+d, b+2*d
					if s.mark[p] == st && !lv.Wall[p2] && !s.occ[p2] && s.dist[p] >= 0 {
						opts = append(opts, pull{i, int8(di)})
					}
				}
			}
			if len(opts) == 0 {
				for _, b := range cur {
					s.occ[b] = false
				}
				break
			}
			o := opts[s.walkRand()%uint64(len(opts))]
			b := cur[o.i]
			p := b + s.dirs[o.di]
			s.occ[b], s.occ[p] = false, true
			np := s.normalize(b, p, s.nodePlayer(id), st)
			copy(child, cur)
			child[o.i] = p
			slices.Sort(child)
			var ch int32
			switch {
			case s.ln.Match:
				ch = s.matchCost(child)
			case s.ln.Free:
				ch = s.stateCost(child)
			default:
				for _, x := range child {
					ch += s.key8[x]
				}
			}
			nid := s.add(child, np, id, b, o.di, ch)
			for _, x := range child {
				s.occ[x] = false
			}
			s.expandedTotal++
			s.walkSteps++
			if nid < 0 {
				break
			}
			id = nid
		}
	}
}

// FXEnd (exp-426 forensics): called when a search ends, with its stored nodes.
var FXEnd func(lane int, lv *Level, nstored int, unpack func(int32, []int32), nbox int, bestID, bestH int32, expanded int64)
