using System.Numerics;

namespace Soksolv.Search;

/// <summary>
/// Hall credit gate for the fa2 engine (Go hall.go, exp-641/681, defaults: side relation on, sink walls on, supply staging off).
/// fa2's packed prefix never unpacks, so it treats the plan's credited targets as permanent; under that assumption the loose
/// boxes must still match the open goals, each serving the goals a lone box can be pushed to. A node's packed credit is the
/// largest prefix whose walls leave a perfect matching. Levels (k = first k permanent entries walled) are built lazily.
/// </summary>
public sealed unsafe partial class FullSolver
{
    sealed class HallLevel
    {
        public ulong[] Wall = [], Open = [], Adj = [];
        public sbyte[]?[]? Reg; public ulong[]? Sadj;
    }
    sealed class HallM
    {
        public int[] Mb = [], Mg = [];
        public void Init(int nb, int ng) { Mb = new int[nb]; Mg = new int[ng]; Reset(); }
        public void Reset() { Array.Fill(Mb, -1); Array.Fill(Mg, -1); }
        public void CopyFrom(HallM o) { Array.Copy(o.Mb, Mb, Mb.Length); Array.Copy(o.Mg, Mg, Mg.Length); }
    }
    sealed class HallPL
    {
        public uint St; public HallM M = new(); public int Loose, Def;
        public uint[] Bst = []; public int[] Bs = []; public ulong[] Bf = [];
    }

    sealed class Fa2Hall
    {
        readonly FullSolver s;
        public readonly int Ng, Gw, Cw;
        readonly int[] gidx, goalAt;
        readonly int[] permAt, permPos, permTo;
        readonly HallLevel?[] lv;
        public readonly bool Off;
        readonly ulong[] vis;
        readonly HallM m0 = new(), mc = new(), mw = new();
        bool parentOK;
        readonly HallPL[] pl;
        uint pst;
        readonly int[] pc;
        int lastLoose;
        readonly bool side;
        int pp, cp;
        readonly sbyte[]? sinkBit;
        ulong mask, pmask;
        readonly Dictionary<ulong, HallLevel?[]> lvm = new();
        bool mcReady;
        public long Nodes, Incons, Gated, Levels, Dead0, DefSum, Skipped, Lazy;
        // build scratch
        sbyte[]? comp; uint[]? mark; uint mst; int[] q = [];
        const int HallCap = 16 << 20;

        public Fa2Hall(FullSolver s, FestPlan fp)
        {
            this.s = s;
            int n = s.cells;
            Cw = (n + 63) >> 6;
            gidx = new int[n];
            var ga = new List<int>();
            for (int c = 0; c < n; c++) { gidx[c] = -1; if (s.goal[c]) { gidx[c] = ga.Count; ga.Add(c); } }
            goalAt = ga.ToArray();
            Ng = goalAt.Length; Gw = (Ng + 63) >> 6;
            permAt = new int[fp.E.Count + 1];
            var pPos = new List<int>(); var pTo = new List<int>();
            for (int i = 0; i < fp.E.Count; i++)
            {
                var e = fp.E[i];
                permAt[i + 1] = permAt[i];
                if (e.Until == -1 && e.To >= 0 && e.To < n) { permAt[i + 1]++; pPos.Add(i); pTo.Add(e.To); }
            }
            permPos = pPos.ToArray(); permTo = pTo.ToArray();
            lv = new HallLevel?[permTo.Length + 1];
            side = s.opt.Fa2.HallSide;
            if (s.opt.Fa2.HallSink)
            {
                sinkBit = new sbyte[n];
                int nb = 0;
                for (int c = 0; c < n; c++)
                {
                    sinkBit[c] = -1;
                    if (s.wall[c] || !s.goal[c] || nb >= 64) continue;
                    bool can = false;
                    foreach (int d in s.dirs) if (!s.wall[c + d] && !s.wall[c - d]) can = true;
                    if (!can) sinkBit[c] = (sbyte)nb++;
                }
            }
            pl = new HallPL[permTo.Length + 1];
            for (int i = 0; i < pl.Length; i++) pl[i] = new HallPL();
            pc = new int[s.n];
            int free = 0;
            foreach (bool w in s.wall) if (!w) free++;
            if ((long)free * free + 4L * Ng * free > HallCap) Off = true;
            vis = new ulong[Gw];
            m0.Init(s.n, Ng); mc.Init(s.n, Ng); mw.Init(s.n, Ng);
        }

        HallLevel Level(int k)
        {
            var arr = lv;
            if (mask != 0)
            {
                if (!lvm.TryGetValue(mask, out var a)) { a = new HallLevel?[lv.Length]; lvm[mask] = a; }
                arr = a;
            }
            return arr[k] ??= Build(k);
        }

        public ulong SinkMask(ReadOnlySpan<int> cur)
        {
            ulong m = 0;
            if (sinkBit != null) foreach (int c in cur) { int b = sinkBit[c]; if (b >= 0) m |= 1UL << b; }
            return m;
        }

        HallLevel Build(int k)
        {
            int n = s.cells;
            var l = new HallLevel { Wall = new ulong[Cw], Adj = new ulong[n * Gw] };
            Levels++;
            sbyte[]? lab = null; var flooded = new List<int>(); int regBytes = 0;
            if (side)
            {
                l.Reg = new sbyte[]?[n]; l.Sadj = new ulong[4 * n * Gw];
                lab = new sbyte[n]; Array.Fill(lab, (sbyte)-1);
            }
            bool Blocked(int c) => s.wall[c] || (l.Wall[c >> 6] >> (c & 63) & 1) != 0;
            for (int i = 0; i < k; i++) { int c = permTo[i]; l.Wall[c >> 6] |= 1UL << (c & 63); }
            if (mask != 0 && sinkBit != null)
                for (int c = 0; c < n; c++) if (sinkBit[c] >= 0 && (mask >> sinkBit[c] & 1) != 0) l.Wall[c >> 6] |= 1UL << (c & 63);
            l.Open = new ulong[Gw];
            for (int gi = 0; gi < Ng; gi++) if (!Blocked(goalAt[gi])) l.Open[gi >> 6] |= 1UL << (gi & 63);
            if (comp == null) { comp = new sbyte[4 * n]; mark = new uint[n]; q = new int[n + 4]; }
            for (int b = 0; b < n; b++)
            {
                for (int d = 0; d < 4; d++) comp[b * 4 + d] = -1;
                if (Blocked(b)) continue;
                Span<bool> done = stackalloc bool[4];
                int nreg = 0;
                flooded.Clear();
                for (int d = 0; d < 4; d++)
                {
                    int x = b + s.dirs[d];
                    if (done[d] || Blocked(x)) continue;
                    mst++;
                    mark![x] = mst;
                    int tail = 0; q[tail++] = x;
                    for (int i = 0; i < tail; i++)
                        foreach (int o in s.dirs)
                        {
                            int y = q[i] + o;
                            if (y != b && mark[y] != mst && !Blocked(y)) { mark[y] = mst; q[tail++] = y; }
                        }
                    for (int d2 = 0; d2 < 4; d2++)
                    {
                        int y = b + s.dirs[d2];
                        if (!Blocked(y) && mark[y] == mst) { done[d2] = true; comp[b * 4 + d2] = (sbyte)d; }
                    }
                    nreg++;
                    if (lab != null) { for (int i = 0; i < tail; i++) { lab[q[i]] = (sbyte)d; flooded.Add(q[i]); } }
                }
                if (lab != null)
                {
                    if (nreg >= 2 && regBytes + n <= HallCap / 4)
                    {
                        regBytes += n;
                        l.Reg![b] = (sbyte[])lab.Clone();
                    }
                    foreach (int y in flooded) lab[y] = -1;
                }
            }
            // one pull search per open goal: state (box cell, player side)
            var seen = new uint[4 * n]; uint st = 0;
            var qq = new List<int>();
            for (int gi = 0; gi < Ng; gi++)
            {
                int g = goalAt[gi];
                if (Blocked(g)) continue;
                st++;
                int gwi = gi >> 6; ulong gb = 1UL << (gi & 63);
                l.Adj[g * Gw + gwi] |= gb;
                qq.Clear();
                for (int d = 0; d < 4; d++) if (comp[g * 4 + d] >= 0) { seen[g * 4 + d] = st; qq.Add(g * 4 + d); }
                for (int i = 0; i < qq.Count; i++)
                {
                    int b = qq[i] / 4, d = qq[i] % 4;
                    int off = s.dirs[d];
                    int to = b + off, p = b + 2 * off;
                    if (Blocked(to) || Blocked(p)) continue;
                    l.Adj[to * Gw + gwi] |= gb;
                    int lb = comp[to * 4 + d];
                    for (int d2 = 0; d2 < 4; d2++)
                    {
                        int k2 = to * 4 + d2;
                        if (comp[k2] == lb && seen[k2] != st) { seen[k2] = st; qq.Add(k2); }
                    }
                }
                if (l.Sadj != null) foreach (int sid in qq) l.Sadj[sid * Gw + gwi] |= gb;
            }
            if (l.Sadj != null)
            {
                // fold each cut cell's side rows into its region's lowest direction
                for (int b = 0; b < n; b++)
                {
                    if (l.Reg![b] == null) continue;
                    for (int d = 0; d < 4; d++)
                    {
                        int lb = comp[b * 4 + d];
                        if (lb < 0 || lb == d) continue;
                        int dst = (b * 4 + lb) * Gw, src = (b * 4 + d) * Gw;
                        for (int w = 0; w < Gw; w++) l.Sadj[dst + w] |= l.Sadj[src + w];
                    }
                }
            }
            return l;
        }

        /// <summary>Goal row of a box on c with the player on cell p (side relation: only the player's region on a cut cell).</summary>
        (ulong[] arr, int off) Row(HallLevel l, int c, int p)
        {
            if (l.Reg != null && p >= 0)
            {
                var r = l.Reg[c];
                if (r != null)
                {
                    int lb = r[p];
                    if (lb >= 0) return (l.Sadj!, (c * 4 + lb) * Gw);
                }
            }
            return (l.Adj, c * Gw);
        }

        bool Augment(HallLevel l, int[] cur, int i, HallM m, int p)
        {
            var (arr, off) = Row(l, cur[i], p);
            for (int w = 0; w < Gw; w++)
            {
                ulong rw = arr[off + w];
                for (ulong word = rw & ~vis[w]; word != 0; word = rw & ~vis[w])
                {
                    int b = BitOperations.TrailingZeroCount(word);
                    vis[w] |= 1UL << b;
                    int g = (w << 6) + b;
                    int j = m.Mg[g];
                    if (j < 0 || Augment(l, cur, j, m, p)) { m.Mg[g] = i; m.Mb[i] = g; return true; }
                }
            }
            return false;
        }

        bool Walled(HallLevel l, int c) => (l.Wall[c >> 6] >> (c & 63) & 1) != 0;

        int Solve(int k, int[] cur, HallM m, int p)
        {
            var l = Level(k);
            int loose = 0;
            for (int i = 0; i < m.Mb.Length; i++)
            {
                int g = m.Mb[i];
                int c = cur[i];
                bool isLoose = !Walled(l, c);
                if (isLoose) loose++;
                if (g >= 0)
                {
                    bool bad = !isLoose || m.Mg[g] != i;
                    if (!bad) { var (arr, off) = Row(l, c, p); bad = (arr[off + (g >> 6)] >> (g & 63) & 1) == 0; }
                    if (bad)
                    {
                        if (m.Mg[g] == i) m.Mg[g] = -1;
                        m.Mb[i] = -1;
                    }
                }
            }
            for (int g = 0; g < m.Mg.Length; g++)
            {
                int i = m.Mg[g];
                if (i >= 0 && m.Mb[i] != g) m.Mg[g] = -1;
            }
            int size = 0;
            for (int i = 0; i < m.Mb.Length; i++)
            {
                if (m.Mb[i] >= 0) { size++; continue; }
                if (Walled(l, cur[i])) continue;
                Array.Clear(vis);
                if (Augment(l, cur, i, m, p)) size++;
            }
            lastLoose = loose;
            return loose - size;
        }

        int PSolve(int k, int[] cur, HallM m)
        {
            int d = Solve(k, cur, m, pp);
            var q0 = pl[k];
            if (q0.M.Mb.Length == 0)
            {
                q0.M.Init(s.n, Ng);
                q0.Bst = new uint[s.n]; q0.Bs = new int[s.n]; q0.Bf = new ulong[s.n * Gw];
            }
            q0.M.CopyFrom(m);
            q0.Loose = lastLoose; q0.Def = d; q0.St = pst;
            return d;
        }

        void BoxStruct(int k, int i)
        {
            var p0 = pl[k]; var l = Level(k);
            var m = mw;
            m.CopyFrom(p0.M);
            int g0 = m.Mb[i];
            if (g0 >= 0) { m.Mg[g0] = -1; m.Mb[i] = -1; }
            int size = 0;
            foreach (int g in m.Mb) if (g >= 0) size++;
            if (p0.Def > 0 || p0.M.Mb[i] < 0)
            {
                for (int j = 0; j < m.Mb.Length; j++)
                {
                    if (m.Mb[j] >= 0 || j == i || Walled(l, pc[j])) continue;
                    Array.Clear(vis);
                    if (Augment(l, pc, j, m, pp)) size++;
                }
            }
            int fo = i * Gw;
            for (int w = 0; w < Gw; w++) p0.Bf[fo + w] = l.Open[w];
            for (int g = 0; g < m.Mg.Length; g++) if (m.Mg[g] >= 0) p0.Bf[fo + (g >> 6)] &= ~(1UL << (g & 63));
            for (bool changed = true; changed;)
            {
                changed = false;
                for (int j = 0; j < m.Mb.Length; j++)
                {
                    int g = m.Mb[j];
                    if (g < 0 || (p0.Bf[fo + (g >> 6)] >> (g & 63) & 1) != 0) continue;
                    var (arr, off) = Row(l, pc[j], pp);
                    for (int w = 0; w < Gw; w++)
                        if ((arr[off + w] & p0.Bf[fo + w]) != 0) { p0.Bf[fo + (g >> 6)] |= 1UL << (g & 63); changed = true; break; }
                }
            }
            p0.Bs[i] = size; p0.Bst[i] = pst;
        }

        int DAt(int k, int[] cur, int i, int b)
        {
            var p0 = pl[k];
            if (sinkBit != null)
            {
                ulong cm = SinkMask(cur);
                if (cm != pmask)
                {
                    mask = cm;
                    if (!mcReady) { mc.Reset(); mcReady = true; }
                    int dd = Solve(k, cur, mc, cp);
                    mask = pmask;
                    return dd;
                }
                mask = pmask;
            }
            if (parentOK && i >= 0 && p0.St != pst)
            {
                mc.CopyFrom(m0);
                mcReady = false;
                PSolve(k, pc, mc);
                Lazy++;
            }
            if (parentOK && i >= 0 && p0.St == pst)
            {
                if (p0.Def >= 2) { Skipped++; return 1; }
                if (p0.Bst[i] != pst) BoxStruct(k, i);
                var l = Level(k);
                int t = cur[i];
                int loose = p0.Loose, size = p0.Bs[i];
                if (!Walled(l, b)) loose--;
                if (!Walled(l, t))
                {
                    loose++;
                    var (arr, off) = Row(l, t, cp);
                    int fo = i * Gw;
                    for (int w = 0; w < Gw; w++) if ((arr[off + w] & p0.Bf[fo + w]) != 0) { size++; break; }
                }
                return loose - size;
            }
            if (!mcReady)
            {
                if (parentOK) mc.CopyFrom(m0); else mc.Reset();
                mcReady = true;
            }
            return Solve(k, cur, mc, cp);
        }

        /// <summary>Records the expanded node's matchings (occ holds cur, p its raw prefix) at every level its credit scan visits.</summary>
        public void Parent(int[] cur, int p, int player)
        {
            parentOK = false;
            pp = player;
            if (Off) return;
            Nodes++;
            pst++;
            if (pst == 0) { foreach (var x in pl) { x.St = 0; Array.Clear(x.Bst); } pst = 1; }
            Array.Copy(cur, pc, cur.Length);
            m0.Reset();
            pmask = SinkMask(cur);
            mask = pmask;
            p = Math.Min(Math.Max(p, 0), permAt.Length - 1);
            int k = permAt[p];
            int d = PSolve(k, cur, m0);
            if (d > 0) { Incons++; DefSum += d; }
            while (d > 0 && k > 0) { k--; d = PSolve(k, cur, m0); }
            parentOK = true;
        }

        /// <summary>Credited prefix of a child (cur holds its cells, box i moved from b; i &lt; 0: no parent), p its raw prefix.</summary>
        public int Child(int[] cur, int p, int i, int b, int player)
        {
            if (Off) return p;
            cp = player;
            mcReady = false;
            p = Math.Min(Math.Max(p, 0), permAt.Length - 1);
            int k = permAt[p];
            int d = DAt(k, cur, i, b);
            while (d > 0 && k > 0) { k--; d = DAt(k, cur, i, b); }
            int res = p;
            if (k != permAt[p]) { res = permPos[k]; Gated++; }
            if (d > 0 && (s.opt.Fa2.HallSink || s.opt.Fa2.HallSide)) { res = 0; Dead0++; }
            return res;
        }
    }

    Fa2Hall? fa2Hall;
}
