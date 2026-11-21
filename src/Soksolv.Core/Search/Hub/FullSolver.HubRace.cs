using System.Diagnostics;
using System.Numerics;
using Soksolv.Model;
using Soksolv.Search.Hub;
using Soksolv.Search.Meet;

namespace Soksolv.Search;

/// <summary>
/// The hub-pull lane (Go main.go "hub-pull" + hubrace.go raceAlternate, exp-686): a Festival-ordered pull tree (pfOn, strategy D) races
/// forward toward-searches aimed at its strata tips. Pull and forward work alternate (pull gets 1/festbFwdRatio of the forward work);
/// the forward side is a base tree retargeted to the pull tree's best state plus fresh budgeted trials per stratum (never-tried
/// strata first, then most progress per expansion; a failed stratum is the first to be replaced).
/// Timing: wall clock (Go: festbUnit 250 ms slices) or, when <see cref="FullOptions.HubNodeSlices"/> &gt; 0, expansion counts
/// (deterministic; used for parity against the patched Go reference, bench/hub-ref).
/// Not ported: retarget reuse (a retarget starts a new base tree, Go -retarget-reuse=false), meetings with other lanes, relay-back.
/// </summary>
public sealed unsafe partial class FullSolver
{
    const int FestbFwdRatio = 2;          // forward slice = this many backward slices
    const double FestbUnitMs = 250;       // Go festbUnit
    const long HubRaceUnit = 4096;        // -hub-race-unit: first trial budget in forward expansions
    const double HubRaceBaseFrac = 0.8;   // -hub-race-base-frac

    sealed class RaceSlot { public ulong Sig; public int Tip = -1; public int Tries; public long Exp; public long Budget; public float Prog; }

    /// <summary>Per-config counters (Go hubRaceStats), reported in the fingerprint.</summary>
    sealed class RaceStats { public int Trials, Retries, Reached, Extensions, SharedSkip, BaseRuns; public long BaseExp, Exp; public int MaxStrata; }

    // process-wide tried-strata table (Go raceShared): shared by both hub lanes
    static readonly Dictionary<ulong, (int tries, float best)> raceShared = new();
    internal static void ResetRaceShared() { lock (raceShared) raceShared.Clear(); }

    ulong RaceBytes => (ulong)(nodes.Bytes + mmv.Bytes + mmvScore.Bytes + maux.Bytes);
    internal long ExpandedN => mExpandedN;
    internal bool MemFull => mMemFull;

    /// <summary>Go newTowardReal: the real-level toward search aimed at tgt (HF ranking against the target board).</summary>
    static FullSolver NewTowardReal(string[] rows, Level lv, FullOptions opt, TowardTarget tgt, long nodeLimit, bool nodeMode, Func<ulong>? peerBytes)
    {
        var to = opt.Clone();
        to.Pull = false; to.PfOn = false; to.NodeBudget = 0; to.Net = null; to.MacroAlt = false; to.Novelty = 0;
        to.StallFinish = !nodeMode && opt.StallFinish; // Go: stall finishing is a timed-run feature
        to.MacroFeatures = opt.MacroFeatures & ~(1 << 5); // no plan4
        to.PeerBytes = peerBytes;
        var s = new FullSolver(lv, to) { mRows = rows, nodeLimitOverride = nodeLimit };
        s.hfRank = true; s.tgtFull = tgt; s.SetTarget(tgt.Boxes);
        return s;
    }

    /// <summary>Go towardRun: complete at once when the start already is the target board, else (re)run the search.</summary>
    string? TowardRun(double timeSec, out SolveResult r)
    {
        if (!mStarted && OnTarget(lv.Boxes))
        {
            string tail = MeetNet.Complete(wall, lv.W, tgtFull!.Boxes, lv.Player, tgtFull.Tail);
            if ((tail.Length > 0 || tgtFull.Tail.Length == 0) && Verifier.Check(mRows!, tail) == null) { r = new SolveResult { Solved = true, Solution = tail }; return tail; }
        }
        if (mMemFull) { r = new SolveResult { Reason = "memory" }; return null; }
        r = RunMacro(timeSec);
        if (r.Reason == "memory") mMemFull = true;
        return r.Solved ? r.Solution : null;
    }

    /// <summary>The hub-pull lane (see the class remarks). Wall clock unless opt.HubNodeSlices &gt; 0 (then opt.NodeBudget caps the total expansions).</summary>
    public static SolveResult SolveHubPull(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        ResetRaceShared();
        var sw = Stopwatch.StartNew();
        bool nodeMode = opt.HubNodeSlices > 0;
        SolveResult r;
        for (int k = 0; ; k++)
        {
            r = HubPullConfig(rows, lv, opt, timeLimitSec, sw, k, nodeMode);
            // Go reviveRule: a config that ended on memory (or a finished alternation) restarts fresh with another exploration seed
            bool again = !r.Solved && !nodeMode && (r.Reason is "memory" or "festb ended") && !(opt.Cancel?.Stopped ?? false) && (timeLimitSec <= 0 || timeLimitSec - sw.Elapsed.TotalSeconds >= 1);
            if (!again) break;
        }
        r.TimeMs = sw.Elapsed.TotalMilliseconds;
        return r;
    }

    static SolveResult HubPullConfig(string[] rows, Level lv, FullOptions opt, double timeLimitSec, Stopwatch sw, int cfg, bool nodeMode)
    {
        var pv = PullView(lv, out var roots, out var spread);
        var po = opt.Clone();
        po.Pull = true; po.PfOn = true; po.Penalties = false; po.MacroStage = 5; po.NodeBudget = 0; po.Net = null; po.StallFinish = false;
        po.MacroFeatures = opt.MacroFeatures & (1 | 8 | 64 | 128);
        po.RngSeed = RevSeed(cfg);
        FullSolver? baseF = null, trialF = null;
        po.PeerBytes = () => (baseF?.RaceBytes ?? 0) + (trialF?.RaceBytes ?? 0);
        using var ps = new FullSolver(pv, po) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots };
        var st = new Strata(ps, 0);
        ps.strataT = [st];
        var S = new RaceStats();
        var slots = new List<RaceSlot>();
        TowardTarget? cur = null; bool baseDone = false;
        long baseQuota = HubRaceUnit;
        long slice = opt.HubNodeSlices;
        double festbUnit = nodeMode ? slice : FestbUnitMs;     // pseudo-time unit: ms (wall) or expansions (node mode)
        double pullSlice = festbUnit, fwdT = 0;
        long nodesTotal = 0;
        bool backDone = false;
        var tipBoxes = new int[ps.n];
        var v0 = st;
        long cap = nodeMode ? opt.NodeBudget : 0;
        bool Live()
        {
            if (opt.Cancel?.Stopped ?? false) return false;
            if (nodeMode) return cap <= 0 || ps.mExpandedN + S.Exp + S.BaseExp < cap;
            return timeLimitSec <= 0 || sw.Elapsed.TotalSeconds < timeLimitSec;
        }
        double Left() => timeLimitSec > 0 ? Math.Max(0.001, timeLimitSec - sw.Elapsed.TotalSeconds) : 0;
        string Fp() => $"pull={ps.mExpandedN} base={S.BaseExp} trials={S.Trials} fwd={S.Exp} strata={st.Sig.Count} added={st.Added} reached={S.Reached} ext={S.Extensions} retries={S.Retries} shared={S.SharedSkip} baseRuns={S.BaseRuns}";
        SolveResult Done(SolveResult res, string reason = "")
        {
            if (!res.Solved && reason != "") res.Reason = reason;
            res.Nodes = nodesTotal; res.Fingerprint = Fp();
            return res;
        }
        while (Live())
        {
            // 1. Pull slice.
            if (!backDone && (!ps.mStarted || fwdT >= pullSlice * FestbFwdRatio || st.Sig.Count == 0))
            {
                if (ps.mStarted) pullSlice = Math.Max(festbUnit, fwdT / FestbFwdRatio);
                SolveResult pr;
                if (nodeMode)
                {
                    long room = cap > 0 ? Math.Max(1, cap - (ps.mExpandedN + S.Exp + S.BaseExp)) : slice;
                    ps.nodeLimitOverride = ps.mExpandedN + Math.Min(slice, room);
                    pr = ps.RunMacro(0);
                }
                else
                {
                    double sliceSec = Math.Min(pullSlice / 1000.0, timeLimitSec > 0 ? Math.Max(0.001, Left()) : double.MaxValue);
                    pr = ps.RunMacro(sliceSec);
                }
                if (pr.Solved) return Done(pr);
                backDone = pr.Reason != "timeout";
                fwdT = 0;
            }
            if (!ps.mStarted) return Done(new SolveResult(), "festb: backward search did not start");
            if (!Live()) break;
            while (slots.Count < st.Sig.Count) slots.Add(new RaceSlot { Tip = -1, Budget = HubRaceUnit, Sig = st.Sig[slots.Count] });
            for (int i = 0; i < slots.Count; i++)
                if (slots[i].Sig != st.Sig[i]) slots[i] = new RaceSlot { Tip = -1, Budget = HubRaceUnit, Sig = st.Sig[i] };
            S.MaxStrata = Math.Max(S.MaxStrata, st.Sig.Count);
            // 2a. Base: the pursuit of the pull tree's best state (one persistent forward tree; retargets start a new tree here)
            {
                var tgt = ps.BestTarget();
                if (tgt != null && (cur == null || !SameTarget(tgt, cur)))
                {
                    cur = tgt; baseDone = false; S.BaseRuns++;
                    baseF?.Dispose();
                    baseF = NewTowardReal(rows, lv, opt, tgt, 0, nodeMode, () => ps.RaceBytes + (trialF?.RaceBytes ?? 0));
                }
                if (baseF != null && !baseDone)
                {
                    long before = baseF.mExpandedN; var t0 = sw.Elapsed;
                    baseF.nodeLimitOverride = baseF.mExpandedN + baseQuota;
                    string? sol = baseF.TowardRun(nodeMode ? 0 : Left(), out var r);
                    long used = baseF.mExpandedN - before;
                    S.BaseExp += used;
                    fwdT += nodeMode ? used : (sw.Elapsed - t0).TotalMilliseconds;
                    nodesTotal += used;
                    if (sol != null) return Done(new SolveResult { Solved = true, Solution = sol }, "");
                    baseDone = r.Reason != "timeout";
                    if (!Live()) break;
                }
            }
            // 2. Pick: never-tried first (best pull order), then progress per expansion.
            int pick = -1;
            for (int i = 0; i < st.Sig.Count; i++)
            {
                if (slots[i].Tip >= 0 || st.Failed[i]) continue;
                ps.nodes.Unpack(st.Best[i], tipBoxes);
                ulong key = BoxSig(v0.Goal, ps.RoomCells, v0.Cnt ??= new int[ps.rmRooms.Length + 1], tipBoxes);
                lock (raceShared)
                    if (raceShared.TryGetValue(key, out var rr) && rr.tries > 0)
                    {
                        // tried by the other lane: treat as tried with its progress
                        slots[i] = new RaceSlot { Sig = st.Sig[i], Tip = st.Best[i], Tries = rr.tries, Budget = HubRaceUnit * 2, Prog = rr.best, Exp = HubRaceUnit };
                        S.SharedSkip++;
                        continue;
                    }
                if (pick < 0 || PfFest.Tuple.Better(ps.pfBest[st.Best[i]], ps.pfBest[st.Best[pick]])) pick = i;
            }
            if (pick < 0)
            {
                double bestScore = -1.0;
                for (int i = 0; i < st.Sig.Count; i++)
                {
                    var sl0 = slots[i];
                    double sc = ((double)sl0.Prog + 0.01) / (double)(64 - BitOperations.LeadingZeroCount((ulong)(sl0.Exp + 1))) / (double)(sl0.Tries + 1);
                    if (st.Best[i] != sl0.Tip) sc *= 2; // a newer tip in a tried stratum
                    if (sc > bestScore) { bestScore = sc; pick = i; }
                }
                if (pick >= 0) S.Retries++;
            }
            if (pick < 0)
            {
                if (backDone) break;
                fwdT = pullSlice * FestbFwdRatio; // nothing to try: pull again
                continue;
            }
            // 3. Trial.
            var sl = slots[pick];
            int tip = st.Best[pick];
            if (sl.Tip != tip) sl.Budget = Math.Max(sl.Budget, HubRaceUnit);
            ps.nodes.Unpack(tip, tipBoxes);
            ulong tkey = BoxSig(v0.Goal, ps.RoomCells, v0.Cnt ??= new int[ps.rmRooms.Length + 1], tipBoxes);
            var ttgt = new TowardTarget { Boxes = (int[])tipBoxes.Clone(), Player = ps.nodes.Player(tip), Tail = ToMPush(ps.PullTail(tip)) };
            S.Trials++;
            var tt0 = sw.Elapsed;
            trialF?.Dispose(); trialF = null;
            string? tsol = RaceTrial(ps, rows, lv, opt, ttgt, sl.Budget, sl.Budget * (Math.Min(sl.Tries, 1) * 3 + 1), nodeMode, () => ps.RaceBytes + (baseF?.RaceBytes ?? 0), f => trialF = f,
                                     timeLimitSec, sw, out float prog, out long usedT, out bool reached, out int ext);
            trialF = null;
            S.Exp += usedT;
            baseQuota = Math.Max((long)(usedT * HubRaceBaseFrac / (1 - HubRaceBaseFrac)), HubRaceUnit);
            S.Extensions += ext;
            fwdT += nodeMode ? usedT : (sw.Elapsed - tt0).TotalMilliseconds;
            nodesTotal += usedT;
            if (tsol != null) return Done(new SolveResult { Solved = true, Solution = tsol });
            if (reached) S.Reached++;
            sl.Tip = tip; sl.Tries++; sl.Exp += usedT;
            sl.Prog = Math.Max(sl.Prog, prog);
            sl.Budget = Math.Max(sl.Budget * 2, usedT * 2);
            st.Failed[pick] = true;
            lock (raceShared) { raceShared.TryGetValue(tkey, out var rec); raceShared[tkey] = (rec.tries + 1, Math.Max(rec.best, prog)); }
        }
        return Done(new SolveResult(), "festb ended");
    }

    static bool SameTarget(TowardTarget a, TowardTarget b) => a.Player == b.Player && a.Boxes.AsSpan().SequenceEqual(b.Boxes);

    /// <summary>Go raceTrial: one forward trial at tgt with an expansion budget, doubled while the best (boxes on hub, matching distance) improved in the window's second half (at most maxExp in all).</summary>
    static string? RaceTrial(FullSolver ps, string[] rows, Level lv, FullOptions opt, TowardTarget tgt, long budget, long maxExp, bool nodeMode, Func<ulong> peerBytes, Action<FullSolver> register,
                             double timeLimitSec, Stopwatch sw, out float frac, out long used, out bool reached, out int ext)
    {
        using var f = NewTowardReal(rows, lv, opt, tgt, budget, nodeMode, () => peerBytes());
        register(f);
        int startOn = 0;
        foreach (int b in lv.Boxes) if (f.tgt![b]) startOn++;
        int bestOn = startOn; int bestDist = 1 << 30;
        long lastImpr = 0; int scanned = 0; long win = budget; ext = 0;
        while (true)
        {
            double left = timeLimitSec > 0 ? Math.Max(0.001, timeLimitSec - sw.Elapsed.TotalSeconds) : 0;
            string? sol = f.TowardRun(nodeMode ? 0 : left, out var r);
            for (int id = scanned; id < f.hfOwn.Count; id++)
            {
                var t = f.hfOwn[id];
                if (t.On > bestOn || t.On == bestOn && !t.Inf && t.Dist < bestDist) { bestOn = t.On; bestDist = t.Dist; lastImpr = f.mExpandedN; }
            }
            scanned = f.hfOwn.Count;
            int n = ps.n;
            frac = n > startOn ? (float)(bestOn - startOn) / (n - startOn) : 0;
            reached = bestOn >= n;
            used = f.mExpandedN;
            if (sol != null) { frac = 1; reached = true; return sol; }
            bool live = !(opt.Cancel?.Stopped ?? false) && (nodeMode || timeLimitSec <= 0 || sw.Elapsed.TotalSeconds < timeLimitSec);
            if (r.Reason == "timeout" && f.mExpandedN >= f.nodeLimitOverride && !f.mMemFull && lastImpr >= f.nodeLimitOverride - win / 2 && f.nodeLimitOverride + 2 * win <= maxExp && live)
            {
                win *= 2;
                f.nodeLimitOverride += win;
                ext++;
                continue;
            }
            return null;
        }
    }
}
