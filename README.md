# soksolv-net

A .NET 11 / modern C# port of the **soksolv** Sokoban solver. The original Go implementation is kept,
untouched, in [`.reference/`](.reference/) (read-only) and is the correctness and performance baseline.

The solver is a portfolio of search lanes that run in parallel on one level: `full`, `reverse`, `macro`,
`novelty`, `pull`, `fa2`, `dive`, `factor`, `toward`, `hub-gen`, `hub-pull` and a forward/backward
`meet` network. The `relay` lane (weight 0 in Go) is not ported yet.

## Build and run

```sh
export PATH=$PATH:/root/.dotnet
bench/publish-aot.sh                       # Native AOT build -> artifacts/aot/Soksolv.Cli (use this for benchmarks)
artifacts/aot/Soksolv.Cli -header -t 10 testdata/some.xsb 3     # whole portfolio, 10 s, level index 3
artifacts/aot/Soksolv.Cli -strategy reverse -nodes 5000 -parity file.xsb
dotnet test                                # 282 tests, incl. differential tests against Go
```

Common flags: `-t <s>` time limit, `-mem <MiB>`, `-nodes <N>` (deterministic node cap, used for parity runs),
`-parity` (reproduce Go's cluster-check search exactly), `-strategy full|reverse|all`, `-header`
(print CPU / cores / RAM / OS / runtime and a calibration score first). JSON output matches the Go CLI where
sensible and adds `alloc_mb`, `gc_gen0`, `gc_gen2`.

Every solution is replayed with `Soksolv.Core.Verifier` (ported from the Go `cmd/verify`).

## Layout

| Path | Contents |
|---|---|
| `src/Soksolv.Core` | level model, parser, verifier, all solver lanes |
| `src/Soksolv.Cli` | the `soksolv` executable |
| `tests/Soksolv.Tests` | xunit tests (ports of the Go tests and golden/differential checks) |
| `bench/` | Go-vs-C# harness (`compare.py`, `fullrun.py`, `ladder.sh`, `lane-cpu.py`, parity scripts), micro-benchmarks |
| `docs/` | `data-model.md`, `lanes.md`, `macro.md`, `reverse.md`, `perf.md` (measurements, what worked, what did not) |
| `todo.md` | port plan and status |

## Key changes vs the Go original

The search algorithms and their tie-breaking are kept identical to Go (node counts match under `-nodes`/`-parity`).
What changed is the data layout and the kernels underneath:

- **State**: bitset over compacted floor cells plus a normalised player (minimum reachable cell), with an
  incremental Zobrist key (a push is two bit flips, so the key and hash are updated, never recomputed).
- **Memory**: search data lives in unmanaged, chunked arenas (no LOH copies when tables grow). The state
  table holds packed 64-bit slots (32-bit fingerprint + id). No allocation per expanded node in any lane.
- **Bitboard-style flood fill**: for boards up to 64 columns the grid is one `ulong` per row and
  reachability, regions and articulation checks run on row words (carry-propagation run fill, dirty-row
  worklists, `tzcnt`), instead of cell-by-cell queues.
- **Cluster deadlock checker**: capped exhaustive proof search per cluster, made faster with A*
  (admissible distance heuristic, same minimal cost), skipping capped box sets, incremental child-region
  normalisation, and bitset matching for feasibility. `-parity` mode reproduces Go's search exactly.
- **Memoisation by box-set hash**: candidate evaluations that depend only on the box set (region/room counts,
  hotspots, plan status, freeze deadlocks) are cached by Zobrist hash; 35-60% of generated candidates are
  transpositions.
- **Novelty lane**: pair tests use a transposed bit-row book (one bit test per box instead of a hash probe).
- **Native AOT** is the benchmark build: no JIT warm-up. Hot paths use `Span<T>`, `stackalloc`,
  `BitOperations` and `Vector128` with scalar fallbacks; no LINQ, delegates or boxing.
- **Portfolio**: one thread per lane with a shared cancel flag, shared cluster tables and the meet network.
  Unlike Go's default portfolio, the C# default also runs the `full` lane.

## Measurements

Every benchmark run prints a machine header (CPU, cores, RAM, OS, runtimes, calibration score) first. Numbers
below are from one 4-core Xeon 2.1 GHz VM, Go 1.24 vs the AOT build, same levels and limits.

### Per lane, equal work (22 testdata levels, 5000 nodes, `-parity`, identical node counts)

| Lane | C# CPU / Go CPU |
|---|---|
| full | 0.25 |
| reverse | 0.40 |
| hf | 0.44 |
| dive | 0.45 |
| novelty | 0.45 |
| macro | 0.51 |
| pull | 0.51 |
| fa2 | 0.56 |

### 10 s sweep vs a restricted Go (NOT the Go default portfolio)

Correction: this sweep compared the C# portfolio against Go run with only its `full` and `reverse` lanes
(one thread each, 6000 MiB), the `go2` mode of `bench/fullrun.py`, not against Go's default all-lane
portfolio. It is therefore **not** a like-for-like comparison with Go and should not be quoted as one. It
only shows how the C# portfolio compares to those two Go lanes on a 4-core VM. All 9771 distinct boards,
10 s each, every solution verified; full output in [`docs/full-benchmark.md`](docs/full-benchmark.md).

| | Go (full + reverse lanes only) | C# (portfolio) |
|---|---|---|
| Solved (of 9771) | 7049 | 8735 |
| Solved by only this solver | 8 | 1694 |
| Time on the 7041 boards both solved | 4592 s | 1475 s |

### 600 s run on the settings of `.reference/benchmark` (all 13315 levels, every solution verified)

600 s per level, 15 GB, 196 batches, C# default portfolio on a 4-core Xeon VM, compared with the published Go
results (8 pinned EPYC cores, so times are not comparable; solved counts are). Details, the lists of levels only one
side solved and the same-VM Go run are in [`docs/benchmark-600s.md`](docs/benchmark-600s.md).

| Suite | Levels | Go (published) | C# | Both | Only Go | Only C# |
|---|---|---|---|---|---|---|
| LTS | 3272 | 3218 | 3198 | 3194 | 24 | 4 |
| OTS | 10043 | 9620 | 9542 | 9530 | 90 | 12 |
| All | 13315 | 12838 | 12740 | 12724 | 114 | 16 |

C# solves 98 fewer levels (0.7 %) than the published Go results, mostly in the hardest sets (Sven_1623, Sasquatch).
The Go default portfolio on this same VM is being measured to separate hardware from algorithmic differences.

## Status

See [`todo.md`](todo.md). Not yet done: the `relay` lane, some toward-lane scheduling details, an arena for
`macros`/`nodeMacros`. Details and negative results are in [`docs/perf.md`](docs/perf.md).
