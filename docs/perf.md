# Performance notes

Method for every number below: Native AOT build, `nice -n 10`, **CPU time (user+sys) of the child process**, best of 2-3, Go and C#
run back-to-back on the same shared 4-core VM (+-10% noise is normal), equal work (`-nodes N`, `-procs 1` for Go, `-parity` for C# so both
run the same Go-identical cluster proofs; node/stored/generated/duplicate counts are identical on every testdata level).
Tools: `bench/lane-parity.py` (vs unpatched Go), `bench/lane-timing.py`, `bench/alloctrace` (allocation tracer, below).

## core primitives (StateTable, Storage, ClusterChecker, GoalArea, SideDead, FullSolver `full` lane + flood kernels)

### Result: `full` lane, `-nodes 20000 -parity`, CPU seconds (Go : C#)

| level | Go | C# before | C# after | after/Go |
|---|---|---|---|---|
| Holland_81_57 | 1.9 | 1.41 | 0.65 | 0.36 |
| SokHard_163_36 | 2.3 | 1.73 | 0.68 | 0.30 |
| Aymeric_Du_Peloux_282_273 | 4.9 | 3.58 | 1.7 | 0.35 |
| Dive_Sven_1623_197 | 7.3 | 4.27 | 2.6 | 0.38 |
| Sasquatch_VI_50_24 | 1.6 | 1.32 | 0.71 | 0.43 |
| 22-level testdata total | 35.2 | 24.3 (0.69x) | see bottom | ~0.37x |

Allocation: the 20k-node runs used to allocate 14-460 MB in the AOT build (`alloc_mb`); now 5-8 MB total (solver construction), and
the steady state is 0 B/node of garbage. What still grows with the node count is *retained* data only (the `macros` / `nodeMacros`
dictionaries and their `Push[]` arrays, ~10-50 B per node on levels that have goal macros) - an arena for them is a todo.

### What won (all exact: identical counts vs Go and vs the pre-change binary on all testdata levels, 3 lanes)

1. **`Span<int>.Sort()` allocates under Native AOT** (it builds a `Comparison<int>` delegate per call; the JIT stack-allocates it, ILC does not).
   The cluster proof search sorted every child: ~23 KB of garbage per expanded node and ~9% of the time. Replaced by `MoveSorted`
   (an insertion move in a sorted array) / `InsertionSort`. *Lesson: measure allocations on the AOT binary (`alloc_mb`), not on the JIT.*
   (Other lanes still call `Array.Sort`/`Span.Sort` in per-node paths: DeadZoneChecker.cs:694/781, FestCorral.cs:95, Toward.cs:37.)
2. **Bit-row region kernels** (boards <= 64x64): free cells as one `ulong` per grid row, flood with a *dirty-row bitmask worklist*
   (`tzcnt` picks the next row to relax; a row is re-queued only when it gained a bit that a neighbour row can still absorb) instead of
   a queue flood or whole-board up/down sweeps. Used by `ClusterChecker.FloodBits` (player region of every proof-search state), its
   pocket floods, `FullSolver.RegionsBits` (region counts) and `FloodRows` (pair/macro word-parallel search). Queue floods are
   cache-miss/branch bound (stamp array + blocked array + queue); the row flood is ~1.4k instructions for a 20-row board and
   region membership / minimum cell / population count become single word operations. This alone took the cluster-dominated levels
   from 0.69x to ~0.45x of Go. The earlier data-model experiment (docs/data-model.md section 2) found no win for a *generic* flood; the win
   here comes from the dirty worklist plus keeping `free`/`reg` rows resident so that the proof search never touches the byte maps in its inner loop.
3. **Ring-split table**: the 8-neighbour "can removing this cell split the region" test of the incremental child normaliser reads the
   three row words (shift + mask), builds the 8-bit ring and looks it up in a 256-entry table (was a 8-iteration branchy loop).
4. `PackedCount` (per stored child) as popcounts over a goal-occupancy bitset and per-round masks (was two loops over all goals).
5. Region count of the *first* candidate push of each box is deferred: a second candidate yields it for free from the articulation pass
   that is computed anyway; a lone candidate pays one flood. (Equal values, ConnAdvisor order preserved by taking the minimum afterwards.)
6. `ComputeArticulation` on pointers (`fixed`, unrolled neighbour offsets) - used by full, macro and fa2 for 2nd+ candidate pushes.
7. Zero-garbage `BoxPath`/`MacroMoves` (scratch queue, exact-size result arrays).
8. `-p:IlcInstructionSet=native` gave ~2% (noise level); `x86-x64-v3` is not an accepted spelling in this ILC. Not adopted (portability).

### What did not matter / not done
* State table (`StateTable.Find/Insert/Grow`, `HashOfKey`), heaps and move lists are < 6% of the `full` profile (cluster proofs + region
  floods are > 80%), so prefetch-batched dedupe, SoA record splits, 8/16-bit key encodings and bucket queues were not pursued: nothing to win
  at this profile; revisit when a lane becomes table-bound (very long runs).
* Queue floods stay as the fallback for boards wider than 64 columns or taller than 64 rows.
* Early-exit connectivity test for the ring-split fallback floods (~half of the proof-search floods) and storing/deriving child regions
  from the parent's rows (would remove the per-state expansion flood) were designed but not implemented: estimated < 10% each.

### Tools
* `bench/alloctrace` (JIT build): runs a lane for N and 2N nodes, prints steady B/node and the GC `AllocationTick` types
  (`dotnet artifacts/alloctrace/alloctrace.dll testdata/X.xsb 10000 -parity [macro|novelty|pull]`; `SOKSOLV_CHECK=1` turns on the
  incremental-region / packed-count self checks).
* Pitfall found on the way: `GC.AllocateArray(pinned: true)` arrays referenced only through a raw pointer are *collected* (heap
  corruption, silent wrong results). Keep a field reference to every pinned array whose address you cache.
