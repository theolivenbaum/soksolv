using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Soksolv.Search;

/// <summary>
/// Node store: unmanaged AoS records [key words | fields] + open-addressing table of packed slots
/// (fingerprint32 | id+1). Keys are bitsets over compacted floor indices; hashes are Zobrist (incremental).
/// </summary>
public sealed unsafe class StateTable : IDisposable
{
    // field offsets after the key words
    public readonly int Nw;
    readonly int fo; // offset of fields
    public readonly RecordArena Rec;
    ulong* table; int tmask;
    readonly ulong[] zBox;      // per floor index
    readonly ulong[] zPlayer;   // per cell
    public readonly int[] Fidx, Fcell;
    public long Duplicates;

    // record fields (after key): parent i32, g i32, w i32, mvIdx i32, player u16, pushFrom u16, bucket u16, pushDir i8, reg i8
    const int OParent = 0, OG = 4, OW = 8, OMv = 12, OPlayer = 16, OFrom = 18, OBucket = 20, ODir = 22, OReg = 23, FieldBytes = 24;

    public StateTable(bool[] wall)
    {
        int n = wall.Length;
        Fidx = new int[n];
        var fc = new List<int>();
        for (int c = 0; c < n; c++) { Fidx[c] = -1; if (!wall[c]) { Fidx[c] = fc.Count; fc.Add(c); } }
        Fcell = [.. fc];
        Nw = (Fcell.Length + 63) / 64;
        fo = Nw * 8;
        Rec = new RecordArena(Nw * 8 + FieldBytes);
        zBox = new ulong[Fcell.Length]; zPlayer = new ulong[n];
        ulong s = 0x2545F4914F6CDD1DUL;
        ulong Next() { s += 0x9E3779B97F4A7C15UL; ulong z = s; z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9UL; z = (z ^ (z >> 27)) * 0x94D049BB133111EBUL; return z ^ (z >> 31); }
        for (int i = 0; i < zBox.Length; i++) zBox[i] = Next();
        for (int i = 0; i < n; i++) zPlayer[i] = Next();
        AllocTable(1 << 12);
    }

    void AllocTable(int size) { table = (ulong*)NativeMemory.AllocZeroed((nuint)size * 8); tmask = size - 1; }
    public int Count => Rec.Count;
    public long Bytes => Rec.Bytes + (long)(tmask + 1) * 8;
    public int TableSlots => tmask + 1;

    // ---- hashing -----------------------------------------------------------------------------------
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong ZBox(int cell) => zBox[Fidx[cell]];
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong ZPlayer(int cell) => zPlayer[cell];

    public ulong HashOf(ReadOnlySpan<int> boxCells, int player)
    {
        ulong h = zPlayer[player];
        foreach (int b in boxCells) h ^= zBox[Fidx[b]];
        return h;
    }
    ulong HashOfKey(byte* r)
    {
        ulong h = zPlayer[*(ushort*)(r + fo + OPlayer)];
        ulong* k = (ulong*)r;
        for (int w = 0; w < Nw; w++)
            for (ulong x = k[w]; x != 0; x &= x - 1) h ^= zBox[w * 64 + System.Numerics.BitOperations.TrailingZeroCount(x)];
        return h;
    }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong Spread(ulong h) { h ^= h >> 29; h *= 0xBF58476D1CE4E5B9UL; return h ^ (h >> 32); }

    // ---- lookup / insert ----------------------------------------------------------------------------
    /// <summary>Finds the state (key words, player, zobrist hash). Returns the node id or -1; slot/fp for insertion.</summary>
    public int Find(ulong* key, int player, ulong zhash, out int slot, out uint fp)
    {
        ulong h = Spread(zhash);
        fp = (uint)(h >> 32);
        int i = (int)h & tmask;
        while (true)
        {
            ulong s = table[i];
            if (s == 0) { slot = i; return -1; }
            if ((uint)(s >> 32) == fp)
            {
                int id = (int)(uint)s - 1;
                byte* r = Rec.At(id);
                if (*(ushort*)(r + fo + OPlayer) == player && new ReadOnlySpan<ulong>(r, Nw).SequenceEqual(new ReadOnlySpan<ulong>(key, Nw))) { slot = i; return id; }
            }
            i = (i + 1) & tmask;
        }
    }

    /// <summary>Appends a node at a slot returned by Find (key copied). Grows the table when 3/4 full.</summary>
    public int Insert(int slot, uint fp, ulong* key, int player)
    {
        int id = Rec.Alloc();
        byte* r = Rec.At(id);
        Buffer.MemoryCopy(key, r, Nw * 8, Nw * 8);
        *(ushort*)(r + fo + OPlayer) = (ushort)player;
        *(int*)(r + fo + OMv) = -1;
        table[slot] = ((ulong)fp << 32) | (uint)(id + 1);
        return id;
    }
    public bool NeedsGrow => (long)Rec.Count * 4 >= (long)(tmask + 1) * 3;
    public void Grow()
    {
        var old = table;
        AllocTable((tmask + 1) * 2);
        for (int id = 0; id < Rec.Count; id++)
        {
            ulong h = Spread(HashOfKey(Rec.At(id)));
            int i = (int)h & tmask;
            while (table[i] != 0) i = (i + 1) & tmask;
            table[i] = (h >> 32 << 32) | (uint)(id + 1);
        }
        NativeMemory.Free(old);
    }

    // ---- field access ---------------------------------------------------------------------------------
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public ulong* Key(int id) => (ulong*)Rec.At(id);
    [MethodImpl(MethodImplOptions.AggressiveInlining)] byte* F(int id) => Rec.At(id) + fo;
    public ref int Parent(int id) => ref *(int*)(F(id) + OParent);
    public ref int G(int id) => ref *(int*)(F(id) + OG);
    public ref int W(int id) => ref *(int*)(F(id) + OW);
    public ref int MvIdx(int id) => ref *(int*)(F(id) + OMv);
    public int Player(int id) => *(ushort*)(F(id) + OPlayer);
    public ref ushort From(int id) => ref *(ushort*)(F(id) + OFrom);
    public ref ushort Bucket(int id) => ref *(ushort*)(F(id) + OBucket);
    public ref sbyte Dir(int id) => ref *(sbyte*)(F(id) + ODir);
    public ref sbyte Reg(int id) => ref *(sbyte*)(F(id) + OReg);

    /// <summary>Fills cur with node id's box cells ascending.</summary>
    public void Unpack(int id, Span<int> cur)
    {
        ulong* k = Key(id); int i = 0;
        for (int w = 0; w < Nw; w++)
            for (ulong x = k[w]; x != 0; x &= x - 1) cur[i++] = Fcell[w * 64 + System.Numerics.BitOperations.TrailingZeroCount(x)];
    }

    public void Dispose() { Rec.Dispose(); if (table != null) { NativeMemory.Free(table); table = null; } }
    ~StateTable() => Dispose();
}
