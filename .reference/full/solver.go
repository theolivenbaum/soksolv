package full

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"runtime/metrics"
	"slices"
	"soksolv/cluster"
	"soksolv/diagdead"
	"soksolv/meet"
	"soksolv/memtrace"
	"soksolv/reach"
	"soksolv/sidedead"
	"soksolv/soloracle"
	"sync/atomic"
	"time"

	"soksolv/trace"
)

// Best-first search over push states. A state is (sorted box cells, normalized player cell),
// where the normalized player is the smallest cell the player can reach.
// h = sum over boxes of push distance to the nearest goal; states with no
// feasible box-goal assignment are pruned at expansion. Ordering is greedy
// best-first on h (not A*): we want any solution fast, not push-optimal ones.
// Goal macros (see goalMacros) add one-move "push this box all the way to the
// next goal in packing order" children for boxes at goal-room entrances.
// Scheduling is FESS-style: feature cells (plan-consistent packing x regions)
// served round-robin, alternating with the global greedy heap. Within a cell,
// nodes are ordered by accumulated weight: advisor moves (goal macros, and the
// push that most reduces the region count) and pushes continuing the same box
// cost 0; switching to another box costs 1 (FESS weights per macro move).

const dirChars = "udlr"

// Stop asks a running search to give up at its next time check (used by the
// portfolio when another strategy has already solved the level).
var Stop atomic.Bool

// Budget is this search's memory budget in bytes, measured on its own data
// structures (trackedBytes). The portfolio sets it and raises it when other
// strategies finish. 0 means no budget (process-wide backstop only).
var Budget atomic.Uint64

// Tracked is this search's latest trackedBytes, for the portfolio's memory
// governor.
var Tracked atomic.Uint64

type Result struct {
	Solved   bool
	Reason   string
	Solution string // LURD: lowercase = move, uppercase = push
	Nodes    int
	Stats    Stats
}

// Stats are search diagnostics, reported in the solver's JSON output.
type Stats struct {
	MemMB       map[string]float64 `json:"mem_mb,omitempty"` // capacity held per structure
	Generated   int                `json:"generated"`        // children that passed all pruning
	Duplicates  int                `json:"duplicates"`       // generated children already in the table
	SideDead    int                `json:"pruned_side_dead"` // children with a box whose player side can't reach a goal
	DiagDead    int                `json:"pruned_diag,omitempty"`
	ClusterDead int                `json:"pruned_cluster"`
	Penalized   int                `json:"penalized,omitempty"` // children whose key carried a cluster penalty
	Stored      int                `json:"stored"`              // states in the table at the end
	DeadSquare  int                `json:"pruned_dead_square"`
	Frozen2x2   int                `json:"pruned_2x2"`
	GoalArea    int                `json:"pruned_goal_area"` // pushes leaving a goal unreachable behind frozen goal boxes
	Freeze      int                `json:"pruned_freeze"`
	Assignment  int                `json:"pruned_assignment"` // expanded states with no feasible box-goal assignment
	Macros      int                `json:"goal_macros"`       // goal-macro children generated
	LeanAt      int                `json:"lean_at,omitempty"` // pop count when one-move mode started
	ConnAdvisor int                `json:"conn_advisor"`      // pushes chosen by the connectivity advisor
	Corrals     int                `json:"corral_expansions"` // expansions restricted by a PI-corral
	PairDead    int                `json:"pruned_pair"`       // pushes into a precomputed two-box deadlock
	PairsDead   int                `json:"pair_table_dead"`   // two-box deadlocks found in preprocessing
	PairsTried  int                `json:"pair_table_tried"`  // pairs analysed before the budget ran out
}

// push is one box push: the box at from moves one cell in direction dir.
type push struct {
	from int32
	dir  int8
}

// A heap key packs (major, minor, id) into one uint64 so ordering is a single
// integer compare: major and minor are clamped to 16 bits (h and FESS weight
// stay far below that in practice), id is the node. The global greedy heap
// uses (h, weight); the feature-cell heaps use (weight, h).
type hkey uint64

func mkKey(major, minor, id int32) hkey {
	return hkey(min(uint64(major), 0xFFFF)<<48 | min(uint64(minor), 0xFFFF)<<32 | uint64(uint32(id)))
}

func (k hkey) id() int32 { return int32(uint32(k)) }

type solver struct {
	lv   *Level
	n    int
	dirs [4]int32
	dist []int32 // push distance from cell to nearest goal; -1 = dead square

	goals    []int32
	rg       *reach.Graph // exp-464 (-exact-reach): player-side access graph; nil = relaxed tables
	goalDist []int32      // goalDist[g*cells+c]: pushes to bring a box from c to goals[g]; -1 = never
	hu, hv   []int32      // Hungarian algorithm scratch, all sized n+1
	hp, hway []int32
	hminv    []int32
	hused    []bool
	matchOf  []int32  // feasible: goal index -> matched box index, or -1
	gseen    []uint32 // feasible: visited stamps per goal
	gstamp   uint32

	occ    []bool
	mark   []uint32 // reachability stamps for the expanded state
	mark2  []uint32 // reachability stamps for child normalization
	fz     []bool   // boxes currently under freeze examination
	cmark  []uint32 // corral component stamps
	poff   []int32  // two-box deadlock table: neighbor offsets
	popp   []int
	dpair  []int8 // dpair[cell*len(poff)+k] for boxes on cell and cell+poff[k]: 0 unknown, 1 dead, 2 alive
	pocc   []bool // pairDead scratch: private board, marks, queue
	pmark  []uint32
	pseen  []uint32
	pq     []int32
	pst    uint32
	pst2   uint32
	cstamp uint32
	stamp  uint32
	queue  []int32

	// node arena
	boxes    []uint64 // nw words per node: bitset of box cells over floor indices
	key      []uint64 // scratch: the state being looked up
	nw       int      // words per state
	fidx     []int32  // cell -> floor index (-1 for walls)
	fcell    []int32  // floor index -> cell
	player   []int32
	parent   []int32
	pushFrom []int32
	pushDir  []int8
	g        []int32
	w        []int32  // per node: accumulated FESS weight (non-advisor moves)
	reg      []int8   // per node: empty-floor region count
	table    []int32  // open-addressing set of node ids; -1 = empty
	tfp      []uint32 // per slot: high 32 bits of the stored state's hash

	// Every queued node is in the global greedy heap and in the bucket for its
	// FESS-style feature cell: (boxes packed in plan order, empty-floor regions). Pops alternate between the global heap and
	// the buckets in round-robin, sharing one state table; done marks expanded
	// nodes so the second copy is skipped.
	heap       []hkey
	rmark      []uint32 // regions() scratch
	floor      []int32  // non-wall cells
	rqueue     []int32
	rstamp     uint32
	buckets    [][]hkey
	bucket     []uint16           // per node: its feature cell
	mvStart    []int32            // per node: start of its moves in s.mv, or -1 if not opened yet
	mvLen      []uint16           // per node: number of moves
	mvNext     []uint16           // per node: next move in FESS order (may point at a used one)
	mvLeft     []uint16           // per node: moves not yet applied
	lean       bool               // memory is tight: one move per pop instead of full expansion
	ga         goalArea           // goal-area deadlock scratch and cache
	memFull    bool               // the table cannot grow within the budget: stop
	mv         []uint32           // move arena (see encodeMove)
	nodeMacros map[int32][][]push // per opened node: its goal-macro push sequences
	turn       int
	rr         int

	// Goal macros.
	round    []int32 // per goal cell: pull-extraction round; higher = fill earlier
	entrance []bool  // non-goal floor cells adjacent to a goal
	macros   map[int32][]push
	mark3    []uint32 // player reach inside box-path search
	bseen    []uint32 // box-path states visited, indexed cell*4+dir
	bprev    []int32  // box-path predecessor state
	pstamp   uint32
	bstamp   uint32

	stats Stats

	freeG   []int32  // setCosts: indexes of goals no box holds
	cc      []int32  // hc memo per cell
	ccMark  []uint32 // hc memo stamps
	ccStamp uint32

	back, fwd   *meet.Table // perimeters: the backward search's, ours
	meetPending []int32     // new nodes that hit the backward perimeter
	meetPosted  int

	// Resources: the package globals unless a restart runs on another
	// strategy's core (SolveWith with Slot).
	budget, tracked *atomic.Uint64
	tr              *trace.Recorder
	freeGoal        bool // see FreeGoal
	owner           int  // our mailbox on the forward perimeter
	noise           []int32
	cl              *cluster.Checker  // cluster deadlocks (shared table)
	sd              *sidedead.Table   // player-side dead squares (real goals only)
	dd              *diagdead.Checker // diagonal-chain deadlocks (exp-459; nil: off)
}

func newSolver(lv *Level) *solver {
	size := lv.W * lv.H
	s := &solver{
		lv:    lv,
		n:     len(lv.Boxes),
		dirs:  [4]int32{-int32(lv.W), int32(lv.W), -1, 1},
		occ:   make([]bool, size),
		mark:  make([]uint32, size),
		mark2: make([]uint32, size),
		fz:    make([]bool, size),
		cmark: make([]uint32, size),
		queue: make([]int32, 0, size),
		table: make([]int32, 1<<12),
		tfp:   make([]uint32, 1<<12),
	}
	for i := range s.table {
		s.table[i] = -1
	}
	s.initBitsets()
	s.computeDistances()
	s.computeGoalDistances()
	s.cc = make([]int32, len(lv.Wall))
	s.ccMark = make([]uint32, len(lv.Wall))
	m := s.n + 1
	s.hu, s.hv, s.hp, s.hway, s.hminv = make([]int32, m), make([]int32, m), make([]int32, m), make([]int32, m), make([]int32, m)
	s.hused = make([]bool, m)
	s.macros = map[int32][]push{}
	s.mark3 = make([]uint32, size)
	s.bseen = make([]uint32, 4*size)
	s.bprev = make([]int32, 4*size)
	s.computePackingOrder()
	return s
}

// computeDistances does a reverse (pull) BFS from all goals. A box at b can be
// pushed in direction d to b+d if b-d (player) and b+d are floor.
func (s *solver) computeDistances() {
	lv := s.lv
	s.dist = make([]int32, len(lv.Wall))
	if reach.Enabled {
		// Exact single-box reach with the player's access (exp-464).
		s.rg = reach.New(lv.Wall, lv.W, lv.Boxes, lv.Player)
		var goals []int32
		for c, isGoal := range lv.Goal {
			if isGoal {
				goals = append(goals, int32(c))
			}
		}
		s.rg.Backward(goals, s.dist)
		return
	}
	for i := range s.dist {
		s.dist[i] = -1
	}
	q := s.queue[:0]
	for c, isGoal := range lv.Goal {
		if isGoal {
			s.dist[c] = 0
			q = append(q, int32(c))
		}
	}
	for i := 0; i < len(q); i++ {
		c := q[i]
		for _, d := range s.dirs {
			b := c - d
			if !lv.Wall[b] && !lv.Wall[b-d] && s.dist[b] < 0 {
				s.dist[b] = s.dist[c] + 1
				q = append(q, b)
			}
		}
	}
}

// computeGoalDistances runs the reverse (pull) BFS once per goal.
func (s *solver) computeGoalDistances() {
	lv := s.lv
	cells := len(lv.Wall)
	for c, isGoal := range lv.Goal {
		if isGoal {
			s.goals = append(s.goals, int32(c))
		}
	}
	s.goalDist = make([]int32, len(s.goals)*cells)
	for i := range s.goalDist {
		s.goalDist[i] = -1
	}
	for g, goal := range s.goals {
		d := s.goalDist[g*cells : (g+1)*cells]
		if s.rg != nil {
			s.rg.Backward([]int32{goal}, d)
			continue
		}
		d[goal] = 0
		q := append(s.queue[:0], goal)
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, dir := range s.dirs {
				b := c - dir
				if !lv.Wall[b] && !lv.Wall[b-dir] && d[b] < 0 {
					d[b] = d[c] + 1
					q = append(q, b)
				}
			}
		}
		s.queue = q
	}
}

// feasible reports whether every box can be matched to a distinct goal it can
// reach (per-goal push distances, ignoring other boxes): a bipartite perfect
// matching, found with augmenting paths. Same answer as assign(boxes) >= 0,
// without computing the minimum cost.
func (s *solver) feasible(boxes []int32) bool {
	cells := len(s.lv.Wall)
	if s.matchOf == nil {
		s.matchOf = make([]int32, len(s.goals))
		s.gseen = make([]uint32, len(s.goals))
	}
	for j := range s.matchOf {
		s.matchOf[j] = -1
	}
	var try func(i int32) bool
	try = func(i int32) bool {
		b := int(boxes[i])
		for j := range s.goals {
			if s.gseen[j] == s.gstamp || s.goalDist[j*cells+b] < 0 {
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
		if s.gstamp == 0 { // wrapped
			clear(s.gseen)
			s.gstamp = 1
		}
		if !try(i) {
			return false
		}
	}
	return true
}

// reach marks cells reachable from p (not crossing walls or boxes) and
// returns the minimum reachable cell.
func (s *solver) reach(p int32, mark []uint32) int32 {
	s.stamp++
	st := s.stamp
	q := append(s.queue[:0], p)
	mark[p] = st
	minC := p
	for i := 0; i < len(q); i++ {
		c := q[i]
		for _, d := range s.dirs {
			nb := c + d
			if mark[nb] != st && !s.lv.Wall[nb] && !s.occ[nb] {
				mark[nb] = st
				q = append(q, nb)
				minC = min(minC, nb)
			}
		}
	}
	s.queue = q
	return minC
}

func (s *solver) blocked(c int32) bool { return s.lv.Wall[c] || s.occ[c] }

// frozen2x2 reports whether the box at t sits in a 2x2 block of walls/boxes
// that contains a box not on a goal.
func (s *solver) frozen2x2(t int32) bool {
	w := int32(s.lv.W)
	for _, tl := range [4]int32{t, t - 1, t - w, t - w - 1} {
		cells := [4]int32{tl, tl + 1, tl + w, tl + w + 1}
		if s.blocked(cells[0]) && s.blocked(cells[1]) && s.blocked(cells[2]) && s.blocked(cells[3]) {
			for _, c := range cells {
				if s.occ[c] && !s.lv.Goal[c] {
					return true
				}
			}
		}
	}
	return false
}

// freezeDeadlock reports whether the box just pushed to t is frozen (cannot move
// along either axis) together with any box it depends on, while one of those
// frozen boxes is off goal. Boxes under examination are treated as walls,
// which prevents cycles and is the standard sound formulation.
func (s *solver) freezeDeadlock(t int32) bool {
	offGoal := false
	return s.frozen(t, &offGoal) && offGoal
}

func (s *solver) frozen(c int32, offGoal *bool) bool {
	s.fz[c] = true
	defer func() { s.fz[c] = false }()
	w := int32(s.lv.W)
	if s.blockedAxis(c, 1, offGoal) && s.blockedAxis(c, w, offGoal) {
		if !s.lv.Goal[c] {
			*offGoal = true
		}
		return true
	}
	return false
}

// blockedAxis reports whether the box at c cannot be pushed along axis d.
func (s *solver) blockedAxis(c, d int32, offGoal *bool) bool {
	a, b := c-d, c+d
	wall := s.lv.Wall
	if wall[a] || wall[b] || s.fz[a] || s.fz[b] {
		return true
	}
	if s.dist[a] < 0 && s.dist[b] < 0 {
		return true
	}
	return s.occ[a] && s.frozen(a, offGoal) || s.occ[b] && s.frozen(b, offGoal)
}

// normalize returns the normalized player cell (minimum reachable cell) of the
// child reached by pushing a box from b to t, with the player now on b. The
// parent's region R is stamped st in s.mark and its minimum is minR; s.occ
// holds the child's boxes. If b adds no new area (its other neighbors are
// walls, boxes or in R) and t either was outside R or blocking it cannot split
// R (the free cells around t stay connected through t's 8-cell ring), the
// child's region is R + b - t and the answer is min(minR, b) without a BFS.
func (s *solver) normalize(b, t, minR int32, st uint32) int32 {
	lv := s.lv
	for _, d := range s.dirs {
		x := b + d
		if x != t && !lv.Wall[x] && !s.occ[x] && s.mark[x] != st {
			return s.reach(b, s.mark2)
		}
	}
	if s.mark[t] == st && (t == minR || !s.ringConnected(t)) {
		return s.reach(b, s.mark2)
	}
	return min(minR, b)
}

// ringConnected reports whether all free orthogonal neighbors of c lie in one
// contiguous run of free cells on the 8-cell ring around c, so that blocking
// c cannot disconnect them.
func (s *solver) ringConnected(c int32) bool {
	w := int32(s.lv.W)
	ring := [8]int32{c - w, c - w + 1, c + 1, c + w + 1, c + w, c + w - 1, c - 1, c - w - 1}
	free := func(x int32) bool { return !s.lv.Wall[x] && !s.occ[x] }
	start := -1 // a blocked ring cell to start scanning from
	for i, x := range ring {
		if !free(x) {
			start = i
			break
		}
	}
	if start < 0 {
		return true // fully open ring
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

// computePackingOrder ranks goals by a reverse (pull) analysis. Starting with
// every goal filled, a goal is extractable if its box can be pulled, with the
// other filled goals as obstacles, to some non-goal cell (player access is
// ignored). All extractable goals get the current round and are emptied, and
// the analysis repeats; goals never extractable share the last round. Goals
// extracted in later rounds are deeper and should be filled earlier.
func (s *solver) computePackingOrder() {
	lv := s.lv
	cells := len(lv.Wall)
	s.round = make([]int32, cells)
	s.entrance = make([]bool, cells)
	filled := slices.Clone(lv.Goal)
	var left []int32
	for c, g := range lv.Goal {
		if g {
			left = append(left, int32(c))
			continue
		}
		s.round[c] = -1
		if !lv.Wall[c] {
			for _, d := range s.dirs {
				if lv.Goal[int32(c)+d] {
					s.entrance[c] = true
				}
			}
		}
	}
	seen := make([]int32, cells)
	stamp := int32(0)
	for r := int32(0); len(left) > 0; r++ {
		var out, keep []int32
		for _, g := range left {
			stamp++
			if s.extractable(g, filled, seen, stamp) {
				out = append(out, g)
			} else {
				keep = append(keep, g)
			}
		}
		if len(out) == 0 {
			out, keep = keep, nil
		}
		for _, g := range out {
			filled[g] = false
			s.round[g] = r
		}
		left = keep
	}
}

// extractable reports whether a box on goal g can be pulled to a non-goal
// cell with the other filled goals as obstacles.
func (s *solver) extractable(g int32, filled []bool, seen []int32, stamp int32) bool {
	lv := s.lv
	free := func(c int32) bool { return !lv.Wall[c] && (!filled[c] || c == g) }
	q := []int32{g}
	seen[g] = stamp
	for i := 0; i < len(q); i++ {
		c := q[i]
		for _, d := range s.dirs {
			nc := c + d
			if seen[nc] == stamp || !free(nc) || !free(nc+d) {
				continue
			}
			if !lv.Goal[nc] {
				return true
			}
			seen[nc] = stamp
			q = append(q, nc)
		}
	}
	return false
}

// macroMoves returns, for the box just pushed onto a goal-room entrance, a move that
// pushes it along a box path to an empty goal of the highest pending round
// (the goal that should be filled next). Ordinary pushes are still generated,
// so this only adds moves. cur holds the state's boxes (s.occ set), the
// player's reach is stamped st in s.mark, and child is scratch.
func (s *solver) macroMoves(cur []int32, id int32, st uint32) [][]push {
	maxR := int32(-1)
	for _, g := range s.goals {
		if !s.occ[g] {
			maxR = max(maxR, s.round[g])
		}
	}
	if maxR < 0 || s.parent[id] < 0 {
		return nil
	}
	// Like Rolling Stone, only the box just pushed (onto an entrance) triggers
	// a macro; boxes already waiting at an entrance were tried before.
	moved := s.pushFrom[id] + s.dirs[s.pushDir[id]]
	if seq, ok := s.macros[id]; ok {
		last := seq[len(seq)-1]
		moved = last.from + s.dirs[last.dir]
	}
	var out [][]push
	for _, b := range cur {
		if b != moved || !s.entrance[b] {
			continue
		}
		if seq := s.boxPath(b, maxR, st); seq != nil {
			s.stats.Macros++
			out = append(out, seq)
		}
	}
	return out
}

// boxPath searches pushes of the box at b (other boxes fixed) for a path to an
// empty goal with round maxR. States are (box cell, push direction) with the
// player behind the box; player access is recomputed after every push. The
// search is capped to keep expansion cheap. Returns the pushes, or nil.
func (s *solver) boxPath(b, maxR int32, st uint32) []push {
	const limit = 500
	lv := s.lv
	if s.pstamp > 1<<31 || s.bstamp > 1<<31 { // stamps would wrap: reset
		clear(s.mark3)
		clear(s.bseen)
		s.pstamp, s.bstamp = 0, 0
	}
	s.bstamp++
	bs := s.bstamp
	var q []int32
	for di, d := range s.dirs {
		if s.mark[b-d] == st {
			k := b*4 + int32(di)
			s.bseen[k], s.bprev[k] = bs, -1
			q = append(q, k)
		}
	}
	s.occ[b] = false
	defer func() { s.occ[b] = true }()
	for i := 0; i < len(q) && i < limit; i++ {
		k := q[i]
		c, di := k/4, k%4
		nc := c + s.dirs[di]
		if lv.Wall[nc] || s.occ[nc] || s.dist[nc] < 0 {
			continue
		}
		if lv.Goal[nc] && s.round[nc] == maxR {
			var seq []push
			for ; k >= 0; k = s.bprev[k] {
				seq = append(seq, push{k / 4, int8(k % 4)})
			}
			slices.Reverse(seq)
			return seq
		}
		// Player is now on c, box on nc: which sides of nc can it reach?
		s.occ[nc] = true
		s.pstamp++
		ps := s.pstamp
		pq := append(s.queue[:0], c)
		s.mark3[c] = ps
		for j := 0; j < len(pq); j++ {
			x := pq[j]
			for _, d := range s.dirs {
				y := x + d
				if s.mark3[y] != ps && !lv.Wall[y] && !s.occ[y] {
					s.mark3[y] = ps
					pq = append(pq, y)
				}
			}
		}
		s.queue = pq
		s.occ[nc] = false
		for d2i, d2 := range s.dirs {
			k2 := nc*4 + int32(d2i)
			if s.mark3[nc-d2] == ps && s.bseen[k2] != bs {
				s.bseen[k2], s.bprev[k2] = bs, k
				q = append(q, k2)
			}
		}
	}
	return nil
}

// packed is FESS's packing feature: boxes (per s.occ) on goals consistent with
// the packing order, i.e. goals whose round is at least the deepest round still
// empty. Boxes on goals that should have been filled later don't count.
func (s *solver) packed() int {
	maxR := int32(-1)
	for _, g := range s.goals {
		if !s.occ[g] {
			maxR = max(maxR, s.round[g])
		}
	}
	n := 0
	for _, g := range s.goals {
		if s.occ[g] && s.round[g] >= maxR {
			n++
		}
	}
	return n
}

// maxRegions caps the connectivity feature (regions beyond it share a cell).
const maxRegions = 16

// regions counts the connected areas of empty floor (per s.occ): FESS's
// connectivity feature. Fewer regions means the player has more access.
func (s *solver) regions() int {
	lv := s.lv
	if s.rmark == nil {
		s.rmark = make([]uint32, len(lv.Wall))
	}
	s.rstamp++
	rs := s.rstamp
	n := 0
	if s.floor == nil {
		for c, w := range lv.Wall {
			if !w {
				s.floor = append(s.floor, int32(c))
			}
		}
	}
	for _, c := range s.floor {
		if s.occ[c] || s.rmark[c] == rs {
			continue
		}
		n++
		s.rmark[c] = rs
		q := append(s.rqueue[:0], c)
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; s.rmark[y] != rs && !lv.Wall[y] && !s.occ[y] {
					s.rmark[y] = rs
					q = append(q, y)
				}
			}
		}
		s.rqueue = q
	}
	return n
}

// piCorral looks for a PI-corral in the current state (player reach stamped
// with st in s.mark) and returns its component stamp, or 0 if there is none.
//
// A corral is a connected area of cells the player cannot reach, flooded
// through empty floor and boxes (so areas separated only by boxes merge). Its
// barrier is the corral's boxes that touch the player's reachable area. It is
// a PI-corral if every legal, non-dead push of a barrier box goes into the
// corral's empty cells (I) and every barrier box has such a push (P). If the
// corral is not already solved (it holds an empty goal or a box off goal),
// some barrier push must happen eventually and other pushes can be postponed,
// so only barrier pushes need to be generated. Among PI-corrals, the one with
// the fewest barrier boxes is chosen.
func (s *solver) piCorral(st uint32) uint32 {
	lv := s.lv
	if s.cstamp > 1<<31 { // stamps would wrap: reset
		clear(s.cmark)
		s.cstamp = 0
	}
	base := s.cstamp // components stamped in this call are > base
	best, bestBoxes := uint32(0), math.MaxInt
	q := s.queue[:0]
	var barrier []int32
	for c := range int32(len(lv.Wall)) {
		if lv.Wall[c] || s.occ[c] || s.mark[c] == st || s.cmark[c] > base {
			continue
		}
		s.cstamp++
		cs := s.cstamp
		s.cmark[c] = cs
		q = append(q[:0], c)
		barrier = barrier[:0]
		unsolved := false
		for i := 0; i < len(q); i++ {
			x := q[i]
			unsolved = unsolved || lv.Goal[x] && !s.occ[x]
			for _, d := range s.dirs {
				nb := x + d
				switch {
				case lv.Wall[nb]:
				case s.cmark[nb] == cs, s.mark[nb] == st && !s.occ[nb]:
				case s.occ[nb]:
					s.cmark[nb] = cs
					q = append(q, nb)
					unsolved = unsolved || !lv.Goal[nb]
					if s.touchesReach(nb, st) {
						barrier = append(barrier, nb)
					}
				default:
					s.cmark[nb] = cs
					q = append(q, nb)
				}
			}
		}
		if len(barrier) >= bestBoxes {
			continue
		}
		ok := true
		for _, b := range barrier {
			pushable := false
			for _, d := range s.dirs {
				t := b + d
				if s.mark[b-d] != st || lv.Wall[t] || s.occ[t] || s.dist[t] < 0 {
					continue // not a legal, non-dead push
				}
				if s.cmark[t] != cs { // t is empty here, so cmark means corral floor
					ok = false // a push that leaves the corral alone
					break
				}
				pushable = true
			}
			if !ok || !pushable {
				ok = false
				break
			}
		}
		if ok && unsolved {
			best, bestBoxes = cs, len(barrier)
		}
	}
	s.queue = q
	return best
}

// touchesReach reports whether box b is adjacent to a player-reachable cell.
func (s *solver) touchesReach(b int32, st uint32) bool {
	for _, d := range s.dirs {
		if s.mark[b+d] == st {
			return true
		}
	}
	return false
}

// pairOffsets are the cell offsets (Chebyshev distance <= 2) for which
// two-box deadlocks are tabulated; pairOpp[k] is the offset pointing back.
func (s *solver) pairOffsets() {
	w := int32(s.lv.W)
	for dy := int32(-2); dy <= 2; dy++ {
		for dx := int32(-2); dx <= 2; dx++ {
			if dx != 0 || dy != 0 {
				s.poff = append(s.poff, dy*w+dx)
			}
		}
	}
	s.popp = make([]int, len(s.poff))
	for k, o := range s.poff {
		s.popp[k] = slices.Index(s.poff, -o)
	}
}

// pairDead runs a bounded search of the two-box subproblem (only boxes on a
// and b) from every player region, on private scratch arrays so it can run in
// the middle of the main search. It reports a deadlock only if no player
// region lets both boxes reach goals; removing the other boxes only relaxes
// the problem, so the answer holds in every real state. Searches that hit the
// state cap count as not dead.
func (s *solver) pairDead(a, b int32) bool {
	const capStates = 200
	lv := s.lv
	if s.pocc == nil {
		s.pocc = make([]bool, len(lv.Wall))
		s.pmark = make([]uint32, len(lv.Wall))
		s.pseen = make([]uint32, len(lv.Wall))
	}
	occ := s.pocc
	// reach marks the player's region from p (stamp returned) and returns its minimum cell.
	reach := func(p int32) (int32, uint32) {
		s.pst++
		st := s.pst
		q := append(s.pq[:0], p)
		s.pmark[p] = st
		m := p
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; s.pmark[y] != st && !lv.Wall[y] && !occ[y] {
					s.pmark[y] = st
					q = append(q, y)
					m = min(m, y)
				}
			}
		}
		s.pq = q
		return m, st
	}
	type st2 struct{ x, y, p int32 }
	occ[a], occ[b] = true, true
	defer func() { occ[a], occ[b] = false, false }()
	s.pst2++
	seenStamp := s.pst2
	var starts []int32
	for _, c := range s.floorCells() {
		if occ[c] || s.pseen[c] == seenStamp {
			continue
		}
		m, _ := reach(c)
		starts = append(starts, m)
		for _, x := range s.pq { // the region just flooded
			s.pseen[x] = seenStamp
		}
	}
	for _, p0 := range starts {
		seen := map[st2]bool{{a, b, p0}: true}
		q := []st2{{a, b, p0}}
		for i := 0; i < len(q); i++ {
			if len(seen) > capStates {
				return false // unknown: assume not dead
			}
			cur := q[i]
			occ[a], occ[b] = false, false
			occ[cur.x], occ[cur.y] = true, true
			_, st := reach(cur.p)
			type mv struct{ box, t, other int32 }
			var moves []mv
			for _, pr := range [2][2]int32{{cur.x, cur.y}, {cur.y, cur.x}} {
				bx, other := pr[0], pr[1]
				for _, d := range s.dirs {
					t := bx + d
					if s.pmark[bx-d] == st && !lv.Wall[t] && t != other && s.dist[t] >= 0 {
						moves = append(moves, mv{bx, t, other})
					}
				}
			}
			occ[cur.x], occ[cur.y] = false, false
			for _, m := range moves {
				if lv.Goal[m.t] && lv.Goal[m.other] {
					occ[a], occ[b] = true, true
					return false
				}
				occ[m.t], occ[m.other] = true, true
				np, _ := reach(m.box)
				occ[m.t], occ[m.other] = false, false
				n := st2{min(m.t, m.other), max(m.t, m.other), np}
				if !seen[n] {
					seen[n] = true
					q = append(q, n)
				}
			}
			occ[a], occ[b] = true, true
		}
	}
	return true
}

func (s *solver) floorCells() []int32 {
	if s.floor == nil {
		for c, w := range s.lv.Wall {
			if !w {
				s.floor = append(s.floor, int32(c))
			}
		}
	}
	return s.floor
}

// pairDeadlock reports whether the box just pushed to t forms a two-box
// deadlock with a box nearby (per s.occ). Pairs are analysed lazily, the
// first time they occur, and cached: 0 unknown, 1 dead, 2 alive.
func (s *solver) pairDeadlock(t int32) bool {
	lv := s.lv
	if lv.Goal[t] || s.dist[t] < 0 {
		return false
	}
	base := int(t) * len(s.poff)
	for k, o := range s.poff {
		x := t + o
		if x < 0 || int(x) >= len(lv.Wall) || !s.occ[x] || lv.Goal[t] && lv.Goal[x] {
			continue
		}
		v := s.dpair[base+k]
		if v == 0 {
			s.stats.PairsTried++
			v = 2
			if s.pairDead(t, x) {
				v = 1
				s.stats.PairsDead++
			}
			s.dpair[base+k] = v
			s.dpair[int(x)*len(s.poff)+s.popp[k]] = v
		}
		if v == 1 {
			return true
		}
	}
	return false
}

// Move encoding (uint32): bits 0-8 box index in the node's sorted boxes (or
// macro index), 9-10 direction, 11 FESS weight delta, 12 macro flag, 13-19
// child region count (capped at 127).
func encodeMove(i int32, dir int8, wd int32, macro bool, r int) uint32 {
	m := uint32(i) | uint32(dir)<<9 | uint32(wd)<<11 | uint32(min(r, 127))<<13
	if macro {
		m |= 1 << 12
	}
	return m
}

func decodeMove(m uint32) (i int32, dir int8, wd int32, macro bool, r int) {
	return int32(m & 0x1FF), int8(m >> 9 & 3), int32(m >> 11 & 1), m>>12&1 == 1, int(m >> 13 & 0x7F)
}

// open generates node id's moves (s.occ holds its boxes, its reach is
// stamped st in s.mark) and stores them sorted by (weight delta, child h).
// Pruning, PI-corral restriction and advisors are as in full expansion.
func (s *solver) open(id int32, cur []int32, h int32, st uint32) {
	lv := s.lv
	type cand struct {
		code uint32
		wd   int32
		ch   int32
	}
	var cands []cand
	s.mvStart[id] = int32(len(s.mv))
	defer func() {
		slices.SortStableFunc(cands, func(a, b cand) int {
			if a.wd != b.wd {
				return int(a.wd - b.wd)
			}
			return int(a.ch - b.ch)
		})
		for _, c := range cands {
			s.mv = append(s.mv, c.code)
		}
		s.mvLen[id] = uint16(min(len(cands), 0xFFFF))
		s.mvLeft[id] = s.mvLen[id]
	}()
	if !s.feasible(cur) {
		s.stats.Assignment++
		return // no feasible box-goal assignment: dead state
	}
	if seqs := s.macroMoves(cur, id, st); seqs != nil {
		if s.nodeMacros == nil {
			s.nodeMacros = map[int32][][]push{}
		}
		s.nodeMacros[id] = seqs
		for k, seq := range seqs {
			cands = append(cands, cand{encodeMove(int32(k), 0, 0, true, 0), 0, h - s.hc(seq[0].from)})
		}
	}
	type push1 struct {
		i, b, t int32
		dir     int8
		r       int
		pen     int32
	}
	var pushes []push1
	best, bestK := int(s.reg[id]), -1
	corral := s.piCorral(st)
	if corral != 0 {
		s.stats.Corrals++
	}
	for i, b := range cur {
		if corral != 0 && s.cmark[b] != corral {
			continue // a PI-corral exists: only its barrier pushes are needed
		}
		for di, d := range s.dirs {
			t := b + d
			if s.mark[b-d] != st || lv.Wall[t] || s.occ[t] {
				continue
			}
			if s.dist[t] < 0 {
				s.stats.DeadSquare++
				continue
			}
			s.occ[b], s.occ[t] = false, true
			switch {
			case s.frozen2x2(t):
				s.stats.Frozen2x2++
			case s.freezeDeadlock(t):
				s.stats.Freeze++
			case s.goalAreaDeadlock(t):
				s.stats.GoalArea++
			case s.pairDeadlock(t):
				s.stats.PairDead++
			case s.dd != nil && s.dd.Dead(s.occ, t, b):
				s.stats.DiagDead++
			case s.cl != nil && s.cl.Dead(s.occ, t, b, func(y int32) bool { return s.mark[y] == st || y == b }):
				s.stats.ClusterDead++
			default:
				s.stats.Generated++
				r := s.regions()
				pen := int32(0)
				if Penalties && s.cl != nil && s.noise == nil {
					pen = s.cl.Penalty()
					if pen > 0 {
						s.stats.Penalized++
					}
				}
				pushes = append(pushes, push1{int32(i), b, t, int8(di), r, pen})
				if r < best {
					best, bestK = r, len(pushes)-1
				}
			}
			s.occ[b], s.occ[t] = true, false
		}
	}
	// Weights approximate FESS macro moves: pushing the box that was just
	// pushed again is free (same macro), switching to another box costs 1.
	// Connectivity advisor: the push that most reduces the number of regions
	// (if any does) also costs 0.
	last := int32(-1)
	if s.parent[id] >= 0 {
		last = s.pushFrom[id] + s.dirs[s.pushDir[id]]
		if seq, ok := s.macros[id]; ok {
			l := seq[len(seq)-1]
			last = l.from + s.dirs[l.dir]
		}
	}
	for k, p := range pushes {
		wd := int32(1)
		switch {
		case k == bestK:
			wd = 0
			s.stats.ConnAdvisor++
		case p.b == last:
			wd = 0
		}
		cands = append(cands, cand{encodeMove(p.i, p.dir, wd, false, p.r), wd, h - s.hc(p.b) + s.hc(p.t) + p.pen})
	}
}

// moveKeyAt returns (child h, child weight) of the move at index k of s.mv.
func (s *solver) moveKeyAt(id, k int32, cur []int32, h int32) (int32, int32) {
	i, dir, wd, macro, _ := decodeMove(s.mv[k] &^ moveUsed)
	if macro {
		return h - s.hc(s.nodeMacros[id][i][0].from), s.w[id]
	}
	b := cur[i]
	return h - s.hc(b) + s.hc(b+s.dirs[dir]), s.w[id] + wd
}

// apply performs move m of node id (s.occ holds its boxes, reach stamped st)
// and adds the child. It returns the child's id (-1 if already stored) and h.
func (s *solver) apply(id int32, m uint32, cur, child []int32, h int32, st uint32) (int32, int32) {
	i, dir, wd, macro, r := decodeMove(m)
	copy(child, cur)
	if macro {
		seq := s.nodeMacros[id][i]
		b := seq[0].from
		last := seq[len(seq)-1]
		g := last.from + s.dirs[last.dir]
		child[slices.Index(cur, b)] = g
		slices.Sort(child)
		s.occ[b], s.occ[g] = false, true
		np := s.reach(g-s.dirs[last.dir], s.mark2)
		ch := h - s.hc(b)
		cid := s.add(child, np, id, b, last.dir, seq, s.g[id]+int32(len(seq)), ch, s.w[id], -1)
		s.occ[b], s.occ[g] = true, false
		return cid, ch
	}
	b := cur[i]
	t := b + s.dirs[dir]
	child[i] = t
	slices.Sort(child)
	s.occ[b], s.occ[t] = false, true
	np := s.normalize(b, t, s.player[id], st)
	ch := h - s.hc(b) + s.hc(t)
	cid := s.add(child, np, id, b, dir, nil, s.g[id]+1, ch, s.w[id]+wd, r)
	s.occ[b], s.occ[t] = true, false
	return cid, ch
}

// SideDead enables player-side dead squares (package sidedead).
var SideDead = true

// DiagDead (exp-459) prunes diagonal-chain deadlocks (package diagdead); main sets it from -diag-dead.
var DiagDead = func() bool { v := os.Getenv("SOKSOLV_DIAG_DEAD"); return v == "1" || v == "all" || v == "true" }()

// FreeGoal scores a box by its push distance to the nearest goal no box
// holds, instead of the nearest goal (which another box may already fill).
var FreeGoal = true

// noGoalCost is a box's cost when no free goal is reachable.
const noGoalCost = 4096

// setCosts starts a pop: it lists the free goals (s.occ holds cur) and
// returns the state's h, the sum of hc over its boxes. Child keys use the
// parent's free goals (hc is memoized per pop); a child's exact h is
// recomputed when it is popped.
func (s *solver) setCosts(cur []int32) int32 {
	s.ccStamp++
	if s.ccStamp == 0 {
		clear(s.ccMark)
		s.ccStamp = 1
	}
	s.freeG = s.freeG[:0]
	for g, c := range s.goals {
		if !s.occ[c] {
			s.freeG = append(s.freeG, int32(g))
		}
	}
	var h int32
	for _, b := range cur {
		h += s.hc(b)
	}
	return h
}

// hc is the cost of a box on c: 0 on a goal, else the distance to the
// nearest free goal (or to the nearest goal without FreeGoal).
func (s *solver) hc(c int32) int32 {
	b := s.hcBase(c)
	if s.noise != nil && !s.lv.Goal[c] {
		return b*4 + s.noise[c] // perturbed restart: never 0 off a goal
	}
	return b
}

func (s *solver) hcBase(c int32) int32 {
	if !s.freeGoal || s.lv.Goal[c] {
		return s.dist[c]
	}
	if s.ccMark[c] == s.ccStamp {
		return s.cc[c]
	}
	cells := int32(len(s.lv.Wall))
	best := int32(noGoalCost)
	for _, g := range s.freeG {
		if d := s.goalDist[g*cells+c]; d >= 0 && d < best {
			best = d
		}
	}
	s.ccMark[c], s.cc[c] = s.ccStamp, best
	return best
}

// perturb gives restart seed > 0 per-cell offsets in [0, 4*seed) on box
// costs (scaled by 4), so repeated restarts explore differently.
func (s *solver) perturb(seed int) {
	if seed <= 0 {
		return
	}
	s.noise = make([]int32, len(s.lv.Wall))
	x := uint64(seed)*0x9E3779B97F4A7C15 + 0x2545F4914F6CDD1D
	for c := range s.noise {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		s.noise[c] = int32(x % uint64(4*seed))
	}
}

// hash mixes a state's box bitset and normalized player cell.
func hash(boxes []uint64, player int32) uint64 {
	h := uint64(player)*0x9E3779B97F4A7C15 + 1
	for _, b := range boxes {
		h = (h ^ b) * 0xBF58476D1CE4E5B9
		h ^= h >> 31
	}
	return h
}

// initBitsets sets up the floor-cell indexing used to store states as bitsets.
func (s *solver) initBitsets() {
	lv := s.lv
	s.fidx = make([]int32, len(lv.Wall))
	for c, w := range lv.Wall {
		s.fidx[c] = -1
		if !w {
			s.fidx[c] = int32(len(s.fcell))
			s.fcell = append(s.fcell, int32(c))
		}
	}
	s.nw = (len(s.fcell) + 63) / 64
	s.key = make([]uint64, s.nw)
}

// pack writes the bitset of boxes into s.key.
func (s *solver) pack(boxes []int32) []uint64 {
	clear(s.key)
	for _, b := range boxes {
		f := s.fidx[b]
		s.key[f>>6] |= 1 << (f & 63)
	}
	return s.key
}

// unpack fills cur with node id's boxes in ascending cell order.
func (s *solver) unpack(id int32, cur []int32) {
	i := 0
	for w, x := range s.boxes[int(id)*s.nw : int(id+1)*s.nw] {
		for x != 0 {
			cur[i] = s.fcell[w*64+bits.TrailingZeros64(x)]
			i++
			x &= x - 1
		}
	}
}

// find returns the table slot for a state and the node id stored there,
// or -1 if the state is new (the slot is then where it should be inserted).
// The returned fingerprint must be stored in s.tfp[slot] on insertion; it lets
// probes skip most non-matching slots without comparing boxes.
func (s *solver) find(boxes []uint64, player int32) (int, int32, uint32) {
	mask := len(s.table) - 1
	h := hash(boxes, player)
	fp := uint32(h >> 32)
	for i := int(h) & mask; ; i = (i + 1) & mask {
		id := s.table[i]
		if id < 0 || s.tfp[i] == fp && s.player[id] == player && slices.Equal(s.boxes[int(id)*s.nw:int(id+1)*s.nw], boxes) {
			return i, id, fp
		}
	}
}

// grow doubles the table and reinserts every node.
func (s *solver) grow() {
	s.table = make([]int32, 2*len(s.table))
	s.tfp = make([]uint32, len(s.table))
	for i := range s.table {
		s.table[i] = -1
	}
	for id := range int32(len(s.player)) {
		slot, _, fp := s.find(s.boxes[int(id)*s.nw:int(id+1)*s.nw], s.player[id])
		s.table[slot], s.tfp[slot] = id, fp
	}
}

// add inserts a new node, or records a shorter path to an existing one.
// w is the node's accumulated FESS weight; reg its region count, or -1 to
// compute it from s.occ (which must hold the node's boxes).
// It returns the new node's id, or -1 if the state was already stored.
func (s *solver) add(boxes []int32, player, parent, from int32, dir int8, seq []push, g, h, w int32, reg int) int32 {
	packed := s.pack(boxes)
	slot, id, fp := s.find(packed, player)
	if id >= 0 {
		s.stats.Duplicates++
		if s.g[id] <= g {
			return -1
		}
		// Keep the shorter path but don't re-queue: greedy order ignores g.
		// Parent g only ever decreases, so parent chains stay acyclic.
		s.parent[id], s.pushFrom[id], s.pushDir[id], s.g[id] = parent, from, dir, g
		if seq != nil {
			s.macros[id] = slices.Clone(seq)
		} else {
			delete(s.macros, id)
		}
		return -1
	}
	if s.memFull {
		return -1
	}
	if s.sd != nil && parent >= 0 {
		for _, c := range boxes {
			if !s.lv.Goal[c] && s.sd.Dead(c, player) {
				s.stats.SideDead++
				return -1
			}
		}
	}
	id = int32(len(s.player))
	if soloracle.On {
		soloracle.Probe("F-full", boxes, player)
	}
	s.table[slot], s.tfp[slot] = id, fp
	if seq != nil {
		s.macros[id] = slices.Clone(seq)
	}
	s.boxes = append(s.boxes, packed...)
	s.player = append(s.player, player)
	s.parent = append(s.parent, parent)
	s.pushFrom = append(s.pushFrom, from)
	s.pushDir = append(s.pushDir, dir)
	s.g = append(s.g, g)
	s.w = append(s.w, w)
	if reg < 0 {
		reg = s.regions()
	}
	s.reg = append(s.reg, int8(min(reg, 127)))
	onGoal := s.packed()
	bucket := onGoal*maxRegions + min(reg, maxRegions-1)
	s.bucket = append(s.bucket, uint16(bucket))
	s.mvStart = append(s.mvStart, -1)
	s.mvLen = append(s.mvLen, 0)
	s.mvNext = append(s.mvNext, 0)
	s.mvLeft = append(s.mvLeft, 0)
	s.push(h, w, id, bucket)
	if s.fwd != nil {
		k := s.fwd.Key(boxes, player)
		s.fwd.Add(k)
		if s.back != nil && s.meetPosted < maxMeetPosts && s.back.Has(k) {
			s.meetPending = append(s.meetPending, id)
		}
	}
	if len(s.player)*4 >= len(s.table)*3 {
		// Doubling allocates the new table while the old one is live. If that
		// would pass the budget, stop at the next check instead: an RSS
		// overshoot gets the whole process (every strategy) killed.
		if b := s.budget.Load(); b > 0 && s.trackedBytes()+uint64(len(s.table))*16 > b {
			s.memFull = true
		} else {
			s.grow()
		}
	}
	return id
}

// Trace, if set, receives this search's progress (diagnostics only).
var Trace *trace.Recorder

func (s *solver) solve(deadline time.Time, memLimit uint64) (res Result) {
	expanded := 0
	defer func() {
		s.tr.SetExtra("lean_at", s.stats.LeanAt)
		if os.Getenv("SOKSOLV_DEBUG_MEM") != "" {
			fmt.Fprintf(os.Stderr, "full mem: states=%d %v\n", len(s.player), s.memBreakdown())
		}
		if Penalties {
			s.tr.SetExtra("penalized", s.stats.Penalized)
		}
		s.tr.Finish(trace.Ended(res.Solved, res.Reason), expanded, len(s.player), s.trackedBytes())
	}()
	lv := s.lv
	s.pairOffsets()
	s.dpair = make([]int8, len(lv.Wall)*len(s.poff))
	s.back, s.fwd = meet.Back.Load(), meet.Fwd.Load()
	start := slices.Clone(lv.Boxes)
	slices.Sort(start)
	var h0 int32
	for _, b := range start {
		if s.dist[b] < 0 {
			return Result{Reason: "box on dead square"}
		}
		h0 += s.dist[b]
	}
	for _, b := range start {
		s.occ[b] = true
	}
	p0 := s.reach(lv.Player, s.mark)
	s.add(start, p0, -1, -1, -1, nil, 0, h0, 0, -1) // with s.occ set, for the root's features
	for _, b := range start {
		s.occ[b] = false
	}

	s.tr.Root(int(h0))
	if h0 == 0 {
		return Result{Solved: true, Solution: "", Stats: s.finalStats()}
	}

	// FESS-style search, one move at a time: popping a node applies only its
	// next-best move (opening the node first if needed), adds that child, and
	// re-queues the node in the same heap keyed by its following move. Only
	// children actually chosen are stored.
	cur := make([]int32, s.n)
	child := make([]int32, s.n)
	var lastMemCheck time.Time
	for {
		if s.memFull {
			return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
		}
		id, src, ok := s.pop()
		if !ok {
			break
		}
		expanded++
		if NodeBudget > 0 && int64(expanded) >= NodeBudget {
			return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
		}
		// Periodic work: every 256 expansions.
		periodic, memCheck := expanded&255 == 0, false
		if periodic {
			now := time.Now()
			if NodeBudget == 0 && now.After(deadline) || Stop.Load() {
				return Result{Reason: "timeout", Nodes: expanded, Stats: s.finalStats()}
			}
			if sol, ok := s.checkMail(); ok {
				s.tr.SetExtra("met", "reverse")
				return Result{Solved: true, Solution: sol, Nodes: expanded, Stats: s.finalStats()}
			}
			if now.Sub(lastMemCheck) > 50*time.Millisecond {
				lastMemCheck, memCheck = now, true
			}
			if memCheck {
				tracked, budget := s.trackedBytes(), s.budget.Load()
				s.tracked.Store(tracked)
				if memtrace.Enabled {
					memtrace.Publish(fmt.Sprintf("full-o%d", s.owner), len(s.player), s.memBreakdown(), s.memLen())
				}
				s.tr.Sample(expanded, len(s.player), tracked)
				if budget > 0 && tracked > budget || mappedOver(memLimit, 92) {
					return Result{Reason: "memory", Nodes: expanded, Stats: s.finalStats()}
				}
				if !s.lean && budget > 0 && tracked > budget/100*60 {
					s.lean = true // from now on: one move per pop
					s.stats.LeanAt = expanded
				}
			}
		}
		s.unpack(id, cur)
		packed := 0
		for _, b := range cur {
			s.occ[b] = true
			if lv.Goal[b] {
				packed++
			}
		}
		h := s.setCosts(cur) // per-box costs for this pop's move keys
		s.tr.Step(expanded, int(h), packed)
		s.reach(s.player[id], s.mark)
		st := s.stamp
		if s.mvStart[id] < 0 {
			s.open(id, cur, h, st)
		}
		// While memory is plentiful, apply all of the node's moves (full
		// expansion). Once lean, apply moves only until one adds a new state:
		// duplicates reuse this pop's unpack and reachability.
		solved := int32(-1)
		for added := false; s.mvLeft[id] > 0 && (!s.lean || !added); {
			k := s.nextMove(id, src, cur, h)
			m := s.mv[k]
			s.mv[k] |= moveUsed
			s.mvLeft[id]--
			if cid, ch := s.apply(id, m, cur, child, h, st); cid >= 0 {
				added = true
				if ch == 0 {
					solved = cid
					break
				}
			}
		}
		if s.mvLeft[id] > 0 {
			nh, nw := s.moveKeyAt(id, s.nextMove(id, src, cur, h), cur, h)
			s.pushTo(src, nh, nw, id)
		}
		for _, b := range cur {
			s.occ[b] = false
		}
		if len(s.meetPending) > 0 {
			s.postMeetings(cur)
		}
		if solved >= 0 {
			return Result{Solved: true, Solution: s.reconstruct(solved), Nodes: expanded, Stats: s.finalStats()}
		}
	}
	return Result{Reason: "exhausted", Nodes: expanded, Stats: s.finalStats()}
}

// maxMeetPosts caps the meetings one search posts (each costs a path
// reconstruction; stale perimeter entries from earlier backward phases can
// hit without confirming).
const maxMeetPosts = 2000

// postMeetings sends the pending perimeter hits, with their forward paths, to
// the backward search (s.occ must be clear; cur is scratch).
func (s *solver) postMeetings(cur []int32) {
	for _, id := range s.meetPending {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		s.meetPosted++
		prefix := s.reconstruct(id)
		s.unpack(id, cur)
		s.back.Post(meet.Meeting{Boxes: slices.Clone(cur), Player: s.player[id], Prefix: prefix,
			At: meet.EndCell(prefix, s.lv.Player, s.lv.W), From: "full"})
	}
	s.meetPending = s.meetPending[:0]
}

// checkMail resolves meetings posted by the backward search: a posted state
// that is in this search's table joins this search's path to it with the
// backward search's pushes from it to the solved position. s.occ must be
// clear.
func (s *solver) checkMail() (string, bool) {
	if s.fwd == nil {
		return "", false
	}
	for {
		m, ok := s.fwd.Receive(s.owner)
		if !ok {
			return "", false
		}
		if len(m.Boxes) != s.n || slices.ContainsFunc(m.Boxes, func(b int32) bool { return s.fidx[b] < 0 }) {
			continue
		}
		if _, id, _ := s.find(s.pack(m.Boxes), m.Player); id >= 0 {
			s.fwd.Confirmed.Add(1)
			prefix := s.reconstruct(id)
			at := meet.EndCell(prefix, s.lv.Player, s.lv.W)
			if tail := meet.Complete(s.lv.Wall, s.lv.W, m.Boxes, at, m.Tail); tail != "" || len(m.Tail) == 0 {
				return prefix + tail, true
			}
		}
	}
}

// reconstruct replays the push sequence and fills in player walks with BFS.
func (s *solver) reconstruct(id int32) string {
	var pushes [][2]int32
	for ; s.parent[id] >= 0; id = s.parent[id] {
		if seq, ok := s.macros[id]; ok {
			for k := len(seq) - 1; k >= 0; k-- {
				pushes = append(pushes, [2]int32{seq[k].from, int32(seq[k].dir)})
			}
			continue
		}
		pushes = append(pushes, [2]int32{s.pushFrom[id], int32(s.pushDir[id])})
	}
	slices.Reverse(pushes)

	lv := s.lv
	for _, b := range lv.Boxes {
		s.occ[b] = true
	}
	prev := make([]int8, len(lv.Wall))
	var out []byte
	p := lv.Player
	for _, pu := range pushes {
		from, di := pu[0], pu[1]
		target := from - s.dirs[di]
		// BFS from p to target, recording the direction used to enter each cell.
		s.stamp++
		st := s.stamp
		q := append(s.queue[:0], p)
		s.mark[p] = st
		for i := 0; i < len(q) && s.mark[target] != st; i++ {
			c := q[i]
			for k, d := range s.dirs {
				nb := c + d
				if s.mark[nb] != st && !lv.Wall[nb] && !s.occ[nb] {
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
		out = append(out, dirChars[di]-'a'+'A')
		s.occ[from], s.occ[from+s.dirs[di]] = false, true
		p = from
	}
	for i := range s.occ {
		s.occ[i] = false
	}
	return string(out)
}

func (s *solver) push(h, w, id int32, bucket int) {
	if s.buckets == nil {
		s.buckets = make([][]hkey, (s.n+1)*maxRegions)
	}
	s.heap = heapPush(s.heap, mkKey(h, w, id)) // ties: lower FESS weight first
	s.buckets[bucket] = heapPush(s.buckets[bucket], mkKey(w, h, id))
}

// pushTo re-queues node id in one heap: src -1 is the global greedy heap,
// otherwise the feature-cell heap with that index.
func (s *solver) pushTo(src int, h, w, id int32) {
	if src < 0 {
		s.heap = heapPush(s.heap, mkKey(h, w, id))
	} else {
		s.buckets[src] = heapPush(s.buckets[src], mkKey(w, h, id))
	}
}

func (s *solver) exhausted(id int32) bool {
	return s.mvStart[id] >= 0 && s.mvLeft[id] == 0
}

// moveUsed marks applied moves (bit 31 of the encoded move).
const moveUsed = 1 << 31

// nextMove returns the index (into s.mv) of the move node id should apply when
// popped from heap src: the next unused move in FESS order for a feature cell,
// the unused move with the lowest child h for the global greedy heap.
func (s *solver) nextMove(id int32, src int, cur []int32, h int32) int32 {
	start, end := s.mvStart[id], s.mvStart[id]+int32(s.mvLen[id])
	if src >= 0 {
		k := start + int32(s.mvNext[id])
		for s.mv[k]&moveUsed != 0 {
			k++
		}
		s.mvNext[id] = uint16(k - start)
		return k
	}
	best, bh := int32(-1), int32(math.MaxInt32)
	for k := start; k < end; k++ {
		if s.mv[k]&moveUsed == 0 {
			if ch, _ := s.moveKeyAt(id, k, cur, h); ch < bh {
				best, bh = k, ch
			}
		}
	}
	return best
}

// pop alternates between the best entry overall and the best entry of the
// next non-empty feature cell, skipping nodes whose moves are used up. It
// returns the node and the heap it came from (-1 = global).
func (s *solver) pop() (int32, int, bool) {
	s.turn++
	if s.turn%2 == 0 {
		for range len(s.buckets) {
			src := s.rr
			b := &s.buckets[src]
			s.rr = (s.rr + 1) % len(s.buckets)
			for len(*b) > 0 {
				var k hkey
				k, *b = heapPop(*b)
				if id := k.id(); !s.exhausted(id) {
					return id, src, true
				}
			}
		}
	}
	for len(s.heap) > 0 {
		var k hkey
		k, s.heap = heapPop(s.heap)
		if id := k.id(); !s.exhausted(id) {
			return id, -1, true
		}
	}
	return 0, 0, false
}

func heapPush(h []hkey, it hkey) []hkey {
	h = append(h, it)
	i := len(h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if h[i] >= h[p] {
			break
		}
		h[i], h[p] = h[p], h[i]
		i = p
	}
	return h
}

func heapPop(h []hkey) (hkey, []hkey) {
	top := h[0]
	last := len(h) - 1
	h[0] = h[last]
	h = h[:last]
	for i := 0; ; {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(h) && h[l] < h[m] {
			m = l
		}
		if r < len(h) && h[r] < h[m] {
			m = r
		}
		if m == i {
			break
		}
		h[i], h[m] = h[m], h[i]
		i = m
	}
	return top, h
}

func (s *solver) finalStats() Stats {
	st := s.stats
	st.Stored = len(s.player)
	st.MemMB = s.memBreakdown()
	return st
}

// memBreakdown reports the capacity (MB) held by the large per-node and
// per-queue structures, for introspection.
func (s *solver) memBreakdown() map[string]float64 {
	mb := func(n int, size int) float64 { return math.Round(float64(n*size)/(1<<20)*10) / 10 }
	bucketCap := 0
	for _, b := range s.buckets {
		bucketCap += cap(b)
	}
	macroLen := 0
	for _, m := range s.macros {
		macroLen += cap(m)
	}
	return map[string]float64{
		"boxes":    mb(cap(s.boxes), 8),
		"player":   mb(cap(s.player), 4),
		"parent":   mb(cap(s.parent), 4),
		"pushFrom": mb(cap(s.pushFrom), 4),
		"pushDir":  mb(cap(s.pushDir), 1),
		"g":        mb(cap(s.g), 4),
		"w":        mb(cap(s.w), 4),
		"reg":      mb(cap(s.reg), 1),
		"moves":    mb(cap(s.mv), 4) + mb(cap(s.mvStart), 4) + mb(cap(s.mvLen), 2) + mb(cap(s.mvNext), 2) + mb(cap(s.mvLeft), 2) + mb(cap(s.bucket), 2),
		"table":    mb(cap(s.table), 4) + mb(cap(s.tfp), 4),
		"heap":     mb(cap(s.heap), 8),
		"buckets":  mb(bucketCap, 8),
		"macros":   mb(macroLen, 8) + mb(len(s.macros), 48),
	}
}

// memLen mirrors memBreakdown with lengths instead of capacities
// (memtrace: the difference is append-doubling slack).
func (s *solver) memLen() map[string]float64 {
	mb := func(n int, size int) float64 { return math.Round(float64(n*size)/(1<<20)*10) / 10 }
	bucketCap := 0
	for _, b := range s.buckets {
		bucketCap += cap(b)
	}
	macroLen := 0
	for _, m := range s.macros {
		macroLen += cap(m)
	}
	return map[string]float64{
		"boxes":    mb(len(s.boxes), 8),
		"player":   mb(len(s.player), 4),
		"parent":   mb(len(s.parent), 4),
		"pushFrom": mb(len(s.pushFrom), 4),
		"pushDir":  mb(len(s.pushDir), 1),
		"g":        mb(len(s.g), 4),
		"w":        mb(len(s.w), 4),
		"reg":      mb(len(s.reg), 1),
		"moves":    mb(len(s.mv), 4) + mb(len(s.mvStart), 4) + mb(len(s.mvLen), 2) + mb(len(s.mvNext), 2) + mb(len(s.mvLeft), 2) + mb(len(s.bucket), 2),
		"table":    mb(len(s.table), 4) + mb(len(s.tfp), 4),
		"heap":     mb(len(s.heap), 8),
		"buckets":  mb(bucketCap, 8),
		"macros":   mb(macroLen, 8) + mb(len(s.macros), 48),
	}
}

// Solve runs the full-expansion search (switching to one move per pop when
// memory gets tight) on one level's rows.
func Solve(rows []string, deadline time.Time, memLimit uint64) (Result, error) {
	return SolveWith(rows, deadline, memLimit, Slot{Budget: &Budget, Tracked: &Tracked, Trace: Trace}, FreeGoal, 0)
}

// SolveWith runs the forward search with the given resources and heuristic
// (free-goal or nearest-goal box costs).
func SolveWith(rows []string, deadline time.Time, memLimit uint64, slot Slot, freeGoal bool, seed int) (Result, error) {
	lv, err := parseLevel(rows)
	if err != nil {
		return Result{}, err
	}
	s := newSolver(lv)
	s.budget, s.tracked, s.tr, s.freeGoal, s.owner = slot.Budget, slot.Tracked, slot.Trace, freeGoal, slot.Owner
	s.perturb(seed)
	if SideDead {
		s.sd = sidedead.New(lv.Wall, lv.Goal, lv.W)
	}
	if DiagDead {
		s.dd = diagdead.New(lv.Wall, lv.Goal, lv.W)
	}
	s.initCluster()
	return s.solve(deadline, memLimit), nil
}

// Slot is the resources a bridge search uses: its memory budget, where it
// reports its tracked bytes, and its trace.
type Slot struct {
	Budget, Tracked *atomic.Uint64
	Trace           *trace.Recorder
	Owner           int // mailbox on the forward perimeter (0 full, 1 macro)
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
	var t float64
	for _, v := range s.memBreakdown() {
		t += v
	}
	return uint64(t*(1<<20)) + s.fwd.Bytes() // the forward perimeter is charged here
}

// ClusterDead enables cluster deadlocks (package cluster).
var ClusterDead = true

func (s *solver) initCluster() {
	if !ClusterDead {
		return
	}
	dead := make([]bool, len(s.lv.Wall))
	for c, d := range s.dist {
		dead[c] = d < 0
	}
	bd := cluster.NewBoard(s.lv.Wall, s.lv.Goal, dead, s.lv.W)
	if Penalties {
		bd.Dist = s.dist
	}
	s.cl = cluster.NewChecker(bd)
}

// Penalties (exp-155): a child whose pushed box forms an alive cluster with
// a learned penalty (true cluster push cost minus the boxes' goal distances,
// package cluster, shared between searchers) carries it in its queue key.
// Keys only: h is recomputed exactly when the node is popped.
var Penalties = true

// NodeBudget (> 0): stop after this many expansions and ignore the wall
// clock (deterministic screening; set by the main package's -nodes).
var NodeBudget int64
