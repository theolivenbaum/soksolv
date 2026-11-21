package main

import (
	"container/heap"
	"fmt"
	"math"
	"math/bits"
	"runtime/metrics"
	"slices"
	"soksolv/cluster"
	"soksolv/deadzone"
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

// Full FESS over box macro states. States use box bitsets and the minimum
// player-reachable cell. A bounded backward sink search supplies packing and
// parking checkpoints. Four-dimensional feature cells are served cyclically;
// each selection applies one least-weight unexpanded macro, breaking ties by
// child features. Seven advisors assign zero cost to promising macros; other
// macros cost one. Nearest-goal distance is used for feasibility, not ordering.

const dirChars = "udlr"

// Stop asks a running search to give up at its next time check (used by the
// portfolio when another strategy has already solved the level).
var Stop atomic.Bool

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
	DeadEnds              int                `json:"dead_ends,omitempty"`
	PlanExpanded          int                `json:"plan_expanded,omitempty"`
	PlanMs                int64              `json:"plan_ms,omitempty"` // exp-530: fa2's planning wall time
	PlanPhases            int                `json:"plan_phases,omitempty"`
	PlanParkings          int                `json:"plan_parkings,omitempty"`
	PlanRemaining         int                `json:"plan_remaining,omitempty"`
	PlanComplete          bool               `json:"plan_complete,omitempty"`
	Plan4Complete         bool               `json:"plan4_complete,omitempty"` // exp-563: the plan4 attempt ended with a complete witness
	Plan4Exp              int                `json:"plan4_exp,omitempty"`
	Plan4Ms               int                `json:"plan4_ms,omitempty"`
	Plan4Pending          bool               `json:"plan4_pending,omitempty"`
	Plan4Swap             int                `json:"plan4_swap,omitempty"` // macro expansion count at which the anytime plan replaced the fallback
	PlanEvents            int                `json:"plan_events,omitempty"`
	PlanDependencies      int                `json:"plan_dependencies,omitempty"`
	PlanCompileVisits     int                `json:"plan_compile_visits,omitempty"`
	PackingPrepQueries    int                `json:"packing_prep_queries,omitempty"`
	PackingPrepPicks      int                `json:"packing_prep_picks,omitempty"`
	PackingPrepClears     int                `json:"packing_prep_clears,omitempty"`
	MemMB                 map[string]float64 `json:"mem_mb,omitempty"` // capacity held per structure
	Generated             int                `json:"generated"`        // children that passed all pruning
	Duplicates            int                `json:"duplicates"`       // generated children already in the table
	Stored                int                `json:"stored"`           // states in the table at the end
	DeadSquare            int                `json:"pruned_dead_square"`
	Frozen2x2             int                `json:"pruned_2x2"`
	GoalArea              int                `json:"pruned_goal_area"` // pushes leaving a goal unreachable behind frozen goal boxes
	SideDead              int                `json:"pruned_side_dead"`
	Inefficient           int                `json:"pruned_inefficient"`
	ClusterDead           int                `json:"pruned_cluster"`
	RegRelTargets         int                `json:"regrel_targets"`  // exp-460: targets with pocket cells
	RegRelAdj             int                `json:"regrel_adjusted"` // exp-460: children whose region count dropped
	Freeze                int                `json:"pruned_freeze"`
	Assignment            int                `json:"pruned_assignment"` // expanded states with no feasible box-goal assignment
	CorridorFlow          int                `json:"pruned_corridor_flow,omitempty"`
	FrozenHall            int                `json:"pruned_frozen_hall,omitempty"`
	DiagDead              int                `json:"pruned_diag,omitempty"` // diagonal-chain deadlocks (exp-459)
	Novel                 int                `json:"novel_moves,omitempty"` // children matching a learned pattern (package learn)
	Reopens               int                `json:"reopens,omitempty"`     // MoveWindow: nodes re-opened for their next window
	Macros                int                `json:"goal_macros"`           // goal-macro children generated
	LeanAt                int                `json:"lean_at,omitempty"`     // pop count when one-move mode started
	ConnAdvisor           int                `json:"conn_advisor"`          // pushes chosen by the connectivity advisor
	RoomAdvisor           int                `json:"room_advisor"`
	Advisors              [advisorCount]int  `json:"advisors,omitempty"` // packing, connectivity, rooms, hotspots, explorer, opener, OOP, displacement
	Corrals               int                `json:"corral_expansions"`  // expansions restricted by a PI-corral
	FrontierCorrals       int                `json:"frontier_corrals"`
	PairDead              int                `json:"pruned_pair"`             // pushes into a precomputed two-box deadlock
	PairsDead             int                `json:"pair_table_dead"`         // two-box deadlocks found in preprocessing
	PairsTried            int                `json:"pair_table_tried"`        // pairs analysed before the budget ran out
	PathAdvisors          [advisorCount]int  `json:"path_advisors,omitempty"` // solution-path macro moves chosen by each advisor
	PathMoves             int                `json:"path_moves,omitempty"`    // macro moves on the solution path
	CorralDead            int                `json:"pruned_corral_dead,omitempty"`
	RetargetRuns          int                `json:"retarget_runs,omitempty"`
	RetargetRegenAdded    int                `json:"retarget_regen_added,omitempty"`
	RetargetTotalAdded    int                `json:"retarget_total_added,omitempty"`
	RetargetRegenExpanded int                `json:"retarget_regen_expanded,omitempty"`
	RetargetTotalExpanded int                `json:"retarget_total_expanded,omitempty"`
	RetargetPrevNodes     int                `json:"retarget_prev_nodes,omitempty"`
	RetargetActiveNodes   int                `json:"retarget_active_nodes,omitempty"`
	RetargetSetupMs       float64            `json:"retarget_setup_ms,omitempty"`
}

// push is one box push: the box at from moves one cell in direction dir.
type push struct {
	from int32
	dir  int8
}

type solver struct {
	meetAudit          *meetAuditState // exp-545, diagnostic-only sequential census
	dead               []bool
	edgeHead           []int32
	liveChildren       []uint32
	edges              []fessEdge
	lastAdded          int32
	setupDeadline      time.Time
	nodeLimit          int64     // optional phase expansion limit
	fa2Preset          *festPlan // exp-549: embedded fa2 engine: the macro lane's plan4 event list
	oopTail            bool      // exp-549: per-solver OOPTailFix (embedded fa2)
	fa2Lane            bool      // exp-594: an engine of the anytime fa2 lane
	expandedN          int64     // expansions so far (NodeBudget)
	bat                *baton    // exp-693: -dive-share: the lane's core is shared with the dive (nil: alone)
	batMe              int       // exp-693: this engine's baton index
	diveAOn            bool      // exp-693 -dive-pair: this dive engine uses strategy A's terms
	budgetDiv          uint64    // exp-693: engines sharing the lane's budget (0, 1: all of it)
	frontier           fessFrontier
	actual             []featureCell
	version            []uint32
	deadline           time.Time
	cancelled          bool
	hot                *hotspotTable
	basin              *packingBasin
	plan               *packingPlan
	packReady          []bool
	packUse, packBlock []int
	packPrepared       bool
	mvScore            []moveScore
	rooms              *roomMap
	cellIDs            map[featureCell]int
	lv                 *Level
	n                  int
	dirs               [4]int32
	rg                 *reach.Graph // exp-464 (-exact-reach): player-side access graph; nil = relaxed tables
	dist               []int32      // push distance from cell to nearest goal; -1 = dead square

	goals     []int32
	goalDist  []int32 // goalDist[g*cells+c]: pushes to bring a box from c to goals[g]; -1 = never
	hu, hv    []int32 // Hungarian algorithm scratch, all sized n+1
	hp, hway  []int32
	hminv     []int32
	hused     []bool
	frLost1   []uint64 // festRooms1 scratch
	hul, hud  []int32  // augmentAssignment scratch: used columns and the D at which each joined
	hfDual    []int32  // parent duals, matching and inverse matching for one-row updates
	hfCost    int64
	matchOf   []int32  // feasible: goal index -> matched box index, or -1
	gseen     []uint64 // feasible: visited goal bitset
	gfree     []uint64 // feasible: unmatched goal bitset
	goalReach []uint64 // goalReach[c*goalW+w]: bit g set when goalDist[g*cells+c] >= 0 (feasible)
	goalW     int

	occ     []bool
	mark    []uint32         // reachability stamps for the expanded state
	mark2   []uint32         // reachability stamps for child normalization
	fz      []bool           // boxes currently under freeze examination
	fhc     *frozenHallCache // exp-681b -frozen-hall tables per frozen set
	fzNo    []uint32         // pull freeze: box proven not frozen in query fzEpoch
	fzEpoch uint32
	fzLeft  int      // pull freeze: box visits left in this query
	cmark   []uint32 // corral component stamps
	cf      corralFrontier
	poff    []int32 // two-box deadlock table: neighbor offsets
	popp    []int
	pvis    []uint64 // pairDeadFrom: visited states (flat, generation-stamped)
	pgen    uint16
	pq2     []st2
	dpair   []int8 // dpair[cell*len(poff)+k] for boxes on cell and cell+poff[k]: 0 unknown, 1 dead, 2 alive
	pocc    []bool // pairDead scratch: private board, marks, queue
	pmark   []uint32
	pseen   []uint32
	pq      []int32
	pst     uint32
	pst2    uint32
	cstamp  uint32
	stamp   uint32
	queue   []int32

	// node arena
	boxes    []uint64 // nw words per node: bitset of box cells over floor indices
	key      []uint64 // scratch: the state being looked up
	revKey   []uint64 // scratch: candidate state bitset in open() (exp-290)
	nw       int      // words per state
	fidx     []int32  // cell -> floor index (-1 for walls)
	fcell    []int32  // floor index -> cell
	player   []int32
	parent   []int32
	pushFrom []int32
	pushTo   []int32 // destination cell of the move that created each node (exp-290)
	pushDir  []int8
	revGroup []int32  // reversible preparation group root representative (exp-290)
	revSize  []uint16 // reversible preparation group size (exp-290)
	revExits u64set   // per-group exit deduplication set (exp-290)
	g        []int32
	w        []int32           // per node: accumulated FESS weight (non-advisor moves)
	reg      []uint16          // per node: exact empty-floor region count
	adv      []uint8           // per node: advisors of the move that reached it (diagnostics)
	art      articulation      // incremental child region counts
	ga       goalArea          // goal-area deadlock scratch and cache
	sd       *sidedead.Table   // player-side dead squares
	dd       *diagdead.Checker // diagonal-chain deadlocks (exp-459; nil: off)
	pairSide []int8            // pairSideDeadlock verdicts per (cell, pair offset, player side): 0 unknown, 1 dead, 2 alive
	fm       *flowManager      // corridor net flow and transit capacity manager (exp-330)

	// Per-instance resources (two FESS searchers can run at once).
	budgetP, trackedP        *atomic.Uint64
	owner                    int        // mailbox on the forward perimeter
	rows                     []string   // the level as given (private reverse searches)
	pathOnly                 bool       // boxSearch reconstructs a known path: no expiry
	novSeen                  [][]uint64 // per FESS cell: squares a box has reached (novelty, exp-192)
	novelty                  int        // novel moves per opened node that cost nothing (0: off)
	novPairs                 u64set
	novPushSeen              [][]uint64 // per FESS cell: push-sides (cell*4 + dirIdx) reached (AccessNovelty, exp-289)
	novAccess                u64set     // per FESS cell: width-2 access capability keys (AccessNovelty, exp-289)
	accBuf                   []uint64   // scratch buffer for candidate access facts across open()
	hfRank                   bool       // toward search ranks like Festival's forward HF search (exp-189)
	tgt                      []bool     // target board's squares (toward search on the real level; nil: none)
	tgtList                  []int32
	regRel                   bool      // exp-480: build tgtPocket for this solver even when -hf-reg-rel is off (hub forward contexts)
	tgtPocket                []bool    // exp-460 -hf-reg-rel: target-sealed pocket cells (nil: off)
	tgtDist                  []int32   // like goalDist, for tgtList
	dtSrc, dtT               []int32   // MatchTranspose: cached transpose of dtSrc
	dtSrc2, dtT2             []int32   // exp-691: the dive's second cached transpose
	bb                       *bitBoard // BoxBitFlood rows (nil: board wider than 64)
	noPlan                   bool      // no packing plan: packed counts boxes on goals (exp-188 toward search)
	pull                     bool      // backward search: pull moves, start squares as goals (exp-179)
	realRows                 []string  // pull: the real level's rows (target probes, exp-252)
	realPlayer               int32     // pull: the real level's player (the search ends in its region)
	pullRoots                []int32   // pull: root player cells, one per zone of the solved position
	lastPub                  int32     // pull: last published best node
	connFirst                bool      // rank moves by connectivity first (Festival's HF order; pull and toward searches)
	spreadDist               []int32   // pull: push distance to the real goals (spread = sum over boxes, larger is better)
	refs                     []uint64  // pull: candidate key -> reference pairs (flat, open addressing)
	refN                     int
	candKey                  []int32          // pull: scratch child boxes
	pendCands                []pendingCand    // pull: candidate children already in the forward perimeter
	lastPubAt                time.Time        // pull: when it was published
	planBits                 []uint64         // inPlan: plan squares, one bit per cell
	candBuf                  []macroCandidate // open(): reused candidate list
	wBuf                     []int32          // open(): reused weights
	ordBuf                   []int            // open(): reused order
	keyBuf                   []moveKey        // open(): reused sort keys (exp-638)
	oopBase, oopBaseSt       []int32          // oopChild (exp-659): the opened node's OOP count per phase+1, stamped
	oopSt                    int32
	pkMark                   []int32 // packedChild (exp-659): plan cells of the opened node's plan, stamped
	pkSt                     int32
	pkVal                    int
	pkHave                   bool
	maskBuf                  []uint64 // open(): reused advisor masks
	walkTab                  []int16  // walk(): all floor-pair walking distances (walkF x walkF)
	walkIdx                  []int32  // cell -> floor index for walkTab
	walkF                    int
	walkScratch, walkQ       []int32
	lazyBoxes                []int32
	retargetNodes            int32
	ineffOn                  bool              // InefficientPrune for this instance
	cl                       *cluster.Checker  // cluster deadlocks (shared table)
	dz                       *deadzone.Checker // corral deadlocks (exp-227)
	lastImprove, searchStart time.Time
	stallTries, stallRuns    int
	tgtFull                  *meet.Target // the toward search's target (its player and tail)
	targetRows               []string
	fc                       festCorralScratch // exp-269
	pfDist                   []int32           // exp-275: push distance cell -> real goal (pull lane)
	pfOwn, pfBest            []pfTuple         // exp-275: Festival backward scores per node / best on path
	bcDisc, bcLow            []int32           // exp-281: biconnectivity scratch (discovery stamp-relative)
	bcStack                  []int32
	bcOn                     []uint32
	bcStamp                  uint32
	pfReachOf                *uint64 // exp-619 -pf-reach: the goalReach slice already narrowed
	pfGoals                  []bool
	pfScored                 []int32 // exp-283
	pfSty                    pfStyle // exp-283
	frLost                   []bool  // exp-285
	pfNext                   pfTuple // exp-275: tuple of the move being applied
	pfHave                   bool
	peerTracked              *atomic.Uint64 // exp-278: another search charged to the same budget (nil: none)
	peerTracked2             *atomic.Uint64 // exp-451: a second one (the hub lane has two forward contexts)
	rng                      uint64         // exp-279
	reviveTypes              int            // exp-588: type-based exploration period on a revived lane (0: off)
	oopz                     *oopZones      // exp-479
	resume                   *resumeState   // exp-278: set once the search loop has started
	p4lv                     p4Levers       // exp-690: plan4 levers for this solver's planners (plan-stream sources)
	planBF                   bool           // exp-690: plan4 plain best-first for this solver (a plan-stream source)
	festPlan                 bool           // exp-615: plan4 runs Festival-style (festsink + roomsep) for this solver
	planAlt                  bool           // exp-769 (-macro-alt-plan): plan4 runs festsink + roomsep + keep-parks 1 for this solver; never published to fa2
	batDyn                   bool           // exp-769: the budget is split between the baton's live engines
	hotSides                 bool           // exp-694: fa2 engine uses the player-side hotspot relation (-hot-sides)
	fa2Peer                  *fa2LaneShared // exp-618: the anytime fa2 lane's shared plan lists (dedup)
	p4pend                   *planner4      // exp-563: plan4 still planning inside the macro loop
	prePlan                  *packingPlan   // exp-563: a finished plan4 plan carried into a restarted macro solver
	prePlanAt                int
	stopCheck                func() bool     // exp-277: ends the search early (retarget)
	strataT                  []*strata       // exp-686: hub strata tables fed by noteBest (pull)
	rback                    *relayBackState // exp-761 -relay-back: this pull tree posts backward relay tips
	pfOn                     bool            // exp-275: Festival's backward order (pull mode)
	dive                     bool            // exp-691: the dive lane's greedy backward order (pull mode)
	dv                       *diveState      // exp-691
	bestSlot                 int             // exp-276: meet.Best slot the pull lane publishes to
	festTun                  []bool          // exp-272
	hfOwn, hfBest            []hfTuple       // exp-271: Festival HF scores per node and of its best ancestor
	addHF                    int32           // exp-271: predicted matching distance of the move being applied (-1: none)
	firstMeet                int64           // expansions (incl. probes) at the first perimeter hit, 0: none (exp-254)
	pairSideOn               bool            // PairSide for this instance
	laneID                   int             // exp-327: identifier for elastic headroom borrowing
	bdOn, bdH, bdID          int32           // exp-716 -best-dump: best board (boxes on goals, h, node+1; 0: none)
	bdAt                     int64           // exp-716: lane expansion at which the best was reached
	rlOn, rlH, rlID          int32           // exp-744 -revive-root: best board (boxes on goals, h, node+1)
	rlNext                   int64           // exp-744: expansions at the next post
	rlFz                     *fzState        // exp-767 -relay-fzset
	rootPfx                  string          // exp-744: moves from the original start to this solver's start
	rootPfxOK                bool
	isolated                 bool          // exp-645: a sub-level search (factor lane): no meetings, no plan publishing
	planCap                  time.Duration // exp-645b: > 0: cap on the packing planner's time (factor sub-solves, timed runs)
	elasticHold              uint64        // exp-327: currently borrowed elastic bytes
	memLimit                 uint64
	memFull                  bool  // the table cannot grow within the budget: stop
	addAdv                   uint8 // advisors of the move being applied, for add
	tr                       *trace.Recorder
	dl                       *DisplacementLandmarks
	dlInit                   bool
	candNP                   int32     // exp-288: min component cell of playerAfter for candidate endpoint
	prevTrees                []*solver // exp-288: retained previous forward search trees across retargets
	setupDuration            time.Duration

	back, fwd   *meet.Table // perimeters: the backward search's, ours
	meetPending []int32     // new nodes that hit the backward perimeter
	meetPosted  int
	table       []int32  // open-addressing set of node ids; -1 = empty
	tfp         []uint32 // per slot: high 32 bits of the stored state's hash

	// One queued move per open node; the exact frontier is in fess_search.go.
	rmark         []uint32 // regions() scratch
	floor         []int32  // non-wall cells
	rqueue        []int32
	rstamp        uint32
	bucket        []uint32 // per node: its feature cell
	mvStart       []int32  // per node: start of its moves in s.mv, or -1 if not opened yet
	mvLen         []uint32 // per node: number of moves
	mvNext        []uint32 // per node: next move in FESS order
	mvLeft        []uint32 // per node: moves not yet applied
	mvOff         []uint32 // MoveWindow: index of the stored window in the node's full move order
	mvTotal       []uint32 // MoveWindow: the node's full move count
	reopen        int32    // MoveWindow: node whose window ran out with moves left (-1: none)
	mv            []uint64 // move arena (see encodeMove)
	novOffset     []int32  // StableWindows: 1 + offset in novWords for recorded novel candidate picks (0: none)
	novWords      []uint64 // StableWindows: recorded novel candidate bitset words across windows
	novTier0Words []uint64 // StableWindows: recorded Tier 0 candidate bitset words across windows
	novTier1Words []uint64 // StableWindows: recorded Tier 1 candidate bitset words across windows
	revWords      []uint64 // StableWindows (exp-290): recorded demoted candidates (undo/internal/dead/dup)
	revOffset     []int32  // StableWindows (exp-290): 1 + offset in revWords (0: none)
	revBestExit   []int16  // StableWindows (exp-290): recorded bestExit index per node (-1: none)

	// Macro routing and optional explicitly stored paths.
	macros map[int32][]push
	mark3  []uint32 // player reach inside box-path search
	bq     []int32  // box-path search queue
	tseen  []uint32 // open(): targets already reached for the current box
	tst    uint32
	bseen  []uint32 // box-path states visited, indexed cell*4+dir
	bpost  []uint32 // endpoint player components already flooded
	bprev  []int32  // box-path predecessor state
	pstamp uint32
	bstamp uint32

	stats Stats
}

func newSolver(lv *Level) *solver {
	return newSolverUntil(lv, time.Time{})
}

func newSolverUntil(lv *Level, deadline time.Time) *solver {
	return newSolverDir(lv, deadline, false)
}

// newSolverDir makes a solver for lv; with pull, lv is the pull view of a
// level (see newPullSolver).
func newSolverDir(lv *Level, deadline time.Time, pull bool) *solver {
	size := lv.W * lv.H
	s := &solver{
		pull:       pull,
		deadline:   deadline,
		budgetP:    &Budget,
		trackedP:   &Tracked,
		owner:      1,
		pairSideOn: PairSide,
		ineffOn:    InefficientPrune,
		addHF:      -1,
		reopen:     -1,
		lv:         lv,
		n:          len(lv.Boxes),
		dirs:       [4]int32{-int32(lv.W), int32(lv.W), -1, 1},
		occ:        make([]bool, size),
		mark:       make([]uint32, size),
		mark2:      make([]uint32, size),
		fz:         make([]bool, size),
		novelty:    Novelty,
		fzNo:       make([]uint32, size),
		cmark:      make([]uint32, size),
		queue:      make([]int32, 0, size),
		table:      make([]int32, 1<<12),
		tfp:        make([]uint32, 1<<12),
		candNP:     -1,
	}
	for i := range s.table {
		s.table[i] = -1
	}
	s.initBitsets()
	s.computeDistances()
	s.computeGoalDistances()
	if SideDead && !pull {
		s.sd = sidedead.New(s.lv.Wall, s.lv.Goal, s.lv.W)
	}
	if DiagDead != diagOff && !pull {
		s.dd = diagdead.New(s.lv.Wall, s.lv.Goal, s.lv.W)
	}
	if ClusterDead && !pull {
		dead := make([]bool, len(s.lv.Wall))
		for c, d := range s.dist {
			dead[c] = d < 0
		}
		s.cl = cluster.NewChecker(cluster.NewBoard(s.lv.Wall, s.lv.Goal, dead, s.lv.W))
	}
	if CorralDead > 0 && !pull {
		dead := make([]bool, len(s.lv.Wall))
		for c, d := range s.dist {
			dead[c] = d < 0
		}
		s.dz = deadzone.NewChecker(deadzone.NewBoard(s.lv.Wall, s.lv.Goal, dead, s.lv.W), CorralDead)
	}
	m := s.n + 1
	s.hu, s.hv, s.hp, s.hway, s.hminv = make([]int32, m), make([]int32, m), make([]int32, m), make([]int32, m), make([]int32, m)
	s.hused = make([]bool, m)
	s.macros = map[int32][]push{}
	s.mark3 = make([]uint32, size)
	s.bb = newBitBoard(s.lv)
	if s.bb != nil {
		s.bb.setAccessGeometry(s.dist, s.dirs, s.lv.W, pull)
	}
	s.bseen = make([]uint32, 4*size)
	s.bpost = make([]uint32, 4*size)
	s.bprev = make([]int32, 4*size)
	s.computeRooms()
	if !deadline.IsZero() {
		s.setupDeadline = time.Now().Add(min(100*time.Millisecond, time.Until(deadline)/10))
	}
	if !pull { // goal rooms and out-of-plan are push-mode features
		s.basin = s.computeBasin()
	}
	s.hot = newHotspotTable(s)
	return s
}

// computeDistances does a reverse (pull) BFS from all goals. A box at b can be
// pushed in direction d to b+d if b-d (player) and b+d are floor.
func (s *solver) computeDistances() {
	lv := s.lv
	s.dist = make([]int32, len(lv.Wall))
	if reach.Enabled && !s.pull {
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
			if !lv.Wall[b] && !lv.Wall[s.supportCell(b, c, d)] && s.dist[b] < 0 {
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
				if !lv.Wall[b] && !lv.Wall[s.supportCell(b, c, dir)] && d[b] < 0 {
					d[b] = d[c] + 1
					q = append(q, b)
				}
			}
		}
		s.queue = q
	}
	s.goalW = (len(s.goals) + 63) >> 6
	s.goalReach = make([]uint64, cells*s.goalW)
	for g := range s.goals {
		for c, d := range s.goalDist[g*cells : (g+1)*cells] {
			if d >= 0 {
				s.goalReach[c*s.goalW+g>>6] |= 1 << (g & 63)
			}
		}
	}
}

// assign returns the minimum total push distance over assignments of boxes to
// distinct goals (Hungarian algorithm, O(n^3)), or -1 if none is feasible.
func (s *solver) assign(boxes []int32) int32 {
	return s.assignTo(boxes, s.goalDist)
}

const assignmentInf = 1 << 20

func assignmentEdge(dist []int32, cells int, box, goal int32) int32 {
	if d := dist[int(goal-1)*cells+int(box)]; d >= 0 {
		return d
	}
	return assignmentInf
}

func (s *solver) assignTo(boxes []int32, dist []int32) int32 {
	clear(s.hu)
	clear(s.hv)
	clear(s.hp)
	for row := int32(1); row <= int32(s.n); row++ {
		s.augmentAssignment(boxes, dist, row)
	}
	if total := s.assignmentCost(boxes, dist); total < assignmentInf {
		return int32(total)
	}
	return -1
}

// The assignment's inner loop reads a box-major copy of the goal-major
// distance table (exp-408), so one box's distances to all goals are
// contiguous instead of cells apart. Same values as the strided
// assignmentEdge loop, which stays as the fallback when the table is not
// goals x cells with goals == boxes (and as the test reference).
// distT returns the box-major transpose of dist (goal-major, one row of
// len(Wall) cells per goal), cached for the last array asked about. Distance
// tables are immutable once built; holding the source keeps its address
// from being reused, so the identity check is sound.
func (s *solver) distT(dist []int32) []int32 {
	if len(s.dtSrc) == len(dist) && &s.dtSrc[0] == &dist[0] {
		return s.dtT
	}
	if s.dive && len(s.dtSrc2) == len(dist) && &s.dtSrc2[0] == &dist[0] {
		// exp-691: the dive alternates two matchings; keep both transposes.
		s.dtSrc, s.dtSrc2 = s.dtSrc2, s.dtSrc
		s.dtT, s.dtT2 = s.dtT2, s.dtT
		return s.dtT
	}
	if s.dive {
		s.dtSrc2, s.dtT2 = s.dtSrc, s.dtT
		s.dtT = nil
	}
	cells := len(s.lv.Wall)
	g := len(dist) / cells
	if cap(s.dtT) >= len(dist) {
		s.dtT = s.dtT[:len(dist)]
	} else {
		s.dtT = make([]int32, len(dist))
	}
	for gi := range g {
		for c, d := range dist[gi*cells : (gi+1)*cells] {
			s.dtT[c*g+gi] = d
		}
	}
	s.dtSrc = dist
	return s.dtT
}

func (s *solver) assignmentCost(boxes []int32, dist []int32) int64 {
	cells := len(s.lv.Wall)
	var cost int64
	if len(dist) == s.n*cells {
		// Box-major table (exp-456): the matched edges' reads stay within
		// each box's contiguous row instead of striding a cells-long row
		// per goal.
		dt := s.distT(dist)
		for g, row := range s.hp[1:] {
			d := dt[int(boxes[row-1])*s.n+g]
			if d < 0 {
				d = assignmentInf
			}
			cost += int64(d)
		}
		return cost
	}
	for g, row := range s.hp[1:] {
		cost += int64(assignmentEdge(dist, cells, boxes[row-1], int32(g+1)))
	}
	return cost
}

// augmentAssignment inserts an unmatched box row into a feasible dual and
// a tight partial matching. The algorithm also works after replacing a row
// of a complete matching and making just that row unmatched.
func (s *solver) augmentAssignment(boxes []int32, dist []int32, row int32) {
	n, cells := s.n, len(s.lv.Wall)
	u, v, p, way, minv, used := s.hu, s.hv, s.hp, s.hway, s.hminv, s.hused
	var dt []int32
	if len(dist) == n*cells {
		dt = s.distT(dist)
	}
	p[0] = row
	j0 := int32(0)
	for j := range minv {
		minv[j], used[j] = math.MaxInt32, false
	}
	// Lazy duals: minv holds each unused column's true value plus D, the sum
	// of the deltas so far, so a step costs one scan; the used columns' u/v
	// shifts are applied once at the end from the D at which each was added.
	D := int32(0)
	ul, ud := s.hul[:0], s.hud[:0]
	for {
		used[j0] = true
		ul, ud = append(ul, j0), append(ud, D)
		i0, ms, j1 := p[j0], int32(math.MaxInt32), int32(0)
		ui0 := u[i0] - D
		if dt != nil {
			// Box-major row: this box's distance to every goal, contiguous.
			er := dt[int(boxes[i0-1])*n : int(boxes[i0-1])*n+n]
			for j := 1; j <= n; j++ {
				if !used[j] {
					e := er[j-1]
					if e < 0 {
						e = assignmentInf
					}
					if cur := e - ui0 - v[j]; cur < minv[j] {
						minv[j], way[j] = cur, j0
					}
					// On a tie prefer a free column: it ends the search at
					// the same optimal cost without the remaining steps.
					if mj := minv[j]; mj < ms || (mj == ms && p[j] == 0) {
						ms, j1 = mj, int32(j)
					}
				}
			}
		} else {
			for j := int32(1); j <= int32(n); j++ {
				if !used[j] {
					if cur := assignmentEdge(dist, cells, boxes[i0-1], j) - ui0 - v[j]; cur < minv[j] {
						minv[j], way[j] = cur, j0
					}
					if mj := minv[j]; mj < ms || (mj == ms && p[j] == 0) {
						ms, j1 = mj, j
					}
				}
			}
		}
		D = ms
		j0 = j1
		if p[j0] == 0 {
			break
		}
	}
	for k, j := range ul {
		d := D - ud[k]
		u[p[j]] += d
		v[j] -= d
	}
	s.hul, s.hud = ul, ud
	for j0 != 0 {
		j1 := way[j0]
		p[j0] = p[j1]
		j0 = j1
	}
}

// feasible reports whether every box can be matched to a distinct goal it can
// reach (per-goal push distances, ignoring other boxes): a bipartite perfect
// matching, found with augmenting paths. Same answer as assign(boxes) >= 0,
// without computing the minimum cost.
func (s *solver) feasible(boxes []int32) bool {
	gw := s.goalW
	if s.matchOf == nil {
		s.matchOf = make([]int32, len(s.goals))
		s.gseen = make([]uint64, gw)
		s.gfree = make([]uint64, gw)
	}
	for j := range s.matchOf {
		s.matchOf[j] = -1
	}
	// free marks the goals not matched yet: a box with an unmatched goal in
	// reach takes it directly (no augmenting search).
	free := s.gfree
	for w := range free {
		free[w] = ^uint64(0)
	}
	if r := len(s.goals) & 63; r != 0 {
		free[gw-1] = 1<<uint(r) - 1
	}
	seen := s.gseen
	var try func(i int32) bool
	try = func(i int32) bool {
		b := int(boxes[i])
		row := s.goalReach[b*gw : (b+1)*gw]
		for w := range row {
			for v := row[w] &^ seen[w]; v != 0; v = row[w] &^ seen[w] {
				j := w<<6 | bits.TrailingZeros64(v)
				seen[w] |= 1 << (j & 63)
				if s.matchOf[j] < 0 || try(s.matchOf[j]) {
					s.matchOf[j] = i
					free[w] &^= 1 << (j & 63)
					return true
				}
			}
		}
		return false
	}
	for i := range int32(len(boxes)) {
		row := s.goalReach[int(boxes[i])*gw : (int(boxes[i])+1)*gw]
		taken := false
		for w, r := range row {
			if f := r & free[w]; f != 0 {
				j := w<<6 | bits.TrailingZeros64(f)
				s.matchOf[j] = i
				free[w] &^= 1 << (j & 63)
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		clear(seen)
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

// frozen reports whether the box at c is frozen, treating boxes under
// examination as walls. *offGoal is meaningful only when frozen returns true:
// it then says whether the closed, mutually blocking set it found holds a box
// off goal. On a false result *offGoal may already be set by boxes that were
// frozen only under the hypothesis "c is a wall" (exp-553/586), so callers
// must not read it then (freezeDeadlock does not).
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

// packed counts supplied boxes at the furthest satisfied packing checkpoint.
func (s *solver) packed() int { return s.packedWith(-1) }

// packedWith is packed given the plan's packed count of s.occ when already
// known (-1: look it up).
func (s *solver) packedWith(planPacked int) int {
	if s.pull {
		// Pull mode (Festival's backward HF order): progress is not boxes on
		// start squares but how far the boxes have spread from the goals.
		var sp int32
		for _, c := range s.fcell {
			if s.occ[c] {
				sp += s.spreadDist[c]
			}
		}
		return int(sp) / SpreadBucket
	}
	if s.tgt != nil {
		n := 0
		for _, g := range s.tgtList {
			if s.occ[g] {
				n++
			}
		}
		return n
	}
	if s.plan != nil {
		if s.plan.fest != nil && (PlanV2 || PackedFest || PlanFile != "" || s.plan.festPacked) && !PlanV2Count {
			return s.plan.fest.packed(s.occ)
		}
		if PackedFest {
			if s.plan.fest == nil {
				s.plan.fest = buildFestPlan(s.plan, s.lv.Goal)
			}
			return s.plan.fest.packed(s.occ)
		}
		if planPacked >= 0 {
			return planPacked
		}
		_, n := s.plan.status(s.occ)
		return n
	}
	// Without a sink-room plan (e.g. a board with no rooms), count goals
	// directly. No fabricated order is imposed on a failed presearch.
	n := 0
	for _, g := range s.goals {
		if s.occ[g] {
			n++
		}
	}
	return n
}

// packedChild is packedWith for the child of the opened node in which a box
// moved from b to t (exp-659 -lazy-child): the Festival-order plan count
// reads only the plan's cells, so a move touching none of them keeps the
// parent's count (found once per opened node; s.oopSt marks the node).
func (s *solver) packedChild(b, t int32, planPacked int) int {
	if s.pull || s.tgt != nil || s.plan == nil || s.plan.fest == nil ||
		!(PlanV2 || PackedFest || PlanFile != "" || s.plan.festPacked) || PlanV2Count {
		return s.packedWith(planPacked)
	}
	f := s.plan.fest
	if len(s.pkMark) != len(s.occ) {
		s.pkMark = make([]int32, len(s.occ))
	}
	if s.pkSt != s.oopSt {
		s.pkSt, s.pkHave = s.oopSt, false
		for _, x := range f.e {
			s.pkMark[x.to] = s.oopSt
		}
	}
	if s.pkMark[b] == s.oopSt || s.pkMark[t] == s.oopSt {
		return f.packed(s.occ)
	}
	if !s.pkHave {
		s.pkVal, s.pkHave = f.packed(s.occ), true
	}
	return s.pkVal
}

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
		s.rmark[c] = rs
		rel := s.tgtPocket == nil || !s.tgtPocket[c]
		q := append(s.rqueue[:0], c)
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; s.rmark[y] != rs && !lv.Wall[y] && !s.occ[y] {
					s.rmark[y] = rs
					q = append(q, y)
					rel = rel || !s.tgtPocket[y]
				}
			}
		}
		s.rqueue = q
		if rel {
			n++
		}
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
func (s *solver) pairDead(a, b int32) bool { return s.pairDeadFrom(a, b, -1) }

// pairDeadFrom is pairDead for one player start: the player's region (in
// the board holding only a and b) containing cell from; from < 0 means
// every region (dead only if dead from all of them).
func (s *solver) pairDeadFrom(a, b, from int32) bool {
	if s.bb == nil {
		return s.pairDeadFromScan(a, b, from)
	}
	return s.pairDeadFromBits(a, b, from)
}

// pairDeadFromScan is pairDeadFrom with queue floods: the path for boards
// wider than 64 cells and the reference for pairDeadFromBits.
func (s *solver) pairDeadFromScan(a, b, from int32) bool {
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
	occ[a], occ[b] = true, true
	defer func() { occ[a], occ[b] = false, false }()
	s.pst2++
	seenStamp := s.pst2
	var starts []int32
	if from >= 0 {
		m, _ := reach(from)
		starts = []int32{m}
	}
	for _, c := range s.floorCells() {
		if from >= 0 {
			break
		}
		if occ[c] || s.pseen[c] == seenStamp {
			continue
		}
		m, _ := reach(c)
		starts = append(starts, m)
		for _, x := range s.pq { // the region just flooded
			s.pseen[x] = seenStamp
		}
	}
	// Allocation-free: visited states live in a reused flat open-addressing
	// table (key x | y<<16 | p<<32, generation in the top 16 bits so it never
	// needs clearing); moves in a fixed array on the stack.
	if s.pvis == nil {
		s.pvis = make([]uint64, 1024)
	}
	mask := uint64(len(s.pvis) - 1)
	visit := func(x, y, p int32) bool { // reports whether newly added
		k := uint64(uint16(x)) | uint64(uint16(y))<<16 | uint64(uint16(p))<<32
		v := k | uint64(s.pgen)<<48
		for i := (k * 0x9E3779B97F4A7C15 >> 40) & mask; ; i = (i + 1) & mask {
			e := s.pvis[i]
			if uint16(e>>48) != s.pgen {
				s.pvis[i] = v
				return true
			}
			if e == v {
				return false
			}
		}
	}
	for _, p0 := range starts {
		s.pgen++
		if s.pgen == 0 {
			clear(s.pvis)
			s.pgen = 1
		}
		visit(a, b, p0)
		nseen := 1
		q := append(s.pq2[:0], st2{a, b, p0})
		for i := 0; i < len(q); i++ {
			if nseen > capStates {
				s.pq2 = q
				return false // unknown: assume not dead
			}
			cur := q[i]
			occ[a], occ[b] = false, false
			occ[cur.x], occ[cur.y] = true, true
			_, st := reach(cur.p)
			type mv struct{ box, t, other int32 }
			var moves [8]mv
			nm := 0
			for _, pr := range [2][2]int32{{cur.x, cur.y}, {cur.y, cur.x}} {
				bx, other := pr[0], pr[1]
				for _, d := range s.dirs {
					t := bx + d
					if s.pmark[bx-d] == st && !lv.Wall[t] && t != other && s.dist[t] >= 0 {
						moves[nm] = mv{bx, t, other}
						nm++
					}
				}
			}
			occ[cur.x], occ[cur.y] = false, false
			for _, m := range moves[:nm] {
				if lv.Goal[m.t] && lv.Goal[m.other] {
					occ[a], occ[b] = true, true
					s.pq2 = q
					return false
				}
				occ[m.t], occ[m.other] = true, true
				np, _ := reach(m.box)
				occ[m.t], occ[m.other] = false, false
				x, y := min(m.t, m.other), max(m.t, m.other)
				if visit(x, y, np) {
					nseen++
					q = append(q, st2{x, y, np})
				}
			}
			occ[a], occ[b] = true, true
		}
		s.pq2 = q
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

// Moves are FESS macro moves: push one box (others fixed) from its cell to
// any square it can reach, as a single move. Encoding (uint64): bits 0-14 box
// index, 15-29 target cell, 30 FESS weight delta, 31-32 final push direction,
// 33-47 exact child region count, 48-54 the advisors that chose it (for
// diagnostics only), 63 used flag (moveUsed). The final direction preserves player components.
func encodeMove(i, t, wd int32, r int, side int32) uint64 {
	return uint64(i) | uint64(t)<<15 | uint64(wd)<<30 | uint64(side)<<31 | uint64(r)<<33
}

func decodeMove(m uint64) (i, t, wd int32, r int) {
	return int32(m & 0x7FFF), int32(m >> 15 & 0x7FFF), int32(m >> 30 & 1), int(m >> 33 & 0x7FFF)
}

func moveSide(m uint64) int32 { return int32(m >> 31 & 3) }

const moveAdvShift = 48
const moveTierShift = 56

func moveAdvisors(m uint64) uint8 { return uint8(m >> moveAdvShift & 0xFF) }

// boxSearch runs a breadth-first search over pushes of the box at b with the
// other boxes fixed (s.occ holds the state; the player's reach is stamped st
// in s.mark). States k = cell*4+dir mean "box on cell, player behind it ready
// to push in dir"; player access is recomputed after every push. For each
// push it calls visit(k, to) with the box's new cell (s.occ[b] is false and
// s.occ[to] is false during the call); returning false stops the search.
// s.bprev links each state to its predecessor (-1 at the start).
func (s *solver) boxSearch(b int32, st uint32, visit func(k, to int32) bool) {
	lv := s.lv
	wall, occ, dirs := lv.Wall, s.occ, s.dirs
	bb := s.bb // nil for boards wider than 64 cells: queue flood only
	bbBuilt, pushes := false, 0
	if s.pstamp > 1<<31 || s.bstamp > 1<<31 { // stamps would wrap: reset
		clear(s.mark3)
		clear(s.bseen)
		clear(s.bpost)
		s.pstamp, s.bstamp = 0, 0
	}
	s.bstamp++
	bs := s.bstamp
	q := s.bq[:0]
	for di, d := range s.dirs {
		// Push: the player stands behind the box (b-d). Pull: in front of it
		// (b+d), with room to step back (b+2d).
		if s.mark[s.front(b, d)] == st && (!s.pull || !lv.Wall[b+2*d] && !s.occ[b+2*d]) {
			k := b*4 + int32(di)
			s.bseen[k], s.bprev[k] = bs, -1
			q = append(q, k)
		}
	}
	// Node mode (exp-415): open() builds the free rows once for the whole
	// node, so each box's search only clears and restores its own bit.
	node := bb != nil && bb.node
	rb, bbit := int(b/int32(lv.W))+1, uint64(1)<<uint(b%int32(lv.W))
	if node {
		if !bb.nodeBuilt {
			bb.build(occ) // occ[b] still set: rows hold every box
			bb.nodeBuilt = true
		}
		bb.free[rb] |= bbit // the searched box is lifted
	}
	s.occ[b] = false
	defer func() {
		s.occ[b] = true
		s.bq = q
		if node {
			bb.free[rb] &^= bbit
		}
	}()
	for i := 0; i < len(q); i++ {
		k := q[i]
		if !TransactionalOpen && i&63 == 0 && !s.pathOnly && s.expired() {
			return
		}
		c, di := k/4, k%4
		nc := c + s.dirs[di]
		if lv.Wall[nc] || s.occ[nc] || s.dist[nc] < 0 {
			continue
		}
		if s.pull {
			if pd := nc + s.dirs[di]; lv.Wall[pd] || s.occ[pd] {
				continue // the pulling player needs room to step back
			}
		}
		// A prior arrival in this component already visited this endpoint
		// and queued every available next push. BFS made that route shortest.
		if s.bpost[nc*4+di] == bs {
			continue
		}
		// Player is now on c (push) or nc+d (pull), box on nc: which sides
		// of nc can it reach?
		s.occ[nc] = true
		s.pstamp++
		ps := s.pstamp
		pc := s.playerAfter(nc, s.dirs[di])
		mark3 := s.mark3
		var minC int32
		if node {
			minC = bb.flood(pc, nc, mark3, ps)
		} else if bb != nil && pushes >= boxBitAfter {
			if !bbBuilt {
				occ[nc] = false // flood blocks nc itself; rows hold only the other boxes
				bb.build(occ)
				occ[nc] = true
				bbBuilt = true
			}
			minC = bb.flood(pc, nc, mark3, ps)
		} else {
			pq := append(s.queue[:0], pc)
			mark3[pc] = ps
			minC = pc
			for j := 0; j < len(pq); j++ {
				x := pq[j]
				for _, d := range dirs {
					y := x + d
					if mark3[y] != ps && !wall[y] && !occ[y] {
						mark3[y] = ps
						if y < minC {
							minC = y
						}
						pq = append(pq, y)
					}
				}
			}
			s.queue = pq
		}
		pushes++
		s.candNP = minC
		s.occ[nc] = false
		// The final player's reach is available to the visitor in mark3,
		// stamped ps. Reuse it to distinguish macro endpoint components.
		if !visit(k, nc) {
			return
		}
		for d2i, d2 := range s.dirs {
			k2 := nc*4 + int32(d2i)
			if s.mark3[s.front(nc, d2)] == ps {
				s.bpost[k2] = bs
				if s.bseen[k2] != bs {
					s.bseen[k2], s.bprev[k2] = bs, k
					q = append(q, k2)
				}
			}
		}
	}
}

// boxPathTo returns the shortest push sequence (as found by boxSearch) taking
// the box at b to t, or nil.
func (s *solver) boxPathTo(b, t int32, st uint32, side int32) []push {
	// One box's path, never cut short by the budget: it reconstructs moves
	// already found (solutions, tails, applied macros). After a search ran
	// out of time or expansions, an expiring boxSearch returned no path, so
	// every tail and meeting completed afterwards came back empty (exp-188).
	s.pathOnly = true
	defer func() { s.pathOnly = false }()
	var seq []push
	s.boxSearch(b, st, func(k, to int32) bool {
		if to != t || side >= 0 && k%4 != side {
			return true
		}
		for ; k >= 0; k = s.bprev[k] {
			seq = append(seq, push{k / 4, int8(k % 4)})
		}
		slices.Reverse(seq)
		return false
	})
	return seq
}

// apply performs macro move m of node id (s.occ holds its boxes, reach
// stamped st) and adds the child. It returns the child's id (-1 if already
// stored) and its h. The push path is recomputed, not stored.
func (s *solver) apply(id int32, m uint64, cur, child []int32, h int32, st uint32) (int32, int32) {
	s.lastAdded = -1
	i, t, wd, r := decodeMove(m)
	b := cur[i]
	seq := s.boxPathTo(b, t, st, moveSide(m))
	if len(seq) == 0 {
		return -1, h
	}
	last := seq[len(seq)-1]
	copy(child, cur)
	child[i] = t
	slices.Sort(child)
	s.occ[b] = false
	s.occ[t] = true
	side := s.playerAfter(t, s.dirs[last.dir])
	np := s.reach(side, s.mark2)
	if LazyCluster && s.cl != nil && !s.pull {
		stamp := s.stamp
		if s.cl.Dead(s.occ, t, side, func(y int32) bool { return s.mark2[y] == stamp }) {
			s.stats.ClusterDead++
			s.occ[t] = false
			s.occ[b] = true
			return -1, h
		}
	}
	if s.dz != nil && s.dz.DeadAfter(s.occ, child, side, t) {
		s.stats.CorralDead++
		s.occ[t] = false
		s.occ[b] = true
		return -1, h
	}
	ch := h - s.dist[b] + s.dist[t]
	s.addAdv = moveAdvisors(m)
	w := wd
	if !s.dive { // exp-691: the dive stores no weights
		w += s.w[id]
	}
	cid := s.add(child, np, id, b, t, last.dir, nil, s.g[id]+int32(len(seq)), ch, w, r)
	s.addAdv = 0
	s.occ[t] = false
	s.occ[b] = true
	return cid, ch
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
	s.revKey = make([]uint64, s.nw)
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

func (s *solver) findRevGroup(id int32) int32 {
	if id < 0 || int(id) >= len(s.revGroup) {
		return id
	}
	root := id
	for root >= 0 && int(root) < len(s.revGroup) && root != s.revGroup[root] {
		root = s.revGroup[root]
	}
	curr := id
	for curr >= 0 && int(curr) < len(s.revGroup) && curr != root {
		nxt := s.revGroup[curr]
		s.revGroup[curr] = root
		curr = nxt
	}
	return root
}

func (s *solver) unionRevGroup(x, y int32) {
	rx, ry := s.findRevGroup(x), s.findRevGroup(y)
	if rx != ry && rx >= 0 && ry >= 0 && int(rx) < len(s.revSize) && int(ry) < len(s.revSize) {
		if int(s.revSize[rx])+int(s.revSize[ry]) > 32 {
			return
		}
		if s.revSize[rx] < s.revSize[ry] {
			rx, ry = ry, rx
		}
		s.revGroup[ry] = rx
		s.revSize[rx] += s.revSize[ry]
	}
}

// add inserts a new node, or records a shorter path to an existing one.
// w is the node's accumulated FESS weight; reg its region count, or -1 to
// compute it from s.occ (which must hold the node's boxes).
// It returns the new node's id, or -1 if the state was already stored.
func (s *solver) add(boxes []int32, player, parent, from, to int32, dir int8, seq []push, g, h, w int32, reg int) int32 {
	if s.dive {
		return s.diveAdd(boxes, player, parent, from, to, dir, g) // exp-691: the pull path's arrays only
	}
	packed := s.pack(boxes)
	slot, id, fp := s.find(packed, player)
	if id >= 0 {
		s.stats.Duplicates++
		s.lastAdded = id
		if s.dead[id] {
			return -1
		}
		if RetargetReuse && id < s.retargetNodes {
			improvesCell := false
			worsensCell := false
			if s.hfRank && HFFestSched && parent >= 0 && int(parent) < len(s.hfBest) && int(id) < len(s.hfBest) {
				improvesCell = hfBetter(s.hfBest[parent], s.hfBest[id])
				worsensCell = hfBetter(s.hfBest[id], s.hfBest[parent]) && hfBetter(s.hfBest[id], s.hfOwn[id])
			}
			shorterPath := g < s.g[id] && !worsensCell
			if improvesCell || shorterPath {
				cyclic := false
				limit := len(s.player)
				depth := 0
				for p := parent; p >= 0 && depth <= limit; p = s.parent[p] {
					if p == id {
						cyclic = true
						break
					}
					depth++
				}
				if !cyclic && depth <= limit {
					s.parent[id], s.pushFrom[id], s.pushTo[id], s.pushDir[id], s.g[id] = parent, from, to, dir, g
					s.adv[id] = s.addAdv
					if seq != nil {
						s.macros[id] = slices.Clone(seq)
					} else {
						delete(s.macros, id)
					}
					var newBucket uint32
					if s.pfOn {
						newBucket = uint32(s.pfReparent(id, parent))
					} else if s.hfRank && HFFestSched {
						newBucket = uint32(s.hfSchedReparent(id, parent))
					} else {
						newBucket = uint32(s.project(s.actual[id], parent))
					}
					s.bucket[id] = newBucket
					s.version[id]++
					if s.mvStart[id] < 0 {
						heap.Push(&s.frontier.queues[newBucket], fessEntry{
							node:    id,
							move:    -1,
							weight:  w,
							version: s.version[id],
							score:   s.lazyScore(id),
						})
					} else {
						s.enqueue(id)
						s.deferReopen(id)
					}
				}
			}
			return -1
		}
		if w > s.w[id] || w == s.w[id] && g >= s.g[id] {
			return -1
		}
		lowerWeight := w < s.w[id]
		if ReparentCycleGuard && s.createsCycle(id, parent) {
			// exp-353: a descendant of id can carry a lower weight/g than id
			// (g is not re-propagated), and reparenting under it closes a
			// loop that reconstruct then follows forever (multi-GB chain).
			return -1
		}
		s.parent[id], s.pushFrom[id], s.pushTo[id], s.pushDir[id], s.g[id] = parent, from, to, dir, g
		s.adv[id] = s.addAdv
		if seq != nil {
			s.macros[id] = slices.Clone(seq)
		} else {
			delete(s.macros, id)
		}
		var newBucket uint32
		if s.pfOn {
			newBucket = uint32(s.pfReparent(id, parent))
		} else if s.hfRank && HFFestSched {
			newBucket = uint32(s.hfSchedReparent(id, parent))
		} else {
			newBucket = uint32(s.project(s.actual[id], parent))
		}
		movedCell := newBucket != s.bucket[id]
		if lowerWeight || movedCell {
			s.w[id] = w
			s.version[id]++
			s.bucket[id] = newBucket
			if lowerWeight && s.mvStart[id] >= 0 {
				for k := s.mvStart[id]; k < s.mvStart[id]+int32(s.mvLen[id]); k++ {
					s.mv[k] &^= moveUsed
				}
				s.mvNext[id] = 0
				s.mvLeft[id] = s.mvTotal[id] - s.mvOff[id] // windowed: earlier windows aren't retried
			}
			return id
		}
		return -1
	}
	if s.memFull {
		s.lastAdded = -1
		return -1
	}
	if s.sd != nil && parent >= 0 {
		// Player-side dead squares, with the child's actual player region
		// (the move-generation check can't always name a player cell).
		for _, c := range boxes {
			if !s.lv.Goal[c] && s.sd.Dead(c, player) {
				s.stats.SideDead++
				s.lastAdded = -1
				return -1
			}
		}
	}
	id = int32(len(s.player))
	s.lastAdded = id
	if soloracle.On {
		soloracle.Probe(s.oracleLane(), boxes, player)
	}
	if len(s.prevTrees) > 0 {
		s.stats.RetargetTotalAdded++
		for _, prev := range s.prevTrees {
			if _, pid, _ := prev.find(packed, player); pid >= 0 {
				s.stats.RetargetRegenAdded++
				break
			}
		}
	}
	s.table[slot], s.tfp[slot] = id, fp
	if seq != nil {
		s.macros[id] = slices.Clone(seq)
	}
	s.boxes = append(s.boxes, packed...)
	s.player = append(s.player, player)
	s.parent = append(s.parent, parent)
	s.pushFrom = append(s.pushFrom, from)
	s.pushTo = append(s.pushTo, to)
	s.pushDir = append(s.pushDir, dir)
	s.revGroup = append(s.revGroup, id)
	s.revSize = append(s.revSize, 1)
	s.g = append(s.g, g)
	s.w = append(s.w, w)
	if reg < 0 {
		reg = s.regions()
	}
	s.reg = append(s.reg, uint16(reg))
	s.adv = append(s.adv, s.addAdv)
	var cell featureCell
	if !s.dive { // exp-691: the dive files nodes by its own features
		cell = featureCell{s.packed(), reg, s.roomConnectivity(), s.outOfPlan(boxes)}
	}
	if FXCell3 {
		cell.roomConnectivity = 0 // exp-477: Festival's forward cell (packed, connectivity, OOP)
	}
	if s.hfRank {
		// Festival's forward HF cells: connectivity, boxes on target
		// squares, hotspots relative to the target.
		cell = featureCell{packed: s.packed(), connectivity: s.hotRel(boxes), oop: reg}
		if HFMatchFirst && (s.matchDist(boxes) >= 1<<20 || HFFrozenMatch && s.hfFrozenOff(boxes)) {
			cell.oop += hfNoMatch // no box-to-target matching: last, as in Festival
		}
	}
	s.actual = append(s.actual, cell)
	s.version = append(s.version, 0)
	s.dead = append(s.dead, false)
	s.edgeHead = append(s.edgeHead, -1)
	s.liveChildren = append(s.liveChildren, 0)
	bucket := 0
	if s.pfOn {
		bucket = s.pfAdd(boxes, parent, reg)
	} else if s.hfRank && HFFestSched {
		bucket = s.hfSchedAdd(boxes, cell, parent, reg)
	} else {
		bucket = s.project(cell, parent)
	}
	s.bucket = append(s.bucket, uint32(bucket))
	s.mvStart = append(s.mvStart, -1)
	s.mvLen = append(s.mvLen, 0)
	s.mvNext = append(s.mvNext, 0)
	s.mvLeft = append(s.mvLeft, 0)
	s.mvOff = append(s.mvOff, 0)
	s.mvTotal = append(s.mvTotal, 0)
	s.novOffset = append(s.novOffset, 0)
	s.revOffset = append(s.revOffset, 0)
	s.revBestExit = append(s.revBestExit, -1)
	if s.fwd != nil {
		k := s.fwd.Key(boxes, player)
		s.fwd.Add(k)
		if s.meetAudit != nil && s.pull {
			s.meetAudit.probe(s, k, boxes, player, 0, id)
		}
		if s.back != nil && s.meetPosted < maxMeetPosts && s.back.Has(k) {
			s.meetPending = append(s.meetPending, id)
			s.noteMeet()
		}
	}
	if len(s.player)*4 >= len(s.table)*3 {
		// Doubling allocates the new table while the old one is live. If that
		// would pass the budget, stop at the next check instead: an RSS
		// overshoot gets the whole process (every strategy) killed.
		needed := s.trackedBytes() + uint64(len(s.table))*16 + s.peerBytes()
		if s.overBudget(needed) {
			s.memFull = true
		} else {
			s.grow()
		}
	}
	return id
}

// maxMeetPosts caps the meetings one search posts (each costs a path
// reconstruction; stale perimeter entries from earlier backward phases can
// hit without confirming).
const maxMeetPosts = 2000

// postMeetings sends the pending perimeter hits, with their forward paths, to
// the backward search (s.occ must be clear; cur is scratch).
func (s *solver) postMeetings(cur []int32) {
	if s.pull {
		s.pullPostMeetings(cur)
		return
	}
	for _, id := range s.meetPending {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		s.meetPosted++
		pm, pa := s.stats.PathMoves, s.stats.PathAdvisors
		prefix := s.reconstruct(id)
		s.stats.PathMoves, s.stats.PathAdvisors = pm, pa
		s.unpack(id, cur)
		s.back.Post(meet.Meeting{Boxes: slices.Clone(cur), Player: s.player[id], Prefix: s.relayRootPrefix() + prefix,
			At: meet.EndCell(prefix, s.lv.Player, s.lv.W), From: "macro", Lane: s.meetLane(), Owner: s.owner})
	}
	s.meetPending = s.meetPending[:0]
	for _, pc := range s.pendCands {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		if ref, ok := s.refGet(pc.k); ok {
			pm, pa := s.stats.PathMoves, s.stats.PathAdvisors
			prefix := s.reconstructRef(ref)
			s.stats.PathMoves, s.stats.PathAdvisors = pm, pa
			if prefix == "" {
				continue
			}
			p, i, t, _ := unpackRef(ref)
			s.unpack(p, cur)
			cur[i] = t
			slices.Sort(cur)
			s.meetPosted++
			s.back.Post(meet.Meeting{Boxes: slices.Clone(cur), Player: pc.np, Prefix: s.relayRootPrefix() + prefix,
				At: meet.EndCell(prefix, s.lv.Player, s.lv.W), From: "macro", Lane: s.meetLane(), Owner: s.owner})
		}
	}
	s.pendCands = s.pendCands[:0]
}

// checkMail resolves meetings posted by the backward search: a posted state
// that is in this search's table joins this search's path to it with the
// backward search's pushes from it to the solved position. s.occ must be
// clear.
func (s *solver) checkMail() (string, bool) {
	if s.fwd == nil {
		return "", false
	}
	if s.pull {
		return s.pullCheckMail()
	}
	for {
		m, ok := s.fwd.Receive(s.owner)
		if !ok {
			return "", false
		}
		if sol, ok := s.confirmMail(m); ok {
			return sol, true
		}
	}
}

// meetLane is the lane id posted with a meeting.
func (s *solver) meetLane() int { return s.laneID }

// confirmMail tries one received meeting against this search's tree (exp-451
// split from checkMail so the hub lane can offer a message to two trees).
func (s *solver) confirmMail(m meet.Meeting) (string, bool) {
	if len(m.Boxes) != s.n || slices.ContainsFunc(m.Boxes, func(b int32) bool { return s.fidx[b] < 0 }) {
		return "", false
	}
	_, id, _ := s.find(s.pack(m.Boxes), m.Player)
	if id >= 0 {
		s.fwd.Confirmed.Add(1)
		prefix := s.reconstruct(id)
		at := meet.EndCell(prefix, s.lv.Player, s.lv.W)
		if tail := meet.Complete(s.lv.Wall, s.lv.W, m.Boxes, at, m.Tail); tail != "" || len(m.Tail) == 0 {
			return prefix + tail, true
		}
	} else if ref, ok := s.refGet(s.fwd.Key(m.Boxes, m.Player)); ok {
		prefix := s.reconstructRef(ref)
		if prefix != "" {
			s.fwd.Confirmed.Add(1)
			s.tr.SetExtra("met_via_ref", 1) // exp-659: resolved through a candidate reference
			at := meet.EndCell(prefix, s.lv.Player, s.lv.W)
			if tail := meet.Complete(s.lv.Wall, s.lv.W, m.Boxes, at, m.Tail); tail != "" || len(m.Tail) == 0 {
				return prefix + tail, true
			}
		}
	}
	if RetainFwdMail {
		for _, prev := range s.prevTrees {
			if prev == nil {
				continue
			}
			_, pid, _ := prev.find(prev.pack(m.Boxes), m.Player)
			if pid >= 0 {
				s.fwd.Confirmed.Add(1)
				prefix := prev.reconstruct(pid)
				at := meet.EndCell(prefix, prev.lv.Player, prev.lv.W)
				if tail := meet.Complete(prev.lv.Wall, prev.lv.W, m.Boxes, at, m.Tail); tail != "" || len(m.Tail) == 0 {
					return prefix + tail, true
				}
			} else if ref, ok := prev.refGet(s.fwd.Key(m.Boxes, m.Player)); ok {
				prefix := prev.reconstructRef(ref)
				if prefix != "" {
					s.fwd.Confirmed.Add(1)
					at := meet.EndCell(prefix, prev.lv.Player, prev.lv.W)
					if tail := meet.Complete(prev.lv.Wall, prev.lv.W, m.Boxes, at, m.Tail); tail != "" || len(m.Tail) == 0 {
						return prefix + tail, true
					}
				}
			}
		}
	}
	return "", false
}

// reconstructRef reconstructs the solution prefix up to candidate ref
// without mutating solver arena state.
func (s *solver) reconstructRef(ref uint64) string {
	p, i, t, side := unpackRef(ref)
	prefix := s.reconstruct(p)
	if p > 0 && prefix == "" {
		return ""
	}
	pc := make([]int32, s.n)
	s.unpack(p, pc)
	for _, x := range pc {
		s.occ[x] = true
	}
	s.reach(s.player[p], s.mark)
	seq := s.boxPathTo(pc[i], t, s.stamp, side)
	for _, x := range pc {
		s.occ[x] = false
	}
	if len(seq) == 0 {
		return ""
	}
	pushes := make([]meet.Push, len(seq))
	for j, pu := range seq {
		pushes[j] = meet.Push{From: pu.from, Dir: int8(pu.dir)}
	}
	at := meet.EndCell(prefix, s.lv.Player, s.lv.W)
	macro := meet.Complete(s.lv.Wall, s.lv.W, pc, at, pushes)
	if macro == "" && len(pushes) > 0 {
		return ""
	}
	return prefix + macro
}

// compactForMail drops search-only queues, frontiers, and novelty buffers
// from an idle solver so its state table and refs can be retained for meeting
// resolution with minimal memory footprint.
func (s *solver) compactForMail() {
	// The active search owns the direct predecessor list; retained trees must
	// not keep older generations alive through nested lists.
	s.prevTrees = nil
	s.frontier = fessFrontier{}
	s.cellIDs = nil
	s.mv = nil
	s.mvScore = nil
	s.mvStart = nil
	s.mvLen = nil
	s.mvNext = nil
	s.mvLeft = nil
	s.mvOff = nil
	s.mvTotal = nil
	s.novSeen = nil
	s.novPairs = u64set{}
	s.novPushSeen = nil
	s.novAccess = u64set{}
	s.accBuf = nil
	s.revGroup = nil
	s.revSize = nil
	s.revExits = u64set{}
	s.pushTo = nil
	s.revKey = nil
	s.revWords = nil
	s.revOffset = nil
	s.revBestExit = nil
	s.novWords = nil
	s.novTier0Words = nil
	s.novTier1Words = nil
	s.novOffset = nil
	s.edges = nil
	s.retargetNodes = 0
	s.edgeHead = nil
	s.liveChildren = nil
	s.actual = nil
	s.version = nil
	s.bucket = nil
	s.w = nil
	s.reg = nil
	s.dl = nil
	s.tgtDist = nil
	s.goalDist = nil
	s.goalReach = nil
	s.dead = nil
	s.wBuf = nil
	s.ordBuf, s.keyBuf = nil, nil
	s.maskBuf = nil
	s.candBuf = nil
}

// addPrevTree stores a previous forward search solver, compacted for mail,
// capping memory and retention queue length.
func (s *solver) addPrevTree(prev *solver) {
	if prev == nil {
		return
	}
	s.prevTrees = appendPrevTree(s.prevTrees, prev)
	for len(s.prevTrees) > 1 && s.budgetP != nil && s.budgetP.Load() > 0 && s.trackedBytes() > s.budgetP.Load()/2 {
		s.prevTrees = slices.Delete(s.prevTrees, 0, 1)
	}
}

// appendPrevTree owns and trims the direct list, clearing evicted references
// from its backing array so they do not retain entire search arenas.
func appendPrevTree(trees []*solver, prev *solver) []*solver {
	prev.compactForMail()
	trees = append(trees, prev)
	if len(trees) > 4 {
		trees = slices.Delete(trees, 0, len(trees)-4)
	}
	return trees
}

// ReparentCycleGuard (exp-353, -reparent-guard): refuse reparenting that would close a parent loop.
var ReparentCycleGuard = true

// createsCycle reports whether making parent the parent of id would close a
// loop (id is an ancestor of parent, or parent's chain is already cyclic).
func (s *solver) createsCycle(id, parent int32) bool {
	for n, p := len(s.parent), parent; p >= 0; p = s.parent[p] {
		if p == id {
			return true
		}
		if n--; n < 0 {
			return true
		}
	}
	return false
}

// reconstruct replays the push sequence and fills in player walks with BFS.
func (s *solver) reconstruct(id int32) string {
	// Each node is a macro move from its parent: the moved box started on
	// pushFrom and ended on the one cell the child has that the parent lacks.
	// Recompute the push path in the parent state.
	var chain []int32
	for ; s.parent[id] >= 0; id = s.parent[id] {
		if len(chain) > len(s.parent) {
			return "" // exp-353: parent loop; no reconstruction rather than an unbounded chain
		}
		chain = append(chain, id)
		s.stats.PathMoves++
		for a := range s.stats.PathAdvisors {
			if s.adv[id]&(1<<a) != 0 {
				s.stats.PathAdvisors[a]++
			}
		}
	}
	slices.Reverse(chain)
	pc, cc := make([]int32, s.n), make([]int32, s.n)
	var pushes [][2]int32
	for _, c := range chain {
		p := s.parent[c]
		s.unpack(p, pc)
		s.unpack(c, cc)
		t := s.pushFrom[c] // returning macros can leave all box squares unchanged
		for _, x := range cc {
			if !slices.Contains(pc, x) {
				t = x
			}
		}
		for _, x := range pc {
			s.occ[x] = true
		}
		s.reach(s.player[p], s.mark)
		for _, pu := range s.boxPathTo(s.pushFrom[c], t, s.stamp, int32(s.pushDir[c])) {
			pushes = append(pushes, [2]int32{pu.from, int32(pu.dir)})
		}
		for _, x := range pc {
			s.occ[x] = false
		}
	}

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
		if s.mark[target] != st {
			// An inconsistent push chain (e.g. a path search cut short):
			// no reconstruction rather than a walk through stale marks.
			for _, b := range lv.Boxes {
				s.occ[b] = false
			}
			clear(s.occ)
			return ""
		}
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

// moveUsed marks applied moves (bit 63 of the encoded move).
const moveUsed uint64 = 1 << 63

func (s *solver) finalStats() Stats {
	if memtrace.Enabled {
		s.moveUseReport()
	}
	st := s.stats
	st.Stored = len(s.player)
	st.MemMB = s.memBreakdown()
	return st
}

// memBreakdown reports the capacity (MB) held by the large per-node and
// per-queue structures, for introspection.
func (s *solver) memBreakdown() map[string]float64 {
	mb := func(n int, size int) float64 { return math.Round(float64(n*size)/(1<<20)*10) / 10 }
	macroLen := 0
	for _, m := range s.macros {
		macroLen += cap(m)
	}
	prevMB := 0.0
	for _, p := range s.prevTrees {
		if p != nil {
			macroLenP := 0
			for _, m := range p.macros {
				macroLenP += cap(m)
			}
			prevMB += mb(cap(p.boxes), 8) + mb(cap(p.player), 4) + mb(cap(p.parent), 4) + mb(cap(p.pushFrom), 4) + mb(cap(p.pushDir), 1) + mb(cap(p.table), 4) + mb(cap(p.tfp), 4) + mb(cap(p.refs), 8) + mb(macroLenP, 8)
		}
	}
	return map[string]float64{
		"boxes":       mb(cap(s.boxes), 8),
		"player":      mb(cap(s.player), 4),
		"parent":      mb(cap(s.parent), 4),
		"pushFrom":    mb(cap(s.pushFrom), 4),
		"pushDir":     mb(cap(s.pushDir), 1),
		"g":           mb(cap(s.g), 4),
		"w":           mb(cap(s.w), 4),
		"reg":         mb(cap(s.reg), 2) + mb(cap(s.adv), 1),
		"moves":       mb(cap(s.mv), 8) + mb(cap(s.mvOff), 4) + mb(cap(s.mvTotal), 4) + mb(cap(s.mvStart), 4) + mb(cap(s.mvLen), 4) + mb(cap(s.mvNext), 4) + mb(cap(s.mvLeft), 4) + mb(cap(s.bucket), 4) + mb(cap(s.novOffset), 4) + mb(cap(s.novWords), 8) + mb(cap(s.revOffset), 4) + mb(cap(s.revWords), 8) + mb(cap(s.revBestExit), 2),
		"move_scores": mb(cap(s.mvScore), 16),
		"frontier":    mb(s.frontier.bytes(), 1) + mb(cap(s.actual), 32) + mb(cap(s.version), 4) + mb(s.dv.bytesOrZero(), 1),
		"deadends":    mb(cap(s.dead), 1) + mb(cap(s.edgeHead), 4) + mb(cap(s.liveChildren), 4) + mb(cap(s.edges), 8),
		"hotspots":    mb(s.hot.bytes(), 1),
		"rooms":       mb(s.rooms.bytes(), 1),
		"table":       mb(cap(s.table), 4) + mb(cap(s.tfp), 4),
		"macros":      mb(macroLen, 8) + mb(len(s.macros), 48),
		"refs":        mb(cap(s.refs), 8),
		"packing_dag": mb(s.packingDAGBytes(), 1),
		"prev_trees":  prevMB,
	}
}

// memLen mirrors memBreakdown with lengths instead of capacities
// (memtrace: the difference is append-doubling slack).
func (s *solver) memLen() map[string]float64 {
	mb := func(n int, size int) float64 { return math.Round(float64(n*size)/(1<<20)*10) / 10 }
	macroLen := 0
	for _, m := range s.macros {
		macroLen += cap(m)
	}
	return map[string]float64{
		"boxes":       mb(len(s.boxes), 8),
		"player":      mb(len(s.player), 4),
		"parent":      mb(len(s.parent), 4),
		"pushFrom":    mb(len(s.pushFrom), 4),
		"pushDir":     mb(len(s.pushDir), 1),
		"g":           mb(len(s.g), 4),
		"w":           mb(len(s.w), 4),
		"reg":         mb(len(s.reg), 2) + mb(len(s.adv), 1),
		"moves":       mb(len(s.mv), 8) + mb(len(s.mvStart), 4) + mb(len(s.mvLen), 4) + mb(len(s.mvNext), 4) + mb(len(s.mvLeft), 4) + mb(len(s.bucket), 4) + mb(len(s.novOffset), 4) + mb(len(s.novWords), 8) + mb(len(s.revOffset), 4) + mb(len(s.revWords), 8) + mb(len(s.revBestExit), 2),
		"move_scores": mb(len(s.mvScore), 16),
		"frontier":    mb(s.frontier.bytes(), 1) + mb(len(s.actual), 32) + mb(len(s.version), 4),
		"deadends":    mb(len(s.dead), 1) + mb(len(s.edgeHead), 4) + mb(len(s.liveChildren), 4) + mb(len(s.edges), 8),
		"hotspots":    mb(s.hot.bytes(), 1),
		"rooms":       mb(s.rooms.bytes(), 1),
		"table":       mb(len(s.table), 4) + mb(len(s.tfp), 4),
		"macros":      mb(macroLen, 8) + mb(len(s.macros), 48),
	}
}

// mappedBytes is the memory the Go runtime has mapped and not released.
func mappedBytes() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64() - sample[1].Value.Uint64()
}

func mappedOver(limit, pct uint64) bool {
	sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()-sample[1].Value.Uint64() > limit/100*pct
}

func (s *solver) backstopHit(memLimit, pct, tracked uint64) bool {
	return mappedOver(memLimit, pct)
}

func (s *solver) overBudget(needed uint64) bool {
	b := uint64(0)
	if s.budgetP != nil {
		b = s.budgetP.Load()
		if s.budgetDiv > 1 {
			b /= s.budgetDiv
		} else if s.batDyn {
			b /= s.bat.live()
		}
	}
	if b == 0 || needed <= b {
		if s.elasticHold > 0 && needed <= b {
			releaseElastic(s.laneID, s.elasticHold)
			s.elasticHold = 0
		}
		return false
	}
	if !ElasticMem || s.memLimit == 0 {
		return true
	}
	// Centralized Atomic Headroom Token Pool (exp-327):
	// Allow elastic borrowing only through acquireElastic if !mappedOver(memLimit, 75).
	if mappedOver(s.memLimit, 75) {
		return true
	}
	extraNeeded := needed - b
	if extraNeeded <= s.elasticHold {
		if extraNeeded < s.elasticHold {
			diff := s.elasticHold - extraNeeded
			releaseElastic(s.laneID, diff)
			s.elasticHold = extraNeeded
		}
		return false
	}
	delta := extraNeeded - s.elasticHold
	if acquireElastic(s.laneID, delta) {
		s.elasticHold += delta
		return false
	}
	return true
}

func (s *solver) trackedBytes() uint64 {
	var total float64
	for _, v := range s.memBreakdown() {
		total += v
	}
	return uint64(total * (1 << 20))
}

// pairSideDeadlock reports whether the box just pushed to t forms a dead
// pair with a neighbour given the player's actual side: from is a free cell
// beside t in the player's region. Results are cached per (pair, from).
func (s *solver) pairSideDeadlock(t, from int32) bool {
	lv := s.lv
	if lv.Goal[t] || s.dist[t] < 0 {
		return false
	}
	if s.pairSide == nil {
		s.pairSide = make([]int8, len(lv.Wall)*len(s.poff)*4)
	}
	side := 0
	for side < 4 && t+s.dirs[side] != from {
		side++
	}
	if side == 4 {
		return false // from isn't beside t
	}
	// Verdicts are static (board, pair, player side): a flat table filled on
	// first use (0 unknown, 1 dead, 2 alive). An eager build costs 0.3-1.2 s
	// per level (the pair search floods the board per move).
	base := int(t) * len(s.poff)
	for k, o := range s.poff {
		x := t + o
		if x < 0 || int(x) >= len(lv.Wall) || !s.occ[x] || x == from || lv.Goal[t] && lv.Goal[x] {
			continue
		}
		v := &s.pairSide[(base+k)*4+side]
		if *v == 0 {
			*v = 2
			s.occ[t], s.occ[x] = false, false
			if s.pairSplits(t, x, from) && s.pairDeadFrom(t, x, from) {
				*v = 1
			} // else one player region: the side-agnostic pair table is exact
			s.occ[t], s.occ[x] = true, true
		}
		if *v == 1 {
			return true
		}
	}
	return false
}

// pairSplits reports whether boxes on a and b (alone on the board) leave
// the player's region from cell from smaller than all other floor: only
// then can the player's side matter.
func (s *solver) pairSplits(a, b, from int32) bool {
	lv := s.lv
	if s.pmark == nil {
		s.pmark = make([]uint32, len(lv.Wall))
	}
	s.pst++
	st := s.pst
	q := append(s.pq[:0], from)
	s.pmark[from] = st
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			if y := q[i] + d; s.pmark[y] != st && !lv.Wall[y] && y != a && y != b {
				s.pmark[y] = st
				q = append(q, y)
			}
		}
	}
	s.pq = q
	return len(q) < len(s.floorCells())-2
}

// moveUseReport (memtrace) publishes how many stored moves were ever applied
// and at which position in their node's sorted order: the case for storing
// only each node's first moves.
func (s *solver) moveUseReport() {
	var stored, used, opened int
	hist := map[string]float64{}
	bucket := func(k int) string {
		switch {
		case k < 4:
			return fmt.Sprintf("pos%d", k)
		case k < 8:
			return "pos4-7"
		case k < 16:
			return "pos8-15"
		case k < 32:
			return "pos16-31"
		}
		return "pos32+"
	}
	for id := range s.mvStart {
		st := s.mvStart[id]
		if st < 0 {
			continue
		}
		opened++
		for k := range int32(s.mvLen[id]) {
			stored++
			if s.mv[st+k]&moveUsed != 0 {
				used++
				hist[bucket(int(k))]++
			}
		}
	}
	hist["opened"], hist["stored"], hist["used"] = float64(opened), float64(stored), float64(used)
	memtrace.Publish(fmt.Sprintf("macro-o%d-moveuse", s.owner), len(s.player), hist, nil)
}

// st2 is a pair-search state: two box cells (x < y) and the normalized player.
type st2 struct{ x, y, p int32 }

// Direction (exp-179): the same FESS engine searches forward (pushes, from
// the start toward the goals) or backward (pulls, from the solved position
// toward the start squares, which are then its "goals").

// front is the cell the player must stand on to move the box at b in
// direction d: behind it to push, in front of it to pull.
func (s *solver) front(b, d int32) int32 {
	if s.pull {
		return b + d
	}
	return b - d
}

// playerAfter is the player's cell after the box arrived at nc moving in
// direction d: behind it after a push, a step beyond it after a pull.
func (s *solver) playerAfter(nc, d int32) int32 {
	if s.pull {
		return nc + d
	}
	return nc - d
}

// supportCell is the cell that must be floor (not wall) for a box to move
// from b to c = b+d: the player's cell behind b for a push, the player's
// destination beyond c for a pull.
func (s *solver) supportCell(b, c, d int32) int32 {
	if s.pull {
		return c + d
	}
	return b - d
}

// SpreadBucket coarsens the pull search's spread feature (pushes per cell).
var SpreadBucket = 4

// hotRel is Festival's forward HF hotspot count against a target board (the
// goals): boxes off the target squares that touch a box or a wall, plus
// empty target squares no box can be pushed onto from any side. s.occ holds
// the position.
func (s *solver) hotRel(boxes []int32) int {
	on, targets := s.lv.Goal, s.goals
	if s.tgt != nil {
		on, targets = s.tgt, s.tgtList
	}
	n := 0
	for _, b := range boxes {
		if on[b] {
			continue
		}
		for _, d := range s.dirs {
			if s.lv.Wall[b+d] || s.occ[b+d] {
				n++
				break
			}
		}
	}
	for _, g := range targets {
		if s.occ[g] {
			continue
		}
		ok := false
		for _, d := range s.dirs {
			x2 := g + 2*d
			if !s.lv.Wall[g+d] && x2 >= 0 && int(x2) < len(s.occ) && !s.lv.Wall[x2] && !s.occ[x2] {
				ok = true
				break
			}
		}
		if !ok {
			n++
		}
	}
	return n
}

// HFFrozenMatch (exp-270): as Festival's distance to the imagined board, a
// box frozen in a 2x2 block of walls and boxes can only match its own square,
// so one off the target's squares leaves no matching.
var HFFrozenMatch = false

// hfFrozenOff reports whether a box off the target squares sits in a 2x2
// block of walls and boxes (s.occ holds boxes).
func (s *solver) hfFrozenOff(boxes []int32) bool {
	w := int32(s.lv.W)
	for _, t := range boxes {
		if s.tgt != nil && s.tgt[t] {
			continue
		}
		for _, tl := range [4]int32{t, t - 1, t - w, t - w - 1} {
			if s.blocked(tl) && s.blocked(tl+1) && s.blocked(tl+w) && s.blocked(tl+w+1) {
				return true
			}
		}
	}
	return false
}

// matchDist is the min-cost matching of boxes to goals (push distances), or
// a large value when none exists.
func (s *solver) matchDist(boxes []int32) int {
	if d := s.assignTo(boxes, s.matchingDistances()); d >= 0 {
		return int(d)
	}
	return assignmentInf
}

func (s *solver) matchingDistances() []int32 {
	if s.pfDist != nil && s.pull {
		return s.pfDist // exp-275: the pull lane's matching to the real goals
	}
	if s.tgt != nil {
		return s.tgtDist
	}
	return s.goalDist
}

// setTarget makes the toward search rank against a target board's squares
// (packed, hotspots, matching distance) while pruning and solving stay
// those of the real level; reaching the board counts as solved (the caller
// appends the board's backward tail).
func (s *solver) setTarget(boxes []int32) {
	cells := len(s.lv.Wall)
	s.tgtPocket = nil
	s.tgt = make([]bool, cells)
	s.tgtList = slices.Clone(boxes)
	slices.Sort(s.tgtList)
	s.tgtDist = make([]int32, len(s.tgtList)*cells)
	for i := range s.tgtDist {
		s.tgtDist[i] = -1
	}
	for g, goal := range s.tgtList {
		s.tgt[goal] = true
		d := s.tgtDist[g*cells : (g+1)*cells]
		if s.rg != nil {
			s.rg.Backward([]int32{goal}, d)
			continue
		}
		d[goal] = 0
		q := []int32{goal}
		for i := 0; i < len(q); i++ {
			c := q[i]
			for _, dir := range s.dirs {
				b := c - dir
				if !s.lv.Wall[b] && !s.lv.Wall[s.supportCell(b, c, dir)] && d[b] < 0 {
					d[b] = d[c] + 1
					q = append(q, b)
				}
			}
		}
	}
}

func (s *solver) onTarget(boxes []int32) bool {
	for _, b := range boxes {
		if !s.tgt[b] {
			return false
		}
	}
	return true
}

// peerBytes is what a search sharing this one's budget holds (exp-278).
func (s *solver) peerBytes() uint64 {
	var n uint64
	if s.peerTracked != nil {
		n = s.peerTracked.Load()
	}
	if s.peerTracked2 != nil {
		n += s.peerTracked2.Load() // exp-451: the hub lane's second forward context
	}
	return n
}
