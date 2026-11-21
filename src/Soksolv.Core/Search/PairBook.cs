using System.Runtime.InteropServices;

namespace Soksolv.Search;

/// <summary>
/// Width-2 novelty book of the macro lane: the set of unordered (cell, cell) pairs seen per FESS cell, stored transposed as one bitset row per
/// (cell bucket, cell): row(bk, c) has bit floorIndex(x) set when the pair {c, x} was recorded in bucket bk. A candidate's test "is every pair
/// (target, other box) already seen?" becomes one bit test per box in rows that stay cache resident, instead of one hash probe per box.
/// Rows are chunked unmanaged memory (never moved, so pointers stay valid).
/// </summary>
internal sealed unsafe class PairBook : IDisposable
{
    readonly int nw;                     // words per row
    struct Slot { public ulong Key; public ulong* Row; }
    Slot* tab; int mask, count;
    readonly List<nint> chunks = new();
    readonly int rowsPerChunk; int usedInChunk;
    ulong* cur;

    public PairBook(int words)
    {
        nw = Math.Max(1, words);
        rowsPerChunk = Math.Max(1, (1 << 16) / (nw * 8));
        usedInChunk = rowsPerChunk;
        AllocTab(1 << 10);
    }
    public long Bytes => ((long)mask + 1) * 16 + (long)chunks.Count * rowsPerChunk * nw * 8;
    public int Rows => count;

    void AllocTab(int slots) { tab = (Slot*)NativeMemory.AllocZeroed((nuint)slots * (nuint)sizeof(Slot)); mask = slots - 1; }
    static int H(ulong k, int mask) => (int)((k * 0x9E3779B97F4A7C15UL) >> 36) & mask;

    /// <summary>The row of (bk, cell) or null. key = bk << 20 | cell (never 0: cells of the padded grid start at W + 1).</summary>
    public ulong* Find(ulong key)
    {
        Slot* t = tab; int m = mask;
        for (int i = H(key, m); ; i = (i + 1) & m)
        {
            ulong k = t[i].Key;
            if (k == key) return t[i].Row;
            if (k == 0) return null;
        }
    }

    public ulong* GetOrAdd(ulong key)
    {
        Slot* t = tab; int m = mask;
        int i = H(key, m);
        for (; ; i = (i + 1) & m)
        {
            ulong k = t[i].Key;
            if (k == key) return t[i].Row;
            if (k == 0) break;
        }
        if ((count + 1) * 2 > m + 1) { Grow(); return GetOrAdd(key); }
        if (usedInChunk == rowsPerChunk)
        {
            chunks.Add((nint)NativeMemory.AllocZeroed((nuint)(rowsPerChunk * nw * 8)));
            usedInChunk = 0; cur = (ulong*)chunks[^1];
        }
        ulong* row = cur + (long)usedInChunk * nw; usedInChunk++;
        t[i].Key = key; t[i].Row = row; count++;
        return row;
    }

    void Grow()
    {
        Slot* old = tab; int oldSlots = mask + 1;
        AllocTab(oldSlots * 2);
        for (int j = 0; j < oldSlots; j++)
        {
            if (old[j].Key == 0) continue;
            int i = H(old[j].Key, mask);
            while (tab[i].Key != 0) i = (i + 1) & mask;
            tab[i] = old[j];
        }
        NativeMemory.Free(old);
    }

    public void Dispose()
    {
        if (tab != null) { NativeMemory.Free(tab); tab = null; }
        foreach (var c in chunks) NativeMemory.Free((void*)c);
        chunks.Clear();
        GC.SuppressFinalize(this);
    }
    ~PairBook() { if (tab != null) NativeMemory.Free(tab); foreach (var c in chunks) NativeMemory.Free((void*)c); }
}
