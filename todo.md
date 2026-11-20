# Port plan / status

Legend: [x] done, [~] in progress, [ ] todo

## Phase 0 — Setup
- [x] Move Go repo to `.reference/`
- [x] Solution + projects (Core, Cli, Tests, Bench), `Directory.Build.props`
- [x] CLAUDE.md, todo.md
- [x] Install .NET 11 SDK (RC3), Go 1.27 builds reference
- [ ] `Diagnostics.MachineInfo` (CPU/mem/OS/calibration header) used by every benchmark

## Phase 1 — Data-model exploration (small test subset)
- [ ] Select subset `testdata/` (levels solvable by Go `-strategy full` in 0.3–20 s)
- [ ] Level/parse/verify in Core
- [ ] Prototype push-search with pluggable state stores; measure bytes/state, states/s, allocs:
  - [ ] A: bitset-over-floor-cells key (Go layout)
  - [ ] B: sorted ushort box list key
  - [ ] C: hash table layout: open addressing int32 ids + fingerprints vs. embedded keys
  - [ ] D: SIMD key compare / hashing, stackalloc scratch, unmanaged chunked arena
- [ ] Record results + decision in `docs/data-model.md`

## Phase 2 — Core port (`full` lane = best-first FESS over push states)
Dependencies in Go: `reach`, `cluster`, `diagdead`, `sidedead`, `soloracle`, `meet`, `memtrace`, `trace`, `holes`, `arena`
- [ ] Level parser (splitCollection, parseLevel, hole closing) + Verifier (`cmd/verify`)
- [ ] reach, sidedead, diagdead, cluster, deadzone deadlock detectors (+ differential tests)
- [ ] full/solver.go (distances, assignment, freeze/2x2, pair deadlocks, goal macros, corrals, FESS buckets)
- [ ] CLI + JSON output compatible with Go
- [ ] Compare vs Go on subset (correctness + perf), then bigger sets

## Phase 3 — Remaining lanes (from `laneTable` in `.reference/lane.go`)
- [ ] reverse (pull) search — `.reference/reverse/` (3.6k lines)
- [ ] macro (`solver.go`, 2.6k) and advisors
- [ ] novelty, fa2 (+rooms, plan4), hub / hub-gen / hub-pull, toward, pull, dive, factor, relay
- [ ] Portfolio scheduler, memory governor, restarts (`main.go`, `lane.go`, `elastic_mem.go`)

## Phase 4 — Full benchmarks
- [ ] LTS (3,272) and OTS (10,043) vs Go at 600 s / 8 cores (`.reference/benchmark/run.sh` equivalent)
