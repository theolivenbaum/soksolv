using System.Diagnostics;
using Soksolv.Model;
using Soksolv.Search.Hub;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    /// <summary>The pull-view solver the hub-gen lane borrows its tables from (Go festbRun1: newPullSolver + pfOn; the hub never runs the pull search itself).</summary>
    static FullSolver NewHubPull(Level lv, FullOptions opt, string[] rows, int cfg)
    {
        var pv = PullView(lv, out var roots, out var spread);
        var po = opt.Clone();
        po.Pull = true; po.PfOn = true; po.Penalties = false; po.MacroStage = 5; po.NodeBudget = 0; po.Net = null; po.StallFinish = false;
        po.MacroFeatures = opt.MacroFeatures & (1 | 8 | 64 | 128);
        po.RngSeed = RevSeed(cfg);
        po.PeerBytes = null;
        return new FullSolver(pv, po) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots, mRows = rows };
    }

    /// <summary>`-strategy hub-gen` / `hub` (Go festbRun(hubOn)): the lane ring — hub configs, with strategy-B alternations after a config ended idle; revive on memory.</summary>
    public static SolveResult SolveHub(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        if (!opt.SharedRace) ResetRaceShared();
        var sw = Stopwatch.StartNew();
        bool nodeMode = opt.HubNodeSlices > 0;
        int idleEnds = 0;
        SolveResult r;
        for (int k = 0; ; k++)
        {
            bool useHub = idleEnds == 0 || k % 2 == 0;
            r = useHub ? HubConfig(rows, lv, opt, timeLimitSec, sw, k, nodeMode) : FestbConfig(rows, lv, opt, timeLimitSec, sw, k, nodeMode);
            if (r.Reason == "hub idle") idleEnds++;
            bool again = !r.Solved && !nodeMode && r.Reason is "memory" or "hub idle" or "festb ended" or "hub ended"
                         && !(opt.Cancel?.Stopped ?? false) && (timeLimitSec <= 0 || timeLimitSec - sw.Elapsed.TotalSeconds >= 1);
            if (!again) break;
        }
        r.TimeMs = sw.Elapsed.TotalMilliseconds;
        return r;
    }

    static SolveResult HubConfig(string[] rows, Level lv, FullOptions opt, double timeLimitSec, Stopwatch sw, int cfg, bool nodeMode)
    {
        using var ps = NewHubPull(lv, opt, rows, cfg);
        var lane = new HubLane(ps, rows, lv, opt, sw, timeLimitSec, nodeMode);
        try
        {
            var r = lane.Run();
            r.Fingerprint = lane.Counters();
            r.PeakBytes = (long)(lane.gen?.Bytes ?? 0);
            return r;
        }
        finally { lane.DisposeAll(); }
    }

    /// <summary>`-strategy hub-gen -hub-gen-only -nodes N`: the generator alone, a full harvest, and a fingerprint of nodes/pool/best hubs (parity against bench/hubgen-ref).</summary>
    public static SolveResult SolveHubGenOnly(string[] rows, FullOptions opt)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var sw = Stopwatch.StartNew();
        using var ps = NewHubPull(lv, opt, rows, 0);
        var lane = new HubLane(ps, rows, lv, opt, sw, 0, true);
        if (lane.gen == null) return new SolveResult { Reason = "hub ended" };
        string fp = lane.GenOnlyFingerprint(opt.NodeBudget);
        return new SolveResult { Reason = "hub ended", Fingerprint = fp, Nodes = lane.gen.Expanded, TimeMs = sw.Elapsed.TotalMilliseconds };
    }

    // festb alternation (odd configs of the idle ring): see FestbAlternate (not yet ported: the ring falls back to hub configs)
    static SolveResult FestbConfig(string[] rows, Level lv, FullOptions opt, double timeLimitSec, Stopwatch sw, int cfg, bool nodeMode)
        => HubConfig(rows, lv, opt, timeLimitSec, sw, cfg, nodeMode);
}
