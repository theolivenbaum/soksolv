
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
