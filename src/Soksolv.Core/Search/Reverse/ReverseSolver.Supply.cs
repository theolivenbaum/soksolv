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
}

/// <summary>
/// DFS forest of the free cells for one obstacle set (Tarjan lowpoints), in compact floor indices, one unmanaged block per slot.
/// Besides the forest it stores, per box-capable cell, the canonical piece id of each neighbour in the graph without that cell
/// (Cn: four nibbles, 15 = neighbour not in the graph) and the SCC memo of the pull-state graph (see Go supGraph).
/// </summary>
public sealed unsafe class SupSlot : IDisposable
{
    public ulong H, Lru, K0, K1, K2, K3;
    public int N;
    public int SccCount, SccCap;
    public ulong* SccR;
    public ushort* Disc, End, Par, Top, Cn, SccOf;
    public byte* Pc;
    byte* block;
    public long Bytes;

    public SupSlot(int f1)
    {
        long sz = 5L * 2 * f1 + ((f1 + 1) & ~1) + 8L * f1;
        block = (byte*)NativeMemory.AlignedAlloc((nuint)sz, 16);
        Disc = (ushort*)block; End = Disc + f1; Par = End + f1; Top = Par + f1; Cn = Top + f1;
        Pc = (byte*)(Cn + f1);
        SccOf = (ushort*)(Pc + ((f1 + 1) & ~1));
        Bytes = sz;
    }

    public void EnsureScc(int nwS, int f1)
    {
        if (SccCount < SccCap) return;
        int nc = SccCap == 0 ? Math.Max(8, f1 / 4) : SccCap * 2;
        SccR = (ulong*)NativeMemory.Realloc(SccR, (nuint)nc * (nuint)nwS * 8);
        Bytes += (long)(nc - SccCap) * nwS * 8;
        SccCap = nc;
    }

    public void Dispose() { NativeMemory.AlignedFree(block); NativeMemory.Free(SccR); block = null; SccR = null; }
}

/// <summary>Supply-aware ordering state (port of Go supply struct, exp-707), compact reimplementation (same results).</summary>
public sealed unsafe class Supply : IDisposable
{
    public readonly int W; // key penalty per unit of U (x8)
    public readonly int Cells, F, F1, NwS, Nbox;
    public readonly int[] CellOf, Fi;
    public readonly int* FiP;          // cell -> floor index (-1 wall)
    public readonly int* Nb;          // (F1) x 4 neighbour floor indices (walls and the sentinel row -> F)
    public readonly byte* BoxOk;      // dist >= 0
    public readonly short* Sidx;      // start index or -1
    public readonly int[] Dirs;
    // slot cache
    SupSlot?[] tab; readonly int tabMask; int slotCount; readonly int maxSlots; ulong clock;
    public readonly SupSlot Scratch;
    // dfs scratch
    readonly int* low, stk; readonly byte* it, chm; readonly uint* lbl; uint lstamp;
    // tarjan scratch
    readonly int* tIdx, tLow, tStk, fv; readonly uint* tStamp; readonly byte* fk; uint tst; readonly ulong* pr;
    // pull BFS scratch (macros)
    public readonly uint* Seen; public readonly int* Prev, Q; uint stamp; public int Target;
    // matching scratch
    public Set256[] ReachOf = new Set256[16];
    public int[] OffFi = new int[16], Left = new int[16];
    public readonly int[] MatchOf; public readonly uint[] Gs; public uint Gstamp;
    public List<int> PathBuf = new();
    public long Evals, Deferred, Graphs, Macros, MacroOK, Hits;
    public bool Ok = true;
    long fixedBytes, slotBytesTotal;
    public const int SlotBudget = 24 << 20;

    public Supply(int w, int cells, int n, bool[] wall, int[] dirs, int[] dist, int[] startIdx)
    {
        W = w; Cells = cells; Nbox = n; Dirs = dirs; NwS = (n + 63) / 64;
        Fi = new int[cells]; var co = new List<int>();
        for (int c = 0; c < cells; c++) { Fi[c] = -1; if (!wall[c]) { Fi[c] = co.Count; co.Add(c); } }
        F = co.Count; F1 = F + 1; CellOf = [.. co];
        if (F > 12000) { Ok = false; F1 = 1; F = 0; CellOf = []; }
        int f1 = F1;
        FiP = (int*)NativeMemory.Alloc((nuint)cells * 4);
        for (int c = 0; c < cells; c++) FiP[c] = Fi[c];
        Nb = (int*)NativeMemory.AllocZeroed((nuint)(f1 * 4 * 4));
        BoxOk = (byte*)NativeMemory.AllocZeroed((nuint)f1 + 8);
        Sidx = (short*)NativeMemory.AllocZeroed((nuint)f1 * 2 + 8);
        if (Ok)
        {
            for (int i = 0; i < F; i++)
            {
                int c = CellOf[i];
                for (int k = 0; k < 4; k++) { int d = c + dirs[k]; Nb[i * 4 + k] = wall[d] ? F : Fi[d]; }
                BoxOk[i] = (byte)(dist[c] >= 0 ? 1 : 0); Sidx[i] = (short)startIdx[c];
            }
            for (int k = 0; k < 4; k++) Nb[F * 4 + k] = F;
            Sidx[F] = -1;
        }
        int slotBytes = f1 * 18 + 256;
        maxSlots = Math.Clamp(SlotBudget / slotBytes, 16, 1 << 16);
        int tn = 64; while (tn < maxSlots) tn *= 2;
        tab = new SupSlot?[tn]; tabMask = tn - 1;
        Scratch = new SupSlot(f1);
        low = (int*)NativeMemory.Alloc((nuint)f1 * 4); stk = (int*)NativeMemory.Alloc((nuint)f1 * 4);
        it = (byte*)NativeMemory.Alloc((nuint)f1); chm = (byte*)NativeMemory.Alloc((nuint)f1); lbl = (uint*)NativeMemory.AllocZeroed((nuint)f1 * 4);
        int ns = 4 * f1 + 4;
        tIdx = (int*)NativeMemory.Alloc((nuint)ns * 4); tLow = (int*)NativeMemory.Alloc((nuint)ns * 4); tStk = (int*)NativeMemory.Alloc((nuint)ns * 4);
        fv = (int*)NativeMemory.Alloc((nuint)ns * 4); fk = (byte*)NativeMemory.Alloc((nuint)ns);
        tStamp = (uint*)NativeMemory.AllocZeroed((nuint)ns * 4); pr = (ulong*)NativeMemory.Alloc((nuint)ns * (nuint)NwS * 8);
        Seen = (uint*)NativeMemory.AllocZeroed((nuint)ns * 4); Prev = (int*)NativeMemory.Alloc((nuint)ns * 4); Q = (int*)NativeMemory.Alloc((nuint)ns * 4);
        MatchOf = new int[n]; Gs = new uint[n];
        fixedBytes = (long)f1 * (16 + 3 + 1 + 1 + 4 + 4 + 4 + 4 + 4 + 4 + 2 + 2) * 1 + Scratch.Bytes + (long)ns * 28;
    }

    public long Bytes()
    {
        return fixedBytes + (long)tab.Length * 8 + slotBytesTotal + (long)slotCount * 80;
    }

    public void Dispose()
    {
        foreach (var s in tab) s?.Dispose();
        Scratch.Dispose();
        NativeMemory.Free(Nb); NativeMemory.Free(FiP); NativeMemory.Free(BoxOk); NativeMemory.Free(Sidx); NativeMemory.Free(low); NativeMemory.Free(stk); NativeMemory.Free(it); NativeMemory.Free(chm); NativeMemory.Free(lbl);
        NativeMemory.Free(tIdx); NativeMemory.Free(tLow); NativeMemory.Free(tStk); NativeMemory.Free(fv); NativeMemory.Free(fk); NativeMemory.Free(tStamp); NativeMemory.Free(pr);
        NativeMemory.Free(Seen); NativeMemory.Free(Prev); NativeMemory.Free(Q);
        Array.Clear(tab);
    }

    /// <summary>Clears a slot for a new obstacle set: obstacles (floor indices) and the sentinel get disc 0xFFFF.</summary>
    public void Reset(SupSlot g, ReadOnlySpan<int> obstacles)
    {
        new Span<ushort>(g.Disc, F1).Clear();
        g.Disc[F] = 0xFFFF;
        foreach (int o in obstacles) g.Disc[o] = 0xFFFF;
        new Span<ushort>(g.SccOf, 4 * F1).Clear();
        g.N = 0; g.SccCount = 0;
    }

    /// <summary>Slot for key words k (hash h): a hit, or a recycled / new slot (fresh = true) to be Reset by the caller.</summary>
    public SupSlot Acquire(ulong h, ulong k0, ulong k1, ulong k2, ulong k3, out bool fresh)
    {
        int b0 = (int)(h >> 24) & tabMask & ~3;
        SupSlot? victim = null; int empty = -1; ulong best = ulong.MaxValue;
        for (int w = 0; w < 4; w++)
        {
            var s = tab[b0 + w];
            if (s == null) { if (empty < 0) empty = b0 + w; continue; }
            if (s.H == h && s.K0 == k0 && s.K1 == k1 && s.K2 == k2 && s.K3 == k3) { s.Lru = ++clock; fresh = false; Hits++; return s; }
            if (s.Lru < best) { best = s.Lru; victim = s; }
        }
        fresh = true;
        SupSlot r;
        if (empty >= 0 && (slotCount < maxSlots || victim == null)) { r = new SupSlot(F1); tab[empty] = r; slotCount++; slotBytesTotal += r.Bytes; }
        else r = victim!;
        r.H = h; r.K0 = k0; r.K1 = k1; r.K2 = k2; r.K3 = k3; r.Lru = ++clock;
        Graphs++;
        return r;
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static bool InGraph(ushort d) => (uint)(d - 1) < 0xFFFE;

    /// <summary>Piece codes of v's neighbours in the graph without v (needs low/chm of the build just done): fills Cn[v], Pc[v] and the Pc low nibble of v's tree children.</summary>
    void Label(SupSlot g, int v)
    {
        ushort* disc = g.Disc, end = g.End, par = g.Par, cn = g.Cn; byte* pc = g.Pc;
        int* nv = Nb + v * 4;
        int pv = par[v];
        int dv = disc[v]; int cm = chm[v];
        bool art;
        if (pv == 0xFFFF) art = (cm & (cm - 1)) != 0;
        else
        {
            art = false;
            for (int m = cm; m != 0; m &= m - 1) if (low[nv[BitOperations.TrailingZeroCount(m)]] >= dv) { art = true; break; }
        }
        if (!art)
        {
            // v is no cut vertex: all its neighbours in the graph form one piece
            int pm = 0;
            for (int kk = 0; kk < 4; kk++) if (disc[nv[kk]] != 0xFFFF) pm |= 1 << kk;
            int e0 = pm == 0 ? 7 : BitOperations.TrailingZeroCount(pm);
            uint cw0 = 0xFFFF;
            for (int m = pm; m != 0; m &= m - 1) { int kk = BitOperations.TrailingZeroCount(m); cw0 = (cw0 & ~(15u << (4 * kk))) | ((uint)e0 << (4 * kk)); }
            cn[v] = (ushort)cw0;
            pc[v] = (byte)(e0 << 4);
            for (int m = cm; m != 0; m &= m - 1) { int c = nv[BitOperations.TrailingZeroCount(m)]; pc[c] = (byte)((pc[c] & 0xF0) | e0); }
            return;
        }
        int* lab = stackalloc int[8]; int* can = lab + 4;
        // piece code of each neighbour: -1 absent, 0 parent side, k+1 separate child subtree in direction k
        for (int kk = 0; kk < 4; kk++)
        {
            int dw = disc[nv[kk]];
            if (dw == 0xFFFF) { lab[kk] = -1; continue; }
            if (dw < dv) { lab[kk] = 0; continue; }
            int code = 0;
            for (int c4 = 0; c4 < 4; c4++)
            {
                if ((cm >> c4 & 1) == 0) continue;
                int c = nv[c4];
                if (dw >= disc[c] && dw < end[c]) { code = (pv != 0xFFFF && low[c] < dv) ? 0 : c4 + 1; break; }
            }
            lab[kk] = code;
        }
        uint cw = 0; int up = 7;
        for (int kk = 0; kk < 4; kk++)
        {
            int l = lab[kk];
            if (l < 0) { can[kk] = 15; cw |= 15u << (4 * kk); continue; }
            int e = kk; for (int j = 0; j < kk; j++) if (lab[j] == l) { e = j; break; }
            can[kk] = e; cw |= (uint)e << (4 * kk);
            if (l == 0 && up == 7) up = e;
        }
        cn[v] = (ushort)cw;
        pc[v] = (byte)(up << 4);
        for (int c4 = 0; c4 < 4; c4++)
            if ((cm >> c4 & 1) != 0) { int c = nv[c4]; pc[c] = (byte)((pc[c] & 0xF0) | can[c4]); }
    }

    /// <summary>Cn of v for a lazily labelled (scratch) graph.</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    int CnLazy(SupSlot g, int v)
    {
        if (lbl[v] != lstamp) { lbl[v] = lstamp; Label(g, v); }
        return g.Cn[v];
    }

    /// <summary>Extends g's forest with the component of floor index root and labels every box-capable cell of it.</summary>
    public void Build(SupSlot g, int root, bool eager = true)
    {
        ushort* disc = g.Disc, end = g.End, par = g.Par, top = g.Top;
        int* nb = Nb; byte* boxOk = BoxOk;
        if (!eager && ++lstamp == 0) { new Span<uint>(lbl, F1).Clear(); lstamp = 1; }
        int gn = g.N + 1;
        int v = root, pv = 0xFFFF, k = 0, lv = gn;
        disc[v] = (ushort)gn; par[v] = 0xFFFF; top[v] = (ushort)root; chm[v] = 0;
        int* nv = nb + v * 4;
        while (true)
        {
            int w = -1;
            for (; k < 4; k++)
            {
                int x = nv[k]; int dx = disc[x];
                if (dx == 0) { w = x; break; }
                if (dx < lv && x != pv) lv = dx;
            }
            if (w >= 0)
            {
                it[v] = (byte)(k + 1); low[v] = lv; chm[v] |= (byte)(1 << k);
                gn++;
                disc[w] = (ushort)gn; par[w] = (ushort)v; top[w] = (ushort)root; chm[w] = 0;
                pv = v; v = w; k = 0; lv = gn; nv = nb + v * 4;
                continue;
            }
            low[v] = lv; end[v] = (ushort)(gn + 1);
            if (eager && boxOk[v] != 0) Label(g, v);
            if (pv == 0xFFFF) break;
            int u = pv; int lu = low[u]; if (lv < lu) lu = lv;
            v = u; lv = lu; k = it[u]; pv = par[u]; nv = nb + v * 4;
        }
        g.N = gn;
    }

    /// <summary>Canonical state (box b, player p's piece of the graph without b) as b*4+e; b and p must lie in one built component.</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static int StateOf(SupSlot g, int* nb, int b, int p)
    {
        int db = g.Disc[b], dp = g.Disc[p];
        if (dp < db || dp >= g.End[b]) return b * 4 + (g.Pc[b] >> 4);
        for (int k = 0; k < 4; k++)
        {
            int c = nb[b * 4 + k]; int dc = g.Disc[c];
            if (InGraph((ushort)dc) && g.Par[c] == b && dp >= dc && dp < g.End[c]) return b * 4 + (g.Pc[c] & 15);
        }
        return -1;
    }

    /// <summary>Next pull state of v in direction k (the box moves one cell, the player follows) or -1.</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    int Succ(SupSlot g, int v, int k)
    {
        int y = v >> 2;
        if (((g.Cn[y] >> (4 * k)) & 15) != (v & 3)) return -1;
        int w = Nb[y * 4 + k];
        if (BoxOk[w] == 0) return -1;
        int c = (g.Cn[w] >> (4 * k)) & 15;
        if (c == 15) return -1;
        return w * 4 + c;
    }

    int SuccLazy(SupSlot g, int v, int k)
    {
        int y = v >> 2;
        if (((CnLazy(g, y) >> (4 * k)) & 15) != (v & 3)) return -1;
        int w = Nb[y * 4 + k];
        if (BoxOk[w] == 0) return -1;
        int c = (CnLazy(g, w) >> (4 * k)) & 15;
        if (c == 15) return -1;
        return w * 4 + c;
    }

    /// <summary>Start squares reachable from pull state root (Tarjan SCCs; kept in the slot for reuse). Returns a pointer to NwS words.</summary>
    public ulong* Closure(SupSlot g, int root)
    {
        int sc = g.SccOf[root];
        if (sc != 0) return g.SccR + (long)(sc - 1) * NwS;
        if (++tst == 0) { new Span<uint>(tStamp, 4 * F1 + 4).Clear(); tst = 1; }
        uint ts = tst;
        int counter = 0, sn = 0, fn = 0;
        int nws = NwS;
        ulong* pr = this.pr;
        VisitState(root, ts, ref counter, pr, nws);
        tStk[sn++] = root; fv[fn] = root; fk[fn++] = 0;
        while (fn > 0)
        {
            int v = fv[fn - 1]; int k = fk[fn - 1];
            if (k < 4)
            {
                fk[fn - 1] = (byte)(k + 1);
                int w = Succ(g, v, k);
                if (w < 0) { }
                else if (g.SccOf[w] != 0)
                {
                    ulong* src = g.SccR + (long)(g.SccOf[w] - 1) * nws; ulong* dst = pr + (long)v * nws;
                    for (int a = 0; a < nws; a++) dst[a] |= src[a];
                }
                else if (tStamp[w] != ts)
                {
                    VisitState(w, ts, ref counter, pr, nws);
                    tStk[sn++] = w; fv[fn] = w; fk[fn++] = 0;
                }
                else if (tIdx[w] < tLow[v]) tLow[v] = tIdx[w];
                continue;
            }
            fn--;
            int u = fn > 0 ? fv[fn - 1] : -1;
            if (u >= 0 && tLow[v] < tLow[u]) tLow[u] = tLow[v];
            if (tLow[v] == tIdx[v])
            {
                int i = sn - 1;
                while (tStk[i] != v) i--;
                if (g.SccCount >= g.SccCap) { long before = g.Bytes; g.EnsureScc(nws, F1); slotBytesTotal += g.Bytes - before; }
                int sid = g.SccCount++;
                ulong* r = g.SccR + (long)sid * nws;
                for (int a = 0; a < nws; a++) r[a] = 0;
                ushort tag = (ushort)(sid + 1);
                for (int x = i; x < sn; x++)
                {
                    int m = tStk[x];
                    g.SccOf[m] = tag;
                    ulong* pm = pr + (long)m * nws;
                    for (int a = 0; a < nws; a++) r[a] |= pm[a];
                }
                sn = i;
                if (u >= 0)
                {
                    ulong* dst = pr + (long)u * nws;
                    for (int a = 0; a < nws; a++) dst[a] |= r[a];
                }
            }
        }
        return g.SccR + (long)(g.SccOf[root] - 1) * NwS;
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    void VisitState(int w, uint ts, ref int counter, ulong* pr, int nws)
    {
        tStamp[w] = ts; tIdx[w] = counter; tLow[w] = counter; counter++;
        ulong* d = pr + (long)w * nws;
        for (int a = 0; a < nws; a++) d[a] = 0;
        int j = Sidx[w >> 2];
        if (j >= 0) d[j >> 6] |= 1UL << (j & 63);
    }

    // ---- placement-macro BFS over pull states ----
    void Visit(SupSlot g, int state, int from, uint st, ref int qn)
    {
        if (Seen[state] == st) return;
        Seen[state] = st; Prev[state] = from; Q[qn++] = state;
    }

    /// <summary>First start square (and its state in Target) a box pulled off cell bf can reach in g, player in g's component of pf; false if none.</summary>
    public bool PullFirst(SupSlot g, int bf, int pf)
    {
        if (++stamp == 0) { new Span<uint>(Seen, 4 * F1 + 4).Clear(); stamp = 1; }
        uint st = stamp; int qn = 0; Target = -1;
        int l = g.Top[pf];
        for (int e = 0; e < 4; e++)
        {
            int w = Nb[bf * 4 + e];
            if (!InGraph(g.Disc[w]) || g.Top[w] != l || BoxOk[w] == 0) continue;
            int c = (CnLazy(g, w) >> (4 * e)) & 15;
            if (c == 15) continue;
            Visit(g, w * 4 + c, -1, st, ref qn);
        }
        for (int i = 0; i < qn; i++)
        {
            int s = Q[i];
            int y = s >> 2;
            if (Sidx[y] >= 0 && y != bf) { Target = s; return true; }
            for (int k = 0; k < 4; k++)
            {
                int w = SuccLazy(g, s, k);
                if (w >= 0) Visit(g, w, s, st, ref qn);
            }
        }
        return false;
    }
}

public sealed unsafe partial class ReverseSolver
{
    public int CellsCount => cells;
    public int BoxCount => n;
    public int[] Dirs => dirs;

    static ulong GoHash(ulong k0, ulong k1, ulong k2, ulong k3)
    {
        ulong h = 1;
        h = (h ^ k0) * 0xBF58476D1CE4E5B9UL; h ^= h >> 31;
        h = (h ^ k1) * 0xBF58476D1CE4E5B9UL; h ^= h >> 31;
        h = (h ^ k2) * 0xBF58476D1CE4E5B9UL; h ^= h >> 31;
        h = (h ^ k3) * 0xBF58476D1CE4E5B9UL; h ^= h >> 31;
        return h;
    }

    /// <summary>SupplyU for n &lt;= 64 boxes: one-word reach sets and matching by bit operations.</summary>
    (int u, int k) SupplyU64(ReadOnlySpan<int> cur, int p)
    {
        var sp = sup!;
        int nc = cur.Length;
        int* obst = stackalloc int[nc]; int* offFi = stackalloc int[nc];
        ulong* rs = stackalloc ulong[nc];
        int* fip = sp.FiP; int* sidx = startIdxP;
        ulong f0 = 0; int no = 0, noff = 0;
        for (int i = 0; i < nc; i++)
        {
            int b = cur[i];
            int j = sidx[b];
            if (j >= 0) { f0 |= 1UL << j; obst[no++] = fip[b]; }
            else offFi[noff++] = fip[b];
        }
        int k = no;
        if (noff == 0) return (0, k);
        sp.Evals++;
        ulong hf = GoHash(f0, 0, 0, 0);
        var g = sp.Acquire(hf, f0, 0, 0, 0, out bool fresh);
        if (fresh) sp.Reset(g, new ReadOnlySpan<int>(obst, no));
        int pf = fip[p];
        if (g.Disc[pf] == 0) sp.Build(g, pf);
        int tp = g.Top[pf];
        ulong free0 = (n >= 64 ? ~0UL : (1UL << n) - 1) & ~f0;
        ushort* disc = g.Disc; ushort* top = g.Top; ushort* sccOf = g.SccOf;
        for (int oi = 0; oi < noff; oi++)
        {
            int b = offFi[oi];
            ulong r = 0;
            if (Supply.InGraph(disc[b]) && top[b] == tp)
            {
                int st = Supply.StateOf(g, sp.Nb, b, pf);
                if (st >= 0)
                {
                    int sc = sccOf[st];
                    r = sc != 0 ? g.SccR[sc - 1] : *sp.Closure(g, st);
                }
            }
            rs[oi] = r & free0;
        }
        // greedy pass (a free square nobody holds yet), then augmenting paths
        ulong taken = 0; int nl = 0;
        int* left = stackalloc int[noff];
        int* matchOf = stackalloc int[64];
        for (int i = 0; i < noff; i++)
        {
            ulong cand = rs[i] & ~taken;
            if (cand == 0) { left[nl++] = i; continue; }
            int j = BitOperations.TrailingZeroCount(cand);
            taken |= 1UL << j;
            matchOf[j] = i;
        }
        int u = 0;
        for (int li = 0; li < nl; li++)
        {
            ulong seen = 0;
            if (!Augment64(left[li], rs, matchOf, ref taken, ref seen)) u++;
        }
        return (u, k);
    }

    static bool Augment64(int i, ulong* rs, int* matchOf, ref ulong taken, ref ulong seen)
    {
        ulong x = rs[i] & ~seen;
        while (x != 0)
        {
            int j = BitOperations.TrailingZeroCount(x);
            seen |= 1UL << j;
            if ((taken >> j & 1) == 0) { taken |= 1UL << j; matchOf[j] = i; return true; }
            if (Augment64(matchOf[j], rs, matchOf, ref taken, ref seen)) { matchOf[j] = i; return true; }
            x = rs[i] & ~seen;
        }
        return false;
    }

    /// <summary>Supply count U for boxes cur (ascending) with the player's normalized cell p, and k = boxes on start squares.</summary>
    (int u, int k) SupplyU(ReadOnlySpan<int> cur, int p)
    {
        var sp = sup!;
        if (sp.NwS == 1) return SupplyU64(cur, p);
        ulong f0 = 0, f1 = 0, f2 = 0, f3 = 0;
        Span<int> obst = stackalloc int[cur.Length];
        int no = 0, noff = 0;
        if (sp.OffFi.Length < cur.Length) { sp.OffFi = new int[cur.Length * 2]; sp.Left = new int[cur.Length * 2]; }
        var offFi = sp.OffFi;
        int k = 0;
        foreach (int b in cur)
        {
            int j = startIdx[b];
            if (j >= 0)
            {
                ulong bit = 1UL << (j & 63);
                switch (j >> 6) { case 0: f0 |= bit; break; case 1: f1 |= bit; break; case 2: f2 |= bit; break; default: f3 |= bit; break; }
                obst[no++] = sp.Fi[b]; k++;
            }
            else offFi[noff++] = sp.Fi[b];
        }
        if (noff == 0) return (0, k);
        sp.Evals++;
        ulong hf = GoHash(f0, f1, f2, f3);
        var g = sp.Acquire(hf, f0, f1, f2, f3, out bool fresh);
        if (fresh) sp.Reset(g, obst[..no]);
        int pf = sp.Fi[p];
        if (g.Disc[pf] == 0) sp.Build(g, pf);
        if (sp.ReachOf.Length < noff) Array.Resize(ref sp.ReachOf, noff * 2);
        int nws = sp.NwS;
        int tp = g.Top[pf];
        ulong free0 = n >= 64 ? ~0UL : (1UL << n) - 1, free1 = 0, free2 = 0, free3 = 0;
        if (n > 64) free1 = n >= 128 ? ~0UL : (1UL << (n - 64)) - 1;
        if (n > 128) free2 = n >= 192 ? ~0UL : (1UL << (n - 128)) - 1;
        if (n > 192) free3 = n >= 256 ? ~0UL : (1UL << (n - 192)) - 1;
        free0 &= ~f0; free1 &= ~f1; free2 &= ~f2; free3 &= ~f3;
        for (int oi = 0; oi < noff; oi++)
        {
            int b = offFi[oi];
            Set256 r = default;
            if (InGraphFast(g.Disc[b]) && g.Top[b] == tp) // b in the player's component
            {
                int st = Supply.StateOf(g, sp.Nb, b, pf);
                if (st >= 0) { ulong* src = sp.Closure(g, st); for (int a = 0; a < nws; a++) r[a] = src[a]; }
            }
            r[0] &= free0; r[1] &= free1; r[2] &= free2; r[3] &= free3;
            sp.ReachOf[oi] = r;
        }
        Array.Fill(sp.MatchOf, -1);
        // Greedy pass (a free square nobody holds yet), then augmenting paths.
        Set256 taken = default;
        int nl = 0;
        for (int i = 0; i < noff; i++)
        {
            ref var r = ref sp.ReachOf[i];
            int w = 0;
            while (w < 4 && (r[w] & ~taken[w]) == 0) w++;
            if (w == 4) { sp.Left[nl++] = i; continue; }
            int j = w * 64 + BitOperations.TrailingZeroCount(r[w] & ~taken[w]);
            taken[j >> 6] |= 1UL << (j & 63);
            sp.MatchOf[j] = i;
        }
        int u = 0;
        for (int li = 0; li < nl; li++)
        {
            if (++sp.Gstamp == 0) { Array.Clear(sp.Gs); sp.Gstamp = 1; }
            if (!Augment(sp.Left[li])) u++;
        }
        return (u, k);
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static bool InGraphFast(ushort d) => (uint)(d - 1) < 0xFFFE;

    bool Augment(int i)
    {
        var sp = sup!;
        var r = sp.ReachOf[i];
        for (int w = 0; w < 4; w++)
        {
            ulong x = r[w];
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

    /// <summary>Fewest-pull path of box b to a free start square in the real state's graph; returns (from cell, direction) pairs in order, or empty.</summary>
    void MacroPath(SupSlot g, int b, int p, List<int> outp)
    {
        var sp = sup!;
        outp.Clear();
        if (!sp.PullFirst(g, sp.Fi[b], sp.Fi[p])) return;
        var cof = sp.CellOf;
        for (int x = sp.Target; x >= 0; x = sp.Prev[x])
        {
            int y = cof[x >> 2], from = b;
            int px = sp.Prev[x];
            if (px >= 0) from = cof[px >> 2];
            for (int di = 0; di < 4; di++)
                if (from + dirs[di] == y) { outp.Add(from); outp.Add(di); }
        }
        for (int i = 0, j = outp.Count - 2; i < j; i += 2, j -= 2)
        {
            (outp[i], outp[j]) = (outp[j], outp[i]);
            (outp[i + 1], outp[j + 1]) = (outp[j + 1], outp[i + 1]);
        }
    }

    /// <summary>Adds, for an expanded node id with U = 0, one child per off-start box: pulled along a fewest-pull path to the nearest free start square.</summary>
    void PlacementMacros(int id, int[] cur, int[] child)
    {
        var sp = sup!;
        int p = store.PlayerOf(id);
        var g = sp.Scratch;
        Span<int> obst = stackalloc int[cur.Length];
        for (int i = 0; i < cur.Length; i++) obst[i] = sp.Fi[cur[i]];
        sp.Reset(g, obst);
        sp.Build(g, sp.Fi[p], false);
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
            int np = lazyNorm ? t + dirs[ld] : Reach(t + dirs[ld], mark2P);
            for (int w = 0; w < store.Nw; w++) key[w] = pkey[w];
            int fb = fidx[b], ft = fidx[t];
            key[fb >> 6] &= ~(1UL << (fb & 63));
            key[ft >> 6] |= 1UL << (ft & 63);
            ulong zh = store.HashOfBoxes(child, lazyNorm ? -1 : np);
            if (FindState(key, np, zh, out _, out _) >= 0)
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
            int kc = 0; foreach (int x in child) if (startIdx[x] >= 0) kc++;
            if (AddKeyed(child, np, parent, lf, ld, h, zh, kc) >= 0) sp.MacroOK++;
            SetOcc(b, true); SetOcc(t, false);
        }
    }
}
