using System.Numerics;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Runtime.Intrinsics.X86;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

/// <summary>
/// Stage 6f of the macro lane: plan4 (Go packing_plan2.go planner2 + packing_plan4.go planner4 + packing_fest.go).
/// A BASE_SEARCH-style backward pull planner whose witness gives the macro lane its Festival-prefix `packed` feature.
/// cutscan.go (low-link scan answering "player region with one more cell blocked") is replaced by exact floods.
/// </summary>
public sealed unsafe partial class FullSolver
{
    // ---- Festival event list (Go festPlan) ----------------------------------------------------------------------------------------
    sealed class FestPlan
    {
        public struct Entry { public int From, To, Until; }
        public List<Entry> E = new();
        public int InitNum;
        public int Mode = 1;
        public bool[]? FestTo;                       // fa2: cells that are some entry's target (Go festTo)
        public List<(int cell, int until)> OopPark = new(); // Go oopPark (planner v2): extra out-of-plan targets

        /// <summary>Festival next_parking_place semantics (Go festPlan.nextParking): first plan entry still missing, or -1.</summary>
        public int NextParking(bool[] occ)
        {
            int n = E.Count;
            for (int i = 0; i < InitNum; i++) if (E[i].Until == -1 && !occ[E[i].To]) return -1;
            int packed = n;
            for (int i = n - 1; i >= InitNum; i--)
            {
                if (occ[E[i].To]) continue;
                if (E[i].Until == -1 || E[i].Until >= packed) packed = i;
            }
            if (packed >= n) return -1;
            return E[packed].To;
        }

        public void RecomputeUntil()
        {
            for (int i = 0; i < E.Count; i++) { var x = E[i]; x.Until = -1; E[i] = x; }
            for (int i = 0; i < E.Count; i++)
                for (int j = i + 1; j < E.Count; j++)
                    if (E[j].From == E[i].To) { var x = E[i]; x.Until = j; E[i] = x; break; }
        }

        public void KeepOnlyPerm()
        {
            var e = new List<Entry>();
            for (int i = 0; i < E.Count; i++)
            {
                var x = E[i];
                if (x.Until == -1) { if (i >= InitNum) x.From = -1; e.Add(x); }
            }
            int n = 0;
            for (int i = 0; i < InitNum; i++) if (E[i].Until == -1) n++;
            E = e; InitNum = n;
            RecomputeUntil();
        }

        /// <summary>Moves permanent initial entries ahead of temporary ones (Festival stable partition).</summary>
        public void PartitionInits()
        {
            RecomputeUntil();
            var init = E.GetRange(0, InitNum);
            // Festival scans the endpoint in descending cell order (stable)
            init = StableSort(init, (a, b) => b.To - a.To);
            init = StableSort(init, (a, b) => { bool ap = a.Until == -1, bp = b.Until == -1; return ap == bp ? 0 : ap ? -1 : 1; });
            for (int i = 0; i < InitNum; i++) E[i] = init[i];
            RecomputeUntil();
        }

        static List<Entry> StableSort(List<Entry> a, Comparison<Entry> cmp)
        {
            var idx = Enumerable.Range(0, a.Count).ToArray();
            Array.Sort(idx, (x, y) => { int c = cmp(a[x], a[y]); return c != 0 ? c : x.CompareTo(y); });
            return idx.Select(i => a[i]).ToList();
        }

        int[]? permCnt; int permN, permE = -1;
        void PermPrepare(int cells)
        {
            if (permCnt == null || permCnt.Length != cells) permCnt = new int[cells];
            Array.Clear(permCnt); permN = 0;
            for (int i = 0; i < InitNum; i++) if (E[i].Until == -1) { permCnt[E[i].To]++; permN++; }
            permE = E.Count;
        }
        /// <summary>Permanent initial entries whose cell is empty in occ.</summary>
        public int PermMissing(bool[] occ)
        {
            if (permE != E.Count || permCnt == null || permCnt.Length != occ.Length) PermPrepare(occ.Length);
            int m = 0;
            for (int i = 0; i < InitNum; i++) if (E[i].Until == -1 && !occ[E[i].To]) m++;
            return m;
        }
        /// <summary><see cref="Packed"/> of the child (occ holds it) of a parent with <paramref name="parentMissing"/> empty permanent cells, after the box on b moved to t.</summary>
        public int PackedChild(bool[] occ, int b, int t, int parentMissing)
        {
            int m = parentMissing + permCnt![b] - permCnt[t];
            return m > 0 ? permN - m : Packed(occ);
        }

        public int Packed(bool[] occ)
        {
            int n = E.Count;
            for (int i = 0; i < InitNum; i++)
                if (E[i].Until == -1 && !occ[E[i].To])
                {
                    int c = 0;
                    for (int j = 0; j < InitNum; j++) if (E[j].Until == -1 && occ[E[j].To]) c++;
                    return c;
                }
            int packed = n;
            for (int i = n - 1; i >= InitNum; i--)
            {
                if (occ[E[i].To]) continue;
                if (E[i].Until == -1 || E[i].Until >= packed) packed = i;
            }
            int good = 0;
            for (int i = 0; i < InitNum; i++) if (occ[E[i].To] || E[i].Until < packed) good++;
            return packed - (InitNum - good);
        }

        /// <summary>buildFestEvents: the plan's witnessed backward moves as Festival's forward event list.</summary>
        public static FestPlan Build(PackingPlan p, int nBoxes)
        {
            if (p.Required.Length == 0) return new FestPlan { Mode = 1 };
            var e = new List<Entry>();
            foreach (int c in p.Required[0]) e.Add(new Entry { From = -2, To = c });
            int initNum = e.Count;
            for (int k = 0; k + 1 < p.Required.Length; k++)
            {
                var m = p.Moves[k];
                int from = m.To;
                if (m.Remove) from = -1;
                if (from == m.From) continue;
                e.Add(new Entry { From = from, To = m.From });
            }
            for (int i = initNum; i < e.Count;)
            {
                if (e[i].To == e[i].From) { e.RemoveAt(i); i = initNum; continue; }
                if (i + 1 < e.Count && e[i].To == e[i + 1].From)
                {
                    var x = e[i]; x.To = e[i + 1].To; e[i] = x;
                    e.RemoveAt(i + 1); i = initNum; continue;
                }
                i++;
            }
            var fp = new FestPlan { InitNum = initNum, Mode = 1, E = e };
            fp.RecomputeUntil();
            if (initNum == nBoxes && nBoxes > 0) fp.KeepOnlyPerm();
            return fp;
        }
    }

    // ---- planner data ---------------------------------------------------------------------------------------------------------------
    struct P2Child { public ulong Key, Hash; public int From, To, Player, Base; }
    struct P2Kid { public ulong Key; public short From, To, Player, Base; }
    struct P2Node
    {
        public int Parent, BOff, Nb, Player; public ulong Hash, Key; public int Goals, Dist, MFrom, MTo, MBase, COff, CLen, CNext, CSorted;
    }
    struct P2Entry { public ulong Key; public uint Seq; public int Node; public int W; }
    struct P2Cand { public int Box, To, Player, Pieces; }

    /// <summary>Append-only store of children in 64K-kid native chunks (bulk append, no per-kid call).</summary>
    sealed unsafe class KidStore
    {
        const int Shift = 16, Per = 1 << Shift, Mask = Per - 1;
        nint[] chunks = new nint[16]; int nChunks;
        public int Count { get; private set; }
        public ref P2Kid this[int i] { [MethodImpl(MethodImplOptions.AggressiveInlining)] get => ref ((P2Kid*)chunks[i >> Shift])[i & Mask]; }
        /// <summary>Appends n kids contiguously (a node's kids never straddle a chunk).</summary>
        public int Append(P2Kid[] a, int n)
        {
            if (n > Per) throw new InvalidOperationException("too many planner kids");
            if (n == 0) return Count;
            if ((Count & Mask) + n > Per) Count = (Count + Per) & ~Mask;
            int ci = Count >> Shift, at = Count & Mask;
            while (ci >= nChunks)
            {
                if (nChunks == chunks.Length) Array.Resize(ref chunks, nChunks * 2);
                chunks[nChunks++] = (nint)NativeMemory.Alloc((nuint)(Per * sizeof(P2Kid)));
            }
            fixed (P2Kid* src = &a[0]) Buffer.MemoryCopy(src, (P2Kid*)chunks[ci] + at, (long)n * sizeof(P2Kid), (long)n * sizeof(P2Kid));
            int off = Count; Count += n;
            return off;
        }
        public P2Kid* Ptr(int i) => (P2Kid*)chunks[i >> Shift] + (i & Mask);
        ~KidStore() { for (int i = 0; i < nChunks; i++) NativeMemory.Free((void*)chunks[i]); }
    }

    sealed class P2Heap
    {
        P2Entry[] a = new P2Entry[4]; int n; public int Count => n;
        static bool Less(in P2Entry x, in P2Entry y) => x.W != y.W ? x.W < y.W : x.Key != y.Key ? x.Key < y.Key : x.Seq < y.Seq;
        public void Push(P2Entry e)
        {
            if (n == a.Length) Array.Resize(ref a, n * 2);
            int i = n++;
            while (i > 0) { int p = (i - 1) >> 1; if (!Less(e, a[p])) break; a[i] = a[p]; i = p; }
            a[i] = e;
        }
        public P2Entry Pop()
        {
            var top = a[0]; var x = a[--n]; int i = 0;
            while (true)
            {
                int l = 2 * i + 1; if (l >= n) break;
                int m = l; if (l + 1 < n && Less(a[l + 1], a[l])) m = l + 1;
                if (!Less(a[m], x)) break;
                a[i] = a[m]; i = m;
            }
            if (n > 0) a[i] = x;
            return top;
        }
    }

    /// <summary>Planner state shared by the anytime stepping in the macro loop.</summary>
    public static bool CheckCut = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_CUT") == "1";   // test hook: compare every cutscan query with the exact flood
    public static bool MatchCheckHook { get => Planner4.MatchCheck; set => Planner4.MatchCheck = value; }
    // (old marker): compare every cutscan query with the exact flood
    Planner4? p4pend;
    PackingPlan? prePlan; int prePlanAt;
    /// <summary>The alt engine (exp-769) plans plan4 with festsink + roomsep + keep-parks 1.</summary>
    bool PlanAlt => opt.PlanAlt;

    sealed class Planner4
    {
        const int Plan4Sep = 6, Plan4Beam = 512;
        static readonly int Plan4Exp = int.TryParse(Environment.GetEnvironmentVariable("SOKSOLV_P4EXP"), out var pe) ? pe : 200000;   // profiling hook (default = Go)
        readonly FullSolver s;
        readonly int n, nw, cells, W;
        readonly int[] dirs; readonly bool[] wall, goal;
        public long DeadlineMs; public int MaxExp; public int Expanded;
        public bool Fest, RoomSep; public int KeepParks;
        bool matchOn = true;

        // statics
        readonly int[] baseAt, baseCell, distance; readonly ulong[][] reach; readonly ulong[] zb, zp, zu;
        // own occupancy (the planner works on a private board while the macro loop owns s.occ)
        readonly bool[] occ; readonly byte[] blk;
        // nodes
        P2Node[] nodes = new P2Node[1024]; int nodeN;
        int[] boxArena = new int[4096]; int boxN;
        ulong[] usedArena = new ulong[1024]; int usedN;
        readonly KidStore kids = new();
        ulong[] seen = new ulong[1 << 16]; ulong seenMask = (1 << 16) - 1; int nseen;
        uint seq;
        // scan scratch
        readonly uint[] mark; uint stamp; readonly int[] queue; int qlen;
        readonly uint[] pgen; uint pg; readonly uint[] seenK; uint boxGen; readonly uint* pPgen, pSeenK; readonly int* pBaseAt, pQ; readonly ulong* pAlive; readonly bool* pElim; readonly byte* pBlk;
        P2Cand[] candA = new P2Cand[256]; int candN; P2Kid[] remK = new P2Kid[256], beamK = new P2Kid[256], sortK = new P2Kid[256]; readonly uint[] egen; readonly byte[] ecnt; readonly int[] eval;
        List<P2Cand> cands = new(); P2Child[] rem = new P2Child[256]; int remN; P2Child[] beam = new P2Child[256]; int beamN;
        readonly int[] demand; readonly ulong[] alive; int[] region0 = new int[64]; int region0N; int[] freeA; int freeN;
        int[] lab; uint[] labGen; uint labStamp; readonly int[] labQ;
        // matching
        readonly int[] mBox; int[] curBox; int curN; readonly int nbw; readonly ulong[] cellBases, freeM, availM, visM, matchedM;
        // elim
        readonly bool[] elimOK; int[] elimFr = []; int elimN; bool[] elimBlk = []; uint[] bseen; uint bstamp;
        int[] sccIdx = [], sccLow = [], sccCmp = [], sccCnt = [], sccCall = []; byte[] sccDir = []; readonly List<int> sccStk = new();
        readonly int[] sepD, sepQ; int sepQn;
        // FESS cells
        readonly Dictionary<ulong, int> cellIdx = new(); readonly List<P2Heap> cellHeap = new(); int rr;
        readonly List<int> nodeW = new(); readonly List<byte> nodeAdv = new(); readonly List<int> sinkTo = new();
        // loop state
        public int Best; public bool Done; int fails; int iter; public bool Exhausted;
        P2Child[] sortTmp = new P2Child[256];
        int[] floorCells;

        public Planner4(FullSolver s, long deadlineMs, int maxNodes, bool fest, bool roomSep, int keepParks)
        {
            this.s = s; n = s.lv.Boxes.Length; nw = (n + 63) / 64; cells = s.cells; W = s.W; dirs = s.dirs; wall = s.wall; goal = s.goal;
            DeadlineMs = deadlineMs; MaxExp = Plan4Exp > 0 ? Plan4Exp : maxNodes;
            Fest = fest; RoomSep = roomSep; KeepParks = keepParks;
            cut = new CutScan(cells, dirs); occ = new bool[cells]; blk = cut.Blk; for (int i = 0; i < cells; i++) blk[i] = (byte)(wall[i] ? 1 : 0);
            mark = new uint[cells]; queue = new int[cells]; pgen = PinArr<uint>(cells); seenK = PinArr<uint>(4 * cells); egen = new uint[cells]; ecnt = new byte[cells]; eval = new int[4 * cells];
            demand = new int[n]; mBox = new int[n]; curBox = new int[n + 1]; freeA = new int[n + 1]; lab = new int[cells]; labGen = new uint[cells]; labQ = new int[cells];
            bseen = new uint[cells]; sepD = new int[cells]; Array.Fill(sepD, -1); sepQ = new int[cells];
            floorCells = s.FloorCells();
            zb = new ulong[cells]; zp = new ulong[cells]; zu = new ulong[n];
            ulong seed = 0x5eed;
            for (int i = 0; i < cells; i++) { zb[i] = Splitmix(ref seed); zp[i] = Splitmix(ref seed); }
            for (int i = 0; i < n; i++) zu[i] = Splitmix(ref seed);
            baseAt = PinArr<int>(cells); Array.Fill(baseAt, -1);
            baseCell = (int[])s.lv.Boxes.Clone(); Array.Sort(baseCell);
            for (int i = 0; i < baseCell.Length; i++) baseAt[baseCell[i]] = i;
            int words = (cells + 63) / 64;
            reach = new ulong[n][];
            var q = new List<int>();
            for (int t = 0; t < baseCell.Length; t++)
            {
                int bc = baseCell[t]; var r = new ulong[words];
                r[bc >> 6] |= 1UL << (bc & 63);
                q.Clear(); q.Add(bc);
                for (int i = 0; i < q.Count; i++)
                {
                    int x = q[i];
                    foreach (int d in dirs)
                    {
                        int c = x - d;
                        if (wall[c] || wall[x] || wall[x + d] || (r[c >> 6] >> (c & 63) & 1) != 0) continue;
                        r[c >> 6] |= 1UL << (c & 63);
                        q.Add(c);
                    }
                }
                reach[t] = r;
            }
            alive = PinArr<ulong>(reach[0].Length); elimOK = PinArr<bool>(Math.Max(1, baseCell.Length)); pullQ_ = PinArr<int>(4 * cells + 8);
            pPgen = (uint*)Unsafe.AsPointer(ref pgen[0]); pSeenK = (uint*)Unsafe.AsPointer(ref seenK[0]); pBaseAt = (int*)Unsafe.AsPointer(ref baseAt[0]);
            pQ = (int*)Unsafe.AsPointer(ref pullQ_[0]); pAlive = (ulong*)Unsafe.AsPointer(ref alive[0]); pElim = (bool*)Unsafe.AsPointer(ref elimOK[0]); pBlk = (byte*)Unsafe.AsPointer(ref blk[0]);
            nbw = Math.Max(1, (baseCell.Length + 63) >> 6);
            cellBases = new ulong[cells * nbw]; freeM = new ulong[nbw]; availM = new ulong[nbw]; visM = new ulong[nbw]; matchedM = new ulong[nbw];
            for (int t = 0; t < baseCell.Length; t++)
            {
                var rt = reach[t];
                for (int w = 0; w < rt.Length; w++)
                    for (ulong m = rt[w]; m != 0; m &= m - 1) cellBases[((w << 6) + System.Numerics.BitOperations.TrailingZeroCount(m)) * nbw + (t >> 6)] |= 1UL << (t & 63);
            }
            // distance score: sum of finite walking distances to all goals
            distance = new int[cells];
            var dg = new int[cells];
            foreach (int g in s.goals)
            {
                Array.Fill(dg, -1); dg[g] = 0; q.Clear(); q.Add(g);
                for (int i = 0; i < q.Count; i++)
                    foreach (int d in dirs)
                    {
                        int x = q[i] + d;
                        if (!wall[x] && dg[x] < 0) { dg[x] = dg[q[i]] + 1; q.Add(x); }
                    }
                for (int i = 0; i < cells; i++) if (dg[i] > 0) distance[i] += dg[i];
            }
        }

        static ulong Splitmix(ref ulong x)
        {
            x += 0x9e3779b97f4a7c15UL; ulong z = x;
            z = (z ^ z >> 30) * 0xbf58476d1ce4e5b9UL; z = (z ^ z >> 27) * 0x94d049bb133111ebUL;
            return z ^ z >> 31;
        }

        bool Weighted => Fest;
        public bool Stopped() => s.opt.Stop || (s.opt.Cancel?.Stopped ?? false) || (s.opt.NodeBudget == 0 && s.clock.ElapsedMilliseconds > DeadlineMs);


        readonly CutScan cut;
        void CutNewNode() => cut.CutNewNode();
        void CutBegin(int b) => cut.CutBegin(b);
        int CutCompMin(int v) => cut.CutCompMin(v);
        (int np, int sides) CutQuery(int to, int player) => cut.CutQuery(to, player);
        int CutPieces(int v) => cut.CutPieces(v);

        // ---- own occupancy / floods ------------------------------------------------------------------------------------------------
        void SetOccCell(int c, bool v) { occ[c] = v; blk[c] = (byte)(v || wall[c] ? 1 : 0); }
        byte[]? wallBlk;
        void ClearOcc()
        {
            if (wallBlk == null) { wallBlk = new byte[cells]; for (int i = 0; i < cells; i++) wallBlk[i] = (byte)(wall[i] ? 1 : 0); }
            Array.Clear(occ); Buffer.BlockCopy(wallBlk, 0, blk, 0, cells);
        }
        void SetOcc(int id)
        {
            ClearOcc();
            foreach (int b in Boxes(id)) SetOccCell(b, true);
        }
        ReadOnlySpan<int> Boxes(int id) => new(boxArena, nodes[id].BOff, nodes[id].Nb);
        Span<ulong> Used(int id) => new(usedArena, id * nw, nw);

        /// <summary>Flood from player; returns (min cell, stamp); marks hold the stamp, queue the cells.</summary>
        (int norm, uint st) Flood(int player)
        {
            uint st = ++stamp; int tail = 0; queue[tail++] = player; mark[player] = st; int norm = player;
            for (int i = 0; i < tail; i++)
            {
                int c = queue[i]; if (c < norm) norm = c;
                foreach (int d in dirs)
                {
                    int x = c + d;
                    if (blk[x] == 0 && mark[x] != st) { mark[x] = st; queue[tail++] = x; }
                }
            }
            qlen = tail;
            return (norm, st);
        }

        int Regions()
        {
            uint rs = ++stamp; int cnt = 0;
            foreach (int c in floorCells)
            {
                if (blk[c] != 0 || mark[c] == rs) continue;
                cnt++; mark[c] = rs; int tail = 0; queue[tail++] = c;
                for (int i = 0; i < tail; i++)
                    foreach (int d in dirs)
                    {
                        int y = queue[i] + d;
                        if (mark[y] != rs && blk[y] == 0) { mark[y] = rs; queue[tail++] = y; }
                    }
            }
            return cnt;
        }

        // cutscan replacements (box b lifted by the caller; occ holds the other boxes)
        (int np, int sides) Query(int to, int player, int di)
        {
            int np = cut.CutQueryDir(to, di, out int sides);
            if (CheckCut) { var f = QueryFlood(to, player); if (f != (np, sides)) throw new InvalidOperationException($"cutscan query ({to},{player}): scan {(np, sides)} != flood {f}"); }
            return (np, sides);
        }
        (int np, int sides) QueryFlood(int to, int player)
        {
            blk[to] = 1;
            var (norm, st) = Flood(player);
            int sides = 0;
            for (int di = 0; di < 4; di++) if (mark[to + dirs[di]] == st) sides |= 1 << di;
            blk[to] = (byte)(occ[to] || wall[to] ? 1 : 0);
            return (norm, sides);
        }
        int Pieces(int to)
        {
            int c = CutPieces(to);
            if (CheckCut) { int f = PiecesFlood(to); if (f != c) throw new InvalidOperationException($"cutscan pieces ({to}): scan {c} != flood {f}"); }
            return c;
        }
        int PiecesFlood(int to)
        {
            blk[to] = 1; uint st = ++stamp; int cnt = 0;
            foreach (int d in dirs)
            {
                int y = to + d;
                if (blk[y] != 0 || mark[y] == st) continue;
                cnt++; int tail = 0; queue[tail++] = y; mark[y] = st;
                for (int i = 0; i < tail; i++)
                    foreach (int dd in dirs)
                    {
                        int x = queue[i] + dd;
                        if (blk[x] == 0 && mark[x] != st) { mark[x] = st; queue[tail++] = x; }
                    }
            }
            blk[to] = (byte)(occ[to] || wall[to] ? 1 : 0);
            return cnt;
        }
        bool NewEnd(int to, int np, uint g)
        {
            if (egen[to] != g) { egen[to] = g; ecnt[to] = 0; }
            for (int i = 0; i < ecnt[to]; i++) if (eval[to * 4 + i] == np) return false;
            eval[to * 4 + ecnt[to]] = np; ecnt[to]++;
            return true;
        }

        ulong Key(int rem, int regions, int goals, int dist, int rooms)
        {
            long inv = (1L << 24) - 1 - dist; if (inv < 0) inv = 0;
            return (ulong)rem << 52 | (ulong)Math.Min(regions, 1023) << 42 | (ulong)Math.Min(goals, 255) << 34 | (ulong)Math.Min(rooms, 255) << 26 | (ulong)inv;
        }

        // ---- seen set --------------------------------------------------------------------------------------------------------------
        bool SeenInsert(ulong h)
        {
            if (h == 0) h = 1;
            if (2 * (nseen + 1) > seen.Length)
            {
                var old = seen; seen = new ulong[2 * old.Length]; seenMask = (ulong)(seen.Length - 1);
                foreach (ulong x in old) if (x != 0) { ulong j = x & seenMask; while (seen[j] != 0) j = (j + 1) & seenMask; seen[j] = x; }
            }
            ulong i = h & seenMask;
            while (seen[i] != 0) { if (seen[i] == h) return false; i = (i + 1) & seenMask; }
            seen[i] = h; nseen++;
            return true;
        }

        // ---- children storage -------------------------------------------------------------------------------------------------------
        int PutKids(P2Kid[] a, int na) => kids.Append(a, na);
        int PutKids(P2Child[] a, int na)
        {
            if (sortK.Length < na) sortK = new P2Kid[na * 2];
            for (int i = 0; i < na; i++) sortK[i] = new P2Kid { Key = a[i].Key, From = (short)a[i].From, To = (short)a[i].To, Player = (short)a[i].Player, Base = (short)a[i].Base };
            return kids.Append(sortK, na);
        }
        ulong KidKey(int id, int i)
        {
            ref var nd = ref nodes[id];
            if (i >= nd.CSorted) EnsureSorted(id, i + 1);
            return kids[nd.COff + i].Key;
        }

        /// <summary>Lazy kid order: moves the next-smallest (stable: earliest among equal keys) of the unsorted tail to the sorted prefix
        /// until <paramref name="need"/> kids are in order; a node that keeps being popped is sorted in one go.</summary>
        P2Kid[] lazyBuf = new P2Kid[256];
        static readonly bool LazyOff = Environment.GetEnvironmentVariable("SOKSOLV_LAZYOFF") == "1";
        void EnsureSorted(int id, int need)
        {
            ref var nd = ref nodes[id];
            int cs = nd.CSorted, len = nd.CLen;
            if (need > len) need = len;
            P2Kid* b = kids.Ptr(nd.COff);
            while (cs < need)
            {
                if (cs >= 12 && len - cs > 48)
                {
                    int m = len - cs;
                    if (lazyBuf.Length < m) lazyBuf = new P2Kid[m * 2];
                    var tmp = new Span<P2Kid>(b + cs, m); tmp.CopyTo(lazyBuf);
                    int mm = m; SortTop(lazyBuf, ref mm, int.MaxValue);
                    new Span<P2Kid>(lazyBuf, 0, m).CopyTo(tmp);
                    cs = len; break;
                }
                int best = cs; ulong bk = b[cs].Key;
                for (int i = cs + 1; i < len; i++) { ulong k = b[i].Key; if (k < bk) { bk = k; best = i; } }
                if (best != cs)
                {
                    P2Kid x = b[best];
                    Buffer.MemoryCopy(b + cs, b + cs + 1, (long)(best - cs) * sizeof(P2Kid), (long)(best - cs) * sizeof(P2Kid));
                    b[cs] = x;
                }
                cs++;
            }
            nd.CSorted = cs;
        }
        P2Child Kid(int id, int i)
        {
            ref var nd = ref nodes[id];
            var k = kids[nd.COff + i];
            var c = new P2Child { Key = k.Key, From = k.From, To = k.To, Player = k.Player, Base = k.Base };
            c.Hash = nd.Hash ^ zb[c.From] ^ zp[nd.Player] ^ zp[c.Player];
            c.Hash ^= c.Base >= 0 ? zu[c.Base] : zb[c.To];
            return c;
        }

        // ---- matching ---------------------------------------------------------------------------------------------------------------
        bool CanReach(int bse, int cell) => (reach[bse][cell >> 6] >> (cell & 63) & 1) != 0;

        /// <summary>free set from the node's used-bases bitset: freeM (bitset) and freeA/freeN (ascending list).</summary>
        void BuildFree(ReadOnlySpan<ulong> used)
        {
            int nb = baseCell.Length, k = 0;
            for (int w = 0; w < nbw; w++)
            {
                ulong m = ~used[w];
                int rest = nb - (w << 6); if (rest < 64) m &= rest <= 0 ? 0 : (1UL << rest) - 1;
                freeM[w] = m;
                for (; m != 0; m &= m - 1) freeA[k++] = (w << 6) + System.Numerics.BitOperations.TrailingZeroCount(m);
            }
            freeN = k;
        }
        void FreeAll()
        {
            int nb = baseCell.Length;
            for (int w = 0; w < nbw; w++) { int rest = nb - (w << 6); freeM[w] = rest >= 64 ? ~0UL : rest <= 0 ? 0 : (1UL << rest) - 1; }
            for (int t = 0; t < nb; t++) freeA[t] = t;
            freeN = nb;
        }

        /// <summary>Kuhn augmenting path from box u over the bases in avail (visM = visited bases; matchedM maintained).</summary>
        bool Augment(int u, ulong[] avail)
        {
            int bo = curBox[u] * nbw;
            for (int w = 0; w < nbw; w++)
                while (true)
                {
                    ulong m = cellBases[bo + w] & avail[w] & ~visM[w];
                    if (m == 0) break;
                    int bit = System.Numerics.BitOperations.TrailingZeroCount(m);
                    visM[w] |= 1UL << bit;
                    int t = (w << 6) + bit;
                    int o = mBox[t];
                    if (o < 0) { matchedM[w] |= 1UL << bit; mBox[t] = u; return true; }
                    if (Augment(o, avail)) { mBox[t] = u; return true; }
                }
            return false;
        }
        bool MatchOK(ReadOnlySpan<int> boxes, int skipBox, int skipBase)
        {
            curN = 0;
            for (int i = 0; i < boxes.Length; i++) if (i != skipBox) curBox[curN++] = boxes[i];
            Array.Copy(freeM, availM, nbw);
            if (skipBase >= 0) availM[skipBase >> 6] &= ~(1UL << (skipBase & 63));
            Array.Fill(mBox, -1); Array.Clear(matchedM);
            for (int u = 0; u < curN; u++) { Array.Clear(visM); if (!Augment(u, availM)) return false; }
            return true;
        }

        // matchSkip (exp-583): matchOK(boxes, skipBox, skipBase) from one maximum matching M of all the node's boxes per expansion.
        //  |M| <= n-2: removing one box leaves a deficiency, so false. M perfect: skipBox's base is freed, at most one alternating-path
        //  search from skipBase's box (Berge). |M| = n-1 (box u0 unmatched): skipBox must be reached from u0 by an even alternating path;
        //  flipping that path gives a maximum matching M1 perfect on the other boxes, then as above.
        int fmGen, fmDef, fmU0; int[] fmBox = [], fmOf = [], fm1 = [], fmPar = [], fmBy = []; int[] fmQ = [];
        public static bool MatchCheck;
        static readonly bool HashHook = Environment.GetEnvironmentVariable("SOKSOLV_P4HASH") == "1";

        bool MatchSkip(ReadOnlySpan<int> boxes, int skipBox, int skipBase)
        {
            if (fmGen != expGen) { fmGen = expGen; FmPrep(boxes); }
            bool r = MatchSkipOK(boxes, skipBox, skipBase);
            if (MatchCheck && r != MatchOK(boxes, skipBox, skipBase)) throw new InvalidOperationException($"matchSkip {r} != matchOK for box {skipBox} base {skipBase}");
            return r;
        }

        void FmPrep(ReadOnlySpan<int> boxes)
        {
            curN = boxes.Length; for (int i = 0; i < curN; i++) curBox[i] = boxes[i];
            Array.Fill(mBox, -1); Array.Clear(matchedM);
            fmDef = 0; fmU0 = -1;
            for (int u = 0; u < curN; u++)
            {
                int bo = curBox[u] * nbw; bool done = false;
                for (int w = 0; w < nbw; w++)
                {
                    ulong m = cellBases[bo + w] & freeM[w] & ~matchedM[w];
                    if (m == 0) continue;
                    int bit = System.Numerics.BitOperations.TrailingZeroCount(m);
                    matchedM[w] |= 1UL << bit; mBox[(w << 6) + bit] = u; done = true; break;
                }
                if (done) continue;
                Array.Clear(visM);
                if (!Augment(u, freeM)) { fmDef++; if (fmU0 < 0) fmU0 = u; }
            }
            if (fmBox.Length != mBox.Length) fmBox = new int[mBox.Length];
            Array.Copy(mBox, fmBox, mBox.Length);
            if (fmOf.Length < boxes.Length) fmOf = new int[boxes.Length];
            for (int i = 0; i < boxes.Length; i++) fmOf[i] = -1;
            for (int t = 0; t < fmBox.Length; t++) if (fmBox[t] >= 0) fmOf[fmBox[t]] = t;
        }

        bool MatchSkipOK(ReadOnlySpan<int> boxes, int skipBox, int skipBase)
        {
            if (fmDef >= 2) return false;
            if (fmDef == 0)
            {
                int fb = fmOf[skipBox];
                if (fb == skipBase || fmBox[skipBase] < 0) return true; // M minus skipBox's pair already avoids skipBase
                return AltPath(boxes, fmBox, fmBox[skipBase], skipBase, fb);
            }
            if (fm1.Length != fmBox.Length) fm1 = new int[fmBox.Length];
            Array.Copy(fmBox, fm1, fmBox.Length);
            if (skipBox != fmU0)
            {
                // BFS over alternating paths from u0 (every base met is matched: M is maximum); flip the path to skipBox
                if (fmPar.Length < boxes.Length) fmPar = new int[boxes.Length];
                if (fmBy.Length < mBox.Length) fmBy = new int[mBox.Length];
                Array.Clear(visM);
                if (fmQ.Length < boxes.Length + 1) fmQ = new int[boxes.Length + 1];
                int qn = 0; fmQ[qn++] = fmU0;
                bool found = false;
                for (int qi = 0; qi < qn && !found; qi++)
                {
                    int cell = boxes[fmQ[qi]];
                    for (int w = 0; w < nbw && !found; w++)
                        while (true)
                        {
                            ulong m = cellBases[cell * nbw + w] & freeM[w] & ~visM[w];
                            if (m == 0) break;
                            int bit = System.Numerics.BitOperations.TrailingZeroCount(m);
                            visM[w] |= 1UL << bit;
                            int t = (w << 6) + bit;
                            int x = fmBox[t];
                            fmPar[x] = t; // x is reached through base t (from box q[qi])
                            fmBy[t] = fmQ[qi];
                            if (x == skipBox) { found = true; break; }
                            fmQ[qn++] = x;
                        }
                }
                if (!found) return false;
                for (int x = skipBox; x != fmU0;)
                {
                    int t = fmPar[x];
                    int y = fmBy[t];
                    fm1[t] = y;
                    x = y;
                }
            }
            // fm1 leaves skipBox unmatched and is perfect on the other boxes
            if (fm1[skipBase] < 0) return true;
            return AltPath(boxes, fm1, fm1[skipBase], skipBase, -1);
        }

        /// <summary>Is there an alternating path (w.r.t. matching m, base -> box) from box u to a base that is free once skipBase's pair (and the base fb freed by the removed box; -1: none) is dropped, avoiding skipBase?</summary>
        bool AltPath(ReadOnlySpan<int> boxes, int[] m, int u, int skipBase, int fb)
        {
            Array.Copy(freeM, availM, nbw);
            availM[skipBase >> 6] &= ~(1UL << (skipBase & 63));
            Array.Clear(visM);
            return AltPathRec(boxes, m, u, fb);
        }
        bool AltPathRec(ReadOnlySpan<int> boxes, int[] m, int u, int fb)
        {
            int bo = boxes[u] * nbw;
            for (int w = 0; w < nbw; w++)
                while (true)
                {
                    ulong mk = cellBases[bo + w] & availM[w] & ~visM[w];
                    if (mk == 0) break;
                    int bit = System.Numerics.BitOperations.TrailingZeroCount(mk);
                    visM[w] |= 1UL << bit;
                    int t = (w << 6) + bit;
                    if (t == fb || m[t] < 0 || AltPathRec(boxes, m, m[t], fb)) return true;
                }
            return false;
        }

        // ---- region labelling -------------------------------------------------------------------------------------------------------
        int LabelRegions()
        {
            uint st = ++labStamp; int cnt = 0;
            var lg = labGen; var lb = lab; var bk = blk; var lq = labQ; var fc = floorCells; int w = W;
            for (int fi = 0; fi < fc.Length; fi++)
            {
                int c = fc[fi];
                if (bk[c] != 0 || lg[c] == st) continue;
                lg[c] = st; lb[c] = cnt; int tail = 0; lq[tail++] = c;
                for (int i = 0; i < tail; i++)
                {
                    int x = lq[i];
                    int y = x - w; if (lg[y] != st && bk[y] == 0) { lg[y] = st; lb[y] = cnt; lq[tail++] = y; }
                    y = x + w; if (lg[y] != st && bk[y] == 0) { lg[y] = st; lb[y] = cnt; lq[tail++] = y; }
                    y = x - 1; if (lg[y] != st && bk[y] == 0) { lg[y] = st; lb[y] = cnt; lq[tail++] = y; }
                    y = x + 1; if (lg[y] != st && bk[y] == 0) { lg[y] = st; lb[y] = cnt; lq[tail++] = y; }
                }
                cnt++;
            }
            return cnt;
        }

        // ---- separation ------------------------------------------------------------------------------------------------------------
        bool Separated(int a, int b)
        {
            int w = W;
            int dx = Math.Abs(a % w - b % w), dy = Math.Abs(a / w - b / w);
            if (dx + dy >= Plan4Sep && a != b) return true;
            for (int i = 0; i < sepQn; i++) sepD[sepQ[i]] = -1;
            int tail = 0; sepQ[tail++] = a; sepD[a] = 0; bool near = false;
            for (int i = 0; i < tail && !near; i++)
            {
                int x = sepQ[i];
                if (sepD[x] >= Plan4Sep - 1) continue;
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (wall[y] || sepD[y] >= 0) continue;
                    sepD[y] = sepD[x] + 1; sepQ[tail++] = y;
                    if (y == b) { near = true; break; }
                }
            }
            sepQn = tail;
            if (!near && a != b) return true;
            if (!s.HasRooms) return false;
            if (RoomSep) return FestRoomSep(a, b);
            int ra = s.rmCell[a], rb = s.rmCell[b];
            if (ra >= 0 && rb >= 0) return ra != rb;
            if (ra != rb) return a / w != b / w && a % w != b % w;
            return false;
        }
        bool FestRoomSep(int a, int b)
        {
            int ra = s.rmCell[a], rb = s.rmCell[b];
            if (ra < 0) ra = -1; if (rb < 0) rb = -1;
            if (ra >= 0 && rb >= 0) return ra != rb;
            if (ra == rb) return false;
            bool Near(int r, int c)
            {
                if (s.rmCell[c] == r) return true;
                foreach (int d in dirs) if (s.rmCell[c + d] == r) return true;
                return false;
            }
            if ((ra >= 0 && Near(ra, b)) || (rb >= 0 && Near(rb, a))) return false;
            int w = W;
            return a / w != b / w && a % w != b % w;
        }

        // ---- sink gating: the largest tied group of mutually push-reachable unused unoccupied bases ---------------------------------
        int[] sccStkA = [];
        void ComputeElim4()
        {
            int nb = baseCell.Length;
            new Span<bool>(pElim, nb).Clear();
            if (elimFr.Length < baseCell.Length) elimFr = new int[baseCell.Length];
            elimN = 0;
            for (int i = 0; i < freeN; i++) { int t = freeA[i]; if (!occ[baseCell[t]]) elimFr[elimN++] = t; }
            int m = elimN; if (m == 0) return;
            if (sccIdx.Length < cells)
            {
                sccIdx = new int[cells]; sccLow = new int[cells]; sccCmp = new int[cells]; sccCnt = new int[cells + 1]; sccCall = new int[cells]; sccDir = new byte[cells]; sccStkA = new int[cells + 1];
            }
            bstamp++; uint st = bstamp;
            var stk = sccStkA; int stn = 0;
            var blkL = blk; var idx = sccIdx; var low = sccLow; var cmp = sccCmp; var call = sccCall; var dir = sccDir; var bs = bseen; var dr = dirs;
            int next = 0, ncomp = 0;
            var fr = new Span<int>(elimFr, 0, elimN);
            for (int fi = 0; fi < fr.Length; fi++)
            {
                int root = baseCell[fr[fi]];
                if (bs[root] == st) continue;
                bs[root] = st; idx[root] = next; low[root] = next; cmp[root] = -1; next++;
                stk[stn++] = root;
                call[0] = root; dir[0] = 0; int sp = 1;
                while (sp > 0)
                {
                    int top = sp - 1, v = call[top];
                    int di = dir[top];
                    if (di < 4)
                    {
                        dir[top] = (byte)(di + 1);
                        int to = v + dr[di], back = v - dr[di];
                        if (blkL[to] != 0 || blkL[back] != 0) continue;
                        if (bs[to] != st)
                        {
                            bs[to] = st; idx[to] = next; low[to] = next; cmp[to] = -1; next++;
                            stk[stn++] = to;
                            call[sp] = to; dir[sp] = 0; sp++;
                        }
                        else if (cmp[to] < 0 && idx[to] < low[v]) low[v] = idx[to];
                        continue;
                    }
                    sp = top;
                    if (low[v] == idx[v])
                    {
                        while (true)
                        {
                            int w2 = stk[--stn];
                            cmp[w2] = ncomp;
                            if (w2 == v) break;
                        }
                        ncomp++;
                    }
                    if (sp > 0) { int u = call[sp - 1]; if (low[v] < low[u]) low[u] = low[v]; }
                }
            }
            Array.Clear(sccCnt, 0, ncomp);
            int best = 0;
            for (int fi = 0; fi < fr.Length; fi++) { int c = cmp[baseCell[fr[fi]]]; sccCnt[c]++; best = Math.Max(best, sccCnt[c]); }
            for (int fi = 0; fi < fr.Length; fi++) if (sccCnt[cmp[baseCell[fr[fi]]]] == best) elimOK[fr[fi]] = true;
        }

        // ---- expansion --------------------------------------------------------------------------------------------------------------
        int[] boxesBuf = new int[64]; readonly int[] pullQ_; int[] pullQ => pullQ_;
        static T[] PinArr<T>(int n) where T : unmanaged => GC.AllocateArray<T>(n, pinned: true);
        int expGen; public static readonly long[] T4 = new long[8];
        void Expand4(int id)
        {
            long t0 = Prof.Start();
            expGen++;
            ref var nd0 = ref nodes[id];
            int nbx = nodes[id].Nb; if (boxesBuf.Length < nbx) boxesBuf = new int[nbx * 2];
            Boxes(id).CopyTo(boxesBuf); var boxes = new ReadOnlySpan<int>(boxesBuf, 0, nbx);
            SetOcc(id);
            var used = Used(id);
            var (_, parentStamp) = Flood(nd0.Player);
            pg++;
            CutNewNode();
            { uint* pgp = pPgen; uint pgv = pg; var qu = queue; for (int i = 0; i < qlen; i++) pgp[qu[i]] = pgv; }
            BuildFree(used);
            bool sinks = freeN > 0;
            if (sinks)
            {
                ComputeElim4();
                bool any = false;
                for (int i = 0; i < freeN; i++)
                { int t = freeA[i]; if (pElim[t]) { any = true; if (pPgen[baseCell[t]] != pg) sinks = false; } }
                if (!any) sinks = false;
            }
            int aw = reach[0].Length;
            new Span<ulong>(pAlive, aw).Clear();
            for (int i = 0; i < freeN; i++) { var rt = reach[freeA[i]]; for (int w = 0; w < aw; w++) pAlive[w] |= rt[w]; }
            candN = 0; remN = 0; region0N = 0;
            int baseR = LabelRegions();
            if (Prof.On) T4[0] += System.Diagnostics.Stopwatch.GetTimestamp() - t0;
            long t1 = Prof.Start();
            if (region0.Length < nbx) region0 = new int[nbx * 2];
            int dd0 = dirs[0], dd1 = dirs[1], dd2 = dirs[2], dd3 = dirs[3];
            byte* bk = pBlk; uint* seenKp = pSeenK; uint* pgp2 = pPgen; int* q = pQ; int* bAt = pBaseAt; ulong* alv = pAlive;
            for (int bi = 0; bi < nbx; bi++)
            {
                int b = boxes[bi];
                int rb = baseR + 1;
                { // distinct free components next to the box (lab is valid where labGen == labStamp: free cells of the unlifted board)
                    int l0 = -1, l1 = -1, l2 = -1, k = 0;
                    for (int di = 0; di < 4; di++)
                    {
                        int y = b + (di == 0 ? dd0 : di == 1 ? dd1 : di == 2 ? dd2 : dd3);
                        if (labGen[y] == labStamp && lab[y] >= 0)
                        {
                            int l = lab[y];
                            if (k == 0 || (l != l0 && (k < 2 || l != l1) && (k < 3 || l != l2))) { if (k == 0) l0 = l; else if (k == 1) l1 = l; else if (k == 2) l2 = l; k++; }
                        }
                    }
                    if (k > 0) rb = baseR - k + 1;
                }
                if ((bi & 7) == 7 && Stopped()) break;
                boxGen++; uint g = boxGen;
                int qc = 0;
                for (int di = 0; di < 4; di++)
                {
                    int d = di == 0 ? dd0 : di == 1 ? dd1 : di == 2 ? dd2 : dd3;
                    if (pgp2[b + d] == pg) { int kk = b * 4 + di; q[qc++] = kk; seenKp[kk] = g; }
                }
                CutBegin(b);
                SetOccCell(b, false);
                bool sunk = false;
                for (int qi = 0; qi < qc; qi++)
                {
                    int kq = q[qi]; int c = kq >> 2, di = kq & 3;
                    int d = di == 0 ? dd0 : di == 1 ? dd1 : di == 2 ? dd2 : dd3;
                    int to = c + d, player = to + d;
                    if (bk[to] != 0 || bk[player] != 0) continue;
                    var qq = cut.CutQ(to);
                    int sides = (int)(qq->Sd >> (di << 3) & 15), np = qq->Np[di];
                    if (CheckCut)
                    {
                        var f = QueryFlood(to, player);
                        if (f != (np, sides)) throw new InvalidOperationException($"cutscan query ({to},{player}): scan {(np, sides)} != flood {f}");
                    }
                    for (int m = sides; m != 0; m &= m - 1)
                    {
                        int k2 = to * 4 + BitOperations.TrailingZeroCount(m);
                        if (seenKp[k2] != g) { seenKp[k2] = g; q[qc++] = k2; }
                    }
                    int t = bAt[to];
                    if (sinks && !sunk && t >= 0 && (used[t >> 6] >> (t & 63) & 1) == 0 && pElim[t])
                    {
                        if (Fest) sinkTo.Add(to);
                        else sunk = TrySink(id, boxes, b, bi, to, t, rb) || sunk;
                    }
                    if ((alv[to >> 6] >> (to & 63) & 1) == 0) continue;
                    if ((qq->Emit >> di & 1) != 0) continue;   // this (to, component) end was already emitted for the box
                    qq->Emit |= (uint)sides;
                    int pc = qq->Pc;
                    if (CheckCut) { int f = PiecesFlood(to); if (f != pc) throw new InvalidOperationException($"cutscan pieces ({to}): scan {pc} != flood {f}"); }
                    if (candN == candA.Length) Array.Resize(ref candA, candN * 2);
                    candA[candN++] = new P2Cand { Box = bi, To = to, Player = np, Pieces = pc };
                }
                if (sinkTo.Count > 0)
                {
                    SortUtil.Insertion(sinkTo);
                    foreach (int to in sinkTo) if (TrySink(id, boxes, b, bi, to, baseAt[to], rb)) break;
                    sinkTo.Clear();
                }
                region0[region0N++] = rb;
                SetOccCell(b, true);
            }
            if (Prof.On) T4[1] += System.Diagnostics.Stopwatch.GetTimestamp() - t1;
            long t2 = Prof.Start();
            ref var nd = ref nodes[id];
            if (remN > 0 && KeepParks > 0) { KeepParks4(id, boxes); return; }
            if (remN > 0)
            {
                int adv = -1; P2Kid advCh = default;
                if (Fest) { adv = FestSinkAdv(boxes); if (adv >= 0) advCh = remK[adv]; }
                SortKidsK(remK, remN);
                if (adv >= 0)
                    for (int i = 0; i < remN; i++)
                        if (remK[i].From == advCh.From && remK[i].To == advCh.To) { SetAdv(id, remK, remN, i); break; }
                nd.COff = PutKids(remK, remN);
                nd.CLen = remN; nd.CSorted = remN;
                return;
            }
            int advSorted = ParkKids4(id, boxes, Fest);
            nd.COff = PutKids(beamK, beamN);
            nd.CLen = beamN; nd.CSorted = advSorted;
            if (LazyOff) EnsureSorted(id, beamN);
            if (Prof.On) T4[2] += System.Diagnostics.Stopwatch.GetTimestamp() - t2;
        }

        bool TrySink(int id, ReadOnlySpan<int> boxes, int b, int bi, int to, int t, int rb)
        {
            bool ok = Separated(b, to);
            if (ok && matchOn && !MatchSkip(boxes, bi, t)) ok = false;
            if (ok)
            {
                ref var nd = ref nodes[id];
                int npl = CompMinWithLift(nd.Player);
                int goals = nd.Goals; if (goal[b]) goals--;
                int dist = nd.Dist - distance[b];
                if (remN == remK.Length) Array.Resize(ref remK, remN * 2);
                remK[remN++] = new P2Kid { From = (short)b, To = (short)to, Player = (short)npl, Base = (short)t, Key = Key(nd.Nb - 1, rb, goals, dist, 0) };
            }
            return ok;
        }

        /// <summary>cs.compMin(v): smallest cell of v's component in the free graph with the lifted box free (the caller has cleared occ[b]).</summary>
        int CompMinWithLift(int v)
        {
            int c = CutCompMin(v);
            if (CheckCut) { int f = Flood(v).norm; if (f != c) throw new InvalidOperationException($"cutscan compMin ({v}): scan {c} != flood {f}"); }
            return c;
        }

        // sink children first (key-sorted, then park children), keep-parks mode
        void KeepParks4(int id, ReadOnlySpan<int> boxes)
        {
            int adv = -1; P2Kid advCh = default;
            if (Fest) { adv = FestSinkAdv(boxes); if (adv >= 0) advCh = remK[adv]; }
            SortKidsK(remK, remN);
            if (adv >= 0)
                for (int i = 0; i < remN; i++)
                    if (remK[i].From == advCh.From && remK[i].To == advCh.To) { SetAdv(id, remK, remN, i); break; }
            if (Fest && KeepParks == 1)
            {
                while (nodeAdv.Count <= id) nodeAdv.Add(0);
                nodeAdv[id] = (byte)Math.Min(remN, 255);
            }
            ParkKids4(id, boxes, false);
            if (remN + beamN > remK.Length) Array.Resize(ref remK, Math.Max(remK.Length * 2, remN + beamN));
            Array.Copy(beamK, 0, remK, remN, beamN);
            ref var nd = ref nodes[id];
            nd.CSorted = remN; remN += beamN;
            nd.COff = PutKids(remK, remN);
            nd.CLen = remN;
        }

        /// <summary>Beam of park children: stable key sort truncated to Plan4Beam; with advMode the Festival packing advisor (chosen over the whole beam) is moved to the front (SetAdv).</summary>
        int ParkKids4(int id, ReadOnlySpan<int> boxes, bool advMode)
        {
            ref var nd = ref nodes[id];
            beamN = 0;
            if (beamK.Length < candN) beamK = new P2Kid[Math.Max(candN, beamK.Length * 2)];
            var ca = candA; var bm = beamK; int nbx = nd.Nb, goalsP = nd.Goals, distP = nd.Dist;
            for (int ci = 0; ci < candN; ci++)
            {
                ref readonly var c = ref ca[ci];
                int b = boxes[c.Box];
                int regions = region0[c.Box] - 1 + c.Pieces;
                int goals = goalsP; if (goal[b]) goals--; if (goal[c.To]) goals++;
                int dist = distP - distance[b] + distance[c.To];
                bm[beamN++] = new P2Kid { Key = Key(nbx, regions, goals, dist, 0), From = (short)b, To = (short)c.To, Player = (short)c.Player, Base = -1 };
            }
            int keep = Plan4Beam > 0 ? Plan4Beam : int.MaxValue;
            if (advMode)
            {
                while (nodeAdv.Count <= id) nodeAdv.Add(0);
                int ai = FestPackAdvU(id);
                if (ai >= 0)
                {
                    var adv = beamK[ai];
                    if (nodeAdv[id] != 0) { SortTop(beamK, ref beamN, int.MaxValue); SetAdv(id, beamK, beamN, FestPackAdv(id)); if (beamN > keep) beamN = keep; return beamN; }
                    SortTop(beamK, ref beamN, keep, false);   // the first `keep` in stable key order, left in input order
                    int pos = -1;
                    for (int i = 0; i < beamN; i++) if (beamK[i].Key == adv.Key && beamK[i].From == adv.From && beamK[i].To == adv.To && beamK[i].Player == adv.Player) { pos = i; break; }
                    if (pos < 0)
                    {
                        if (beamN == keep)
                        {   // drop the child that is last in stable key order (the latest among the largest keys)
                            int last = 0; ulong mk = beamK[0].Key;
                            for (int i = 1; i < beamN; i++) if (beamK[i].Key >= mk) { mk = beamK[i].Key; last = i; }
                            Array.Copy(beamK, last + 1, beamK, last, beamN - last - 1); beamN--;
                        }
                        if (beamK.Length < beamN + 1) Array.Resize(ref beamK, beamN * 2 + 1);
                        pos = beamN++;
                    }
                    Array.Copy(beamK, 0, beamK, 1, pos);
                    beamK[0] = adv; nodeAdv[id]++;
                    return 1;
                }
            }
            SortTop(beamK, ref beamN, keep, false);
            return 0;
        }

        /// <summary>FestPackAdv over the unsorted beam: the index minimising (regions, -walk, key, index) = the first best in sorted order.</summary>
        int FestPackAdvU(int id)
        {
            ref var nd = ref nodes[id];
            ulong pr = nd.Key >> 42 & 1023;
            int best = -1; ulong bR = 0; int bL = 0; ulong bK = 0;
            for (int i = 0; i < beamN; i++)
            {
                ulong r = beamK[i].Key >> 42 & 1023;
                if (r > pr || !goal[beamK[i].From]) continue;
                if (distance[beamK[i].To] <= distance[beamK[i].From]) continue;
                int l = s.MWalk(beamK[i].To, beamK[i].From);
                if (best < 0 || r < bR || (r == bR && (l > bL || (l == bL && beamK[i].Key < bK)))) { best = i; bR = r; bL = l; bK = beamK[i].Key; }
            }
            return best;
        }

        void SetAdv(int id, P2Kid[] ch, int nch, int i)
        {
            while (nodeAdv.Count <= id) nodeAdv.Add(0);
            if (i < 0) return;
            int k = nodeAdv[id];
            if (i < k) return;
            var x = ch[i];
            Array.Copy(ch, k, ch, k + 1, i - k);
            ch[k] = x;
            nodeAdv[id]++;
        }

        int FestSinkAdv(ReadOnlySpan<int> boxes)
        {
            for (int i = 0; i < remN; i++) if (!goal[remK[i].From]) return i;
            int best = -1, bR = 0, bD = 0;
            for (int i = 0; i < remN; i++)
            {
                SetOccCell(remK[i].From, false); SetOccCell(remK[i].To, true);
                int r = Regions();
                SetOccCell(remK[i].To, false); SetOccCell(remK[i].From, true);
                int d = s.MWalk(remK[i].To, remK[i].From);
                if (best < 0 || r < bR || (r == bR && d > bD)) { best = i; bR = r; bD = d; }
            }
            return best;
        }

        int FestPackAdv(int id)
        {
            ref var nd = ref nodes[id];
            ulong pr = nd.Key >> 42 & 1023;
            int best = -1; ulong bR = 0; int bL = 0;
            for (int i = 0; i < beamN; i++)
            {
                ulong r = beamK[i].Key >> 42 & 1023;
                if (r > pr || !goal[beamK[i].From]) continue;
                if (distance[beamK[i].To] <= distance[beamK[i].From]) continue;
                int l = s.MWalk(beamK[i].To, beamK[i].From);
                if (best < 0 || r < bR || (r == bR && l > bL)) { best = i; bR = r; bL = l; }
            }
            return best;
        }

        /// <summary>Stable sort by key (Go: slices.SortStableFunc / radix, same result): LSD radix over the bits where the keys differ
        /// (compressed with PEXT when available), carrying (key, index) records instead of the kids themselves.</summary>
        struct SRec { public ulong K; public int I; }
        static readonly bool UsePext = Bmi2.X64.IsSupported && Environment.GetEnvironmentVariable("SOKSOLV_NOPEXT") != "1";
        SRec[] srA = new SRec[1024], srB = new SRec[1024]; readonly int[] shist = new int[6 * 2048];
        void SortKidsK(P2Kid[] a, int cnt) { int c = cnt; SortTop(a, ref c, int.MaxValue); }

        /// <summary>Stable key sort of a[0..cnt), keeping only the first <paramref name="keep"/> (cnt is reduced): threshold selection on the
        /// compressed key, then an LSD radix sort of the survivors.</summary>
        void SortTop(P2Kid[] a, ref int cntRef, int keep, bool sort = true)
        {
            int cnt = cntRef;
            if (cnt < 2 || (!sort && cnt <= keep)) return;
            if (sort && cnt < 40)
            {
                for (int i = 1; i < cnt; i++)
                {
                    var x = a[i]; int j = i;
                    while (j > 0 && a[j - 1].Key > x.Key) { a[j] = a[j - 1]; j--; }
                    a[j] = x;
                }
                if (cnt > keep) cntRef = keep;
                return;
            }
            ulong x0 = a[0].Key, diff = 0;
            for (int i = 1; i < cnt; i++) diff |= a[i].Key ^ x0;
            if (diff == 0) { if (cnt > keep) cntRef = keep; return; }   // equal keys: the stable order is the input order
            if (srA.Length < cnt) { srA = new SRec[cnt * 2]; srB = new SRec[cnt * 2]; }
            if (sortK.Length < cnt) sortK = new P2Kid[cnt * 2];
            var src = srA; var dst = srB; int nbits;
            if (UsePext)
            {
                for (int i = 0; i < cnt; i++) { src[i].K = Bmi2.X64.ParallelBitExtract(a[i].Key, diff); src[i].I = i; }
                nbits = BitOperations.PopCount(diff);
            }
            else
            {
                // software compress: the differing bits form a few runs of consecutive bits; pack them in order
                int nr = 0, dpos = 0; ulong mrest = diff;
                Span<int> rsh = stackalloc int[8], rdp = stackalloc int[8]; Span<ulong> rmk = stackalloc ulong[8];
                while (mrest != 0 && nr < 8)
                {
                    int lo = BitOperations.TrailingZeroCount(mrest);
                    int len = BitOperations.TrailingZeroCount(~(mrest >> lo));
                    ulong mk = len >= 64 ? ~0UL : (1UL << len) - 1;
                    rsh[nr] = lo; rdp[nr] = dpos; rmk[nr] = mk; nr++; dpos += len;
                    mrest &= ~(mk << lo);
                }
                if (mrest == 0)
                {
                    nbits = dpos;
                    for (int i = 0; i < cnt; i++)
                    {
                        ulong key = a[i].Key, k = 0;
                        for (int r = 0; r < nr; r++) k |= (key >> rsh[r] & rmk[r]) << rdp[r];
                        src[i].K = k; src[i].I = i;
                    }
                }
                else
                {
                    int lo = BitOperations.TrailingZeroCount(diff);
                    nbits = 64 - BitOperations.LeadingZeroCount(diff) - lo;
                    ulong keepBits = nbits >= 64 ? ~0UL : (1UL << nbits) - 1;   // the bits above the differing span are shared by every key
                    for (int i = 0; i < cnt; i++) { src[i].K = (a[i].Key >> lo) & keepBits; src[i].I = i; }
                }
            }
            // balanced digits of at most 11 bits: nbits=12 -> two 6-bit passes
            int passes = (nbits + 10) / 11, w = (nbits + passes - 1) / passes, bucket = 1 << w, dmask = bucket - 1;
            var h = shist;
            int n = cnt;
            if (cnt > keep)
            {
                // threshold T = the keep-th smallest compressed key; survivors: K < T, plus the first (keep - below) with K == T (stability)
                ulong prefix = 0; int below = 0;
                for (int p = passes - 1; p >= 0; p--)
                {
                    int sh = p * w; Array.Clear(h, 0, bucket);
                    if (p == passes - 1) for (int i = 0; i < cnt; i++) h[(int)(src[i].K >> sh) & dmask]++;
                    else for (int i = 0; i < cnt; i++) { ulong k = src[i].K; if (k >> (sh + w) == prefix) h[(int)(k >> sh) & dmask]++; }
                    int cum = below, d = 0;
                    while (cum + h[d] < keep) { cum += h[d]; d++; }
                    below = cum; prefix = prefix << w | (uint)d;
                }
                int quota = keep - below, m = 0;
                for (int i = 0; i < cnt; i++)
                {
                    var r = src[i];
                    if (r.K < prefix) dst[m++] = r;
                    else if (r.K == prefix && quota > 0) { quota--; dst[m++] = r; }
                }
                (src, dst) = (dst, src);
                n = m;
            }
            if (!sort)
            {
                // survivors in input order (the lazy kid order sorts them on demand)
                var t2 = sortK;
                for (int i = 0; i < n; i++) t2[i] = a[src[i].I];
                Array.Copy(t2, a, n); cntRef = n; return;
            }
            Array.Clear(h, 0, passes * bucket);
            for (int i = 0; i < n; i++)
            {
                ulong k = src[i].K;
                for (int p = 0; p < passes; p++) h[(p << w) + ((int)(k >> (p * w)) & dmask)]++;
            }
            for (int p = 0; p < passes; p++)
            {
                int off = p << w, sh = p * w;
                if (h[off + ((int)(src[0].K >> sh) & dmask)] == n) continue;
                int sum = 0;
                for (int v = 0; v < bucket; v++) { int c = h[off + v]; h[off + v] = sum; sum += c; }
                for (int i = 0; i < n; i++)
                {
                    var r = src[i];
                    dst[h[off + ((int)(r.K >> sh) & dmask)]++] = r;
                }
                (src, dst) = (dst, src);
            }
            var tmp = sortK;
            for (int i = 0; i < n; i++) tmp[i] = a[src[i].I];
            Array.Copy(tmp, a, n);
            cntRef = n;
        }

        /// <summary>Stable LSD radix sort on the 64-bit key, skipping bytes every child shares (Go sortKids); planner v2 path.</summary>
        void SortKids(P2Child[] a, int cnt)
        {
            if (cnt < 2) return;
            if (cnt < 48)
            {
                for (int i = 1; i < cnt; i++)
                {
                    var x = a[i]; int j = i;
                    while (j > 0 && a[j - 1].Key > x.Key) { a[j] = a[j - 1]; j--; }
                    a[j] = x;
                }
                return;
            }
            if (sortTmp.Length < cnt) sortTmp = new P2Child[Math.Max(cnt, sortTmp.Length * 2)];
            RadixSortKids(a, cnt);
        }

        int[] sortCnt = new int[8 * 256];
        void RadixSortKids(P2Child[] a, int n)
        {
            var cnt = sortCnt; Array.Clear(cnt);
            for (int i = 0; i < n; i++)
            {
                ulong k = a[i].Key;
                for (int b = 0; b < 8; b++) cnt[b * 256 + (int)(k >> (8 * b) & 255)]++;
            }
            P2Child[] src = a, dst = sortTmp;
            for (int b = 0; b < 8; b++)
            {
                int off = b * 256;
                if (cnt[off + (int)(a[0].Key >> (8 * b) & 255)] == n) continue; // every key has this byte
                int sum = 0;
                for (int v = 0; v < 256; v++) { int c = cnt[off + v]; cnt[off + v] = sum; sum += c; }
                for (int i = 0; i < n; i++)
                {
                    int v = (int)(src[i].Key >> (8 * b) & 255);
                    dst[cnt[off + v]++] = src[i];
                }
                (src, dst) = (dst, src);
            }
            if (!ReferenceEquals(src, a)) Array.Copy(src, a, n);
        }
        static void MergeSort(P2Child[] a, P2Child[] t, int lo, int hi)
        {
            if (hi - lo < 2) return;
            int mid = (lo + hi) >> 1;
            MergeSort(a, t, lo, mid); MergeSort(a, t, mid, hi);
            int i = lo, j = mid, k = lo;
            while (i < mid && j < hi) t[k++] = a[j].Key < a[i].Key ? a[j++] : a[i++];
            while (i < mid) t[k++] = a[i++];
            while (j < hi) t[k++] = a[j++];
            Array.Copy(t, lo, a, lo, hi - lo);
        }

        // ---- node store ------------------------------------------------------------------------------------------------------------
        int AddNode(int parent, in P2Child c)
        {
            if (!SeenInsert(c.Hash)) return -1;
            ref var pn = ref nodes[parent];
            int off = boxN;
            bool removed = c.Base >= 0;
            EnsureBoxes(pn.Nb);
            for (int i = 0; i < pn.Nb; i++)
            {
                int b = boxArena[pn.BOff + i];
                if (b == c.From) { if (!removed) boxArena[boxN++] = c.To; continue; }
                boxArena[boxN++] = b;
            }
            int id = nodeN;
            var nn = new P2Node { Parent = parent, BOff = off, Nb = pn.Nb, Player = c.Player, Hash = c.Hash, Key = c.Key, Goals = pn.Goals, Dist = pn.Dist, MFrom = c.From, MTo = c.To, MBase = c.Base };
            if (removed) nn.Nb--;
            if (goal[c.From]) nn.Goals--;
            nn.Dist -= distance[c.From];
            if (!removed) { if (goal[c.To]) nn.Goals++; nn.Dist += distance[c.To]; }
            PushNode(nn);
            EnsureUsed();
            Array.Copy(usedArena, parent * nw, usedArena, id * nw, nw); usedN += nw;
            if (removed) usedArena[id * nw + (c.Base >> 6)] |= 1UL << (c.Base & 63);
            return id;
        }
        void EnsureBoxes(int add) { if (boxN + add > boxArena.Length) Array.Resize(ref boxArena, Math.Max(boxArena.Length * 2, boxN + add)); }
        void EnsureUsed() { if (usedN + nw > usedArena.Length) Array.Resize(ref usedArena, Math.Max(usedArena.Length * 2, usedN + nw)); }
        void PushNode(in P2Node nn) { if (nodeN == nodes.Length) Array.Resize(ref nodes, nodeN * 2); nodes[nodeN++] = nn; }

        int CellOf(ulong key)
        {
            ulong k = key >> 26;
            if (cellIdx.TryGetValue(k, out int i)) return i;
            i = cellHeap.Count; cellIdx[k] = i; cellHeap.Add(new P2Heap());
            return i;
        }

        void Enqueue(int id)
        {
            ref var nd = ref nodes[id];
            if (nd.CNext >= nd.CLen) return;
            seq++;
            int c = CellOf(nd.Key);
            int w = 0;
            if (Weighted) { w = nodeW[id]; if (id >= nodeAdv.Count || nd.CNext >= nodeAdv[id]) w++; }
            cellHeap[c].Push(new P2Entry { Key = KidKey(id, nd.CNext), Seq = seq, Node = id, W = w });
        }

        bool AtStart(int id)
        {
            var used = Used(id);
            foreach (int b in Boxes(id))
            {
                int t = baseAt[b];
                if (t < 0 || (used[t >> 6] >> (t & 63) & 1) != 0) return false;
            }
            SetOcc(id);
            var (_, st) = Flood(nodes[id].Player);
            return mark[s.lv.Player] == st;
        }


        static readonly bool TraceOn = Environment.GetEnvironmentVariable("SOKSOLV_P4TRACE") == "1";
        void TraceNode(int id)
        {
            if (!TraceOn) return;
            ref var nd = ref nodes[id]; ulong h = 1469598103934665603UL;
            for (int i = 0; i < nd.CLen; i++) { KidKey(id, i); var k = kids[nd.COff + i]; foreach (long v in new long[] { (long)k.Key, k.From, k.To, k.Player, k.Base }) h = (h ^ (ulong)v) * 1099511628211UL; }
            Console.Error.WriteLine($"TR {id} nb={nd.Nb} clen={nd.CLen} {h:x16}");
        }
        public bool Start4()
        {
            ClearOcc();
            foreach (int g in s.goals) SetOccCell(g, true);
            var roots = new List<int>(); var covered = new bool[cells];
            foreach (int c in floorCells)
                if (!occ[c] && !covered[c])
                {
                    var (norm, st) = Flood(c);
                    roots.Add(norm);
                    for (int i = 0; i < qlen; i++) if (mark[queue[i]] == st) covered[queue[i]] = true;
                }
            if (roots.Count == 0) return false;
            int gsum = 0; foreach (int g in s.goals) gsum += distance[g];
            foreach (int r in roots)
            {
                ulong h = zp[r]; foreach (int g in s.goals) h ^= zb[g];
                EnsureBoxes(s.goals.Length);
                int off = boxN; foreach (int g in s.goals) boxArena[boxN++] = g;
                PushNode(new P2Node { Parent = -1, BOff = off, Nb = s.goals.Length, Player = r, Hash = h, Goals = s.goals.Length, Dist = gsum, MBase = -1, Key = Key(s.goals.Length, 1, s.goals.Length, gsum, 0) });
                EnsureUsed(); Array.Clear(usedArena, usedN, nw); usedN += nw;
                SeenInsert(h);
                if (Weighted) nodeW.Add(0);
            }
            if (matchOn)
            {
                SetOcc(0);
                FreeAll();
                if (!MatchOK(Boxes(0), -1, -1)) matchOn = false;
            }
            int best = 0; bool done = false;
            for (int id = 0; id < nodeN; id++)
            {
                if (nodes[id].Key < nodes[best].Key) best = id;
                if (AtStart(id)) { best = id; done = true; break; }
                Expand4(id); TraceNode(id);
                Expanded++;
                Enqueue(id);
            }
            Best = best; Done = done;
            return true;
        }

        public void Step4(int maxIter)
        {
            int best = Best; bool done = Done; int tgt = Expanded + maxIter;
            while (!done && !Stopped() && Expanded < MaxExp && fails < 10000 && (maxIter <= 0 || Expanded < tgt))
            {
                int it = iter++;
                int c = -1;
                if ((it & 1) == 1)
                    if (cellIdx.TryGetValue(nodes[best].Key >> 26, out int bc) && cellHeap[bc].Count > 0) c = bc;
                if (c < 0)
                {
                    int nc = cellHeap.Count;
                    for (int k = 0; k < nc; k++)
                    {
                        int i = (rr + k) % nc;
                        if (cellHeap[i].Count > 0) { c = i; rr = i + 1; break; }
                    }
                }
                if (c < 0) { Exhausted = true; break; }
                var e = cellHeap[c].Pop();
                ref var pn = ref nodes[e.Node];
                var ch = Kid(e.Node, pn.CNext);
                pn.CNext++;
                Enqueue(e.Node);
                int id = AddNode(e.Node, ch);
                if (id < 0) { fails++; continue; }
                if (Weighted) nodeW.Add(e.W);
                if (nodes[id].Key < nodes[best].Key) best = id;
                if (nodes[id].Nb == 0 || AtStart(id)) { best = id; done = true; break; }
                fails = 0;
                Expand4(id); TraceNode(id);
                Expanded++;
                Enqueue(id);
                if (Prof.On && Expanded % 2000 == 0) Console.Error.WriteLine($"p4 expanded {Expanded} nodes {nodeN} kids {kids.Count} t={s.clock.ElapsedMilliseconds} ms | setup {T4[0]*1000/System.Diagnostics.Stopwatch.Frequency} boxloop {T4[1]*1000/System.Diagnostics.Stopwatch.Frequency} tail {T4[2]*1000/System.Diagnostics.Stopwatch.Frequency} ms");
            }
            Best = best; Done = done;
            if (fails >= 10000) Exhausted = true;
        }


        // ---- planner v2 (Go packing_plan2.go planner2: fa2's own planner, exp-411) ------------------------------------------------------
        public const int V2Beam = 48;

        /// <summary>planner2's goal-distance score: pull distance to the NEAREST goal (planner4 sums over all goals).</summary>
        public void UseV2Distance()
        {
            Array.Fill(distance, -1);
            var q = new List<int>();
            foreach (int g in s.goals) { distance[g] = 0; q.Add(g); }
            for (int i = 0; i < q.Count; i++)
                foreach (int d in dirs)
                {
                    int x = q[i] + d;
                    if (!wall[x] && distance[x] < 0) { distance[x] = distance[q[i]] + 1; q.Add(x); }
                }
            for (int i = 0; i < distance.Length; i++) distance[i] = Math.Max(distance[i], 0);
        }

        /// <summary>Go planner2.expand: removals when any box can reach an unused base (only those), else the best V2Beam plain pulls.</summary>
        void ExpandV2(int id)
        {
            expGen++;
            ref var nd0 = ref nodes[id];
            int nbx = nd0.Nb; if (boxesBuf.Length < nbx) boxesBuf = new int[nbx * 2];
            Boxes(id).CopyTo(boxesBuf); var boxes = new ReadOnlySpan<int>(boxesBuf, 0, nbx);
            SetOcc(id);
            var used = Used(id);
            var (_, parentStamp) = Flood(nd0.Player);
            pg++;
            CutNewNode();
            for (int i = 0; i < qlen; i++) pgen[queue[i]] = pg;
            BuildFree(used);
            cands.Clear(); remN = 0; region0N = 0;
            Array.Clear(demand);
            for (int fi = 0; fi < freeN; fi++) { int t = freeA[fi]; for (int i = 0; i < nbx; i++) if (CanReach(t, boxes[i])) demand[t]++; }
            int aw = reach[0].Length;
            Array.Clear(alive);
            for (int fi = 0; fi < freeN; fi++) { var rt = reach[freeA[fi]]; for (int w = 0; w < aw; w++) alive[w] |= rt[w]; }
            var usedArr = used.ToArray();
            bool IsFree(int t) => t >= 0 && (usedArr[t >> 6] >> (t & 63) & 1) == 0;
            var reachBases = new List<int>();
            for (int bi = 0; bi < nbx; bi++)
            {
                int b = boxes[bi];
                if ((bi & 7) == 7 && Stopped()) break;
                reachBases.Clear();
                { int t0 = baseAt[b]; if (IsFree(t0)) reachBases.Add(t0); }
                boxGen++; uint g = boxGen;
                int qc = 0; var q = pullQ;
                for (int di = 0; di < 4; di++)
                {
                    int d = dirs[di];
                    if (pgen[b + d] == pg) { int kk = b * 4 + di; q[qc++] = kk; seenK[kk] = g; }
                }
                CutBegin(b);
                SetOccCell(b, false);
                for (int qi = 0; qi < qc; qi++)
                {
                    int kq = q[qi]; int c = kq / 4, d = dirs[kq % 4];
                    int to = c + d, player = c + 2 * d;
                    if (wall[to] || occ[to] || wall[player] || occ[player]) continue;
                    var (np, sides) = Query(to, player, kq & 3);
                    for (int di = 0; di < 4; di++)
                    {
                        int k2 = to * 4 + di;
                        if ((sides & (1 << di)) != 0 && seenK[k2] != g) { seenK[k2] = g; q[qc++] = k2; }
                    }
                    int t = baseAt[to];
                    if (IsFree(t)) { reachBases.Add(t); continue; }
                    if ((alive[to >> 6] >> (to & 63) & 1) == 0) continue;
                    if (!NewEnd(to, np, g)) continue;
                    cands.Add(new P2Cand { Box = bi, To = to, Player = np, Pieces = Pieces(to) });
                }
                if (reachBases.Count > 0)
                {
                    reachBases.Sort((a, c2) => demand[a] != demand[c2] ? demand[a] - demand[c2] : a - c2);
                    int chosen = -1;
                    for (int ti = 0; ti < reachBases.Count; ti++)
                    {
                        if (ti >= 64) break;
                        if (!matchOn || MatchSkip(boxes, bi, reachBases[ti])) { chosen = reachBases[ti]; break; }
                    }
                    if (chosen >= 0)
                    {
                        ref var nd = ref nodes[id];
                        int np = Flood(nd.Player).norm;      // the box is gone: the player's region is its component in the board without it
                        int regions = Regions();             // box b is off the board
                        int goals = nd.Goals; if (goal[b]) goals--;
                        int dist = nd.Dist - distance[b];
                        var ch = new P2Child { From = b, To = baseCell[chosen], Player = np, Base = chosen };
                        ch.Hash = nd.Hash ^ zb[b] ^ zp[nd.Player] ^ zp[np] ^ zu[chosen];
                        ch.Key = Key(nd.Nb - 1, regions, goals, dist, 0);
                        if (remN == rem.Length) Array.Resize(ref rem, remN * 2);
                        rem[remN++] = ch;
                    }
                }
                if (region0N == region0.Length) Array.Resize(ref region0, region0N * 2); region0[region0N++] = Regions();
                SetOccCell(b, true);
            }
            ref var nd1 = ref nodes[id];
            if (remN > 0)
            {
                SortKids(rem, remN);
                nd1.CLen = remN; nd1.CSorted = int.MaxValue;
                nd1.COff = PutKids(rem, remN);
                return;
            }
            beamN = 0;
            int K = V2Beam;
            if (beam.Length < K + 1) beam = new P2Child[K + 1];
            foreach (var c in cands)
            {
                int b = boxes[c.Box];
                int regions = region0[c.Box] - 1 + c.Pieces;
                int goals = nd1.Goals; if (goal[b]) goals--; if (goal[c.To]) goals++;
                int dist = nd1.Dist - distance[b] + distance[c.To];
                ulong key = Key(nd1.Nb, regions, goals, dist, 0);
                if (beamN >= K && key >= beam[beamN - 1].Key) continue;
                var ch = new P2Child { Key = key, From = b, To = c.To, Player = c.Player, Base = -1 };
                ch.Hash = nd1.Hash ^ zb[b] ^ zb[c.To] ^ zp[nd1.Player] ^ zp[c.Player];
                int i = beamN;
                if (i < K) beam[beamN++] = ch; else i--;
                for (; i > 0 && beam[i - 1].Key > key; i--) beam[i] = beam[i - 1];
                beam[i] = ch;
            }
            nd1.COff = PutKids(beam, beamN);
            nd1.CLen += beamN; nd1.CSorted = int.MaxValue;
        }

        /// <summary>Go planner2.run: best-first over (key, seq); returns the best witness (complete when every box was consumed).</summary>
        public PackingPlan? RunV2()
        {
            ClearOcc();
            foreach (int g in s.goals) SetOccCell(g, true);
            var roots = new List<int>(); var covered = new bool[cells];
            foreach (int c in floorCells)
                if (!occ[c] && !covered[c])
                {
                    var (norm, st) = Flood(c);
                    roots.Add(norm);
                    for (int i = 0; i < qlen; i++) if (mark[queue[i]] == st) covered[queue[i]] = true;
                }
            if (roots.Count == 0) return null;
            int gsum = 0; foreach (int g in s.goals) gsum += distance[g];
            foreach (int r in roots)
            {
                ulong h = zp[r]; foreach (int g in s.goals) h ^= zb[g];
                EnsureBoxes(s.goals.Length);
                int off = boxN; foreach (int g in s.goals) boxArena[boxN++] = g;
                PushNode(new P2Node { Parent = -1, BOff = off, Nb = s.goals.Length, Player = r, Hash = h, Goals = s.goals.Length, Dist = gsum, MBase = -1, Key = Key(s.goals.Length, 1, s.goals.Length, gsum, 0) });
                EnsureUsed(); Array.Clear(usedArena, usedN, nw); usedN += nw;
                SeenInsert(h);
                if (matchOn) SetOcc(nodeN - 1);
            }
            if (matchOn)
            {
                FreeAll();
                if (!MatchOK(Boxes(0), -1, -1)) matchOn = false;
            }
            var heap = v2Heap = new P2Heap();
            int best = 0;
            int nroots = nodeN;
            for (int id = 0; id < nroots; id++)
            {
                if (nodes[id].Key < nodes[best].Key) best = id;
                ExpandV2(id);
                Expanded++;
                if (nodes[id].CLen > 0) { seq++; heap.Push(new P2Entry { Key = KidKey(id, 0), Seq = seq, Node = id }); }
            }
            while (!Stopped() && nodes[best].Nb > 0 && Expanded < MaxExp && heap.Count > 0)
            {
                var e = heap.Pop();
                ref var pn = ref nodes[e.Node];
                var c = Kid(e.Node, pn.CNext);
                pn.CNext++;
                if (pn.CNext < pn.CLen) { seq++; heap.Push(new P2Entry { Key = KidKey(e.Node, pn.CNext), Seq = seq, Node = e.Node }); }
                int id = AddNode(e.Node, c);
                if (id < 0) continue;
                if (nodes[id].Key < nodes[best].Key) best = id;
                if (nodes[id].Nb == 0) { best = id; break; }
                ExpandV2(id);
                Expanded++;
                if (nodes[id].CLen > 0) { seq++; heap.Push(new P2Entry { Key = KidKey(id, 0), Seq = seq, Node = id }); }
            }
            Best = best;
            return Result(best);
        }
        P2Heap? v2Heap;

        public PackingPlan? Finish4()
        {
            var res = Result(Best);
            if (res != null && AtStart(Best)) res.Complete = true;
            return res;
        }

        /// <summary>Turns the path root -> best into packing phases (latest backward state first).</summary>
        PackingPlan Result(int best)
        {
            var path = new List<int>();
            for (int id = best; id >= 0; id = nodes[id].Parent) path.Add(id);
            path.Reverse();
            var res = new PackingPlan { Complete = nodes[best].Nb == 0, Expanded = Expanded, Remaining = nodes[best].Nb };
            var cfg = Boxes(path[0]).ToArray().ToList();
            var req = new List<int[]>(); var plr = new List<int>(); var mvs = new List<PMove>();
            void Snap(int player, PMove mv) { var r = cfg.ToArray(); Array.Sort(r); req.Add(r); plr.Add(player); mvs.Add(mv); }
            Snap(nodes[path[0]].Player, default);
            for (int pi = 1; pi < path.Count; pi++)
            {
                ref var nn = ref nodes[path[pi]];
                int i = cfg.IndexOf(nn.MFrom);
                if (nn.MBase < 0)
                {
                    cfg[i] = nn.MTo;
                    Snap(nn.Player, new PMove { From = nn.MFrom, To = nn.MTo, Player = nn.Player });
                    res.Parkings++;
                    continue;
                }
                if (nn.MFrom != nn.MTo)
                {
                    cfg[i] = nn.MTo;
                    Snap(nn.Player, new PMove { From = nn.MFrom, To = nn.MTo, Player = nn.Player });
                    res.Parkings++;
                }
                cfg.RemoveAt(i);
                Snap(nn.Player, new PMove { From = nn.MTo, To = nn.MTo, Player = nn.Player, Remove = true });
            }
            req.Reverse(); plr.Reverse(); mvs.Reverse();
            res.Required = req.ToArray(); res.Players = plr.ToArray(); res.Moves = mvs.ToArray();
            res.Fest = FestPlan.Build(res, n);
            if (HashHook)
            {
                ulong h = 1469598103934665603UL;
                foreach (int id in path) { ref var q = ref nodes[id]; foreach (long v in new long[] { q.MFrom, q.MTo, q.MBase, q.Player, q.Nb, (long)q.Key }) h = (h ^ (ulong)v) * 1099511628211UL; }
                Console.Error.WriteLine($"P4HASH {h:x16} expanded={Expanded} nodes={nodeN} path={path.Count} complete={res.Complete} rem={res.Remaining}");
            }
            return res;
        }
    }

    // ---- plan4 entry points on the solver (Go buildPlan4x / newPlanner4 / planAnytime / stepPlan4 / endPlan4) ----------------------------
    Planner4 NewPlanner4(long deadlineMs, int maxNodes) =>
        new(this, deadlineMs, maxNodes, PlanAlt, PlanAlt, PlanAlt ? 1 : 0);

    /// <summary>Go buildPlanV2Core (fa2's own planner): returns the best witness plan.</summary>
    PackingPlan? BuildPlanV2(long deadlineMs, int maxNodes)
    {
        var p = new Planner4(this, deadlineMs, maxNodes, false, false, 0);
        p.MaxExp = maxNodes;
        p.UseV2Distance();
        return p.RunV2();
    }

    PackingPlan? EndPlan4(PackingPlan? res)
    {
        if (res?.Fest != null && res.Fest.Mode == 1) res.Fest.PartitionInits();
        return res;
    }

    /// <summary>plan4 up to the deadline; a complete witness is returned, otherwise the planner stays resumable in p4pend (returns null).</summary>
    PackingPlan? PlanAnytime(long deadlineMs, int maxNodes)
    {
        var p = NewPlanner4(deadlineMs, maxNodes);
        if (!p.Start4()) return EndPlan4(null);
        p.Step4(0);
        var res = p.Finish4();
        if (res != null && res.Complete) return EndPlan4(res);
        if (!p.Exhausted && p.Expanded < p.MaxExp && !opt.Stop && !(opt.Cancel?.Stopped ?? false)) { p4pend = p; stats.Plan4Pending = true; }
        return null;
    }

    /// <summary>Advances the pending planner by 8 iterations (-plan4-anytime-ratio); true = it completed and the lane must restart on its plan.</summary>
    bool StepPlan4()
    {
        var p = p4pend!;
        p.DeadlineMs = long.MaxValue;
        p.Step4(8);
        if (!p.Done && !p.Exhausted && p.Expanded < p.MaxExp) return false;
        p4pend = null;
        var res = p.Finish4();
        stats.Plan4Exp = p.Expanded;
        if (res == null || !res.Complete) return false;
        res = EndPlan4(res)!;
        res.FestPacked = true;
        prePlan = res; prePlanAt = (int)mExpandedN;   // Plan4AnytimeRestart (default on)
        return true;
    }
}
