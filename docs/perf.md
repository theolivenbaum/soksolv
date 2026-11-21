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

### Result (CPU s user+sys, 8 testdata levels: Holland 57/27, Sasquatch VI 24 / IV 42, Aymeric 273, SokHard 36, Dive Sven 197, SokEvo 82; best of 2; cs/go; before = same tree with only the two DZ/cluster files reverted)

| lane (nodes) | before | after |
|---|---|---|
| full (8000) | 0.341 | 0.313 |
| macro (5000) | 0.806 | 0.745 |
| novelty (5000) | 0.794 | 0.730 |
| pull / hf / dive | 0.94 / 0.81 / 0.62 | 0.93 / 0.84 / 0.62 (DZ/cluster are ~0% of pull and dive; hf within noise) |

### What won
1. **Cluster proof search keeps a region per queued state** (`ClusterChecker.SearchBfsBits`, bit-row boards). The BFS used to flood the player's region of every
   expanded state (+ a second flood for every move whose 8-ring said "may split"). Now a child's region rows are *derived* from the parent's:
   `R - t + x + pockets(x)` (copy of `reg`, clear t, set x, OR the pocket rows that `FastPlayerB` already floods), written to a preallocated pinned slab only for states that
   are new in the seen-set; only ring-split candidates flood (into a scratch row buffer that is copied into the slab if the state is new). No flood at expansion time any more
   (floods per Holland_81_57 macro run: 178k -> 105k). Derived rows are verified against a real flood by `SOKSOLV_CHECK_CLUSTER=1` (`ClusterChecker.CheckRegions`; deliberately not `CheckFast`, which the unit tests toggle while other tests run in parallel). The child's key is built
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

## macro open() (per-candidate / per-node hot path: pair search, box-search flood, move access, region counts)

Method as above (AOT, CPU time, `-parity`, equal work); deterministic C# callgrind instruction counts (Ir) guided the work. Everything below is
exact: fingerprints/node counts identical to the pre-change binary and to Go on testdata (macro, novelty, pull, fa2, dive, hf 24/24 @3000) and on the
12 wide levels (macro 12/12 @600); `dotnet test` green.

Profile facts that decided the order: on testdata levels the macro engine's own code (box search, MVisit, move access, floods) is only ~5% of a macro/novelty
run - the rest is the sink planner (plan4/cut scan), cluster/dead-zone proofs (other agents' areas). It is dominant only on **pull** (pure open() work) and on
levels with 100+ boxes and corridor-heavy boards (Sven_1623), where one wide node costs 2-3 M Ir. There the top items were the pair-deadlock searches (21% Ir),
move access (9%), side-dead (3%) and the displacement precompute (8%).

| change | effect (Ir, deterministic) |
|---|---|
| **pair-search successor memo** (`PmFind/PmInsert`, Expand.cs): the expansion of an ordered state (box cells x, y + region start p) is a pure function of the state, so its successor list (in generation order) is cached in a fixed open-addressing table + ushort arena; 600k floods -> 50k on Sven_1623#21. Same visited-set/cap semantics, goal-pair flag cached too | Sven#21: 3.34G -> 2.65G (-21%) |
| generated pair states keep their region rows (`psreg`), so the expansion of a generated state does not flood again | -3% of the above |
| non-splitting pair shortcut: when neither box cell can split its component locally (8-ring LUT) and they do not touch, the region is `component(start) \ {x,y}`: min cell and membership in O(1) (`PairFast`); only used when it applies (open rooms; none on Sven#21) | exact vs `PairDeadFromScan` (`SOKSOLV_PAIR_CHECK=1`) |
| **move access as a delta** (`MMoveAccessDelta`): per-node tables (parent region rows, `accCnt[cell]` = (box,dir) pairs supported by a cell, parent mobility); child mobility = Mp + sum(cnt over D) - sum(cnt over E) + 8 corrections, explorer from D only (D/E = child region xor parent region, usually a handful of cells); row kernel as fallback for > 160 changed cells and for the novelty fact recording | MMoveAccessBits 294M -> 72M on Sven#21 (4x) |
| box-search flood keeps the region **only as row bits** (no `m3` stamping loop over the region; `InPR(c)` bit test with a multiply-shift row index), clears only the previous region's rows, contiguity gives min/lo/hi without scanning | BbFlood 151M -> 34M |
| side-dead per box search: boxes on cells whose verdict is independent of the player's side are counted once, only separator cells (and the moved box) are asked per candidate (`MSdBuild`) | MPushDead+SideDead -5% on Sven |
| displacement precompute `canWalk` via the row-bit flood (`CanWalkRows`) instead of a queue BFS per neighbour (`SOKSOLV_DISP_ROWS_OFF=1` = old; unit test compares the pinned masks) | -4% on short Sven runs |
| child region count: 8-ring LUT settles a non-separator candidate (0 or 1 piece removed) from the component count learned on an earlier candidate; the whole-board Tarjan pass (`MArtic`) runs only after two separator candidates (a lone one costs one flood) (`SOKSOLV_REGRING_OFF=1` = old; `SOKSOLV_CHECK=1` compares with a flood) | pull -4..-12% Ir |

Equal-work CPU time vs Go (22 testdata levels, 5000 nodes, best of 3, same minute): macro 0.764 -> 0.673, novelty 0.749 -> 0.69, pull 0.88 -> 0.84,
wide macro (12 levels) 0.91 -> 0.88 (Sven_1623#21 0.98 -> 0.67, #10 0.98 -> 0.54; the total is dominated by Sasquatch_IV#32 (13 s, 12 s of it plan4 setup) and
Sasquatch_VI#48 (6 s, plan4/cut scan) - not in this area).

Allocation: steady-state garbage 0 (gen0 count unchanged, alloc_mb grows 0.1-1 MB per 1000 nodes by retained `mmv`/novelty words only; the macros / nodeMacros dictionaries
belong to the `full` lane, not to this engine). The pair memo adds 1-3 MB once (preallocated, sized by the board).

What did not work / not worth it
* Skipping the box-search flood for non-splitting `nc` (region = base component minus nc, ring LUT on the free rows, base flood once per box search): exact, but <= 0.6% Ir
  on testdata and Sven (separator-dense boards defeat the LUT). Reverted.
* Pointer rewrite of `MBoxSearch` (fixed pointers for mpost/mseen/mbq/dist, `blk` instead of wall||occ): no change in Ir (ILC already hoists the bounds checks). Reverted.
* Pair floods across the pair search cannot all be skipped: the memo only helps for repeated states; the remaining 50k floods are first visits.
Remaining (not in this area): plan4/cut scan setup dominates the Sasquatch wide levels (12 s of 13 s on Sasquatch_IV#32); cluster/dead-zone proofs dominate the testdata macro/novelty
profile (~35%); pull is now spread flat over MArtic 9%, MBoxSearch 8%, RoomBroken 7%, floods 7%, MVisit 6%, move access 6%.

## planners (plan4, v1 sink planner, planner v2, CutScan, fa2 hot sides)

Files: `Macro/FullSolver.MacroPlan4.cs` (Planner4: `Expand4`/`PullBox`, `ExpandV2`/`PullBoxV2`, matching, elimination SCC, kid order), `Macro/FullSolver.MacroPlan.cs` (v1 sink planner of the
novelty lane, `PackingPlan`), `Macro/CutScan.cs` (low-link scan answering pull-state queries), `Macro/CellPieces.cs` (block-cut view used by fa2), `Fa2/Fa2HotSides.cs`,
`Fa2/Fa2Oop.cs` (`SourcesOf`). Method as above (CPU time, same-minute Go, `-parity`, equal work); deterministic Ir from callgrind on the AOT binary guided every step
(`P4EXP=n` env var caps plan4 expansions for profiling; `SOKSOLV_P4HASH=1` prints a hash of the planner's best path, the exactness oracle for planner-only changes;
`SOKSOLV_CHECK_CUT=1` / `SOKSOLV_CHECK_HS=1` re-verify every cutscan answer / hot-side row / `SourcesOf` against the original flood).

### Result (CPU, C# / Go, equal work, parity exact everywhere)

| workload | before | after |
|---|---|---|
| plan4 setup, Sasquatch II_50#25 (200000 expansions, 50 boxes): C# s / Go s | 76 / 75 (1.0x) | 36 / 80 (0.44x, two reps); steady Ir per expansion 1.58M -> 0.77M |
| macro, 12 wide levels (bench/wide-levels.tsv) | 0.92 | 0.55-0.58 |
| fa2, 12 wide levels | 0.80 | 0.66 (Sasquatch_VI_50#48: 18.1 s Go, 10.9 s C#; 120 nodes 7.5 s -> 0.84 s) |
| novelty, 12 wide levels | 0.70 | 0.70 (setup is small there; the rest is outside the planners: move access, FloodRows, novelty hash) |
| macro / novelty / fa2, 22 testdata levels, 5000 nodes | 0.75 / 0.62 / 0.83 | 0.64 / 0.63 / 0.62 |

Parity (`bench/bin-parity.py BASE NEW lane nodes [list]`, 43/43 on every 7th level of the LTS sample for macro/fa2/novelty/factor as well): full-statistics comparison (solved/nodes/pushes/stored/generated/duplicates/fingerprint/solution) of the new AOT binary against the pre-change AOT
binary (itself Go-exact) on the 24 testdata levels for macro, novelty, fa2, factor, dive, hf, pull and on the 12 wide levels for macro/fa2/factor/novelty; identical. `dotnet test` green
(except the known load-sensitive `HubPull_NodeSlicesMatchGo`, which passes alone).
Allocation: plan4 growth storage (nodes, boxes, used sets, kids) is native chunked memory that never moves: alloc_mb at 30000 expansions 92 -> 58 MB, gen0/gen2 GCs 3 -> 1; no per-node garbage.

### What won (largest first)
1. **Pull-state queries through one DFS per node (CutScan), made cheap.** The scan was already there; the costs were in how it was used: (a) a per-`to` *lift-independent* record `G`
   (separated child subtrees, piece of each neighbour, parent-side minimum, plain answer) is computed once per node and shared by every box (distinct `to` per node ~300 vs ~800 fills);
   (b) a lift that touches at most one piece of `to` only lowers that piece's minimum (fast path, ~85% of fills); (c) labels in locals with unique negative sentinels for blocked
   directions: sides/pieces become 12 compares, no loops, no stackalloc security cookie; (d) pointer kernel over POH arrays (kept referenced), `Q` record = one cache line.
2. **Dedupe of (to, component) ends without hash/stamp arrays**: components of the neighbours of `to` are the `sides` groups, so a 4-bit `Emit` mask in the per-lift query record
   replaces `NewEnd`'s (generation, count, value) arrays (plan4/v2) and the `HashSet<(int,int)>` of the v1 planner (which also allocated and cost 4% of setup).
3. **Matching on bitsets.** `reach[t]` transposed once into `cellBases[cell]` (bases that reach a cell); free bases are one bitset; Kuhn augmentation, the one-removal
   alternating-path searches (`MatchSkip`) iterate `cellBases[cell] & free & ~visited` with `tzcnt` instead of `List<int>` enumerators + `CanReach` per base (was 28% of plan4).
4. **Kids in lazy stable order.** Only ~0.3% of the ~340 stored kids per node are ever consumed, so the full stable radix sort (~120k Ir/expansion) became: threshold selection of
   the `Plan4Beam` smallest (compressed key, software run-compress since `Bmi2` is not enabled in the default AOT target) only when more than 512 kids exist, and an on-demand
   "next smallest, rotate to the sorted prefix" (`EnsureSorted`; a node popped >12 times is sorted in one go). Exactness: the rotation keeps the relative order of the rest, so ties
   resolve exactly as the stable sort; the Festival advisor is chosen over the unsorted beam with the tie rule (regions, -walk, key, index) = first best in sorted order.
5. **Region components for free**: `CutAll` runs the DFS over every free cell at node start, so component ids = regions of the unlifted board (`baseR`, the player's region test,
   lifted-box region counts): the player flood, `LabelRegions` and the per-box `Regions()` floods of planner v2 (2 per box!) disappeared.
6. **`Separated`** (sink gating): the depth-5 BFS per candidate became a lazily built per-cell bitset ball (12% of Sasquatch_IV setup -> 1.6%).
7. **fa2 hot sides / out-of-plan zones (Go hsZones, hsReachGoal, sourcesOf)** were 80% of fa2 on Sasquatch_VI_50#48 (O(cells^2) floods per blocker and per plan step). `CellPieces`
   (one low-link DFS giving, for every cell, which neighbours stay connected without it) replaces the zone floods and the per-state flood of `SourcesOf`; the 50 per-goal reverse BFS per
   blocker became one Tarjan condensation of the (cell, side) graph with goal bitmasks flowing once per edge. 46.5G -> 13.1G -> 7.2G Ir on that level.
8. v1 planner: the advisor's second pass re-filled a cutscan record per candidate in a new lift context; the piece count of the BFS state is the same number (stored); LINQ gone from `Run`.
9. Smaller: pinned arrays + one `PullBox` method per box (register pressure: Expand4 as one method spilled ~40% of its instructions), merged candidate/beam pass, `FestPlan.Packed` on flat arrays.

### What did not work / not done
* Comparison-free full sort of every beam (even with compressed keys and 6-11 bit digits): ~170 Ir per kid because the generated loops spill; replaced by the lazy order.
* `Bmi2.X64.ParallelBitExtract` is not usable in the default AOT target (`IsSupported` false), the software run-compress is used; a first version that only masked the shifted key had a
  prefix bug that only showed on the AOT build (JIT used PEXT) - always test planner changes on the AOT binary.
* Incremental reuse of a parent's per-box pull sets for the child (exactness would need a proof that the moved box cannot change a visited query's component/minimum): not attempted.
* Bit-parallel push-graph SCC for `ComputeElim4` (6-10% of plan4 on open boards): designed (row-word closure with carry fill), not implemented; the pointer Tarjan is kept.
* `PackingRoute.Flood` (per-state floods, 5% of v1 setup) must keep its visit counts (the 8M cap changes the plan), so it cannot use cutscan answers.
* Remaining top costs, plan4 on Sasquatch II (0.80M Ir/expansion): PullBox 33%, CutQueryFill 23%, FillG 10%, CutDfs 7%, ComputeElim4 7%; on open boards CutDfs + ComputeElim4 reach 30%.

## hub/toward (hub-gen generator, hub-pull, hf / toward glue: `Search/Hub/*`, `Search/Toward/*`)

Equal-work CPU time (child user+sys, AOT, best of 3, Go and C# back to back, nice, 4 shared cores). Go = the patched copies used by the parity scripts
(`bench/hubgen-ref`, `bench/hub-ref`, `bench/hf-ref`, deterministic): hub-gen `-hub-gen-only -nodes 20000`, hub-pull `-hub-node-slices 1000 -nodes 20000` (Go
`SOKSOLV_NODESLICES=1000`), hf `-nodes 10000 -parity`; 7 testdata levels (Holland_81_27/43, Sasquatch_IV_50_42, Sasquatch_VI_50_24, SokHard_163_13, Grigr2002_40_24, SokEvo_107_55).

| lane | before (C#/Go) | after (C#/Go) | per level after |
|---|---|---|---|
| hub-gen generator | 1.03 | **0.36** | 0.31-0.43 |
| hub-pull (node-slice) | 0.74 | **0.53** | 0.31-0.63 |
| hf / toward | 0.80 | **0.61** | 0.40-0.66 (Sasquatch_VI 0.93: 0.14 s runs, start-up bound) |

Parity (all exact, after every step): `bench/hubgen-parity.py` 24/24 at N = 1000 / 3000 / 5000 / 6000 / 20000, `bench/hub-parity.py` hub-gen 24/24 (256/20000, 256/15000, 256/8000)
and hub-pull 24/24 (1000/20000, 512/12000, 512/6000), pull `-pf-on` vs `soksolv-pf` 24/24 (2000, 3000), `bench/hf-parity.py` 24/24 (3000, 6000, 15000; also `-hf-festival=false`,
`-hf-fest-corral=false -hf-no-return`), `dotnet test` green (the HubPull node-slice test is load-sensitive, passes alone). Self checks added (each compares every call with the old code,
`SOKSOLV_CHECK_BIC=1`, `SOKSOLV_CHECK_FESTROOMS=1`, `SOKSOLV_CHECK_MATCH=1`) plus `HubTests.BiconBits_MatchesDfs_OnRandomBoards`.

Allocation (`bench/alloctrace`, modes `hubgen|hubpull|hf`, JIT; AOT `alloc_mb` agrees): no per-node garbage left in these lanes. What still grows with the node count is retained data only
(hub-gen: nodes/boxes/seen/heap arrays ~0.4 KB per expansion; hub-pull ~0.35 KB/node, hf ~0.1 KB/node: stored nodes, edges, aux words, frontier entries). Found and fixed on the way:
a closure + delegate per `MOpen` (`PackingObligationAdvice(k => MChoose(ref pk, k))`, now a cached delegate on fields), `new List<int>` per hub `Score`, per-call `List<bool>/List<ulong>` in `FestCorral`.

### What won (instruction counts from callgrind on the AOT binary; Ir of 3000-node hub-gen on Holland_81_27: 818M -> 268M)

1. **Bicon (Festival biconnect_score) by planar duality** (`Hub/BiconBits.cs`; was a DFS over the board per generated node = 36% of hub-gen, per candidate in `Score`). A free cell is on a cycle iff
   it has a non-bridge edge; an edge of the free-cell grid graph is a bridge iff both sides lie in the same *face*, and the faces other than the 2x2 free squares are exactly the **8-connected components
   of the blocked cells** (walls + boxes). So: bit-row 8-connected flood of the blocked cells (single blocked cells are dropped in one word-parallel pass: they cannot bridge), per component its
   "dual square" rows (`q = (c | c>>1)`, `c = row_i | row_{i+1}`), bridges = horizontal `q[i-1] & q[i]`, vertical `q<<1 & q`, non-bridge edges -> cell masks -> popcount. ~4.5k instructions per call for a 13x13 board
   with 15 boxes vs ~35k for the DFS. `Score` counts only the player's region (a component's cycles do not depend on other components), so the pocket cells are no longer blocked/listed.
   An earlier version with static wall-component masks + a union-find over boxes was *slower* than the DFS (K wall components x rows per call); the plain per-call flood wins.
2. **Generator region flood on bit rows** (`Hub/RowFlood.cs`, `HubGen.Flood`): player region as one flood, other components counted by repeated floods (labels only when `Expand` needs them for `DReg`); region 1 rows
   serve `MacroBfs`, `Score`, `InPlayerRegion`. 21k -> ~3k instr per flood.
3. **Pool signatures cached** in `HubCand.Sig` (`Admit` recomputed `BoxSig` of every pool entry on every admit: 8.6%).
4. **Festival score_rooms on bit rows, incremental and memoised** (`Hub/FullSolver.PfRooms.cs`): per room one dirty-row flood over `free & (room | room-less cells)` and a dilation test against the neighbour rooms'
   row masks instead of a queue walk per start cell (2.2x); room r only depends on the occupancy of D_r (its walk plus the walk's neighbours), so a state that differs from the last full evaluation (the expanded parent, found by an
   O(n) diff of the box list, no hook in MacroOpen) in one box re-walks only the rooms whose D_r holds the old or new cell, and the pair count is updated only for pairs touching them; each room walk is additionally
   memoised in a direct-mapped table keyed by the Zobrist xor of the boxes inside D_r (pure function of that local occupancy). FestRooms: 16% -> 1.7% of hub-pull Holland, 11% -> 1.7% on Sasquatch_IV.
5. **Matching distance**: edge table transposed per box cell (`tgtDistT[cell * P + j]`, AssignmentInf folded in; the old per-goal rows were a stride-`cells` gather), the e-maxx augment with **lazy duals**
   (minv kept as minv+D, used columns get `v -= D - D_j` once at the end) over Vector128 (4 columns per step; `Vector256` is *not* hardware accelerated in the default AOT target - AVX2 code silently took the
   scalar fallback), first-minimal-column tie-break kept; `MatchChild` cost in closed form from the dual objective (`hfCost - old edge + u + v[goalCol] + sum of deltas`, no O(n) re-summation);
   and **memoisation by the Zobrist hash of the box set** (only the optimum value is observable: duplicate children / revisited parents skip the solve, the parent's duals are computed lazily on the first child miss,
   rebuilt from the child's boxes). ~45% of child matchings hit. MatchChild+AugmentRow: 17% -> 6% of hub-pull Sasquatch_IV.
6. **Toward mode skips the dead room-connectivity of children** (`MacroOpen`: `room` is overwritten by `HotRel` in hf/toward, `MRoomConnChild` was 9-12% of hf and of the forward trials of hub-pull; hf Sasquatch_IV -23%).
   The parent side (`MRoomConnParent`: corridor labelling used by the corridor-flow deadlock check) and `HotCounts` / `MMoveAccess` must stay: skipping them changes the search (hf-parity differs), tried and reverted.
7. **hub-gen glue**: `blk` byte mirror (wall|box) padded by two rows, branch-free `MobOf`, tighter `Local` (24% -> 14%).

### What did not work
* HotRel (hf hotspot count) as bit rows (1.7k instr vs 0.85k scalar at 15 boxes: fixed per-row cost beats the 4-neighbour scalar loop below ~30 boxes) and as an incremental delta around the moved box (1.5k, the 10+10 cell
  re-evaluation in two states costs as much as the full count). Reverted.
* Warm-starting `MatchDist` from the previous solve's column potentials: -12M +14M Ir, no gain (rows still need 3-4 steps); reverted.
* Dedicated dilate-once component test in the room walk (slower than the per-neighbour row-range test: components span many rows, neighbours few).
* `Vector256` Hungarian (see 5), kept `Vector128`. Fusing the first-min-index into the update pass: not worth the extra lane bookkeeping.

### Remaining (not in this area)
hub-pull/hf are now dominated by the macro engine and the proofs: `MPushDead` + dead-zone (11-16%), box-search floods (`BbFlood` 13%), `MArtic` 6%, `MMoveAccessDelta` 3-7%, `RoomBroken`/`MRoomConnChild` in the pull phase (5-8%);
mine: remaining Hungarian augments of the missed children (5%), `PfFill` 2%, `FestCorral` 3% (the whole-board component labelling per expanded node), hub-gen `Local` 14% / `BiconBits` 18% / flood 15%.
