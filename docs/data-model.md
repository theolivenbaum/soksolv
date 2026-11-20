# Data-model exploration (Phase 1)

Method: `bench/Soksolv.Bench explore testdata 400000` — harvests up to 400k real push-states from each of
18 test levels (`testdata/`, 27–112 floor cells, 3–16 boxes) and measures candidate layouts on them.
Machine: Intel Xeon @ 2.10GHz, 4 vCPU, 16 GiB, .NET 11 RC3, AVX-512 (noisy shared VM: ±30% run to run;
only large, consistent gaps are treated as findings).

Where Go spends its time (`go tool pprof`, `-strategy full`, Holland_81 #57): **cluster deadlock checker 62%**
(`cluster.region` flood 40%, fastPlayer 7%), `regions()` 11%, `reach` 4%, `boxPath`/`piCorral` ~4% each.
State store / hash table (`find`, `add`, `grow`) < 5%. So the per-state record matters for *memory*
(it bounds how long a search can run), the flood/BFS kernels matter for *speed*.

## 1. State store (insert-new / lookup-dup ns, bytes per stored state, GC allocation)

| variant | ins ns | dup ns | B/state | GC alloc (400k) |
|---|---|---|---|---|
| V1 Go layout: managed SoA arrays doubling, int32 table + uint32 fp, bitset key | 80–390 | 27–137 | 71–92 | 25–67 MB churn |
| **V2 unmanaged chunked AoS record (key inline), packed `ulong` slot (fp32\|id32)** | 60–210 | 28–136 | **48–67** | **0** |
| V3 same, key = sorted `ushort` box list | 56–193 | 20–54 | 52–80 | 0 |

Decision: **V2**. ~30% less memory per state, zero GC heap use (no LOH doubling copies, no GC scans),
one cache line per probe (fingerprint and id share a slot). V3's list key is not consistently smaller or
faster (bitset needs 1–2 words for these boards) and loses O(1) box-at-cell tests. Per-node fields that
only matter after a node is expanded (move list start/len/next/left, 10 B in Go) move to a side array
indexed lazily (4 B/node in the record).

## 2. Chess-style bitboards

* **Flood fill** (player region + min cell): row bitboards (`ulong` per grid row, W ≤ 64) with carry-propagate
  run fill vs stamp-queue: 0.6×–1.8×, no consistent winner (mazy boards need many sweeps).
  **Not adopted** for the generic flood; the queue flood (with Go's incremental `normalize` shortcut) stays.
* **Push generation** for all boxes at once (`boxes & shift(reach) & ~shift(blocked|dead)` per direction) vs
  per-box loop, both including the flood: 0.88×–1.32×, median ≈1.15× — the flood dominates (300–600 ns);
  the push enumeration itself is tiny. Worth taking only if the flood also produces bits for free (it does:
  `reach[y>>6] |= …` during the queue flood), so the bitboard path is kept as an *optional* optimisation
  (todo) for boards with W ≤ 64 and for frozen/2×2 tests, not as the primary representation.
* The **state key itself is a bitboard** over compacted floor indices (`nw = ceil(floors/64)` words).
  That is where the chess idea pays off: equality is a word compare (vectorised `SequenceEqual`),
  "box at cell" is a bit test, and child keys are two bit flips (next section).

## 3. Zobrist / incremental hashing and incremental keys

Per child, cost of producing (key, hash) from the parent (`bench HashBench`, ns):

| floors / boxes | words | Go-style rebuild (copy, sort, clear, pack, mix-hash) | incremental key (2 bit flips) + Zobrist XOR | incr key + word-mix hash |
|---|---|---|---|---|
| 50 / 10 | 1 | 59 | **8** | 13 |
| 112 / 15 | 2 | 91 | **15** | 15 |
| 256 / 20 | 4 | 106 | **8** | 10 |
| 1024 / 40 | 16 | 263 | **10** | 36 |
| 4096 / 120 | 64 | 784 | **13** | 150 |

Decision: **adopt** —
* node hash = XOR of `Z[floorIdx]` over boxes ^ `ZP[normalizedPlayer]` (64-bit Zobrist tables seeded
  deterministically); computed once per *expanded* node (n lookups), each child's hash is
  `h ^ Z[from] ^ Z[to] ^ ZP[oldPlayer] ^ ZP[newPlayer]` (4 XORs, O(1) regardless of board size);
* child key = parent key words with two bits flipped — **no per-child sort and no repack** (Go sorts
  the child's box array every time; the sorted box list is only reconstructed by `unpack` for expanded nodes);
* the table keeps **full-key compare** after the 32-bit fingerprint matches, so a 64-bit Zobrist collision
  can never merge two states (exactness preserved; ~3e-4 collision chance at 10^8 states would otherwise matter);
* table growth rehashes from stored keys (Zobrist over set bits) — or from a stored 64-bit hash if growth
  cost shows up in profiles.
The player term: the *normalized* player is only known after the (usually shortcut) normalisation, so the
dedupe probe happens after normalisation as in Go; Zobrist makes everything else around it O(1).

Same idea for the cluster proof search: states are packed into a 128-bit key (≤ 7 boxes + player,
16 bits each) in an open-addressing set with generation stamps (no clearing, no allocation) instead of
Go's `map[[9]int16]struct{}`; measured end-to-end in the Phase-2 comparison.

## 4. Ahead-of-time compilation (JIT cost)

Measured with `bench/compare.py` / best-of-5 wall time, same machine, single lane (ms):

| level | JIT (default) | ReadyToRun | **Native AOT** | Go |
|---|---|---|---|---|
| Microban_155_31 (20 nodes) | 81 | 44 | **5** | 6 |
| Sven_1623_100 | 300 | 101 | **47** | 57 |
| Holland_81_27 (4.3k nodes) | 786 | 332 | **280** | 341 |
| GrigrSpecial_40_3 | 1424 | 1190 | **892** | 1036 |
| Holland_81_57 (153k nodes, 5 s) | 5945 | 5551 | **4965** | 5682 |

* JIT's cost is not just startup: tier-0 → tier-1 warm-up of the hot flood/BFS loops costs seconds on
  runs of several seconds. R2R removes startup but still re-JITs hot methods; **Native AOT** compiles
  everything optimised up-front and is fastest at every length, ~13% faster than Go on the long level.
* `IlcInstructionSet=native` / `x86-64-v3` gave no measurable gain (BitOperations use popcnt/tzcnt anyway);
  the portable baseline build is used so results are comparable across VMs. (Side effect: in AOT
  `Vector256/512.IsHardwareAccelerated` report false, so SIMD paths must be opted into explicitly if added.)
* Profile-guided AOT (`--mibc`) needs `dotnet-pgo` to turn an event-pipe trace into a `.mibc`; that tool is
  not published on NuGet (only built from the dotnet/runtime sources), so it is not used here. Dynamic PGO
  stays on for the JIT build (`TieredPGO`), but did not beat AOT.
* Build: `bench/publish-aot.sh` → `artifacts/aot/Soksolv.Cli` (1.3 MB, needs clang/lld to link);
  `bench/compare.py` uses it by default (`CS_JIT=1` to compare the JIT build).

## 5. Cluster checker optimisation (Phase 2)

Profile (Go and C# alike): cluster deadlock proofs = 60% of runtime; **66–85% of proof-search states were
spent in searches that hit the 1000-state cap and learned nothing** (Holland_81_57: 874k of 1.3M states),
and 70–85% of those were repeats of a box set that had already capped from another player side.

| change | effect | parity with Go |
|---|---|---|
| pinned buffers, wall\|box byte map, unrolled 4-neighbour floods (cluster + solver `Reach`/`Regions`) | parity mode 1.02× → 0.75× Go time | identical nodes |
| incremental child-region normalisation (pocket flood behind the pushed box, region-min rescan; full flood only when t may split the region) | floods/state 3.2 → 2.2 | identical (checked vs flood with `-cluster-check`) |
| **A\*** proof search (h = Σ box→nearest-goal push distance; admissible+consistent ⇒ same minimal cost, dead proofs still exhaustive) | alive/capped searches far cheaper, penalties now available for clusters that used to cap | changes proof order ⇒ ±1–4% nodes |
| **skip capped box sets** for other player sides (alive is always safe) | 1.5M of 2.2M SokHard_36 states removed | idem |

Result (Native AOT, 18-level subset, `bench/compare.py`): Go 48–49 s; C# `-parity` 36 s (0.75×, all nodes
and pushes identical); C# default (fast cluster proofs) **22.4 s (0.46×)**, 3× less memory, all solutions verified.
On the 28 levels Go's `full` lane fails to solve in 20 s, the C# default solves 5 (Grigr2001_100#59,
Grigr2002_40#34, Sasquatch_V_50#32, SokHard_163#57, #124) and loses none of the levels Go solves.
