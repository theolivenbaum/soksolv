using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Runtime.Intrinsics.X86;

namespace Soksolv.Search;

/// <summary>Open-addressing set of ulong keys on unmanaged memory (8 B per slot, load factor &lt;= 1/2, no per-entry allocation); replaces HashSet&lt;ulong&gt; in the novelty / group-exit books.</summary>
internal sealed unsafe class FlatSet64 : IDisposable
{
    ulong* t; int mask; int count; bool hasZero;
    public FlatSet64(int initialSlots = 1 << 12) { Alloc(initialSlots); }
    void Alloc(int slots) { t = (ulong*)NativeMemory.AllocZeroed((nuint)slots * 8); mask = slots - 1; }
    public long Bytes => ((long)mask + 1) * 8;
    public int Count => count + (hasZero ? 1 : 0);
    [MethodImpl(MethodImplOptions.AggressiveInlining)] int Slot(ulong k) => (int)((k * 0x9E3779B97F4A7C15UL) >> 36) & mask;

    /// <summary>Prefetches the slot of k (call before a batch of Contains/Add probes).</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public void Prefetch(ulong k) { if (Sse.IsSupported) Sse.Prefetch0(t + Slot(k)); }

    public bool Contains(ulong k)
    {
        if (k == 0) return hasZero;
        ulong* p = t; int m = mask;
        for (int i = Slot(k); ; i = (i + 1) & m)
        {
            ulong s = p[i];
            if (s == k) return true;
            if (s == 0) return false;
        }
    }

    public bool Add(ulong k)
    {
        if (k == 0) { bool had = hasZero; hasZero = true; return !had; }
        if ((count + 1) * 2 > mask + 1) Grow();
        ulong* p = t; int m = mask;
        for (int i = Slot(k); ; i = (i + 1) & m)
        {
            ulong s = p[i];
            if (s == k) return false;
            if (s == 0) { p[i] = k; count++; return true; }
        }
    }

    void Grow()
    {
        ulong* old = t; int oldSlots = mask + 1;
        Alloc(oldSlots * 2);
        for (int j = 0; j < oldSlots; j++)
        {
            ulong k = old[j];
            if (k == 0) continue;
            int i = Slot(k);
            while (t[i] != 0) i = (i + 1) & mask;
            t[i] = k;
        }
        NativeMemory.Free(old);
    }

    public void Dispose() { if (t != null) { NativeMemory.Free(t); t = null; } GC.SuppressFinalize(this); }
    ~FlatSet64() { if (t != null) NativeMemory.Free(t); }
}
