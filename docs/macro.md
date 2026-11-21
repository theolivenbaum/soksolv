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


### Hard levels (28 Go-unsolved-at-20 s levels, `-parity`, stage 5 + 6a, 20 s each, Go and C# AOT run concurrently, niced, shared cores)
Neither solver solves any of the 27 stable ones within 20 s. One level (Sasquatch_VII_50 #13) is solved by the Go reference in ~42-64 ms
in *some* runs (1 of 3 reruns; the other runs time out): Go's timed mode is itself nondeterministic there (time-budgeted planner/stall
logic), the node-capped mode of the same level matches C# exactly (fingerprint identical at 100k nodes), so this is not a C# defect.
Throughput on the 27 levels that ran to the limit: Go 6,916 nodes/s vs C# 7,976 nodes/s on average (median per-level ratio 0.89, range
0.33–2.43 — timed searches diverge, so per-level node counts are not like-for-like work); peak tracked memory 46.7 MB (Go) vs 16.0 MB (C#), 2.9x lower.
Speed (node-capped, equal work, AOT, 50k nodes, 6 testdata levels): Go 7.3 s vs C# 7.9 s (1.08x); per-candidate costs on Holland_81_27
(JIT, 30k nodes): room connectivity 16%, region counts 10%, push-deadlock pipeline 6%, move access 4%, hotspots 3%.
Next speed work (not done): Go's incremental `roomConnChild` and tree-based hotspot rows.


## Stage 6b (corral deadzone proofs + frontier corral) and 6c (displacement landmarks): DONE (exact parity)

* **6b** (`SOKSOLV_FEAT` bit1): `Search/DeadZone/DeadZoneChecker.cs` ports Go package `deadzone` (zone/corral enumeration, pairs, groups of >=3
  corrals, fixed boxes + matching, FastKey cache + shared verdict table, BFS with Kill/Escape/Shrink) including the **word-parallel path**
  (row bitsets: `escapeBits`, seeded child floods `seedChild/seedFinal`, `floodFreeMode`) used on boards up to 64 wide; wider boards use Go's live
  `escapeScan`. *Parity pitfall found*: Go's "scan" and "bits" escapes are not interchangeable (fence cells are not monotone under removal; the
  bit path judges a whole pass against the pass-start snapshot) - a Gauss-Seidel scan diverged on `Sven_1623#98` after ~15k nodes. The frontier corral
  (`FirstPushCorral`, `corral_frontier.go`) precedes the PI-corral in `open`; the checker runs in `apply` after the cluster check.
* **6c** (bit2): `Macro/Displacement.cs` (single-pin CMD certificates + collective unpacking), the score adjustment (`packed`/`oop` of a mandatory
  displacement push) and the 8th (displacement) advisor.
Parity (`STAGE=5`, 20,000-node caps; FEAT=3 for 6b, FEAT=7 for 6c): Holland_81_27 at 1/10/100/1000/20000 identical; testdata 18/18; LTS sample 42/42 for both.
C# is at parity or faster than Go on testdata (e.g. 13.2 s vs 15.3 s total on FEAT=7); on wide levels with ~775 candidate moves per open (Sven_1623#21)
it is still ~2.2x slower: Go uses word-parallel variants of the box-search flood, move access, child region counts and pair search that are not yet ported.


## Stages 6d (corridor flow) and 6e (packing obligations): DONE (exact parity); StallFinish / StallScale (timed mode)

* **6d** (`FEAT` bit3): `Macro/FullSolver.MacroCorridor.cs` (1-wide cut corridors, transit-capacity rule). Parity on testdata 18/18 + LTS 42/42 (FEAT=15) - but note the
  rule fires **0 times** on all of those levels and on ~100 further LTS levels at 12k nodes (earlier deadlock rules win), so its parity is only "never wrongly fires".
* **6e** (bit4): `Macro/FullSolver.MacroObligations.cs` = `packing_route.go` (witness route compiler), `packing_causal.go` (event DAG, `infer`) and
  `packing_obligations.go` (ready/blocked cells + the fallback packing advisor); the plan now keeps each phase's player and reverse move. Parity testdata 18/18 +
  synthetic corridor level (FEAT=31); the advisor fires thousands of times per level (e.g. `prep=3940 picks=1101`).
* **StallFinish / StallScale** (Go `fess_search.go`, timed runs only - disabled under `-nodes`): when the best node has not improved for max(1 s*scale, 15% of the search time),
  up to 3 times, a private reverse search from the solved position toward the best node runs for min(1 s*scale, remaining/10) and its reversed pulls are appended to the
  prefix (`ReverseToward`). `StallScale = t/600` for t < 600 s (main.go `-stall-scale`; also `CellsAfterSec = 2 s*scale` in that reverse search), set from the CLI limit / the
  portfolio's per-call limit. Time-based constants audited in the macro path: plan budget `min(250 ms, remaining/10)`, setup deadline `min(100 ms, remaining/10)`, the 50 ms
  memory check (not scaled in Go) - plan4's `Plan4Frac` and anytime budget will be mirrored with 6f.
* Timed-mode sanity (`bench/macro-timed.py`, macro lane stage 5 + FEAT 31 on both sides, 28 hard levels at `-t 10`, 4-core shared VM): **Go 7 solved, C# 8 solved**; on the 7 levels both
  solve C# needs 20.0 s vs Go 21.1 s; deterministic cases have identical node counts (75506, 55512, 55507, 266151); on very wide unsolved levels C# reaches 10-50% fewer nodes in
  the 10 s (same gap as in the -nodes timing: word-parallel box-search/move-access/region variants not yet ported), on narrower ones (SokHard_163) it reaches more.


## Status after stage 6e (what is and is not the Go default)

Ported with exact fingerprint parity (patched Go reference, `SOKSOLV_STAGE=5 SOKSOLV_FEAT=31`): stages 1-5, move windows/reopen (6a), corral deadzone proofs + frontier
corral (6b), displacement landmarks (6c), corridor flow (6d), packing obligations (6e); plus the timed-mode StallFinish/StallScale. `-strategy macro` and the portfolio's macro lane now
default to `-macro-stage 5 -macro-features 31`. 141 tests pass.

**Not ported (the macro lane is still weaker than Go's default):**
* **6f plan4** (`-plan4-macro`, `-plan4-anytime`, `-plan4-anytime-restart`, all default on; macro lane only): Festival's BASE_SEARCH-style packing planner
  `planner2`/`planner4` (`packing_plan2.go` ~280 executed lines, `packing_plan4.go` ~800, `packing_fest.go` ~70) whose witness gives the macro lane its *Festival prefix* `packed`
  feature (`festPacked`; `plan.fest.packed(occ)` in `solver.go packed()` and the child variant at `solver.go:927`), the anytime stepping inside the loop (`stepPlan4`, `-plan4-anytime-ratio 8`)
  and the restart when it completes (`Result{Reason:"replan"}` -> the lane ring in `lane.go`). The functions that actually run are listed by coverage (`bench/macro-scope.sh`, executed-lines per function
  in the session notes): `newPlanner4`, `expand4`, `computeElim4`, `start4/step4/finish4`, `separated/festRoomSep`, `keepParks4/parkKids4/sortKids`, `endPlan4`, `planAnytime`,
  `labelRegions`, and in planner2 `kid`, `addNode`, `result`, the matching helpers (`matchOK`/`matchSkip*` are one result-identical pair: port `matchOK`), `seenInsert`, `flood`.
  Under `-nodes` the plan4 phase is bounded only by the planner's own caps, so it is deterministic and comparable with the same fingerprint method (flag `SOKSOLV_FEAT` bit 5).
* 6g: `macroalt.go` (second engine on the same core, ~50 executed lines), rev-groups/novelty hooks (flag-gated, not executed by default), pair-side (bit7, flag-gated).
* The portfolio wrapper around the lane (`lane.go` ring: replan/revive restarts) and elastic memory.
* Speed: Go's word-parallel variants of the box-search flood (`boxflood.go`), move access (`moveaccess.go` bits), child region counts (`regionsChild`) and the pair search (`pairbits.go`) are
  not ported; on wide levels (hundreds of candidate moves per open) C# is ~2x slower than Go (10-50% fewer nodes in a timed run), at parity or faster on narrow ones.


## Stages 6f (plan4) and 6g (alt engine, baton, pair-side, rev-groups): DONE (exact parity); the macro lane now equals Go's default lane

* **6f plan4** (`FEAT` bit5, `Macro/FullSolver.MacroPlan4.cs`): `planner2` + `planner4` (BASE_SEARCH-style backward pull planner with gated sink moves, FESS feature cells,
  matching-based sink feasibility `matchOK`, Tarjan SCC `computeElim4`, region labelling, stable key sort, seen-set, kids store), `packing_fest.go` (`festPlan`: Festival event list,
  `packed(occ)`, partition/keep-only-perm), the **anytime planner** (`planAnytime`/`stepPlan4`: 8 planner iterations per macro expansion when the up-front phase did not complete, restart of the
  lane ring on the finished plan = `prePlan`), and the lane ring (`RingRun`). `cutscan.go` is replaced by exact floods (`Query`, `Pieces`, `CompMin`). Under `-nodes` the planner runs to completion
  (`planBudget = 1 h`), so the node-count-driven path is exact; the timed path mirrors Go (`Plan4Frac` 20% of the remaining time, planner gets 4/5 of it).
* **6g**: `Macro/MacroAlt.cs` — the **alt engine** (exp-769, default on): a second macro solver whose plan4 runs festsink + roomsep + keep-parks 1 (`FestSinkAdv`, `FestPackAdv`, `FestRoomSep`, `SetAdv`, `KeepParks4`),
  alternating with the default engine on a `Baton` (doubling slices `1024 << min(k, 4)` expansions, per-engine `-nodes/2`, first solve wins, lane `Nodes` = sum of both engines). **Pair-side deadlocks**
  (`PairSideDeadlock`, `PairSplits`, `PairDeadFrom`, bit 7) and **reversible preparation groups** (bit 6: per-node union-find groups, exit deduplication, undo/internal/dead-child demotion, best-exit advisor,
  recorded demoted words replayed on reopen). Novelty / access-novelty are inert in the macro lane (`noveltyBudget() == 0`), so they are not ported.
* Fingerprint parity (`bench/macro-parity.py`, `STAGE=5`, `-parity`, 20,000 nodes, patched Go reference; `ALT=1` = the alt planner config as the single engine):

| mask | what | testdata (19) | Holland_81_27 @1/10/100/1000/20000 |
|---|---|---|---|
| FEAT=63 | + plan4 | 19/19 | identical |
| FEAT=63 ALT=1 | + alt planner config | 19/19 | - |
| FEAT=191 | + pair-side | 19/19 | identical |
| FEAT=255 (ALT=0 and 1) | + rev-groups = all default features | 19/19 and 19/19 | identical (solves at 14,873 nodes like unpatched single-engine Go) |

* **End to end vs unpatched Go** `soksolv -strategy macro -procs 1 -nodes N` (two engines + baton + all features; `bench/compare.py` with `GO_FLAGS`/`CS_FLAGS`, parity mode): **19/19 testdata levels identical in solved, pushes and
  nodes_expanded at N=40000**, time ratio 1.04 on the 12 solved ones; ten further level/cap pairs spot-checked (N=4000/40000) identical. Goldens in `MacroTests` (`Stage6f`, `Stage6g`, `MacroLane_EndToEnd`); 176 tests pass.


## Final status (6f/6g done) — speed work and measurements

**Parity summary (all with `-parity`, node-capped):** fingerprint parity vs the patched Go reference at the full default feature mask (FEAT=255) on testdata 19/19 (single engine, and `ALT=1` = the alt
planner configuration) and on the 42-level LTS sample 41/41 identical + 1 skipped (`Sasquatch II_50#25`: 50 boxes, plan4 runs to its 200,000-expansion cap; Go 47 s, C# > 60 s per run), for both ALT=0 and ALT=1.
End to end against **unpatched** Go `-strategy macro -procs 1 -nodes 40000`: 19/19 testdata levels identical in solved/pushes/nodes (two engines + baton + all features), time ratio 1.04.

**Speed work done after parity** (each validated against the exact version it replaces, see the `*Check` hooks and tests):
* plan4's `cutscan.go` low-link scan instead of one flood per query (`CheckCut`): plan4 setup on Sven_1623#21 362 ms → 31 ms.
* `pairbits.go` word-parallel pair search for boards up to 64 wide, pinned rows (`PairBitsCheck`).
* `matchSkip` (one maximum matching per planner expansion + alternating-path queries; `MatchCheck`): setup on a 198-box level (Sasquatch_VI_50#48) 19.2 s → 2.1 s.
* pooled per-box scratch in the planner; allocation-free `PairDead`; wall|box byte maps and pinned flood kernels (earlier).

**Timed comparison vs unpatched Go** (`bench/compare.py`, `GO_FLAGS="-strategy macro -procs 1" CS_FLAGS="-strategy macro"`, Native AOT, 4-vCPU shared VM, calib alu ≈ 497 Mop/s / mem-latency 8.0 Mop/s):

| set | result |
|---|---|
| 28 hard levels (Go `full` lane failed them at 20 s), `-t 10` | solved: Go 7, C# 7; time on the 7 both solve: Go 25.6 s, C# 22.3 s (ratio 0.87) |
| per-level node throughput on the 21 unsolved ones | SokHard_163: C# 85–145% of Go; wide Sasquatch levels: C# 45–100% (e.g. VI_50#48 1,045 vs 2,877 nodes in 10 s) |
| tracked structure memory (C# `peak_memory_mb`: node store + move arenas) vs Go peak | C# 8–13 MB vs Go 14–200 MB (not strictly like-for-like: Go reports its heap) |

The timed solves are non-deterministic in Go itself (Sasquatch_VII_50#13: 71–300 ms in Go, 16 ms in C#); `DIVERGED` flags in timed mode are expected (different time-driven interleavings), node-capped runs are the exact check.

**Remaining gaps**
* Per-candidate `open()` cost is still ~2x Go on very wide levels (hundreds of candidates per node): goal-area (≈1.3 µs/call), pair-table lookups, hotspot `after()` and move-access scans are not yet word-parallel; plan4's box loop is ~2.5x Go per expansion on 50+-box levels (96 s vs 37 s for the up-front phase of Sasquatch II_50#25).
* `LazyChild` (incremental per-child packed/oop) and Go's incremental `roomConnChild` / tree-based hotspot rows are not ported (performance-only).
* Unported experiment features: novelty/access-novelty (inert at the macro lane's default `noveltyBudget()==0`), the relay/hub/fa2 hooks, the elastic-memory pool and memory-governed `revive` ring.

## Speed work on wide levels (equal-work CPU time vs Go, AOT build)

Harness: `bench/wide-timing.py macro bench/wide-levels.tsv` — same `-nodes` cap on both sides, child **CPU time**
(user+sys, robust to the loaded shared VM), `-parity` mode, every row also checks identical solved/nodes/pushes.
Profiling: `SOKSOLV_PROF=1` section counters (+ `bench/ss-top.py` over `dotnet-trace --format Speedscope` for JIT builds)
against `go tool pprof` of the same run.

| level (nodes) | before | after | what changed |
|---|---|---|---|
| Sven_1623#21 (3000) | 1.99x | 1.13x | hotspot columns, goal-area memo, bit box search |
| Sven_1623#10 / #50 | 1.29 / 1.18 | 1.06 / 1.10 | idem |
| Sasquatch_VI_50#48 (1500, 50 boxes, plan4) | 1.65x | 1.16x | tree hotspot rows, plan4 radix sort |
| Sasquatch_VI_50#34 | 1.26x | 1.1-1.2x | bit flood + moveAccessBits |
| Sasquatch_50#19 | 0.97x | 0.99x | |
| Sasquatch_IV_50#32 (plan4-bound, 13 s) | 1.17x | 1.01x | plan4 radix sort / in-place beam |
| SokHard_163#36 / #96 | 0.89 / 0.71 | 0.84 / 0.82 | |
| Sasquatch_VII_50#27 / Holland_81#14 / III_50#11 | 1.18 / 1.08 / 1.02 | 0.98 / 0.99 / 1.07 | |
| **total, 12 levels** | **1.22x** | **1.05x** | parity 12/12 identical at every step |

Changes (each verified fingerprint-identical with `bench/lane-parity.py macro` on the 19 testdata levels at 5000 nodes
and the 12 wide levels at 800 nodes, plus the unit suites):
* `FullSolver.MacroHot.cs`: hotspot rows cached in an array (was a `Dictionary` lookup per box per candidate), Go-style
  per-node column bitsets (`HotCounts`/`HotAfter` = word ops per candidate), and tree-based rows (per-goal pull trees with
  Euler tour; only the subtrees a blocker cuts are re-searched). The definition-based builder stays as the reference
  (`SOKSOLV_CHECK_HOT=1` compares every row).
* `FullSolver.MacroBits.cs`: node-mode `bitBoard` (row-bitset flood inside `MBoxSearch`, `boxBitAfter` rule for non-node
  searches) and `moveAccessBits` (mobility / explorer / access facts as row expressions); the scan version remains as the
  fallback for boards wider than 64 and as the reference (`SOKSOLV_CHECK_BITS=1` compares every call, incl. the novelty facts).
* `FullSolver.MacroPlan4.cs`: stable LSD radix sort of plan4 children (Go `sortKids`), in-place beam fill, `ref` node access.
* `GoalArea.cs`: exact-key survivors memo (Go `GoalAreaMemo`), pure cache.

Remaining gap (>1.0x): per-candidate constant factors on the widest levels (Sasquatch_VI_50#34/#48, Sven_1623#21) —
`CutQueryFill` (35% of plan4 in both languages) and `Expand4` bounds checks, displacement matching, region counts.
Fixed start-up costs are equal (0.19-0.20 s for Sven_1623#21 in both).
