using System.Diagnostics;
using Soksolv.Model;
using Soksolv.Search.Reverse;

namespace Soksolv.Search;

public sealed class PortfolioOptions
{
    public bool Meet = true;   // bidirectional meetings (Go: on in `all` mode; -no-meet)
    public long NodeBudget;
    public ulong MemBytes = 15360UL << 20;   // total budget (Go -mem); 70% goes to lane data structures, split by share
    public bool Full = true, Reverse = true, Macro = true;
    /// <summary>Novelty lane (forward FESS with the novelty advisor). Pull lane: off by default — without the backward perimeter (not ported) it rarely finishes a level alone.</summary>
    public bool Novelty = true, Pull = false;
    /// <summary>fa2 lane (plan-guided forward FESS: own v2 plan; Go's anytime lane also runs a second engine on the macro lane's plan4 list — not ported).</summary>
    public bool Fa2 = true;
    public int MacroStage = 5, MacroFeatures = 255; // all ported stage-6 features (bits 0-4: windows, deadzone+frontier corral, displacement, corridor flow, obligations)
    public int RevSupply = 4;
    public Action<string>? Log;
}

/// <summary>
/// Runs the ported lanes on their own threads (full + reverse + macro) over one level; the first verified solution wins and
/// cancels the others. Lanes share the cluster proof tables. Starts-solved (circular) levels are routed through
/// <see cref="Circular"/>. Memory: 70% of -mem split equally between the running lanes (Go splits by weight, both 3).
/// </summary>
public static class Portfolio
{
    public static SolveResult Solve(string[] rows, PortfolioOptions po, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        if (Circular.StartsSolved(lv))
            return Circular.Solve(rows, (r, t) => SolveInner(r, po, t), timeLimitSec);
        return SolveInner(rows, po, timeLimitSec);
    }

    static SolveResult SolveInner(string[] rows, PortfolioOptions po, double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        var cancel = new CancelFlag();
        int lanes = (po.Full ? 1 : 0) + (po.Reverse ? 1 : 0) + (po.Macro ? 1 : 0) + (po.Novelty ? 1 : 0) + (po.Pull ? 1 : 0) + (po.Fa2 ? 1 : 0);
        if (lanes == 0) return new SolveResult { Reason = "no lanes" };
        ulong budget = po.MemBytes / 100 * 70 / (ulong)lanes;
        var shared = new ClusterTable(16); var sharedPen = new ClusterTable(16); var sharedDz = new Search.DeadZone.DeadZoneTable(18);
        SolveResult? winner = null; var all = new List<SolveResult>(); var gate = new object();
        var threads = new List<Thread>();
        // bidirectional meetings between forward lanes (full) and the backward reverse lane (Go: meet.Back/Fwd in `all` mode)
        Meet.MeetNet? net = null;
        if (po.Meet && (po.Reverse || po.Pull) && (po.Full || po.Macro || po.Novelty))
        {
            try { net = new Meet.MeetNet(Level.Parse(rows).Cells); } catch (InvalidDataException) { }
        }

        void Lane(string name, Func<SolveResult> run)
        {
            var t = new Thread(() =>
            {
                SolveResult r;
                try { r = run(); }
                catch (Exception e) { r = new SolveResult { Reason = name + ": " + e.GetType().Name + " " + e.Message }; }
                r.Strategy = name;
                if (r.Solved && Verifier.CheckSolution(rows, r.Solution) is { } bad) { r.Solved = false; r.Reason = name + ": invalid solution (" + bad + ")"; }
                lock (gate)
                {
                    all.Add(r);
                    if (r.Solved && winner == null) { winner = r; cancel.Cancel(); }
                }
            }, 256 << 20) { IsBackground = true, Name = name };
            threads.Add(t); t.Start();
        }

        if (po.Full)
            Lane("full", () => FullSolver.Solve(rows, new FullOptions
            {
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, SharedPenalties = sharedPen, SharedDeadZone = sharedDz, Net = net, MeetOwner = 0,
            }, timeLimitSec));
        if (po.Reverse)
        {
            var ro = ReverseRunner.Config(2, po.RevSupply);
            ro.NodeBudget = po.NodeBudget; ro.BudgetBytes = budget; ro.MemLimitBytes = po.MemBytes; ro.Cancel = cancel; ro.Net = net; ro.BackOwner = 0;
            Lane("reverse", () => ReverseRunner.Solve(rows, ro, timeLimitSec));
        }
        if (po.Macro)
            Lane("macro", () => FullSolver.SolveMacroLane(rows, new FullOptions
            {
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, Net = net, MeetOwner = 1,
                MacroStage = po.MacroStage, MacroFeatures = po.MacroFeatures, StallScale = timeLimitSec > 0 && timeLimitSec < 600 ? timeLimitSec / 600 : 1.0,
            }, timeLimitSec));
        if (po.Novelty)
            Lane("novelty", () => FullSolver.SolveNovelty(rows, new FullOptions
            {
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, SharedDeadZone = sharedDz, Net = net, MeetOwner = 3,
                MacroStage = po.MacroStage, MacroFeatures = po.MacroFeatures, StallScale = timeLimitSec > 0 && timeLimitSec < 600 ? timeLimitSec / 600 : 1.0,
            }, timeLimitSec));
        if (po.Fa2)
            Lane("fa2", () => FullSolver.SolveFa2(rows, new FullOptions
            {
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, SharedDeadZone = sharedDz,
                StallScale = timeLimitSec > 0 && timeLimitSec < 600 ? timeLimitSec / 600 : 1.0,
            }, timeLimitSec));
        if (po.Pull)
            Lane("pull", () => FullSolver.SolvePull(rows, new FullOptions
            {
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, Net = net,
            }, timeLimitSec));
        foreach (var t in threads) t.Join();
        if (net != null && Environment.GetEnvironmentVariable("SOKSOLV_MEET_DEBUG") == "1")
            Console.Error.WriteLine($"meet: back adds {net.Back.Adds} fwd adds {net.Fwd.Adds} posted(to back) {net.Back.Posted} confirmed(back) {net.Back.Confirmed} posted(to fwd) {net.Fwd.Posted} confirmed(fwd) {net.Fwd.Confirmed} dropped {net.Back.Dropped + net.Fwd.Dropped}");

        var res = winner ?? new SolveResult { Reason = string.Join("; ", all.Select(a => $"{a.Strategy}:{a.Reason}")) };
        res.Nodes = all.Sum(a => a.Nodes);
        res.PeakBytes = all.Sum(a => a.PeakBytes);
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        if (winner != null) { res.Stats = winner.Stats; }
        return res;
    }
}
