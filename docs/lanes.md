
# Hub lane family (`hub-gen`, `hub-pull`, `hub`) — scope and status

Measured with a coverage-instrumented Go build (`go build -cover`, `GOCOVERDIR`), running `-strategy hub-gen|hub-pull|hub
-procs 1 -nodes 20000 -t 20` on Holland_81_27, SokEvo_107_55, Sven_1623_100, Microban_155_65 (same method as
`bench/macro-scope.sh`). `-nodes` runs are deterministic in Go (identical nodes/pushes twice), so fingerprint/node-count
parity is a valid method for these lanes once the pull solver exists. `-strategy hub` ≡ `hub-gen` (hubOn = true by default:
the festb slot runs the hub lane).

## What a hub lane is at default flags
* `hub-gen` / `hub`: `festbRun(useHub=true)` → `reviveLoop` → `festbRun1` builds a **pull solver** (`newPullSolver`, laneID 7,
  `pfOn = true` = Festival-order pull search, owner/bestSlot FestBLane) and calls `hubLane(...)` (hub.go) = a backward pull tree
  plus **hub generators** (hubgen.go) that nominate hubs checked by exact single-box reach (hubreach.go, `-hub-gen-reach`
  default on) and met by a **forward toward-search** (fess_toward.go, HF scheduling hfsched.go), in doubling commitment windows
  with U = hubUnit(T) (7.5 s at T=90, 30 s at T=600, 10 s unlimited). Idle rounds alternate with strategy B (`festbAlternate`).
* `hub-pull`: `festbRun`-style `reviveLoop` with `newPullSolver` (laneID 9, `pfSty = festXStyle()`, strategy D) and, because
  `HubRace` and `HubIdleRing` are default on, `raceAlternate` (hubrace.go): backward pull search racing forward toward searches,
  hub strata (hubstrata.go), pullzone (corral proofs for pull mode).

## Executed Go lines (unique lines run, of total) — only the hub-specific files
| file | hub-gen | hub-pull | role |
|---|---|---|---|
| hub.go | 539/722 | – | hubLane: windows, commitment, hub nomination |
| hubgen.go | 538/695 | – | hub generators (pull-walks c→x), class counts |
| hubreach.go | 159/169 | 118/169 | **ported** (`Search/Hub/HubReach.cs`, exact) |
| hubrace.go | 36/277 | 221/277 | raceAlternate |
| hubstrata.go | – | 71/171 | strata |
| pullfest.go | 146/252 | 181/252 | Festival-order pull search (pfOn) |
| fess_pull.go | 63/320 | 149/320 | pull solver construction, pullTail/solution, perimeter refs |
| pullzone.go | – | 129/137 | pull-mode corral/zone proofs |
| fess_toward.go | 64/273 | 36/273 | forward toward-search |
| festineff.go, hfsched.go, corral_fest.go | 79, 31, 231 | 77, 31, 231 | shared FESS pieces |
| meet/meet.go | 45/121 | 38/121 | perimeter tables, Complete(), meetings |
The rest of the executed lines (≈ 6k) is the macro solver (solver.go 940–970, rooms, fess_moves, deadzone, hotspots, …) which is
already ported — **but only in push direction**.

## Why this is blocked on a pull-direction macro solver
The hub lanes drive `solver` in **pull mode** (`newSolverDir(pv, deadline, pull=true)`: boxes start on the goals, the start squares
are the goals). `s.pull` changes ~35 sites in the shared engine (solver.go 479/875/927/1333/1368/1478/1843/1891/2446–2546,
fess_moves.go 49/74/95/248/264/332/944/1040, fess_search.go 366/465/512/533/587/723/828, advisors.go 341 …: `front`/`playerAfter`/
`supportCell` pull macros, no packing plan, no PI-corral pruning, pull-specific feasibility and dead-state tests), plus
`pfOn` (pullfest.go) and the perimeter machinery (`meet`: `Table`, `Meeting`, `Complete`, `StoreBest`, candidate refs) that hubLane
uses to hand pull-tree states to the forward toward-search. The C# macro engine (`Search/Macro`, `FullSolver.*`) hard-wires push
direction (`mark[b-d]`, push legality, dead squares from goal distances, plan4…), so the pull mode has to be added to it first
(or a parallel pull engine written), validated against Go with pfOn off (`-strategy pull`), before any hub code can run.

## Status
* [x] `HubReach` (exact reach classes, class counts, `Ok`, exact matching `Feasible`): **exact vs Go on all 19 testdata levels**
  (nCls, need[], inU, checksum of every per-box reach set and class mask, matching probes, class counts, Ok() acceptance) —
  `tests/Soksolv.Tests/HubTests.cs`, goldens in `tests/Soksolv.Tests/golden/hubreach.json` (dumped by a Go test in a scratch copy:
  `hubdump_test.go` calling `newHubReach(newPullSolver(lv, …))`).
* [ ] pull-direction mode of the macro engine + `-strategy pull` parity (prerequisite; est. ~1.5k Go lines of deltas:
  fess_pull.go 498, pullzone.go 191, pullreach.go 41 + the ~35 `s.pull` sites)
* [ ] pullfest.go (pfOn Festival order, 400 lines), meet/meet.go (273), festineff.go (131)
* [ ] hub-pull: hubrace.go (393), hubstrata.go (255) → `-strategy hub-pull`
* [ ] hub-gen/hub: hubgen.go (1037), hub.go (1112), fess_toward.go (388) + hfsched.go (58) → `-strategy hub-gen|hub`
* [ ] Portfolio lanes (hub-gen, hub-pull) once the above run; timed behaviour: `hubUnit(T)`, doubling windows, StallScale.

---

# Pull, novelty (and toward) — status (seventh fork)

Machine for all numbers: Intel Xeon @ 2.10GHz, 4 vCPU shared with other forks' runs (niced), .NET 11 RC3 Native AOT build vs Go 1.2x
(`bench/lane-parity.py`, `bench/lane-timing.py` print/compare; both print the machine header via `-header`).

## `pull` — DONE, exact parity
`-strategy pull` = `FullSolver.SolvePull` (`Search/Pull/FullSolver.Pull.cs`): the macro engine in **pull direction**. A pull view of the level
(boxes on the real goals, start squares as goals, one root per player zone of the solved position) runs through the existing macro
solver with `pull = true`; `front()/playerAfter()/supportCell()` generalise the push geometry at the ~12 sites that matter at default
flags (distance BFS, box search, hotspot rows, move access, apply). Pull-specific: spread feature (`packed = Σ spreadDist / 4`,
incremental per child), `pullFreezeDead` (64-visit frozen-group proof, replaces the push deadlock family), no cluster / side-dead /
deadzone / PI-corral / displacement / basin / packing plan, best-state feasibility (`PullBestFeasible`), multi-root start, solved when all
boxes reach start squares with the real player in the region → `pullTail` (pulls reversed) + `meet.Complete` walking → forward LURD.
Not ported (not on the standalone path): perimeter tables / meetings (`publishCandidate`, `pullPostMeetings`, mailboxes), pfOn
(Festival-order pull used by the hub lanes), revive-diversify restarts.

| check (Go `-strategy pull -procs 1 -nodes N` vs C# `-parity`) | result |
|---|---|
| 19 testdata levels, N=5000 (solved, pushes, expanded, stored, generated, duplicates) | 19/19 identical |
| + 40 sampled LTS levels, N=20000 | 59/59 identical |
| equal-work time, N=20000, 17 testdata levels | Go 7.2 s, C# 6.7 s (**0.93x**); wide levels (Sasquatch) 1.2–1.4x slower (word-parallel box search not ported) |
| mean peak memory | Go 14.9 MB, C# 8.8 MB |

## `novelty` — DONE, exact parity
`-strategy novelty` = `FullSolver.SolveNovelty`: the macro lane as Go's laneID 5 (v1 sink plan, **no plan4, no alt engine**) with
`novelty = 100`: per FESS cell memory of box squares (width 1), box-square pairs (width 2, `Novelty2`) and access capability facts
(`AccessNovelty`: push sides, passage, displacement; recorded by `MMoveAccess`), novel moves cost nothing, tiers 0–3 order them
(StableWindows: recorded per node for reopened windows). Code: `Macro/FullSolver.MacroOpen.cs` (`MNoveltyBlock`, tier keys),
`Macro/FullSolver.MacroHot.cs` (fact recording), `Novelty/FullSolver.Novelty.cs`.

| check (Go `-strategy novelty -procs 1 -nodes N` vs C# `-parity`) | result |
|---|---|
| 19 testdata levels, N=3000 (incl. 5 solved, same pushes) | 19/19 identical |
| + 40 sampled LTS levels, N=20000 | 59/59 identical |
| equal-work time, N=5000, 17 testdata levels | Go 7.4 s, C# 7.1 s (**0.96x**) |
| mean peak memory | Go 13.1 MB, C# 8.6 MB |

Both lanes are in `Portfolio` (`novelty` on by default, `pull` opt-in with `-pull`: without the backward perimeter it rarely finishes a
level alone and would only burn a core).

## `toward` / `tfwd` / `hf` — NOT ported (scoped)
Go's `toward` lane cannot run alone: it waits for the pull lane's best state (`meet.Best[PullLane]`) and then runs the FESS
"toward" search (`fessTowardRealWithPrev`, fess_toward.go) against that board, meeting the live perimeter. The deterministic
stand-alone form is `-strategy hf` (pull for 1/3 of the nodes, then toward). Coverage of `hf -nodes 20000` on testdata (executed
lines): solver.go 1030/1586 (target ranking `tgt*`, `hfRank` features), deadzone 808, corral_fest.go 231, diagdead 124, meet.go 111
(perimeter tables / `Complete`), pullfest 0 (pfOn off), fess_toward 49, festineff 61, incremental_match 45, hfsched 31, plus the
already ported macro/pull code. Needs: target-board machinery (`setTarget`, `tgtDist/tgtPocket`, `hfRank` scoring in open/advisors,
`hfMatchParent` incremental matching), `festCorral`, `diagdead`, `festIneff`, and the in-process `meet` tables with mailboxes (shared with
the hub family). Estimate ~1.5k Go lines; it is the first thing to port once the perimeter exists, together with the hub lanes.


---

# Meeting network (meet) and what is blocked behind `toward` (tenth fork)

## Done: bidirectional meetings (`Search/Meet/`, port of Go package `meet` + the lane-side hooks)
* `MeetNet`/`Table`/`Meeting`/`Target`/`MPush` (`Meet/Meet.cs`): Go's lossy 4-way perimeter (Zobrist key per cell, splitmix constants identical,
  grow x8 up to 2^24 slots, lock-free CAS inserts), per-owner bounded mailboxes (64, a full box drops the post), `Complete` (walk
  between pushes by BFS, "" if unreachable), `EndCell`. The `Back` table holds the backward searches' states (owners: 0 reverse,
  3 pull), `Fwd` the forward searches' (0 full, 1 macro, 3 novelty).
* Hooks, each following the Go code path for stored nodes: `full` lane (`Meet/FullSolver.Meet.cs`: add -> `Fwd.Add`, new node that
  `Back.Has` -> pending; after the expansion post prefix + player to `Back`; every 256 expansions resolve mail from `Fwd`),
  `macro`/`novelty` lanes (same code through `MReconstruct`), `reverse` lane (`Reverse/ReverseSolver.Meet.cs`: add -> `Back.Add`,
  probe `Fwd`, post tails to `Fwd`, resolve mail from `Back` owner 0) and the `pull` lane (`Meet/FullSolver.PullMeet.cs`, owner 3,
  tail = `PullTail`). Every joined solution is re-verified by the portfolio (`Portfolio.Lane`).
* `Portfolio`: the network is created when a backward lane (reverse, or pull with `-pull`) and a forward lane run; `-no-meet`
  disables it; `SOKSOLV_MEET_DEBUG=1` prints post/confirm counts. Meetings observed: reverse posts 20-70 hits per level on the SokHard
  levels, 1-3 confirmed (a confirmed meeting finished the level), e.g. `SokHard_163#36` solved by `full` through a reverse post.
* Tests: `MeetTests.cs` (key order independence, add/has/growth, mailbox cap/drop, `Complete`, portfolio with meetings solves+verifies).
* **Not exact Go parity:** meetings are timing dependent in Go as well (threads), so no node-count parity exists; the check is that
  every meeting yields a verified solution and that the lane parities (`-nodes` mode, no net) are unchanged (they are: tests green).
* Not ported from the Go meeting protocol: candidate references for lazily generated children (`pendCands`/`refGet`/`reconstructRef`,
  exp-659), `RetainFwdMail` previous-tree mail, `Best` target publishing (`publishPullBest`/`StoreBest`; the table slot exists in `MeetNet.Best`).

## Done: diagonal-chain deadlocks (`Search/DiagDead.cs`)
Exact port of `diagdead.Checker.Dead` (the Go default `-diag-dead=target` applies it to toward-family searches only). Not wired to a lane
yet (needs `toward`) and not unit-tested against Go goldens yet.

## Not done: `toward` / `tfwd` / `hf`, `hub-pull`, `hub-gen`, `hub`
Scope measured for `toward`/`hf` (default flags; `TowardHF = true` -> `fessTowardReal`):
* Target mode in the macro engine (`setTarget`: target squares replace the goals for ranking only, `tgtDist` per target square, `packed` =
  boxes on target squares, `hotRel` = hotspot count against the target, `hfMatchParent/Child` = min-cost matching distance to the target
  (exact Hungarian, `assignmentInf = 1<<20`; the incremental dual update in incremental_match.go returns the same value), no plan
  (`noPlan`), no displacement landmarks, `cell = {packed, hotRel, oop: reg}` at node creation, `diagdead` on for `hfRank`, solved test
  `onTarget(child)` -> `completeTarget` = prefix + `Complete(target boxes, tail)` verified, `nodeLimit`) — about 15 sites in
  `FullSolver.Macro*.cs` (MAdd cell, MOpen parent/child features, MPacked, deadlock chain, search loop) plus ~300 new lines (Hungarian,
  target setup, hotRel). `hf` = `SolvePull` for `N/3` expansions, `bestTarget()` (frontier best + `PullTail`), then toward for the rest
  while a drain loop empties the pull mailbox (`pullCheckMail`, now available as `PullMeetCheckMail`).
* Prerequisites now in place: meet network, pull lane with perimeter + tails, `DiagDead`. Remaining: the macro-engine target mode and
  the Hungarian matcher, validated by `bench/lane-parity.py hf "" N` (solved, pushes, expanded, stored, generated, duplicates) against Go's
  deterministic `-strategy hf -nodes N`.
* Hub lanes additionally need `pullfest.go` (`pfOn` Festival-order pull, 400 lines), `hubrace.go`/`hubstrata.go`/`hubgen.go`/`hub.go`
  (~2.8k lines), `fess_toward.go` retarget machinery (`RetargetReuse`, previous trees), see the scope table above. Not started.

## fa2 lane (`-strategy fa2`) — Go fa2.go, fa2rooms.go (semantics), hall.go, oopfest.go, rooms_fx.go, hotsides.go, packing_plan2.go (planner v2)

Plan-guided forward FESS engine: children are scored once into 16-byte edges (windowed to the next 16), duplicates are
skipped by one hash probe, the first expansion owns a state. Built on the macro move generator (box search, push-dead tests,
corral restriction, dead-zone/cluster late checks) and the Festival-prefix plan. Code: `src/Soksolv.Core/Search/Fa2/`
(`Fa2.cs` engine, `Fa2Run.cs` setup/loop, `Fa2Oop.cs` out-of-plan zones, `Fa2Rooms.cs` room-pair score, `Fa2Hall.cs` Hall credit
gate, `Fa2HotSides.cs` player-side hotspot relation) plus a V2 mode in `Planner4` (`ExpandV2`/`RunV2`, planner2's best-first loop).
Coverage of what `-strategy fa2` executes (Go, default flags, `bench/lane-scope.sh`): fa2.go 707 lines, fa2rooms 416 (incremental
form of rooms_fx), hall.go 407, packing_plan2.go 539, oopfest 124, rooms_fx 134, hotsides 99 + the macro machinery already ported.

### Parity (vs unpatched Go, `bench/lane-parity.py`, `-nodes 20000`, `-parity` cluster mode) — identical solved / nodes_expanded / pushes

| configuration (Go flags) | testdata (19) | LTS sample (40) |
|---|---|---|
| core: `-fa2-plan old -fa2-rooms=false -fa2-hall=false -hot-sides=false` | 19/19 | 40/40 |
| + rooms (`-fa2-plan old -fa2-hall=false -hot-sides=false`) | 19/19 | 40/40 |
| + hot-sides + Hall (`-fa2-plan old`) | 19/19 | 40/40 |
| **default** (planner v2) | **19/19** | **40/40** |

xunit: `tests/Soksolv.Tests/Fa2Tests.cs` (Go golden counts for default and `old` configs, solve + verify).

### Speed / memory vs Go (AOT build, single lane, 19 testdata levels, `bench/compare.py`, same machine, nodes identical)

* parity cluster mode: time ratio 1.19 (Go 37.8 s, C# 44.9 s); default fast cluster proofs: **1.11** (36.5 s vs 40.4 s);
  per level 0.58x–1.36x; memory 1.5–3.2x lower than Go (e.g. 29.7 vs 96–101 MB on Sasquatch_IV_50 #42).
* where the time goes (`SOKSOLV_PROF=1`): box search + push-dead tests ~55%, advisor verification (late cluster/dead-zone checks) ~22%,
  room-pair score ~17% on wide levels. Go computes the room score incrementally per child (fa2rooms.go, ~600 lines, verified equal to the
  full recompute in Go itself); the C# port recomputes `festRoomsFull` per candidate — exact but the main remaining gap (todo).

### Not ported / differences
* The *anytime lane* used inside Go's portfolio (`fa2LaneRun`: own v2 plan from t=0 plus a second engine on the macro lane's plan4 list, baton
  slices) — the C# portfolio runs the plain fa2 engine (Go's `-strategy fa2` / `-fa2-lane-solo` engine 1).
* Experiment flags off by default: planner `v2ng`, `-fa2-hall-sup`, `-fa2-mobility=false` (last key = goal distance), plan files, replay/stream modes.
* Time-dependent behaviour: planner budget = min(1 s, t/10) (as Go); nothing else in the plain engine reads the clock.

## factor lane (`-strategy factor`) — Go factor.go (exp-645)

Boards whose live squares (pull flood from the goals) split into >= 2 non-trivial static box components (a component with boxes != goals).
Each component is solved as its own level (the other components' boxes absent, trivial components as walls then absent) by a ladder of
engines — fa2, macro, reverse (private, `MaxNodes = cap*16`), novelty — under escalating node caps (steps 1k..8k, 16k..32k, 64k..128k,
round robin); the plans are scheduled on the real board by a depth-first search over (plan index per group, box set of each small
group, player region); stuck groups are re-planned with the other boxes frozen as walls, planned by another engine (`altEngine`), or
merged with the group that blocks the player. Gate (`FactorLane.Gate`): 224 of the 13,315 levels (`bench` scan of LTS+OTS).
Code: `Search/Factor/FactorLane.cs` (lane, scheduler), `Search/Factor/FullSolver.Sub.cs` (sub-engine entry points).

* **Resumable sub-engines**: Go keeps each sub-level's macro/novelty solver alive between caps (`s.resume`) and the fa2 engine paused on a
  baton (it yields every 1024 expansions). C#: `MacroSearch` is now resumable (`mStarted`/`SubRunMacro`, node limit raised per call, deadline
  relative to the call, `MExpired` follows Go's `expired()` — a node limit no longer disables the clock in timed mode); the fa2 engine runs on a
  thread with the lane's `Baton` (yield added at the 1024-expansion mark, `Fa2Runner.RunTo`).
* **Parity** (`bench/lane-parity.py factor "" 20000`, unpatched Go, `-parity` cluster mode): **12/12 gated levels identical** (solved, nodes,
  pushes), incl. Sasquatch_50 #29 (596 nodes) and unsolved 20000-node pools; goldens in `tests/Soksolv.Tests/FactorTests.cs`.
* **Speed** (AOT, equal work, `-nodes 20000`, 8 gated levels): **1.26x Go's total time** (C# 114.9 s vs Go 91.1 s); per level 0.74x–1.34x
  (Microban_155 #80 0.74x, Sasquatch_50 #29 1.13x, Sasquatch_50 #46 1.17x, Sasquatch_IV_50 #47 1.34x: 73.1 s vs 54.6 s). The time is the
  sub-engines (macro/fa2 box search on wide boards), whose speed work is tracked in `docs/macro.md`. Go's peak memory on these levels is
  37-950 MB; the C# column printed 0 MB at measurement time (PeakBytes was not filled; fixed afterwards, not re-measured).
* Portfolio: lane `factor` (not solo) joins gated boards (`PortfolioOptions.Factor`). Not ported: `-factor-alt` (off by default).

## dive lane (`-strategy dive`) — Go dive.go (exp-691/693)

Landmark-phased greedy *backward* search on the pull view: landmarks are real goals that are not start squares; while one holds a box
(E > 0) the search is one zero-weight greedy queue on (fewer E, fewer U, larger matching distance D), then nodes join two FESS-style
return axes (R1: boxes on start squares / least matching distance to the start squares; R2: chain prefix / largest D), every fourth
pop from the dive queue. The default lane runs two engines — base and the "A" dive (strategy A's crowding/box-gap-box terms) — alternating on a baton
(1024-expansion slices, each with half the node budget). Gate: solved-board peel depth of a start-empty goal >= 10 (315 of 13,315
levels); on gated boards the dive replaces the reverse lane (`DiveReplace=reverse`).
Code: `Search/Dive/` — `FullSolver.Dive.cs` (loop, queues, features, pair runner), `DiveAssign.cs` (Hungarian matcher with dual reuse:
`matchPrime`/`matchChild`), `PullFest.cs` (Festival push distances with player access, exact single-box reach via `HubReach`, `pullZoneDead`
root filter). The box search is the macro engine's pull mode (`MBoxSearch` with a dive visitor).

* **Parity** (`bench/lane-parity.py dive "" 20000`, solved/nodes/pushes identical): testdata 22/22, LTS sample 40/40, dive-gated sample 38/40 —
  the two exceptions (Galadriel #29 with 119 boxes, Sokodeal #52 with 212 boxes) never finish their setup within Go's 120 s either
  (Go reports 0 expansions at the deadline); C# needs ~30 s / ~130 s. One quirk mirrored on purpose: Go evaluates strategy A's term for
  the *roots* after clearing the box map (so it is 0 there).
* **Speed/memory** (AOT, equal work `-nodes 20000`, testdata): **0.89x Go's time** (10.1 s vs 11.3 s), mean peak memory 4.9 MB vs 19.2 MB.
* Portfolio: lane `dive` on gated boards (reverse dropped). Not ported: perimeter posts/meetings of the dive's states, the revive ring on
  memory deaths, `-dive-ineff`, `-dive-share`.

# `hf` / toward (thirteenth fork) — `-strategy hf`

Go's `-strategy hf` (exp-188): FESS on the real level ranked against a *target board* — pull for N/3 expansions, take the pull search's best
state (boxes, player, backward tail), then run the macro engine in **target mode** for the remaining 2N/3 (`fess_toward.go`,
`fessTowardReal`), completing with `meet.Complete(target boxes, prefix end, tail)` when the target board is reached.

## What was ported (`Search/Toward/`, hooks in `Search/Macro/*`)
* `FullSolver.Toward.cs`: `SetTarget` (`tgtDist` per target square), `OnTarget`, `HotRel` (hotspots against the target), exact min-cost box->target
  matching (Hungarian; `MatchParent`/`MatchChild` reuse the parent's duals exactly like Go's `incremental_match.go`), `HfFrozenOff`,
  Festival's HF scheduling tuples (`hfOwn`/`hfBest`, `HfBetter`, cell = best-ancestor (regions, boxes on target)), `CompleteTarget`, `BestTarget`
  (pull side) and the standalone driver `FullSolver.SolveHf`.
* `FullSolver.TowardPrune.cs`: Festival's inefficient positions (`festTunnels`, `festInefficient`) and the `DiagDeadChecker` hook (Go default
  `-diag-dead=target`: toward searches only).
* `FullSolver.FestCorral.cs`: Festival's component-growing PI-corral (`corral_fest.go`).
* Engine hooks: target-count `MPacked`, HF cell in `MAdd`, parent/child score overrides + `HfNoReturn` + one-advisor + unweighted-3/4 in `MOpen`/`MVisit`,
  best-so-far orders, target completion in the search loop; no plan / no displacement / no plan4 in this mode (Go `noPlan`, `hfRank`).
* CLI: `-strategy hf` with Go's switches (`-hf-festival=false`, `-hf-no-return`, `-hf-fest-corral`, `-hf-frozen-match`, `-hf-fest-sched`, `-hf-unweighted`,
  `-hf-match-first`, `-hf-one-advisor`, `-hf-best-fest`, `-hf-fest-ineff`, `-diag-dead=false`), `-no-meet`.

## Parity (deterministic `-nodes N`, `-no-meet`; `bench/hf-parity.py`; Go side = `bench/hf-ref/build.sh` patched copy that prints pull/toward stored counts)
Compared: solved, toward nodes, pushes, pull stored, toward stored, target_on_start, target_tail.

| configuration (Go flags) | testdata (19) | LTS sample (40) |
|---|---|---|
| reduced: `-hf-festival=false -diag-dead=false` (target mode alone) | 19/19 | 40/40 |
| `-hf-festival=false` (+ diag-dead) | 19/19 | |
| + `-hf-no-return` / `-hf-frozen-match` / `-hf-match-first` / `-hf-unweighted` / `-hf-one-advisor` / `-hf-best-fest` (each alone) | 19/19 each | |
| + `-hf-fest-sched` | 19/19 | |
| + `-hf-fest-corral` | 19/19 | |
| bundle minus fest-corral (`-hf-fest-corral=false`) | 19/19 | 40/40 |
| **default (all features), N=3000** | **19/19** | **40/40** |
| **default, N=15000** | **19/19** | |

(`-hf-fest-ineff` was only exercised inside the full default bundle runs, which include it.) Go's `hf` is *not* deterministic when a meeting
resolves the level (it races a drain goroutine), hence `-no-meet`; `hf-parity.py` skips levels where two Go runs differ.

## Speed / memory (AOT, equal work: `-nodes 15000`, 19 testdata levels, same machine, best of 2)
Go 9.4 s, C# 8.4 s => **0.90x** (worst levels 1.3x: Microban_155_31, Sasquatch_VI_50_24). Before the incremental matching repair it was 1.02x. Memory:
the C# CLI does not yet report `peak_memory_mb` for `hf` (not measured).

## Not ported
* The portfolio `toward` lane: it is a retargeting loop (`main.go` ~l.930: `RetargetReuse`, `retargetTo`, commitment windows, `towardStop`, `RetainFwdMail`
  previous trees) fed by the pull lane's *live* best state (`meet.Best`) plus meetings, not a standalone search. `hf` is its deterministic standalone form
  and is what is ported; the portfolio keeps its existing lanes (no `toward` lane).
* Meeting (`meet`) hooks inside the toward phase (forward adds / pull drain); `hf` runs `-no-meet` only.
* `hub-pull`, `hub-gen`, `hub`: not started in this fork (they need pullfest `pfOn` (400 lines), `hubrace`/`hubstrata`/`hubgen`/`hub`/`hfsched` (~2.8k lines)
  on top of the toward machinery now available; see the scope table above).

## Hub family — status after the 14th fork (pfOn leaf functions only)

**Determinism finding (blocks exact `-nodes` parity for the hub lanes).** `-strategy hub-pull` and `hub-gen` ignore `-nodes`
(only hubgen.go:858 reads `NodeBudget`); they run on wall-clock windows (`hubUnit(T)`, doubling slices, idle rings, `reviveLoop`), and two
identical runs of the unmodified Go binary expand different node counts (hub-pull 475,136 vs 495,616; hub-gen 139,264 vs 126,976 on
Holland_81_27, `-nodes 3000 -t 20`). Exact fingerprint parity is therefore only possible against a *patched* Go copy that bounds the
solver by expansions and disables the timed windows — i.e. after the engine hooks below exist.

**Ported and verified exactly (commit below):** `Search/Hub/PfFest.cs` = the leaf functions of `pullfest.go`:
`pushDistances` (Festival's player-aware push distances, `-pf-exact-dist` default), the matching-distance table `pfDist`, `pfHot`
(boxes no player can pull), `pfBicon` (free squares on no cycle), `pfSpreadSum`, the `pfTuple` order (`pfBetter`) and the packed
`pfScore`/`pfFromMove` mobility key. Test `PfFestTests.PullFest_LeafFunctions_MatchGo` compares against goldens dumped by Go
(`pfdump_test.go` in a scratch copy of `.reference`, `tests/Soksolv.Tests/golden/pffest.json`): pfDist checksum and non-negative count per
level + `pfHot`/`pfBicon`/`pfSpreadSum` on 8 pseudo-random box sets per level, 18 testdata levels (144 samples): all equal.

**Not ported (engine hooks of `pfOn`, each a branch next to the existing `hfRank` branches in `Macro/FullSolver.Macro*.cs`):**
`pfAdd`/`pfReparent` (node tuples, best-on-path, bucket = `project(oop=best.reg, connectivity=best.rooms)`), root scoring (`pfInit`, `rootBare`),
`open()` parent/child scoring (`pfFill`, `hfMatchParent/Child` dist, `pfScore` into oop/room/hot/mobility), the single Festival advisor
with board-order ties (`pfFestAdv`), `m.b/m.t` sort keys, `pfInefficient` dead reason (ported as `FestInefficient` for hf), start-zone
filtering (`PFReach` exact reach, `PFZoneDead`), `pfBetter` best-so-far in the frontier, type-exploration period `pfSty.types`,
`festRooms` (`-pf-fest-rooms`), revive-diversify skipped for pfOn. Then `hubrace.go` (393), `hubstrata.go` (255), `hubprobe.go` (530),
`festb.go` (214), `hubgen.go` (1037), `hub.go` (1112), `hfsched.go` (58) — about 3.6k Go lines — and the portfolio `toward` retargeting loop.

**Suggested method for the engine hooks:** a patched Go copy (like `bench/hf-ref/build.sh`) that adds `SOKSOLV_PFON=1` to `-strategy pull`
(sets `ps.pfOn=true`, `pfSty=festXStyle()`) so the Festival-order pull search can be fingerprint-compared under `-nodes N`, then `hubstrata`
(deterministic), then the timed `raceAlternate`/`hubLane` with a documented solve-rate/time equivalence.

### pfOn engine hooks — reference built, hook map for the next worker

**Reference:** `bench/pf-ref/build.sh` builds `$SCRATCH/soksolv-pf` from a copy of `.reference` where `SOKSOLV_PFON=1` makes `-strategy pull`
run the pull solver with `pfOn` + `festXStyle()` (exactly how `hub-pull` builds it, main.go:1093). It is deterministic under `-nodes`
(two runs identical) and pfOn changes the search (Holland_81_43 at 2000 nodes: stored 690 / generated 11864 with pfOn vs 1038 / 11038 without).
Compare with `bench/lane-parity.py pull "" N` after pointing GO_SOKSOLV at `soksolv-pf` with SOKSOLV_PFON=1 and adding a C# switch
(suggested: `-pf-on`, set on `FullOptions` as `PfOn`, valid with `Pull`).

**Hook map (Go site → C# analogue; each pfOn branch sits beside the existing hfRank branch):**
| Go | what | C# place |
|---|---|---|
| solver.go:1658 / pullfest.go `pfAdd` | node tuple `pfOwn`, best-on-path `pfBest`, bucket = `project(oop=best.reg, connectivity=best.rooms)`; root scored on its own board (`rootBare`) | `Macro/FullSolver.Macro.cs:253-255` (hfRank bucket/cell) |
| pullfest.go `pfReparent` | reparent keeps best-on-path | `Macro.cs:228` (`HfSchedReparent` analogue) |
| fess_search.go:365 | frontier best-so-far uses `pfBetter(pfBest)` | `Macro.cs:374` |
| fess_moves.go:77-90 | parent score = `pfScore(pfTuple{reg: s.reg[id], rooms: parent.room, dist: hfMatchParent})` filled by `pfFill`; `parent.conn = reg` (pfAdvFix) | `MacroOpen.cs:64-71` (hfRank parent block) |
| fess_moves.go:304-325 | child score = `pfScore` of `{m.r, m.room, dist: hfMatchChild}` + `pfFill`; `rootBare` branch | `MacroOpen.cs:~469` (hfRank child block) |
| advisors.go:104 | single advisor: best child better than parent, ties in board order (`m.b`, `m.t`), skips stored children (`PFFestAdv`) | `MacroOpen.cs:121` (hfRank one-advisor) |
| fess_moves.go:464, 785 | no extra zero-weight advisor under `pfFestAdv`; sort keys `k.x,k.y = m.b,m.t` | `MacroOpen.cs` rev-group weight + sort key code |
| fess_moves.go:948 | `PFIneff && pfOn` → `pfInefficient` dead reason | `MacroOpen.cs:558` (`FestInefficient`, already ported for hf) |
| fess_search.go:535-538 | `PFReach` / `pfZoneDead` start-zone filtering of roots | pull root setup in `Pull/FullSolver.Pull.cs` |
| fess_search.go:670, 826-831 | pfOn type-exploration period `pfSty.types` | Macro types code |
| pullfest.go `pfFill`/`pfMatch`/`pfInit` | tuple completion (hot, bic, spread, rooms=`festRooms` when `PFFestRooms`) | **ported**: `Search/Hub/PfFest.cs` (+ port `festRooms`) |
Ported leaf pieces are exact (PfFestTests). After the hooks: `hubstrata` (deterministic, ~255 lines), then add a node-slice switch to a patched
Go (`SOKSOLV_NODESLICES`: replace the wall-clock slices of `raceAlternate`/`festbAlternate`/`hubLane` by expansion counts) for the timed lanes.


## Hub family — status after the 15th fork: pfOn, strata and `hub-pull` ported (`-strategy hub-pull`)

**Step 1 — pfOn (Festival-order pull), exact.** `Search/Hub/FullSolver.PfOn.cs` (+ hooks in `Macro/FullSolver.Macro*.cs`): node tuples
`pfOwn/pfBest` and bucket = `(best.reg, best.rooms)` (MAdd/dup reparent), `MNoteBest` by `pfBetter`, parent/child scoring
(`MatchParent/MatchChild` against `pfDist` via `tgtDist`, `PfFill` = `festRooms` (Festival's score_rooms, flood version) + spread key + hotspots),
the single advisor (best child better than the parent, ties in board order `b`,`t`, stored children skipped), sort key `(b,t)` instead of `plen`,
`pfInefficient` dead reason, `pfReachInit` (HubReach narrows the start-square matching), `liveZoneRoots` (`pullZoneDead`, 1000 positions),
type exploration every 8th selection (`FessHeap.Remove` = Go `heap.Remove`, xorshift `Rnd`, seed `RngSeed`; hub lanes use `RevSeed(k)`).
Switch: `FullOptions.PfOn` / CLI `-pf-on` (with `-strategy pull`). Reference: `bench/pf-ref/build.sh` (`SOKSOLV_PFON=1`); compare with
`PARITY_FULL=1 GO_SOKSOLV=.../soksolv-pf SOKSOLV_PFON=1 CS_EXTRA=-pf-on bench/lane-parity.py pull "" N [list]` (solved, expanded, pushes, stored, generated,
duplicates):

| N | testdata (22 files) | LTS sample |
|---|---|---|
| 1 | 22/22 | 296/296 (whole sample list) |
| 10 / 100 / 1000 | 22/22 | 42/42 (every 7th level of the sample) |
| 20000 | 22/22 | 42/42 |

(`GrigrSpecial_40 #40` spends > 60 s in setup under pfOn in Go as well — `pushDistances` on its large board; excluded from the sample runs.)

**Step 2 — hub strata, exact.** `FullSolver.Strata.cs`: `Strata` tables (v0 room/goal signature, v1 half-depth ancestor), `StrataOffer` from `MNoteBest`,
`BoxSig`. `-strategy hub-strata -hub-strata-dump F` (+ `-nodes N`) writes the same dump as Go: byte-identical on all 22 testdata files at N=5000
(18 unsolved dumps + 4 levels solved by both), goldens in `tests/Soksolv.Tests/golden/strata_*` (`HubTests`: `HubStrata_DumpMatchesGo`).

**Step 3a — `hub-pull` = `raceAlternate`, exact in node-slice mode.** `FullSolver.HubRace.cs`: `SolveHubPull` (revive ring on `memory` / `festb ended`),
`HubPullConfig` (pull slices, base forward tree aimed at `BestTarget`, strata picking never-tried first then progress per expansion, `RaceTrial` with
budget doubling, process-wide tried-strata table), `NewTowardReal`/`TowardRun`. Timing: **wall clock by default** (festbUnit 250 ms, forward slice = 2 pull
slices, as Go); `-hub-node-slices K` (`FullOptions.HubNodeSlices`) replaces every wall-clock decision by expansion counts (pull slice = K pull expansions, forward
"time" = forward expansions; `-nodes N` caps the lane's total expansions). Reference: `bench/hub-ref/build.sh` (patched `raceAlternate`, env `SOKSOLV_NODESLICES`,
prints `HUBRACE ...`); run Go with `-no-meet -retarget-reuse=false -lane-revive=false` (retarget reuse is not ported: a changed pull best starts a NEW base tree).
`bench/hub-parity.py SLICE NODES [list]` compares solved, pushes and the counters (pull/base/forward expansions, trials, strata, reached, extensions, retries,
shared-table skips, base runs).

Not ported in this step (all Go-side behaviours the C# hub-pull lacks): `retargetTo` reuse of the base forward tree, meetings between hub-pull and the other lanes
(the forward toward search has no `meet` hooks; `Back`/`Fwd` owners 4/5 do not exist), `relayBack` (off by default in Go anyway), the memory elastic budget
(`budgetP` shares: forward trees only count against `BudgetBytes` via `PeerBytes`), Portfolio wiring.

**Engine fix found by the node-slice parity (affects every macro-engine lane run in slices / with a cancelled window):** Go's `deferReopen`/`resumeWindow`
(`ResumeWindowFix`, default on): when a slice (or a node limit) ends in the middle of reopening a node's next move window, the exhausted window survives as a
frontier entry (`move = -2`, weight `w`, `lazyScore`) that reopens it on resume. The C# macro engine dropped such windows (it only mattered for
resumed searches: pull slices, hub trials/base trees; `-nodes` single runs never resume). Ported: `DeferReopen`, `LazyScore`, `OpenFrontierEntry`
(`Macro/FullSolver.Macro.cs`), also in the type-exploration pick. Before the fix hub-pull diverged by +-1 stratum at some slice boundaries; after it matches.

**Wall-clock mode (the default) — what is mirrored from Go:** `festbUnit` = 250 ms pull slices, a forward slice is 2 pull slices (here: forward time >= 2 x the
pull slice before the pull tree runs again; `pullSlice = max(festbUnit, fwdT/2)`), `roundEnd(slice, deadline)` clipping, trial/base budgets in expansions
(these are not wall-clock in Go either), `StallScale = t/600` (set by the CLI/Portfolio for `-t < 600`; stall finishing runs only in toward trees and never
in node-slice mode), memory: every tree counts the lane's other trees (`FullOptions.PeerBytes`) against `BudgetBytes`, lane revive ring (`memory` / `festb ended`
-> fresh pull tree, `RngSeed = RevSeed(k)`).
