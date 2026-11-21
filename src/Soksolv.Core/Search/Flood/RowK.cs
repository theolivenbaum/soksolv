using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search;

/// <summary>
/// Shared bit-row flood kernels (flood kernels v2, docs/perf.md). Boards are one <c>ulong</c> per grid row (bit = column) with zero padding rows at both ends and at most 64 rows.
/// <list type="bullet">
/// <item><see cref="Fill"/>: in-row fill without a per-cell loop towards the high bits (one carry-propagating add) and a short smear towards the low bits.</item>
/// <item><see cref="Flood"/>: chain-following dirty-row flood: after filling a row the walk continues straight into the neighbouring row that received new seeds (the other one is
/// deferred in a row mask), so a row is visited about once and its neighbours' words stay in registers. Same result as the old alternating-pass worklist kernel
/// (<see cref="FloodOld"/>, kept as the reference for the SOKSOLV_CHECK_FLOOD self-check).</item>
/// </list>
/// </summary>
public static unsafe class RowK
{
    /// <summary>Debug builds (-p:DefineConstants=FLOODCHECK): the old kernels' tolerance for seeds outside the free cells is not reproduced, so assert it never happens.</summary>
    [System.Diagnostics.Conditional("FLOODCHECK")]
    public static void AssertSubset(ulong y, ulong f, string where) { if ((y & ~f) != 0) throw new InvalidOperationException("seed outside the free cells: " + where); }

    public static readonly bool Check = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_FLOOD") == "1";

    /// <summary>All cells of <paramref name="f"/>'s horizontal runs that contain a seed bit of <paramref name="y"/> (y &amp; ~f is ignored). Branch-light: one carry-propagating
    /// add fills towards the high bits; towards the low bits a doubling (Kogge-Stone) smear covers distances up to 7, and the longer steps run only for rows holding a run
    /// of 8+ free cells (predictable per row type). Mispredicted branches, not instructions, dominated the per-cell smear loops this replaces.</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;                      // seeds towards the high bits to the end of their runs (carry chain)
        ulong q = f & (f >> 1);
        ulong d = a | (a >> 1) & f;
        d |= (d >> 2) & q;
        ulong q2 = q & (q >> 2);
        d |= (d >> 4) & q2;
        ulong q4 = q2 & (q2 >> 4);
        if (q4 != 0)
        {
            d |= (d >> 8) & q4;
            ulong q8 = q4 & (q4 >> 8);
            d |= (d >> 16) & q8;
            ulong q16 = q8 & (q8 >> 16);
            d |= (d >> 32) & q16;
        }
        return d;
    }

    /// <summary>Component of the seed bits (cells of f[rp]; may contain several) over the free rows f into reg (rows outside the written span untouched; reg must hold no cells of
    /// this component). lo/hi are widened to cover every row written.</summary>
    public static void Flood(ulong* f, ulong* reg, int rp, ulong seed, ref int lo, ref int hi)
    {
        nint r = rp; ulong s = seed;
        AssertSubset(seed, f[rp], "Flood seed");
        ulong pend = 0, touched = 0;
        while (true)
        {
            touched |= 1UL << (int)r;
            ulong y = Fill(s, f[r]);
            reg[r] |= y;
            ulong su = y & f[r + 1] & ~reg[r + 1], sd = y & f[r - 1] & ~reg[r - 1];
            if (su != 0)
            {
                pend |= (sd != 0 ? 1UL : 0UL) << (int)(r - 1);
                r++; s = su; continue;
            }
            if (sd != 0) { r--; s = sd; continue; }
            if (pend == 0)
            {
                int a = BitOperations.TrailingZeroCount(touched), z = 63 - BitOperations.LeadingZeroCount(touched);
                if (a < lo) lo = a; if (z > hi) hi = z;
                return;
            }
            // a pending row always ends up non-empty (its seeds are free cells next to a written row); if another path already filled them the seed word is just empty
            r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
            s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
        }
    }

    /// <summary><see cref="Flood"/> over the cells f &amp; ~ban (free and not in ban), e.g. the pockets outside a known region.</summary>
    public static void FloodMasked(ulong* f, ulong* ban, ulong* reg, int rp, ulong seed, ref int lo, ref int hi)
    {
        nint r = rp; ulong s = seed;
        AssertSubset(seed, f[rp] & ~ban[rp], "FloodMasked seed");
        ulong pend = 0, touched = 0;
        while (true)
        {
            touched |= 1UL << (int)r;
            ulong y = Fill(s, f[r] & ~ban[r]);
            reg[r] |= y;
            ulong su = y & f[r + 1] & ~ban[r + 1] & ~reg[r + 1], sd = y & f[r - 1] & ~ban[r - 1] & ~reg[r - 1];
            if (su != 0)
            {
                pend |= (sd != 0 ? 1UL : 0UL) << (int)(r - 1);
                r++; s = su; continue;
            }
            if (sd != 0) { r--; s = sd; continue; }
            if (pend == 0)
            {
                int a = BitOperations.TrailingZeroCount(touched), z = 63 - BitOperations.LeadingZeroCount(touched);
                if (a < lo) lo = a; if (z > hi) hi = z;
                return;
            }
            r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
            s = (reg[r - 1] | reg[r + 1]) & f[r] & ~ban[r] & ~reg[r];
        }
    }

    /// <summary>The previous kernel (alternating up/down passes over a dirty-row mask), kept as the self-check reference.</summary>
    public static void FloodOld(ulong* free, ulong* reg, int rows, int rp, ulong seed)
    {
        reg[rp] = FillOld(seed, free[rp]);
        int last = rows - 2;
        ulong valid = ((1UL << last) - 1) << 1;
        ulong cur = ((1UL << (rp - 1)) | (1UL << (rp + 1))) & valid;
        bool up = true;
        while (cur != 0)
        {
            ulong nxt = 0, m = cur;
            if (up)
                while (m != 0)
                {
                    int r = BitOperations.TrailingZeroCount(m);
                    m &= m - 1;
                    ulong cr = reg[r], f = free[r];
                    ulong y = cr | (reg[r - 1] | reg[r + 1]) & f;
                    if (y == cr) continue;
                    y = FillOld(y, f);
                    if (y == cr) continue;
                    reg[r] = y;
                    m |= (1UL << (r + 1)) & valid; nxt |= (1UL << (r - 1)) & valid;
                }
            else
                while (m != 0)
                {
                    int r = 63 - BitOperations.LeadingZeroCount(m);
                    m &= ~(1UL << r);
                    ulong cr = reg[r], f = free[r];
                    ulong y = cr | (reg[r - 1] | reg[r + 1]) & f;
                    if (y == cr) continue;
                    y = FillOld(y, f);
                    if (y == cr) continue;
                    reg[r] = y;
                    m |= (1UL << (r - 1)) & valid; nxt |= (1UL << (r + 1)) & valid;
                }
            cur = nxt; up = !up;
        }
    }

    public static ulong FillOld(ulong y, ulong f)
    {
        while (true) { ulong z = (y | y << 1 | y >> 1) & f; if (z == y) return y; y = z; }
    }
}
