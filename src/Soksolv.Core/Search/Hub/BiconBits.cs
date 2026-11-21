using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search.Hub;

/// <summary>
/// Word-parallel "free cells on no cycle" count (Festival biconnect_score, <see cref="PfFest.Bicon"/>) for boards at most 64 columns wide and
/// 62 rows tall whose outer ring is wall. Planar duality: an edge of the free-cell grid graph is a bridge iff both sides lie in the same face,
/// and the faces other than the unit squares of four free cells are exactly the 8-connected components of the blocked cells (walls + blocked
/// cells); a free cell is on a cycle iff it has a non-bridge edge. Per call: bit-row 8-connected component flood of the blocked cells,
/// per component its "dual square" rows (a unit square touches the component if one of its four corner cells belongs to it), and the
/// horizontal / vertical edges whose two squares both belong to one component are the bridges. Single blocked cells cannot bridge and
/// are dropped in one word-parallel pass. Equal to the DFS (checked with <c>SOKSOLV_CHECK_BIC=1</c>).
/// Row arrays are indexed y+1 (index 0 and h+1 are padding).
/// </summary>
public sealed unsafe class BiconBits
{
    readonly int w, h, cells, rows;
    readonly ulong mask;
    readonly ulong[] wallR, goalR;
    readonly PfFest? dfs;
    public static readonly bool Check = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_BIC") == "1";

    public int Rows => rows;
    public static bool Supported(bool[] wall, int w)
    {
        if (w > 64 || w < 3 || wall.Length % w != 0) return false;
        int h = wall.Length / w;
        if (h > 62) return false;
        for (int x = 0; x < w; x++) if (!wall[x] || !wall[(h - 1) * w + x]) return false;
        for (int y = 0; y < h; y++) if (!wall[y * w] || !wall[y * w + w - 1]) return false;
        return true;
    }

    public BiconBits(bool[] wall, int w, bool[] isGoal, PfFest? dfs = null)
    {
        this.w = w; cells = wall.Length; h = cells / w; rows = h + 2; this.dfs = dfs;
        mask = w == 64 ? ~0UL : (1UL << w) - 1;
        wallR = new ulong[rows]; goalR = new ulong[rows];
        for (int c = 0; c < cells; c++)
        {
            int y = c / w, x = c % w;
            if (wall[c]) wallR[y + 1] |= 1UL << x;
            if (isGoal[c]) goalR[y + 1] |= 1UL << x;
        }
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong Fill(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }

    /// <summary>
    /// Free cells on no cycle of the graph whose vertices are the non-wall cells not in <paramref name="blocked"/> (distinct, non-wall),
    /// without free real goals. <paramref name="region"/> (rows indexed y+1, may be null) restricts the counted cells.
    /// </summary>
    public int Count(ReadOnlySpan<int> blocked, ulong[]? region = null)
    {
        int r2 = rows, hh = h, w0 = w;
        ulong m = mask;
        ulong* rem = stackalloc ulong[r2], reg = stackalloc ulong[r2], q = stackalloc ulong[r2], bh = stackalloc ulong[r2], bv = stackalloc ulong[r2], fre = stackalloc ulong[r2];
        for (int i = 0; i < r2; i++) { rem[i] = wallR[i]; reg[i] = 0; q[i] = 0; bh[i] = 0; bv[i] = 0; }
        foreach (int c in blocked) rem[c / w0 + 1] |= 1UL << (c % w0);
        fre[0] = 0; fre[r2 - 1] = 0;
        for (int i = 1; i <= hh; i++) fre[i] = ~rem[i] & m;
        ulong valid = ((1UL << hh) - 1) << 1;           // row bits 1..h
        bool first = true;
        int start = 1;
        while (true)
        {
            if (!first)
            {
                // drop single blocked cells (no 8-neighbour in the remaining set): they cannot bridge
                for (int i = start; i <= hh; i++)
                {
                    ulong r = rem[i];
                    if (r == 0) continue;
                    ulong up = rem[i - 1] | rem[i + 1];
                    ulong nb = up | up << 1 | up >> 1 | r << 1 | r >> 1;
                    rem[i] = r & nb;
                }
            }
            int i0 = start;
            while (i0 <= hh && rem[i0] == 0) i0++;
            if (i0 > hh) break;
            start = i0;
            ulong seed = rem[i0] & (0UL - rem[i0]);
            reg[i0] = Fill(seed, rem[i0]);
            ulong dirty = (1UL << (i0 - 1) | 1UL << (i0 + 1)) & valid;
            int lo = i0, hi = i0;
            while (dirty != 0)
            {
                int r = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
                ulong cand = reg[r - 1] | reg[r] | reg[r + 1];
                ulong y = (cand | cand << 1 | cand >> 1) & rem[r];
                if ((y & ~reg[r]) == 0) continue;
                y = Fill(y, rem[r]);
                reg[r] = y;
                if (r < lo) lo = r; if (r > hi) hi = r;
                dirty |= (1UL << (r - 1) | 1UL << (r + 1)) & valid;
            }
            // dual squares of the component, then its bridges
            for (int i = lo - 1; i <= hi; i++) { ulong c = reg[i] | reg[i + 1]; q[i] = c | c >> 1; }
            for (int i = lo; i <= hi + 1; i++) bh[i] |= q[i - 1] & q[i];
            for (int i = lo - 1; i <= hi; i++) { bv[i] |= q[i] << 1 & q[i]; q[i] = 0; }
            for (int i = lo; i <= hi; i++) { rem[i] &= ~reg[i]; reg[i] = 0; }
            first = false;
        }
        int cnt = 0;
        ulong vprev = 0;
        fixed (ulong* gr = goalR)
        {
            for (int i = 1; i <= hh; i++)
            {
                ulong fr = fre[i], fnext = fre[i + 1];
                ulong nbh = fr & fr >> 1 & ~bh[i];
                ulong nbv = fr & fnext & ~bv[i];
                ulong on = nbh | nbh << 1 | nbv | vprev;
                vprev = nbv;
                ulong cs = fr & ~on & ~gr[i];
                if (region != null) cs &= region[i];
                cnt += BitOperations.PopCount(cs);
            }
        }
        if (Check && dfs != null && region == null)
        {
            var occ = new bool[cells];
            foreach (int c in blocked) occ[c] = true;
            int e = dfs.Bicon(occ);
            if (e != cnt) throw new InvalidOperationException($"BiconBits {cnt} != dfs {e}");
        }
        return cnt;
    }
}
