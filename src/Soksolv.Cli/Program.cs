using System.Globalization;
using System.Text;
using Soksolv.Diagnostics;
using Soksolv.Model;
using Soksolv.Search;

// soksolv [-t seconds] [-mem MiB] [-nodes N] [-header] <file.xsb> [level index (1-based)]
ClusterChecker.AStar = true; ClusterChecker.SkipCapped = true; // fast cluster proofs by default (see docs); -parity restores Go behaviour
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
        case "-cluster-astar": ClusterChecker.AStar = true; break;
        case "-cluster-check": ClusterChecker.CheckFast = true; break;
        case "-cluster-skipcap": ClusterChecker.SkipCapped = true; break;
        case "-cluster-cap": ClusterChecker.StateCap = int.Parse(args[++i]); break;
        case "-procs": i++; break; // accepted for CLI compatibility (single lane for now)
        case "-strategy": i++; break;
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
var res = FullSolver.Solve(rows, opt, tLimit);
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
sb.Append($"  \"solution\": \"{res.Solution}\",\n  \"strategy\": \"full\"\n}}\n");
Console.Out.Write(sb.ToString());
return res.Solved && verr == null ? 0 : 1;
