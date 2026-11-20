using System.Numerics;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Soksolv.Bench.Explore;

public interface IStateStore : IDisposable
{
    string Name { get; }
    /// <summary>true if the state was new.</summary>
    bool Insert(ReadOnlySpan<ushort> boxes, ushort player, int parent);
    int Count { get; }
    long BytesReserved { get; }
}

static class H
{
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static ulong Mix(ulong h, ulong v) { h = (h ^ v) * 0xBF58476D1CE4E5B9UL; return h ^ (h >> 31); }
}

/// <summary>V1 — mirrors the Go layout: managed SoA arrays that double on growth, bitset key, int32 table + separate uint32 fingerprints.</summary>
public sealed class GoLikeStore : IStateStore
{
    readonly int nw, floors;
    ulong[] boxes = new ulong[1 << 10];
    int[] player = new int[1 << 8], parent = new int[1 << 8], pushFrom = new int[1 << 8], g = new int[1 << 8], w = new int[1 << 8], mvStart = new int[1 << 8];
    sbyte[] pushDir = new sbyte[1 << 8], reg = new sbyte[1 << 8];
    ushort[] bucket = new ushort[1 << 8], mvLen = new ushort[1 << 8], mvNext = new ushort[1 << 8], mvLeft = new ushort[1 << 8];
    int[] table = new int[1 << 12]; uint[] tfp = new uint[1 << 12];
    int n;
    readonly ulong[] key;
    public GoLikeStore(int floors) { this.floors = floors; nw = (floors + 63) / 64; key = new ulong[nw]; Array.Fill(table, -1); }
    public string Name => "V1 go-like managed SoA, bitset key, int32 table + uint32 fp";
    public int Count => n;
    public long BytesReserved => boxes.Length * 8L + (long)player.Length * (4 * 6 + 2 * 2 + 4 * 0) + (long)player.Length * (1 + 1 + 2 * 4) + table.Length * 8L;
    static ulong Hash(ReadOnlySpan<ulong> k, int p) { ulong h = (ulong)p * 0x9E3779B97F4A7C15UL + 1; foreach (var b in k) h = H.Mix(h, b); return h; }
    int Find(ulong h, int p, out uint fp)
    {
        int mask = table.Length - 1; fp = (uint)(h >> 32);
        for (int i = (int)h & mask; ; i = (i + 1) & mask)
        {
            int id = table[i];
            if (id < 0 || tfp[i] == fp && player[id] == p && key.AsSpan().SequenceEqual(boxes.AsSpan(id * nw, nw))) return i;
        }
    }
    public bool Insert(ReadOnlySpan<ushort> bx, ushort pl, int par)
    {
        Array.Clear(key);
        foreach (var b in bx) key[b >> 6] |= 1UL << (b & 63);
        ulong h = Hash(key, pl);
        int slot = Find(h, pl, out uint fp);
        if (table[slot] >= 0) return false;
        if (n == player.Length)
        {
            int m = n * 2;
            Array.Resize(ref player, m); Array.Resize(ref parent, m); Array.Resize(ref pushFrom, m); Array.Resize(ref g, m); Array.Resize(ref w, m); Array.Resize(ref mvStart, m);
            Array.Resize(ref pushDir, m); Array.Resize(ref reg, m); Array.Resize(ref bucket, m); Array.Resize(ref mvLen, m); Array.Resize(ref mvNext, m); Array.Resize(ref mvLeft, m);
        }
        if ((n + 1) * nw > boxes.Length) Array.Resize(ref boxes, boxes.Length * 2);
        key.CopyTo(boxes, n * nw);
        player[n] = pl; parent[n] = par;
        table[slot] = n; tfp[slot] = fp; n++;
        if (n * 4 >= table.Length * 3)
        {
            table = new int[table.Length * 2]; tfp = new uint[table.Length]; Array.Fill(table, -1);
            for (int id = 0; id < n; id++)
            {
                var k = boxes.AsSpan(id * nw, nw);
                ulong hh = Hash(k, player[id]);
                int mask = table.Length - 1; int i = (int)hh & mask;
                while (table[i] >= 0) i = (i + 1) & mask;
                table[i] = id; tfp[i] = (uint)(hh >> 32);
            }
        }
        return true;
    }
    public void Dispose() { }
}

/// <summary>Unmanaged chunked arena of fixed-size records (no growth copies, no GC pressure).</summary>
public unsafe sealed class ChunkArena : IDisposable
{
    const int Shift = 16;
    readonly int rec; readonly List<IntPtr> chunks = new();
    public ChunkArena(int recordBytes) { rec = recordBytes; }
    public long Bytes => (long)chunks.Count * (1 << Shift) * rec;
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public byte* Get(int id) => (byte*)chunks[id >> Shift] + (long)(id & ((1 << Shift) - 1)) * rec;
    public void EnsureFor(int id) { while ((id >> Shift) >= chunks.Count) chunks.Add((IntPtr)NativeMemory.AllocZeroed((nuint)((1L << Shift) * rec))); }
    public void Dispose() { foreach (var c in chunks) NativeMemory.Free((void*)c); chunks.Clear(); }
}

/// <summary>V2 — unmanaged chunked AoS records (key inline), packed ulong table slots (fp32|id32+1), bitset key.</summary>
public unsafe sealed class PackedBitsetStore : IStateStore
{
    // record: [key nw*8][parent i32][g i32][player u16][pushFrom u16][w u16][meta u16 (dir2|reg7)][bucket u16][pad]
    readonly int nw, rec; readonly ChunkArena arena;
    ulong* table; int tmask; int n;
    readonly ulong* key;
    public PackedBitsetStore(int floors)
    {
        nw = (floors + 63) / 64; rec = nw * 8 + 4 + 4 + 2 * 5; rec = (rec + 7) & ~7;
        arena = new ChunkArena(rec); key = (ulong*)NativeMemory.AllocZeroed((nuint)(nw * 8));
        AllocTable(1 << 12);
    }
    void AllocTable(int size) { table = (ulong*)NativeMemory.AllocZeroed((nuint)size * 8); tmask = size - 1; }
    public string Name => "V2 unmanaged chunked AoS (key inline), packed ulong slots, bitset key";
    public int Count => n;
    public long BytesReserved => arena.Bytes + (long)(tmask + 1) * 8;
    ulong Hash(ulong* k, int p) { ulong h = (ulong)p * 0x9E3779B97F4A7C15UL + 1; for (int i = 0; i < nw; i++) h = H.Mix(h, k[i]); return h; }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    bool Eq(byte* r, ulong* k, ushort p)
    {
        if (*(ushort*)(r + nw * 8 + 8) != p) return false;
        return new ReadOnlySpan<ulong>(r, nw).SequenceEqual(new ReadOnlySpan<ulong>(k, nw));
    }
    public bool Insert(ReadOnlySpan<ushort> bx, ushort pl, int par)
    {
        new Span<ulong>(key, nw).Clear();
        foreach (var b in bx) key[b >> 6] |= 1UL << (b & 63);
        ulong h = Hash(key, pl); uint fp = (uint)(h >> 32) | 0; 
        int i = (int)h & tmask;
        while (true)
        {
            ulong s = table[i];
            if (s == 0) break;
            if ((uint)(s >> 32) == fp && Eq(arena.Get((int)(uint)s - 1), key, pl)) return false;
            i = (i + 1) & tmask;
        }
        arena.EnsureFor(n);
        byte* r = arena.Get(n);
        Buffer.MemoryCopy(key, r, nw * 8, nw * 8);
        *(int*)(r + nw * 8) = par; *(ushort*)(r + nw * 8 + 8) = pl;
        table[i] = ((ulong)fp << 32) | (uint)(n + 1); n++;
        if (n * 4 >= (tmask + 1) * 3) Grow();
        return true;
    }
    void Grow()
    {
        var old = table; int oldSize = tmask + 1;
        AllocTable(oldSize * 2);
        for (int id = 0; id < n; id++)
        {
            byte* r = arena.Get(id);
            ulong h = Hash((ulong*)r, *(ushort*)(r + nw * 8 + 8));
            int i = (int)h & tmask;
            while (table[i] != 0) i = (i + 1) & tmask;
            table[i] = ((ulong)(uint)(h >> 32) << 32) | (uint)(id + 1);
        }
        NativeMemory.Free(old);
    }
    public void Dispose() { arena.Dispose(); NativeMemory.Free(table); NativeMemory.Free(key); }
}

/// <summary>V3 — like V2 but the key is the sorted list of box floor indices (ushort each) when that is smaller than the bitset.</summary>
public unsafe sealed class PackedListStore : IStateStore
{
    readonly int nb, rec; readonly ChunkArena arena;
    ulong* table; int tmask; int n;
    public PackedListStore(int boxes)
    {
        nb = boxes; rec = nb * 2 + 2 /*player*/ + 4 /*parent*/ + 4 /*g*/ + 2 * 4; rec = (rec + 3) & ~3;
        arena = new ChunkArena(rec); AllocTable(1 << 12);
    }
    void AllocTable(int size) { table = (ulong*)NativeMemory.AllocZeroed((nuint)size * 8); tmask = size - 1; }
    public string Name => "V3 unmanaged chunked AoS, packed ulong slots, sorted ushort-list key";
    public int Count => n;
    public long BytesReserved => arena.Bytes + (long)(tmask + 1) * 8;
    static ulong Hash(ushort* k, int nb, int p)
    {
        // read 4 ushorts at a time
        ulong h = (ulong)p * 0x9E3779B97F4A7C15UL + 1; int i = 0;
        for (; i + 4 <= nb; i += 4) h = H.Mix(h, *(ulong*)(k + i));
        ulong tail = 0; for (int j = 0; i + j < nb; j++) tail |= (ulong)k[i + j] << (16 * j);
        return H.Mix(h, tail ^ ((ulong)nb << 56));
    }
    public bool Insert(ReadOnlySpan<ushort> bx, ushort pl, int par)
    {
        fixed (ushort* k = bx)
        {
            ulong h = Hash(k, nb, pl); uint fp = (uint)(h >> 32);
            int i = (int)h & tmask;
            while (true)
            {
                ulong s = table[i];
                if (s == 0) break;
                if ((uint)(s >> 32) == fp)
                {
                    byte* r = arena.Get((int)(uint)s - 1);
                    if (*(ushort*)(r + nb * 2) == pl && new ReadOnlySpan<ushort>(r, nb).SequenceEqual(bx.Slice(0, nb))) return false;
                }
                i = (i + 1) & tmask;
            }
            arena.EnsureFor(n);
            byte* rr = arena.Get(n);
            Buffer.MemoryCopy(k, rr, nb * 2, nb * 2);
            *(ushort*)(rr + nb * 2) = pl; *(int*)(rr + nb * 2 + 2) = par;
            table[i] = ((ulong)fp << 32) | (uint)(n + 1); n++;
            if (n * 4 >= (tmask + 1) * 3) Grow();
            return true;
        }
    }
    void Grow()
    {
        var old = table; int oldSize = tmask + 1; AllocTable(oldSize * 2);
        for (int id = 0; id < n; id++)
        {
            byte* r = arena.Get(id);
            ulong h = Hash((ushort*)r, nb, *(ushort*)(r + nb * 2));
            int i = (int)h & tmask; while (table[i] != 0) i = (i + 1) & tmask;
            table[i] = ((ulong)(uint)(h >> 32) << 32) | (uint)(id + 1);
        }
        NativeMemory.Free(old);
    }
    public void Dispose() { arena.Dispose(); NativeMemory.Free(table); }
}
