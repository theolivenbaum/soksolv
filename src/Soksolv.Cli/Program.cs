using System.Globalization;
using System.Text;
using Soksolv.Diagnostics;
using Soksolv.Model;
using Soksolv.Search;
using Soksolv.Search.Reverse;

// soksolv [-t seconds] [-mem MiB] [-nodes N] [-header] <file.xsb> [level index (1-based)]
ClusterChecker.AStar = true; ClusterChecker.SkipCapped = true; // fast cluster proofs by default (see docs); -parity restores Go behaviour
string strategy = "all"; int revLanes = 2, revSupply = 4, revExtra = 0;
int macroStage = 5, macroFeat = 255; bool macroPlanAlt = false, macroAlt = true;
bool portPull = false, portNovelty = true, portFa2 = true;
double tLimit = 0; int memMb = 15360; long nodes = 0; bool header = false;
var pos = new List<string>();
var fa2o = new Fa2Options();
static bool GoBool(string a) => !a.EndsWith("=false") && !a.EndsWith("=0");
for (int i = 0; i < args.Length; i++)
{
    switch (args[i])
    {
        case "-t": tLimit = double.Parse(args[++i], CultureInfo.InvariantCulture); break;
        case "-mem": memMb = int.Parse(args[++i]); break;
        case "-nodes": nodes = long.Parse(args[++i]); break;
        case "-header": header = true; break;
        case "-macro-alt": macroAlt = args[++i] != "0"; break;
        case "-macro-planalt": macroPlanAlt = true; break;
        case "-parity": ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; break; // Go-identical cluster proofs (BFS, no skipping)
        case "-macro-features": macroFeat = int.Parse(args[++i]); break;
        case "-macro-stage": macroStage = int.Parse(args[++i]); break;
        case "-cluster-astar": ClusterChecker.AStar = true; break;
        case "-cluster-check": ClusterChecker.CheckFast = true; break;
        case "-cluster-skipcap": ClusterChecker.SkipCapped = true; break;
        case "-cluster-cap": ClusterChecker.StateCap = int.Parse(args[++i]); break;
        case "-procs": i++; break; // accepted for CLI compatibility (single lane for now)
        case "-strategy": strategy = args[++i]; break;
        case "-pull": portPull = true; break;           // add the pull lane to the portfolio (off by default: no backward perimeter yet)
        case "-no-novelty": portNovelty = false; break;
        case "-no-fa2": portFa2 = false; break;
        case "-rev-lanes": revLanes = int.Parse(args[++i]); break;
        case "-rev-supply": revSupply = int.Parse(args[++i]); break;
        case "-rev-novelty": revExtra = 1; break;
        case var a when a.StartsWith("-fa2-") || a.StartsWith("-hot-sides"):
            {
                string nm = a.Split('=')[0];
                switch (nm)
                {
                    case "-fa2-plan": fa2o.Plan = args[++i]; break;
                    case "-fa2-oopadv": fa2o.OopAdv = int.Parse(args[++i]); break;
                    case "-fa2-edge-win": fa2o.EdgeWin = int.Parse(args[++i]); break;
                    case "-fa2-plan-exp": fa2o.PlanExp = int.Parse(args[++i]); break;
                    case "-fa2-smallfree": fa2o.SmallFree = GoBool(a); break;
                    case "-fa2-opener": fa2o.Opener = GoBool(a); break;
                    case "-fa2-noreturn": fa2o.NoReturn = GoBool(a); break;
                    case "-fa2-verifyadv": fa2o.VerifyAdv = GoBool(a); break;
                    case "-fa2-ooptail": fa2o.OopTail = GoBool(a); break;
                    case "-fa2-explorer": fa2o.Explorer = GoBool(a); break;
                    case "-fa2-rooms": fa2o.Rooms = GoBool(a); break;
                    case "-fa2-goalhot": fa2o.GoalHot = GoBool(a); break;
                    case "-fa2-dist": fa2o.Dist = GoBool(a); break;
                    case "-fa2-corral": fa2o.Corral = GoBool(a); break;
                    case "-fa2-hall": fa2o.Hall = GoBool(a); break;
                    case "-hot-sides": fa2o.HotSides = GoBool(a); break;
                    case "-fa2-lastkey": fa2o.Mobility = args[++i] == "mobility"; break;
                    case "-fa2-debug": fa2o.Debug = GoBool(a); break;
                }
                break;
            }
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
    SharedDeadZone = new Soksolv.Search.DeadZone.DeadZoneTable(18),
    MacroStage = macroStage, MacroFeatures = macroFeat, PlanAlt = macroPlanAlt, MacroAlt = macroAlt, Fa2 = fa2o,
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
    if (strategy == "pull") { opt.StallScale = tl > 0 && tl < 600 ? tl / 600 : 1.0; return FullSolver.SolvePull(r, opt, tl); }
    if (strategy == "fa2") { opt.StallScale = tl > 0 && tl < 600 ? tl / 600 : 1.0; return FullSolver.SolveFa2(r, opt, tl); }
    if (strategy == "novelty") { opt.StallScale = tl > 0 && tl < 600 ? tl / 600 : 1.0; return FullSolver.SolveNovelty(r, opt, tl); }
    if (strategy == "macro") { opt.StallScale = tl > 0 && tl < 600 ? tl / 600 : 1.0; return FullSolver.SolveMacroLane(r, opt, tl); } // main.go -stall-scale
    return Portfolio.Solve(r, new PortfolioOptions { Pull = portPull, Novelty = portNovelty, Fa2 = portFa2, NodeBudget = nodes, MemBytes = (ulong)memMb << 20, RevSupply = revSupply }, tl);
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
