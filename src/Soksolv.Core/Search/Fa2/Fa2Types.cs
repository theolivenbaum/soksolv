using System.Runtime.CompilerServices;

namespace Soksolv.Search;

/// <summary>Go FA2* settings for the fa2 lane (defaults as in Go; each flag exists so parity can be checked piecewise).</summary>
public sealed class Fa2Options
{
    public bool SmallFree = true, Opener = true, NoReturn = true, VerifyAdv = true, OopTail = true, Explorer = true,
        Rooms = true, GoalHot = true, Dist = true, Corral = true, Hall = true, HotSides = true, Mobility = true,
        HallSide = true, HallSink = true;
    public int OopAdv = 2, PlanExp = 4096, EdgeWin = 15;
    public string Plan = "v2";   // v2 (exp-411 planner), v2ng, old (macro sink planner as event list)
    public bool Debug;
}

/// <summary>One scored child of an expanded node, 16 bytes (Go fa2Edge).</summary>
public struct Fa2Edge
{
    public ulong Key;        // feature key (smaller is better)
    public ushort B, T;      // moved box: origin and destination cells
    public ushort Np;        // the child's normalised player cell
    public byte Side;        // direction of the last push
    public byte Wadv;        // bit 0: weight (0 advised, 1 otherwise); bits 1..7: advisors
    public readonly int Dir => Side & 3;          // push direction index
    public readonly bool Verified => (Side & 0x80) != 0;  // the late cluster/dead-zone proof already passed for this child at expansion
    public readonly int W => Wadv & 1;
    public readonly byte Adv => (byte)(Wadv >> 1);
}

/// <summary>Per-node fa2 data (the node's key, player, parent... live in the shared StateTable records).</summary>
public struct Fa2Aux
{
    public ulong Score, Best, BoxH;
    public int W, EOff, ELen, ECur, EBase, ETot;
    public byte Adv;
}

public struct Fa2HE { public int W, Id; public ulong Key; }

/// <summary>Open-addressing set of uint64 keys (k+1 stored, 0 = empty); same layout as Go u64set.</summary>
public sealed class U64Set
{
    ulong[] slots = [];
    int n;
    public long Bytes => slots.Length * 8L;
    public bool Insert(ulong k)
    {
        k++;
        if (n * 4 >= slots.Length * 3)
        {
            var old = slots;
            slots = new ulong[Math.Max(1024, 2 * old.Length)];
            n = 0;
            foreach (ulong v in old) if (v != 0) Put(v);
        }
        return Put(k);
    }
    bool Put(ulong k)
    {
        ulong mask = (ulong)(slots.Length - 1);
        ulong i = (k * 0x9E3779B97F4A7C15UL) >> 32 & mask;
        while (true)
        {
            ulong v = slots[i];
            if (v == 0) { slots[i] = k; n++; return true; }
            if (v == k) return false;
            i = (i + 1) & mask;
        }
    }
    public bool Has(ulong k)
    {
        if (slots.Length == 0) return false;
        k++;
        ulong mask = (ulong)(slots.Length - 1);
        ulong i = (k * 0x9E3779B97F4A7C15UL) >> 32 & mask;
        while (true)
        {
            ulong v = slots[i];
            if (v == 0) return false;
            if (v == k) return true;
            i = (i + 1) & mask;
        }
    }
}
