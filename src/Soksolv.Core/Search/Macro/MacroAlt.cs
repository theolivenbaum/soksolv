using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// Go baton (lane.go) restricted to the macro lane's two engine slots: engines share one lane's core, one at a time
/// (deterministic). A token passes round robin at every slice (an engine calls Yield every 1024 expansions); with
/// <c>Dbl &gt; 0</c> an engine keeps the token for 1 &lt;&lt; min(k, Dbl) yields in its k-th turn (exp-769).
/// </summary>
public sealed class Baton
{
    readonly SemaphoreSlim[] ch = [new(0, 1), new(0, 1), new(0, 1)];
    readonly bool[] alive;
    readonly long[] quota; readonly int[] turnK;
    readonly object mu = new();
    bool stop;
    public readonly int Dbl;

    public Baton(bool[] aliveSlots, int dbl)
    {
        alive = (bool[])aliveSlots.Clone(); Dbl = dbl;
        quota = [1, 1, 0]; turnK = new int[3];
    }

    public void Wait(int me) => ch[me].Wait();
    /// <summary>Passes the token to slot (the Go lane's `bat.ch[slot] &lt;- struct{}{}`).</summary>
    public void Release(int slot) => ch[slot].Release();
    public bool IsAlive(int slot) { lock (mu) return alive[slot]; }
    public void RequestStop() { lock (mu) stop = true; }
    public int Live() { lock (mu) { int n = 0; foreach (bool a in alive) if (a) n++; return Math.Max(n, 1); } }
    public bool Stopped { get { lock (mu) return stop; } }

    int Next(int me)
    {
        for (int k = 1; k <= 3; k++) { int j = (me + k) % 3; if (alive[j] && j != me) return j; }
        return -1;
    }

    /// <summary>Hands the token to the next live engine and waits for it back; false = stop.</summary>
    public bool Yield(int me)
    {
        int other;
        lock (mu)
        {
            if (Dbl > 0)
            {
                if (--quota[me] > 0) return !stop;
                turnK[me]++;
                quota[me] = 1L << Math.Min(turnK[me], Dbl);
            }
            other = Next(me);
        }
        if (other >= 0) { ch[other].Release(); ch[me].Wait(); }
        lock (mu) return !stop;
    }

    public void Done(int me, bool solved)
    {
        int other;
        lock (mu)
        {
            alive[me] = false;
            if (solved) stop = true;
            other = Next(me);
        }
        if (other >= 0) ch[other].Release();
    }
}

public sealed unsafe partial class FullSolver
{
    // baton membership of this engine (Go s.bat / s.batMe / s.batDyn), and the per-engine node cap (s.nodeLimit)
    internal Baton? bat; internal int batMe; internal long nodeLimitOverride;

    /// <summary>One engine's ring of plans (Go macro lane ring): config k+1 restarts on the plan4 plan config k completed.</summary>
    static SolveResult RingRun(Level lv, string[] rows, FullOptions opt, double timeLimitSec, Stopwatch sw, Baton? baton, int me)
    {
        PackingPlan? pre = null; int preAt = 0;
        while (true)
        {
            double left = timeLimitSec > 0 ? timeLimitSec - sw.Elapsed.TotalSeconds : 0;
            if (timeLimitSec > 0 && left <= 0) return new SolveResult { Reason = "timeout" };
            using var s = new FullSolver(lv, opt) { mRows = rows, prePlan = pre, prePlanAt = preAt, bat = baton, batMe = me };
            if (baton != null && opt.NodeBudget > 0) s.nodeLimitOverride = Math.Max(opt.NodeBudget / 2, 1);
            var r = s.RunMacro(left);
            if (r.Reason == "replan" && (timeLimitSec <= 0 || sw.Elapsed.TotalSeconds < timeLimitSec)) { pre = s.prePlan; preAt = s.prePlanAt; continue; }
            return r;
        }
    }

    /// <summary>
    /// Go "macro" strategy: two macro engines alternate on the lane's core in doubling baton slices (exp-769): the default
    /// engine and an alt engine whose plan4 runs festsink + roomsep + keep-parks 1. Under -nodes each engine's configs are
    /// bounded by -nodes/2. The first solve wins; the lane's Nodes is the sum of both engines'.
    /// </summary>
    public static SolveResult SolveMacroLane(string[] rows, FullOptions opt, double timeLimitSec)
    {
        if (!opt.MacroAlt) return SolveMacro(rows, opt, timeLimitSec);
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        opt.Penalties = false;
        var sw = Stopwatch.StartNew();
        var baton = new Baton([true, true, false], 4); // MacroAltShift 4: slices 1024 << min(k, 4)
        var altOpt = opt.Clone(); altOpt.PlanAlt = true; altOpt.MacroAlt = false;
        SolveResult altRes = new() { Reason = "alt not run" };
        var t = new Thread(() =>
        {
            baton.Wait(1);
            if (opt.NodeBudget == 0 && baton.Stopped) { altRes = new SolveResult { Reason = "stopped" }; baton.Done(1, false); return; } // timed runs: no point planning after a solve (Go does; node counts are only deterministic under -nodes)
            try { altRes = RingRun(lv, rows, altOpt, timeLimitSec, sw, baton, 1); }
            catch (Exception e) { altRes = new SolveResult { Reason = "alt: " + e.GetType().Name + " " + e.Message }; }
            finally { baton.Done(1, altRes.Solved); }
        }, 256 << 20) { IsBackground = true, Name = "macro-alt" };
        t.Start();
        SolveResult r;
        try { r = RingRun(lv, rows, opt, timeLimitSec, sw, baton, 0); }
        catch (Exception e) { r = new SolveResult { Reason = "macro: " + e.GetType().Name + " " + e.Message }; }
        baton.Done(0, r.Solved);   // a solve stops the other engine; otherwise it continues alone
        t.Join();
        SolveResult res;
        if (!r.Solved && altRes.Solved) { altRes.Nodes += r.Nodes; res = altRes; }
        else { r.Nodes += altRes.Nodes; res = r; }
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        return res;
    }
}
