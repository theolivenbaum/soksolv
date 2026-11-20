using System.Runtime.InteropServices;

namespace Soksolv.Search.Reverse;

/// <summary>Growable unmanaged int buffer.</summary>
public unsafe struct IntBuf
{
    public int* P; public int Len, Cap;
    public void Add(int v)
    {
        if (Len == Cap) { Cap = Cap == 0 ? 4 : Cap * 2; P = (int*)NativeMemory.Realloc(P, (nuint)Cap * 4); }
        P[Len++] = v;
    }
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
    int min;
    public int N;

    public void Push(int h, int id)
    {
        int k = Math.Min(h, MaxBuckets - 1);
        if (k >= b.Length)
        {
            int nb = Math.Max(k + 1, 2 * b.Length);
            Array.Resize(ref b, nb); Array.Resize(ref head, nb);
        }
        b[k].Add(id);
        if (N == 0 || k < min) min = k;
        N++;
    }

    public (int id, int h) Pop()
    {
        while (head[min] == b[min].Len) { b[min].Len = 0; head[min] = 0; min++; }
        int k = min;
        int id = b[k].P[head[k]++];
        N--;
        int hd = head[k];
        if (hd >= 1 << 16 && hd * 2 >= b[k].Len)
        {
            int rest = b[k].Len - hd;
            Buffer.MemoryCopy(b[k].P + hd, b[k].P, rest * 4L, rest * 4L);
            b[k].Len = rest; head[k] = 0;
        }
        return (id, k);
    }

    public long Bytes { get { long t = (long)b.Length * 16 + (long)head.Length * 4; for (int i = 0; i < b.Length; i++) t += (long)b[i].Cap * 4; return t; } }
    public void Dispose() { for (int i = 0; i < b.Length; i++) b[i].Free(); b = []; head = []; N = 0; }
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

/// <summary>Type-based exploration (port of Go explore.Types): random non-empty type, then a random node in it; fixed-seed xorshift.</summary>
public sealed class TypeQueue
{
    readonly Dictionary<ulong, int> idx = new();
    readonly List<int[]?> dense = new();
    readonly List<List<(int id, int payload)>> nodes = new();
    readonly List<int> live = new();
    readonly List<int> pos = new();
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
            i = nodes.Count;
            if (h < 4096 && k < 256) dense[(int)h]![k] = i; else idx[key] = i;
            nodes.Add(new()); pos.Add(-1);
        }
        if (nodes[i].Count == 0) { pos[i] = live.Count; live.Add(i); }
        nodes[i].Add((id, payload));
        N++;
    }

    public bool Pop(out int id, out int payload)
    {
        id = payload = 0;
        if (live.Count == 0) return false;
        int i = live[(int)(Next() % (ulong)live.Count)];
        var b = nodes[i];
        int j = (int)(Next() % (ulong)b.Count);
        var e = b[j];
        b[j] = b[^1]; b.RemoveAt(b.Count - 1);
        N--;
        if (b.Count == 0)
        {
            int p = pos[i]; int last = live[^1];
            live[p] = last; pos[last] = p;
            live.RemoveAt(live.Count - 1);
            pos[i] = -1;
        }
        id = e.id; payload = e.payload;
        return true;
    }
    public long Bytes => (long)N * 8 + 64;
}
