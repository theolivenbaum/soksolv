using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search.Hub;

/// <summary>Bit-row (one ulong per grid row, bit = column, rows indexed y+1 with zero padding rows 0 and h+1, h &lt;= 62) 4-connected flood with a dirty-row worklist.</summary>
public static unsafe class RowFlood
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Fill(ulong y, ulong f)
    {
        Soksolv.Search.RowK.AssertSubset(y, f, "RowFlood.Fill");
        return Soksolv.Search.RowK.Fill(y, f);
    }

    /// <summary>Floods the component of the single-bit <paramref name="seed"/> (a cell of <paramref name="rem"/> in row r0) into reg (must be zero on entry); returns the row span.</summary>
    public static void One(ulong* rem, ulong* reg, int r0, ulong seed, out int lo, out int hi)
    {
        lo = hi = r0;
        Soksolv.Search.RowK.Flood(rem, reg, r0, seed, ref lo, ref hi);
    }
}
