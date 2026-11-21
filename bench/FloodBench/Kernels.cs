using System.Numerics;
using System.Runtime.CompilerServices;
using System.Runtime.Intrinsics;
namespace FloodBench;

public interface IFill { static abstract ulong Fill(ulong y, ulong f); }

public struct FillLoop : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        while (true) { ulong z = (y | y << 1 | y >> 1) & f; if (z == y) return y; y = z; }
    }
}

/// <summary>upward by carry add (exact, no loop), downward by smear loop.</summary>
public struct FillAddLoop : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;      // seeds upward to the end of each hit run
        while (true) { ulong z = (a | a >> 1) & f; if (z == a) return a; a = z; }
    }
}

/// <summary>per-run extraction with tzcnt/lzcnt; no per-cell loop.</summary>
public struct FillRuns : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        ulong res = 0;
        while (y != 0)
        {
            ulong s = y & (0UL - y);
            ulong up = ((f + s) ^ f) & f | s;                 // [s .. top]
            ulong walls = ~f & (s - 1);
            ulong hb = walls == 0 ? 0 : 0x8000_0000_0000_0000UL >> BitOperations.LeadingZeroCount(walls);
            ulong down = (s - 1) & ~((hb << 1) - 1);          // [bottom .. s-1]; hb==0 -> (0<<1)-1 = all ones -> wrong
            if (hb == 0) down = s - 1;
            ulong run = up | down;
            res |= run;
            y &= ~run;
        }
        return res;
    }
}

/// <summary>Kogge-Stone occluded fill both directions.</summary>
public struct FillKS : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        ulong pu = f & (f << 1), pd = f & (f >> 1);
        ulong u = y | (y << 1) & f, d = y | (y >> 1) & f;
        ulong a = u | d;
        // second..: doubling on the combined propagators
        ulong p2 = pu & (pu << 2), q2 = pd & (pd >> 2);
        u |= (u << 2) & pu; d |= (d >> 2) & pd;
        u |= (u << 4) & p2; d |= (d >> 4) & q2;
        ulong p4 = p2 & (p2 << 4), q4 = q2 & (q2 >> 4);
        u |= (u << 8) & p4; d |= (d >> 8) & q4;
        ulong p8 = p4 & (p4 << 8), q8 = q4 & (q4 >> 8);
        u |= (u << 16) & p8; d |= (d >> 16) & q8;
        ulong p16 = p8 & (p8 << 16), q16 = q8 & (q8 >> 16);
        u |= (u << 32) & p16; d |= (d >> 32) & q16;
        return u | d;
    }
}

/// <summary>Add up + KS-down limited: one smear step test then loop.</summary>
public struct FillAddKS : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;
        ulong pd = f & (f >> 1);
        ulong d = a | (a >> 1) & f;
        if (d == a) return a;
        ulong q2 = pd & (pd >> 2);
        d |= (d >> 2) & pd; d |= (d >> 4) & q2;
        ulong q4 = q2 & (q2 >> 4);
        d |= (d >> 8) & q4;
        ulong q8 = q4 & (q4 >> 8);
        d |= (d >> 16) & q8;
        ulong q16 = q8 & (q8 >> 16);
        d |= (d >> 32) & q16;
        return d;
    }
}

public struct FillAddPeel : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;
        ulong d = (a | a >> 1) & f;
        if (d == a) return a;
        while (true) { ulong z = (d | d >> 1) & f; if (z == d) return d; d = z; }
    }
}

/// <summary>add up (exact) + doubling smear down for distances up to 7, and the longer steps only when the row has a run of 8+ free cells.</summary>
public struct FillAddKS3 : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;
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
}

/// <summary>as FillAddKS3 but peeling the common "nothing to extend downwards" case first (73% of fills).</summary>
public struct FillAddKS3p : IFill
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        y &= f;
        ulong a = ((f + y) ^ f) & f | y;
        ulong d = a | (a >> 1) & f;
        if (d == a) return a;
        ulong q = f & (f >> 1);
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
}

public static unsafe class K
{
    // ---- current kernels (copied from the solver) ----
    public static void Alt<F>(ulong* free, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        reg[rp] = F.Fill(seed, free[rp]);
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
                    y = F.Fill(y, f);
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
                    y = F.Fill(y, f);
                    if (y == cr) continue;
                    reg[r] = y;
                    m |= (1UL << (r - 1)) & valid; nxt |= (1UL << (r + 1)) & valid;
                }
            cur = nxt; up = !up;
        }
        int l = rp, h = rp;
        while (reg[l - 1] != 0) l--;
        while (reg[h + 1] != 0) h++;
        lo = l; hi = h;
    }

    public static void Dirty<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        int l = rp, h = rp;
        ulong y0 = F.Fill(seed, f[rp]);
        ulong nw0 = y0;
        reg[rp] = y0;
        ulong dirty = 0;
        if ((nw0 & f[rp - 1] & ~reg[rp - 1]) != 0) dirty |= 1UL << (rp - 1);
        if ((nw0 & f[rp + 1] & ~reg[rp + 1]) != 0) dirty |= 1UL << (rp + 1);
        while (dirty != 0)
        {
            int r = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
            ulong cur = reg[r];
            ulong y = cur | (reg[r - 1] | reg[r + 1]) & f[r];
            if (y == cur) continue;
            y = F.Fill(y, f[r]);
            reg[r] = y;
            ulong nw = y & ~cur;
            if (r < l) l = r; if (r > h) h = r;
            if ((nw & f[r - 1] & ~reg[r - 1]) != 0) dirty |= 1UL << (r - 1);
            if ((nw & f[r + 1] & ~reg[r + 1]) != 0) dirty |= 1UL << (r + 1);
        }
        lo = l; hi = h;
    }

    /// <summary>chain-follow: after filling a row continue directly into the neighbouring row that received new seeds; the other direction is deferred in a row mask.</summary>
    public static void Chain<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        int l = rp, h = rp;
        int r = rp; ulong s = seed;
        ulong pend = 0;
        while (true)
        {
            ulong fr = f[r];
            ulong y = F.Fill(s, fr);
            reg[r] = y | reg[r];        // seeds are outside reg[r]; old bits are never lost
            ulong nw = y & ~0UL;
            ulong fu = f[r + 1], fd = f[r - 1];
            ulong su = nw & fu & ~reg[r + 1], sd = nw & fd & ~reg[r - 1];
            if (su != 0)
            {
                if (sd != 0) pend |= 1UL << (r - 1);
                r++; s = su; if (r > h) h = r; continue;
            }
            if (sd != 0) { r--; s = sd; if (r < l) l = r; continue; }
            // pop a pending row
            while (true)
            {
                if (pend == 0) { lo = l; hi = h; return; }
                r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
                s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
                if (s != 0) { if (r < l) l = r; if (r > h) h = r; break; }
            }
        }
    }

    /// <summary>Up/down row passes in registers (no worklist): a pass is repeated in the other direction only when it created cells that can feed the rows it already passed.</summary>
    public static void Sweep<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        reg[rp] = F.Fill(seed, f[rp]);
        int l = rp, h = rp;
        int startU = rp + 1;
        ulong back0 = reg[rp] & f[rp - 1];
        while (true)
        {
            // up pass
            ulong back = back0; back0 = 0;
            int r = startU;
            ulong below = reg[r - 1];
            while (true)
            {
                ulong cur = reg[r], fr = f[r];
                ulong y = F.Fill((cur | below | reg[r + 1]) & fr, fr);
                reg[r] = y;
                ulong nw = y & ~cur;
                back |= nw & f[r - 1] & ~below;
                below = y;
                if (y == 0 && r > h) break;
                if (r > h) h = r;
                r++;
            }
            if (back == 0) break;
            // down pass
            ulong fwd = 0;
            r = h;
            ulong above = reg[r + 1];
            while (true)
            {
                ulong cur = reg[r], fr = f[r];
                ulong y = F.Fill((cur | above | reg[r - 1]) & fr, fr);
                reg[r] = y;
                ulong nw = y & ~cur;
                fwd |= nw & f[r + 1] & ~above;
                above = y;
                if (y == 0 && r < l) break;
                if (r < l) l = r;
                r--;
            }
            if (fwd == 0) break;
            startU = l;
        }
        lo = l; hi = h;
    }

    public static long CVisit, CPop, CPopEmpty, CBoth, CFillIt, CPc1, CPc2, CSame, CRuns1, CDown0;
    public static void ChainStat(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi)
    {
        int l = rp, h = rp;
        int r = rp; ulong s = seed;
        ulong pend = 0;
        while (true)
        {
            ulong fr = f[r];
            ulong y = FillLoop.Fill(s, fr);
            { ulong a = s, ff = fr; while (true) { CFillIt++; ulong z = (a | a << 1 | a >> 1) & ff; if (z == a) break; a = z; } }
            CVisit++; { int pc = BitOperations.PopCount(s); if (pc == 1) CPc1++; else if (pc == 2) CPc2++; if (y == s) CSame++; ulong ff = fr; ulong up = ((ff + s) ^ ff) & ff | s; if (up == y) CDown0++; ulong ys = y; int runs = 0; while (ys != 0) { ulong lo1 = ys & (0UL - ys); ulong run = ((ff + lo1) ^ ff) & ff | lo1; ys &= ~((((ff + lo1) ^ ff) & ff) | lo1 | 0); runs++; if (runs > 8) break; } }
            reg[r] = y | reg[r];
            ulong nw = y;
            ulong fu = f[r + 1], fd = f[r - 1];
            ulong su = nw & fu & ~reg[r + 1], sd = nw & fd & ~reg[r - 1];
            if (su != 0)
            {
                if (sd != 0) { pend |= 1UL << (r - 1); CBoth++; }
                r++; s = su; if (r > h) h = r; continue;
            }
            if (sd != 0) { r--; s = sd; if (r < l) l = r; continue; }
            while (true)
            {
                if (pend == 0) { lo = l; hi = h; return; }
                r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
                CPop++;
                s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
                if (s != 0) { if (r < l) l = r; if (r > h) h = r; break; }
                CPopEmpty++;
            }
        }
    }

    public static void Chain2<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        int l = rp, h = rp;
        int r = rp; ulong s = seed;
        ulong pend = 0;
        ulong fr = f[r], fu, fd, ru, rd;
        while (true)
        {
            ulong y = F.Fill(s, fr);
            ulong cur = reg[r] | y;
            reg[r] = cur;
            fu = f[r + 1]; fd = f[r - 1]; ru = reg[r + 1]; rd = reg[r - 1];
            ulong su = y & fu & ~ru, sd = y & fd & ~rd;
            if (su != 0)
            {
                pend |= (sd != 0 ? 1UL : 0UL) << (r - 1);
                r++; s = su; fr = fu; if (r > h) h = r; continue;
            }
            if (sd != 0) { r--; s = sd; fr = fd; if (r < l) l = r; continue; }
            while (true)
            {
                if (pend == 0) { lo = l; hi = h; return; }
                r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
                fr = f[r];
                s = (reg[r - 1] | reg[r + 1]) & fr & ~reg[r];
                if (s != 0) { if (r < l) l = r; if (r > h) h = r; break; }
            }
        }
    }

    /// <summary>chain with the touched-row mask (no per-visit span tracking) and a branchless pending-row update.</summary>
    public static void Chain3<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        nint r = rp; ulong s = seed;
        ulong pend = 0, touched = 0;
        while (true)
        {
            touched |= 1UL << (int)r;
            ulong y = F.Fill(s, f[r]);
            reg[r] |= y;
            ulong su = y & f[r + 1] & ~reg[r + 1], sd = y & f[r - 1] & ~reg[r - 1];
            if (su != 0)
            {
                pend |= (sd != 0 ? 1UL : 0UL) << (int)(r - 1);
                r++; s = su; continue;
            }
            if (sd != 0) { r--; s = sd; continue; }
            while (true)
            {
                if (pend == 0)
                {
                    lo = BitOperations.TrailingZeroCount(touched); hi = 63 - BitOperations.LeadingZeroCount(touched);
                    return;
                }
                r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
                s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
                if (s != 0) break;
            }
        }
    }

    /// <summary>same, branch on the pending-row pop result removed by always processing the popped row (fill of an empty seed is a no-op).</summary>
    public static void Chain4<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        nint r = rp; ulong s = seed;
        ulong pend = 0, touched = 0;
        while (true)
        {
            touched |= 1UL << (int)r;
            ulong y = F.Fill(s, f[r]);
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
                lo = BitOperations.TrailingZeroCount(touched); hi = 63 - BitOperations.LeadingZeroCount(touched);
                return;
            }
            r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
            s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
            // an empty seed row: Fill(0) = 0, nothing written, falls through to the next pop
            touched &= ~(0UL);
        }
    }

    /// <summary>chain4 with the up/down choice as conditional moves (one branch: continue the chain or pop).</summary>
    public static void Chain5<F>(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi) where F : IFill
    {
        nint r = rp; ulong s = seed;
        ulong pend = 0, touched = 0;
        while (true)
        {
            touched |= 1UL << (int)r;
            ulong y = F.Fill(s, f[r]);
            reg[r] |= y;
            ulong su = y & f[r + 1] & ~reg[r + 1], sd = y & f[r - 1] & ~reg[r - 1];
            bool up = su != 0;
            ulong s2 = up ? su : sd;
            pend |= ((up & (sd != 0)) ? 1UL : 0UL) << (int)(r - 1);
            if (s2 != 0)
            {
                r = up ? r + 1 : r - 1; s = s2;
                continue;
            }
            if (pend == 0)
            {
                lo = BitOperations.TrailingZeroCount(touched); hi = 63 - BitOperations.LeadingZeroCount(touched);
                return;
            }
            r = BitOperations.TrailingZeroCount(pend); pend &= pend - 1;
            s = (reg[r - 1] | reg[r + 1]) & f[r] & ~reg[r];
        }
    }
}
