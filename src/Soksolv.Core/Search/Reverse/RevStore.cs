using System.Numerics;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Soksolv.Search.Reverse;

/// <summary>
/// Reverse-search node store: one unmanaged record per node [header word | key words (box bitset over floor indices)]
/// plus an open-addressing table of packed slots (id+1 | fingerprint32&lt;&lt;32); the slot is a function of the
/// fingerprint, so growing never reads the records. Hashes are Zobrist (a pull changes two bits).
/// Header: bits 0-31 parent+1, 32-46 player cell, 47 / 63 pull direction (lo / hi bit), 48-62 floor index pulled from.
/// </summary>
public sealed unsafe class RevStore : IDisposable
{
    public readonly int Nw;
    public readonly RecordArena Rec;
    ulong* table; int tmask;
    readonly ulong[] zBox, zPlayer;
    public readonly int[] Fidx, Fcell;

    public RevStore(int cells, int[] fidx, int[] fcell)
    {
        Fidx = fidx; Fcell = fcell;
        Nw = (fcell.Length + 63) / 64;
        Rec = new RecordArena(8 * (1 + Nw));
        zBox = new ulong[Math.Max(1, fcell.Length)]; zPlayer = new ulong[cells];
        ulong s = 0x2545F4914F6CDD1DUL;
        ulong Next() { s += 0x9E3779B97F4A7C15UL; ulong z = s; z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9UL; z = (z ^ (z >> 27)) * 0x94D049BB133111EBUL; return z ^ (z >> 31); }
        for (int i = 0; i < zBox.Length; i++) zBox[i] = Next();
        for (int i = 0; i < cells; i++) zPlayer[i] = Next();
        AllocTable(1 << 12);
    }
    void AllocTable(int n) { table = (ulong*)NativeMemory.AllocZeroed((nuint)n * 8); tmask = n - 1; }
    public int Count => Rec.Count;
    public int TableSlots => tmask + 1;
    public long Bytes => Rec.Bytes + (long)(tmask + 1) * 8;

    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong ZFloor(int f) => zBox[f];
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong ZPlayer(int cell) => zPlayer[cell];
    public ulong HashOfBoxes(ReadOnlySpan<int> cells, int player)
    {
        ulong h = player >= 0 ? zPlayer[player] : 0;
        foreach (int c in cells) h ^= zBox[Fidx[c]];
        return h;
    }
    ulong HashOfRec(byte* r)
    {
        ulong h = zPlayer[Player(r)];
        ulong* k = (ulong*)(r + 8);
        for (int w = 0; w < Nw; w++)
            for (ulong x = k[w]; x != 0; x &= x - 1) h ^= zBox[w * 64 + BitOperations.TrailingZeroCount(x)];
        return h;
    }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong Spread(ulong h) { h ^= h >> 29; h *= 0xBF58476D1CE4E5B9UL; return h ^ (h >> 32); }

    [MethodImpl(MethodImplOptions.AggressiveInlining)] static int Player(byte* r) => (int)((*(ulong*)r >> 32) & 0x7FFF);

    public int Start(ulong zh, out uint fp) { ulong h = Spread(zh); fp = (uint)(h >> 32); return (int)fp & tmask; }
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong Entry(int i) => table[i];
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public int Next(int i) => (i + 1) & tmask;
    public bool KeyEquals(int id, ulong* key) => new ReadOnlySpan<ulong>(Rec.At(id) + 8, Nw).SequenceEqual(new ReadOnlySpan<ulong>(key, Nw));

    /// <summary>Finds a state; returns its id or -1 (slot/fp then say where to insert).</summary>
    public int Find(ulong* key, int player, ulong zh, out int slot, out uint fp)
    {
        ulong h = Spread(zh);
        fp = (uint)(h >> 32);
        int i = (int)fp & tmask;
        while (true)
        {
            ulong e = table[i];
            if (e == 0) { slot = i; return -1; }
            if ((uint)(e >> 32) == fp)
            {
                int id = (int)(uint)e - 1;
                byte* r = Rec.At(id);
                if (Player(r) == player && new ReadOnlySpan<ulong>(r + 8, Nw).SequenceEqual(new ReadOnlySpan<ulong>(key, Nw))) { slot = i; return id; }
            }
            i = (i + 1) & tmask;
        }
    }

    /// <summary>Appends a node record; with slot >= 0 also registers it in the table (otherwise: chain-only node).</summary>
    public int Append(int parent, int player, ulong* key, int fromF, int dir, int slot, uint fp)
    {
        int id = Rec.Alloc();
        byte* r = Rec.At(id);
        ulong hdr = (uint)(parent + 1) | ((ulong)(uint)player & 0x7FFF) << 32 | (ulong)(dir & 1) << 47 | (ulong)(fromF & 0x7FFF) << 48 | (ulong)((dir >> 1) & 1) << 63;
        *(ulong*)r = hdr;
        Buffer.MemoryCopy(key, r + 8, Nw * 8, Nw * 8);
        if (slot >= 0) table[slot] = (uint)(id + 1) | (ulong)fp << 32;
        return id;
    }
    public bool NeedsGrow => (long)Rec.Count * 4 >= (long)(tmask + 1) * 3;
    public long Load8 => (long)Rec.Count * 8;
    public bool OverSevenEighths => (long)Rec.Count * 8 >= (long)(tmask + 1) * 7;
    public void Grow()
    {
        var old = table; int n = tmask + 1;
        AllocTable(n * 2);
        for (int i = 0; i < n; i++)
        {
            ulong e = old[i];
            if (e == 0) continue;
            int j = (int)(uint)(e >> 32) & tmask;
            while (table[j] != 0) j = (j + 1) & tmask;
            table[j] = e;
        }
        NativeMemory.Free(old);
    }

    public int ParentOf(int id) => (int)(uint)*(ulong*)Rec.At(id) - 1;
    public int PlayerOf(int id) => Player(Rec.At(id));
    public ulong* Key(int id) => (ulong*)(Rec.At(id) + 8);
    /// <summary>The pull that created node id: box moved from floor index f in direction dir.</summary>
    public void Pulled(int id, out int fromF, out int dir)
    {
        ulong h = *(ulong*)Rec.At(id);
        fromF = (int)((h >> 48) & 0x7FFF);
        dir = (int)((h >> 47) & 1) | (int)((h >> 63) << 1);
    }
    public void Unpack(int id, Span<int> cur)
    {
        ulong* k = Key(id); int i = 0;
        for (int w = 0; w < Nw; w++)
            for (ulong x = k[w]; x != 0; x &= x - 1) cur[i++] = Fcell[w * 64 + BitOperations.TrailingZeroCount(x)];
    }
    public void Dispose() { Rec.Dispose(); if (table != null) { NativeMemory.Free(table); table = null; } }
    ~RevStore() => Dispose();
}
