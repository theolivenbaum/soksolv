// Allocation tracer: runs the full lane for N and 2N nodes on a level; prints bytes/node (delta) and GC AllocationTick types.
// usage: alloctrace <file.xsb> <nodes> [-parity]   (JIT build; needs runtime GC events via EventListener)
using System.Diagnostics.Tracing;
using Soksolv.Model;
using Soksolv.Search;

if (Environment.GetEnvironmentVariable("SOKSOLV_CHECK") == "1") FullSolver.CheckRegions = true;
string file = args[0]; long nodes = long.Parse(args[1]);
bool parity = args.Contains("-parity");
if (parity) { ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; } else { ClusterChecker.AStar = true; ClusterChecker.SkipCapped = true; }
var rows = File.ReadAllLines(file).TakeWhile(l => l.Trim().Length > 0 && !l.StartsWith(";")).ToArray();
string strat = args.Length > 2 && !args[2].StartsWith("-") ? args[2] : "full";
SolveResult Run(long n)
{
    var o = new FullOptions { NodeBudget = n, BudgetBytes = 0 };
    return strat switch { "macro" => FullSolver.SolveMacroLane(rows, o, 0), "novelty" => FullSolver.SolveNovelty(rows, o, 0), "pull" => FullSolver.SolvePull(rows, o, 0), _ => FullSolver.Solve(rows, o, 0) };
}
Run(200); // warm
long Measure(long n) { Run(1); long a = GC.GetAllocatedBytesForCurrentThread(); Run(n); return GC.GetAllocatedBytesForCurrentThread() - a; }
long a1 = Measure(nodes), a2 = Measure(nodes * 2);
Console.WriteLine($"alloc N={nodes}: {a1 / 1048576.0:F2} MB, 2N: {a2 / 1048576.0:F2} MB, steady {(a2 - a1) / (double)nodes:F1} B/node");
var l = new Listener();
Run(nodes * 2);
l.Dump();
sealed class Listener : EventListener
{
    readonly Dictionary<string, long> bytes = new(); readonly Dictionary<string, long> cnt = new();
    protected override void OnEventSourceCreated(EventSource s) { if (s.Name == "Microsoft-Windows-DotNETRuntime") EnableEvents(s, EventLevel.Verbose, (EventKeywords)1); }
    protected override void OnEventWritten(EventWrittenEventArgs e)
    {
        if (!e.EventName!.StartsWith("GCAllocationTick")) return;
        string t = (string)e.Payload![e.PayloadNames!.IndexOf("TypeName")]!; long amt = Convert.ToInt64(e.Payload[e.PayloadNames.IndexOf("AllocationAmount64")]);
        lock (bytes) { bytes[t] = bytes.GetValueOrDefault(t) + amt; cnt[t] = cnt.GetValueOrDefault(t) + 1; }
    }
    public void Dump() { Thread.Sleep(500); lock (bytes) foreach (var kv in bytes.OrderByDescending(k => k.Value).Take(12)) Console.WriteLine($"{kv.Value / 1048576.0,9:F1} MB  ticks {cnt[kv.Key],6}  {kv.Key}"); }
}
