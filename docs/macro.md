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
