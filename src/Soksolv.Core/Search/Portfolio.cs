using System.Diagnostics;
using Soksolv.Model;
using Soksolv.Search.Reverse;

namespace Soksolv.Search;

public sealed class PortfolioOptions
{
    public long NodeBudget;
    public ulong MemBytes = 15360UL << 20;   // total budget (Go -mem); 70% goes to lane data structures, split by share
    public bool Full = true, Reverse = true;
    public int RevSupply = 4;
    public Action<string>? Log;
}

/// <summary>
/// Runs the ported lanes on their own threads (full + reverse) over one level; the first verified solution wins and
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
        int lanes = (po.Full ? 1 : 0) + (po.Reverse ? 1 : 0);
        if (lanes == 0) return new SolveResult { Reason = "no lanes" };
        ulong budget = po.MemBytes / 100 * 70 / (ulong)lanes;
        var shared = new ClusterTable(16); var sharedPen = new ClusterTable(16);
        SolveResult? winner = null; var all = new List<SolveResult>(); var gate = new object();
        var threads = new List<Thread>();

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
                NodeBudget = po.NodeBudget, BudgetBytes = budget, Cancel = cancel, SharedClusters = shared, SharedPenalties = sharedPen,
            }, timeLimitSec));
        if (po.Reverse)
        {
            var ro = ReverseRunner.Config(2, po.RevSupply);
            ro.NodeBudget = po.NodeBudget; ro.BudgetBytes = budget; ro.MemLimitBytes = po.MemBytes; ro.Cancel = cancel;
            Lane("reverse", () => ReverseRunner.Solve(rows, ro, timeLimitSec));
        }
        foreach (var t in threads) t.Join();

        var res = winner ?? new SolveResult { Reason = string.Join("; ", all.Select(a => $"{a.Strategy}:{a.Reason}")) };
        res.Nodes = all.Sum(a => a.Nodes);
        res.PeakBytes = all.Sum(a => a.PeakBytes);
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        if (winner != null) { res.Stats = winner.Stats; }
        return res;
    }
}
