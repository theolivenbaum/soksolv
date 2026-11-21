using System.Numerics;
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
        public int Parent, BOff, Nb, Player; public ulong Hash, Key; public int Goals, Dist, MFrom, MTo, MBase, COff, CLen, CNext;
    }
    struct P2Entry { public ulong Key; public uint Seq; public int Node; public int W; }
    struct P2Cand { public int Box, To, Player, Pieces; }

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
    public static bool CheckCut;   // test hook: compare every cutscan query with the exact flood
    public static bool MatchCheckHook { get => Planner4.MatchCheck; set => Planner4.MatchCheck = value; }
    // (old marker): compare every cutscan query with the exact flood
    Planner4? p4pend;
    PackingPlan? prePlan; int prePlanAt;
    /// <summary>The alt engine (exp-769) plans plan4 with festsink + roomsep + keep-parks 1.</summary>
    bool PlanAlt => opt.PlanAlt;

    sealed class Planner4
    {
        const int Plan4Sep = 6, Plan4Beam = 512, Plan4Exp = 200000;
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
        readonly ChunkList<P2Kid> kids = new(); 
        ulong[] seen = new ulong[1 << 16]; ulong seenMask = (1 << 16) - 1; int nseen;
        uint seq;
        // scan scratch
        readonly uint[] mark; uint stamp; readonly int[] queue; int qlen;
        readonly uint[] pgen; uint pg; readonly uint[] seenK; uint boxGen; readonly uint[] egen; readonly byte[] ecnt; readonly int[] eval;
        List<P2Cand> cands = new(); P2Child[] rem = new P2Child[256]; int remN; P2Child[] beam = new P2Child[256]; int beamN;
        readonly int[] demand; ulong[] alive = []; readonly List<int> region0 = new(); readonly List<int> free = new();
        int[] lab; uint[] labGen; uint labStamp; readonly int[] labQ;
        // matching
        readonly int[] mBox; readonly uint[] mSeen; uint mStamp; readonly List<int> curBox = new(); int skipT;
        // elim
        bool[] elimOK = []; readonly List<int> elimFr = new(); bool[] elimBlk = []; uint[] bseen; uint bstamp;
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
            occ = new bool[cells]; blk = new byte[cells]; for (int i = 0; i < cells; i++) blk[i] = (byte)(wall[i] ? 1 : 0);
            mark = new uint[cells]; queue = new int[cells]; pgen = new uint[cells]; seenK = new uint[4 * cells]; egen = new uint[cells]; ecnt = new byte[cells]; eval = new int[4 * cells];
            demand = new int[n]; mBox = new int[n]; mSeen = new uint[n]; lab = new int[cells]; labGen = new uint[cells]; labQ = new int[cells];
            bseen = new uint[cells]; sepD = new int[cells]; Array.Fill(sepD, -1); sepQ = new int[cells];
            floorCells = s.FloorCells();
            zb = new ulong[cells]; zp = new ulong[cells]; zu = new ulong[n];
            ulong seed = 0x5eed;
            for (int i = 0; i < cells; i++) { zb[i] = Splitmix(ref seed); zp[i] = Splitmix(ref seed); }
            for (int i = 0; i < n; i++) zu[i] = Splitmix(ref seed);
            baseAt = new int[cells]; Array.Fill(baseAt, -1);
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


        // ---- cutscan.go port: one low-link DFS per planner node over G0 (every box placed) answers the queries of every lifted box -------------
        static bool CheckCut => FullSolver.CheckCut;
        struct CutNode { public uint Vgen; public int Tin, Tout, Low, Par, Sub, Cid; }
        CutNode[] cnd = []; uint cgen, cdgen; int clift = -1; uint cbgen; int cnbn, cnbc, cball;
        readonly int[] cbn = new int[4], cbnc = new int[4], cbcm = new int[4], cuF = new int[4], cloF = new int[4], chiF = new int[4]; readonly bool[] csepF = new bool[4];
        byte[] cqpc = []; uint[] cqgen = []; int[] cqnp = []; byte[] cqsd = []; int[] cpre = [], csuf = [], corder = [], ccs = [], cce = [], cstack = []; byte[] csdir = [];
        int corderN, ccsN;

        void CutInit()
        {
            if (cnd.Length != 0) return;
            cnd = new CutNode[cells]; cqpc = new byte[cells]; cqgen = new uint[cells]; cqnp = new int[4 * cells]; cqsd = new byte[4 * cells];
            cpre = new int[cells]; csuf = new int[cells]; corder = new int[cells]; ccs = new int[cells + 1]; cce = new int[cells + 1]; cstack = new int[cells + 1]; csdir = new byte[cells + 1];
        }
        void CutNewNode() { CutInit(); cgen++; cdgen = cgen; clift = -1; corderN = 0; ccsN = 0; }
        void CutBegin(int b) { cgen++; clift = b; }

        void CutDfs(int root)
        {
            uint gen = cdgen; int lift = clift;
            int comp = ccsN; int start = corderN;
            ccs[ccsN] = start; // component start (ce filled at the end)
            ccsN++;
            cnd[root] = new CutNode { Vgen = gen, Tin = corderN, Low = corderN, Par = -1, Sub = root, Cid = comp };
            corder[corderN++] = root;
            cstack[0] = root; csdir[0] = 0; int sp = 1;
            while (sp > 0)
            {
                int top = sp - 1, v = cstack[top];
                ref var nv = ref cnd[v];
                bool pushed = false;
                for (int di = csdir[top]; di < 4; di++)
                {
                    int w = v + dirs[di];
                    if (wall[w] || occ[w] || w == lift) continue;
                    ref var nw = ref cnd[w];
                    if (nw.Vgen != gen)
                    {
                        int t = corderN;
                        nw = new CutNode { Vgen = gen, Tin = t, Low = t, Par = v, Sub = w, Cid = comp };
                        corder[corderN++] = w;
                        csdir[top] = (byte)(di + 1);
                        cstack[sp] = w; csdir[sp] = 0; sp++;
                        pushed = true;
                        break;
                    }
                    else if (w != nv.Par && nw.Tin < nv.Low) nv.Low = nw.Tin;
                }
                if (pushed) continue;
                nv.Tout = corderN;
                sp = top;
                int p = nv.Par;
                if (p >= 0)
                {
                    ref var np = ref cnd[p];
                    np.Low = Math.Min(np.Low, nv.Low);
                    np.Sub = Math.Min(np.Sub, nv.Sub);
                }
            }
            int end = corderN;
            cce[comp] = end;
            int m = 1 << 30;
            for (int i = start; i < end; i++) { m = Math.Min(m, corder[i]); cpre[i] = m; }
            m = 1 << 30;
            for (int i = end - 1; i >= start; i--) { m = Math.Min(m, corder[i]); csuf[i] = m; }
        }

        void CutEnsure(int v) { if (cnd[v].Vgen != cdgen) CutDfs(v); }

        void CutBoxInfo()
        {
            cbgen = cgen; cnbn = 0; cnbc = 0;
            int b = clift, m = b;
            foreach (int d in dirs)
            {
                int y = b + d;
                if (wall[y] || occ[y]) continue;
                CutEnsure(y);
                int cid = cnd[y].Cid;
                cbn[cnbn] = y; cbnc[cnbn] = cid; cnbn++;
                bool dup = false;
                for (int i = 0; i < cnbc; i++) dup |= cbcm[i] == cid;
                if (!dup) { cbcm[cnbc++] = cid; m = Math.Min(m, csuf[ccs[cid]]); }
            }
            cball = m;
        }

        int CutCompMin(int v)
        {
            if (cbgen != cgen) CutBoxInfo();
            if (v == clift) return cball;
            CutEnsure(v);
            int cid = cnd[v].Cid;
            for (int i = 0; i < cnbc; i++) if (cbcm[i] == cid) return cball;
            return csuf[ccs[cid]];
        }

        (int np, int sides) CutQuery(int to, int player)
        {
            if (cqgen[to] != cgen) CutQueryFill(to);
            for (int di = 0; di < 4; di++) if (to + dirs[di] == player) return (cqnp[to * 4 + di], cqsd[to * 4 + di]);
            throw new InvalidOperationException("cutScan.query: player is not adjacent to to");
        }
        int CutPieces(int v) { if (cqgen[v] != cgen) CutQueryFill(v); return cqpc[v]; }

        void CutQueryFill(int to)
        {
            cqgen[to] = cgen;
            if (cbgen != cgen) CutBoxInfo();
            int lift = clift, baseI = to * 4;
            Span<int> lab = stackalloc int[4]; Span<bool> fr = stackalloc bool[4];
            lab.Clear(); fr.Clear();
            if (to == lift)
            {
                for (int di = 0; di < 4; di++)
                {
                    int w = to + dirs[di];
                    if (wall[w] || occ[w]) continue;
                    fr[di] = true; lab[di] = cnd[w].Cid; // ensured by boxInfo
                }
                int npc = 0;
                for (int di = 0; di < 4; di++)
                {
                    if (!fr[di]) continue;
                    int sides0 = 0; bool first0 = true;
                    for (int dj = 0; dj < 4; dj++)
                        if (fr[dj] && lab[dj] == lab[di]) { sides0 |= 1 << dj; first0 = first0 && dj >= di; }
                    if (first0) npc++;
                    cqsd[baseI + di] = (byte)sides0;
                    cqnp[baseI + di] = csuf[ccs[lab[di]]];
                }
                cqpc[to] = (byte)npc;
                return;
            }
            CutEnsure(to);
            uint gen = cdgen;
            ref var nt = ref cnd[to];
            int tinTo = nt.Tin, toutTo = nt.Tout, cx = nt.Cid;
            bool isRoot = nt.Par < 0;
            int[] cu = cuF, clo = cloF, chi = chiF; bool[] csep = csepF;
            int nc = 0;
            foreach (int d in dirs)
            {
                int u = to + d;
                if (wall[u] || occ[u] || u == lift) continue;
                ref var nu = ref cnd[u];
                if (nu.Vgen == gen && nu.Par == to)
                {
                    cu[nc] = u; clo[nc] = nu.Tin; chi[nc] = nu.Tout;
                    csep[nc] = isRoot || nu.Low >= tinTo;
                    nc++;
                }
            }
            // pieceOf: index of the separated child subtree holding DFS time tw, else -1 (the parent side)
            int PieceOf(int tw)
            {
                if (tw > tinTo && tw < toutTo)
                    for (int i = 0; i < nc; i++)
                        if (clo[i] <= tw && tw < chi[i]) { if (csep[i]) return i; break; }
                return -1;
            }
            Span<bool> inM = stackalloc bool[4]; inM.Clear();
            bool parentInM = false;
            int mm = lift;
            for (int i = 0; i < cnbn; i++)
            {
                int y = cbn[i];
                if (y == to) continue;
                if (cbnc[i] != cx) { mm = Math.Min(mm, csuf[ccs[cbnc[i]]]); continue; }
                int j = PieceOf(cnd[y].Tin);
                if (j >= 0) inM[j] = true; else parentInM = true;
            }
            const int merged = -2;
            bool needParent = parentInM;
            for (int di = 0; di < 4; di++)
            {
                int w = to + dirs[di];
                if (w == lift) { fr[di] = true; lab[di] = merged; continue; }
                if (wall[w] || occ[w]) continue;
                fr[di] = true;
                int j = PieceOf(cnd[w].Tin);
                if ((j >= 0 && inM[j]) || (j < 0 && parentInM)) lab[di] = merged;
                else { lab[di] = j; needParent = needParent || j < 0; }
            }
            int parentMin = 0;
            if (needParent)
            {
                int m = 1 << 30;
                if (tinTo > ccs[cx]) m = cpre[tinTo - 1];
                if (toutTo < cce[cx]) m = Math.Min(m, csuf[toutTo]);
                for (int i = 0; i < nc; i++) if (cnd[cu[i]].Low < tinTo) m = Math.Min(m, cnd[cu[i]].Sub);
                parentMin = m;
            }
            if (parentInM) mm = Math.Min(mm, parentMin);
            for (int i = 0; i < nc; i++) if (inM[i]) mm = Math.Min(mm, cnd[cu[i]].Sub);
            int np2 = 0;
            for (int di = 0; di < 4; di++)
            {
                if (!fr[di]) continue;
                int sides = 0; bool first = true;
                for (int dj = 0; dj < 4; dj++)
                    if (fr[dj] && lab[dj] == lab[di]) { sides |= 1 << dj; first = first && dj >= di; }
                if (first) np2++;
                cqsd[baseI + di] = (byte)sides;
                int l = lab[di];
                cqnp[baseI + di] = l == merged ? mm : l >= 0 ? cnd[cu[l]].Sub : parentMin;
            }
            cqpc[to] = (byte)np2;
        }

        // ---- own occupancy / floods ------------------------------------------------------------------------------------------------
        void SetOccCell(int c, bool v) { occ[c] = v; blk[c] = (byte)(v || wall[c] ? 1 : 0); }
        void SetOcc(int id)
        {
            for (int c = 0; c < cells; c++) if (occ[c]) { occ[c] = false; blk[c] = 0; }
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
        (int np, int sides) Query(int to, int player)
        {
            var c = CutQuery(to, player);
            if (CheckCut) { var f = QueryFlood(to, player); if (f != c) throw new InvalidOperationException($"cutscan query ({to},{player}): scan {c} != flood {f}"); }
            return c;
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
        int PutKids(P2Child[] a, int na)
        {
            int off = kids.Count;
            for (int i = 0; i < na; i++) kids.Add(new P2Kid { Key = a[i].Key, From = (short)a[i].From, To = (short)a[i].To, Player = (short)a[i].Player, Base = (short)a[i].Base });
            return off;
        }
        ulong KidKey(int id, int i) => kids[nodes[id].COff + i].Key;
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
        bool Augment(int u, uint sd)
        {
            int cell = curBox[u];
            foreach (int t in free)
            {
                if (t == skipT || !CanReach(t, cell) || mSeen[t] == sd) continue;
                mSeen[t] = sd;
                if (mBox[t] < 0 || Augment(mBox[t], sd)) { mBox[t] = u; return true; }
            }
            return false;
        }
        bool MatchOK(ReadOnlySpan<int> boxes, int skipBox, int skipBase)
        {
            curBox.Clear();
            for (int i = 0; i < boxes.Length; i++) if (i != skipBox) curBox.Add(boxes[i]);
            skipT = skipBase;
            Array.Fill(mBox, -1);
            for (int u = 0; u < curBox.Count; u++) { mStamp++; if (!Augment(u, mStamp)) return false; }
            return true;
        }

        // matchSkip (exp-583): matchOK(boxes, skipBox, skipBase) from one maximum matching M of all the node's boxes per expansion.
        //  |M| <= n-2: removing one box leaves a deficiency, so false. M perfect: skipBox's base is freed, at most one alternating-path
        //  search from skipBase's box (Berge). |M| = n-1 (box u0 unmatched): skipBox must be reached from u0 by an even alternating path;
        //  flipping that path gives a maximum matching M1 perfect on the other boxes, then as above.
        int fmGen, fmDef, fmU0; int[] fmBox = [], fmOf = [], fm1 = [], fmPar = [], fmBy = []; readonly List<int> fmQ = new();
        public static bool MatchCheck;

        bool MatchSkip(ReadOnlySpan<int> boxes, int skipBox, int skipBase)
        {
            if (fmGen != expGen) { fmGen = expGen; FmPrep(boxes); }
            bool r = MatchSkipOK(boxes, skipBox, skipBase);
            if (MatchCheck && r != MatchOK(boxes, skipBox, skipBase)) throw new InvalidOperationException($"matchSkip {r} != matchOK for box {skipBox} base {skipBase}");
            return r;
        }

        void FmPrep(ReadOnlySpan<int> boxes)
        {
            curBox.Clear(); for (int i = 0; i < boxes.Length; i++) curBox.Add(boxes[i]);
            skipT = -1;
            Array.Fill(mBox, -1);
            fmDef = 0; fmU0 = -1;
            for (int u = 0; u < curBox.Count; u++) { mStamp++; if (!Augment(u, mStamp)) { fmDef++; if (fmU0 < 0) fmU0 = u; } }
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
                mStamp++;
                return AltPath(boxes, fmBox, fmBox[skipBase], skipBase, fb, mStamp);
            }
            if (fm1.Length != fmBox.Length) fm1 = new int[fmBox.Length];
            Array.Copy(fmBox, fm1, fmBox.Length);
            if (skipBox != fmU0)
            {
                // BFS over alternating paths from u0 (every base met is matched: M is maximum); flip the path to skipBox
                if (fmPar.Length < boxes.Length) fmPar = new int[boxes.Length];
                if (fmBy.Length < mBox.Length) fmBy = new int[mBox.Length];
                mStamp++;
                uint st = mStamp;
                fmQ.Clear(); fmQ.Add(fmU0);
                bool found = false;
                for (int qi = 0; qi < fmQ.Count && !found; qi++)
                {
                    int cell = boxes[fmQ[qi]];
                    foreach (int t in free)
                    {
                        if (!CanReach(t, cell) || mSeen[t] == st) continue;
                        mSeen[t] = st;
                        int x = fmBox[t];
                        fmPar[x] = t; // x is reached through base t (from box q[qi])
                        fmBy[t] = fmQ[qi];
                        if (x == skipBox) { found = true; break; }
                        fmQ.Add(x);
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
            mStamp++;
            return AltPath(boxes, fm1, fm1[skipBase], skipBase, -1, mStamp);
        }

        /// <summary>Is there an alternating path (w.r.t. matching m, base -> box) from box u to a base that is free once skipBase's pair (and the base fb freed by the removed box; -1: none) is dropped, avoiding skipBase?</summary>
        bool AltPath(ReadOnlySpan<int> boxes, int[] m, int u, int skipBase, int fb, uint seen)
        {
            int cell = boxes[u];
            foreach (int t in free)
            {
                if (t == skipBase || !CanReach(t, cell) || mSeen[t] == seen) continue;
                mSeen[t] = seen;
                if (t == fb || m[t] < 0 || AltPath(boxes, m, m[t], skipBase, fb, seen)) return true;
            }
            return false;
        }

        // ---- region labelling -------------------------------------------------------------------------------------------------------
        int LabelRegions()
        {
            uint st = ++labStamp; int cnt = 0;
            foreach (int c in floorCells)
            {
                if (blk[c] != 0 || labGen[c] == st) continue;
                labGen[c] = st; lab[c] = cnt; int tail = 0; labQ[tail++] = c;
                for (int i = 0; i < tail; i++)
                    foreach (int d in dirs)
                    {
                        int y = labQ[i] + d;
                        if (labGen[y] != st && blk[y] == 0) { labGen[y] = st; lab[y] = cnt; labQ[tail++] = y; }
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
        void ComputeElim4()
        {
            int nb = baseCell.Length;
            if (elimOK.Length < nb) elimOK = new bool[nb];
            Array.Clear(elimOK, 0, nb);
            elimFr.Clear();
            foreach (int t in free) if (!occ[baseCell[t]]) elimFr.Add(t);
            int m = elimFr.Count; if (m == 0) return;
            if (elimBlk.Length < cells) elimBlk = new bool[cells];
            for (int c = 0; c < cells; c++) elimBlk[c] = wall[c] || occ[c];
            if (sccIdx.Length < cells)
            {
                sccIdx = new int[cells]; sccLow = new int[cells]; sccCmp = new int[cells]; sccCnt = new int[cells + 1]; sccCall = new int[cells]; sccDir = new byte[cells];
            }
            bstamp++; uint st = bstamp;
            var stk = sccStk; stk.Clear();
            int next = 0, ncomp = 0;
            foreach (int r in elimFr)
            {
                int root = baseCell[r];
                if (bseen[root] == st) continue;
                bseen[root] = st; sccIdx[root] = next; sccLow[root] = next; sccCmp[root] = -1; next++;
                stk.Add(root);
                sccCall[0] = root; sccDir[0] = 0; int sp = 1;
                while (sp > 0)
                {
                    int top = sp - 1, v = sccCall[top];
                    int di = sccDir[top];
                    if (di < 4)
                    {
                        sccDir[top] = (byte)(di + 1);
                        int to = v + dirs[di], back = v - dirs[di];
                        if (elimBlk[to] || elimBlk[back]) continue;
                        if (bseen[to] != st)
                        {
                            bseen[to] = st; sccIdx[to] = next; sccLow[to] = next; sccCmp[to] = -1; next++;
                            stk.Add(to);
                            sccCall[sp] = to; sccDir[sp] = 0; sp++;
                        }
                        else if (sccCmp[to] < 0 && sccIdx[to] < sccLow[v]) sccLow[v] = sccIdx[to];
                        continue;
                    }
                    sp = top;
                    if (sccLow[v] == sccIdx[v])
                    {
                        while (true)
                        {
                            int w2 = stk[^1]; stk.RemoveAt(stk.Count - 1);
                            sccCmp[w2] = ncomp;
                            if (w2 == v) break;
                        }
                        ncomp++;
                    }
                    if (sp > 0) { int u = sccCall[sp - 1]; if (sccLow[v] < sccLow[u]) sccLow[u] = sccLow[v]; }
                }
            }
            Array.Clear(sccCnt, 0, ncomp);
            int best = 0;
            foreach (int ti in elimFr) { int c = sccCmp[baseCell[ti]]; sccCnt[c]++; best = Math.Max(best, sccCnt[c]); }
            foreach (int ti in elimFr) if (sccCnt[sccCmp[baseCell[ti]]] == best) elimOK[ti] = true;
        }

        // ---- expansion --------------------------------------------------------------------------------------------------------------
        int[] boxesBuf = new int[64]; int[]? pullQ_; int[] pullQ => pullQ_ ??= new int[4 * cells + 8];
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
            for (int i = 0; i < qlen; i++) pgen[queue[i]] = pg;
            free.Clear();
            for (int t = 0; t < baseCell.Length; t++) if ((used[t >> 6] >> (t & 63) & 1) == 0) free.Add(t);
            bool sinks = free.Count > 0;
            if (sinks)
            {
                ComputeElim4();
                bool any = false;
                foreach (int t in free)
                    if (elimOK[t]) { any = true; if (pgen[baseCell[t]] != pg) sinks = false; }
                if (!any) sinks = false;
            }
            int aw = reach[0].Length;
            if (alive.Length < aw) alive = new ulong[aw];
            Array.Clear(alive);
            foreach (int t in free) for (int w = 0; w < aw; w++) alive[w] |= reach[t][w];
            cands.Clear(); remN = 0; region0.Clear();
            int baseR = LabelRegions();
            if (Prof.On) T4[0] += System.Diagnostics.Stopwatch.GetTimestamp() - t0;
            long t1 = Prof.Start();
            for (int bi = 0; bi < nbx; bi++)
            {
                int b = boxes[bi];
                int rb = baseR + 1;
                Span<int> seenL = stackalloc int[4]; int k = 0;
                foreach (int d in dirs)
                {
                    int y = b + d;
                    if (labGen[y] == labStamp && lab[y] >= 0) // lab is valid where labGen == labStamp (free cells of the unlifted board)
                    {
                        int l = lab[y]; bool dup = false;
                        for (int j = 0; j < k; j++) dup |= seenL[j] == l;
                        if (!dup) seenL[k++] = l;
                    }
                }
                if (k > 0) rb = baseR - k + 1;
                if ((bi & 7) == 7 && Stopped()) break;
                boxGen++; uint g = boxGen;
                int qc = 0; var q = pullQ;
                for (int di = 0; di < 4; di++)
                {
                    int d = dirs[di];
                    if (pgen[b + d] == pg) { int kk = b * 4 + di; q[qc++] = kk; seenK[kk] = g; }
                }
                CutBegin(b);
                SetOccCell(b, false);
                bool sunk = false;
                for (int qi = 0; qi < qc; qi++)
                {
                    int kq = q[qi]; int c = kq / 4, d = dirs[kq % 4];
                    int to = c + d, player = c + 2 * d;
                    if (wall[to] || occ[to] || wall[player] || occ[player]) continue;
                    var (np, sides) = Query(to, player);
                    for (int di = 0; di < 4; di++)
                    {
                        int k2 = to * 4 + di;
                        if ((sides & (1 << di)) != 0 && seenK[k2] != g) { seenK[k2] = g; q[qc++] = k2; }
                    }
                    int t = baseAt[to];
                    if (sinks && !sunk && t >= 0 && (used[t >> 6] >> (t & 63) & 1) == 0 && elimOK[t])
                    {
                        if (Fest) sinkTo.Add(to);
                        else sunk = TrySink(id, boxes, b, bi, to, t, rb) || sunk;
                    }
                    if ((alive[to >> 6] >> (to & 63) & 1) == 0) continue;
                    if (!NewEnd(to, np, g)) continue;
                    cands.Add(new P2Cand { Box = bi, To = to, Player = np, Pieces = Pieces(to) });
                }
                if (sinkTo.Count > 0)
                {
                    sinkTo.Sort();
                    foreach (int to in sinkTo) if (TrySink(id, boxes, b, bi, to, baseAt[to], rb)) break;
                    sinkTo.Clear();
                }
                region0.Add(rb);
                SetOccCell(b, true);
            }
            if (Prof.On) T4[1] += System.Diagnostics.Stopwatch.GetTimestamp() - t1;
            long t2 = Prof.Start();
            ref var nd = ref nodes[id];
            if (remN > 0 && KeepParks > 0) { KeepParks4(id, boxes); return; }
            if (remN > 0)
            {
                int adv = -1; P2Child advCh = default;
                if (Fest) { adv = FestSinkAdv(boxes); if (adv >= 0) advCh = rem[adv]; }
                SortKids(rem, remN);
                if (adv >= 0)
                    for (int i = 0; i < remN; i++)
                        if (rem[i].From == advCh.From && rem[i].To == advCh.To) { SetAdv(id, rem, remN, i); break; }
                nd.COff = PutKids(rem, remN);
                nd.CLen = remN;
                return;
            }
            ParkKids4(id, boxes);
            if (Fest) SetAdv(id, beam, beamN, FestPackAdv(id));
            if (Plan4Beam > 0 && beamN > Plan4Beam) beamN = Plan4Beam;
            nd.COff = PutKids(beam, beamN);
            nd.CLen = beamN;
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
                var ch = new P2Child { From = b, To = to, Player = npl, Base = t };
                ch.Key = Key(nd.Nb - 1, rb, goals, dist, 0);
                if (remN == rem.Length) Array.Resize(ref rem, remN * 2);
                rem[remN++] = ch;
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
            int adv = -1; P2Child advCh = default;
            if (Fest) { adv = FestSinkAdv(boxes); if (adv >= 0) advCh = rem[adv]; }
            SortKids(rem, remN);
            if (adv >= 0)
                for (int i = 0; i < remN; i++)
                    if (rem[i].From == advCh.From && rem[i].To == advCh.To) { SetAdv(id, rem, remN, i); break; }
            if (Fest && KeepParks == 1)
            {
                while (nodeAdv.Count <= id) nodeAdv.Add(0);
                nodeAdv[id] = (byte)Math.Min(remN, 255);
            }
            ParkKids4(id, boxes);
            if (Plan4Beam > 0 && beamN > Plan4Beam) beamN = Plan4Beam;
            if (remN + beamN > rem.Length) Array.Resize(ref rem, Math.Max(rem.Length * 2, remN + beamN));
            Array.Copy(beam, 0, rem, remN, beamN); remN += beamN;
            ref var nd = ref nodes[id];
            nd.COff = PutKids(rem, remN);
            nd.CLen = remN;
        }

        void ParkKids4(int id, ReadOnlySpan<int> boxes)
        {
            ref var nd = ref nodes[id];
            beamN = 0;
            foreach (var c in cands)
            {
                int b = boxes[c.Box];
                int regions = region0[c.Box] - 1 + c.Pieces;
                int goals = nd.Goals; if (goal[b]) goals--; if (goal[c.To]) goals++;
                int dist = nd.Dist - distance[b] + distance[c.To];
                if (beamN == beam.Length) Array.Resize(ref beam, beamN * 2);
                beam[beamN++] = new P2Child { Key = Key(nd.Nb, regions, goals, dist, 0), From = b, To = c.To, Player = c.Player, Base = -1 };
            }
            SortKids(beam, beamN);
        }

        void SetAdv(int id, P2Child[] ch, int nch, int i)
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
            for (int i = 0; i < remN; i++) if (!goal[rem[i].From]) return i;
            int best = -1, bR = 0, bD = 0;
            for (int i = 0; i < remN; i++)
            {
                SetOccCell(rem[i].From, false); SetOccCell(rem[i].To, true);
                int r = Regions();
                SetOccCell(rem[i].To, false); SetOccCell(rem[i].From, true);
                int d = s.MWalk(rem[i].To, rem[i].From);
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
                ulong r = beam[i].Key >> 42 & 1023;
                if (r > pr || !goal[beam[i].From]) continue;
                if (distance[beam[i].To] <= distance[beam[i].From]) continue;
                int l = s.MWalk(beam[i].To, beam[i].From);
                if (best < 0 || r < bR || (r == bR && l > bL)) { best = i; bR = r; bL = l; }
            }
            return best;
        }

        /// <summary>Stable sort by key (Go: slices.SortStableFunc / radix, same result).</summary>
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
            MergeSort(a, sortTmp, 0, cnt);
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
            var pn = nodes[parent];
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

        public bool Start4()
        {
            for (int c = 0; c < cells; c++) if (occ[c]) { occ[c] = false; blk[c] = 0; }
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
                free.Clear(); for (int t = 0; t < baseCell.Length; t++) free.Add(t);
                if (!MatchOK(Boxes(0), -1, -1)) matchOn = false;
            }
            int best = 0; bool done = false;
            for (int id = 0; id < nodeN; id++)
            {
                if (nodes[id].Key < nodes[best].Key) best = id;
                if (AtStart(id)) { best = id; done = true; break; }
                Expand4(id);
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
                Expand4(id);
                Expanded++;
                Enqueue(id);
                if (Prof.On && Expanded % 2000 == 0) Console.Error.WriteLine($"p4 expanded {Expanded} nodes {nodeN} kids {kids.Count} t={s.clock.ElapsedMilliseconds} ms | setup {T4[0]*1000/System.Diagnostics.Stopwatch.Frequency} boxloop {T4[1]*1000/System.Diagnostics.Stopwatch.Frequency} tail {T4[2]*1000/System.Diagnostics.Stopwatch.Frequency} ms");
            }
            Best = best; Done = done;
            if (fails >= 10000) Exhausted = true;
        }

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
            return res;
        }
    }

    // ---- plan4 entry points on the solver (Go buildPlan4x / newPlanner4 / planAnytime / stepPlan4 / endPlan4) ----------------------------
    Planner4 NewPlanner4(long deadlineMs, int maxNodes) =>
        new(this, deadlineMs, maxNodes, PlanAlt, PlanAlt, PlanAlt ? 1 : 0);

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
