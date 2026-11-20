# Macro lane (Go `-strategy macro`) — scope analysis and port plan

Status: **not ported yet.** This note records what the lane actually is, measured, so the port can be staged.

## What "macro" is at default settings

`macro` = FESS over box macro states with advisors (Festival's search), *plus* everything promoted to default over
the Go project's experiments. Running `soksolv -strategy macro -procs 1` executes (coverage-instrumented Go build,
6 testdata levels, 20k nodes each — `bench/macro-scope.sh`): **≈7,900 Go statement lines in ~35 files**
(not counting `main.go`/`lane.go`/trace plumbing, ≈ 7,000 lines of solver logic):

| lines run | file | role |
|---|---|---|
| 861 / 1586 | solver.go | node store, add/apply, assignment matching, freeze/2x2, PI-corral, pair deadlocks, box paths |
| 808 / 1019 | deadzone/deadzone.go | corral deadlock proofs (`-corral-dead 200`, default on) |
| 806 / 1143 | packing_plan4.go | plan4 planner (`-plan4-macro`, default on) incl. anytime restart |
| 470 / 641 | rooms.go (+rooms_fx, fa2rooms) | room graph, room advisor, connectivity features |
| 468 / 800 | fess_moves.go | open(): move generation, scoring windows (`-move-window 8`), lazy children |
| 407 / 433 | displacement.go | displacement landmarks (default on) |
| 346 / 606 | fess_search.go | frontier cells, heap, loop, reopen windows |
| 271 / 659 | packing_plan2.go, 92 / 419 packing_plan.go | sink planner / packing plan (always built) |
| 265 / 274 | cutscan.go | cut scan obligations |
| 250 / 311 | cluster/cluster.go | **already ported** (Search/ClusterChecker) |
| 223 / 237 | hotspots.go | hotspot features |
| 164 / 265 | advisors.go | the 7 advisors |
| 145 / 169 | basin.go; 145 / 151 goalarea.go (**ported**); 112 corridor_flow.go; 107 boxflood.go; 126 packing_causal.go; 106 packing_route.go; 99 pairbits.go; 90 moveaccess.go; 79 corral_frontier.go; 72 packing_fest.go; 59 packing_obligations.go; 73 sidedead (**ported**) | smaller pieces |

Even with every experiment flag that can be disabled turned off
(`-plan4-macro=false -corral-frontier=false -packing-obligations=false -lazy-child=false -access-novelty=false
-rev-groups=false -novelty2=false -displacement-landmarks=false -corridor-flow=false -corral-dead 0 -move-window 0
-pair-side=false ...`) the lane still builds the sink packing plan (`buildPackingPlan`), room/advisor scoring and
corral machinery, so there is no small "macro-lite" whose node counts could be reproduced exactly without porting
roughly half of the above (~4k lines).

## Why this is a separate project from `full`/`reverse`

* The search order depends on `moveScore.full()` comparisons across ~8 scoring terms, versioned heap entries,
  reopen windows and `propagateDead`; node-count parity (our validation method) only holds if all are exact.
* Every default-on sub-feature changes expansion order, so they must land in the order below, each validated
  against Go with the corresponding flag on/off.

## Proposed port order (each step validated with `bench/compare.py`, `GO_FLAGS` = Go flags that turn the *later* steps off)

1. Base FESS skeleton: solver.go node store/add/apply, fess_search frontier + loop, fess_moves open() with
   window = all moves, advisors 1–3 (goal macro, connectivity, room), default `packed`, no plan (`-plan4-macro=false`
   and a stub plan), no corral/displacement/obligations. (Go needs a no-plan switch: add via the existing `s.noPlan` path.)
2. Sink packing plan (`packing_plan.go`, `packing_route.go`, `packing_causal.go`).
3. Rooms (`rooms.go`) + hotspots + remaining advisors.
4. PI-corral frontier (`corral_frontier.go`) + deadzone checker (shared with other lanes: pull, novelty, fa2).
5. Move windows / lazy children / reopen; displacement landmarks; corridor flow; cut scan; obligations.
6. plan4 (+ anytime restart ring).
Components reusable from the existing port: `ClusterChecker`, `SideDead`, `GoalArea`, `StateTable`/`RecordArena`/`KeyHeap`,
FullSolver's flood/normalize/feasible/PI-corral/pair-dead code (to be factored into shared internal helpers in step 1).

Estimate: ~7k lines of C#, comparable to everything ported so far combined; it should be a dedicated effort.


## Attempt 2 (steps 1–2): findings — the proposed step order does not work

A second pass tried to port steps 1–2 (FESS skeleton + sink packing plan) against a Go copy with every switchable
experiment flag off (`-plan4-macro=false -corral-frontier=false -packing-obligations=false -lazy-child=false
-access-novelty=false -rev-groups=false -novelty2=false -displacement-landmarks=false -corridor-flow=false
-corral-dead 0 -move-window 0 -pair-side=false`; `novelty` is already 0 by default). Coverage of that build
(20k nodes × 18 testdata levels) still executes, outside already-ported code:

| executed lines | file | why it can't be skipped |
|---|---|---|
| 766 | solver.go | boxSearch (macro move enumeration), apply/add, pushDead pipeline, packedWith, reconstruct |
| 390 | rooms.go | **room map: the packing plan's sink (`computeBasin`→`rooms.rooms[basin.sink]`), `roomConn*` score term, room advisor** |
| 354 | packing_plan.go | backward pull planner (weights, heap) used for `packedChild`/`childStatus`/`placedRow` |
| 307 / 259 | fess_search.go / fess_moves.go | frontier cells, heap entries, windows (`enqueue`/`reopen`), `open()` scoring and sort |
| 263 | cutscan.go | executed by the room-connectivity bookkeeping |
| 223 | hotspots.go | hotspot counts feed the hot score term and the opener advisor |
| 128 | advisors.go | 7 advisors: packing, connectivity, room, hotspots, explorer, opener, out-of-plan |
| 115 / 107 / 97 / 90 | basin.go / boxflood.go / pairbits.go / moveaccess.go | out-of-plan basin; bitboard flood (result-equal to the plain flood); bit pair-dead (equal to pairDead); mobility/explorer terms |

Consequences:
* **Rooms must come before the plan** (the plan's sink basin is chosen from the room graph) — the previous order had
  rooms in step 3. Basin/OOP, hotspots (opener advisor) and move-access (explorer advisor, mobility tie-break) are
  also unconditional parts of `open()`'s ranking, so there is no smaller exactly-reproducible configuration:
  the minimum parity target is ≈ 3.5k Go lines (rooms 390, cutscan 263, hotspots 223, basin 115, plan 354,
  solver core 766, FESS loop/open 560, advisors 128, access 90 + small helpers), i.e. a dedicated multi-day port.
* Zeroing rooms/hotspots/access in a patched Go copy (room/hot terms → 0, `computeRooms` → nil ⇒ basin nil ⇒ OOP 0)
  would give an exactly reproducible *reduced* lane for staged validation, but it is a different (weaker) search
  than the real macro lane, so the staged port must still end up porting everything above.
* Recommended order: (1) core push/macro-move machinery + `articulation` (done, see below) + FESS frontier with
  the 4 cell features and scores (oop/packed/conn) against Go with room/hot/access zeroed; (2) rooms+cutscan+basin
  (adds `room` term + room advisor + OOP); (3) sink plan; (4) hotspots; (5) move-access; (6) the flag features.
  Validate each stage with a Go copy where only the *later* stages' score terms are zeroed.

### Spin-off delivered from this attempt
`articulation` (Go artic.go) is ported into `FullSolver` as incremental child region counts (one iterative Tarjan
pass per box from its second candidate on, O(1) per candidate): bit-exact with the flood count
(`FullSolver.CheckRegions` test hook, `IncrementalChildRegionCount_AgreesWithFlood`), parity-mode node counts
unchanged (Holland_81_57 152817, SokHard_163_36 362653), fast mode ≈ 7% faster
(Holland_81_57 3.73 → 3.41 s, SokHard_163_36 5.77 → 5.37 s, JIT build). The same code is what `open()` of the
macro lane uses (`regionsChild`/`childRegions`), so it is reusable when the lane is ported.

## Stage 1 — DONE (exact parity with the patched Go reference)

**What it is.** The macro lane's core with the *feature set* reduced exactly as the revised plan prescribes: FESS over
box macro states (one box pushed to any reachable square per move), frontier cells keyed by the **packed** (boxes on
goals) and **connectivity** (empty-floor regions) features, the packing and connectivity advisors, lexicographic move
ranking, windowless move storage, dead-end propagation over retained edges, PI-corral restriction, the full push-deadlock
pipeline (2×2, freeze, goal-area, pair, side-dead; cluster proofs applied lazily per applied move), and solution
reconstruction. **Off** (zero/absent in both Go reference and C#): rooms (`room` term/advisor, `computeRooms`), hotspots
(opener/hot terms), move access (mobility/explorer, access novelty), out-of-plan/basin, sink packing plan, plan4,
displacement landmarks, corridor flow, rev-groups, novelty, move windows, frontier corral, obligations, corral deadzone,
pair-side, macro-alt engine.

**Reference.** `bench/macro-stage1/` holds the patch (`reference-patch.diff` + `stage1.go`, ~110 lines) applied to a copy of
`.reference` by `build.sh` (never edits `.reference`): `SOKSOLV_STAGE1=1` overrides those flags in `newSolverDir`, nils
rooms/basin/plan, zeroes hotspot and move-access results, disables the alt engine, and prints an `S1HASH` fingerprint:
`expanded stored generated duplicates` + an FNV hash over every applied `(node, local move index, child id)`;
`SOKSOLV_S1TRACE=1` prints each move. Coverage of this reference (20k nodes × 18 levels): ≈1,500 lines of Go solver logic
actually run (solver.go 768, fess_search 294, fess_moves 251, advisors 91; plus already-ported cluster/goalarea/sidedead/artic).

**C#.** `src/Soksolv.Core/Search/Macro/` — `MacroTypes.cs` (cells, move score, frontier heap, per-node aux),
`FullSolver.Macro.cs` (state, add/dup/reparent, dead-end propagation, frontier select, box search),
`FullSolver.MacroOpen.cs` (open/visit/pushDead/advisors/apply/loop/reconstruct): ≈800 lines. It is a *partial of
`FullSolver`* (namespace `Soksolv.Search`, not `.Macro`) so it reuses the existing flood/normalise, freeze, goal-area,
pair-dead, PI-corral, articulation, feasibility, cluster and side-dead kernels directly and the same unmanaged
`StateTable`/`ChunkList` storage (per-node FESS data in a `ChunkList<MacroAux>`, move arena in `ChunkList<ulong>`).
CLI: `-strategy macro` (use `-parity` for BFS cluster proofs like Go; `SOKSOLV_S1=1` prints the fingerprint).

**Parity** (`bench/macro-parity.py`, `-parity`, fingerprints identical = same sequence of applied moves and child ids):

| set | cap | result |
|---|---|---|
| Holland_81_27 | 1 / 10 / 100 / 1000 / 20k nodes | identical |
| testdata/ (18 levels) | 20,000 nodes | 18/18 identical |
| LTS sample (42 levels, every 7th of `lts_sample.tsv`, minus Sasquatch_VI_50) | 20,000 nodes | 42/42 identical |
| testdata every 3rd (6 levels, incl. 29k-state 100k runs) | 100,000 nodes | 6/6 identical |
Solved levels (e.g. Microban_155_65 after 142 nodes, SokEvo_107_55 after 12,504) produce verified solutions.
Unit tests: `tests/Soksolv.Tests/MacroTests.cs` (10 golden fingerprints from the reference + 3 solve/verify).

**Speed** (Native AOT, `artifacts/aot-macro`, same machine, calib alu ≈435 Mop/s, mem-latency ≈7 Mop/s; 100k-node caps,
whole-process wall time): Go reference 8.1 s vs C# 7.4 s on 6 levels (0.91×); per level 0.76–1.02×. Not yet tuned
(per-expansion `HashOf`, `Regions()` flood for the first candidate per box, per-apply path BFS recompute are the
obvious hot spots, same as Go).

**Not covered by Stage 1** (and therefore not claimed): any behaviour involving the later stages' features — the lane
as the portfolio runs it differs (weaker without rooms/plan: e.g. Holland_81_27 is *not* solved in 100k nodes).

### Next stages (unchanged order from the revision above)
2. rooms + cutscan + basin (adds `room` term, room advisor, OOP) — reference: Go copy with only hotspots/access/plan zeroed.
3. sink packing plan. 4. hotspots. 5. move access. 6. flag features (windows, lazy children, novelty, rev-groups, corral
frontier/deadzone, displacement, corridor flow, obligations, plan4).


## Stage 2 — DONE (rooms + basin/out-of-plan; exact parity)

Adds the room map (`computeRooms`: 2x2-block unions, shared-cell corridors, direct room edges), room connectivity
(corridor labelling + per-room fragment union-find; computed from scratch for every candidate — exact, the Go incremental
`roomConnChild` is only a speed-up checked equal by `checkRooms`), the packing basin (`targetBasin`/`computeBasin`) with the
out-of-plan term (no plan: boxes outside the basin and off goals; child counts adjusted by the moved box), the **room advisor**
and the **out-of-plan advisor** (needs walking distance; per-source BFS cache), and the full `oop,-packed,conn,room` score
ordering. Go reference: `SOKSOLV_STAGE=2` (rooms/basin on; plan, hotspots, access still off). `cutscan.go` is *not* part of
this stage (only the packing planner uses it). C#: `Macro/FullSolver.MacroRooms.cs` (~330 lines); `-macro-stage N`.

| set | cap | result |
|---|---|---|
| Holland_81_27 | 1 / 10 / 100 / 1000 / 20k | identical |
| testdata (18) | 20,000 | 18/18 identical |
| LTS sample (42) | 20,000 | 42/42 identical (go 47.7 s, cs JIT 81 s wall) |
Tests: `MacroTests.Stage2_*` (10 goldens + 2 solve/verify). Not yet tuned: the full room-connectivity recompute per candidate is the
obvious cost (Go's incremental `roomConnChild` is the speed-up to port when tuning).


## Stages 3-5 — DONE (exact parity at every stage)

* **Stage 3 — sink packing plan** (`Macro/FullSolver.MacroPlan.cs`): `buildPackingPlan` (real-sink reachability, sink/pushable
  squares, goal distances), the pull planner (`pullMoves`: removals take precedence, parking moves sorted by weight/score with the
  connectivity advisor, stable sorts, per-box distinct `(to, player region)` ends, round-robin cell queues, 4096-node cap) and the
  plan queries (`status`, `nodeSetup`, `childStatus`, `placed`/`placedRow`). Effects: `packed` and `progress` come from plan phases,
  the packing advisor also fires on phase progress, out-of-plan uses the plan's placed row. **cutscan.go is replaced by exact
  floods** (it is a speed-up of "player region with the box on `to`"; one flood per pull instead of one DFS per box).
* **Stage 4 — hotspots** (`FullSolver.MacroHot.cs`): `row(y)` computed from its definition (box squares that lose reachability of a
  goal when y becomes a wall, per goal: tree minus re-search with y walled as cell and as support) instead of Go's Euler-tour
  re-search; per-node counts, `after()` for children, the hot score term, the hotspot advisor and the opener advisor (needs walking
  distances: cached BFS per source).
* **Stage 5 — move access** (mobility term, explorer advisor): scan form of `moveAccess` with access novelty off.

Go references: `SOKSOLV_STAGE=3|4|5` of `bench/macro-stage1` (plan on / hot on / access on; each earlier stage's features stay on).
Fingerprint parity (`bench/macro-parity.py`, `STAGE=n`, `-parity`, 20,000-node caps unless noted):

| stage | Holland_81_27 @1/10/100/1000 | testdata (18) | LTS sample (42) |
|---|---|---|---|
| 1 | identical | 18/18 | 42/42 |
| 2 | identical | 18/18 | 42/42 |
| 3 | identical | 18/18 | 42/42 |
| 4 | identical | 18/18 | 42/42 |
| 5 | identical | 18/18 | 42/42 |
Unit tests: `MacroTests` (golden fingerprints from the Go reference for stages 1-5, 10 each, plus solve/verify): 61 pass.

**What this lane is at Stage 5:** everything of the default macro lane except the *flag features* (stage 6): move window /
lazy children / reopen, novelty (+ access novelty), rev-groups, frontier corral, corral deadzone, displacement landmarks, corridor
flow, packing obligations, plan4 (+ anytime ring), the macro-alt engine and the portfolio ring/revive wrapper. Speed has not been
tuned since Stage 1 (full room-connectivity recompute per candidate; plan/hot rows computed from definitions): C# JIT wall time is
~1.0-1.7x the Go reference's on 20k-node runs (dominated by .NET startup/JIT on short runs); optimisation is the next job once
stage 6 lands (Go's incremental `roomConnChild` and tree-based hotspot rows are the known speed-ups).


## Stage 6a — move windows / reopen: DONE (exact parity); 6b-6g NOT ported

`SOKSOLV_FEAT` / `-macro-features` is a bitmask of the stage-6 default-on features (bit0 move windows+reopen, bit1 deadzone
corral proofs + frontier corral, bit2 displacement landmarks, bit3 corridor flow, bit4 packing obligations, bit5 plan4,
bit6 rev-groups/novelty hooks, bit7 pair-side); the Go reference forces every clear bit off, C# mirrors it. Implemented: bit0
(window of 8 stored moves per opening, `mvOff`, reopen loop in the search loop incl. the `expired()` guard, re-opening a node
re-runs candidate generation exactly as Go does). `LazyChild` (partial selection, incremental packed/oop) is performance-only
(`checkRegions` invariants) and not ported. Parity (`STAGE=5 FEAT=1`, 20k nodes): Holland_81_27 @1/10/100/1000/20000, testdata 18/18,
LTS sample 42/42 identical; 10 goldens in `MacroTests.Stage6a_*`.

Not yet ported (each needs its own reference toggle, already in the Go patch): 6b deadzone corral proofs (`deadzone` ~1.5k lines) +
frontier corral (`corral_frontier.go`, read in full, ~130 lines), 6c displacement landmarks, 6d corridor flow, 6e packing
obligations/causal, 6f plan4 (+anytime ring), 6g alt engine / rev-groups / novelty hooks, pair-side (bit7). The CLI/portfolio macro
lane therefore runs "stages 1-5 + move windows"; it is *not* the full default Go macro lane.

Wiring done: `-strategy macro` (defaults `-macro-stage 5 -macro-features 1`), a `macro` lane in `Portfolio` (own thread, shared cancel
flag and cluster table, equal memory budget; macro lane checks its tracked bytes against the budget every 256 expansions);
stage/features live in `FullOptions` (no statics, safe for parallel lanes/tests). 105 tests pass.
