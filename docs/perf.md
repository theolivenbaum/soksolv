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


## fa2 / dive / factor

Same method as above (CPU time, equal work `-nodes N`, `-parity`, Go from `.reference`). Totals over the 22 testdata levels (fa2 N=5000, dive N=8000) and 8 gated factor levels (N=4000):

| lane | before (Go = 1.0) | after |
|---|---|---|
| fa2 | 0.97 (this session's baseline run; docs said 1.11) | 0.78-0.83 total; 0.4-0.6 on room/corridor levels (GrigrSpecial, Holland_43, SokEvo_107_55, Aymeric_282); 0.87-0.93 on Dive_Sven_1623_197 (100 boxes, per-candidate O(boxes) terms) and Sasquatch_IV (dead-zone proofs ~45%) |
| dive | 0.74 total, Dive_Sven 0.66 | 0.67 total, Dive_Sven 0.50 (0.58 incl. noise), others 0.7-0.8 |
| factor | 1.11 | 0.94 (time is the plan4 planner: CutQueryFill/Expand4/ComputeElim4/RadixSortKids on wide boards) |

Parity: fa2 24/24 testdata @5000 and 42/42 LTS @20000; dive 24/24 and 42/42 @20000; factor 24/24 testdata @3000; `dotnet test` green (HubPull node-slice test is load-sensitive, passes alone).

What helped
* Allocation: 68-81 MB -> 9-11 MB per 5000 nodes (remaining ~0.9 KB/node is the stored node, edge and aux records). Root cause was `Span<int>.Sort()` (AOT allocates a delegate per call) in the cluster search plus per-pop `Array.Sort`; fa2/dive now use `SmallSort`/heapsort.
* fa2 room score is incremental per child (`Fa2Rooms.cs`): per-room rows P_r of the parent, only rooms of b/t, rooms adjacent to either, and rooms touched by splitting/merging corridor components are re-evaluated; corridor relabel only when b or t is a corridor cell. Exact.
* Child region counts: lazy articulation. A 256-entry ring LUT settles `pieces(t)` (0/1) for non-articulation cells, and the Tarjan pass over the board runs only when a candidate cannot be settled (`SOKSOLV_FA2CHECK=1` verifies every count against a flood).
* Pointer kernels for explorer/mobility; cluster precheck before the player flood in `Fa2DeadLate`.
* dive: Hungarian prime warm-started from the previous prime's column potentials (unique optimum, so exact; 186k -> few cold augment calls), 4-lane SIMD augment loop, A-term as a delta over the <=24 cells around b/t, child region reused from `DiveApply`, no per-expand `ToArray`.

What did not work
* Skipping repeated cluster/dead-zone proofs (memo of (state,t) results, and skipping the pop-time check for edges verified at expansion) saved 5-15% but is NOT parity-exact: the checkers are history dependent (Sven_1623#901 diverges, 1298 vs 1314 nodes). Kept behind `SOKSOLV_FA2MEMO=1`, off by default.
* Remaining fa2 gap is the dead-zone prover (`DeadZoneChecker.Search/EscapeBits`, 45% on Sasquatch_IV) and the macro box search; both are outside these files. Factor needs plan4 (`CutScan`, `Expand4`) work.
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

## deadzone / cluster / articulation (Search/DeadZone/*, ClusterChecker, FullSolver.Expand.cs)

Method as above (AOT, `nice`, CPU time, `-parity`, equal work); the machine was shared with 3-5 other agents (load 6-16), so wall/CPU ratios carry +-10% noise and
the work was steered by deterministic callgrind instruction counts (Ir) of the C# AOT binary (`valgrind --tool=callgrind`, 600-3000 node runs). Every step below is
fingerprint-identical (solved / nodes / pushes / stored / generated / duplicates, and the macro/novelty S1HASH-style `fingerprint`) against the previous C# build on
all 23 testdata levels for full, macro, novelty, pull, hf (-no-meet), fa2, dive, factor, and on the 12 wide levels for macro/novelty; `dotnet test` green.

### What won
1. **Cluster proof search keeps a region per queued state** (`ClusterChecker.SearchBfsBits`, bit-row boards). The BFS used to flood the player's region of every
   expanded state (+ a second flood for every move whose 8-ring said "may split"). Now a child's region rows are *derived* from the parent's:
   `R - t + x + pockets(x)` (copy of `reg`, clear t, set x, OR the pocket rows that `FastPlayerB` already floods), written to a preallocated pinned slab only for states that
   are new in the seen-set; only ring-split candidates flood (into a scratch row buffer that is copied into the slab if the state is new). No flood at expansion time any more
   (floods per Holland_81_57 macro run: 178k -> 105k). Derived rows are verified against a real flood by `-cluster-check` (`ClusterChecker.CheckFast`). The child's key is built
   directly as the 8 x ushort lane array (`Key` layout is exactly that), `MoveSorted` on the lanes, no per-move spans. Cluster share of `full` was 51-80% of all instructions;
   this step: -14..-17% Ir on Holland macro/full, FastPlayerB micro-cleanup another -4%.
2. **Corral prover (`DeadZoneChecker`) rewritten on pointers + row words** (boards <= 64x64):
   * all row kernels on pinned `ulong*` (cell -> `row<<6|col` table in one ushort load instead of two byte-table loads + bounds checks);
   * `FloodFreeDirty`: dirty-row worklist flood (tzcnt) instead of the up/down sweep passes; the escape fixpoint passes only re-judge rows next to a row whose free cells or
     flooded region changed (`fdChg`/`rmask`, exact because a row's verdict depends only on rows r-1..r+1 of F and R);
   * **`SearchBits`: corral states are row words**. A child (box x -> t, or killed) is parent rows with two bits toggled; F is the parent's F with the same two bits; the escape
     fixpoint runs on rows (`EscapeRows`, removals are whole row words, no per-box arrays); the child state array is written by *enumerating the bits in row-major order*
     (which is exactly ascending cell order = Go's sorted state) with an incremental Zobrist hash instead of FNV over the shorts; no blk-byte occupancy toggling
     (`Occ()` reads the rows). Per child cost went from O(k) several times over (k = zone boxes, ~30 on Sasquatch) to O(H) copies + O(live rows).
   * `MatchFixed`: free reachable goal first (the maximum matching size does not depend on the augmenting path), `Array.Clear(mSeen)` only when an augmentation is needed.
   Sasquatch_VI_50#34 macro 600 nodes: DZ functions 1.1G -> 0.4G Ir; whole run 4.6G -> 3.9G (-15%); DZ share of macro on the 8 reference levels 4.6% (was ~10%).
3. `FestCorral`: no per-call `List<bool>`/`List<ulong>`/`Span` array literals (growable pooled arrays) - 0 B/call.

### What did not work (measured, reverted)
* **Lazy articulation** (ring-table `pieces(t)` for non-splitting candidates, one region flood for `comps`, flood per split candidate; or hybrid: Tarjan only when a candidate's ring
  splits): total Ir unchanged (+0.6%). Most boxes with >= 2 candidates have at least one ring-split candidate (corridors), and a pointer Tarjan (~13 instr per free cell,
  4.7k Ir on Sasquatch, ~17k on Holland) costs about 4-8 row floods, so floods only win when almost no candidate splits. Also tried an adaptive budget (flood the first K
  ring-split candidates, then switch to the Tarjan pass; K=0/2/6): pull 0.707G -> 0.723/0.718/0.711G Ir, macro 6.69G -> 6.70G: no gain on any lane.
* Rewriting the Tarjan frame loop with the current frame in registers and a monotone discovery base (no per-pass stamps): -4% of MArtic itself; kept, and `ComputeArticulation`
  (full/fa2) now shares that single pass (`MArtic`) instead of a second array-based copy.
* **7x7 window connectivity** to resolve ring-split candidates without a flood (all free orthogonal neighbours of t joined by a path inside the window): only ~25% of the
  split-possible cases resolve (42-60% are real splits, the rest connect over longer paths), the window test costs ~310 Ir -> net +6% on Holland `full`.
* Ternary-selected neighbour offsets instead of `b.Dirs[di]` in `FastPlayerB`: slower (kept the table, now a pinned `int*`).
* Statistics: ring-split candidates are 33% of cluster BFS moves; of those 40-85% only look splitting (diagonals), 42-60% are real splits and the lost fragment is
  < 40 cells in >95% of them. A flood from the *small side* with early exit (or lockstep floods) is the remaining idea for `FloodBitsTo` (15-21% of `full` on Holland).

### Remaining in this area
* `ClusterChecker` is still 7% (macro family, 50-80% on Holland-like `full` runs): SearchBfsBits self ~300 Ir/move (spills; the `Key` compare and `seenSlots` probe dominate TryAdd),
  FastPlayerB ~200 Ir/move, FloodBitsTo ~680 Ir/flood.
* `MArtic` (Tarjan, macro/pull/novelty 5-9%): needs a different formulation (incremental block-cut tree per parent node) to beat floods.
* `DeadAfterCore` (player region flood on `bool[]`, corral discovery) and `BuildZoneCells` are ~1% each and still on managed arrays.
