using System.Runtime.CompilerServices;

namespace Soksolv.Search.Macro;

/// <summary>FESS feature cell (Go featureCell): oop, packed, connectivity, roomConnectivity.</summary>
public readonly record struct FeatureCell(int Packed, int Connectivity, int RoomConnectivity, int Oop);

/// <summary>Per-move score (Go moveScore): compared lexicographically as (oop, -packed, conn, room, hot, -mobility).</summary>
public struct MoveScore
{
    public ushort Oop, Packed, Conn, Hot; public uint Room, Mobility;
    public static int Compare(in MoveScore a, in MoveScore b)
    {
        if (a.Oop != b.Oop) return a.Oop < b.Oop ? -1 : 1;
        if (a.Packed != b.Packed) return a.Packed > b.Packed ? -1 : 1;   // -packed ascending
        if (a.Conn != b.Conn) return a.Conn < b.Conn ? -1 : 1;
        if (a.Room != b.Room) return a.Room < b.Room ? -1 : 1;
        if (a.Hot != b.Hot) return a.Hot < b.Hot ? -1 : 1;
        if (a.Mobility != b.Mobility) return a.Mobility > b.Mobility ? -1 : 1;
        return 0;
    }
}

/// <summary>Frontier heap entry (Go fessEntry): ordered by weight, score, node, move.</summary>
public struct FessEntry
{
    public int Node, Move, Weight; public uint Version; public MoveScore Score;
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static bool Less(in FessEntry a, in FessEntry b)
    {
        if (a.Weight != b.Weight) return a.Weight < b.Weight;
        int c = MoveScore.Compare(a.Score, b.Score);
        if (c != 0) return c < 0;
        if (a.Node != b.Node) return a.Node < b.Node;
        return a.Move < b.Move;
    }
}

/// <summary>Binary min-heap of frontier entries (one per feature cell).</summary>
public sealed class FessHeap
{
    FessEntry[] a = new FessEntry[8]; int n;
    public int Count => n;
    public long Bytes => (long)a.Length * 40;
    public void Push(in FessEntry e)
    {
        if (n == a.Length) Array.Resize(ref a, n * 2);
        int i = n++;
        while (i > 0)
        {
            int p = (i - 1) >> 1;
            if (!FessEntry.Less(e, a[p])) break;
            a[i] = a[p]; i = p;
        }
        a[i] = e;
    }
    /// <summary>Go container/heap.Remove(h, i): swap with the last, restore the heap, return the removed entry.</summary>
    public FessEntry Remove(int i)
    {
        int last = n - 1;
        if (i != last)
        {
            (a[i], a[last]) = (a[last], a[i]);
            // down(i, last)
            int i0 = i, cur = i;
            while (true)
            {
                int j1 = 2 * cur + 1;
                if (j1 >= last || j1 < 0) break;
                int j = j1;
                if (j1 + 1 < last && FessEntry.Less(a[j1 + 1], a[j1])) j = j1 + 1;
                if (!FessEntry.Less(a[j], a[cur])) break;
                (a[cur], a[j]) = (a[j], a[cur]);
                cur = j;
            }
            if (cur == i0)
            {
                // up(i)
                int jj = i0;
                while (true)
                {
                    int p = (jj - 1) / 2;
                    if (p == jj || !FessEntry.Less(a[jj], a[p])) break;
                    (a[jj], a[p]) = (a[p], a[jj]);
                    jj = p;
                }
            }
        }
        n--;
        return a[n];
    }
    public FessEntry Pop()
    {
        var top = a[0];
        var x = a[--n];
        int i = 0;
        while (true)
        {
            int l = 2 * i + 1;
            if (l >= n) break;
            int m = l;
            if (l + 1 < n && FessEntry.Less(a[l + 1], a[l])) m = l + 1;
            if (!FessEntry.Less(a[m], x)) break;
            a[i] = a[m]; i = m;
        }
        if (n > 0) a[i] = x;
        return top;
    }
}

/// <summary>Per-node search data of the macro lane (Go parallel arrays), stored beside the StateTable record.</summary>
public struct MacroAux
{
    public int MvStart;                     // start of the node's moves in the mv arena, -1 = not opened
    public uint MvLen, MvNext, MvLeft, MvOff, MvTotal;
    public uint Version, Bucket;
    public int EdgeHead, LiveChildren;
    public ushort Reg;
    public byte Dead;
    // rev-groups (exp-290)
    public int RevGroup, RevOffset, NovOffset; public ushort RevSize, PushTo; public short RevBestExit;
    public FeatureCell Actual;
}

public struct MacroEdge { public int Parent, Next; }
