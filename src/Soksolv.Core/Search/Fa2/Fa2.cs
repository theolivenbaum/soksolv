using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// fa2 lane (port of Go fa2.go): a plan-guided forward FESS engine. Children of an expanded node are scored once and stored
/// as 16-byte edges (windowed: only the next K+1 are kept); duplicates are skipped inside the cell turn by one hash probe;
/// the first expansion owns a state. Reuses the macro move generator (box search + push-dead tests), corral restriction,
/// the Festival-prefix plan and the node records of <see cref="FullSolver"/>.
/// </summary>
public sealed unsafe partial class FullSolver
{
    // ---- state ------------------------------------------------------------------------------------------------------------
    bool fa2On;
    static readonly bool fa2Trace = Environment.GetEnvironmentVariable("SOKSOLV_FA2TRACE") == "1";
    FestPlan? fa2Fp;
    readonly ChunkList<Fa2Aux> fa2Aux = new();
    readonly ChunkList<Fa2Edge> fa2Ed = new();
    readonly U64Set fa2Seen = new();
    readonly List<(uint key, List<Fa2HE> heap)> fa2Cells = new();
    readonly Dictionary<uint, int> fa2CellIdx = new();
    int fa2BestCell = -1, fa2Rr, fa2Turn;
    ulong[] fa2Zb = [], fa2Zp = [];
    Fa2Cand[] fa2Cand = new Fa2Cand[64]; int fa2CandN;
    Fa2Ord[] fa2Ord = new Fa2Ord[64];
    int[] fa2Scratch = [];
    readonly int[] fa2Chosen = new int[NAdv];
    int fa2Rebase = -1;
    long fa2Dups, fa2LateDead, fa2LateDead2, fa2Refills, fa2ExpandedN;
    ulong fa2ImpKey = ~0UL; long fa2ImpAt;
    ulong[]?[]? fa2RevReach;
    Dictionary<int, int[]?> fa2ZDist = new();

    const int AdvPack = 0, AdvConn = 1, AdvRooms = 2, AdvHot = 3, AdvOop = 4, AdvOpener = 5, AdvSmall = 6, AdvExplorer = 7, NAdv = 8;

    struct Fa2Cand
    {
        public ulong Key, Hash; public int T; public ushort I; public byte Side, W, Adv; public int Np, B, Packed, Conn, Rooms, Oop, Hot;
        public bool Explorer, Checked;
        public readonly Fa2Edge Edge() => new() { Key = Key, B = (ushort)B, T = (ushort)T, Np = (ushort)Np, Side = Side, Wadv = (byte)(W & 1 | Adv << 1) };
    }
    struct Fa2Ord { public ulong Key; public uint Wi; }

    // ---- keys ---------------------------------------------------------------------------------------------------------------
    ulong Fa2Key(int packed, int conn, int rooms, int oop, int hot, int dist)
    {
        static ulong c8(int x) => (ulong)Math.Min(Math.Max(x, 0), 255);
        ulong d = opt.Fa2.Dist ? 0xFFFFFFUL - (ulong)Math.Min(Math.Max(dist, 0), 0xFFFFFF) : 0;
        return (255 - c8(packed)) << 56 | c8(conn) << 48 | c8(rooms) << 40 | c8(oop) << 32 | c8(hot) << 24 | d;
    }
    static uint Fa2CellKey(ulong k) => (uint)(k >> 56) << 16 | (uint)(k >> 48 & 255) << 8 | (uint)(k >> 32 & 255);
    static void Fa2Fields(ulong k, out int packed, out int conn, out int rooms, out int oop, out int hot)
    {
        packed = 255 - (int)(k >> 56); conn = (int)(k >> 48 & 255); rooms = (int)(k >> 40 & 255); oop = (int)(k >> 32 & 255); hot = (int)(k >> 24 & 255);
    }

    int Fa2PackedNow()
    {
        if (fa2Fp == null) { int c = 0; foreach (int g in goals) if (occ[g]) c++; return c; }
        return fa2Fp.Packed(occ);
    }
    int Fa2OopNow(ReadOnlySpan<int> boxes, int packed) => fa2Fp == null || mPlan == null ? 0 : OopFestCount(boxes, packed);

    // ---- cell heaps ----------------------------------------------------------------------------------------------------------
    static bool HeLess(in Fa2HE a, in Fa2HE b) => a.W != b.W ? a.W < b.W : a.Key != b.Key ? a.Key < b.Key : a.Id < b.Id;

    ref Fa2Aux A(int id) => ref fa2Aux[id];

    void Fa2Push(int c, int id)
    {
        ref var a = ref A(id);
        ref var e = ref fa2Ed[a.EOff + a.ECur];
        var h = fa2Cells[c].heap;
        h.Add(new Fa2HE { W = a.W + e.W, Id = id, Key = e.Key });
        int i = h.Count - 1;
        while (i > 0)
        {
            int p = (i - 1) / 2;
            if (!HeLess(h[i], h[p])) break;
            (h[i], h[p]) = (h[p], h[i]);
            i = p;
        }
        if (fa2BestCell < 0 || fa2Cells[c].key < fa2Cells[fa2BestCell].key) fa2BestCell = c;
    }

    int Fa2Pop(int c)
    {
        var h = fa2Cells[c].heap;
        int top = h[0].Id;
        int last = h.Count - 1;
        h[0] = h[last];
        h.RemoveAt(last);
        int i = 0;
        while (true)
        {
            int l = 2 * i + 1, r = 2 * i + 2, m = i;
            if (l < h.Count && HeLess(h[l], h[m])) m = l;
            if (r < h.Count && HeLess(h[r], h[m])) m = r;
            if (m == i) break;
            (h[i], h[m]) = (h[m], h[i]);
            i = m;
        }
        return top;
    }

    int Fa2CellFor(ulong key)
    {
        uint ck = Fa2CellKey(key);
        if (fa2CellIdx.TryGetValue(ck, out int c)) return c;
        c = fa2Cells.Count;
        fa2Cells.Add((ck, new List<Fa2HE>()));
        fa2CellIdx[ck] = c;
        return c;
    }

    int Fa2PickCell()
    {
        if (fa2Turn % 2 == 0)
        {
            if (fa2BestCell >= 0 && fa2Cells[fa2BestCell].heap.Count == 0)
            {
                fa2BestCell = -1;
                for (int c = 0; c < fa2Cells.Count; c++)
                    if (fa2Cells[c].heap.Count > 0 && (fa2BestCell < 0 || fa2Cells[c].key < fa2Cells[fa2BestCell].key)) fa2BestCell = c;
            }
            if (fa2BestCell >= 0) return fa2BestCell;
        }
        for (int k = 0; k < fa2Cells.Count; k++)
        {
            int c = (fa2Rr + k) % fa2Cells.Count;
            if (fa2Cells[c].heap.Count > 0) { fa2Rr = c + 1; return c; }
        }
        return -1;
    }

    int Fa2AddNode(int player, int parent, int from, sbyte side, byte adv, ulong key, ulong best, int w, ulong boxH)
    {
        // the child's key words are in ckey
        int id = nodes.AppendUnindexed(ckey, player);
        nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = side; nodes.W(id) = w;
        fa2Aux.Add(new Fa2Aux { Score = key, Best = best, W = w, BoxH = boxH, Adv = adv });
        return id;
    }

    // ---- helpers ----------------------------------------------------------------------------------------------------------------
    int Fa2Mobility(ReadOnlySpan<int> cur, int moved, int t, uint[] mk, uint st)
    {
        int cnt = 0;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = i == moved ? t : cur[i];
            foreach (int d in dirs) if (mk[Front(b, d)] == st) cnt++;
        }
        return cnt;
    }

    /// <summary>A lone box on an empty board can be pushed from t back to b (Festival's explorer rejects irreversible moves).</summary>
    bool Fa2Reversible(int t, int b)
    {
        if (t == b) return true;
        fa2RevReach ??= new ulong[]?[cells];
        var r = fa2RevReach[t];
        if (r == null)
        {
            r = new ulong[(cells + 63) / 64];
            r[t >> 6] |= 1UL << (t & 63);
            var q = new List<int> { t };
            for (int i = 0; i < q.Count; i++)
            {
                int x = q[i];
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (wall[y] || wall[x - d] || (r[y >> 6] >> (y & 63) & 1) != 0) continue;
                    r[y >> 6] |= 1UL << (y & 63);
                    q.Add(y);
                }
            }
            fa2RevReach[t] = r;
        }
        return (r[b >> 6] >> (b & 63) & 1) != 0;
    }

    /// <summary>The child (occ; m3 == mpstamp its player region, mark == st the parent's) lets the player stand, for the first time, beside an unchanged box with that push free.</summary>
    bool Fa2Explores(ReadOnlySpan<int> cur, int moved, uint st)
    {
        int b = cur[moved];
        for (int j = 0; j < cur.Length; j++)
        {
            if (j == moved) continue;
            int x = cur[j];
            foreach (int d in dirs)
            {
                int sq = x + d;
                if (m3[sq] != mpstamp || sq == b || mark[sq] == st) continue;
                int dst = x - d;
                if (!wall[dst] && !occ[dst]) return true;
            }
        }
        return false;
    }

    /// <summary>Apply-time deadlock tests (lazy cluster, corral) on the child of moving cur[i] to t through side; occ holds the parent and is restored.</summary>
    bool Fa2DeadLate(int[] cur, int i, int t, int side, int[] child)
    {
        int b = cur[i];
        Array.Copy(cur, child, cur.Length);
        child[i] = t;
        Array.Sort(child);
        SetOcc(b, false); SetOcc(t, true);
        try
        {
            int ps = t - dirs[side];
            if (cl != null)
            {
                Reach(ps, mark2);
                uint st = stamp;
                if (cl.Dead(occ, t, ps, mark2, st, -1)) return true;
            }
            return dz != null && dz.DeadAfter(occ, child, ps, t);
        }
        finally { SetOcc(t, false); SetOcc(b, true); }
    }

    /// <summary>Push distance of a lone box from plan step k's out-of-plan zone, on the board whose walls are the walls plus the step's parked targets.</summary>
    int[]? Fa2ZoneDist(int k)
    {
        if (fa2Fp == null || oopz == null || oopz.N == 0 || k >= oopz.N - 1) return null;
        if (fa2ZDist.TryGetValue(k, out var dcached)) return dcached;
        var z = oopz;
        int rowBase = k * z.W;
        var blocked = (bool[])wall.Clone();
        for (int j = 0; j <= k && j < fa2Fp.E.Count; j++)
        {
            var e = fa2Fp.E[j];
            if (e.Until == -1 && e.To >= 0) blocked[e.To] = true;
        }
        var d = new int[cells];
        var q = new List<int>();
        for (int c = 0; c < cells; c++)
        {
            d[c] = -1;
            if ((z.Z[rowBase + (c >> 6)] >> (c & 63) & 1) != 0) { d[c] = 0; q.Add(c); }
        }
        for (int i = 0; i < q.Count; i++)
        {
            int x = q[i];
            foreach (int dd in dirs)
            {
                int y = x + dd;
                if (blocked[y] || blocked[x - dd] || d[y] >= 0) continue;
                d[y] = d[x] + 1;
                q.Add(y);
            }
        }
        fa2ZDist[k] = d;
        return d;
    }

    // ---- expansion ------------------------------------------------------------------------------------------------------------------
    // per-expand state shared with the box-search visitor
    int[] fa2Cur = [];
    uint fa2St;
    int fa2VisitI, fa2Cands, fa2Solved;
    int fa2H;
    ulong fa2Bh;
    int[] fa2Counts = [];
    int fa2PPacked;
    uint fa2Ts;

    bool Fa2Visit(int k, int t, ReadOnlySpan<int> curSpan)
    {
        var cur = fa2Cur;
        int i = fa2VisitI, b = cur[i];
        int canonical = 0;
        for (int side = 0; side < 4; side++)
            if (m3[t - dirs[side]] == mpstamp) { canonical = side; break; }
        if (t == b && opt.Fa2.NoReturn) return true;
        int endpoint = t * 4 + canonical;
        if (mtseen[endpoint] == fa2Ts) return true;
        mtseen[endpoint] = fa2Ts;
        SetOcc(t, true);
        try
        {
            if (MPushDead(cur, i, t, canonical) != 0) return true;
            int np = mCandNP;
            var c = new Fa2Cand { B = b, T = t, I = (ushort)i, Side = (byte)(k & 3) };
            fa2Cands++;
            switch (fa2Cands)
            {
                case 1: c.Conn = Regions(); break;
                case 2:
                    SetOcc(t, false); ComputeArticulation(); SetOcc(t, true);
                    c.Conn = ChildRegions(t); break;
                default: c.Conn = ChildRegions(t); break;
            }
            c.Packed = Fa2PackedNow();
            c.Rooms = Fa2RoomsChild(b, t);
            c.Hot = HotAfter(cur, fa2Counts, i, t);
            cur[i] = t;
            c.Oop = Fa2OopNow(cur, c.Packed);
            Fa2HallChild(cur, ref c, i, b, np);
            cur[i] = b;
            if (opt.Fa2.Explorer && c.Packed >= fa2PPacked && Fa2Reversible(t, b)) c.Explorer = Fa2Explores(cur, i, fa2St);
            int ch = fa2H - dist[b] + dist[t];
            int last = ch;
            if (opt.Fa2.Mobility) last = Fa2Mobility(cur, i, t, m3, mpstamp);
            c.Key = Fa2Key(c.Packed, c.Conn, c.Rooms, c.Oop, c.Hot, last);
            c.Hash = fa2Bh ^ fa2Zb[b] ^ fa2Zb[t] ^ fa2Zp[np];
            c.Np = np;
            c.W = 1;
            if (ch == 0 && fa2Solved < 0) fa2Solved = fa2CandN;
            if (fa2CandN == fa2Cand.Length) Array.Resize(ref fa2Cand, fa2CandN * 2);
            fa2Cand[fa2CandN++] = c;
            return true;
        }
        finally { SetOcc(t, false); }
    }

    /// <summary>Scores every legal macro child of node id (occ holds its boxes, mark stamped st) and stores them as sorted edges. Returns the index of a child with every box on a goal (stored as the single edge), or -1... 0 when solved.</summary>
    int Fa2Expand(int id, int[] cur, uint st)
    {
        if (!Feasible(cur)) return -1;
        ref var a0 = ref A(id);
        Fa2Fields(a0.Score, out int pPacked, out int pConn, out int pRooms, out int pOop, out int pHot);
        int h = 0;
        foreach (int b in cur) h += dist[b];
        fa2Counts = HotCounts(cur);
        int openSq = -1;
        if (opt.Fa2.Opener && fa2Fp != null) openSq = fa2Fp.NextParking(occ);
        Fa2RoomsParent();
        Fa2HallParent(cur, id);
        uint corral = 0;
        if (opt.Fa2.Corral) corral = PiCorral(st);
        fa2Cur = cur; fa2St = st; fa2H = h; fa2PPacked = pPacked; fa2Bh = A(id).BoxH;
        fa2CandN = 0; fa2Solved = -1;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = cur[i];
            if (corral != 0 && cmark[b] != corral) continue;
            fa2Ts = ++mtst;
            fa2Cands = 0; fa2VisitI = i;
            MBoxSearch(b, st, false, 0, 0, cur);
        }
        int solved = fa2Solved;
        if (fa2Trace) Console.Error.WriteLine($"fa2 expand id={id} cands={fa2CandN} corral={corral} feasible");
        var cand = fa2Cand;
        int nc = fa2CandN;
        var chosen = fa2Chosen;
    again:
        Array.Fill(chosen, -1);
        void Pick(int adv, int k) { if (chosen[adv] < 0 || cand[k].Key < cand[chosen[adv]].Key) chosen[adv] = k; }
        bool raises = false;
        for (int k = 0; k < nc; k++)
        {
            ref var c = ref cand[k];
            if (c.Packed > pPacked) { Pick(AdvPack, k); raises = true; }
            if (c.Packed < pPacked) continue;
            if (c.Conn < pConn) Pick(AdvConn, k);
            if (c.Rooms < pRooms) Pick(AdvRooms, k);
            if (c.Hot < pHot) Pick(AdvHot, k);
            if (c.Explorer) Pick(AdvExplorer, k);
            if (opt.Fa2.OopAdv == 1 && pOop > 0 && c.Oop < pOop && c.Conn <= pConn && c.Rooms <= pRooms) Pick(AdvOop, k);
        }
        if (opt.Fa2.OopAdv == 2 && pOop > 0)
        {
            var zd = Fa2ZoneDist(pPacked);
            int bestFrom = 1 << 30, bestTo = -1;
            for (int k = 0; k < nc; k++)
            {
                ref var c = ref cand[k];
                if (zd == null || c.Packed < pPacked || c.Conn > pConn || c.Rooms > pRooms || c.Oop > pOop || !Fa2Reversible(c.T, c.B)) continue;
                int d0 = zd[c.B], d1 = zd[c.T];
                if (d0 < 0 || d1 < 0 || d1 <= d0 || d0 > bestFrom) continue;
                if (d0 < bestFrom || d1 > bestTo) { bestFrom = d0; bestTo = d1; chosen[AdvOop] = k; }
            }
        }
        if (openSq >= 0 && !raises)
        {
            int bestD = 1 << 30, bestFar = -1;
            for (int k = 0; k < nc; k++)
            {
                ref var c = ref cand[k];
                if (c.Packed != pPacked || c.Conn > pConn || c.Rooms > pRooms || (c.B == openSq && c.T == openSq)) continue;
                int d0 = MWalk(c.B, openSq), d1 = MWalk(c.T, openSq);
                if (d0 < 0 || d1 < d0) continue;
                if (d0 < bestD || (d0 == bestD && d1 > bestFar)) { bestD = d0; bestFar = d1; chosen[AdvOpener] = k; }
            }
        }
        if (opt.Fa2.SmallFree && nc > 0 && nc <= 2) chosen[AdvSmall] = 0;
        if (opt.Fa2.VerifyAdv)
        {
            int drop = -1;
            for (int ai = 0; ai < NAdv; ai++)
            {
                int k = chosen[ai];
                if (k >= 0 && !cand[k].Checked)
                {
                    cand[k].Checked = true;
                    if (Fa2DeadLate(cur, cand[k].I, cand[k].T, cand[k].Side, fa2Scratch)) { drop = k; break; }
                }
            }
            if (drop >= 0)
            {
                fa2LateDead2++;
                Array.Copy(cand, drop + 1, cand, drop, nc - drop - 1);
                nc--;
                if (solved > drop) solved--;
                goto again;
            }
        }
        for (int a = 0; a < NAdv; a++)
        {
            int k = chosen[a];
            if (k >= 0) { cand[k].W = 0; cand[k].Adv |= (byte)(1 << a); }
        }
        ref var node = ref A(id);
        if (solved >= 0 && fa2Rebase < 0)
        {
            node.EOff = fa2Ed.Count;
            fa2Ed.Add(cand[solved].Edge());
            node.ELen = 1;
            return 0;
        }
        if (fa2Ord.Length < nc) Array.Resize(ref fa2Ord, Math.Max(nc, fa2Ord.Length * 2));
        for (int i = 0; i < nc; i++) fa2Ord[i] = new Fa2Ord { Key = cand[i].Key, Wi = (uint)cand[i].W << 31 | (uint)i };
        Array.Sort(fa2Ord, 0, nc, Fa2OrdCmp.Instance);
        int win = opt.Fa2.EdgeWin;
        if (win > 0 && (fa2Rebase >= 0 || nc > win + 1))
        {
            Fa2StoreWindow(id, nc, cand);
            return -1;
        }
        node.EOff = fa2Ed.Count;
        for (int i = 0; i < nc; i++) fa2Ed.Add(cand[(int)(fa2Ord[i].Wi & (1u << 31) - 1)].Edge());
        node.ELen = nc;
        return -1;
    }

    sealed class Fa2OrdCmp : IComparer<Fa2Ord>
    {
        public static readonly Fa2OrdCmp Instance = new();
        public int Compare(Fa2Ord a, Fa2Ord b)
        {
            if (a.Wi >> 31 != b.Wi >> 31) return (int)(a.Wi >> 31) - (int)(b.Wi >> 31);
            if (a.Key < b.Key) return -1;
            if (a.Key > b.Key) return 1;
            return (int)(a.Wi & (1u << 31) - 1) - (int)(b.Wi & (1u << 31) - 1);
        }
    }

    void Fa2StoreWindow(int id, int nc, Fa2Cand[] cand)
    {
        ref var a = ref A(id);
        int baseI = Math.Max(fa2Rebase, 0);
        int n2 = Math.Min(nc - baseI, opt.Fa2.EdgeWin + 1);
        if (n2 < 0) n2 = 0;
        if (fa2Rebase < 0)
        {
            a.EOff = fa2Ed.Count;
            for (int j = 0; j < n2; j++) fa2Ed.Add(cand[(int)(fa2Ord[baseI + j].Wi & (1u << 31) - 1)].Edge());
        }
        else
        {
            n2 = Math.Min(n2, a.ELen);
            for (int j = 0; j < n2; j++) fa2Ed[a.EOff + j] = cand[(int)(fa2Ord[baseI + j].Wi & (1u << 31) - 1)].Edge();
        }
        a.EBase = baseI; a.ETot = nc; a.ELen = n2; a.ECur = 0;
    }

    void Fa2Refill(int id, int[] cur)
    {
        nodes.Unpack(id, cur);
        foreach (int b in cur) SetOcc(b, true);
        Reach(nodes.Player(id), mark);
        ref var a = ref A(id);
        fa2Rebase = a.EBase + a.ELen - 1;
        Fa2Expand(id, cur, stamp);
        fa2Rebase = -1;
        fa2Refills++;
        foreach (int b in cur) SetOcc(b, false);
    }

    int Fa2IdxOf(int[] cur, int b) { int i = Array.BinarySearch(cur, b); return i; }
}
