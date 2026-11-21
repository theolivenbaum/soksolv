using System.Diagnostics;
using System.Numerics;
using System.Runtime.CompilerServices;
using Soksolv.Model;

namespace Soksolv.Bench.Explore;

/// <summary>
/// Legal non-dead push generation for one state: per-box loop with stamp marks (Go style) vs row bitboards
/// (chess-style: one ulong per grid row, W &lt;= 64, shifts line up the player-reach / free / box sets).
/// Both start from a flood of the player's region; the bitboard variant also records region bits.
/// </summary>
public sealed class BitboardPushGen
{
    readonly Level lv; readonly int W, H, N; readonly int[] dirs; readonly bool[] dead;
    readonly int[] mark; int stamp; readonly int[] q;
    readonly ulong[] wallRows, deadRows, occ, reach, cand = new ulong[4];
    readonly bool[] occB;
    public BitboardPushGen(Level lv, bool[] dead)
    {
        this.lv = lv; W = lv.W; H = lv.H; N = lv.Cells; dirs = [-W, W, -1, 1]; this.dead = dead;
        mark = new int[N]; q = new int[N]; occB = new bool[N];
        wallRows = new ulong[H]; deadRows = new ulong[H]; occ = new ulong[H]; reach = new ulong[H];
        for (int c = 0; c < N; c++) { if (lv.Wall[c]) wallRows[c / W] |= 1UL << (c % W); if (dead[c]) deadRows[c / W] |= 1UL << (c % W); }
    }
    public bool Fits => W <= 64;

    /// <summary>Per-box loop. Returns number of pushes and a checksum.</summary>
    public int Loop(ReadOnlySpan<int> boxes, int player, out long sum)
    {
        foreach (int b in boxes) occB[b] = true;
        stamp++; int st = stamp, head = 0, tail = 0; q[tail++] = player; mark[player] = st;
        while (head < tail) { int c = q[head++]; foreach (int d in dirs) { int y = c + d; if (mark[y] != st && !lv.Wall[y] && !occB[y]) { mark[y] = st; q[tail++] = y; } } }
        int n = 0; sum = 0;
        foreach (int b in boxes)
            for (int di = 0; di < 4; di++)
            {
                int d = dirs[di], t = b + d;
                if (mark[b - d] != st || lv.Wall[t] || occB[t] || dead[t]) continue;
                n++; sum += b * 4 + di;
            }
        foreach (int b in boxes) occB[b] = false;
        return n;
    }

    /// <summary>Bitboard: flood sets row bits; pushes for all boxes come from 4 shifted ANDs per row.</summary>
    public int Bits(ReadOnlySpan<int> boxes, int player, out long sum)
    {
        Array.Clear(occ); Array.Clear(reach);
        foreach (int b in boxes) occ[b / W] |= 1UL << (b % W);
        foreach (int b in boxes) occB[b] = true;
        stamp++; int st = stamp, head = 0, tail = 0; q[tail++] = player; mark[player] = st; reach[player / W] |= 1UL << (player % W);
        while (head < tail)
        {
            int c = q[head++];
            foreach (int d in dirs) { int y = c + d; if (mark[y] != st && !lv.Wall[y] && !occB[y]) { mark[y] = st; q[tail++] = y; reach[y / W] |= 1UL << (y % W); } }
        }
        foreach (int b in boxes) occB[b] = false;
        int n = 0; sum = 0;
        for (int y = 1; y < H - 1; y++)
        {
            ulong okTarget(int row, int shiftDummy) => 0;
            ulong blocked(int r) => wallRows[r] | occ[r] | deadRows[r];
            ulong B = occ[y];
            if (B == 0) continue;
            ulong right = B & (reach[y] << 1) & ~(blocked(y) >> 1);   // player at x-1, target x+1
            ulong left = B & (reach[y] >> 1) & ~(blocked(y) << 1);    // player at x+1, target x-1
            ulong down = B & reach[y - 1] & ~blocked(y + 1);          // player above, target below
            ulong up = B & reach[y + 1] & ~blocked(y - 1);            // player below, target above
            n += BitOperations.PopCount(right) + BitOperations.PopCount(left) + BitOperations.PopCount(down) + BitOperations.PopCount(up);
            // checksum identical to Loop (b*4+di, di: 0=up(-W) 1=down(+W) 2=left 3=right)
            for (ulong m = up; m != 0; m &= m - 1) sum += (y * W + BitOperations.TrailingZeroCount(m)) * 4 + 0;
            for (ulong m = down; m != 0; m &= m - 1) sum += (y * W + BitOperations.TrailingZeroCount(m)) * 4 + 1;
            for (ulong m = left; m != 0; m &= m - 1) sum += (y * W + BitOperations.TrailingZeroCount(m)) * 4 + 2;
            for (ulong m = right; m != 0; m &= m - 1) sum += (y * W + BitOperations.TrailingZeroCount(m)) * 4 + 3;
        }
        return n;
    }
}

/// <summary>Child key + hash construction cost: rebuild-from-boxes (Go) vs incremental (bit flip + Zobrist XOR delta).</summary>
public static unsafe class HashBench
{
    static ulong Mix(ulong h, ulong v) { h = unchecked((h ^ v) * 0xBF58476D1CE4E5B9UL); return h ^ (h >> 31); }

    public static void Run(int floors, int boxes, int reps = 2_000_000)
    {
        int nw = (floors + 63) / 64;
        var rng = new Random(1);
        var z = new ulong[floors]; var zp = new ulong[floors];
        for (int i = 0; i < floors; i++) { z[i] = (ulong)rng.NextInt64(); zp[i] = (ulong)rng.NextInt64(); }
        // random parent state
        var set = new HashSet<int>(); while (set.Count < boxes) set.Add(rng.Next(floors));
        var par = set.OrderBy(x => x).ToArray();
        var pkey = new ulong[nw]; foreach (int b in par) pkey[b >> 6] |= 1UL << (b & 63);
        ulong phash = 0; foreach (int b in par) phash ^= z[b];
        // pre-draw moves: (box index, target floor idx not occupied)
        var mv = new (int i, int t)[1024];
        for (int k = 0; k < mv.Length; k++) { int i = rng.Next(boxes); int t; do t = rng.Next(floors); while (set.Contains(t)); mv[k] = (i, t); }
        var child = new int[boxes]; var ckey = new ulong[nw]; ulong acc = 0;
        // (a) Go style: copy, replace, sort, clear, pack, mix-hash over words
        for (int w = 0; w < 2; w++)
        {
            var sw = Stopwatch.StartNew();
            for (int r = 0; r < reps; r++)
            {
                var (i, t) = mv[r & 1023];
                par.CopyTo(child, 0); child[i] = t; Array.Sort(child);
                Array.Clear(ckey); foreach (int b in child) ckey[b >> 6] |= 1UL << (b & 63);
                ulong h = unchecked(99 * 0x9E3779B97F4A7C15UL + 1); foreach (ulong x in ckey) h = Mix(h, x);
                acc += h;
            }
            if (w == 1) Console.Write($"  floors={floors,5} boxes={boxes,3} nw={nw,2} | rebuild+sort+pack+hash {sw.Elapsed.TotalMilliseconds * 1e6 / reps,6:F1} ns");
        }
        // (b) incremental: copy parent key words, flip two bits, hash = phash ^ z[from] ^ z[to] (Zobrist, no sort)
        for (int w = 0; w < 2; w++)
        {
            var sw = Stopwatch.StartNew();
            for (int r = 0; r < reps; r++)
            {
                var (i, t) = mv[r & 1023]; int f = par[i];
                pkey.CopyTo(ckey, 0); ckey[f >> 6] ^= 1UL << (f & 63); ckey[t >> 6] ^= 1UL << (t & 63);
                ulong h = phash ^ z[f] ^ z[t] ^ zp[r & (floors - 1 & 0x7FFFFFFF) % floors];
                acc += h;
            }
            if (w == 1) Console.Write($" | incremental flip+zobrist {sw.Elapsed.TotalMilliseconds * 1e6 / reps,6:F1} ns");
        }
        // (c) incremental key, but mix-hash words (no Zobrist; still avoids sort/repack)
        for (int w = 0; w < 2; w++)
        {
            var sw = Stopwatch.StartNew();
            for (int r = 0; r < reps; r++)
            {
                var (i, t) = mv[r & 1023]; int f = par[i];
                pkey.CopyTo(ckey, 0); ckey[f >> 6] ^= 1UL << (f & 63); ckey[t >> 6] ^= 1UL << (t & 63);
                ulong h = unchecked(99 * 0x9E3779B97F4A7C15UL + 1); foreach (ulong x in ckey) h = Mix(h, x);
                acc += h;
            }
            if (w == 1) Console.WriteLine($" | incr key + word-mix hash {sw.Elapsed.TotalMilliseconds * 1e6 / reps,6:F1} ns");
        }
        if (acc == 1) Console.WriteLine();
    }
}
