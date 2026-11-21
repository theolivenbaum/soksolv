using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    /// <summary>
    /// The portfolio's toward lane (Go main.go "toward", exp-177/277/278): forward FESS in target mode aimed at the pull lane's best state (published
    /// by publishPullBest), retargeted when a newer target arrives after a doubling minimum slice (500 ms &lt;&lt; runs-1, at most &lt;&lt; 8), keeping the
    /// stored tree when it is small enough (RetargetReuse). Not ported: meetings with the live perimeter, previous-tree mail.
    /// </summary>
    public static SolveResult SolveTowardLane(string[] rows, FullOptions opt, double timeLimitSec, Func<TowardTarget?> best)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var sw = Stopwatch.StartNew();
        bool Cancelled() => opt.Cancel?.Stopped ?? false;
        double Left() => timeLimitSec > 0 ? timeLimitSec - sw.Elapsed.TotalSeconds : double.MaxValue;
        TowardTarget? last = null;
        FullSolver? active = null;
        int runs = 0;
        long nodes = 0;
        SolveResult Done(SolveResult r) { r.Nodes = nodes + (active?.mExpandedN ?? 0); r.TimeMs = sw.Elapsed.TotalMilliseconds; active?.Dispose(); return r; }
        while (Left() > 0.3 && !Cancelled())
        {
            var tgt = best();
            if (tgt == null || ReferenceEquals(tgt, last)) { Thread.Sleep(50); continue; }
            last = tgt; runs++;
            double minSliceMs = 500.0 * (1L << Math.Min(runs - 1, 8));
            double startMs = sw.Elapsed.TotalMilliseconds;
            if (opt.RetargetReuse && active != null && active.n == tgt.Boxes.Length && !active.mMemFull && active.nodes.Count > 0 && active.nodes.Count <= 16384)
            {
                string? rsol = active.RetargetTo(tgt);
                if (rsol != null) return Done(new SolveResult { Solved = true, Solution = rsol });
            }
            else
            {
                if (active != null) { nodes += active.mExpandedN; active.Dispose(); }
                active = NewTowardReal(rows, lv, opt, tgt, 0, false, null);
            }
            while (!Cancelled() && Left() > 0)
            {
                string? sol = active.TowardRun(Math.Min(0.05, Left()), out var r);
                if (sol != null) return Done(new SolveResult { Solved = true, Solution = sol });
                if (r.Reason != "timeout") break;                         // exhausted / memory / target unreachable: wait for another target
                if (sw.Elapsed.TotalMilliseconds - startMs >= minSliceMs && !ReferenceEquals(best(), tgt)) break;
            }
        }
        return Done(new SolveResult { Reason = "toward ended" });
    }
}
