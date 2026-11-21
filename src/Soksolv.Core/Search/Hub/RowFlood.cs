using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search.Hub;

/// <summary>Bit-row (one ulong per grid row, bit = column, rows indexed y+1 with zero padding rows 0 and h+1, h &lt;= 62) 4-connected flood with a dirty-row worklist.</summary>
public static unsafe class RowFlood
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }

    /// <summary>Floods the component of the single-bit <paramref name="seed"/> (a cell of <paramref name="rem"/> in row r0) into reg (must be zero on entry); returns the row span.</summary>
    public static void One(ulong* rem, ulong* reg, int r0, ulong seed, out int lo, out int hi)
    {
        ulong y0 = Fill(seed, rem[r0]);
        reg[r0] = y0;
        lo = hi = r0;
        ulong dirty = 0;
        if ((y0 & rem[r0 - 1]) != 0) dirty |= 1UL << (r0 - 1);
        if ((y0 & rem[r0 + 1]) != 0) dirty |= 1UL << (r0 + 1);
        while (dirty != 0)
        {
            int r = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
            ulong cur = reg[r];
            ulong y = cur | (reg[r - 1] | reg[r + 1]) & rem[r];
            if (y == cur) continue;
            y = Fill(y, rem[r]);
            reg[r] = y;
            ulong nw = y & ~cur;
            if (r < lo) lo = r; if (r > hi) hi = r;
            if ((nw & rem[r - 1] & ~reg[r - 1]) != 0) dirty |= 1UL << (r - 1);
            if ((nw & rem[r + 1] & ~reg[r + 1]) != 0) dirty |= 1UL << (r + 1);
        }
    }
}
