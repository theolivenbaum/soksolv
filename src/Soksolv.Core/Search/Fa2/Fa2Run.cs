using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- step stubs (filled in by later fa2 steps) ----------------------------------------------------------------------------------
    void Fa2HallParent(int[] cur, int id)
    {
        if (fa2Hall != null) fa2Hall.Parent(cur, Fa2PackedNow(), nodes.Player(id));
    }
    void Fa2HallChild(int[] cur, ref Fa2Cand c, int i, int b, int np)
    {
        if (fa2Hall == null) return;
        int cpk = fa2Hall.Child(cur, c.Packed, i, b, np);
        if (opt.Fa2.Hall) c.Packed = cpk;
    }
    void Fa2HallRoot(int[] cur, ref int pk, int np)
    {
        if (fa2Hall == null) return;
        int cpk = fa2Hall.Child(cur, pk, -1, -1, np);
        if (opt.Fa2.Hall) pk = cpk;
    }

    static ulong Splitmix(ref ulong x)
    {
        x += 0x9e3779b97f4a7c15UL;
        ulong z = x;
        z = (z ^ z >> 30) * 0xbf58476d1ce4e5b9UL;
        z = (z ^ z >> 27) * 0x94d049bb133111ebUL;
        return z ^ z >> 31;
    }

    /// <summary>Parses rows and runs the fa2 engine (Go `-strategy fa2` without the anytime lane).</summary>
    public static SolveResult SolveFa2(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        opt.Penalties = false; // the macro/fa2 cluster checker has no distance table (no penalties)
        using var s = new FullSolver(lv, opt) { mRows = rows };
        var r = s.RunFa2(timeLimitSec);
        return r;
    }

    SolveResult RunFa2(double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        fa2On = true;
        if (opt.SideDead) sd = new SideDead(wall, goal, W);
        var deadSq = new bool[cells];
        for (int c = 0; c < cells; c++) deadSq[c] = dist[c] < 0;
        if (opt.ClusterDead) cl = new ClusterChecker(new ClusterBoard(wall, goal, deadSq, W, salt: true)) { Shared = opt.SharedClusters, SharedPen = null };
        dz = new Search.DeadZone.DeadZoneChecker(new Search.DeadZone.DeadZoneBoard(wall, goal, deadSq, W), 200) { Shared = opt.SharedDeadZone };
        mNodeLimit = nodeLimitOverride > 0 ? nodeLimitOverride : opt.NodeBudget;
        hotGoalFree = opt.Fa2.GoalHot; hotSides = opt.Fa2.HotSides;
        double limitMs = timeLimitSec > 0 && opt.NodeBudget == 0 ? timeLimitSec * 1000 : 0;
        bool Expired() => (mNodeLimit > 0 && fa2ExpandedN >= mNodeLimit) || opt.Stop || (opt.Cancel?.Stopped ?? false) || (limitMs > 0 && sw.ElapsedMilliseconds > limitMs);

        MComputeRooms(); MComputeBasin();
        if (Fa2Setup(sw, timeLimitSec) is { } early) { early.TimeMs = sw.Elapsed.TotalMilliseconds; return early; }

        m3 = GC.AllocateArray<uint>(cells, pinned: true);
        m3P = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref m3[0]);
        mpost = new uint[4 * cells]; mseen = new uint[4 * cells]; mtseen = new uint[4 * cells];
        mbprev = new int[4 * cells]; mbq = new int[4 * cells + 4];
        PairOffsets();
        fa2Zb = new ulong[cells]; fa2Zp = new ulong[cells];
        ulong seed = 0x491fa2;
        for (int i = 0; i < cells; i++) { fa2Zb[i] = Splitmix(ref seed); fa2Zp[i] = Splitmix(ref seed); }
        fa2Scratch = new int[n];

        var cur = new int[n]; var child = new int[n];
        Array.Copy(lv.Boxes, cur, n);
        Array.Sort(cur);
        ulong bh = 0; bool allGoal = true;
        foreach (int b in cur)
        {
            if (dist[b] < 0) return new SolveResult { Reason = "box on dead square" };
            bh ^= fa2Zb[b];
            SetOcc(b, true);
            allGoal &= goal[b];
        }
        if (allGoal) { foreach (int b in cur) SetOcc(b, false); return new SolveResult { Solved = true }; }
        int np0 = Reach(lv.Player, mark);
        uint st0 = stamp;
        int h0 = 0; foreach (int b in cur) h0 += dist[b];
        int pk = Fa2PackedNow();
        int rootOop = Fa2OopNow(cur, pk);
        Fa2HallRoot(cur, ref pk, np0);
        int rootLast = h0;
        if (opt.Fa2.Mobility) rootLast = Fa2Mobility(cur, -1, 0, mark, st0);
        ulong rootKey = Fa2Key(pk, Regions(), Fa2RoomsNow(), rootOop, HotspotCount(HotCounts(cur)), rootLast);
        for (int x = 0; x < nodes.Nw; x++) ckey[x] = 0;
        foreach (int b in cur) Flip(b);
        int root = Fa2AddNode(np0, -1, -1, -1, 0, rootKey, rootKey, 0, bh);
        fa2Seen.Insert(bh ^ fa2Zp[np0]);

        SolveResult Done(SolveResult r)
        {
            for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
            r.Nodes = fa2ExpandedN;
            r.Stats = stats; stats.Stored = nodes.Count;
            r.TimeMs = sw.Elapsed.TotalMilliseconds;
            F2Report(r.TimeMs);
            r.PeakBytes = (long)Fa2Bytes();
            if (opt.Fa2.Debug) Console.Error.WriteLine($"fa2: nodes {fa2ExpandedN} edges {fa2Ed.Count} cells {fa2Cells.Count} dups {fa2Dups} lateDead {fa2LateDead} advDead {fa2LateDead2} refills {fa2Refills} plan {fa2Fp?.E.Count} init {fa2Fp?.InitNum}");
            return r;
        }
        SolveResult SolvedChild(int id, int[] curArr)
        {
            ref var a = ref A(id);
            var e = fa2Ed[a.EOff];
            int b = e.B;
            if (!ReferenceEquals(curArr, child)) Array.Copy(curArr, child, n);
            child[Fa2IdxOf(curArr, b)] = e.T;
            Array.Sort(child);
            for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
            ulong* pk2 = nodes.Key(id);
            for (int w = 0; w < nodes.Nw; w++) ckey[w] = pk2[w];
            Flip(b); Flip(e.T);
            int cid = Fa2AddNode(e.T, id, b, (sbyte)e.Side, e.Adv, e.Key, Math.Min(a.Best, e.Key), a.W + e.W, 0);
            fa2ExpandedN++;
            string sol = MReconstruct(cid);
            return Done(new SolveResult { Solved = sol != "", Solution = sol, Reason = sol == "" ? "reconstruct failed" : "" });
        }

        if (Fa2Expand(root, cur, st0) == 0) return SolvedChild(root, cur);
        if (A(root).ELen > 0) Fa2Push(Fa2CellFor(A(root).Best), root);
        foreach (int b in cur) SetOcc(b, false);

        while (true)
        {
            if (Expired()) return Done(new SolveResult { Reason = "timeout" });
            if ((fa2ExpandedN & 1023) == 0 && opt.BudgetBytes > 0 && Fa2Bytes() > opt.BudgetBytes) return Done(new SolveResult { Reason = "memory" });
            int c = Fa2PickCell();
            if (c < 0) return Done(new SolveResult { Reason = "exhausted" });
            int id = Fa2Pop(c);
            ref var an = ref A(id);
            if (opt.Fa2.EdgeWin > 0 && an.ECur == an.ELen - 1 && an.EBase + an.ELen < an.ETot) Fa2Refill(id, cur);
            ref var an2 = ref A(id);
            var e = fa2Ed[an2.EOff + an2.ECur];
            an2.ECur++;
            if (an2.ECur < an2.ELen) Fa2Push(c, id);
            ulong eh = an2.BoxH ^ fa2Zb[e.B] ^ fa2Zb[e.T] ^ fa2Zp[e.Np];
            if (fa2Trace) Console.Error.WriteLine($"pop c={c} id={id} b={e.B} t={e.T} seen={fa2Seen.Has(eh)} np={e.Np} side={e.Side} pl={nodes.Player(id)}");
            if (fa2Seen.Has(eh)) { fa2Dups++; continue; }
            nodes.Unpack(id, cur);
            foreach (int b in cur) SetOcc(b, true);
            int eb = e.B, et = e.T;
            Array.Copy(cur, child, n);
            child[Fa2IdxOf(cur, eb)] = et;
            Array.Sort(child);
            SetOcc(eb, false); SetOcc(et, true);
            int side = et - dirs[e.Side];
            int np = Reach(side, mark);
            uint st = stamp;
            if (fa2Trace) Console.Error.WriteLine("  child " + string.Join(" ", child) + $" sideCell={side} np={np}");
            bool dead = false;
            if (cl != null && cl.Dead(occ, et, side, mark, st, -1)) dead = true;
            else if (dz != null && dz.DeadAfter(occ, child, side, et)) dead = true;
            if (dead)
            {
                fa2LateDead++; if (fa2Trace) Console.Error.WriteLine($"  late dead cl={(cl != null && cl.Dead(occ, et, side, mark, st, -1))} dz.Frozen={dz?.Frozen} matching={dz?.MatchingDead} corrals={dz?.Corrals} searches={dz?.Searches}");
                fa2Seen.Insert(eh);
                foreach (int x in child) SetOcc(x, false);
                continue;
            }
            if (stamp != st) { Reach(side, mark); st = stamp; }
            fa2ExpandedN++;
            fa2Turn++;
            if (bat != null && (fa2ExpandedN & 1023) == 0 && !bat.Yield(batMe)) return Done(new SolveResult { Reason = "timeout" }); // baton slice (factor lane pauses the engine here)
            ulong cbh = an2.BoxH ^ fa2Zb[eb] ^ fa2Zb[et];
            fa2Seen.Insert(eh);
            ulong* pk3 = nodes.Key(id);
            for (int w = 0; w < nodes.Nw; w++) ckey[w] = pk3[w];
            Flip(eb); Flip(et);
            int cid = Fa2AddNode(np, id, eb, (sbyte)e.Side, e.Adv, e.Key, Math.Min(an2.Best, e.Key), an2.W + e.W, cbh);
            uint kc = Fa2CellKey(e.Key);
            if (kc < fa2ImpKey) { fa2ImpKey = kc; fa2ImpAt = fa2ExpandedN; }
            if (Fa2Expand(cid, child, st) == 0) return SolvedChild(cid, child);
            if (A(cid).ELen > 0) Fa2Push(Fa2CellFor(A(cid).Best), cid);
            foreach (int x in child) SetOcc(x, false);
        }
    }

    ulong Fa2Bytes() => (ulong)(fa2Ed.Bytes + fa2Aux.Bytes + nodes.Bytes + fa2Cells.Sum(c => c.heap.Capacity * 16L));

    /// <summary>Builds the plan and static analysis for fa2 (Go fa2Run up to the root). Returns a result only if setup fails.</summary>
    SolveResult? Fa2Setup(Stopwatch sw, double timeLimitSec)
    {
        long budgetMs = opt.NodeBudget > 0 || timeLimitSec <= 0 ? long.MaxValue / 4 : (long)Math.Min(1000, timeLimitSec * 1000 / 10);
        long deadlineMs = budgetMs >= long.MaxValue / 4 ? long.MaxValue : sw.ElapsedMilliseconds + budgetMs;
        PackingPlan? pl = null;
        switch (opt.Fa2.Plan)
        {
            case "old":
                {
                    var old = MBuildPackingPlan(opt.Fa2.PlanExp, deadlineMs);
                    if (old != null) { old.Fest = FestPlan.Build(old, n); pl = old; }
                    break;
                }
            default:
                pl = Fa2BuildPlanV2(deadlineMs, opt.Fa2.PlanExp);
                break;
        }
        if (pl != null && pl.Fest != null && pl.Fest.E.Count > 0)
        {
            fa2Fp = pl.Fest;
            mPlan = pl;
        }
        if (fa2Fp != null)
        {
            fa2Fp.Mode = 1;
            if (fa2Fp.FestTo == null)
            {
                fa2Fp.FestTo = new bool[cells];
                foreach (var e in fa2Fp.E) if (e.To >= 0 && e.To < cells) fa2Fp.FestTo[e.To] = true;
            }
            mPlan ??= new PackingPlan();
            mPlan.Fest = fa2Fp;
        }
        if (opt.Fa2.Debug && fa2Fp != null) { Console.Error.Write(" "); foreach (var e in fa2Fp.E) Console.Error.Write($" {e.From}->{e.To}/{e.Until}"); Console.Error.WriteLine($" plan init {fa2Fp.InitNum}"); }
        if (fa2Fp != null && opt.Fa2.Hall) fa2Hall = new Fa2Hall(this, fa2Fp);
        Fa2SetupRooms();
        return null;
    }

    void Fa2SetupRooms() { }
    PackingPlan? Fa2BuildPlanV2(long deadlineMs, int maxNodes) => BuildPlanV2(deadlineMs, maxNodes);
}
