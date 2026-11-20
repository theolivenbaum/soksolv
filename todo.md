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
