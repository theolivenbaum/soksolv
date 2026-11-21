using System.Numerics;
using System.Runtime.InteropServices;

namespace Soksolv.Search.Reverse;

/// <summary>Growable unmanaged int buffer.</summary>
public unsafe struct IntBuf
{
    public int* P; public int Len, Cap;
    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    public void Add(int v)
    {
        if (Len == Cap) Grow();
        P[Len++] = v;
    }
    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.NoInlining)]
    void Grow() { Cap = Cap == 0 ? 4 : Cap * 2; P = (int*)NativeMemory.Realloc(P, (nuint)Cap * 4); }
    public void Free() { NativeMemory.Free(P); P = null; Len = Cap = 0; }
}

/// <summary>
/// Pops open nodes by smallest h, FIFO within one h (= binary heap on (h&lt;&lt;32|id) since ids grow as nodes are
/// pushed). h >= MaxBuckets share the last bucket. O(1) operations, 4 bytes per entry (port of Go bucketQueue).
/// </summary>
public sealed unsafe class BucketQueue : IDisposable
{
    public const int MaxBuckets = 1 << 16;
    IntBuf[] b = [];
    int[] head = [];
    ulong[] bits = new ulong[2];   // bit k: bucket k holds entries
    int min;
    public int N;

    public void Push(int h, int id)
    {
        int k = Math.Min(h, MaxBuckets - 1);
        if (k >= b.Length)
        {
            int nb = Math.Max(k + 1, 2 * b.Length);
            Array.Resize(ref b, nb); Array.Resize(ref head, nb);
            Array.Resize(ref bits, (nb >> 6) + 2);
        }
        b[k].Add(id);
        bits[k >> 6] |= 1UL << (k & 63);
        if (N == 0 || k < min) min = k;
        N++;
    }

    public (int id, int h) Pop()
    {
        int from = min, w = from >> 6;
        ulong x = bits[w] & (~0UL << (from & 63));
        while (x == 0) x = bits[++w];
        int k = (w << 6) + BitOperations.TrailingZeroCount(x);
        min = k;
        ref var bk = ref b[k];
        int hd = head[k];
        int id = bk.P[hd++];
        head[k] = hd;
        N--;
        if (hd == bk.Len) { bk.Len = 0; head[k] = 0; bits[k >> 6] &= ~(1UL << (k & 63)); }
        else if (hd >= 1 << 16 && hd * 2 >= bk.Len)
        {
            int rest = bk.Len - hd;
            Buffer.MemoryCopy(bk.P + hd, bk.P, rest * 4L, rest * 4L);
            bk.Len = rest; head[k] = 0;
        }
        return (id, k);
    }

    public long Bytes { get { long t = (long)b.Length * 16 + (long)head.Length * 4; for (int i = 0; i < b.Length; i++) t += (long)b[i].Cap * 4; return t; } }
    public void Dispose() { for (int i = 0; i < b.Length; i++) b[i].Free(); b = []; head = []; bits = new ulong[2]; N = 0; }
}

/// <summary>FESS-style cell scheduling (port of Go cellQueue): open nodes live in per-cell queues (cell = boxes on start squares).</summary>
public sealed class CellQueue : IDisposable
{
    readonly List<BucketQueue> q = new();
    readonly List<int> order = new();
    readonly List<bool> seen = new();
    int next;
    public int Turn;
    public int N;

    public void Push(int k, int h, int id)
    {
        while (k >= q.Count) { q.Add(new BucketQueue()); seen.Add(false); }
        if (!seen[k]) { seen[k] = true; order.Add(k); next = order.Count - 1; } // visit a new cell next
        q[k].Push(h, id);
        N++;
    }

    /// <summary>Round robin (with new-cell jumps) or the best cell (most boxes on start squares). id = -1 when empty.</summary>
    public (int id, int h) PopCell(bool best)
    {
        int cell = -1;
        if (best)
        {
            for (int k = q.Count - 1; k >= 0; k--) if (q[k].N > 0) { cell = k; break; }
        }
        else
        {
            for (int i = 0, n = order.Count; i < n; i++)
            {
                int k = order[next % order.Count];
                next = (next + 1) % order.Count;
                if (q[k].N > 0) { cell = k; break; }
            }
        }
        if (cell < 0) return (-1, 0);
        N--;
        return q[cell].Pop();
    }
    public long Bytes { get { long t = 0; foreach (var x in q) t += x.Bytes; return t; } }
    public void Dispose() { foreach (var x in q) x.Dispose(); q.Clear(); }
}

/// <summary>Type-based exploration (port of Go explore.Types): random non-empty type, then a random node in it; fixed-seed xorshift. Unmanaged per-type (id, payload) lists.</summary>
public sealed unsafe class TypeQueue : IDisposable
{
    readonly Dictionary<ulong, int> idx = new();
    readonly List<int[]?> dense = new();
    IntBuf[] nodes = new IntBuf[64]; int nTypes;
    readonly List<int> live = new();
    int[] pos = new int[64];
    int[] typeH = new int[64];     // the payload (h) of every node of a type is the h part of the type key
    ulong rng = 0x9E3779B97F4A7C15UL;
    public int N;

    ulong Next() { rng ^= rng << 13; rng ^= rng >> 7; rng ^= rng << 17; return rng; }

    public void Push(ulong key, int id, int payload)
    {
        ulong h = key >> 16, k = key & 0xFFFF;
        int i; bool ok = false;
        if (h < 4096 && k < 256)
        {
            while ((ulong)dense.Count <= h) dense.Add(null);
            var row = dense[(int)h] ?? [];
            if ((ulong)row.Length <= k) { int old = row.Length; Array.Resize(ref row, (int)k + 1); Array.Fill(row, -1, old, row.Length - old); }
            dense[(int)h] = row;
            i = row[k];
            if (i >= 0) ok = true;
        }
        else ok = idx.TryGetValue(key, out i);
        if (!ok)
        {
            i = nTypes++;
            if (h < 4096 && k < 256) dense[(int)h]![k] = i; else idx[key] = i;
            if (i >= nodes.Length) { Array.Resize(ref nodes, nodes.Length * 2); Array.Resize(ref pos, pos.Length * 2); Array.Resize(ref typeH, typeH.Length * 2); }
            pos[i] = -1; typeH[i] = payload;
        }
        ref var b = ref nodes[i];
        if (b.Len == 0) { pos[i] = live.Count; live.Add(i); }
        b.Add(id);
        N++;
    }

    public bool Pop(out int id, out int payload)
    {
        id = payload = 0;
        if (live.Count == 0) return false;
        int i = live[(int)(Next() % (ulong)live.Count)];
        ref var b = ref nodes[i];
        int j = (int)(Next() % (ulong)b.Len);
        id = b.P[j]; payload = typeH[i];
        b.Len -= 1;
        b.P[j] = b.P[b.Len];
        N--;
        if (b.Len == 0)
        {
            int p = pos[i]; int last = live[^1];
            live[p] = last; pos[last] = p;
            live.RemoveAt(live.Count - 1);
            pos[i] = -1;
        }
        return true;
    }
    public long Bytes { get { long t = 64 + (long)nodes.Length * 24; for (int i = 0; i < nTypes; i++) t += (long)nodes[i].Cap * 4; return t; } }
    public void Dispose() { for (int i = 0; i < nTypes; i++) nodes[i].Free(); nTypes = 0; N = 0; }

}
