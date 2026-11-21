using Soksolv.Search;

namespace Soksolv.Tests;

/// <summary>Flood kernels v2 (<see cref="RowK"/>): exhaustive-style random checks against the previous kernels (old alternating-pass flood, per-cell smear fill) and a cell-level BFS.</summary>
public unsafe class RowKTests
{
    static ulong[] RandomRows(Random rnd, int rows, int w, double density)
    {
        var f = new ulong[rows];
        for (int r = 1; r < rows - 1; r++)
            for (int x = 1; x < w - 1; x++) if (rnd.NextDouble() < density) f[r] |= 1UL << x;
        return f;
    }

    [Fact]
    public void FillEqualsSmearLoop()
    {
        var rnd = new Random(1);
        for (int i = 0; i < 200000; i++)
        {
            ulong f = (ulong)(rnd.NextInt64() & rnd.NextInt64()) | (i % 3 == 0 ? (ulong)rnd.NextInt64() : 0UL) | (i % 7 == 0 ? ulong.MaxValue : 0UL);
            if (i % 11 == 0) f &= ~(0xFFUL << rnd.Next(0, 56));
            ulong y = (ulong)rnd.NextInt64() & (ulong)rnd.NextInt64() & (ulong)rnd.NextInt64() & f;
            Assert.Equal(RowK.FillOld(y, f), RowK.Fill(y, f));
        }
    }

    [Theory]
    [InlineData(64, 20, 0.55)]
    [InlineData(40, 62, 0.7)]
    [InlineData(12, 14, 0.45)]
    [InlineData(64, 64, 0.8)]
    public void FloodEqualsOldKernelAndBfs(int w, int rows, double density)
    {
        var rnd = new Random(w * 1000 + rows);
        var reg = stackalloc ulong[64]; var old = stackalloc ulong[64]; var m = stackalloc ulong[64]; var ban = stackalloc ulong[64];
        for (int it = 0; it < 4000; it++)
        {
            var fa = RandomRows(rnd, rows, w, density);
            fixed (ulong* f = fa)
            {
                // a free start cell
                int rp = 0, cp = 0; bool found = false;
                for (int t = 0; t < 200 && !found; t++) { rp = rnd.Next(1, rows - 1); cp = rnd.Next(1, w - 1); found = (f[rp] >> cp & 1) != 0; }
                if (!found) continue;
                for (int i = 0; i < 64; i++) { reg[i] = 0; old[i] = 0; m[i] = 0; ban[i] = 0; }
                int lo = rp, hi = rp;
                RowK.Flood(f, reg, rp, 1UL << cp, ref lo, ref hi);
                RowK.FloodOld(f, old, rows, rp, 1UL << cp);
                int ol = rows, oh = -1;
                for (int r = 0; r < rows; r++) { Assert.Equal(old[r], reg[r]); if (old[r] != 0) { ol = Math.Min(ol, r); oh = Math.Max(oh, r); } }
                Assert.Equal(ol, lo); Assert.Equal(oh, hi);
                // cell-level BFS
                var seen = new bool[rows * 64]; var q = new Stack<int>(); q.Push(rp * 64 + cp); seen[rp * 64 + cp] = true;
                while (q.Count > 0)
                {
                    int c = q.Pop(); int r = c / 64, x = c % 64;
                    foreach (var (dr, dx) in new[] { (-1, 0), (1, 0), (0, -1), (0, 1) })
                    {
                        int nr = r + dr, nx = x + dx;
                        if (nr < 0 || nr >= rows || nx < 0 || nx > 63 || (f[nr] >> nx & 1) == 0 || seen[nr * 64 + nx]) continue;
                        seen[nr * 64 + nx] = true; q.Push(nr * 64 + nx);
                    }
                }
                for (int r = 0; r < rows; r++) for (int x = 0; x < 64; x++) Assert.Equal(seen[r * 64 + x], (reg[r] >> x & 1) != 0);
                // masked flood: ban a random subset of the free cells (not the start), compare with the plain flood over f & ~ban
                var g = new ulong[rows];
                for (int r = 0; r < rows; r++) { ulong b = (ulong)rnd.NextInt64() & (ulong)rnd.NextInt64() & f[r]; if (r == rp) b &= ~(1UL << cp); ban[r] = b; g[r] = f[r] & ~b; }
                int l2 = rp, h2 = rp;
                RowK.FloodMasked(f, ban, m, rp, 1UL << cp, ref l2, ref h2);
                fixed (ulong* gp = g)
                {
                    for (int i = 0; i < 64; i++) old[i] = 0;
                    RowK.FloodOld(gp, old, rows, rp, 1UL << cp);
                }
                for (int r = 0; r < rows; r++) Assert.Equal(old[r], m[r]);
            }
        }
    }
}
