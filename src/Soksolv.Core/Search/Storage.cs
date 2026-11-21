using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Soksolv.Search;

/// <summary>Append-only list of unmanaged items in fixed 64K-item chunks: growing never copies, lives off the GC heap.</summary>
public sealed unsafe class ChunkList<T> : IDisposable where T : unmanaged
{
    const int Shift = 16, Per = 1 << Shift, Mask = Per - 1;
    nint[] chunks = new nint[16];
    int nChunks;
    public int Count { get; private set; }
    public long Bytes => (long)nChunks * Per * sizeof(T);

    public ref T this[int i] { [MethodImpl(MethodImplOptions.AggressiveInlining)] get => ref ((T*)chunks[i >> Shift])[i & Mask]; }

    public int Add(in T v)
    {
        if ((Count >> Shift) == nChunks)
        {
            if (nChunks == chunks.Length) Array.Resize(ref chunks, nChunks * 2);
            chunks[nChunks++] = (nint)NativeMemory.AllocZeroed((nuint)(Per * sizeof(T)));
        }
        int id = Count++;
        ((T*)chunks[id >> Shift])[id & Mask] = v;
        return id;
    }
    /// <summary>Forgets every element but keeps the allocated chunks (Go s.x = s.x[:0]).</summary>
    public void Clear() => Count = 0;
    public void Dispose() { for (int i = 0; i < nChunks; i++) NativeMemory.Free((void*)chunks[i]); nChunks = 0; Count = 0; }
    ~ChunkList() => Dispose();
}

/// <summary>Fixed-size byte records in 64K-record chunks (key words inline with node fields).</summary>
public sealed unsafe class RecordArena : IDisposable
{
    const int Shift = 16, Per = 1 << Shift, Mask = Per - 1;
    public readonly int RecBytes;
    nint[] chunks = new nint[16];
    int nChunks;
    public int Count { get; private set; }
    public RecordArena(int recBytes) { RecBytes = recBytes; }
    public long Bytes => (long)nChunks * Per * RecBytes;
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public byte* At(int id) => (byte*)chunks[id >> Shift] + (long)(id & Mask) * RecBytes;
    /// <summary>Reserves the next record (zeroed) and returns its id.</summary>
    public int Alloc()
    {
        if ((Count >> Shift) == nChunks)
        {
            if (nChunks == chunks.Length) Array.Resize(ref chunks, nChunks * 2);
            chunks[nChunks++] = (nint)NativeMemory.AllocZeroed((nuint)((long)Per * RecBytes));
        }
        return Count++;
    }
    public void Dispose() { for (int i = 0; i < nChunks; i++) NativeMemory.Free((void*)chunks[i]); nChunks = 0; Count = 0; }
    ~RecordArena() => Dispose();
}

/// <summary>Binary min-heap of 64-bit keys in unmanaged memory (realloc growth).</summary>
public sealed unsafe class KeyHeap : IDisposable
{
    ulong* a; int n, cap;
    public int Count => n;
    public long Bytes => (long)cap * 8;
    public void Push(ulong k)
    {
        if (n == cap) { cap = cap == 0 ? 16 : cap * 2; a = (ulong*)NativeMemory.Realloc(a, (nuint)cap * 8); }
        int i = n++;
        while (i > 0)
        {
            int p = (i - 1) >> 1;
            if (k >= a[p]) break;
            a[i] = a[p]; i = p;
        }
        a[i] = k;
    }
    public ulong Pop()
    {
        ulong top = a[0];
        ulong x = a[--n];
        int i = 0;
        while (true)
        {
            int l = 2 * i + 1;
            if (l >= n) break;
            int m = l;
            if (l + 1 < n && a[l + 1] < a[l]) m = l + 1;
            if (a[m] >= x) break;
            a[i] = a[m]; i = m;
        }
        if (n > 0) a[i] = x;
        return top;
    }
    public void Dispose() { NativeMemory.Free(a); a = null; n = cap = 0; }
    ~KeyHeap() => Dispose();
}
