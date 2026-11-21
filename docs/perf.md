# Performance notes

Method for every number below: Native AOT build, `nice -n 10`, **CPU time (user+sys) of the child process**, best of 2-3, Go and C#
run back-to-back on the same shared 4-core VM (+-10% noise is normal), equal work (`-nodes N`, `-procs 1` for Go, `-parity` for C# so both
run the same Go-identical cluster proofs; node/stored/generated/duplicate counts are identical on every testdata level).
Tools: `bench/lane-parity.py` (vs unpatched Go), `bench/lane-timing.py`, `bench/alloctrace` (allocation tracer, below).

## core primitives (StateTable, Storage, ClusterChecker, GoalArea, SideDead, FullSolver `full` lane + flood kernels)

### Result: `full` lane, `-nodes 20000 -parity`, CPU seconds (Go : C#)

| level | Go | C# before | C# after | after/Go |
|---|---|---|---|---|
| Holland_81_57 | 1.9 | 1.41 | 0.65-0.77 | 0.36-0.43 |
| SokHard_163_36 | 2.3 | 1.73 | 0.68 | 0.30 |
| Aymeric_Du_Peloux_282_273 | 4.9 | 3.58 | 1.7 | 0.35 |
| Dive_Sven_1623_197 | 7.3 | 4.27 | 2.6 | 0.38 |
| Sasquatch_VI_50_24 | 1.6 | 1.32 | 0.71 | 0.43 |
| 24-level testdata total (CPU s) | 35.0 | 24.3 (0.69x) | 12.5 | 0.36 |

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

## reverse (reverse/pull lane: `Search/Reverse/*`, `Search/Meet/*`)

Method as above, plus a deterministic second metric: callgrind instruction counts of the AOT binary
(`valgrind --tool=callgrind`, `DOTNET_TieredCompilation=0`; Go does not run under valgrind) - immune to the load of the shared VM and
the tool that found everything below (`--dump-instr=yes` + `objdump` gives per-instruction counts of one function).
Equal work = `-nodes N` (`-procs 1`, Go default lanes = our default `-rev-lanes 2`, supply 4). `bench/lane-cpu.py reverse N R set` prints CPU s.

### Result (CPU seconds, best of 3 / 2; Go : C# before : C# after)

| set | Go | C# before | C# after | before/Go | after/Go |
|---|---|---|---|---|---|
| testdata 22 levels, default lane, `-nodes 100000` | 6.52 | 6.40 | 2.96 | 0.98 | **0.46** |
| testdata 22 levels, `-rev-lanes 1`, `-nodes 100000` | 5.72 | 5.21 | 2.51 | 0.91 | **0.45** |
| LTS sample 43 levels (every 7th of the 297-level parity sample), default lane, `-nodes 20000` | 7.69 | 8.51 | 2.99 | 1.11 | **0.38** |
| same, `-rev-lanes 1` | 7.66 | 8.32 | 2.80 | 1.09 | **0.38** |

Per level (testdata, default lane, 100k nodes, after/Go): Dive_Sven_1623_197 0.33, Sasquatch_IV_50_42 0.39, Holland_81_27 0.43,
Grigr2002_40_24 0.44, SokEvo_107_55 0.46, Sasquatch_VI_50_24 0.48, SokHard_163_13/23/36 0.48-0.51, Aymeric_282_273 0.51,
Factor_SasquatchII_50_24 0.58 (placement-macro heavy), GrigrSpecial_40_3 0.58, Holland_81_57 0.59 (the < 0.1 s levels are dominated by process start-up).

Instruction counts at 10k nodes (before -> after): Holland_81_27 689M -> 236M, Factor_SasquatchII_50_24 546M -> 261M,
Dive_Sven_1623_197 1612M -> 311M, SokHard_163_13 282M -> 138M, Sasquatch_IV_50_42 273M -> 126M.

Parity (all exact, identical solved / nodes_expanded / pushes vs the unpatched Go binary, `bench/lane-parity.py reverse`):
testdata 24/24 at `-nodes 200000` (default lane and `-rev-lanes 1`) and 24/24 `-rev-supply 0` at 100000; LTS sample 297/297 at 100000 (default lane) and 297/297 at 50000 (`-rev-lanes 1`); `dotnet test` green (261 + new capped-run / allocation tests).

### Allocation / memory

`alloc_mb` (GC heap, AOT) is flat in the node count: Holland_81_27 3.3 MB @200k nodes, 3.7 MB @800k (the remainder is the bounded
bucket/type tables); before 30.0 MB @100k, 62.1 MB @400k (~60-90 B/node: managed `List` type queue, `Array.Resize` of the expanded
bits / supply values, one managed object per supply slot, 20 MB lossy reach cache). Everything per node is unmanaged now.
Peak tracked MB at equal node counts (before -> after): Holland_81_27 @100k 28.6 -> 22.0 (-23%), @400k 75.5 -> 63.7 (-16%),
Sasquatch_IV_50_42 @100k 13.9 -> 9.5, @400k 39.3 -> 32.9, Dive_Sven_1623_197 @100k 31.1 -> 26.7, @400k 84.5 -> 78.5 (-7%; 4-word keys).
The type queue lost its payload (the node's h is the h of its type): -4 B/node. Remaining per node (Holland, 1-word keys):
record 16, table ~12, queues 5+6+4, supply values 1.

### What won

1. **Supply slots** (see docs/reverse.md): compact unmanaged floor-indexed graph blocks, 4-way LRU slot cache keyed by the occupied
   start squares, canonical piece ids, per-slot SCC memo with single-pass Tarjan (partial reach sets accumulated along the DFS).
   The Go layout (direct mapped 512 slots) rebuilt the DFS forest for 19 of 20 evaluations (56k builds for 2.9k distinct keys);
   supply U went from 6.9 us to 1.2 us per evaluation (-83%, section timers on), the whole run 0.96x -> 0.70x Go on its own.
2. **Lazy player normalisation**: hash and key cover the boxes only; the stored player is any cell of the region; equal-box probe hits
   test "stored player in my region" with an early-exit flood. Floods per expanded node 5 -> 2; no per-child `Normalize`/ring test.
   Exact: the identity of a state is still (boxes, region), only its representative cell differs.
3. **Deferred flood**: a pop that ends in a supply deferral (50% of pops) never needs the region; the flood moved behind the deferral.
   Marked (second) pops also skip the feasibility check.
4. **Bitset matching**: `Feasible`/`Inherits` on per-cell start-square masks, supply U for <= 64 boxes in one word (recursion on a
   `seen` mask, no stamps). `[SkipLocalsInit]` on the stackalloc users.
5. **Placement macros**: lazy piece labels on the scratch graph, "no legal first pull -> no graph" pre-check (10-20% of calls), cut-vertex
   label fast paths (non-articulation and two-neighbour cells), `Reset` clears only what the graph reads.
6. **Pointer kernels, `nint` indices, hoisted pointers**: AOT reloads `readonly` instance fields and sign-extends every `int` index
   (`movsxd`) inside loops; floods/DFS/BFS with local pointers and `nint` cell indices were 10-25% fewer instructions each.
   The big `Search` method also spilled; extracting `PullChildren`/`EmitChild` removed the spills (-35% for the loop).
7. **Child box list only on demand** (novelty scan when the at-home count changes, meeting network); the novelty scan of a child
   whose count did not change checks the one moved box. Bucket queue with a non-empty bitmap (was a linear scan over empty buckets).
8. **Hash-slot prefetch**: the candidate children of an expansion are enumerated first and their table slots prefetched (`Sse.Prefetch0`)
   before the probes: -6..10% CPU at 400k nodes (no change below ~100k, where the table is cache resident). Prefetching the next
   popped record: no gain.
9. `TypeQueue`, expanded bits, supply values, supply slots on unmanaged memory (allocation-free steady state);
   `Table.AddBatched` (meeting perimeter: shared add counter updated once per 64 adds).

### What did not work / not done
* Compact neighbour lists for the supply DFS (fewer iterations, more spills): +8% instructions. Reverted.
* Parent-region shortcut for the duplicate region test (child region = parent region - p + b when the ring is connected): applies
  too rarely, net +1%. Reverted. Pre-scan of a probe chain for an exactly equal stored player: no gain.
* `-p:IlcInstructionSet=x86-64-v3`: no measurable change (confirms core's note).
* Row-bitboard flood: regions are 14-60 cells on these levels, a queue flood is ~1.3k instructions; not worth a second representation.
* Not done: delta-coded / compact node records (Go's compact store keeps a full key every 4th node): a chain walk is a cache miss
  per ancestor on every unpack and duplicate compare; byte-list keys would save 8-20 B/node only for levels with >= 2-word keys.
  Merging the blocked-cell and mark arrays of the flood (est. 2-3%). Skipping the macro graph DFS by reusing the region flood.
* Remaining profile (default lane, testdata, instruction share): supply U 11%, macro graph DFS+labels 15%, pull children 14%,
  floods 8%, queues/store 12%. Per node roughly 55 B and ~2.3-3 us CPU.

## macro family (macro, novelty, pull, hf)

Equal-work CPU time (child user+sys, `-parity`, AOT, same minute as Go; machine heavily loaded, +-10% noise; deterministic C# callgrind
instruction counts used to guide the work: 3000-node macro runs on 6 testdata levels 18.1G -> 14.7G Ir before the later steps).

| lane (6 testdata levels, 5000 nodes) | before (cs/go) | after (cs/go) |
|---|---|---|
| macro | 0.94-1.13 | 0.75 |
| pull | 0.93 | 0.90 |
| novelty | 0.87 (Holland 1.56) | 0.62 |
| hf | 0.89 | 0.67 |
| wide levels, macro, 8 levels | 1.03 | 0.92 (SokHard_163#36 0.51, Sven_1623#10 1.16) |

Parity (all exact): macro/pull/novelty 23/23 testdata at 3000 nodes, macro wide 12/12 at 600, hf 24/24 at 3000 (`bench/hf-parity.py`), `dotnet test` green.
Allocation: 0.1 KB/node garbage-or-growth (Holland alloc_mb 13.1 @8000 nodes vs 13.9 @16000; 1 gen0 GC), per-node Sort delegates removed.

Wins (each verified with `SOKSOLV_CHECK_*` reference hooks where one exists):
* incremental room connectivity (Go roomConnChild: only corridor components next to b/t relabelled, only affected rooms re-evaluated; interior-move shortcut;
  single-word pointer kernel for <=64 rooms) - `SOKSOLV_CHECK_ROOMS=1`.
* pointer Tarjan articulation pass for child region counts (`MArtic`), 2x faster than the array version.
* plan queries O(1) per candidate: candidate phases (Miss<=1), per-node out-of-plan cache per phase, Festival packed via permanent-missing count.
* displacement: bitmask augmenting-path matching (was 18% of Sasquatch_VI#34), early-exit canWalk precompute.
* v1 sink planner (novelty lane) uses the cut scan (`CutScan.cs`, extracted from plan4) instead of a flood per pull state and per advisor candidate - `SOKSOLV_CHECK_CUT=1`.
* plan4: clear occupancy by memcpy, SCC elim with arrays and `blk`, pointer region labelling; packing-route flood on pointers.
* dirty-row worklist `FloodRows`, move-access clipped to the region's rows, allocation-free merge sort of move keys / insertion sorts.

Did not work: restricting row sweeps to the region's row span for the box-search flood (no gain: regions already small; pair-search floods dominate FloodRows).
Remaining: ClusterChecker.Region is the top item on long runs (not in this area); pair-search floods; plan4 expansion (ComputeElim4/Expand4/CutQueryFill) ~20% on 50-box levels;
full-DFS articulation per box; room-fragment recomputation on Holland-like levels. Targets of <=0.5x Go are met only on some levels (novelty/hf/SokHard), not overall.
