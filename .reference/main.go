package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"soksolv/cluster"
	"soksolv/deadzone"
	"soksolv/full"
	"soksolv/holes"
	"soksolv/meet"
	"soksolv/memtrace"
	"soksolv/reach"
	"soksolv/reverse"
	"soksolv/trace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"soksolv/verify"
)

type Output struct {
	Level        string  `json:"level"`
	Solved       bool    `json:"solved"`
	Reason       string  `json:"reason,omitempty"`
	TimeMs       float64 `json:"time_ms"`
	Moves        int     `json:"moves"`
	Pushes       int     `json:"pushes"`
	Nodes        int     `json:"nodes_expanded"`
	PeakMemoryMB float64 `json:"peak_memory_mb"`
	Solution     string  `json:"solution,omitempty"`
	Strategy     string  `json:"strategy,omitempty"`
	Stats        struct {
		Stats
		NodesPerSec float64 `json:"nodes_per_sec"`
		// Per-strategy progress and end state (always recorded), and the
		// strategy whose heuristic got closest to a solution.
		Portfolio map[string]*trace.Summary `json:"portfolio,omitempty"`
		Furthest  string                    `json:"furthest,omitempty"`
		// Bidirectional meetings posted by forward searches / confirmed.
		MeetPosted    int64 `json:"meet_posted,omitempty"`
		MeetConfirmed int64 `json:"meet_confirmed,omitempty"`
	} `json:"stats"`
}

var countSuffix = regexp.MustCompile(`_\d+$`)

// levelName turns "Microban_155.xsb" + 1 into "microban-1".
func levelName(path string, idx int) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stem = countSuffix.ReplaceAllString(stem, "")
	stem = strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(stem))
	return fmt.Sprintf("%s-%d", stem, idx)
}

var (
	CrossSynergy   = true
	CleanPerimeter = true
	RetainFwdMail  = true
	MemFusePct     = 150  // exp-346: exit "memory (fuse)" above this % of -mem (0: off)
	RestartBackoff = true // exp-346: back off quick reverse memory restarts
	ElasticMem     = true // exp-326/327: dynamic memory sharing and central headroom token pool
)

func main() {
	runtime.GOMAXPROCS(4) // portfolio: three strategies plus the pattern learner, one core each (wide: 6, -pull/-toward: more, set after flags)
	stallScale := flag.Bool("stall-scale", true, "exp-349 (default on): scale StallFinish cap/floor, CellsAfter and lane-c-at by t/600 s")
	timeLimit := flag.Float64("t", 0, "time limit in seconds (0: none, until solved or out of memory)")
	only := flag.String("strategy", "all", "run one lane only (macro, novelty, fa2, hub-pull, hub-gen, hub, toward, pull, full, reverse, hf, ...; historical IDs festx/festb are accepted) or all as a portfolio")
	memMB := flag.Int("mem", 15360, "memory limit in MiB (user 2026-11-10: up to 15 GB, so runs fit an 8-core/16 GB benchmark machine; Festival native peaks ~6.9 GB)")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile to this file")
	memTrace := flag.String("memtrace", "", "write memory samples (JSON lines, every 250 ms) to this file; see cmd/memreport")
	memProfile := flag.String("memprofile", "", "write a heap profile (at the end of the search) to this file")
	flag.BoolVar(&trace.Detail, "trace", false, "add periodic node/memory samples to each strategy's progress summary")
	flag.BoolVar(&allFinish, "all-finish", false, "diagnostics: keep the other strategies running after the first solution, to time each one (reports the first solution)")
	flag.BoolVar(&reverse.FreeStart, "rev-free", false, "reverse: phase 0 scores boxes by distance to the nearest free start square (the second lane uses the other heuristic)")
	flag.BoolVar(&full.FreeGoal, "full-free", true, "full: score boxes by distance to the nearest free goal")
	flag.IntVar(&revLanes, "rev-lanes", 2, "reverse: 1 = one search (heuristic per -rev-free), 2 = free-start and nearest-start lanes time-sharing the core")
	flag.BoolVar(&BestCellTurns, "fess-best", true, "FESS: alternate best-cell and round-robin cell selection (Festival)")
	flag.BoolVar(&NewCellJump, "fess-jump", true, "FESS: the round robin visits each newly created cell next (Festival)")
	flag.BoolVar(&holes.Enabled, "close-holes", holes.Enabled, "exp-464: at level load, floor cells with >= 3 wall neighbours, no goal, no box and not the player's start become walls (cascading)")
	flag.BoolVar(&GoalAreaMemo, "ga-memo", GoalAreaMemo, "exp-586: goal-area deadlock reuses survivors for a recurring frozen goal set (exact key; search-identical)")
	flag.BoolVar(&reach.Enabled, "exact-reach", reach.Enabled, "exp-464: box distance/dead-square tables respect the player's access (side components), not just a non-wall player cell")
	flag.Var(&DiagDead, "diag-dead", "exp-459: prune pushes that create a diagonal-chain deadlock: target (toward/festb/festx forward lanes only), true (also macro and novelty), false; default target; env SOKSOLV_DIAG_DEAD")
	flag.BoolVar(&SideDead, "side-dead", true, "FESS and full: prune states with a box whose player side can no longer reach a goal")
	flag.BoolVar(&PairSide, "pair-side", true, "FESS: two-box deadlocks evaluated from the player's actual side")
	flag.BoolVar(&full.Penalties, "penalties", true, "full search: learned cluster push-cost penalties in queue keys (exp-155)")
	flag.IntVar(&Novelty, "novelty", 0, "FESS: novel moves (box onto a square not reached before in the node's cell) per opened node that cost nothing, as advisor picks (exp-192; 0: off)")
	flag.BoolVar(&reverse.Walks, "rev-walks", false, "reverse lanes: random-walk escape from plateaus (exp-198)")
	flag.Int64Var(&reverse.WalkAfter, "rev-walk-after", 5000, "reverse walks: expansions without improvement before walking")
	flag.IntVar(&reverse.WalkCount, "rev-walk-count", 16, "reverse walks per stall")
	flag.IntVar(&reverse.WalkLen, "rev-walk-len", 50, "reverse walk length (pulls)")
	flag.IntVar(&reverse.TypesEvery, "rev-types-every", 4, "reverse lanes: one pop in this many from the type queue (exp-197)")
	flag.BoolVar(&reverse.Types, "rev-types", false, "reverse lanes: type-based exploration (exp-197)")
	flag.BoolVar(&reverse.Novelty, "rev-novelty", false, "reverse lanes: a third queue of novel states (best-first width search, exp-196)")
	flag.Uint64Var(&Seed, "seed", 0, "FESS: seeded tie-breaking (exp-195; 0: off)")
	flag.BoolVar(&LazyCluster, "lazy-cluster", true, "FESS: cluster deadlock search only when a move is applied (exp-199)")
	flag.BoolVar(&LazyChild, "lazy-child", true, "exp-659 (promoted 2026-11-12): identical-search per-child cost cuts: one-room interior moves settled from the opened node's room fragments, top-k window selection, near-phase childStatus, incremental OOP")
	flag.BoolVar(&FrontierCorral, "corral-frontier", true, "FESS: certify first-push corral obligations with outside boxes removed")
	flag.BoolVar(&PackingObligations, "packing-obligations", true, "FESS: fallback packing advice from ready graph commitments and route clearance")
	flag.BoolVar(&Novelty2, "novelty2", true, "FESS novelty of width 2: new box-square pairs in the cell also count (exp-194)")
	flag.BoolVar(&NoveltyLane, "novelty-lane", true, "portfolio: an extra forward FESS lane with every novel move free (exp-192)")
	flag.BoolVar(&AccessNovelty, "access-novelty", true, "exp-289: access-aware capability novelty in FESS (push-side, passage, displacement)")
	flag.BoolVar(&RevGroups, "rev-groups", true, "exp-290: reversible preparation groups and exit prioritization")
	flag.BoolVar(&ResumeWindowFix, "resume-window-fix", true, "preserve exhausted move windows across slices")
	flag.IntVar(&MoveWindow, "move-window", 8, "FESS: store at most this many moves per opened node, re-opening for the next window (0: all; exp-165)")
	flag.Int64Var(&NodeBudget, "nodes", 0, "deterministic screening: a single FESS/pull strategy stops after this many expansions, ignoring wall time (with -strategy)")
	flag.BoolVar(&pullOn, "pull", true, "run the FESS engine backward (pull moves) as an extra backward lane on its own core (exp-179)")
	flag.BoolVar(&TargetFloorFix, "target-floor-fix", true, "preserve replacement goals on alternate floor characters")
	flag.BoolVar(&towardOn, "toward", true, "FESS toward the backward search's best connected state after 1/3 of the time (exp-177)")
	flag.BoolVar(&reverse.Compact, "compact", reverse.Compact, "exp-397: reverse lanes store nodes as delta records (keyframe every 4th node down a chain)")
	flag.BoolVar(&reverse.RevCells, "rev-cells", true, "reverse: FESS-style cell scheduling on lane 0")
	flag.BoolVar(&ClusterDead, "cluster-dead", true, "FESS and full: prove clusters of 3-5 touching boxes dead (shared between searchers)")
	flag.BoolVar(&revMatch, "rev-match", false, "reverse: the first lane uses the exact matching heuristic (diagnostics)")
	flag.BoolVar(&revSupplyNoMacro, "rev-supply-nomacro", false, "exp-707 ablation: -rev-supply without placement macros")
	flag.BoolVar(&revSupplyRing, "rev-supply-ring", false, "exp-707: with -rev-supply, only the reverse ring's odd phases (restarts after a memory fill) are supply-aware; phase 0 stays plain")
	flag.BoolVar(&revSupplyInh, "rev-supply-inherit", false, "exp-707: with -rev-supply, children of a node with U = u inherit u-1 as a provisional penalty (a lower bound when an off-start box is pulled), so fewer are evaluated and deferred")
	flag.IntVar(&revSupply, "rev-supply", 4, "exp-707: reverse lane: supply-aware ordering with weight W (0: off): exact player-tracked supply count U in the key (+W*8 per unit) and cell, placement macros at U = 0")
	flag.BoolVar(&InefficientPrune, "inefficient", false, "FESS: skip pushes parking a box over a one-wide hole (heuristic, Festival)")
	flag.StringVar(&PlanFile, "plan-file", "", "exp-472 oracle: load Festival strategy-A plan entries (JSON) as the packed feature and OOP cell set")
	flag.BoolVar(&PlanFileOOP, "plan-file-oop", true, "exp-472: with -plan-file, out-of-plan = box not on a plan parking cell")
	flag.BoolVar(&PackedFest, "packed-festorder", false, "packed feature: Festival prefix semantics over the plan (exp-409)")
	flag.BoolVar(&PlanV2, "plan-v2", false, "packing planner v2: best-first backward pull search removing boxes onto unused start cells, Festival-style event-prefix packed (exp-411)")
	flag.BoolVar(&RealSink, "real-sink", true, "FESS planner: sink squares must be push-reachable from a start square")
	flag.IntVar(&CorralDead, "corral-dead", 200, "FESS: prove corral deadlocks of applied pushes, zone search capped at this many states (exp-227; 0: off)")
	flag.BoolVar(&SharedCorral, "corral-shared", SharedCorral, "share corral deadlock verdicts between searchers (exp-228/288)")
	flag.BoolVar(&deadzone.Matching, "corral-matching", true, "deadzone: matching with fixed boxes as walls (exp-229)")
	flag.BoolVar(&deadzone.Groups, "corral-groups", true, "deadzone: also search groups of 3+ corrals linked by fence boxes (exp-230)")
	flag.BoolVar(&HFRegRel, "hf-reg-rel", false, "exp-460: toward searches count player regions relative to the target (pockets the target also seals are free)")
	flag.BoolVar(&HFMatchFirst, "hf-match-first", false, "exp-254: toward-hf ranks states with no box-to-target matching last")
	flag.Int64Var(&HFPullNodes, "hf-pull-nodes", 0, "fixed pull work for standalone HF, permitting a wall-timed forward phase")
	flag.BoolVar(&HFIncrementalMatch, "hf-incremental-match", HFIncrementalMatch, "exp-262: reuse the parent assignment when scoring a one-box macro")
	flag.BoolVar(&HFBestFest, "hf-best-fest", false, "exp-254: toward-hf best-so-far in Festival's HF order (regions, boxes on target, hotspots)")
	flag.BoolVar(&HFOneAdvisor, "hf-one-advisor", false, "exp-254: toward-hf uses only Festival's HF default advisor")
	flag.BoolVar(&HFUnweighted, "hf-unweighted", false, "exp-254: toward-hf moves cost 0 for the first 3/4 of its expansions")
	flag.BoolVar(&HFNoReturn, "hf-no-return", false, "exp-268: toward-hf: no macros returning their box to its own square")
	flag.BoolVar(&FestCorral, "hf-fest-corral", false, "exp-269: toward-hf: Festival's component-growing PI-corral detection")
	flag.BoolVar(&HFFrozenMatch, "hf-frozen-match", false, "exp-270: toward-hf: a box frozen in 2x2 off target squares leaves no matching")
	flag.BoolVar(&HFFestSched, "hf-fest-sched", false, "exp-271: toward-hf: Festival's best-ancestor cells and best-so-far order")
	flag.BoolVar(&FestIneff, "hf-fest-ineff", false, "exp-272: toward-hf: Festival's inefficient-position pruning (Festival's HF search never applies it: exp-273)")
	flag.BoolVar(&retarget, "retarget", retarget, "exp-277: the toward lane retargets to the pull lane's newer best after a doubling minimum slice (from -retarget-slice)")
	flag.DurationVar(&retargetSlice, "retarget-slice", retargetSlice, "exp-277: first minimum slice before a retarget")
	flag.IntVar(&maxProcs, "procs", maxProcs, "exp-277: cap on threads running Go code (0: one per lane)")
	flag.IntVar(&festbFwdRatio, "festb-fwd-ratio", festbFwdRatio, "exp-278: festb's forward slice in backward slices")
	flag.StringVar(&HubStrataDump, "hub-strata-dump", "", "exp-686: -strategy hub-strata writes the hub-pull pull tree's best node per stratum (v0, v1, baseline) to this file as HUB blocks")
	flag.StringVar(&HubStrataPlans, "hub-strata-plans", "", "exp-732: with -hub-strata-dump, also write each dumped tip as a soft plan file PREFIX.N.json (N = -target-index)")
	flag.BoolVar(&HubNoSpin, "hub-nospin", HubNoSpin, "exp-754: an idle hub-gen round (no forward slice, no generator expansion, scan caught up) backs off (1 ms doubling to 64 ms) instead of re-entering at once")
	flag.BoolVar(&HubIdleRing, "hub-idle-ring", HubIdleRing, "exp-758: hub-gen revisits candidates its strided harvest skipped when idle, and ends a config that stays idle (-hub-idle-k rounds at the -hub-nospin back-off cap, implied) or whose first burst found no candidate; after such an end the lane's ring alternates strategy B (odd k) and hub mode (even k)")
	flag.IntVar(&HubIdleK, "hub-idle-k", HubIdleK, "exp-758: -hub-idle-ring idle rounds at the 64 ms back-off cap before the config ends")
	flag.BoolVar(&HubRace, "hub-race", HubRace, "exp-686: hub-pull races the best pull tip per stratum in expansion-budget trials (shared tried-strata table with hub-gen)")
	flag.IntVar(&HubRaceSig, "hub-race-sig", HubRaceSig, "exp-686: -hub-race strata signature (0: goals+boxes per room, 1: half-depth ancestor)")
	flag.BoolVar(&HubRaceTrials, "hub-race-trials", HubRaceTrials, "exp-686: -hub-race runs the strata trials (false: the base pursuit only)")
	flag.Float64Var(&HubRaceBaseFrac, "hub-race-base-frac", HubRaceBaseFrac, "exp-686b: -hub-race base pursuit's share of forward expansions (0.05-0.95)")
	flag.BoolVar(&HubRaceBase, "hub-race-base", HubRaceBase, "exp-686: -hub-race keeps the old frontier.best pursuit on half the forward expansions")
	flag.Int64Var(&HubRaceUnit, "hub-race-unit", HubRaceUnit, "exp-686: -hub-race first trial budget (forward expansions)")
	flag.BoolVar(&HubFallbackCheck, "hub-fallback-check", HubFallbackCheck, "exp-680: hub-gen's empty-pool fallback hub (tier 4) must pass the pool's start checks (exact single-box reach matching, assignment)")
	flag.StringVar(&HubDump, "hub-dump", "", "exp-449: festb/festx append each forward target (hub board) they choose to this file")
	flag.StringVar(&TargetFile, "target-file", "", "exp-449: -strategy tfwd: fixed forward target board file (HUB/IMAGINED blocks)")
	flag.IntVar(&TargetIndex, "target-index", -1, "exp-449: which board of -target-file (negative: from the end)")
	flag.StringVar(&HubLine, "hub-line", "", "exp-459: -strategy tfwd: write the root-to-best-node line as xsb boards to this file")
	flag.BoolVar(&HubNoCorral, "hub-nocorral", false, "exp-449: toward search without corral pruning")
	flag.BoolVar(&HubChar, "hub-char", false, "exp-449: tfwd: only characterise the target board")
	flag.DurationVar(&festbUnit, "festb-unit", festbUnit, "exp-278: festb's first round per phase")
	flag.StringVar(&festX, "festx", festX, "exp-283: an extra Festival-style lane: B, D (summed goal distance), E (D + biconnectivity), F (B + biconnectivity)")
	flag.BoolVar(&PFFestAdv, "pf-fest-adv", PFFestAdv, "exp-609: Festival-ordered pull advisor ties in board order and skips children already in the tree (Festival set_default_advisor)")
	flag.BoolVar(&DiveLane, "dive", DiveLane, "exp-691: add the dive lane (landmark-phased greedy backward search) on boards where a start-empty goal has solved-board peel depth >= -dive-min-peel")
	flag.StringVar(&DiveShare, "dive-share", DiveShare, "exp-693: on gated boards run the dive inside this lane's goroutine (baton slices; \"pull\") instead of as its own lane")
	flag.StringVar(&DiveReplace, "dive-replace", DiveReplace, "exp-693: on gated boards drop this lane (e.g. reverse, pull); the dive keeps its own lane")
	flag.Uint64Var(&DiveMemW, "dive-mem-w", DiveMemW, "exp-693: the dive lane's memory weight on gated boards (reverse 3, other lanes 1)")
	flag.BoolVar(&DivePair, "dive-pair", DivePair, "exp-693: the dive lane alternates the base and the -dive-a dive on one core (baton), half the memory each")
	flag.BoolVar(&DiveA, "dive-a", DiveA, "exp-693: the dive adds Festival strategy A's terms (fewer crowded boxes, more box-gap-box) after U")
	flag.BoolVar(&DiveFastApply, "dive-fast-apply", DiveFastApply, "exp-693: the dive stores children without a second box search")
	flag.Int64Var(&DiveShareMask, "dive-share-mask", DiveShareMask, "exp-693: -dive-share engines yield every mask+1 expansions (power of two minus one)")
	flag.IntVar(&DiveMinPeel, "dive-min-peel", DiveMinPeel, "exp-691: the dive lane's activation peel depth")
	flag.StringVar(&DiveRet, "dive-ret", DiveRet, "exp-691: the dive's return axes: both, r1 (boxes on start squares) or r2 (prefix)")
	flag.IntVar(&PFZoneDead, "pf-zone-dead", PFZoneDead, "exp-609: Festival-ordered pull (festx/festb) drops start zones a bounded exhaustive pull search of this many positions proves dead (0: off)")
	flag.BoolVar(&PFReach, "pf-reach", PFReach, "exp-619: Festival-ordered pull (festx/festb) prunes states and filters hubs by the exact (player-aware) single-box-reach matching to the start squares")
	flag.BoolVar(&PFAdvFix, "pf-adv-fix", PFAdvFix, "exp-605: Festival-ordered pull (festx/festb): the parent score carries its region count, so the advisor exists without a region merge")
	flag.BoolVar(&macroLane, "macro-lane", macroLane, "exp-283: run the macro (plain FESS) lane")
	flag.BoolVar(&fullLane, "full-lane", fullLane, "exp-283: run the full (greedy best-first) lane")
	flag.BoolVar(&hubRegRel, "hub-reg-rel", hubRegRel, "exp-480: hub lane forward contexts use -hf-reg-rel semantics (other lanes follow -hf-reg-rel, default off)")
	flag.BoolVar(&hubOn, "hub", hubOn, "exp-451: the festb slot runs the hub lane (many hubs from one pull tree, doubling commitment windows; below -hub-from it is festb)")
	flag.IntVar(&hubCtxN, "hub-ctx", hubCtxN, "exp-451: live forward contexts (1 or 2)")
	flag.IntVar(&hubGenTopK, "hub-gen-topk", hubGenTopK, "exp-453: -hub-back G keeps this many children per expansion")
	flag.IntVar(&hubGenMacro, "hub-gen-macro", hubGenMacro, "exp-453: -hub-back G pull-walk states explored per box (1: single pulls)")
	flag.BoolVar(&hubGenCells, "hub-gen-cells", hubGenCells, "exp-453: -hub-back G alternates best-first with the least-expanded (regions, trapped) cell")
	flag.IntVar(&hubGenPockets, "hub-gen-pockets", hubGenPockets, "exp-453: -hub-back G also admits hubs with up to this many floor regions (player region plus sealed pockets; use with -hf-reg-rel); 1: strict one-region only")
	flag.BoolVar(&hubGenBic, "hub-gen-bic", hubGenBic, "exp-453: -hub-back G ranks by the biconnectivity count (off-cycle cells) after trapped boxes")
	flag.StringVar(&hubGenKey, "hub-gen-key", hubGenKey, "exp-453: -hub-back G last two key terms: fm (far then mobility) or mf")
	flag.Float64Var(&hubGenCap, "hub-gen-cap", hubGenCap, "exp-469: -hub-back G generator memory cap as a fraction of the lane memory budget (0: default budget/2 - min(tracked, budget/4))")
	flag.Float64Var(&hubGenInit, "hub-gen-init", hubGenInit, "exp-453: -hub-back G fraction of the hub phase spent generating before the first nomination")
	flag.BoolVar(&hubGenReach, "hub-gen-reach", true, "exp-463: -hub-back G keeps only hubs the start boxes can reach (exact single-box reach classes in the generator, exact matching at nomination)")
	flag.BoolVar(&hubGenOnly, "hub-gen-only", false, "exp-453: -hub-back G generator-quality harness: generate alone (-t or -nodes), print the best hubs, return")
	flag.StringVar(&hubInject, "hub-inject", hubInject, "exp-451 diagnostics: commit these boards (exp-449 hub files) first, as a positive control for in-lane reach")
	flag.BoolVar(&festBLane, "festb", true, "exp-276: an extra lane running Festival's strategy B (Festival-ordered pull search, then Festival-faithful toward search)")
	flag.IntVar(&PFTypes, "pf-types", PFTypes, "exp-279: one selection in N is a random move of a random cell in Festival-ordered pull search (0: off)")
	flag.BoolVar(&festXRootBare, "festx-root-bare", festXRootBare, "exp-285: festx scores root moves on a board without boxes (pfStyle.rootBare)")
	flag.BoolVar(&PFExactDist, "pf-exact-dist", PFExactDist, "exp-285: Festival-ordered pull uses push distances with player access (Festival distance_from_to)")
	flag.BoolVar(&PFFestRooms, "pf-fest-rooms", PFFestRooms, "exp-285: Festival-ordered pull scores rooms like Festival (lost room pairs)")
	flag.BoolVar(&PFIneff, "pf-ineff", PFIneff, "exp-696: Festival-ordered pull solvers (hub-pull, hub-gen) drop pulls that park a box in a forbidden pull tunnel or a walled nook (Festival's pull-mode is_inefficient_position; start squares exempt)")
	flag.BoolVar(&PFRoomsOcc, "pf-rooms-occ", PFRoomsOcc, "exp-696: Festival room scoring follows rooms_bfs: a box on a neighbouring room's boundary cell does not break the link")
	flag.BoolVar(&DiveIneff, "dive-ineff", DiveIneff, "exp-696: the dive lane applies -pf-ineff's pull-mode inefficient-position filter")
	flag.BoolVar(&hfFestival, "hf-festival", true, "exp-268..273: Festival forward-HF bundle (no-return, fest-corral, frozen-match, fest-sched, unweighted, match-first, one-advisor, best-fest, fest-ineff; exp-273 ablation: trimming fest-ineff was neutral/noisy, one-advisor+best-fest matter)")
	flag.BoolVar(&SaturateScores, "saturate-scores", true, "exp-254 fix: huge move scores saturate instead of wrapping")
	flag.BoolVar(&PullBestFeasible, "pull-best-feasible", true, "exp-252: the pull lane's best state (forward target) must pass the start-square matching")
	flag.BoolVar(&cluster.Salt, "verdict-salt", true, "exp-688: salt the shared cluster/deadzone verdict tables with a per-level wall/goal hash")
	flag.BoolVar(&TargetProofPrivate, "target-proof-private", true, "isolate target-goal proof caches")
	flag.BoolVar(&TowardHF, "toward-hf-rank", TowardHF, "the toward lane runs on the real level with Festival's forward HF ranking (exp-189/244)")
	flag.IntVar(&gcLimitPct, "gc-limit", 72, "Go soft memory limit as % of -mem (0: none; exp-246)")
	flag.IntVar(&gcPercent, "gogc", 100, "Go GC percent (exp-246)")
	flag.Float64Var(&towardWait, "toward-wait", towardWait, "toward lane: fraction of the time before it starts (exp-246)")
	flag.BoolVar(&deadzone.BitFlood, "dz-bits", true, "deadzone: word-parallel floods (exp-248)")
	flag.BoolVar(&deadzone.FastKey, "dz-fastkey", true, "deadzone: cache keyed by the real player region (exp-248)")
	flag.BoolVar(&FixInvariants, "fix-invariants", FixInvariants, "exp-286: search integrity and exploration invariant bundle")
	flag.BoolVar(&StableWindows, "stable-windows", StableWindows, "exp-286: stable novelty enumeration across move windows")
	flag.BoolVar(&TransactionalOpen, "transactional-open", TransactionalOpen, "exp-286: atomic move generation without interruption corrupting node state")
	flag.BoolVar(&RootBareScore, "root-bare-score", RootBareScore, "exp-285/286: score root moves on bare board without corrupting legal occupancy")
	flag.BoolVar(&DisplacementLandmarksOn, "displacement-landmarks", DisplacementLandmarksOn, "exp-287: certified mandatory displacement landmarks")
	flag.BoolVar(&CrossSynergy, "cross-synergy", CrossSynergy, "exp-288: cross-lane perimeter and retargeting synergy bundle")
	flag.BoolVar(&CleanPerimeter, "clean-perimeter", CleanPerimeter, "exp-288: publish only live deduplicated candidate states to perimeter")
	flag.BoolVar(&RetainFwdMail, "retain-fwd-mail", RetainFwdMail, "exp-288: retain previous forward trees across retargets and drain mail during backward rounds")
	flag.BoolVar(&RetargetReuse, "retarget-reuse", RetargetReuse, "exp-299: active graph reuse on retarget without restarting")
	flag.BoolVar(&ReparentCycleGuard, "reparent-guard", true, "exp-353: refuse FESS reparenting that closes a parent loop (reconstruct always stops on loops)")
	flag.IntVar(&MemFusePct, "mem-fuse", 150, "last-resort exit when mapped memory stays above this % of -mem (0: off; exp-346)")
	flag.BoolVar(&RestartBackoff, "restart-backoff", true, "pace reverse lanes' quick memory restarts (exp-346)")
	flag.BoolVar(&LaneRevive, "lane-revive", true, "exp-588/601 (promoted 2026-11-10, default on): forward lanes (pull, novelty, festb, festx) that stop on memory restart fresh with a new exploration seed instead of exiting")
	flag.BoolVar(&ElasticMem, "elastic-mem", true, "dynamic memory sharing: lanes borrow from unreserved process headroom (exp-326/327)")
	flag.IntVar(&ElasticPoolPct, "elastic-pool-pct", 40, "exp-710: elastic pool size as % of -mem (0: 72%-70%, i.e. 2%); the 75% mapped backstop still applies")
	flag.BoolVar(&CorridorFlow, "corridor-flow", true, "corridor net flow and transit capacity obligation pruning (exp-330)")
	flag.BoolVar(&noMeet, "no-meet", false, "disable the bidirectional perimeter between the backward and forward searches")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: soksolv [-t seconds] <file.xsb> [level index, 1-based]")
	}
	flag.Parse()
	relayWatchParent() // exp-747: a relay child exits with its parent
	HubRaceBaseFrac = min(max(HubRaceBaseFrac, 0.05), 0.95)
	*only = canonLane(*only) // exp-639: historical lane IDs (festx, festb)
	var stableWindowsSet, transactionalOpenSet, rootBareScoreSet bool
	var cleanPerimeterSet, retainFwdMailSet, sharedCorralSet bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "stable-windows":
			stableWindowsSet = true
		case "transactional-open":
			transactionalOpenSet = true
		case "root-bare-score":
			rootBareScoreSet = true
		case "clean-perimeter":
			cleanPerimeterSet = true
		case "retain-fwd-mail":
			retainFwdMailSet = true
		case "corral-shared":
			sharedCorralSet = true
		}
	})
	if !FixInvariants {
		if !stableWindowsSet {
			StableWindows = false
		}
		if !transactionalOpenSet {
			TransactionalOpen = false
		}
		if !rootBareScoreSet {
			RootBareScore = false
		}
	}
	if !CrossSynergy {
		if !cleanPerimeterSet {
			CleanPerimeter = false
		}
		if !retainFwdMailSet {
			RetainFwdMail = false
		}
		if !sharedCorralSet {
			SharedCorral = false
		}
	}
	full.NodeBudget, reverse.NodeBudget = NodeBudget, NodeBudget // -nodes applies to every engine run alone
	reverse.ElasticMem = ElasticMem
	reverse.AcquireElastic = acquireElastic
	reverse.ReleaseElastic = releaseElastic
	// One thread per portfolio lane (lane.go), at most -procs. Before
	// exp-640 a hand count that missed the fa2 lane gave 7 threads to 8
	// lanes once exp-625 retired reverse-b.
	if n := len(laneShares("all")); maxProcs > 0 {
		runtime.GOMAXPROCS(min(n, maxProcs))
	} else {
		runtime.GOMAXPROCS(n)
	}
	full.SideDead = SideDead
	full.DiagDead = DiagDead == diagAll
	full.ClusterDead = ClusterDead
	deadzone.Salt = cluster.Salt
	if ClusterDead {
		cluster.Shared.Store(cluster.NewTable(16))
		cluster.SharedPen.Store(cluster.NewTable(16)) // proven patterns, shared by all searchers
	}
	if SharedCorral {
		deadzone.Shared.Store(deadzone.NewTable(18)) // corral verdicts, shared by all searchers (exp-228)
	}
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := flag.Arg(0)
	idx := 1
	if flag.NArg() > 1 {
		fmt.Sscan(flag.Arg(1), &idx)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	levels := splitCollection(data)
	if idx < 1 || idx > len(levels) {
		fmt.Fprintf(os.Stderr, "level %d out of range (file has %d)\n", idx, len(levels))
		os.Exit(1)
	}
	lv, err := parseLevel(levels[idx-1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "level %d: %v\n", idx, err)
		os.Exit(1)
	}
	if RefuteRoot { // exp-681 diag: refute the given board itself
		refuteRootMain(lv)
		return
	}
	if DiveLane && diveMaxPeel(lv) >= DiveMinPeel { // exp-691: the dive lane joins deep-peel boards only
		diveGated = true
		if n := len(laneShares("all")); maxProcs > 0 {
			runtime.GOMAXPROCS(min(n, maxProcs))
		} else {
			runtime.GOMAXPROCS(n)
		}
	}
	if FactorLane && factorGate(lv) { // exp-645: the factor lane joins only boards with >= 2 box components
		factorGated = true
		if n := len(laneShares("all")); maxProcs > 0 {
			runtime.GOMAXPROCS(min(n, maxProcs))
		} else {
			runtime.GOMAXPROCS(n)
		}
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pprof.StartCPUProfile(f)
	}
	if hfFestival {
		// Defaults of the bundle; switches given explicitly keep their values.
		set := map[string]bool{}
		flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
		for name, v := range map[string]*bool{"hf-no-return": &HFNoReturn, "hf-fest-corral": &FestCorral,
			"hf-frozen-match": &HFFrozenMatch, "hf-fest-sched": &HFFestSched,
			"hf-unweighted": &HFUnweighted, "hf-match-first": &HFMatchFirst,
			"hf-one-advisor": &HFOneAdvisor, "hf-best-fest": &HFBestFest, "hf-fest-ineff": &FestIneff} {
			if !set[name] {
				*v = true
			}
		}
	}
	memLimit := uint64(*memMB) << 20
	if meetAuditOn {
		if err := runMeetAudit(lv, levels[idx-1], memLimit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fxInstall()
	fxTraceInstall()
	fxReplayInstall()
	fxReverseHook()
	if gcLimitPct > 0 {
		debug.SetMemoryLimit(int64(memLimit / 100 * uint64(gcLimitPct)))
	}
	if gcPercent != 100 {
		debug.SetGCPercent(gcPercent)
	}
	stopTrace := func() {}
	if *memTrace != "" {
		stopTrace = memtrace.Start(*memTrace, 250*time.Millisecond)
	}
	if *stallScale && *timeLimit > 0 && *timeLimit < 600 {
		// exp-349: scale fixed wall-clock constants to the budget (600 s value unchanged).
		f := *timeLimit / 600
		StallScale = f
		reverse.CellsAfter = time.Duration(float64(reverse.CellsAfter) * f)
	}
	start := time.Now()
	deadline := start.Add(time.Duration(*timeLimit * float64(time.Second)))
	if *timeLimit <= 0 {
		deadline = start.Add(100 * 365 * 24 * time.Hour) // open-ended (exp-278)
	}
	startMemFuse(memLimit, uint64(MemFusePct), func() {
		outputOnce.Do(func() {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(Output{Level: levelName(path, idx), Reason: "memory (fuse)", TimeMs: math.Round(float64(time.Since(start).Microseconds())/100) / 10, PeakMemoryMB: peakMemoryMB()})
			os.Exit(1)
		})
	})
	if *timeLimit > 0 {
		// Last resort (exp-337): if the portfolio never returns (setup before
		// the lanes, a stuck GC or join), still print a result and exit.
		lvName := levelName(path, idx)
		go func() {
			time.Sleep(time.Until(deadline) + watchdogGrace)
			outputOnce.Do(func() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				enc.Encode(Output{Level: lvName, Reason: "timeout (watchdog)", TimeMs: math.Round(float64(time.Since(start).Microseconds())/100) / 10, PeakMemoryMB: peakMemoryMB()})
				os.Exit(1)
			})
		}()
	}
	if testHang == "pre" {
		select {} // exp-337 regression seam: the portfolio never returns
	}
	initSolOracle(lv)
	var res Result
	var strategy string
	var summaries map[string]*trace.Summary
	if startsSolved(lv) {
		// A circular level: a solution needs at least one push (circular.go).
		res, strategy = solveCircular(levels[idx-1], start, deadline)
	} else {
		res, strategy, summaries = portfolio(lv, levels[idx-1], deadline, memLimit, *only)
	}
	elapsed := time.Since(start)
	stopTrace()
	pprof.StopCPUProfile() // no-op unless profiling; os.Exit below skips defers
	if *memProfile != "" {
		if f, err := os.Create(*memProfile); err == nil {
			pprof.WriteHeapProfile(f)
			f.Close()
		}
	}

	reportSolOracle()
	out := Output{
		Level:        levelName(path, idx),
		Solved:       res.Solved,
		Reason:       res.Reason,
		TimeMs:       math.Round(float64(elapsed.Microseconds())/100) / 10,
		Nodes:        res.Nodes,
		PeakMemoryMB: max(peakMemoryMB(), circularPeakMB),
		Solution:     res.Solution,
	}
	out.Stats.Stats = res.Stats
	out.Strategy = strategy
	out.Stats.Portfolio = summaries
	if b, f := meet.Back.Load(), meet.Fwd.Load(); b != nil {
		out.Stats.MeetPosted = b.Posted.Load() + f.Posted.Load()
		if os.Getenv("SOKSOLV_DEBUG_MEET") != "" {
			fmt.Fprintf(os.Stderr, "perimeters: backward %d keys inserted, forward %d\n", b.Adds(), f.Adds())
		}
		out.Stats.MeetConfirmed = b.Confirmed.Load() + f.Confirmed.Load()
	}
	best := -1.0
	for name, sm := range summaries {
		if sm.Progress > best {
			best, out.Stats.Furthest = sm.Progress, name
		}
	}
	if res.Solved {
		out.Stats.Furthest = strategy
	}
	if secs := elapsed.Seconds(); secs > 0 {
		out.Stats.NodesPerSec = math.Round(float64(res.Nodes) / secs)
	}
	if res.Solved {
		if verify.CheckSolution(levels[idx-1], res.Solution) != nil {
			out.Solved = false
			out.Reason = "solution failed verification"
		}
		out.Moves = len(res.Solution)
		out.Pushes = strings.Count(res.Solution, "U") + strings.Count(res.Solution, "D") +
			strings.Count(res.Solution, "L") + strings.Count(res.Solution, "R")
	}
	outputOnce.Do(func() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	})
	if !out.Solved {
		os.Exit(1)
	}
}

// Shutdown bounds (exp-337): the portfolio stops waiting for lanes this long
// after the deadline, and the process prints and exits this long after it
// even if the portfolio itself never returns.
const (
	shutdownGrace = 250 * time.Millisecond
	watchdogGrace = 1000 * time.Millisecond
)

var outputOnce sync.Once

// testHang (SOKSOLV_TEST_HANG=lane|pre) makes a lane, or the whole portfolio,
// hang after finishing its work, to test the shutdown watchdogs.
var testHang = os.Getenv("SOKSOLV_TEST_HANG")

// portfolio runs three different searches concurrently, one core each,
// sharing the memory budget: FESS with macro moves (this package), the
// full-expansion FESS-lite search (package full) and a backward pull search
// (package reverse). The first solution wins and the others are stopped. The
// caller verifies the result.
// bridgeLoop is what a forward core does after its search stops early: fresh
// full searches, alternating the box-cost heuristic (new forward states for
// meetings, and a differently scored search; from the third run on,
// perturbed), until one is solved, time runs out or the portfolio stops.
func bridgeLoop(rows []string, deadline time.Time, memLimit uint64, slot full.Slot) Result {
	if full.Stop.Load() {
		return Result{Reason: "stopped"}
	}
	free := !full.FreeGoal
	for runs := 1; time.Until(deadline) > 500*time.Millisecond && !full.Stop.Load(); runs++ {
		runtime.GC()
		slot.Trace.SetExtra("runs", runs)
		fr, err := full.SolveWith(rows, deadline, memLimit, slot, free, (runs-1)/2)
		if err != nil {
			break
		}
		if fr.Solved {
			return Result{Solved: true, Solution: fr.Solution, Nodes: fr.Nodes}
		}
		slot.Tracked.Store(0)
		if fr.Reason != "memory" {
			break
		}
		free = !free
	}
	return Result{Reason: "restarts ended"}
}

var allFinish bool
var hfFestival bool
var maxProcs = 8    // exp-277: 8 threads, as Festival on the leaderboard machine
var retarget = true // exp-277: the toward lane switches to a newer pull-lane target after a doubling minimum slice
var retargetSlice = 500 * time.Millisecond
var noMeet bool

var revMatch bool     // -rev-match: the single/first reverse lane uses exact matching
var revSupply int     // -rev-supply W (exp-707)
var revSupplyInh bool // -rev-supply-inherit (exp-707)
var revSupplyNoMacro bool
var revSupplyRing bool
var towardOn = true  // exp-177: target-directed FESS strategy
var towardWait = 0.0 // exp-246 (1/3); exp-278: start at the pull lane's first target
var Budget3, Tracked3 atomic.Uint64

// Budget6, Tracked6: the novelty lane (exp-192).
var Budget6, Tracked6 atomic.Uint64

// Tracked11: the hub lane's second forward context (exp-451; its budget is Budget7).
var Tracked11 atomic.Uint64

// Budget7, Tracked7: the festb lane (exp-276).
var Budget7, Tracked7 atomic.Uint64

// festBLane (exp-276): Festival's strategy B as one more lane.
var festBLane = false
var Budget4, Tracked4 atomic.Uint64
var pullOn = true
var gcLimitPct = 80 // exp-246: Go soft memory limit, % of -mem (0: none)
var gcPercent = 100 // exp-246: GOGC

var revLanes int

func portfolio(lv *Level, rows []string, deadline time.Time, memLimit uint64, only string) (result Result, strategy string, summaries map[string]*trace.Summary) {
	recs := map[string]*trace.Recorder{"hub-pull": trace.New(), "hub-pull-fwd": trace.New(), "hub-gen": trace.New(), "hub-gen-fwd": trace.New(), "novelty": trace.New(), "hf": trace.New(), "pull": trace.New(), "toward": trace.New(), "macro": trace.New(), "full": trace.New(), "reverse": trace.New(), "bridge": trace.New(), "fa2": trace.New(), "factor": trace.New(), "dive": trace.New()}
	recs["hub"] = recs["hub-gen"] // -strategy hub reports the pull search under its own name
	fa2LanePlan.Store(nil)        // exp-594: the macro lane publishes its complete plan4 list
	if only == "all" || relayDumpDir != "" {
		relayReset(lv, rows) // exp-744 -revive-root (exp-767 -relay-dump: single lanes post too)
	} else {
		relayReset(nil, nil)
	}
	// Registered before other defers: publish detached summaries last, after
	// deferred learner and meeting diagnostics have been recorded.
	companions := map[string]bool{} // summaries a lane owns (laneTable); dropped if never used
	defer func() {
		for name, previous := range summaries {
			if previous != nil && previous.End == "stopped" {
				continue
			}
			if rec := recs[name]; rec != nil {
				snapshot := rec.Snapshot()
				summaries[name] = &snapshot
				if companions[name] && snapshot.End == "" && snapshot.Nodes == 0 && snapshot.Extra == nil {
					delete(summaries, name) // e.g. full's bridge when full ran to the end
				}
			}
		}
	}()
	// Bidirectional meeting: the backward search publishes its states in a
	// perimeter (1/32 of the memory limit, charged to reverse's budget) and
	// the forward searches probe it.
	if (only == "all" || only == "hf") && !noMeet {
		logSize := 10
		for uint64(8)<<(logSize+1) <= memLimit/32 {
			logSize++
		}
		owners := 6
		if diveGated && only == "all" || only == "dive" {
			owners = 7 // exp-691: the dive lane's mailbox (DiveOwner)
		}
		if MacroAltPlan && only == "all" {
			macroAltOwner, owners = owners, owners+1 // exp-769: the macro lane's alt engine
		}
		meet.Back.Store(meet.New(lv.W*lv.H, logSize, owners)) // one mailbox per reverse lane, the pull-FESS lane (3), the festb lanes (4, 5), dive (6)
		meet.Fwd.Store(meet.New(lv.W*lv.H, logSize, owners))  // full, macro, macro2/toward, novelty, toward-hf/festb, festx
	}
	if ReviveRoot && only == "all" {
		defer func() { // runs before the snapshots above; on a solve the winner's summary carries it
			name := "macro"
			if result.Solved && recs[strategy] != nil {
				name = strategy
			}
			recs[name].SetExtra("revive_root", relayReport())
		}()
	}
	full.Trace, reverse.Trace = recs["full"], recs["reverse"]
	if bk, fw := meet.Back.Load(), meet.Fwd.Load(); bk != nil && fw != nil && only == "all" {
		// Lane health (exp-250): meetings posted/confirmed/dropped per mailbox.
		defer func() {
			drops := func(t *meet.Table) []int64 {
				d := make([]int64, len(t.DroppedAt))
				for i := range d {
					d[i] = t.DroppedAt[i].Load()
				}
				return d
			}
			recs["reverse"].SetExtra("meet_back", map[string]any{"posted": bk.Posted.Load(), "confirmed": bk.Confirmed.Load(), "dropped": drops(bk)})
			recs["reverse"].SetExtra("meet_fwd", map[string]any{"posted": fw.Posted.Load(), "confirmed": fw.Confirmed.Load(), "dropped": drops(fw)})
		}()
	}
	// festbRun is the festb slot (exp-276): Festival's strategy B on one core,
	// or with useHub (exp-451) the hub lane on the same pull tree.
	var festbRun1 func(useHub bool, k int) Result
	festbRun := func(useHub bool) Result {
		if !HubIdleRing || !useHub {
			return reviveLoop(recs["hub-gen"], deadline, laneTable["hub-gen"].slots, func(k int) Result { return festbRun1(useHub, k) })
		}
		// exp-758 -hub-idle-ring: after a hub config ends idle, odd configs
		// run strategy B (the festb alternation), even ones hub mode.
		hubIdleSleepMs.Store(0)
		hubRingEnds.Store(0)
		idleEnds := 0
		var ring []any
		lane0 := time.Now()
		return reviveLoop(recs["hub-gen"], deadline, laneTable["hub-gen"].slots, func(k int) Result {
			useH := idleEnds == 0 || k%2 == 0
			t0 := time.Now()
			r := festbRun1(useH, k)
			if r.Reason == "hub idle" {
				idleEnds++
				hubRingEnds.Store(int64(idleEnds))
			}
			if len(ring) < 64 {
				ring = append(ring, []any{k, useH, r.Reason, time.Since(t0).Milliseconds()})
			}
			recs["hub-gen"].SetExtra("idle_ring", ring)
			recs["hub-gen"].SetExtra("idle_ring_ms", []int64{hubIdleSleepMs.Load(), time.Since(lane0).Milliseconds()}) // slept in idle rounds, lane time
			return r
		})
	}
	festbRun1 = func(useHub bool, k int) Result {
		// exp-276: Festival's strategy B on one core: a backward search in
		// Festival's order (-pull-fest), then the Festival-faithful toward
		// search aimed at its best state, alternating in doubling rounds
		// (exp-278). Its own backward mailbox (4) and forward mailbox (4,
		// the toward-hf lane's); meetings with every other lane's
		// perimeter count too.
		lv, rows := rootOf(relayPick("hub-gen", k), lv, rows) // exp-744
		ps := newPullSolver(lv, deadline)
		ps.laneID = 7
		ps.pfOn, ps.owner, ps.bestSlot = true, FestBLane, FestBLane
		ps.realRows = rows
		ps.budgetP, ps.trackedP = &Budget7, &Tracked7
		ps.tr = recs["hub-gen"]
		reviveDiversify(ps, k)
		if useHub {
			return relayFix(hubLane(ps, rows, lv, deadline, memLimit, recs["hub-gen-fwd"], &Tracked8, FestBLane))
		}
		return relayFix(festbAlternate(ps, rows, lv, deadline, memLimit, recs["hub-gen-fwd"], &Tracked8, FestBLane))
	}
	// diveRun runs the dive lane's ring (exp-691); with a baton it shares
	// another lane's core (exp-693 -dive-share) and half that lane's budget.
	diveRun1 := func(bat *baton, me int, aOn bool, div uint64) Result {
		return reviveLoop(recs["dive"], deadline, laneTable["dive"].slots, func(k int) Result {
			lv, rows := rootOf(relayPick("dive", k), lv, rows) // exp-744
			s := newPullSolver(lv, deadline)
			s.laneID = 13
			s.dive, s.owner, s.bestSlot = true, DiveOwner, DiveOwner
			s.realRows = rows
			s.budgetP, s.trackedP = &BudgetDive, &TrackedDive
			if aOn {
				s.diveAOn, s.trackedP = true, &TrackedDiveA
			}
			if bat != nil {
				s.bat, s.batMe, s.budgetDiv = bat, me, div
				if NodeBudget > 0 && div > 1 {
					s.nodeLimit = max(NodeBudget/int64(div), 1)
				}
			}
			s.tr = recs["dive"]
			return relayFix(s.solve(deadline, memLimit))
		})
	}
	// diveRun runs the dive lane's ring (exp-691); with a baton it shares
	// another lane's core (exp-693 -dive-share) and half that lane's budget.
	diveRun := func(bat *baton, me int) Result {
		if bat != nil {
			return reviveLoop(recs["dive"], deadline, laneTable["dive"].slots, func(k int) Result {
				lv, rows := rootOf(relayPick("dive", k), lv, rows) // exp-744
				s := newPullSolver(lv, deadline)
				s.laneID = 13
				s.dive, s.owner, s.bestSlot = true, DiveOwner, DiveOwner
				s.realRows = rows
				s.budgetP, s.trackedP, s.bat, s.batMe, s.budgetDiv = &Budget4, &TrackedDive, bat, me, 2
				s.tr = recs["dive"]
				return relayFix(s.solve(deadline, memLimit))
			})
		}
		if !DivePair {
			return diveRun1(nil, 0, false, 1)
		}
		// exp-693 -dive-pair: base and A dives alternate on one baton.
		pb := newBaton([3]bool{true, true, false})
		pb.ch[0] <- struct{}{}
		var ra Result
		da := make(chan struct{})
		go func() {
			defer close(da)
			pb.wait(1)
			ra = diveRun1(pb, 1, true, 2)
			pb.done(1, ra.Solved)
		}()
		pb.wait(0)
		r := diveRun1(pb, 0, false, 2)
		pb.done(0, r.Solved)
		<-da
		recs["dive"].SetExtra("dive_pair_a", ra.Reason)
		if !r.Solved && ra.Solved {
			recs["dive"].SetExtra("dive_pair_solved", "a")
			return ra
		}
		return r
	}
	strategies := map[string]func() Result{
		"fa2": func() Result {
			// exp-491: plan-guided forward FESS engine (research/exp-491/DESIGN.md)
			s := newSolverUntil(lv, deadline)
			s.laneID = 12
			s.rows = rows
			s.budgetP, s.trackedP = &Budget12, &Tracked12
			s.tr = recs["fa2"]
			if FA2LaneAny && (only == "all" || FA2LaneSolo) {
				return fa2LaneRun(s, deadline, memLimit, recs["fa2"]) // exp-594 (exp-618: -fa2-lane-solo)
			}
			return fa2Solve(s, deadline, memLimit)
		},
		"macro": func() Result {
			// A ring of plans (exp-563): config k+1 restarts on the
			// anytime plan4 that config k completed.
			var s *solver
			// exp-744 -revive-root: macro also revives on memory death; odd
			// revives start from a posted board. A replan keeps the root.
			var root *rootBoard
			revs, revived := 0, false
			alt := macroAltStart(lv, rows, deadline, memLimit) // exp-769 (nil: off)
			res := ringRun(deadline, laneTable["macro"].slots, func(k int) Result {
				prev := s
				if revived {
					root, revived = relayPick("macro", revs), false
					prev = nil
				}
				lvk, rowsk := rootOf(root, lv, rows)
				s = newSolverUntil(lvk, deadline)
				s.laneID, s.tr, s.rows = 1, recs["macro"], rowsk
				alt.attach(s, 0)
				if prev != nil {
					s.prePlan, s.prePlanAt = prev.prePlan, prev.prePlanAt
				}
				if MacroRevive && !ReviveRoot {
					reviveDiversify(s, revs) // exp-767: a revived macro tree is a new search (posts restart at 100k)
				}
				r := s.solve(deadline, memLimit)
				if ReviveRoot && !r.Solved {
					s.relayFlush()
				}
				return relayFix(r)
			}, func(r Result) restart {
				if r.Reason == "replan" && time.Now().Before(deadline) {
					return replan
				}
				if (ReviveRoot || MacroRevive) && reviveRule(r, deadline) == revive {
					return revive
				}
				return stop
			}, func(int) {
				revs, revived = revs+1, true
				if MacroRevive {
					macroReviveNote(recs["macro"], revs)
				}
			})
			res = alt.finish(res, recs["macro"])
			fxReport("macro", s, res)
			return res
		},
		"dive": func() Result {
			// exp-691: landmark-phased greedy backward search (dive.go).
			return diveRun(nil, 0)
		},
		"pull": func() Result {
			// exp-179: the FESS engine run backward (pull moves) as a
			// backward lane: it fills the backward perimeter, completes
			// forward searchers' meetings and publishes a forward target.
			shared := DiveShare == "pull" && diveGated && only == "all"
			var bat *baton
			pullRun := func() Result {
				return reviveLoop(recs["pull"], deadline, laneTable["pull"].slots, func(k int) Result {
					lv, rows := rootOf(relayPick("pull", k), lv, rows) // exp-744
					s := newPullSolver(lv, deadline)
					s.laneID = 6
					s.realRows = rows
					s.budgetP, s.trackedP = &Budget4, &Tracked4
					if shared {
						s.bat, s.batMe, s.budgetDiv = bat, 0, 2
					}
					s.tr = recs["pull"]
					reviveDiversify(s, k)
					if relayBackPull {
						s.rback = newRelayBack(s, lv) // exp-761 -relay-back -relay-back-pull
					}
					res := s.solve(deadline, memLimit)
					fxReport("pull", s, res)
					return relayFix(res)
				})
			}
			if !shared {
				return pullRun()
			}
			// exp-693 -dive-share pull: the dive runs on this lane's core,
			// alternating baton slices with the pull search.
			bat = newBaton([3]bool{true, true, false})
			bat.ch[0] <- struct{}{}
			var dres Result
			dd := make(chan struct{})
			go func() {
				defer close(dd)
				bat.wait(1)
				dres = diveRun(bat, 1)
				bat.done(1, dres.Solved)
			}()
			bat.wait(0)
			r := pullRun()
			bat.done(0, r.Solved)
			<-dd
			recs["pull"].SetExtra("dive_shared", dres.Reason)
			if !r.Solved && dres.Solved {
				recs["pull"].SetExtra("dive_shared_solved", 1)
				return dres
			}
			return r
		},
		"toward": func() Result {
			// exp-177: after a third of the time (Festival's split), FESS
			// toward the backward lane's best connected state, meeting the
			// live perimeter on the way; retarget when it improves.
			start := time.Now()
			wait := time.Duration(float64(time.Until(deadline)) * towardWait)
			for time.Since(start) < wait && !full.Stop.Load() {
				time.Sleep(20 * time.Millisecond)
			}
			var last *meet.Target
			var prevTrees []*solver
			var activeToward *solver
			runs, fresh := 0, 0 // exp-744: fresh toward trees (odd ones start from a posted board)
			var cumRegenAdded, cumTotalAdded, cumRegenExpanded, cumTotalExpanded int
			var cumSetupMs float64
			for time.Until(deadline) > 300*time.Millisecond && !full.Stop.Load() {
				lane := 0
				if pullOn {
					lane = PullLane // the pull-FESS lane's best: chosen by FESS features
				}
				tgt := meet.Best[lane].Load()
				if tgt == nil || tgt == last {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				if RelayWalls && len(tgt.Boxes) != len(lv.Boxes) {
					last = tgt // exp-744: a residual pull lane's target (fewer boxes): not for this lane
					continue
				}
				last = tgt
				runs++
				recs["toward"].SetExtra("runs", runs)
				if retarget {
					// exp-277: after a doubling minimum slice, a better backward
					// target from the same lane replaces this search.
					started, minSlice, cur := time.Now(), doubling(retargetSlice, runs-1, 8), tgt
					towardStop = func() bool {
						return time.Since(started) >= minSlice && meet.Best[lane].Load() != cur
					}
				}
				var sol string
				var r Result
				var towardSolver *solver
				if RetargetReuse && TowardHF && activeToward != nil && activeToward.n == len(tgt.Boxes) && !activeToward.memFull && len(activeToward.player) > 0 && len(activeToward.player) <= 16384 {
					towardSolver = activeToward
					t0 := time.Now()
					hitSol, ok := activeToward.retargetTo(tgt)
					setupMs := float64(time.Since(t0).Microseconds()) / 1000.0
					cumSetupMs += setupMs
					activeToward.stats.RetargetSetupMs = cumSetupMs
					activeToward.stats.RetargetRuns = runs
					activeToward.stats.RetargetActiveNodes = len(activeToward.player)
					activeToward.stats.RetargetPrevNodes = len(activeToward.player)
					activeToward.stopCheck = towardStop
					if ok {
						sol = hitSol
						r = Result{Solved: true, Solution: hitSol}
					} else {
						sol, r = activeToward.towardRun(deadline, memLimit)
					}
				} else {
					if RetainFwdMail && activeToward != nil {
						prevTrees = appendPrevTree(prevTrees, activeToward)
						activeToward = nil
					}
					if TowardHF {
						lvk, rowsk := rootOf(relayPick("toward", fresh), lv, rows) // exp-744
						if len(lvk.Boxes) != len(tgt.Boxes) {
							lvk, rowsk = lv, rows // the residual's boxes differ from the target's
						}
						fresh++
						sol, r, towardSolver = fessTowardRealWithPrev(rowsk, lvk, tgt, deadline, memLimit, recs["toward"], &Budget3, &Tracked3, 2, 0, prevTrees)
						if towardSolver != nil {
							towardSolver.laneID = 3
						}
						if RetargetReuse {
							activeToward = towardSolver
						}
					} else {
						sol, r = fessToward(rows, lv, tgt, deadline, memLimit, recs["toward"])
					}
				}
				towardStop = nil
				if towardSolver != nil {
					cumRegenAdded += towardSolver.stats.RetargetRegenAdded
					cumTotalAdded += towardSolver.stats.RetargetTotalAdded
					cumRegenExpanded += towardSolver.stats.RetargetRegenExpanded
					cumTotalExpanded += towardSolver.stats.RetargetTotalExpanded
					if !RetargetReuse && runs > 1 {
						cumSetupMs += towardSolver.stats.RetargetSetupMs
					}
					prevNodes := 0
					for _, p := range prevTrees {
						prevNodes += len(p.player)
					}
					recs["toward"].SetExtra("retarget_runs", runs)
					recs["toward"].SetExtra("retarget_regen_added", cumRegenAdded)
					recs["toward"].SetExtra("retarget_total_added", cumTotalAdded)
					recs["toward"].SetExtra("retarget_regen_expanded", cumRegenExpanded)
					recs["toward"].SetExtra("retarget_total_expanded", cumTotalExpanded)
					recs["toward"].SetExtra("retarget_prev_nodes", prevNodes)
					recs["toward"].SetExtra("retarget_active_nodes", len(towardSolver.player))
					recs["toward"].SetExtra("retarget_setup_ms", cumSetupMs)
				}
				if sol != "" {
					if fx := relayFix(Result{Solved: true, Solution: sol, Nodes: r.Nodes}); fx.Solved {
						return fx
					}
				}
				if !RetargetReuse && RetainFwdMail && towardSolver != nil {
					prevTrees = appendPrevTree(prevTrees, towardSolver)
				}
			}
			return Result{Reason: "toward ended"}
		},
		"hf": func() Result {
			// exp-188: Festival's strategy B shape on one core, deterministic
			// under -nodes: pull FESS for a third of the work (backward
			// perimeter, best state by connectivity/rooms/hotspots/spread),
			// then FESS toward that state (connectivity first, no packing
			// plan) for the rest; meetings completed by the pull solver.
			total := NodeBudget
			pullNodes := HFPullNodes
			if pullNodes <= 0 && total > 1 {
				pullNodes = max(1, total/3)
			}
			if pullNodes <= 0 || total > 0 && pullNodes >= total {
				return Result{Reason: "hf needs a positive pull and forward budget"}
			}
			ps := newPullSolver(lv, deadline)
			ps.nodeLimit = pullNodes
			ps.realRows = rows
			ps.tr = recs["pull"]
			if r := ps.solve(deadline, memLimit); r.Solved {
				return r
			}
			tgt := ps.bestTarget()
			if tgt == nil {
				return Result{Reason: "no backward state"}
			}
			forwardNodes := int64(0)
			if total > 0 {
				forwardNodes = total - pullNodes
			}
			Budget3.Store(Budget4.Load()) // the toward phase's slot: the same memory
			onStart := 0
			for _, b := range tgt.Boxes {
				if slices.Contains(lv.Boxes, b) {
					onStart++
				}
			}
			recs["hf"].SetExtra("target_on_start", onStart)
			recs["hf"].SetExtra("target_tail", len(tgt.Tail))
			defer func() {
				if bk := meet.Back.Load(); bk != nil {
					recs["hf"].SetExtra("meet_posted", bk.Posted.Load())
					recs["hf"].SetExtra("meet_confirmed", bk.Confirmed.Load())
					recs["hf"].SetExtra("meet_dropped", bk.Dropped.Load())
				}
			}()
			// The pull solver's search is over, but meetings posted to it
			// must still be drained while the toward phase runs (its mailbox
			// holds 64; exp-244).
			var joined string
			drainDone, drained := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(drained)
				for {
					select {
					case <-drainDone:
						return
					case <-time.After(5 * time.Millisecond):
					}
					if sol, ok := ps.pullCheckMail(); ok && verify.Check(rows, sol) == nil {
						joined = sol
						Stop.Store(true)
						return
					}
				}
			}()
			sol, r := fessTowardReal(rows, lv, tgt, deadline, memLimit, recs["toward"], &Budget3, &Tracked3, 2, forwardNodes)
			close(drainDone)
			<-drained
			if sol != "" {
				return Result{Solved: true, Solution: sol, Nodes: r.Nodes}
			}
			if joined != "" {
				return Result{Solved: true, Solution: joined, Nodes: r.Nodes}
			}
			if sol, ok := ps.pullCheckMail(); ok && verify.Check(rows, sol) == nil {
				return Result{Solved: true, Solution: sol, Nodes: r.Nodes}
			}
			return Result{Reason: "hf ended", Nodes: r.Nodes}
		},
		"pathdiag":   func() Result { return pathdiagRun(rows, lv, deadline, memLimit) },
		"hub-gen":    func() Result { return festbRun(hubOn) },
		"hub":        func() Result { return festbRun(true) },
		"tfwd":       func() Result { return hubProbe(rows, lv, deadline, memLimit) },
		"hub-strata": func() Result { return strataDumpRun(rows, lv, deadline, memLimit) },
		"hub-pull": func() Result {
			// exp-283: another Festival-style backward-then-forward lane
			// (-festx D|E|F|B: Festival's strategy of that letter).
			return reviveLoop(recs["hub-pull"], deadline, laneTable["hub-pull"].slots, func(k int) Result {
				lv, rows := rootOf(relayPick("hub-pull", k), lv, rows) // exp-744
				ps := newPullSolver(lv, deadline)
				ps.laneID = 9
				ps.pfOn, ps.owner, ps.bestSlot = true, FestXLane, FestXLane
				ps.pfSty = festXStyle()
				ps.rng = reviveSeed(0)
				ps.realRows = rows
				ps.budgetP, ps.trackedP = &Budget9, &Tracked9
				ps.tr = recs["hub-pull"]
				reviveDiversify(ps, k)
				if HubRace {
					return relayFix(raceAlternate(ps, rows, lv, deadline, memLimit, recs["hub-pull-fwd"], &Tracked10, FestXLane))
				}
				return relayFix(festbAlternate(ps, rows, lv, deadline, memLimit, recs["hub-pull-fwd"], &Tracked10, FestXLane))
			})
		},
		"novelty": func() Result {
			// exp-192: forward FESS whose novel moves (a box onto a square not
			// reached before in the node's cell: best-first width search's
			// novelty) cost nothing, on its own core and mailbox; the macro
			// lane stays plain FESS.
			return reviveLoop(recs["novelty"], deadline, laneTable["novelty"].slots, func(k int) Result {
				lv, rows := rootOf(relayPick("novelty", k), lv, rows) // exp-744
				s := newSolverUntil(lv, deadline)
				s.laneID = 5
				s.tr = recs["novelty"]
				s.rows = rows
				s.novelty = 100
				s.budgetP, s.trackedP, s.owner = &Budget6, &Tracked6, 3
				reviveDiversify(s, k)
				res := s.solve(deadline, memLimit)
				if ReviveRoot && !res.Solved {
					s.relayFlush()
				}
				fxReport("novelty", s, res)
				return relayFix(res)
			})
		},
		"full": func() Result {
			fr, err := full.Solve(rows, deadline, memLimit)
			if err != nil {
				return Result{Reason: err.Error()}
			}
			res := Result{Solved: fr.Solved, Reason: fr.Reason, Solution: fr.Solution, Nodes: fr.Nodes}
			if res.Solved || only != "all" || meet.Back.Load() == nil {
				return res
			}
			// The forward search stopped early (usually on memory): use the
			// core for bridges toward the backward lanes' best states.
			if br := bridgeLoop(rows, deadline, memLimit, full.Slot{Budget: &full.Budget, Tracked: &full.Tracked, Trace: recs["bridge"]}); br.Solved {
				return br
			}
			return res
		},
		"reverse": func() Result {
			// A ring of phases: when a phase fills its memory budget with
			// time left, the next (perturbed) phase starts rather than
			// leaving the core idle.
			runLane := func(ln reverse.Lane) Result {
				nodes := 0
				return ringRun(deadline, nil, func(phase int) Result {
					pl := ln
					if revSupplyRing && phase%2 == 0 {
						pl.Supply = 0 // exp-707: supply-aware phases alternate with plain ones, phase 0 plain
					}
					prow := rows
					if relayEndLv == lv && relayEnd >= 0 {
						pl.Root = int32(relayEnd + 1) // exp-761 back-trial child: only the suffix's player region is a root
					}
					if b := relayPick("reverse", phase); rootRowsW(b) != nil {
						prow = rootRowsW(b) // exp-744 -revive-root-walls: the phase runs on the board's residual
					} else if b != nil {
						pl.RootBoxes, pl.RootPlayer = b.boxes, b.player // exp-744: this phase pulls toward a posted board
					}
					rr, err := reverse.SolveLane(prow, deadline, memLimit, phase, pl)
					if err != nil {
						return Result{Reason: err.Error()}
					}
					nodes += rr.Nodes
					ln.Trace.SetExtra("phases", phase+1)
					return relayFix(Result{Solved: rr.Solved, Reason: rr.Reason, Solution: rr.Solution, Nodes: nodes})
				}, func(r Result) restart {
					if (r.Reason == "memory" || r.Reason == "reroot-invalid") && time.Until(deadline) > time.Second && !reverse.Stop.Load() {
						return revive
					}
					return stop
				}, nil)
			}
			// Phase 0 runs alone with the whole budget (free-start-square
			// heuristic). If it fills memory, two lanes time-share the core:
			// the free heuristic's perturbed restarts and, as a differently
			// scored search, the nearest-start-square heuristic.
			if revLanes < 2 {
				return runLane(reverse.Lane{Free: reverse.FreeStart, Match: revMatch, Cells: reverse.RevCells, Trace: recs["reverse"], Supply: int32(revSupply), SupplyInh: revSupplyInh, NoMacros: revSupplyNoMacro})
			}
			// The portfolio's reverse lane (exp-625 retired lane b, exp-640
			// deleted it): cell scheduling, novelty, types and walks, with
			// a live tracker; each phase restarts with perturbation when it
			// fills its share of memory.
			bt := reverse.NewLive(1)
			bt.Join(0)
			r := runLane(reverse.Lane{Index: 0, Free: reverse.FreeStart, Cells: reverse.RevCells, Novelty: true, Types: true, Walks: true, Live: bt, Trace: recs["reverse"], Connected: towardOn, Supply: int32(revSupply), SupplyInh: revSupplyInh, NoMacros: revSupplyNoMacro})
			bt.Done(0)
			if r.Solved {
				reverse.Stop.Store(true)
			}
			return r
		},
	}
	// Each strategy gets its own memory budget over its own data structures:
	// 70% of the limit in total (the rest is headroom for GC and growth),
	// split by share and redistributed when a strategy finishes.
	strategies["factor"] = func() Result { // exp-645
		t0 := time.Now()
		r := factorRun(lv, deadline, memLimit, only != "all")
		recs["factor"].SetExtra("reason", r.Reason) // exp-645b: lane diagnostics (retirement)
		recs["factor"].SetExtra("ended_ms", time.Since(t0).Milliseconds())
		recs["factor"].SetExtra("nodes", r.Nodes)
		return r
	}
	shares := laneShares(only) // lane.go: the lane table
	if RelayTrial && only == "all" {
		relayDiag.Store(nil)
		defer func() { // runs before the summary snapshots: a winner other than relay carries the trials
			if p := relayDiag.Load(); p != nil && result.Solved && strategy != "relay" && recs[strategy] != nil {
				recs[strategy].SetExtra("relay", *p)
			}
		}()
	}
	if HubIdleRing && only == "all" {
		defer func() { // exp-758: a winner other than hub-gen carries the ring's state (a stopped lane's summary is bare)
			if result.Solved && strategy != "hub-gen" && recs[strategy] != nil {
				recs[strategy].SetExtra("hub_ring", []int64{hubRingEnds.Load(), hubIdleSleepMs.Load()})
			}
		}()
	}
	if RelayTrial && only == "all" { // exp-747: weight 0, no memory share, no thread (laneShares does not list it)
		shares["relay"] = 0
		recs["relay"] = trace.New()
		strategies["relay"] = func() Result { return relayRun(rows, deadline, memLimit, recs["relay"]) }
	}
	if _, ok := strategies[only]; only != "all" && !ok {
		return Result{Reason: "unknown strategy " + only}, only, nil
	}
	var mu sync.Mutex // guards shares and scale
	scale := 1.0
	memLimitGlobal.Store(memLimit)
	resetElasticPool(memLimit)
	setBudgetsLocked := func() {
		var sum uint64
		for _, v := range shares {
			sum += v
		}
		for name, v := range shares {
			laneTable[name].budget.Store(uint64(float64(memLimit/100*70/sum*v) * scale))
		}
	}
	mu.Lock()
	setBudgetsLocked()
	mu.Unlock()
	// Memory governor: untracked overhead (allocation transients, freed
	// phases, GC lag) varies by level. Measure mapped memory against the
	// strategies' tracked bytes and shrink every budget so that tracked x
	// overhead stays under 85% of the limit: strategies then stop or
	// restart on their own budgets instead of the process being killed.
	govDone := make(chan struct{})
	defer close(govDone)
	govStep := func() {
		{
			mu.Lock()
			if memtrace.Enabled {
				memtrace.Publish("meet", 0, map[string]float64{"back": float64(meet.Back.Load().Bytes()) / (1 << 20), "fwd": float64(meet.Fwd.Load().Bytes()) / (1 << 20)}, nil)
			}
			var sumT uint64
			for name := range shares {
				for _, t := range laneTable[name].slots {
					sumT += t.Load()
				}
			}
			if sumT >= 64<<20 {
				ratio := float64(mappedBytes()) / float64(sumT)
				scale = min(1, 0.85/0.70/ratio)
				setBudgetsLocked()
			}
			mu.Unlock()
		}
	}
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-govDone:
				return
			case <-tick.C:
			}
			govStep()
		}
	}()
	ch := make(chan res, len(shares))
	portStart := time.Now()
	for _, name := range slices.Sorted(maps.Keys(shares)) {
		run := strategies[name]
		go func() {
			r := run()
			if r.Solved && !relayEndOK(r.Solution) {
				r = Result{Reason: "relay-end: wrong player region", Nodes: r.Nodes, Stats: r.Stats} // exp-761 back-trial child: this lane is out, the others go on
			}
			if testHang == name {
				select {} // exp-337 regression seam: a lane that never finishes
			}
			ch <- res{r, name}
		}()
	}
	// If nothing solves the level, report the main (full) strategy's reason
	// (or the only strategy's): guests stopping early say little about the level.
	summaries = map[string]*trace.Summary{}
	for name := range shares {
		summaries[name] = &trace.Summary{}
		for _, c := range laneTable[name].owns {
			summaries[c], companions[c] = &trace.Summary{}, true
		}
	}
	var main res
	var winner *res
	infeasible := map[string]bool{} // exp-761
	// Shutdown watchdog (exp-337): lanes poll the deadline cooperatively, so
	// one stuck in an unpolled phase would hold back the report. Wait a short
	// grace past the deadline, then report with whatever has finished.
	giveUp := time.NewTimer(time.Until(deadline) + shutdownGrace)
	defer giveUp.Stop()
	for range len(shares) {
		var r res
		select {
		case r = <-ch:
		case <-giveUp.C:
			Stop.Store(true)
			full.Stop.Store(true)
			reverse.Stop.Store(true)
			mu.Lock()
			late := make([]string, 0, len(shares))
			for name := range shares {
				late = append(late, name)
			}
			slices.Sort(late)
			markStopped(summaries, shares)
			fmt.Fprintf(os.Stderr, "shutdown: lanes still running %.2fs past the deadline: %v\n", shutdownGrace.Seconds(), late)
			mu.Unlock()
			if main.r.Reason == "" {
				main.r.Reason = "timeout"
			}
			return main.r, main.name, summaries
		}
		if r.r.Solved {
			recs[r.name].SetExtra("lane_solved_ms", time.Since(portStart).Milliseconds()) // lane matrix (-all-finish)
		}
		if relayEndLv != nil && r.r.Reason == "exhausted" && (r.name == "pull" || r.name == "reverse") {
			// exp-761 back-trial child: both backward searches, rooted only in the
			// suffix's player region, ran dry: no start -> (B, region) exists.
			infeasible[r.name] = true
			if infeasible["pull"] && infeasible["reverse"] {
				Stop.Store(true)
				full.Stop.Store(true)
				reverse.Stop.Store(true)
				mu.Lock()
				markStopped(summaries, shares)
				mu.Unlock()
				return Result{Reason: "infeasible"}, r.name, summaries
			}
		}
		if r.name == "full" || only != "all" {
			main = r
		}
		mu.Lock()
		delete(shares, r.name) // its memory goes to the strategies still running
		if len(shares) > 0 {
			setBudgetsLocked()
		}
		mu.Unlock()
		if r.r.Solved && winner == nil {
			winner = &r
			if !allFinish {
				Stop.Store(true)
				full.Stop.Store(true)
				reverse.Stop.Store(true)
				// Don't wait for the others: their recorders are still being
				// written, so they get a bare "stopped" summary.
				mu.Lock()
				markStopped(summaries, shares)
				mu.Unlock()
				break
			}
		}
	}
	if winner != nil {
		return winner.r, winner.name, summaries
	}
	return main.r, main.name, summaries
}

// markStopped stamps a bare "stopped" summary on each lane still running (the
// keys of running) and on the companion summaries it owns. Lanes that already
// finished keep their own summary (exp-468: the shutdown watchdog used to
// overwrite finished bridge/festb-fwd/festx-fwd lanes with "stopped, 0 ms").
func markStopped(summaries map[string]*trace.Summary, running map[string]uint64) {
	for name := range running {
		summaries[name] = &trace.Summary{End: "stopped"}
		for _, c := range laneTable[name].owns {
			if _, ok := summaries[c]; ok {
				summaries[c] = &trace.Summary{End: "stopped"}
			}
		}
	}
}
