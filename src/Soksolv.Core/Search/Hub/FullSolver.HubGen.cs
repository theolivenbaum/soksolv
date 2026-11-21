using System.Diagnostics;
using Soksolv.Search.Hub;
using Soksolv.Search.Meet;

namespace Soksolv.Search;

/// <summary>
/// The hub generator (Go hubgen.go, exp-453): a backward best-first search over single pulls whose keys never refer to the start
/// (player regions, boxes that cannot be pulled, free cells on no cycle, summed goal distance, pull mobility). Own node store; only
/// the best TopK children of an expanded node are kept (a generator, not a prover). Entries are bucketed by (regions, trapped/4) and pops
/// alternate between the globally best entry and the best entry of the least-expanded bucket.
/// </summary>
public sealed unsafe partial class FullSolver
{
    internal static class HubGenFlags
    {
        public static int TopK = 16;
        public static bool Bic = true;
        public static bool KeyFm = true;      // "fm": far then mobility; false = "mf"
        public static double Init = 0.15;     // fraction of the hub phase spent generating before the first nomination
        public static int Macro = 48;         // pull-walk states explored per box
        public static bool Cells = true;
        public static int Pockets = 4;
        public static bool Reach = true;
        public static double Cap = 0.75;
    }

    internal const int HubPocketMax = 60;
    const uint GenExpand = ~0u;

    internal struct GenNode
    {
        public int Parent; public uint Mv; public int Player;
        public int Reg, Trap, Mob, Bic, Far, G, Depth;
        public ulong Hash;
    }

    internal struct GenEnt
    {
        public ulong Key; public int Id; public uint Mv;
        public static bool Less(in GenEnt a, in GenEnt b)
        {
            if (a.Key != b.Key) return a.Key < b.Key;
            if (a.Id != b.Id) return a.Id < b.Id;
            return a.Mv < b.Mv;
        }
    }

    struct MacroSt { public int X, Par; public sbyte K; }

    sealed class GenCell { public GenEnt[] Heap = new GenEnt[4]; public int Len; public int Count; }

    internal sealed class HubGen
    {
        readonly FullSolver ps; readonly int n, cells;
        GenNode[] nodes = new GenNode[1024]; int nNodes;
        ushort[] boxes = new ushort[1024 * 8]; int nBoxes;
        readonly Dictionary<ulong, int> seen = new();
        readonly List<GenCell> cellList = new();
        readonly Dictionary<uint, int> cellIdx = new();
        int pending; long tick;
        readonly ulong[] z, zp;
        readonly int[] gd;
        readonly int[] cur; int curId = -2;
        readonly uint[] mark; readonly uint[] vis; uint visStamp;
        MacroSt[] st = new MacroSt[64];
        internal readonly int[] lab; internal uint stamp;
        int[] q = new int[0];
        readonly uint[] dd; uint ddStamp;
        readonly GenEnt[] top; readonly int[] topR, topT; int nTop;
        public ulong Base;
        public readonly List<int> Cand = new();
        public int Best = -1; ulong bestKey = ulong.MaxValue;
        public long Expanded, Created, Dupes;
        public ulong CapBytes;
        public bool Full;
        public HubReach? Reach;
        readonly int[] dirs; readonly bool[] wall; readonly bool[] occ; readonly int[] dist;

        public int Count => nNodes;
        internal ref GenNode Node(int id) => ref nodes[id];

        public static HubGen? Create(FullSolver ps)
        {
            if (ps.cells >= 1 << 16 || ps.n == 0) return null;
            return new HubGen(ps);
        }

        HubGen(FullSolver ps)
        {
            this.ps = ps; n = ps.n; cells = ps.cells; dirs = ps.dirs; wall = ps.wall; occ = ps.occ; dist = ps.dist;
            ps.PfOnInit();
            var pf = ps.pff!;
            gd = new int[cells];
            for (int c = 0; c < cells; c++)
            {
                int m = -1;
                for (int gi = 0; (long)gi * cells < pf.PfDist.Length; gi++)
                {
                    int d = pf.PfDist[gi * cells + c];
                    if (d >= 0 && (m < 0 || d < m)) m = d;
                }
                gd[c] = Math.Max(m, 0);
            }
            z = new ulong[cells]; zp = new ulong[cells];
            ulong x = 0x9E3779B97F4A7C15UL;
            ulong Next()
            {
                x += 0x9E3779B97F4A7C15UL;
                ulong zz = x;
                zz = (zz ^ zz >> 30) * 0xBF58476D1CE4E5B9UL;
                zz = (zz ^ zz >> 27) * 0x94D049BB133111EBUL;
                return zz ^ zz >> 31;
            }
            for (int c = 0; c < cells; c++) { z[c] = Next(); zp[c] = Next(); }
            mark = new uint[cells]; lab = new int[cells]; vis = new uint[cells * 4]; dd = new uint[cells];
            cur = new int[n];
            top = new GenEnt[HubGenFlags.TopK + 1]; topR = new int[HubGenFlags.TopK + 1]; topT = new int[HubGenFlags.TopK + 1];
            if (HubGenFlags.Reach)
            {
                var gl = new List<int>();
                for (int c = 0; c < cells; c++) if (ps.goal[c]) gl.Add(c);
                var r = gl.Count == ps.n ? HubReach.Create(wall, ps.W, gl.ToArray(), ps.realPlayer) : null;
                if (r != null)
                {
                    Span<int> cnt = stackalloc int[64];
                    var goals = new ushort[n];
                    for (int i = 0; i < n; i++) goals[i] = (ushort)ps.lv.Boxes[i];
                    r.Counts(goals, cnt);
                    bool sound = true;
                    for (int j = 0; j < r.NCls; j++) sound = sound && cnt[j] >= r.Need[j];
                    if (sound && r.Feasible(ps.lv.Boxes)) Reach = r;
                }
            }
            var roots = ps.pullRoots;
            if (roots.Length == 0) roots = [ps.lv.Player];
            foreach (int root in roots)
            {
                var gs = new ushort[n];
                for (int i = 0; i < n; i++) gs[i] = (ushort)ps.lv.Boxes[i];
                AddRoot(gs, root);
            }
        }

        public ulong Bytes => (ulong)nNodes * 72 + (ulong)nBoxes * 2 + (ulong)seen.Count * 40 + (ulong)pending * 16;

        ReadOnlySpan<ushort> NodeBoxes(int id) => new ReadOnlySpan<ushort>(boxes, id * n, n);

        void Load(ReadOnlySpan<ushort> bs)
        {
            if (curId != -2) for (int i = 0; i < n; i++) occ[cur[i]] = false;
            for (int i = 0; i < n; i++) { cur[i] = bs[i]; occ[bs[i]] = true; }
            curId = -1;
        }

        void LoadNode(int id)
        {
            if (curId == id) return;
            Load(NodeBoxes(id));
            curId = id;
        }

        /// <summary>Marks the free cells reachable from p; returns the smallest and the number of free components.</summary>
        int Flood(int p, out int regs)
        {
            stamp++;
            uint s = stamp;
            int minC = p;
            int regNo = 0;
            if (q.Length < cells) q = new int[cells];
            var qq = q;
            void Fill(int s0, bool first)
            {
                regNo++;
                int tail = 0; qq[tail++] = s0;
                mark[s0] = s; lab[s0] = regNo;
                for (int i = 0; i < tail; i++)
                {
                    int c = qq[i];
                    for (int k = 0; k < 4; k++)
                    {
                        int nb = c + dirs[k];
                        if (mark[nb] != s && !wall[nb] && !occ[nb])
                        {
                            mark[nb] = s; lab[nb] = regNo;
                            qq[tail++] = nb;
                            if (nb < minC && first) minC = nb;
                        }
                    }
                }
            }
            Fill(p, true);
            regs = 1;
            for (int c = 0; c < wall.Length; c++)
                if (!wall[c] && !occ[c] && mark[c] != s) { regs++; Fill(c, false); }
            return minC;
        }

        public bool InPlayerRegion(int c) => mark[c] == stamp && lab[c] == 1;
        public bool LabIs1(int c) => lab[c] == 1;
        public void FloodAt(int p) => Flood(p, out _);

        /// <summary>FNV-1a over every node's fields (parity fingerprint, bench/hubgen-ref).</summary>
        public ulong Checksum()
        {
            ulong h = 14695981039346656037UL;
            void Mix(uint v) { h ^= v; h *= 1099511628211UL; }
            for (int i = 0; i < nNodes; i++)
            {
                ref var nd = ref nodes[i];
                Mix((uint)nd.Parent); Mix(nd.Mv); Mix((uint)nd.Player); Mix((uint)nd.Reg); Mix((uint)nd.Trap); Mix((uint)nd.Mob); Mix((uint)nd.Bic); Mix((uint)nd.Far); Mix((uint)nd.G); Mix((uint)nd.Depth);
            }
            return h;
        }

        int MobOf(int x)
        {
            int m = 0;
            for (int k = 0; k < 4; k++)
            {
                int d = dirs[k], a = x + d;
                if (wall[a] || occ[a]) continue;
                int b = a + d;
                if (!wall[b] && !occ[b]) m++;
            }
            return m;
        }

        readonly int[] csBuf = new int[18];
        void Local(int a, int b, out int trapB, out int mobB, out int trapA, out int mobA)
        {
            var cs = csBuf;
            int nc = 0;
            ddStamp++;
            uint s = ddStamp;
            trapB = mobB = trapA = mobA = 0;
            void Add(int c) { if (dd[c] != s) { dd[c] = s; cs[nc++] = c; } }
            Add(a); Add(b);
            for (int k = 0; k < 4; k++)
            {
                int d = dirs[k];
                for (int j = 0; j < 2; j++)
                {
                    int c = j == 0 ? a : b;
                    int c1 = c - d;
                    if (!wall[c1])
                    {
                        Add(c1);
                        int c2 = c1 - d;
                        if (!wall[c2]) Add(c2);
                    }
                }
            }
            for (int i = 0; i < nc; i++)
            {
                int c = cs[i];
                if (occ[c]) { int m = MobOf(c); mobB += m; if (m == 0) trapB++; }
            }
            occ[a] = false; occ[b] = true;
            for (int i = 0; i < nc; i++)
            {
                int c = cs[i];
                if (occ[c]) { int m = MobOf(c); mobA += m; if (m == 0) trapA++; }
            }
            occ[a] = true; occ[b] = false;
        }

        void FullEval(ref GenNode nd, ReadOnlySpan<ushort> bs)
        {
            nd.Trap = nd.Mob = nd.Far = nd.G = 0;
            foreach (ushort b in bs)
            {
                int m = MobOf(b);
                nd.Mob += m;
                if (m == 0) nd.Trap++;
                nd.Far += gd[b];
                if (ps.pff!.IsGoal(b)) nd.G++;
            }
        }

        static ulong Key(int reg, int trap, int bic, int far, int mob)
        {
            ulong f = (ulong)((1 << 19) - 1 - Math.Min(far, (1 << 19) - 1));
            ulong m = (ulong)((1 << 19) - 1 - Math.Min(mob, (1 << 19) - 1));
            ulong k = (ulong)Math.Min(reg, 15) << 60 | (ulong)Math.Min(trap, 1023) << 50 | (ulong)Math.Min(bic, 4095) << 38;
            return HubGenFlags.KeyFm ? k | f << 19 | m : k | m << 19 | f;
        }

        ulong NodeKey(in GenNode nd) => Key(nd.Reg, nd.Trap, nd.Bic, nd.Far, nd.Mob);

        int AddNode(in GenNode nd, ReadOnlySpan<ushort> bs, int replaceSlot = -1, ushort to = 0)
        {
            if (nNodes == nodes.Length) Array.Resize(ref nodes, nNodes * 2);
            if ((nNodes + 1) * n > boxes.Length) Array.Resize(ref boxes, Math.Max(boxes.Length * 2, (nNodes + 1) * n));
            nodes[nNodes] = nd;
            bs.CopyTo(new Span<ushort>(boxes, nNodes * n, n));
            if (replaceSlot >= 0) boxes[nNodes * n + replaceSlot] = to;
            nBoxes += n;
            return nNodes++;
        }

        void AddRoot(ushort[] bs, int player)
        {
            Load(bs);
            var nd = new GenNode { Parent = -1 };
            foreach (ushort b in bs) nd.Hash ^= z[b];
            int minC = Flood(player, out int regs);
            nd.Player = minC; nd.Reg = regs;
            FullEval(ref nd, bs);
            if (HubGenFlags.Bic) nd.Bic = ps.pff!.Bicon(occ);
            ulong full = nd.Hash ^ zp[minC];
            if (seen.ContainsKey(full)) return;
            int id = AddNode(nd, bs);
            seen[full] = id;
            Note(id);
            Push(new GenEnt { Key = NodeKey(nd), Id = id, Mv = GenExpand }, nd.Reg, nd.Trap);
        }

        void Note(int id)
        {
            ulong k = NodeKey(nodes[id]);
            if (k < bestKey) { bestKey = k; Best = id; }
        }

        GenCell CellOf(int reg, int trap)
        {
            uint ty = (uint)Math.Min(reg, 255) << 16 | (uint)Math.Min(trap >> 2, 255);
            if (!cellIdx.TryGetValue(ty, out int ci))
            {
                ci = cellList.Count; cellIdx[ty] = ci; cellList.Add(new GenCell());
            }
            return cellList[ci];
        }

        void Push(in GenEnt e, int reg, int trap)
        {
            var c = CellOf(reg, trap);
            if (c.Len == c.Heap.Length) Array.Resize(ref c.Heap, c.Len * 2);
            var h = c.Heap;
            int i = c.Len++;
            h[i] = e;
            while (i > 0)
            {
                int p = (i - 1) / 2;
                if (!GenEnt.Less(h[i], h[p])) break;
                (h[i], h[p]) = (h[p], h[i]);
                i = p;
            }
            pending++;
        }

        GenEnt Pop()
        {
            tick++;
            int bi = -1;
            if ((tick & 1) == 0 || !HubGenFlags.Cells)
            {
                for (int i = 0; i < cellList.Count; i++)
                {
                    var ci = cellList[i];
                    if (ci.Len > 0 && (bi < 0 || GenEnt.Less(ci.Heap[0], cellList[bi].Heap[0]))) bi = i;
                }
            }
            else
            {
                for (int i = 0; i < cellList.Count; i++)
                {
                    var ci = cellList[i];
                    if (ci.Len == 0) continue;
                    if (bi < 0 || ci.Count < cellList[bi].Count || ci.Count == cellList[bi].Count && GenEnt.Less(ci.Heap[0], cellList[bi].Heap[0])) bi = i;
                }
            }
            var c = cellList[bi];
            c.Count++;
            var h = c.Heap;
            var e = h[0];
            int nn = c.Len - 1;
            h[0] = h[nn];
            c.Len = nn;
            int j = 0;
            while (true)
            {
                int l = 2 * j + 1;
                if (l >= nn) break;
                int r = l + 1;
                if (r < nn && GenEnt.Less(h[r], h[l])) l = r;
                if (!GenEnt.Less(h[l], h[j])) break;
                (h[j], h[l]) = (h[l], h[j]);
                j = l;
            }
            pending--;
            return e;
        }

        /// <summary>Expands until the deadline (Stopwatch timestamp, 0: none) or maxExp more expansions (0: unbounded), the memory cap, or the heap empties. True: the search is over.</summary>
        public bool Run(long deadlineTs, long maxExp, Func<bool>? stopped)
        {
            long t0 = Stopwatch.GetTimestamp();
            long start = Expanded;
            try
            {
                for (int steps = 0; pending > 0; steps++)
                {
                    if ((steps & 31) == 0)
                    {
                        if (stopped != null && stopped() || deadlineTs != 0 && Stopwatch.GetTimestamp() > deadlineTs) return false;
                        if (CapBytes > 0 && Bytes > CapBytes) { Full = true; return true; }
                    }
                    if (maxExp > 0 && Expanded - start >= maxExp) return false;
                    var e = Pop();
                    if (e.Mv == GenExpand) Expand(e.Id); else Create(e);
                }
                return true;
            }
            finally { Ns += Stopwatch.GetTimestamp() - t0; }
        }
        public long Ns;

        void Create(in GenEnt e)
        {
            int p = e.Id;
            int i = (int)(e.Mv >> 18), k = (int)(e.Mv >> 16 & 3), to = (int)(e.Mv & 0xFFFF);
            int from = boxes[p * n + i];
            LoadNode(p);
            var pn = nodes[p];
            Local(from, to, out int trapB, out int mobB, out int trapA, out int mobA);
            var nd = new GenNode { Parent = p, Mv = e.Mv, Depth = pn.Depth + 1 };
            nd.Trap = pn.Trap - trapB + trapA; nd.Mob = pn.Mob - mobB + mobA;
            nd.Far = pn.Far - gd[from] + gd[to];
            nd.G = pn.G;
            var pf = ps.pff!;
            if (pf.IsGoal(from)) nd.G--;
            if (pf.IsGoal(to)) nd.G++;
            nd.Hash = pn.Hash ^ z[from] ^ z[to];
            occ[from] = false; occ[to] = true;
            cur[i] = to;
            curId = -1;
            int minC = Flood(to + dirs[k], out int regs);
            nd.Player = minC; nd.Reg = regs;
            ulong full = nd.Hash ^ zp[minC];
            if (seen.ContainsKey(full)) { Dupes++; return; }
            if (HubGenFlags.Bic) nd.Bic = pf.Bicon(occ);
            int id = AddNode(nd, new ReadOnlySpan<ushort>(boxes, p * n, n), i, (ushort)to);
            seen[full] = id;
            curId = id;
            Created++;
            if (nd.Reg <= Math.Max(HubGenFlags.Pockets, 1)) Cand.Add(id);
            Note(id);
            Push(new GenEnt { Key = NodeKey(nd), Id = id, Mv = GenExpand }, nd.Reg, nd.Trap);
        }

        int MacroBfs(int c, int limit)
        {
            visStamp++;
            uint vs = visStamp;
            int cnt = 0;
            occ[c] = false;
            for (int k = 0; k < 4; k++)
            {
                int d = dirs[k], p = c + d;
                if (mark[p] != stamp || lab[p] != 1) continue;
                int qq = p + d;
                if (!wall[qq] && !occ[qq])
                {
                    vis[p * 4 + k] = vs;
                    if (cnt == st.Length) Array.Resize(ref st, cnt * 2);
                    st[cnt++] = new MacroSt { X = p, Par = -1, K = (sbyte)k };
                }
            }
            for (int idx = 0; idx < cnt && cnt < limit; idx++)
            {
                int x = st[idx].X, k = st[idx].K;
                int dk = dirs[k];
                void Try(int nx, int nk)
                {
                    int v = nx * 4 + nk;
                    if (vis[v] != vs)
                    {
                        vis[v] = vs;
                        if (cnt == st.Length) Array.Resize(ref st, cnt * 2);
                        st[cnt++] = new MacroSt { X = nx, Par = idx, K = (sbyte)nk };
                    }
                }
                bool Free(int cc) => !wall[cc] && !occ[cc];
                if (Free(x + 2 * dk)) Try(x + dk, k);
                for (int k2 = 0; k2 < 4; k2++)
                {
                    int d2 = dirs[k2];
                    if (k2 >> 1 == k >> 1) continue;
                    if (Free(x + dk + d2) && Free(x + d2) && Free(x + 2 * d2)) Try(x + d2, k2);
                }
            }
            occ[c] = true;
            return cnt;
        }

        int DReg(int a, int b)
        {
            Span<int> seenL = stackalloc int[4];
            int k = 0;
            for (int j = 0; j < 4; j++)
            {
                int c = a + dirs[j];
                if (c == b || wall[c] || occ[c]) continue;
                int l = lab[c];
                bool dup = false;
                for (int t = 0; t < k; t++) dup |= seenL[t] == l;
                if (!dup) seenL[k++] = l;
            }
            if (k == 0) return 1;
            return 1 - k;
        }

        void Expand(int id)
        {
            LoadNode(id);
            var nd = nodes[id];
            Expanded++;
            Flood(nd.Player, out _);
            Span<int> cnt = stackalloc int[64];
            var rc = Reach;
            if (rc != null) rc.Counts(NodeBoxes(id), cnt);
            nTop = 0;
            for (int i = 0; i < n; i++)
            {
                int c = boxes[id * n + i];
                int ns = MacroBfs(c, HubGenFlags.Macro);
                if (ns == 0) continue;
                for (int si = 0; si < ns; si++)
                {
                    var s = st[si];
                    int x = s.X;
                    if (x == c || dist[x] < 0 || rc != null && (!rc.InU[x] || !rc.Ok(c, x, cnt))) continue;
                    if (ps.PfInefficient(x, c)) continue;
                    Local(c, x, out int trapB, out int mobB, out int trapA, out int mobA);
                    int er = Math.Max(nd.Reg + DReg(c, x), 1), et = nd.Trap - trapB + trapA;
                    ulong key = Key(er, et, nd.Bic, nd.Far - gd[c] + gd[x], nd.Mob - mobB + mobA);
                    var ent = new GenEnt { Key = key, Id = id, Mv = (uint)i << 18 | (uint)s.K << 16 | (uint)x };
                    if (nTop == HubGenFlags.TopK && !GenEnt.Less(ent, top[nTop - 1])) continue;
                    if (nTop < HubGenFlags.TopK) { top[nTop] = ent; topR[nTop] = er; topT[nTop] = et; nTop++; }
                    else { top[nTop - 1] = ent; topR[nTop - 1] = er; topT[nTop - 1] = et; }
                    for (int j = nTop - 1; j > 0 && GenEnt.Less(top[j], top[j - 1]); j--)
                    {
                        (top[j], top[j - 1]) = (top[j - 1], top[j]);
                        (topR[j], topR[j - 1]) = (topR[j - 1], topR[j]);
                        (topT[j], topT[j - 1]) = (topT[j - 1], topT[j]);
                    }
                }
            }
            for (int j = 0; j < nTop; j++) Push(top[j], topR[j], topT[j]);
        }

        public void Unpack(int id, int[] curOut) { for (int i = 0; i < n; i++) curOut[i] = boxes[id * n + i]; }

        public PfFest.Tuple Tuple(int id) { ref var nd = ref nodes[id]; return new PfFest.Tuple(nd.Reg, 0, nd.Trap, nd.Far, nd.Bic); }

        /// <summary>The forward pushes from node id's position to the solved position: each pull-walk undone, newest first.</summary>
        public MPush[]? Tail(int id)
        {
            var outL = new List<MPush>();
            ReadOnlySpan<sbyte> opp = [1, 0, 3, 2];
            for (; nodes[id].Parent >= 0; id = nodes[id].Parent)
            {
                ref var nd = ref nodes[id];
                int par = nd.Parent;
                int i = (int)(nd.Mv >> 18); sbyte k = (sbyte)(nd.Mv >> 16 & 3); int x = (int)(nd.Mv & 0xFFFF);
                int c = boxes[par * n + i];
                LoadNode(par);
                Flood(nodes[par].Player, out _);
                int cntS = MacroBfs(c, 1 << 30);
                int at = -1;
                for (int j = 0; j < cntS; j++) if (st[j].X == x && st[j].K == k) { at = j; break; }
                if (at < 0) return null;
                for (int j = at; j >= 0; j = st[j].Par) outL.Add(new MPush(st[j].X, opp[st[j].K]));
            }
            return outL.ToArray();
        }

        public bool NearDup(int a, int b, int delta)
        {
            ddStamp++;
            uint s = ddStamp;
            for (int i = 0; i < n; i++) dd[boxes[a * n + i]] = s;
            int common = 0;
            for (int i = 0; i < n; i++) if (dd[boxes[b * n + i]] == s) common++;
            return n - common < delta;
        }

        public void Invalidate()
        {
            if (curId != -2) { for (int i = 0; i < n; i++) occ[cur[i]] = false; curId = -2; }
        }
    }
}
