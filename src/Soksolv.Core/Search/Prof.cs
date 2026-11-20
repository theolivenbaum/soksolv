using System.Diagnostics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search;

/// <summary>Section timers for diagnostics (SOKSOLV_PROF=1). When off, the static readonly check is folded away by the JIT.</summary>
public static class Prof
{
    public static readonly bool On = Environment.GetEnvironmentVariable("SOKSOLV_PROF") == "1";
    public static readonly long[] Ticks = new long[16];
    public static readonly long[] Calls = new long[16];
    public static readonly string[] Names = ["cluster", "regions", "frozen2x2+freeze", "goalarea", "pair", "piCorral", "normalize/reach", "add", "open(total)", "apply(total)", "feasible", "macro", "unpack/setcosts", "cl.searches", "cl.states", "cl.floods"];
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public static long Start() => On ? Stopwatch.GetTimestamp() : 0;
    [MethodImpl(MethodImplOptions.AggressiveInlining)] public static void Stop(int slot, long t0) { if (On) { Ticks[slot] += Stopwatch.GetTimestamp() - t0; Calls[slot]++; } }
    public static void Report(double totalMs)
    {
        if (!On) return;
        for (int i = 0; i < Names.Length; i++)
            if (Calls[i] > 0) Console.Error.WriteLine($"prof {Names[i],-18} {Ticks[i] * 1000.0 / Stopwatch.Frequency,9:F0} ms {Ticks[i] * 100.0 / Stopwatch.Frequency / totalMs,5:F1}%  calls {Calls[i],10}  {Ticks[i] * 1e9 / Stopwatch.Frequency / Calls[i],7:F0} ns/call");
    }
}
