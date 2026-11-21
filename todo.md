# Port plan / status

Legend: [x] done, [~] in progress, [ ] todo

## Phase 0 — Setup
- [x] Move Go repo to `.reference/`
- [x] Solution + projects (Core, Cli, Tests, Bench), `Directory.Build.props`
- [x] CLAUDE.md, todo.md
- [x] Install .NET 11 SDK (RC3), Go 1.27 builds reference
- [x] `Diagnostics.MachineInfo` (CPU/mem/OS/calibration header) used by every benchmark

## Phase 1 — Data-model exploration (small test subset) — DONE, see docs/data-model.md
- [x] Subset `testdata/` (18 levels, manifest.tsv)
- [x] Level/parse/verify/MachineInfo in Core
- [x] State store variants (V1 Go-like / V2 unmanaged AoS+packed slots / V3 list key) → V2
- [x] Bitboards: flood (no win), push-gen (~1.15x, optional later), key as floor-bitset (adopted)
- [x] Zobrist + incremental key/hash (6-60x cheaper per child) → adopted
- [ ] (later) bitboard frozen/2x2/dead-push masks for W<=64; SIMD key compare for nw>=4

## Phase 2 — Core port (`full` lane = best-first FESS over push states)
Dependencies in Go: `reach`, `cluster`, `diagdead`, `sidedead`, `soloracle`, `meet`, `memtrace`, `trace`, `holes`, `arena`
- [ ] Level parser (splitCollection, parseLevel, hole closing) + Verifier (`cmd/verify`)
- [ ] reach, sidedead, diagdead, cluster, deadzone deadlock detectors (+ differential tests)
- [ ] full/solver.go (distances, assignment, freeze/2x2, pair deadlocks, goal macros, corrals, FESS buckets)
- [x] full/solver.go ported (distances, assignment, freeze/2x2, pair, goal-area, goal macros, PI-corral, FESS buckets, side-dead, cluster+penalties); node counts identical to Go on 18-level subset
- [x] CLI + JSON output compatible with Go (-t -mem -nodes -header)
- [x] circular levels: `Circular.cs` (in-process first-push search, doubling slices) + tests; [ ] hook into CLI dispatch (Program.cs is owned by the reverse-lane work for now)
- [ ] Compare vs Go on subset (correctness + perf), then bigger sets

- [x] Native AOT build + compare harness uses it: 18/18 identical to Go, 0.85x Go's time (JIT build was 1.02x)
- [ ] Re-run node-capped 297-level LTS sample (-nodes 5000) to hunt divergences (first attempt aborted: Sasquatch_VI#50 slow)
- [x] Cluster checker: A*, capped-set skipping, incremental region normalisation (0.46x Go time, +5 solves on Go-unsolved set); `-parity` keeps Go-identical mode (0.75x)
- [x] Incremental child region counts (articulation) in FullSolver (exact; ~7% faster fast mode)
- [ ] Remaining hot spots (SokHard_36 profile): Regions() 12%, piCorral 9%, pairDeadlock 6%, apply 13% — carry region bitmaps / incremental region count; bitboard pair offsets

- [x] macro 6b deadzone+frontier corral, 6c displacement (exact parity, goldens in MacroTests)

- [x] macro 6d corridor flow, 6e packing obligations, StallFinish/StallScale; macro lane default = stage 5 + features 31 (CLI + portfolio)
- [ ] macro 6f plan4 (planner2/planner4/packing_fest + anytime + restart), 6g macroalt; word-parallel boxflood/moveaccess/regionsChild/pairbits for wide levels

## Phase 3 — Remaining lanes (from `laneTable` in `.reference/lane.go`)
- [x] reverse (pull) search — default config incl. supply, novelty, types, walks, restart ring (see docs/reverse.md): exact vs Go on 18 testdata + 296/297 LTS sample; 0.9x Go time, ~half memory
- [ ] reverse: `Match`/`Free` heuristics, Root/RootBoxes (toward/relay), `-rev-supply-inherit` validation, delta-coded node store
- [ ] macro lane: scoped in docs/macro.md (≈7.9k executed Go lines, 35 files; `bench/macro-scope.sh`); staged port order 1–6 there. Unported experiment flags: plan-v2, plan-file, packed-festorder, diag-dead, seed/novelty (exp-192/194/195), inefficient, hf-* (toward lane), fx426/fx477 families
- [ ] novelty, fa2 (+rooms, plan4), hub / hub-gen / hub-pull, toward, pull, dive, factor, relay
- [ ] Portfolio scheduler, memory governor, restarts (`main.go`, `lane.go`, `elastic_mem.go`)

## Phase 4 — Full benchmarks
- [ ] LTS (3,272) and OTS (10,043) vs Go at 600 s / 8 cores (`.reference/benchmark/run.sh` equivalent)

### Macro lane staged port (docs/macro.md)
- [x] Stage 1: core push/macro-move machinery + frontier (packed, connectivity), advisors 1-2, deadlock pipeline, reconstruct — exact fingerprint parity with patched Go reference (66 levels; 100k-node caps), 0.91x Go time (AOT)
- [x] Stage 2: rooms + basin (room term/advisor, OOP term/advisor) — exact parity: 18 testdata + 42 LTS @20k, Holland_27 @1/10/100/1000 (cutscan belongs to Stage 3)
- [x] Stage 3: sink packing plan (planner + plan queries; cutscan replaced by floods) — exact parity (18 testdata + 42 LTS @20k)
- [x] Stage 4: hotspots (hot term, hotspot + opener advisors) — exact parity (18 + 42 @20k)
- [x] Stage 5: move access (mobility, explorer advisor) — exact parity (18 + 42 @20k)
- [x] Stage 6a: move windows + reopen (exact parity 18 + 42 @20k); lazy children = perf only, not ported
- [ ] Stage 6b: deadzone corral proofs (Search/DeadZone, reusable) + frontier corral   - [ ] 6c displacement landmarks   - [ ] 6d corridor flow
- [ ] 6e packing obligations/causal   - [ ] 6f plan4 (+ anytime ring)   - [ ] 6g alt engine, rev-groups, novelty hooks, pair-side
- [x] macro lane in Portfolio (stages 1-5 + 6a); [ ] switch to full default config once 6b-6g land
- [ ] Stage-1 speed tuning (HashOf per expansion, first-candidate Regions flood, apply path BFS cache)

## Hub lanes (hub-gen / hub-pull / hub) — see docs/lanes.md (Hub lane family)
- [x] HubReach (hubreach.go) ported, exact vs Go goldens (HubTests)
- [ ] BLOCKER: pull-direction mode of the C# macro engine (+ `-strategy pull` parity), then pullfest/meet/festineff
- [ ] hub-pull (hubrace/hubstrata), hub-gen/hub (hubgen/hub/toward/hfsched), portfolio lanes

## Lanes pull / novelty / toward (seventh fork)
- [x] pull: exact parity 59/59 (testdata+LTS sample), 0.93x Go time, -strategy pull, Portfolio (opt-in)
- [x] novelty: exact parity 59/59, 0.96x Go time, -strategy novelty, Portfolio (default on)
- [ ] toward/tfwd/hf: needs target-board ranking (hfRank/tgt*), festCorral, diagdead, festIneff, meet tables — see docs/lanes.md
- [ ] pull: perimeter publishing/meetings, pfOn (Festival-order), revive restarts; wide-level word-parallel box search speed


## Macro lane — final state (see docs/macro.md)
- [x] stages 1-5, 6a-6g with exact parity (fingerprint + end-to-end vs unpatched Go on testdata; LTS-42 41/41 + 1 skip)
- [ ] speed: per-candidate open() on wide levels (~2x Go), plan4 box loop on 50+-box levels (~2.5x Go): word-parallel goal-area/move-access/hot rows, LazyChild, incremental roomConnChild
- [ ] memory-governed revive ring + elastic memory for the macro lane (portfolio)

## Meetings / toward / hub (tenth fork)
- [x] meet network (Table/Meeting/Complete/Target) + hooks in full, macro, novelty, reverse, pull lanes; portfolio `Meet` option, `-no-meet`
- [x] `DiagDead` port (toward-family prune) — unit goldens still to add
- [ ] macro-engine target mode (setTarget/hotRel/hfMatch/onTarget/completeTarget) + Hungarian -> `toward`/`hf` (parity vs `-strategy hf -nodes N`)
- [ ] `Best` target publishing from pull (`publishPullBest`) and reverse lanes -> `toward` portfolio lane
- [ ] pfOn (Festival-order pull), hubrace/hubstrata -> `hub-pull`; hubgen/hub/hfsched -> `hub-gen`/`hub`
- [ ] candidate-ref meetings (pendCands/refGet) for lazily generated children

## fa2 lane (docs/lanes.md)
- [x] engine core + OOP zones + advisors + edge windows (exact vs Go), rooms score (full recompute), hot-sides, Hall gate, planner v2: default config 19/19 testdata, 40/40 LTS sample identical at 20k nodes
- [x] `-strategy fa2`, portfolio lane (`-no-fa2` to drop), Fa2Tests
- [ ] incremental room-pair score (fa2rooms.go) — ~17% of fa2 time on wide levels
- [ ] fa2 anytime lane (second engine on the macro lane's plan4 list) for the portfolio
- [ ] unported fa2 experiment flags: v2ng/old plan variants in portfolio, hall-sup, plan-file/replay/stream engines

- [x] factor lane (resumable macro/novelty/fa2/reverse sub-engines): exact parity 12/12 gated levels; gate 224/13,315 levels; portfolio lane. Speed 1.12-1.17x Go.
- [x] dive lane (pull-view landmark dive, paired base+A engines, Hungarian matcher, HubReach/zone-dead roots): exact parity 22/22 + 40/40 + 38/40 (2 setup-timeouts in Go); gate 315/13,315 levels; replaces reverse on gated boards. Speed 0.89x Go.
- [ ] dive: perimeter meetings, revive ring; factor: -factor-alt; both: timed-mode ladder runs on the gated levels
- [x] Macro wide-level speed: 1.22x -> 1.05x Go CPU on 12 wide levels (hot rows/columns, bit box search + moveAccessBits, plan4 radix sort, goal-area memo); remaining: CutQueryFill/Expand4 constants, displacement, region counts

## toward / hf (thirteenth fork)
- [x] target mode in the macro engine, `-strategy hf`: exact vs Go (19/19 testdata, 40/40 LTS at 3k nodes; 19/19 at 15k), 0.90x Go equal-work time
- [ ] portfolio `toward` lane (retarget loop with the live pull best state + meetings), `-strategy toward|tfwd`
- [ ] hub-pull / hub-gen / hub (pullfest pfOn, hubrace, hubstrata, hubgen, hub, hfsched)
- [ ] `hf` peak-memory reporting; meetings inside the toward phase

- [x] pullfest leaf functions (pushDistances/pfDist/pfHot/pfBicon/pfSpreadSum/tuple order): Search/Hub/PfFest.cs, exact vs Go goldens (18 levels x 8 samples)
- [ ] pfOn engine hooks in the macro/pull engine (patched-Go `-strategy pull` + SOKSOLV_PFON reference), then hubstrata, hubrace, hubprobe, festb, hubgen, hub, hfsched, portfolio hub lanes + toward retargeting loop (see docs/lanes.md 'Hub family - status after the 14th fork')
