
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
* **Speed** (AOT, equal work, `-nodes 20000`, gated levels): 1.12–1.17x Go's time (Sasquatch_50 #29: 3.9 s vs 3.4 s; #46: 16.4 s vs 14.0 s) —
  the time is the sub-engines (macro/fa2 box search on wide boards), whose speed work is tracked in `docs/macro.md`.
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
