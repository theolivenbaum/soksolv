using System.Numerics;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Soksolv.Search.Reverse;

[InlineArray(4)]
public struct Set256
{
    ulong e0;
    public Span<ulong> S => MemoryMarshal.CreateSpan(ref e0, 4);
    public void Set(int j) => S[j >> 6] |= 1UL << (j & 63);
    public bool SameAs(in Set256 o) { var a = S; var b = MemoryMarshal.CreateReadOnlySpan(in o.e0, 4); return a.SequenceEqual(b); }
}

/// <summary>DFS forest of the free cells for one obstacle set (Tarjan lowpoints); see Go supGraph.</summary>
public sealed class SupGraph
{
    public Set256 Key;
    public uint Epoch;
    public int[] Disc, End, Low, Par, Top;
    public sbyte[] Nl; public uint[] NlStamp;
    public int N;
    public int[] SccOf, SccStamp;
    public List<Set256> SccR = new();
    public bool Allocated;
    public SupGraph() { Disc = End = Low = Par = Top = SccOf = SccStamp = []; Nl = []; NlStamp = []; }
    public void Alloc(int c)
    {
        Disc = new int[c]; End = new int[c]; Low = new int[c]; Par = new int[c]; Top = new int[c];
        Nl = new sbyte[4 * c]; NlStamp = new uint[c]; SccOf = new int[4 * c]; SccStamp = new int[4 * c];
        Allocated = true;
    }
}

public struct SupEntry { public ulong Fp; public Set256 R; }

/// <summary>Supply-aware ordering state (port of Go supply struct, exp-707).</summary>
public sealed class Supply
{
    public readonly int W; // key penalty per unit of U (x8)
    public readonly int Cells;
    public uint Epoch;
    public readonly bool[] Obst;
    public readonly SupGraph[] Slots;
    public readonly SupGraph Scratch;
    public SupEntry[]? Cache;
    public List<int> Stk = new(), Order = new();
    public readonly sbyte[] It;
    public readonly int[] TIdx, TLow; public readonly uint[] TStamp; public uint Tst;
    public List<int> TStk = new(), TFr = new();
    public readonly uint[] Seen; public readonly int[] Prev;
    public uint Stamp;
    public List<int> Q = new();
    public int Target;
    public Set256[] ReachOf = new Set256[16];
    public List<int> Off = new(), Left = new();
    public readonly int[] MatchOf; public readonly uint[] Gs; public uint Gstamp;
    public List<int> PathBuf = new();
    public long Evals, Deferred, Misses, Graphs, Macros, MacroOK;
    public const int SupSlotBytes = 5 * 4 + 4 + 4 + 32;
    public const int SupCache = 1 << 19;

    public Supply(ReverseSolver s, int w)
    {
        W = w; Cells = s.CellsCount;
        int nslots = 16;
        while (nslots < 1024 && 2L * nslots * Cells * SupSlotBytes <= 8 << 20) nslots *= 2;
        Obst = new bool[Cells]; Slots = new SupGraph[nslots];
        for (int i = 0; i < nslots; i++) Slots[i] = new SupGraph();
        It = new sbyte[Cells]; TIdx = new int[4 * Cells]; TLow = new int[4 * Cells]; TStamp = new uint[4 * Cells];
        Seen = new uint[Cells * 4]; Prev = new int[Cells * 4];
        MatchOf = new int[s.BoxCount]; Gs = new uint[s.BoxCount];
        Scratch = new SupGraph(); Scratch.Alloc(Cells);
    }

    public long Bytes()
    {
        int used = 1; foreach (var g in Slots) if (g.Allocated) used++;
        return (long)used * Cells * SupSlotBytes + (long)Cells * (1 + 1 + 4 + 4 + 4) * 4 + (Cache?.Length ?? 0) * 40L;
    }

    public void Reset(SupGraph g)
    {
        if (!g.Allocated) g.Alloc(Cells);
        Array.Clear(g.Disc);
        g.N = 0; g.SccR.Clear();
        Epoch++;
        g.Epoch = Epoch;
    }

    /// <summary>Extends g's forest with the component of root (obstacles in Obst); returns its smallest cell; Order lists the component.</summary>
    public int Dfs(ReverseSolver s, SupGraph g, int root)
    {
        int m = root;
        Order.Clear(); Order.Add(root);
        g.N++;
        g.Disc[root] = g.N; g.Low[root] = g.N; g.Par[root] = -1; g.Top[root] = root;
        It[root] = 0;
        Stk.Clear(); Stk.Add(root);
        var dirs = s.Dirs;
        while (Stk.Count > 0)
        {
            int v = Stk[^1];
            int k = It[v];
            if (k < 4)
            {
                It[v]++;
                int w = v + dirs[k];
                if (Obst[w]) continue;
                if (g.Disc[w] == 0)
                {
                    g.N++;
                    g.Disc[w] = g.N; g.Low[w] = g.N; g.Par[w] = v; g.Top[w] = root;
                    It[w] = 0;
                    Stk.Add(w); Order.Add(w);
                    if (w < m) m = w;
                }
                else if (w != g.Par[v]) g.Low[v] = Math.Min(g.Low[v], g.Disc[w]);
                continue;
            }
            Stk.RemoveAt(Stk.Count - 1);
            g.End[v] = g.N + 1;
            int p = g.Par[v];
            if (p >= 0) g.Low[p] = Math.Min(g.Low[p], g.Low[v]);
        }
        return m;
    }

    /// <summary>Extends g's forest with the component of p, rooted at its smallest cell.</summary>
    public void Tree(ReverseSolver s, SupGraph g, int p)
    {
        int n0 = g.N;
        int m = Dfs(s, g, p);
        if (m == p) return;
        foreach (int c in Order) g.Disc[c] = 0;
        g.N = n0;
        Dfs(s, g, m);
    }

    /// <summary>Piece code of w (a cell of z's tree, w != z) in the graph without z.</summary>
    public static sbyte Lab(int[] dirs, SupGraph g, int z, int w)
    {
        int dw = g.Disc[w], dz = g.Disc[z];
        if (dw <= dz || dw >= g.End[z]) return 0;
        for (int k = 0; k < 4; k++)
        {
            int c = z + dirs[k];
            int dc = g.Disc[c];
            if (dc > dz && dc <= dw && dw < g.End[c] && g.Par[c] == z)
            {
                if (g.Par[z] >= 0 && g.Low[c] < dz) return 0;
                return (sbyte)(k + 1);
            }
        }
        return 0;
    }

    /// <summary>Index into g.Nl of z's four neighbour piece codes (computed on demand; -1 = not a cell of the graph).</summary>
    public static int NLab(int[] dirs, SupGraph g, int z)
    {
        int b = z * 4;
        if (g.NlStamp[z] != g.Epoch)
        {
            g.NlStamp[z] = g.Epoch;
            for (int k = 0; k < 4; k++)
            {
                g.Nl[b + k] = -1;
                if (g.Disc[z + dirs[k]] != 0) g.Nl[b + k] = Lab(dirs, g, z, z + dirs[k]);
            }
        }
        return b;
    }

    public static int Canon(int[] dirs, SupGraph g, int y, int l)
    {
        int nb = NLab(dirs, g, y);
        for (int e = 0; e < 4; e++) if (g.Nl[nb + e] == l) return y * 4 + e;
        return -1;
    }
}

public sealed unsafe partial class ReverseSolver
{
    public int CellsCount => cells;
    public int BoxCount => n;
    public int[] Dirs => dirs;

    static ulong GoHash(ReadOnlySpan<ulong> boxes, int player)
    {
        ulong h = (ulong)player * 0x9E3779B97F4A7C15UL + 1;
        foreach (ulong b in boxes) { h = (h ^ b) * 0xBF58476D1CE4E5B9UL; h ^= h >> 31; }
        return h;
    }

    SupGraph GraphFor(in Set256 f, int p)
    {
        var sp = sup!;
        var fs = f; // copy for span
        var g = sp.Slots[(int)(GoHash(fs.S, 0) >> 40) & (sp.Slots.Length - 1)];
        if (!g.Allocated || !g.Key.SameAs(f))
        {
            sp.Reset(g);
            g.Key = f;
            sp.Graphs++;
        }
        if (g.Disc[p] == 0) sp.Tree(this, g, p);
        return g;
    }

    /// <summary>Supply count U for boxes cur (ascending) with the player's normalized cell p, and k = boxes on start squares.</summary>
    (int u, int k) SupplyU(ReadOnlySpan<int> cur, int p)
    {
        var sp = sup!;
        Set256 f = default, free = default;
        for (int j = 0; j < n; j++) free.Set(j);
        sp.Off.Clear();
        int k = 0;
        foreach (int b in cur)
        {
            int j = startIdx[b];
            if (j >= 0) { f.Set(j); free.S[j >> 6] &= ~(1UL << (j & 63)); k++; }
            else sp.Off.Add(b);
        }
        if (sp.Off.Count == 0) return (0, k);
        sp.Evals++;
        ulong hf = GoHash(f.S, 0);
        var g = sp.Slots[(int)(hf >> 40) & (sp.Slots.Length - 1)];
        if (!g.Allocated || !g.Key.SameAs(f) || g.Disc[p] == 0)
        {
            Array.Copy(wall, sp.Obst, cells);
            foreach (int b in cur) if (startIdx[b] >= 0) sp.Obst[b] = true;
            g = GraphFor(f, p);
        }
        if (sp.ReachOf.Length < sp.Off.Count) Array.Resize(ref sp.ReachOf, sp.Off.Count * 2);
        sp.Cache ??= new SupEntry[Supply.SupCache];
        for (int oi = 0; oi < sp.Off.Count; oi++)
        {
            int b = sp.Off[oi];
            Set256 r = default;
            if (g.Disc[b] != 0 && g.Top[b] == g.Top[p]) // b in the player's component
            {
                sbyte l = Supply.Lab(dirs, g, b, p);
                ulong fp = (hf ^ (ulong)b << 3 ^ (ulong)l) * 0x9E3779B97F4A7C15UL | 1;
                fp ^= fp >> 29;
                fp |= 1;
                ref var e = ref sp.Cache[(int)(fp & (Supply.SupCache - 1))];
                if (e.Fp == fp) r = e.R;
                else
                {
                    sp.Misses++;
                    r = Closure(g, Supply.Canon(dirs, g, b, l));
                    e.Fp = fp; e.R = r;
                }
            }
            for (int w = 0; w < 4; w++) r.S[w] &= free.S[w];
            sp.ReachOf[oi] = r;
        }
        Array.Fill(sp.MatchOf, -1);
        // Greedy pass (a free square nobody holds yet), then augmenting paths.
        Set256 taken = default;
        sp.Left.Clear();
        for (int i = 0; i < sp.Off.Count; i++)
        {
            var r = sp.ReachOf[i];
            int w = 0;
            while (w < 4 && (r.S[w] & ~taken.S[w]) == 0) w++;
            if (w == 4) { sp.Left.Add(i); continue; }
            int j = w * 64 + BitOperations.TrailingZeroCount(r.S[w] & ~taken.S[w]);
            taken.Set(j);
            sp.MatchOf[j] = i;
        }
        int u = 0;
        foreach (int i in sp.Left)
        {
            if (++sp.Gstamp == 0) { Array.Clear(sp.Gs); sp.Gstamp = 1; }
            if (!Augment(i)) u++;
        }
        return (u, k);
    }

    bool Augment(int i)
    {
        var sp = sup!;
        var r = sp.ReachOf[i];
        for (int w = 0; w < 4; w++)
        {
            ulong x = r.S[w];
            while (x != 0)
            {
                int j = w * 64 + BitOperations.TrailingZeroCount(x);
                x &= x - 1;
                if (sp.Gs[j] == sp.Gstamp) continue;
                sp.Gs[j] = sp.Gstamp;
                if (sp.MatchOf[j] < 0 || Augment(sp.MatchOf[j])) { sp.MatchOf[j] = i; return true; }
            }
        }
        return false;
    }

    /// <summary>State (box on y, player in y's piece l) enqueued once per piece.</summary>
    void SupVisit(SupGraph g, int y, int l, int from, uint st)
    {
        var sp = sup!;
        int nb = Supply.NLab(dirs, g, y);
        int e0 = -1;
        for (int e = 0; e < 4; e++)
        {
            if (g.Nl[nb + e] != l) continue;
            if (sp.Seen[y * 4 + e] == st) return;
            sp.Seen[y * 4 + e] = st;
            if (e0 < 0) e0 = e;
        }
        if (e0 >= 0) { sp.Prev[y * 4 + e0] = from; sp.Q.Add(y * 4 + e0); }
    }

    /// <summary>Start squares a box on b can be pulled onto, the player in piece l (or, with stop, the first one: leaves BFS parents in Prev and the state in Target).</summary>
    Set256 PullReach(SupGraph g, int b, int l, bool stop)
    {
        var sp = sup!;
        Set256 r = default;
        if (++sp.Stamp == 0) { Array.Clear(sp.Seen); sp.Stamp = 1; }
        uint st = sp.Stamp;
        sp.Q.Clear();
        sp.Target = -1;
        if (stop)
        {
            for (int e = 0; e < 4; e++)
            {
                int d = dirs[e], w = b + d, w2 = b + 2 * d;
                if (g.Disc[w] != 0 && g.Top[w] == l && g.Disc[w2] != 0 && dist[w] >= 0)
                    SupVisit(g, w, g.Nl[Supply.NLab(dirs, g, w) + e], -1, st);
            }
        }
        else SupVisit(g, b, (sbyte)l, -1, st);
        var q = sp.Q;
        for (int i = 0; i < q.Count; i++)
        {
            int y = q[i] >> 2;
            int nb = Supply.NLab(dirs, g, y);
            int l2 = g.Nl[nb + (q[i] & 3)];
            int j = startIdx[y];
            if (j >= 0 && y != b)
            {
                r.Set(j);
                if (stop) { sp.Target = q[i]; break; }
            }
            for (int e = 0; e < 4; e++)
            {
                if (g.Nl[nb + e] != l2) continue;
                int d = dirs[e], w = y + d, w2 = y + 2 * d;
                if (g.Disc[w2] == 0 || dist[w] < 0) continue;
                SupVisit(g, w, g.Nl[Supply.NLab(dirs, g, w) + e], q[i], st);
            }
        }
        return r;
    }

    /// <summary>Fewest-pull path of box b to a free start square in the real state's graph; returns (from cell, direction) pairs in order, or empty.</summary>
    void MacroPath(SupGraph g, int b, int p, List<int> outp)
    {
        var sp = sup!;
        outp.Clear();
        PullReach(g, b, g.Top[p], true);
        if (sp.Target < 0) return;
        for (int x = sp.Target; x >= 0; x = sp.Prev[x])
        {
            int y = x >> 2, from = b;
            int px = sp.Prev[x];
            if (px >= 0) from = px >> 2;
            for (int di = 0; di < 4; di++)
                if (from + dirs[di] == y) { outp.Add(from); outp.Add(di); }
        }
        for (int i = 0, j = outp.Count - 2; i < j; i += 2, j -= 2)
        {
            (outp[i], outp[j]) = (outp[j], outp[i]);
            (outp[i + 1], outp[j + 1]) = (outp[j + 1], outp[i + 1]);
        }
    }

    int Succ(SupGraph g, int v, int k)
    {
        int y = v >> 2;
        int nb = Supply.NLab(dirs, g, y);
        if (g.Nl[nb + k] != g.Nl[nb + (v & 3)]) return -1;
        int d = dirs[k];
        int w = y + d, w2 = y + 2 * d;
        if (g.Disc[w2] == 0 || dist[w] < 0) return -1;
        return Supply.Canon(dirs, g, w, g.Nl[Supply.NLab(dirs, g, w) + k]);
    }

    /// <summary>Start squares reachable from pull state root in g (Tarjan SCCs; kept in the slot for reuse).</summary>
    Set256 Closure(SupGraph g, int root)
    {
        if (root < 0) return default;
        if (g.SccStamp[root] == (int)g.Epoch) return g.SccR[g.SccOf[root]];
        var sp = sup!;
        if (++sp.Tst == 0) { Array.Clear(sp.TStamp); sp.Tst = 1; }
        uint ts = sp.Tst; int ep = (int)g.Epoch;
        int counter = 0;
        var stk = sp.TStk; var fr = sp.TFr;
        stk.Clear(); fr.Clear();
        void Visit(int v)
        {
            sp.TStamp[v] = ts; sp.TIdx[v] = counter; sp.TLow[v] = counter;
            counter++;
            stk.Add(v); fr.Add(v); fr.Add(0);
        }
        Visit(root);
        while (fr.Count > 0)
        {
            int v = fr[^2], k = fr[^1];
            if (k < 4)
            {
                fr[^1]++;
                int w = Succ(g, v, k);
                if (w < 0 || g.SccStamp[w] == ep) { }
                else if (sp.TStamp[w] != ts) Visit(w);
                else sp.TLow[v] = Math.Min(sp.TLow[v], sp.TIdx[w]);
                continue;
            }
            fr.RemoveRange(fr.Count - 2, 2);
            if (fr.Count > 0)
            {
                int u = fr[^2];
                sp.TLow[u] = Math.Min(sp.TLow[u], sp.TLow[v]);
            }
            if (sp.TLow[v] != sp.TIdx[v]) continue;
            int i = stk.Count - 1;
            while (stk[i] != v) i--;
            int sid = g.SccR.Count;
            for (int x = i; x < stk.Count; x++) { g.SccOf[stk[x]] = sid; g.SccStamp[stk[x]] = ep; }
            Set256 r = default;
            for (int x = i; x < stk.Count; x++)
            {
                int m = stk[x];
                int j = startIdx[m >> 2];
                if (j >= 0) r.Set(j);
                for (int kk = 0; kk < 4; kk++)
                {
                    int w = Succ(g, m, kk);
                    if (w >= 0 && g.SccOf[w] != sid)
                    {
                        var xr = g.SccR[g.SccOf[w]];
                        for (int a = 0; a < 4; a++) r.S[a] |= xr.S[a];
                    }
                }
            }
            g.SccR.Add(r);
            stk.RemoveRange(i, stk.Count - i);
        }
        return g.SccR[g.SccOf[root]];
    }

    /// <summary>Adds, for an expanded node id with U = 0, one child per off-start box: pulled along a fewest-pull path to the nearest free start square.</summary>
    void PlacementMacros(int id, int[] cur, int[] child)
    {
        var sp = sup!;
        int p = store.PlayerOf(id);
        var g = sp.Scratch;
        Array.Copy(wall, sp.Obst, cells);
        foreach (int b in cur) sp.Obst[b] = true;
        sp.Reset(g);
        sp.Dfs(this, g, p);
        var path = sp.PathBuf;
        foreach (int b in cur)
        {
            if (startIdx[b] >= 0) continue;
            MacroPath(g, b, p, path);
            if (path.Count < 4) continue; // none, or a single pull: an ordinary child
            sp.Macros++;
            int lf = path[^2], ld = path[^1];
            int t = lf + dirs[ld];
            int j = 0;
            foreach (int x in cur) if (x != b) child[j++] = x;
            child[j] = t;
            Array.Sort(child);
            SetOcc(b, false); SetOcc(t, true);
            int np = Reach(t + dirs[ld], mark2P);
            for (int w = 0; w < store.Nw; w++) key[w] = pkey[w];
            int fb = fidx[b], ft = fidx[t];
            key[fb >> 6] &= ~(1UL << (fb & 63));
            key[ft >> 6] |= 1UL << (ft & 63);
            ulong zh = store.HashOfBoxes(child, np);
            if (store.Find(key, np, zh, out _, out _) >= 0)
            {
                SetOcc(b, true); SetOcc(t, false);
                continue;
            }
            int h = 0;
            foreach (int x in child) h += key8[x];
            int parent = id;
            for (int k = 0; k + 2 < path.Count; k += 2)
            {
                int from = path[k], di = path[k + 1];
                int to = from + dirs[di];
                for (int w = 0; w < store.Nw; w++) key[w] = pkey[w];
                key[fb >> 6] &= ~(1UL << (fb & 63));
                int ff = fidx[to];
                key[ff >> 6] |= 1UL << (ff & 63);
                parent = store.Append(parent, to + dirs[di], key, fidx[from], di, -1, 0);
            }
            for (int w = 0; w < store.Nw; w++) key[w] = pkey[w];
            key[fb >> 6] &= ~(1UL << (fb & 63));
            key[ft >> 6] |= 1UL << (ft & 63);
            if (AddKeyed(child, np, parent, lf, ld, h, zh) >= 0) sp.MacroOK++;
            SetOcc(b, true); SetOcc(t, false);
        }
    }
}
