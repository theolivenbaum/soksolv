using System.Diagnostics;
using System.Runtime.InteropServices;
using FloodBench;

unsafe class P
{
    delegate void KFn(ulong* f, ulong* reg, int rows, int rp, ulong seed, out int lo, out int hi);
    static KFn? Get(string n) => n switch
    {
        "alt" => K.Alt<FillLoop>,
        "altadd" => K.Alt<FillAddLoop>,
        "altruns" => K.Alt<FillRuns>,
        "altks" => K.Alt<FillKS>,
        "dirty" => K.Dirty<FillLoop>,
        "dirtyadd" => K.Dirty<FillAddLoop>,
        "dirtyruns" => K.Dirty<FillRuns>,
        "chain" => K.Chain<FillLoop>,
        "chainadd" => K.Chain<FillAddLoop>,
        "chainruns" => K.Chain<FillRuns>,
        "chainks" => K.Chain<FillKS>,
        "sweep" => K.Sweep<FillLoop>,
        "sweepadd" => K.Sweep<FillAddLoop>,
        "sweepaddks" => K.Sweep<FillAddKS>,
        "sweepks" => K.Sweep<FillKS>,
        "chainstat" => K.ChainStat,
        "chain2add" => K.Chain2<FillAddLoop>,
        "chain2peel" => K.Chain2<FillAddPeel>,
        "chain2loop" => K.Chain2<FillLoop>,
        "chainks3" => K.Chain<FillAddKS3>,
        "chainks3p" => K.Chain<FillAddKS3p>,
        "dirtyks3" => K.Dirty<FillAddKS3>,
        "sweepks3" => K.Sweep<FillAddKS3>,
        "altks3" => K.Alt<FillAddKS3>,
        "chain3ks3" => K.Chain3<FillAddKS3>,
        "chain4ks3" => K.Chain4<FillAddKS3>,
        "chain5ks3" => K.Chain5<FillAddKS3>,
        "chainaddks" => K.Chain<FillAddKS>,
        _ => null,
    };

    static int Main(string[] a)
    {
        if (a[0] == "isa")
        {
            Console.WriteLine($"Bmi1 {System.Runtime.Intrinsics.X86.Bmi1.IsSupported} Bmi2 {System.Runtime.Intrinsics.X86.Bmi2.IsSupported} Lzcnt {System.Runtime.Intrinsics.X86.Lzcnt.IsSupported} Popcnt {System.Runtime.Intrinsics.X86.Popcnt.IsSupported} Avx2 {System.Runtime.Intrinsics.X86.Avx2.IsSupported} Ssse3 {System.Runtime.Intrinsics.X86.Ssse3.IsSupported} Sse42 {System.Runtime.Intrinsics.X86.Sse42.IsSupported} Vector128 {System.Runtime.Intrinsics.Vector128.IsHardwareAccelerated} Vector256 {System.Runtime.Intrinsics.Vector256.IsHardwareAccelerated}");
            return 0;
        }
        string kn = a[0]; string path = a[1]; int reps = int.Parse(a[2]); int srcMask = a.Length > 3 ? int.Parse(a[3]) : 15;
        var raw = File.ReadAllBytes(path);
        // parse records
        var offs = new List<int>(); int p = 0;
        while (p < raw.Length) { int src = raw[p], rows = raw[p + 1]; if (((srcMask >> src) & 1) != 0) offs.Add(p); p += 16 + rows * 8; }
        int n = offs.Count;
        // flat arrays
        var meta = new int[n * 3]; var seeds = new ulong[n]; var data = new ulong[n * 64];
        for (int i = 0; i < n; i++)
        {
            int o = offs[i]; int rows = raw[o + 1];
            meta[i * 3] = raw[o]; meta[i * 3 + 1] = rows; meta[i * 3 + 2] = raw[o + 2];
            seeds[i] = BitConverter.ToUInt64(raw, o + 8);
            for (int r = 0; r < rows; r++) data[i * 64 + r] = BitConverter.ToUInt64(raw, o + 16 + r * 8);
        }
        var k = Get(kn);
        ulong* reg = (ulong*)NativeMemory.AlignedAlloc(64 * 8, 64);
        new Span<ulong>(reg, 64).Clear();
        var refK = Get("alt")!;
        ulong* rref = (ulong*)NativeMemory.AlignedAlloc(64 * 8, 64);
        new Span<ulong>(rref, 64).Clear();
        fixed (ulong* d = data)
        {
            if (k != null && Environment.GetEnvironmentVariable("NOVALID") == null)
            {
                int bad = 0; long tot = 0, cells = 0, rowsSum = 0; long[] srcN = new long[8];
                for (int i = 0; i < n; i++)
                {
                    int rows = meta[i * 3 + 1], rp = meta[i * 3 + 2];
                    refK(d + i * 64, rref, rows, rp, seeds[i], out int l0, out int h0);
                    k(d + i * 64, reg, rows, rp, seeds[i], out int l1, out int h1);
                    bool ok = l0 == l1 && h0 == h1;
                    for (int r = 0; r < rows; r++) ok &= reg[r] == rref[r];
                    if (!ok) { if (bad++ < 3) Console.Error.WriteLine($"MISMATCH rec {i} src {meta[i*3]} rows {rows} rp {rp} lo {l0}/{l1} hi {h0}/{h1}"); }
                    for (int r = 0; r < rows; r++) { cells += System.Numerics.BitOperations.PopCount(rref[r]); rref[r] = 0; reg[r] = 0; }
                    rowsSum += h0 - l0 + 1; srcN[meta[i*3]]++;
                }
                Console.Error.WriteLine($"{kn}: {n} queries, mismatches {bad}, avg cells {cells / (double)n:F1}, avg span rows {rowsSum / (double)n:F1}; per src {srcN[1]} {srcN[2]} {srcN[3]} {srcN[4]}");
            }
            if (kn == "chainstat") Console.Error.WriteLine($"visits/q {K.CVisit/(double)n:F2} pops {K.CPop/(double)n:F2} emptypops {K.CPopEmpty/(double)n:F2} both {K.CBoth/(double)n:F2} fillIters/visit {K.CFillIt/(double)K.CVisit:F2} pc1 {K.CPc1/(double)K.CVisit:F2} pc2 {K.CPc2/(double)K.CVisit:F2} same {K.CSame/(double)K.CVisit:F2} down0 {K.CDown0/(double)K.CVisit:F2}");
            ulong sink = 0;
            var sw = Stopwatch.StartNew();
            if (k != null)
                for (int rep = 0; rep < reps; rep++)
                    for (int i = 0; i < n; i++)
                    {
                        k(d + i * 64, reg, meta[i * 3 + 1], meta[i * 3 + 2], seeds[i], out int lo, out int hi);
                        for (int r = lo; r <= hi; r++) { sink += reg[r]; reg[r] = 0; }
                    }
            else
                for (int rep = 0; rep < reps; rep++)
                    for (int i = 0; i < n; i++) sink += seeds[i] + (ulong)meta[i * 3 + 2];
            sw.Stop();
            Console.WriteLine($"{kn,-12} {sw.Elapsed.TotalMilliseconds * 1e6 / ((double)reps * n):F1} ns/query  sink {sink & 0xff}");
        }
        return 0;
    }
}
