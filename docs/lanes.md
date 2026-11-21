
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
