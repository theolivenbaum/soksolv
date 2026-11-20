# Reverse (pull) lane port

Go: `.reference/reverse/` (+ `explore/` Types). C#: `src/Soksolv.Core/Search/Reverse/`
(`ReverseSolver.cs` main loop, `ReverseSolver.Supply.cs` supply ordering, `RevStore.cs` node store + hash table,
`RevQueues.cs` bucket / cell / type queues, `ReverseRunner.cs` restart ring). CLI: `-strategy reverse [-rev-lanes 1|2] [-rev-supply W]`.

## What is ported (all stages exact vs Go)

| stage | Go configuration | status |
|---|---|---|
| A | `-strategy reverse -rev-lanes 1 -rev-supply 0` (greedy bucket queue by h, FESS cell scheduling, key8 perturbation, canReach / inherits / matching prune) | exact |
| B | `-strategy reverse -rev-lanes 1` (supply-aware ordering U, deferral, placement macros; exp-707, default W=4) | exact |
| C | `-strategy reverse` default (lane 0 of the portfolio: + novelty queue, type queue, random walks, connected-best, restart ring with perturbation phases) | exact (single phase); ring ported, not bit-comparable (memory-timing dependent) |

Not ported (not on the default path): `Match` (Hungarian) and `Free` (free-start) heuristics, `KeepBest`/`Closest`,
`Root`/`RootBoxes`, supply-inherit (`-rev-supply-inherit`, kept as an option but unvalidated), meet/perimeter mailboxes,
the elastic memory pool, trace. The Go `bitReach` row-bitset flood (exp-415) is replaced by the stamp-queue flood
(measured earlier: no consistent win for bitboard floods, see docs/data-model.md).

## Parity (deterministic `-nodes` mode; Go's cell scheduling switches on wall clock otherwise)

Same machine, Go binary built from `.reference`, `bench/compare.py` (prints CPU / RAM / calibration first).

* 18 testdata levels, `-nodes 200000` and `-nodes 400000`, three configurations: **identical `nodes_expanded` and
  `pushes` on 18/18**, including levels solved and levels capped.
* 297 sampled Large-Test-Suite levels (every 11th), `-nodes 100000`, three configurations: **296/297 identical**; the only
  difference is `GrigrSpecial_40#40`, a level that starts solved (Go's `main` handles these in `circular.go`; not ported yet,
  C# reports "solution has no push").
* Golden node counts from Go are pinned in `tests/Soksolv.Tests/ReverseTests.cs`.

## Speed / memory (Native AOT build vs Go, single lane, same machine)

| run | Go | C# | ratio |
|---|---|---|---|
| 18 levels, A (supply 0), `-nodes 200000` | 0.4 s common-solved | 0.3 s | 0.82 |
| 18 levels, default lane, `-nodes 400000` | 3.6 s | 3.3 s | **0.92** |
| 297 LTS sample, A, `-nodes 100000` | 4.8 s | 4.3 s | 0.90 |
| 297 LTS sample, B, `-nodes 100000` | 14.6 s | 14.6 s | 1.00 |

Memory (reported tracked bytes) is 35-60% lower than Go on the long runs (e.g. Holland_81_27 at 400k nodes: 75 MB vs 113 MB,
Sasquatch_IV_50_42: 39 vs 74 MB): unmanaged record arena (header + key words inline), packed hash slots with Zobrist
incremental hashing (a pull flips two bits), off-heap int bucket lists, pinned pointer flood kernels.

Profile (default lane, Holland_81_27): supply U evaluation ~42% (graph DFS ~20%, Tarjan closure ~14%), pull loop ~25%,
placement macros ~12%, flood ~14%. Go spends the same share in `supply.dfs` (22% self); the supply code is the ceiling for
this lane. Ideas not done: more/larger graph slots, incremental forest updates, delta-coded nodes (Go's `Compact` store keeps a
full key only every 4th node; our plain records are already smaller than Go's compact store on these boards).

## Notes

* The supply reach cache and graph slots are pure memos (results never depend on them): the C# cache starts at 4K entries and
  doubles with misses up to Go's 512K (saves 20 MB on small levels).
* Memory-restart ring (`ReverseRunner`): a phase that ends with "memory" restarts with perturbation phase+1 (key8 offsets),
  with Go's quick-failure backoff; verified to survive `-mem 60` runs, solutions verified. Exact trajectories after a restart
  depend on memory/timing, so they are not comparable bit for bit.
