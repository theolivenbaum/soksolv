using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search.Reverse;

/// <summary>Runs the reverse lane as Go's `-strategy reverse`: a ring of phases, each perturbed (key8 offsets), restarted when a phase fills its memory budget.</summary>
public static class ReverseRunner
{
    /// <summary>lanes: 1 = plain lane (cells + supply); 2 (Go default) = the portfolio configuration (cells, novelty, types, walks, connected best).</summary>
    public static RevOptions Config(int lanes, int supply)
    {
        var o = new RevOptions { Supply = supply, Cells = true };
        if (lanes >= 2) { o.Novelty = true; o.Types = true; o.Walks = true; o.Connected = true; }
        return o;
    }

    public static SolveResult Solve(string[] rows, RevOptions template, double timeLimitSec, Action<RevOptions>? tweak = null)
    {
        var sw = Stopwatch.StartNew();
        Level lv;
        try { lv = Level.Parse(rows, template.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        long nodes = 0; int quick = 0;
        for (int phase = 0; ; phase++)
        {
            var o = Clone(template); o.Phase = phase;
            tweak?.Invoke(o);
            long begun = sw.ElapsedMilliseconds;
            SolveResult r;
            using (var s = new ReverseSolver(lv, o))
            {
                double left = timeLimitSec > 0 ? Math.Max(0.001, timeLimitSec - sw.Elapsed.TotalSeconds) : 0;
                r = s.Run(left);
            }
            nodes += r.Nodes; r.Nodes = nodes;
            double remaining = timeLimitSec > 0 ? timeLimitSec - sw.Elapsed.TotalSeconds : double.MaxValue;
            if (r.Reason == "memory" && remaining > 1.0 && !template.Stop && !(template.Cancel?.Stopped ?? false) && template.NodeBudget == 0)
            {
                GC.Collect();
                // restart backoff: quick failures sleep with exponential backoff (20 ms .. 1 s)
                if (sw.ElapsedMilliseconds - begun >= 250) quick = 0;
                else
                {
                    double d = Math.Min(20 << Math.Min(quick, 6), 1000); quick++;
                    d = Math.Min(d, Math.Max(0, (remaining - 1.0) * 1000));
                    Thread.Sleep((int)d);
                }
                continue;
            }
            r.TimeMs = sw.Elapsed.TotalMilliseconds;
            return r;
        }
    }

    static RevOptions Clone(RevOptions t) => (RevOptions)typeof(RevOptions).GetMethod("MemberwiseClone", System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.NonPublic)!.Invoke(t, null)!;
}
