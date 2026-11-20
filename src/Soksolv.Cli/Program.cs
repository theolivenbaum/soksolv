using System.Globalization;
using System.Text;
using Soksolv.Diagnostics;
using Soksolv.Model;
using Soksolv.Search;
using Soksolv.Search.Reverse;

// soksolv [-t seconds] [-mem MiB] [-nodes N] [-header] <file.xsb> [level index (1-based)]
ClusterChecker.AStar = true; ClusterChecker.SkipCapped = true; // fast cluster proofs by default (see docs); -parity restores Go behaviour
string strategy = "all"; int revLanes = 2, revSupply = 4, revExtra = 0;
double tLimit = 0; int memMb = 15360; long nodes = 0; bool header = false;
var pos = new List<string>();
for (int i = 0; i < args.Length; i++)
{
    switch (args[i])
    {
        case "-t": tLimit = double.Parse(args[++i], CultureInfo.InvariantCulture); break;
        case "-mem": memMb = int.Parse(args[++i]); break;
        case "-nodes": nodes = long.Parse(args[++i]); break;
        case "-header": header = true; break;
        case "-parity": ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; break; // Go-identical cluster proofs (BFS, no skipping)
        case "-macro-stage": FullSolver.MacroStage = int.Parse(args[++i]); break;
        case "-cluster-astar": ClusterChecker.AStar = true; break;
        case "-cluster-check": ClusterChecker.CheckFast = true; break;
        case "-cluster-skipcap": ClusterChecker.SkipCapped = true; break;
        case "-cluster-cap": ClusterChecker.StateCap = int.Parse(args[++i]); break;
        case "-procs": i++; break; // accepted for CLI compatibility (single lane for now)
        case "-strategy": strategy = args[++i]; break;
        case "-rev-lanes": revLanes = int.Parse(args[++i]); break;
        case "-rev-supply": revSupply = int.Parse(args[++i]); break;
        case "-rev-novelty": revExtra = 1; break;
        default: pos.Add(args[i]); break;
    }
}
if (pos.Count < 1) { Console.Error.WriteLine("usage: soksolv [-t seconds] [-mem MiB] [-nodes N] [-header] <file.xsb> [level index]"); return 2; }
if (header) Console.Error.Write(MachineInfo.Header());
var levels = XsbCollection.ReadFile(pos[0]);
int idx = pos.Count > 1 ? int.Parse(pos[1]) : 1;
if (idx < 1 || idx > levels.Count) { Console.Error.WriteLine("level index out of range"); return 2; }
var rows = levels[idx - 1];

var opt = new FullOptions
{
    NodeBudget = nodes,
    BudgetBytes = (ulong)memMb * 1024 * 1024 / 100 * 70,
    SharedClusters = new ClusterTable(16),
    SharedPenalties = new ClusterTable(16),
};
SolveResult Run(string[] r, double tl)
{
    if (strategy == "reverse")
    {
        var ro = ReverseRunner.Config(revLanes, revSupply);
        ro.NodeBudget = nodes; ro.BudgetBytes = opt.BudgetBytes; ro.MemLimitBytes = (ulong)memMb * 1024 * 1024;
        return ReverseRunner.Solve(r, ro, tl);
    }
    if (strategy == "full") return FullSolver.Solve(r, opt, tl);
    if (strategy == "macro") return FullSolver.SolveMacro(r, opt, tl);
    return Portfolio.Solve(r, new PortfolioOptions { NodeBudget = nodes, MemBytes = (ulong)memMb << 20, RevSupply = revSupply }, tl);
}
SolveResult res;
{
    Level lvl;
    try { lvl = Level.Parse(rows); } catch (InvalidDataException) { lvl = null!; }
    // levels that start solved need a solution with at least one push: try each first push (circular.go)
    res = lvl != null && strategy != "all" && Circular.StartsSolved(lvl) ? Circular.Solve(rows, Run, tLimit) : Run(rows, tLimit);
}
string? verr = res.Solved ? Verifier.CheckSolution(rows, res.Solution) : null;
int pushes = res.Solution.Count(char.IsUpper);
var sb = new StringBuilder();
sb.Append("{\n");
sb.Append(CultureInfo.InvariantCulture, $"  \"level\": \"{Path.GetFileNameWithoutExtension(pos[0])}-{idx}\",\n");
sb.Append($"  \"solved\": {(res.Solved && verr == null ? "true" : "false")},\n");
sb.Append(CultureInfo.InvariantCulture, $"  \"time_ms\": {res.TimeMs:F1},\n");
sb.Append($"  \"moves\": {res.Solution.Length},\n  \"pushes\": {pushes},\n  \"nodes_expanded\": {res.Nodes},\n");
sb.Append(CultureInfo.InvariantCulture, $"  \"peak_memory_mb\": {res.PeakBytes / 1048576.0:F1},\n");
sb.Append($"  \"stored\": {res.Stats.Stored},\n  \"generated\": {res.Stats.Generated},\n  \"duplicates\": {res.Stats.Duplicates},\n");
sb.Append($"  \"reason\": \"{res.Reason}\",\n");
if (verr != null) sb.Append($"  \"verify_error\": \"{verr}\",\n");
sb.Append($"  \"solution\": \"{res.Solution}\",\n  \"strategy\": \"{(res.Strategy != "" ? res.Strategy : strategy)}\"\n}}\n");
Console.Out.Write(sb.ToString());
return res.Solved && verr == null ? 0 : 1;
