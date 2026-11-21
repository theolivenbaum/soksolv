using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;

namespace Soksolv.Diagnostics;

/// <summary>
/// Header printed at the start of every benchmark run: runs may be scheduled on different VMs, so
/// CPU, memory, runtime and a fixed calibration score are recorded to make results comparable.
/// </summary>
public static class MachineInfo
{
    public static string Cpu()
    {
        try
        {
            foreach (var l in File.ReadLines("/proc/cpuinfo"))
                if (l.StartsWith("model name")) return l[(l.IndexOf(':') + 1)..].Trim();
        }
        catch { }
        return RuntimeInformation.ProcessArchitecture.ToString();
    }

    public static long TotalMemoryMb()
    {
        try
        {
            foreach (var l in File.ReadLines("/proc/meminfo"))
                if (l.StartsWith("MemTotal:")) return long.Parse(l.Split(' ', StringSplitOptions.RemoveEmptyEntries)[1]) / 1024;
        }
        catch { }
        return GC.GetGCMemoryInfo().TotalAvailableMemoryBytes / (1024 * 1024);
    }

    /// <summary>
    /// Fixed single-thread calibration: a dependent pseudo-random walk over a 64 MiB table (cache/memory
    /// latency dominated, like hash-table-heavy search) plus a popcount/ALU loop. Returns Mops/s of the
    /// combined loop (higher = faster machine). Deterministic work, ~0.3 s.
    /// </summary>
    public static (double alu, double mem) Calibrate()
    {
        const int N = 1 << 24; // 16M * 4 B = 64 MiB
        var t = new uint[N];
        uint x = 12345;
        for (int i = 0; i < N; i++) { x ^= x << 13; x ^= x >> 17; x ^= x << 5; t[i] = x; }
        long sw = Stopwatch.GetTimestamp();
        ulong acc = 0, s = 88172645463325252UL;
        const int AluIters = 100_000_000;
        for (int i = 0; i < AluIters; i++)
        {
            s ^= s << 13; s ^= s >> 7; s ^= s << 17;
            acc += (ulong)System.Numerics.BitOperations.PopCount(s);
        }
        double aluSec = Stopwatch.GetElapsedTime(sw).TotalSeconds;
        sw = Stopwatch.GetTimestamp();
        uint p = 1;
        const int MemIters = 10_000_000;
        for (int i = 0; i < MemIters; i++) p = t[(p ^ (uint)i * 2654435761u) & (N - 1)] + p;
        double memSec = Stopwatch.GetElapsedTime(sw).TotalSeconds;
        GC.KeepAlive(t);
        if (acc == 42 && p == 42) Console.Write(""); // keep results live
        return (AluIters / aluSec / 1e6, MemIters / memSec / 1e6);
    }

    public static string Header(bool calibrate = true)
    {
        var sb = new StringBuilder();
        sb.AppendLine("=== machine ===");
        sb.AppendLine($"cpu:     {Cpu()}");
        sb.AppendLine($"cores:   {Environment.ProcessorCount} logical");
        sb.AppendLine($"memory:  {TotalMemoryMb()} MiB");
        sb.AppendLine($"os:      {RuntimeInformation.OSDescription}");
        sb.AppendLine($"runtime: {RuntimeInformation.FrameworkDescription}  simd: {(System.Runtime.Intrinsics.Vector512.IsHardwareAccelerated ? "avx512" : System.Runtime.Intrinsics.Vector256.IsHardwareAccelerated ? "avx2" : System.Runtime.Intrinsics.Vector128.IsHardwareAccelerated ? "sse/neon" : "none")}");
        if (calibrate)
        {
            Calibrate(); // warm-up/JIT
            var (alu, mem) = Calibrate();
            sb.AppendLine($"calib:   alu {alu:F0} Mop/s, mem-latency {mem:F1} Mop/s");
        }
        return sb.ToString();
    }
}
