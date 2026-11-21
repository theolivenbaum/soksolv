using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Bench.Explore;

/// <summary>Player-region flood fill + min cell: stamp-queue (Go style) vs row bitboards (W &lt;= 64).</summary>
public sealed class Flood
{
    readonly int W, H, N;
    readonly bool[] wall;
    readonly int[] mark; int stamp; readonly int[] q; readonly int[] dirs;
    // bitboard
    readonly ulong[] freeRows; readonly ulong[] occRows; readonly ulong[] reach;
    public Flood(Soksolv.Model.Level lv)
    {
        W = lv.W; H = lv.H; N = lv.Cells; wall = lv.Wall; mark = new int[N]; q = new int[N]; dirs = [-W, W, -1, 1];
        freeRows = new ulong[H]; occRows = new ulong[H]; reach = new ulong[H];
        for (int y = 0; y < H; y++) for (int x = 0; x < W; x++) if (!wall[y * W + x]) freeRows[y] |= 1UL << x;
    }
    public bool Fits => W <= 64;

    public int Queue(int p, ReadOnlySpan<int> boxes)
    {
        // boxes are marked with a negative stamp trick: use mark = -1 for boxes
        stamp++; int st = stamp;
        foreach (int b in boxes) mark[b] = -st;
        int head = 0, tail = 0; q[tail++] = p; mark[p] = st; int m = p;
        while (head < tail)
        {
            int c = q[head++];
            foreach (int d in dirs)
            {
                int y = c + d;
                if (mark[y] != st && mark[y] != -st && !wall[y]) { mark[y] = st; q[tail++] = y; if (y < m) m = y; }
            }
        }
        return m;
    }

    public int Bits(int p, ReadOnlySpan<int> boxes)
    {
        Array.Clear(occRows);
        foreach (int b in boxes) occRows[b / W] |= 1UL << (b % W);
        Array.Clear(reach);
        int py = p / W; reach[py] = 1UL << (p % W);
        bool changed = true;
        int lo = py, hi = py;
        while (changed)
        {
            changed = false;
            for (int y = lo; y <= hi; y++) // down sweep
            {
                ulong f = freeRows[y] & ~occRows[y];
                ulong r = reach[y];
                if (y > 0 && y - 1 >= lo - 1) r |= reach[y - 1];
                r = Fill(r & f | (reach[y]), f);
                if (r != reach[y]) { reach[y] = r; changed = true; if (y == hi && hi + 1 < H) hi++; if (y == lo && lo > 0) lo--; }
            }
            for (int y = hi; y >= lo; y--)
            {
                ulong f = freeRows[y] & ~occRows[y];
                ulong r = reach[y];
                if (y + 1 < H) r |= reach[y + 1];
                r = Fill(r & f | reach[y], f);
                if (r != reach[y]) { reach[y] = r; changed = true; if (y == hi && hi + 1 < H) hi++; if (y == lo && lo > 0) lo--; }
            }
        }
        for (int y = 0; y < H; y++) if (reach[y] != 0) return y * W + BitOperations.TrailingZeroCount(reach[y]);
        return p;
    }

    // fill seeds along runs of free cells within a row (both directions)
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong Fill(ulong seed, ulong free)
    {
        seed &= free;
        // toward higher bits: carry propagation through the run
        ulong up = (((free + seed) ^ free) | seed) & free;
        // toward lower bits: reverse trick via bit reversal
        ulong rs = ReverseBits(seed), rf = ReverseBits(free);
        ulong dn = ReverseBits((((rf + rs) ^ rf) | rs) & rf);
        return up | dn;
    }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong ReverseBits(ulong v)
    {
        v = ((v >> 1) & 0x5555555555555555UL) | ((v & 0x5555555555555555UL) << 1);
        v = ((v >> 2) & 0x3333333333333333UL) | ((v & 0x3333333333333333UL) << 2);
        v = ((v >> 4) & 0x0F0F0F0F0F0F0F0FUL) | ((v & 0x0F0F0F0F0F0F0F0FUL) << 4);
        return System.Buffers.Binary.BinaryPrimitives.ReverseEndianness(v);
    }
}
