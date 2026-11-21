using System.Diagnostics;
using System.Text;
using Soksolv.Model;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

/// <summary>
/// Macro lane, Stage 1 (port of Go solver.go / fess_search.go / fess_moves.go / advisors.go with rooms, hotspots,
/// move access, out-of-plan, sink plan and every default-on experiment switched off; see docs/macro.md).
/// FESS over box macro states: a move pushes one box (others fixed) to any square it can reach. States use the same
/// floor-bitset records as the full lane; per-node FESS data lives beside them in <see cref="MacroAux"/>.
/// Implemented as a partial of <see cref="FullSolver"/> to share its flood / deadlock / region kernels directly.
/// </summary>
public sealed unsafe partial class FullSolver
{
    // ---- macro state ---------------------------------------------------------------------------------------------
    readonly ChunkList<MacroAux> maux = new();
    readonly ChunkList<ulong> mmv = new();            // move arena (Go s.mv)
    readonly ChunkList<MoveScore> mmvScore = new();   // Go s.mvScore
    readonly List<MacroEdge> medges = new();
    readonly List<FeatureCell> mcells = new();
    readonly List<FessHeap> mqueues = new();
    readonly Dictionary<FeatureCell, int> mcellIDs = new();
    int mNext, mTurn, mBest = -1, mLastAdded = -1, mReopen = -1;
    bool mCancelled;
    long mExpandedN, mNodeLimit;
    uint[] m3 = [], mpost = [], mseen = [], mtseen = []; int[] mbprev = [], mbq = [];
    uint mpstamp, mbstamp, mtst; int mCandNP;
    uint* m3P;
    int[] mCur = [], mChild = [];
    const ulong MMoveUsed = 1UL << 63;

    struct MCand
    {
        public int I, B, T, R, Packed, Plen, Side, Room, Oop, Progress, Hot, Mobility; public bool Explorer;
        public ulong ScoreKey;
        public bool Undo, Internal, DeadChild, GroupDupExit, DistinctExit; public ulong ExitKey;
        public readonly bool RevBad => Undo || Internal || DeadChild;
    }
    struct MKey { public int Bad, W, Oop, Packed, R, Room, Hot, Mobility, Plen, Idx; }
    Search.DeadZone.DeadZoneChecker? dz;
    MCand[] mCands = new MCand[64]; int mCandN;
    int[] mWeights = new int[64]; MKey[] mKeys = new MKey[64];

    public static SolveResult SolveMacro(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        opt.Penalties = false; // the macro lane's cluster checker has no distance table (no penalties)
        // ring of plans (Go lane ring, exp-563): config k+1 restarts on the anytime plan4 that config k completed
        var sw = Stopwatch.StartNew();
        PackingPlan? pre = null; int preAt = 0;
        while (true)
        {
            double left = timeLimitSec > 0 ? timeLimitSec - sw.Elapsed.TotalSeconds : 0;
            if (timeLimitSec > 0 && left <= 0) return new SolveResult { Reason = "timeout", TimeMs = sw.Elapsed.TotalMilliseconds };
            using var s = new FullSolver(lv, opt) { mRows = rows, prePlan = pre, prePlanAt = preAt };
            var r = s.RunMacro(left);
            if (r.Reason == "replan" && (timeLimitSec <= 0 || sw.Elapsed.TotalSeconds < timeLimitSec)) { pre = s.prePlan; preAt = s.prePlanAt; continue; }
            r.TimeMs = sw.Elapsed.TotalMilliseconds;
            return r;
        }
    }

    SolveResult RunMacro(double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        if (opt.SideDead) sd = new SideDead(wall, goal, W);
        if (opt.ClusterDead)
        {
            var deadSq = new bool[cells];
            for (int c = 0; c < cells; c++) deadSq[c] = dist[c] < 0;
            var bd = new ClusterBoard(wall, goal, deadSq, W, salt: true);
            cl = new ClusterChecker(bd) { Shared = opt.SharedClusters, SharedPen = null };
        }
        if (Feat(1))
        {
            var deadSq = new bool[cells];
            for (int c = 0; c < cells; c++) deadSq[c] = dist[c] < 0;
            dz = new Search.DeadZone.DeadZoneChecker(new Search.DeadZone.DeadZoneBoard(wall, goal, deadSq, W), 200) { Shared = opt.SharedDeadZone };
        }
        mSetupDeadlineMs = opt.NodeBudget > 0 || timeLimitSec <= 0 ? long.MaxValue : (long)Math.Min(100, timeLimitSec * 1000 / 10);
        mNodeLimit = nodeLimitOverride > 0 ? nodeLimitOverride : opt.NodeBudget;
        if (MacroStage >= 2) { MComputeRooms(); MComputeBasin(); }
        if (Prof.On) Console.Error.WriteLine($"rooms+basin done t={clock.ElapsedMilliseconds} ms");
        long tPlan0 = clock.ElapsedMilliseconds;
        if (MacroStage >= 3)
        {
            long planDeadline = opt.NodeBudget > 0 || timeLimitSec <= 0 ? long.MaxValue : (long)Math.Min(250, timeLimitSec * 1000 / 10);
            if (prePlan != null) { mPlan = prePlan; stats.Plan4Complete = true; stats.Plan4Swap = prePlanAt; }   // a finished plan4 plan carried into a restarted solver
            else if (Feat(5))
            {
                // Go: planBudget = 20% of the remaining time (Plan4Frac); -nodes: unbounded (planner caps only); plan4 gets 4/5 of it
                long nowMs = clock.ElapsedMilliseconds;
                long budgetMs = opt.NodeBudget > 0 || timeLimitSec <= 0 ? long.MaxValue / 4 : (long)(timeLimitSec * 1000 * 0.2);
                long p4Deadline = budgetMs >= long.MaxValue / 4 ? long.MaxValue : nowMs + budgetMs * 4 / 5;
                var p4 = PlanAnytime(p4Deadline, 4096);
                if (p4 != null && p4.Complete) { p4.FestPacked = true; mPlan = p4; stats.Plan4Complete = true; }
                else
                {
                    long fb = opt.NodeBudget > 0 || timeLimitSec <= 0 ? long.MaxValue : (long)Math.Min(250, budgetMs / 5);
                    mPlan = MBuildPackingPlan(4096, fb);
                }
            }
            else mPlan = MBuildPackingPlan(4096, planDeadline);
            if (mPlan != null && Feat(4)) mPlan.Dag = CompilePackingDag(mPlan); // installPlanStats: PackingObligations
        }
        if (Prof.On) Console.Error.WriteLine($"setup: rooms+plan {clock.ElapsedMilliseconds - tPlan0} ms (before plan: {tPlan0} ms), plan complete={mPlan?.Complete} festPacked={mPlan?.FestPacked} p4 expanded={stats.Plan4Exp} remaining={mPlan?.Remaining}");
        m3 = GC.AllocateArray<uint>(cells, pinned: true);
        m3P = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref m3[0]);
        mpost = new uint[4 * cells]; mseen = new uint[4 * cells]; mtseen = new uint[4 * cells];
        mbprev = new int[4 * cells]; mbq = new int[4 * cells + 4];
        mCur = new int[n]; mChild = new int[n];
        var res = MacroSearch(timeLimitSec, sw);
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        res.Stats = stats; stats.Stored = nodes.Count;
        res.PeakBytes = (long)(nodes.Bytes + mmv.Bytes + mmvScore.Bytes + maux.Bytes);
        MacroReport(res);
        if (Prof.On) { Prof.Names[0] = "pushDead"; Prof.Names[1] = "roomConn"; Prof.Names[2] = "hotAfter"; Prof.Names[3] = "moveAccess"; Prof.Names[4] = "regions"; Prof.Names[5] = "piCorral+setup"; Prof.Names[11] = "apply:deadzone"; Prof.Names[12] = "apply:cluster"; Prof.Names[6] = "pd:freeze2x2"; Prof.Names[7] = "pd:goalarea"; Prof.Names[10] = "pd:pair"; Prof.Names[14] = "pd:sidedead"; Prof.Names[8] = "MOpen"; Prof.Names[9] = "MApply"; Prof.Report(res.TimeMs); Console.Error.WriteLine($"pairs tried {stats.PairsTried} dead {stats.PairsDead}"); if (dz != null) Console.Error.WriteLine($"dz: calls {dz.Calls} corrals {dz.Corrals} searches {dz.Searches} states {dz.States} pfloods {dz.PFloods} cfloods {dz.CFloods} escCalls {dz.EscCalls} escPasses {dz.EscPasses} cacheHits {dz.CacheHits} opened {dz.Opened} matching {dz.MatchingDead}"); }
        return res;
    }

    /// <summary>Per-run fingerprint for parity checks: stderr line when SOKSOLV_S1=1 (matches the patched Go reference).</summary>
    void MacroReport(SolveResult res)
    {
        res.Fingerprint = $"expanded={res.Nodes} stored={nodes.Count} generated={stats.Generated} duplicates={stats.Duplicates} hash={s1Hash:x16}";
        if (s1Env) { Console.Error.WriteLine($"S1FEAT corral_dead={stats.CorralDead} frontier_corrals={stats.FrontierCorrals} disp_adv={stats.DispAdvisor} corridor={stats.CorridorFlow} prep={stats.PackingPrepQueries} picks={stats.PackingPrepPicks}"); Console.Error.WriteLine("S1HASH " + res.Fingerprint); }
    }
    static readonly bool s1Env = Environment.GetEnvironmentVariable("SOKSOLV_S1") == "1";
    /// <summary>Maintain the per-move fingerprint (tests / parity runs). Off in production runs.</summary>
    public static bool MacroFingerprint;
    static bool s1On => s1Env || MacroFingerprint;
    ulong s1Hash = 1469598103934665603UL;
    static readonly bool s1Trace = Environment.GetEnvironmentVariable("SOKSOLV_S1TRACE") == "1";
    void S1Mix(ulong v) { s1Hash = (s1Hash ^ v) * 1099511628211UL; }
    void S1Step(long n, int node, int local, int cid)
    {
        S1Mix((uint)node); S1Mix((uint)local); S1Mix((uint)cid);
        if (s1Trace) Console.Error.WriteLine($"S1 {n} node={node} move={local} child={cid} w={nodes.W(node)} stored={nodes.Count}");
    }

    // ---- scoring helpers -------------------------------------------------------------------------------------------
    static bool CellBetter(in FeatureCell a, in FeatureCell b)
    {
        if (a.Oop != b.Oop) return a.Oop < b.Oop;
        if (a.Packed != b.Packed) return a.Packed > b.Packed;
        if (a.Connectivity != b.Connectivity) return a.Connectivity < b.Connectivity;
        return a.RoomConnectivity < b.RoomConnectivity;
    }
    static bool ProgressBetter(in FeatureCell a, in FeatureCell b)
    {
        if (a.Packed != b.Packed) return a.Packed > b.Packed;
        if (a.Connectivity != b.Connectivity) return a.Connectivity < b.Connectivity;
        if (a.RoomConnectivity != b.RoomConnectivity) return a.RoomConnectivity < b.RoomConnectivity;
        return a.Oop < b.Oop;
    }

    /// <summary>Boxes on goals (no sink plan: Go packedWith with plan == nil).</summary>
    int MPacked()
    {
        if (mPlan != null) return mPlan.Fest != null && mPlan.FestPacked ? mPlan.Fest.Packed(occ) : mPlan.Status(occ).packed;
        int c = 0;
        foreach (int g in goals) if (occ[g]) c++;
        return c;
    }

    int Project(in FeatureCell cell, int parent)
    {
        if (parent >= 0)
        {
            var best = mcells[(int)maux[parent].Bucket];
            if (!CellBetter(cell, best)) return (int)maux[parent].Bucket;
        }
        if (mcellIDs.TryGetValue(cell, out int id)) return id;
        id = mcells.Count;
        mcellIDs[cell] = id; mcells.Add(cell); mqueues.Add(new FessHeap());
        mNext = id; // NewCellJump
        return id;
    }

    // ---- node management (Go add) ----------------------------------------------------------------------------------------
    bool MCreatesCycle(int id, int parent)
    {
        int n2 = nodes.Count;
        for (int p = parent; p >= 0; p = nodes.Parent(p))
        {
            if (p == id) return true;
            if (--n2 < 0) return true;
        }
        return false;
    }

    int MAdd(ulong zh, int player, int parent, int from, int to, int dir, int g, int h, int w, int reg, ReadOnlySpan<int> boxes)
    {
        int id = nodes.Find(ckey, player, zh, out int slot, out uint fp);
        if (id >= 0)
        {
            stats.Duplicates++;
            mLastAdded = id;
            ref var a = ref maux[id];
            if (a.Dead != 0) return -1;
            if (w > nodes.W(id) || w == nodes.W(id) && g >= nodes.G(id)) return -1;
            bool lowerWeight = w < nodes.W(id);
            if (MCreatesCycle(id, parent)) return -1;
            nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = (sbyte)dir; nodes.G(id) = g; a.PushTo = (ushort)to;
            uint newBucket = (uint)Project(a.Actual, parent);
            bool movedCell = newBucket != a.Bucket;
            if (lowerWeight || movedCell)
            {
                nodes.W(id) = w;
                a.Version++;
                a.Bucket = newBucket;
                if (lowerWeight && a.MvStart >= 0)
                {
                    for (long k = a.MvStart; k < a.MvStart + a.MvLen; k++) mmv[(int)k] &= ~MMoveUsed;
                    a.MvNext = 0;
                    a.MvLeft = a.MvTotal - a.MvOff;
                }
                return id;
            }
            return -1;
        }
        if (sd != null && parent >= 0)
            foreach (int c in boxes)
                if (!goal[c] && sd.Dead(c, player)) { stats.SideDead++; mLastAdded = -1; return -1; }
        id = nodes.Insert(slot, fp, ckey, player);
        mLastAdded = id;
        nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = (sbyte)dir; nodes.G(id) = g; nodes.W(id) = w;
        if (reg < 0) reg = Regions();
        var cell = new FeatureCell(MPacked(), reg, MRoomConnectivity(), MOutOfPlan(boxes));
        int bucket = Project(cell, parent);
        maux.Add(new MacroAux { MvStart = -1, Version = 0, Bucket = (uint)bucket, EdgeHead = -1, Reg = (ushort)reg, Actual = cell, RevGroup = id, RevSize = 1, RevBestExit = -1, PushTo = (ushort)to });
        if (nodes.NeedsGrow) nodes.Grow();
        return id;
    }

    // ---- reversible preparation groups (Go solver.go findRevGroup / unionRevGroup; exp-290) ---------------------------------------
    int MFindRevGroup(int id)
    {
        if (id < 0 || id >= maux.Count) return id;
        int root = id;
        while (root >= 0 && root < maux.Count && root != maux[root].RevGroup) root = maux[root].RevGroup;
        int curr = id;
        while (curr >= 0 && curr < maux.Count && curr != root) { int nxt = maux[curr].RevGroup; maux[curr].RevGroup = root; curr = nxt; }
        return root;
    }
    void MUnionRevGroup(int x, int y)
    {
        int rx = MFindRevGroup(x), ry = MFindRevGroup(y);
        if (rx != ry && rx >= 0 && ry >= 0 && rx < maux.Count && ry < maux.Count)
        {
            if (maux[rx].RevSize + maux[ry].RevSize > 32) return;
            if (maux[rx].RevSize < maux[ry].RevSize) (rx, ry) = (ry, rx);
            maux[ry].RevGroup = rx;
            maux[rx].RevSize += maux[ry].RevSize;
        }
    }
    readonly HashSet<ulong> mRevExits = new();
    readonly List<ulong> mRevWords = new();
    ulong* mRevKey;
    int mGid, mRevP = -1, mPB0 = -1, mPT0 = -1, mPPlayer = -1; bool mReopening; ulong mRevParentHash;

    // ---- dead-end propagation (Go fess_deadends.go) --------------------------------------------------------------------------
    void RecordEdge(int parent, int child)
    {
        if (child < 0 || maux[child].Dead != 0) return;
        medges.Add(new MacroEdge { Parent = parent, Next = maux[child].EdgeHead });
        maux[child].EdgeHead = medges.Count - 1;
        maux[parent].LiveChildren++;
    }

    void PropagateDead(int id)
    {
        ref var a = ref maux[id];
        if (mCancelled || a.Dead != 0 || a.MvStart < 0 || a.MvLeft > 0 || a.LiveChildren > 0) return;
        var q = new Stack<int>(); q.Push(id);
        while (q.Count > 0)
        {
            int x = q.Pop();
            ref var ax = ref maux[x];
            if (ax.Dead != 0) continue;
            ax.Dead = 1; ax.Version++;
            for (int e = ax.EdgeHead; e >= 0; e = medges[e].Next)
            {
                int parent = medges[e].Parent;
                ref var ap = ref maux[parent];
                ap.LiveChildren--;
                if (ap.Dead == 0 && ap.MvStart >= 0 && ap.MvLeft == 0 && ap.LiveChildren == 0) q.Push(parent);
            }
        }
    }

    // ---- frontier (Go fess_search.go) -----------------------------------------------------------------------------------------
    bool MExpired()
    {
        if (mCancelled) return true;
        if (mNodeLimit > 0 && (mExpandedN >= mNodeLimit || opt.Stop || (opt.Cancel?.Stopped ?? false))) { mCancelled = true; return true; }
        if (mNodeLimit > 0) return false;
        if (mDeadlineMs > 0 && (opt.Stop || (opt.Cancel?.Stopped ?? false) || clock.ElapsedMilliseconds > mDeadlineMs)) { mCancelled = true; return true; }
        return false;
    }
    double mDeadlineMs;

    void MEnqueue(int id)
    {
        ref var a = ref maux[id];
        if (a.Dead != 0 || a.MvStart < 0 || a.MvLeft == 0) return;
        long start = a.MvStart, end = a.MvStart + a.MvLen;
        long k = start + a.MvNext;
        while (k < end && (mmv[(int)k] & MMoveUsed) != 0) k++;
        if (k == end) { if (a.MvLeft > 0) mReopen = id; return; } // window ran out: the caller re-opens the node
        a.MvNext = (uint)(k - start);
        int wd = (int)(mmv[(int)k] >> 30 & 1);
        mqueues[(int)a.Bucket].Push(new FessEntry { Node = id, Move = (int)k, Weight = nodes.W(id) + wd, Version = a.Version, Score = mmvScore[(int)k] });
    }

    bool MSelect(out FessEntry result)
    {
        mTurn++;
        if (mTurn % 2 == 0 && mBest >= 0)
        {
            int cell = (int)maux[mBest].Bucket;
            if (cell < mqueues.Count)
            {
                var q = mqueues[cell];
                while (q.Count > 0)
                {
                    var e = q.Pop();
                    if (e.Version == maux[e.Node].Version && (uint)cell == maux[e.Node].Bucket && (mmv[e.Move] & MMoveUsed) == 0) { result = e; return true; }
                }
            }
        }
        for (int c = 0, nq = mqueues.Count; c < nq; c++)
        {
            int cell = mNext;
            mNext = (mNext + 1) % mqueues.Count;
            var q = mqueues[cell];
            while (q.Count > 0)
            {
                var e = q.Pop();
                if (e.Version == maux[e.Node].Version && (uint)cell == maux[e.Node].Bucket && (mmv[e.Move] & MMoveUsed) == 0) { result = e; return true; }
            }
        }
        result = default; return false;
    }

    void MNoteBest(int id)
    {
        if (id < 0 || maux[id].Dead != 0) return;
        if (mBest < 0 || maux[mBest].Dead != 0 || ProgressBetter(maux[id].Actual, maux[mBest].Actual)) { mBest = id; mLastImproveMs = clock.ElapsedMilliseconds; }
    }

    // ---- StallFinish (Go fess_search.go finishFromBest) ------------------------------------------------------------------------------------
    string[]? mRows; long mLastImproveMs, mSearchStartMs; int mStallTries;

    /// <summary>A short private backward search from the solved position toward the best-so-far node; null if it fails.</summary>
    string? MFinishFromBest()
    {
        int best = mBest;
        string prefix = MReconstruct(best);
        var boxes = new int[n];
        nodes.Unpack(best, boxes);
        int player = Reverse.ReverseToward.EndCell(prefix, lv.Player, W);
        double leftMs = mDeadlineMs > 0 ? mDeadlineMs - clock.ElapsedMilliseconds : double.MaxValue;
        double untilMs = Math.Min(1000.0 * opt.StallScale, leftMs / 10);
        var r = Reverse.ReverseToward.Solve(mRows!, boxes, player, prefix, untilMs / 1000, opt.StallScale, opt.Cancel);
        return r.Solved ? r.Solution : null;
    }

    // ---- box search (Go boxSearch): BFS over pushes of box b, others fixed -------------------------------------------------------
    // mode 0: candidate enumeration (calls MVisit); mode 1: path to (pathT, pathSide) into mPath.
    List<Push> mPath = new();
    int mVisitI; bool mCollectingOpen;

    bool MBoxSearch(int b, uint st, bool pathMode, int pathT, int pathSide, ReadOnlySpan<int> cur)
    {
        if (mpstamp > 1u << 31 || mbstamp > 1u << 31)
        {
            Array.Clear(m3); Array.Clear(mseen); Array.Clear(mpost); mpstamp = mbstamp = 0;
        }
        uint bs = ++mbstamp;
        int qn = 0;
        for (int di = 0; di < 4; di++)
        {
            int d = dirs[di];
            if (mark[Front(b, d)] == st && (!pull || !wall[b + 2 * d] && !occ[b + 2 * d])) { int k = b * 4 + di; mseen[k] = bs; mbprev[k] = -1; mbq[qn++] = k; }
        }
        SetOcc(b, false);
        bool found = false;
        try
        {
            for (int i = 0; i < qn; i++)
            {
                int k = mbq[i];
                int c = k >> 2, di = k & 3;
                int nc = c + dirs[di];
                if (wall[nc] || occ[nc] || dist[nc] < 0) continue;
                if (pull) { int pd = nc + dirs[di]; if (wall[pd] || occ[pd]) continue; } // the pulling player needs room to step back
                if (mpost[nc * 4 + di] == bs) continue;
                SetOcc(nc, true);
                uint ps = ++mpstamp;
                int pc = PlayerAfter(nc, dirs[di]);
                int minC = pc;
                {
                    int* q = queueP; int tail = 0; q[tail++] = pc; m3P[pc] = ps;
                    int w = W;
                    for (int j = 0; j < tail; j++)
                    {
                        int x = q[j];
                        int y = x - w; if (m3P[y] != ps && blk[y] == 0) { m3P[y] = ps; q[tail++] = y; if (y < minC) minC = y; }
                        y = x + w; if (m3P[y] != ps && blk[y] == 0) { m3P[y] = ps; q[tail++] = y; if (y < minC) minC = y; }
                        y = x - 1; if (m3P[y] != ps && blk[y] == 0) { m3P[y] = ps; q[tail++] = y; if (y < minC) minC = y; }
                        y = x + 1; if (m3P[y] != ps && blk[y] == 0) { m3P[y] = ps; q[tail++] = y; if (y < minC) minC = y; }
                    }
                }
                mCandNP = minC;
                SetOcc(nc, false);
                if (pathMode)
                {
                    if (nc == pathT && (pathSide < 0 || (k & 3) == pathSide))
                    {
                        mPath.Clear();
                        for (int kk = k; kk >= 0; kk = mbprev[kk]) mPath.Add(new Push(kk >> 2, (sbyte)(kk & 3)));
                        mPath.Reverse();
                        found = true;
                        return true;
                    }
                }
                else if (!MVisit(k, nc, cur)) return false;
                for (int d2i = 0; d2i < 4; d2i++)
                {
                    int k2 = nc * 4 + d2i;
                    if (m3[Front(nc, dirs[d2i])] == ps)
                    {
                        mpost[k2] = bs;
                        if (mseen[k2] != bs) { mseen[k2] = bs; mbprev[k2] = k; mbq[qn++] = k2; }
                    }
                }
            }
            return found;
        }
        finally { SetOcc(b, true); }
    }

    List<Push> MBoxPathTo(int b, int t, uint st, int side, ReadOnlySpan<int> cur)
    {
        MBoxSearch(b, st, true, t, side, cur);
        return mPath;
    }
}
