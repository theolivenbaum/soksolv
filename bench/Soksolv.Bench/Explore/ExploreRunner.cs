using System.Diagnostics;

namespace Soksolv.Bench.Explore;

public static class ExploreRunner
{
    public static void Run(string dir, int maxStates)
    {
        var files = Directory.GetFiles(dir, "*.xsb").OrderBy(f => f).ToArray();
        Console.WriteLine($"\n=== harvesting up to {maxStates} states per level from {files.Length} levels ===");
        var hs = new List<Harvest>();
        foreach (var f in files)
        {
            var sw = Stopwatch.StartNew();
            var h = Harvest.Run(f, maxStates);
            Console.WriteLine($"{h.Name,-34} floors={h.Floors,4} boxes={h.NBoxes,2} states={h.States.Count,7} ({sw.ElapsedMilliseconds} ms)");
            hs.Add(h);
        }
        StoreBench(hs);
        FloodBench(hs);
        PushGenBench(hs);
        Console.WriteLine("\n=== child key+hash construction: rebuild vs incremental (synthetic sizes) ===");
        foreach (var (f, b) in new[] { (50, 10), (112, 15), (256, 20), (1024, 40), (4096, 120) }) HashBench.Run(f, b);
    }

    static void StoreBench(List<Harvest> hs)
    {
        Console.WriteLine("\n=== state store: insert-all (new) then re-insert-all (dup lookups) ===");
        Console.WriteLine($"{"level",-34} {"variant",-78} {"ins ns",7} {"dup ns",7} {"B/state",8} {"GC KB",8}");
        foreach (var h in hs.Where(x => x.States.Count >= 20_000))
        {
            for (int v = 0; v < 3; v++)
            {
                // warm
                for (int rep = 0; rep < 2; rep++)
                {
                    IStateStore s = v switch { 0 => new GoLikeStore(h.Floors), 1 => new PackedBitsetStore(h.Floors), _ => new PackedListStore(h.NBoxes) };
                    long gc0 = GC.GetAllocatedBytesForCurrentThread();
                    var sw = Stopwatch.StartNew();
                    foreach (var st in h.States) s.Insert(st.AsSpan(0, h.NBoxes), st[h.NBoxes], 0);
                    double ins = sw.Elapsed.TotalMilliseconds * 1e6 / h.States.Count;
                    sw.Restart();
                    int dups = 0;
                    foreach (var st in h.States) if (!s.Insert(st.AsSpan(0, h.NBoxes), st[h.NBoxes], 0)) dups++;
                    double dup = sw.Elapsed.TotalMilliseconds * 1e6 / h.States.Count;
                    long gc = GC.GetAllocatedBytesForCurrentThread() - gc0;
                    if (rep == 1)
                        Console.WriteLine($"{h.Name,-34} {s.Name,-78} {ins,7:F0} {dup,7:F0} {(double)s.BytesReserved / s.Count,8:F1} {gc / 1024,8}{(dups != h.States.Count || s.Count != h.States.Count ? "  !! count mismatch" : "")}");
                    s.Dispose();
                }
            }
        }
    }

    static void FloodBench(List<Harvest> hs)
    {
        Console.WriteLine("\n=== player-region flood (min reachable cell): stamp-queue vs row bitboards ===");
        Console.WriteLine($"{"level",-34} {"floors",6} {"queue ns",9} {"bits ns",8} {"speedup",8}");
        foreach (var h in hs)
        {
            var fl = new Flood(h.Lv);
            if (!fl.Fits || h.States.Count < 1000) continue;
            int step = Math.Max(1, h.States.Count / 20000);
            var samples = new List<(int[] boxes, int player)>();
            for (int i = 0; i < h.States.Count; i += step)
            {
                var st = h.States[i];
                var b = new int[h.NBoxes]; for (int k = 0; k < b.Length; k++) b[k] = h.Fcell[st[k]];
                samples.Add((b, h.Fcell[st[^1]]));
            }
            long sum1 = 0, sum2 = 0; int bad = 0;
            foreach (var (b, p) in samples) { int a = fl.Queue(p, b), c = fl.Bits(p, b); if (a != c) bad++; }
            double t1 = 0, t2 = 0; int reps = 20;
            for (int r = 0; r < reps; r++)
            {
                var sw = Stopwatch.StartNew(); foreach (var (b, p) in samples) sum1 += fl.Queue(p, b); if (r > 1) t1 += sw.Elapsed.TotalMilliseconds;
                sw.Restart(); foreach (var (b, p) in samples) sum2 += fl.Bits(p, b); if (r > 1) t2 += sw.Elapsed.TotalMilliseconds;
            }
            double n = samples.Count * (reps - 2);
            Console.WriteLine($"{h.Name,-34} {h.Floors,6} {t1 * 1e6 / n,9:F0} {t2 * 1e6 / n,8:F0} {t1 / t2,7:F2}x{(bad > 0 ? $"  !! {bad} mismatches" : "")}");
        }
    }

    static void PushGenBench(List<Harvest> hs)
    {
        Console.WriteLine("\n=== push generation (flood + all legal non-dead pushes): per-box loop vs row bitboards ===");
        Console.WriteLine($"{"level",-34} {"boxes",5} {"loop ns",8} {"bits ns",8} {"speedup",8}");
        foreach (var h in hs)
        {
            var g = new BitboardPushGen(h.Lv, h.Dead);
            if (!g.Fits || h.States.Count < 1000) continue;
            int step = Math.Max(1, h.States.Count / 20000);
            var samples = new List<(int[] boxes, int player)>();
            for (int i = 0; i < h.States.Count; i += step)
            {
                var st = h.States[i];
                var b = new int[h.NBoxes]; for (int k = 0; k < b.Length; k++) b[k] = h.Fcell[st[k]];
                samples.Add((b, h.Fcell[st[^1]]));
            }
            int bad = 0;
            foreach (var (b, p) in samples) { int a = g.Loop(b, p, out long s1), c = g.Bits(b, p, out long s2); if (a != c || s1 != s2) bad++; }
            double t1 = 0, t2 = 0; int reps = 12; long sink = 0;
            for (int r = 0; r < reps; r++)
            {
                var sw = Stopwatch.StartNew(); foreach (var (b, p) in samples) sink += g.Loop(b, p, out _); if (r > 1) t1 += sw.Elapsed.TotalMilliseconds;
                sw.Restart(); foreach (var (b, p) in samples) sink += g.Bits(b, p, out _); if (r > 1) t2 += sw.Elapsed.TotalMilliseconds;
            }
            double n = samples.Count * (reps - 2);
            Console.WriteLine($"{h.Name,-34} {h.NBoxes,5} {t1 * 1e6 / n,8:F0} {t2 * 1e6 / n,8:F0} {t1 / t2,7:F2}x{(bad > 0 ? $"  !! {bad} mismatches" : "")}");
        }
    }
}
