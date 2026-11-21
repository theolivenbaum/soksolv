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

## Speed / memory (Native AOT build vs Go, single lane, same machine; details and method in docs/perf.md section "reverse")

Equal-work CPU time (user+sys, `bench/lane-cpu.py`, `-nodes N`, best of 2-3): **0.46x Go** on the 22-level testdata
(`-nodes 100000`, default lane) and 0.45x with `-rev-lanes 1`; **0.38x** on a 43-level LTS sample (`-nodes 20000`, both lane
configurations). Before the perf pass: 0.92-0.96x (testdata) / 0.9-1.0x (LTS). Callgrind instruction counts at 10k nodes fell
2.0-5.2x (Holland_81_27 689M -> 236M, Dive_Sven_1623_197 1612M -> 311M).

Memory: peak tracked MB 10-18% lower at equal node counts (Holland_81_27 @200k: 47.8 -> 36 MB); the garbage heap is flat
(`alloc_mb` 3.3 MB at 200k nodes, 3.7 MB at 800k; it grew by ~60-90 B/node before: 62 MB at 400k).

Profile (default lane, 20k nodes, instruction share, all testdata levels): supply U evaluation (one-word matching, slot lookup,
SCC memo) 11%, placement-macro graph (DFS 9% + lazy piece labels 6% + BFS 5%), pull children (EmitChild + PullChildren + AddNew)
14%, region floods for duplicate tests and pops 8%, queues/store 12%.

## Data layout and algorithms (src/Soksolv.Core/Search/Reverse/)

* **Supply graph slots** (`ReverseSolver.Supply.cs`): the DFS forest / Tarjan lowpoints of the free cells for one obstacle set (the
  start squares occupied by boxes; `f`) live in one unmanaged block per slot, indexed by compact floor index (`Disc/End/Par/Top/Cn`
  as `ushort`, `Cn` = four 4-bit canonical piece ids per cell, SCC memo `SccOf` + growable `SccR` reach sets, ~18 B per floor cell).
  Slots sit in a 4-way set-associative table with LRU replacement (budget 24 MiB). The Go design (direct mapped, 512K entry
  lossy reach cache) rebuilt a graph for 19 of 20 evaluations on Holland_81_27 (56k builds for 2.9k distinct `f` @100k nodes); now
  one build per distinct `f` (2.9k) and no separate cache. Piece ids are *canonical* (lowest neighbour direction of the piece) so they
  do not depend on the DFS root; results are identical to Go's (parity).
* **Placement macros** build a scratch graph of the real state (all boxes obstacles) with *lazy* labels (only cells the pull BFS
  touches) and are skipped when no off-start box has a legal first pull (10-20% of calls).
* **Lazy player normalisation**: without a meeting network the stored player is any cell of the region and the Zobrist hash covers the
  boxes only; a probe hit on equal boxes asks "is the stored player in my region" with an early-exit flood (`ReachTo`) instead of
  flooding every child to find its minimum cell (floods per expansion: ~5 -> ~2). With the meeting network (portfolio) the eager,
  normalised path is kept (the forward lanes key their tables by the normalised player).
* Pops of supply-deferred nodes (50% of all pops) skip the region flood and the feasibility check; matching uses start-square bitsets.
* All per-node arrays (types queue, expanded bits, supply values, slots) are unmanaged; the child box list is only materialised when
  the novelty scan or the meeting network needs it.

## Notes

* The supply graph slots and SCC memos are pure memos (results never depend on them); there is no separate reach cache any more.
  Boards with more than 12000 floor cells (`ushort` indices) run without supply ordering.
* Memory-restart ring (`ReverseRunner`): a phase that ends with "memory" restarts with perturbation phase+1 (key8 offsets),
  with Go's quick-failure backoff; verified to survive `-mem 60` runs, solutions verified. Exact trajectories after a restart
  depend on memory/timing, so they are not comparable bit for bit.
