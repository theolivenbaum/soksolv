using System.Runtime.Intrinsics;
using Soksolv.Model;
using Soksolv.Search.Macro;
using Soksolv.Search.Meet;

namespace Soksolv.Search;

/// <summary>Target of a toward search: a backward state's boxes/player and its backward tail (forward pushes to the solved position).</summary>
public sealed class TowardTarget
{
    public int[] Boxes = [];
    public int Player;
    public MPush[] Tail = [];
}

/// <summary>
/// Toward (target) mode of the macro engine (Go solver hfRank + setTarget; fess_toward.go): the search runs on the real level, but ranking
/// uses a backward state's box squares as goals: packed = boxes on target squares, hot = hotspots against the target, matching distance =
/// min-cost box-to-target assignment (push distances). No packing plan. Reaching the target board completes with the backward tail.
/// </summary>
public sealed unsafe partial class FullSolver
{
    public const int AssignmentInf = 1 << 20;
    public const int HfNoMatch = 1000;

    bool hfRank;
    bool[]? tgt;
    int[] tgtList = [];
    int[] tgtDist = [];
    TowardTarget? tgtFull;
    int[] hfBoxes = [];

    /// <summary>Go setTarget: target squares replace the goals for ranking only; tgtDist[g*cells+c] = pushes taking a box from c to target square g (-1 never).</summary>
    void SetTarget(int[] boxes)
    {
        tgt = new bool[cells];
        tgtList = (int[])boxes.Clone();
        SortUtil.Insertion(tgtList);
        tgtDist = new int[tgtList.Length * cells];
        Array.Fill(tgtDist, -1);
        var q = new int[cells];
        for (int g = 0; g < tgtList.Length; g++)
        {
            int goalCell = tgtList[g];
            tgt[goalCell] = true;
            var d = tgtDist.AsSpan(g * cells, cells);
            d[goalCell] = 0;
            int tail = 0; q[tail++] = goalCell;
            for (int i = 0; i < tail; i++)
            {
                int c = q[i];
                foreach (int dir in dirs)
                {
                    int b = c - dir;
                    if (!wall[b] && !wall[SupportCell(b, c, dir)] && d[b] < 0) { d[b] = d[c] + 1; q[tail++] = b; }
                }
            }
        }
        hfBoxes = new int[n];
        HungarianInit();
    }

    bool[]? tgtPocket;   // exp-460/480 (hub contexts): floor cells the target board seals off from its player and holds no target box (null: off)

    /// <summary>Go setTargetPockets (hub forward contexts, regRel): pockets the target itself seals; Regions() ignores components made only of them.</summary>
    void SetTargetPockets(TowardTarget t)
    {
        var o = new bool[cells];
        foreach (int b in t.Boxes) o[b] = true;
        var seenC = new bool[cells];
        var q = new List<int> { t.Player };
        seenC[t.Player] = true;
        for (int i = 0; i < q.Count; i++)
            foreach (int d in dirs)
            {
                int y = q[i] + d;
                if (!seenC[y] && !wall[y] && !o[y]) { seenC[y] = true; q.Add(y); }
            }
        var pk = new bool[cells];
        for (int c = 0; c < cells; c++) pk[c] = !wall[c] && !o[c] && !seenC[c];
        tgtPocket = pk;
    }

    bool OnTarget(ReadOnlySpan<int> boxes)
    {
        foreach (int b in boxes) if (!tgt![b]) return false;
        return true;
    }

    /// <summary>Go hotRel: boxes off the target squares touching a box or wall, plus empty target squares no box can be pushed onto from any side (occ holds the position).</summary>
    int HotRel(ReadOnlySpan<int> boxes)
    {
        int cnt = 0;
        foreach (int b in boxes)
        {
            if (tgt![b]) continue;
            foreach (int d in dirs)
                if (wall[b + d] || occ[b + d]) { cnt++; break; }
        }
        foreach (int g in tgtList)
        {
            if (occ[g]) continue;
            bool ok = false;
            foreach (int d in dirs)
            {
                int x2 = g + 2 * d;
                if (!wall[g + d] && x2 >= 0 && x2 < occ.Length && !wall[x2] && !occ[x2]) { ok = true; break; }
            }
            if (!ok) cnt++;
        }
        return cnt;
    }

    int[] hu = [], hv = [], hp = [], hway = [], hminv = []; bool[] hused = [];

    // Hungarian state: duals/columns padded to a multiple of 8 lanes (index 0 is the virtual column); edge table transposed (per box cell: its distance to every target, AssignmentInf for none).
    int hP; int[] tgtDistT = []; int[] hmask = [];

    /// <summary>(Re)allocates the Hungarian arrays and rebuilds the per-box-cell target distance rows from tgtDist.</summary>
    void HungarianInit()
    {
        int m = n + 1;
        hP = (m + 7) & ~7;
        hu = new int[m]; hp = new int[m];
        hv = new int[hP]; hway = new int[hP]; hminv = new int[hP]; hmask = new int[hP]; hused = new bool[m];
        tgtDistT = new int[cells * hP];
        for (int g = 0; g < n; g++)
            for (int c = 0; c < cells; c++)
            {
                int e = tgtDist[g * cells + c];
                tgtDistT[c * hP + g + 1] = e < 0 ? AssignmentInf : e;
            }
    }

    int Edge(int box, int j) => tgtDistT[box * hP + j];

    /// <summary>One augmenting path for row (1-based) of the Hungarian algorithm over the current duals hu/hv/hp (e-maxx formulation, as Go augmentAssignment).</summary>
    void AugmentRow(ReadOnlySpan<int> boxes, int row)
    {
        if (Vector128.IsHardwareAccelerated) { AugmentRowV(boxes, row); return; }
        int m = n; var u = hu; var v = hv; var p = hp; var way = hway; var minv = hminv; var used = hused;
        p[0] = row; int j0 = 0;
        Array.Fill(minv, int.MaxValue); Array.Clear(used);
        do
        {
            used[j0] = true;
            int i0 = p[j0], delta = int.MaxValue, j1 = 0;
            int box = boxes[i0 - 1];
            int baseIdx = box * hP;
            for (int j = 1; j <= m; j++)
            {
                if (used[j]) continue;
                int cur = tgtDistT[baseIdx + j] - u[i0] - v[j];
                if (cur < minv[j]) { minv[j] = cur; way[j] = j0; }
                if (minv[j] < delta) { delta = minv[j]; j1 = j; }
            }
            for (int j = 0; j <= m; j++)
                if (used[j]) { u[p[j]] += delta; v[j] -= delta; } else minv[j] -= delta;
            j0 = j1;
        } while (p[j0] != 0);
        do { int j1 = way[j0]; p[j0] = p[j1]; j0 = j1; } while (j0 != 0);
    }

    /// <summary>
    /// SSE form of <see cref="AugmentRow"/> (4 columns per step; Vector128 is accelerated in the default Native AOT target). Same arithmetic, first minimal
    /// column wins ties, but the dual updates are lazy: with D the sum of the deltas so far, minv is kept as minv+D, and a column that became used at
    /// D_j ends up with v -= D - D_j (and its row u += D - D_j), applied once at the end of the path search.
    /// </summary>
    void AugmentRowV(ReadOnlySpan<int> boxes, int row)
    {
        int m = n, P = hP;
        var u = hu; var p = hp;
        Span<int> usedList = stackalloc int[m + 1];
        Span<int> usedAt = stackalloc int[m + 1];
        int nUsed = 0, D = 0;
        p[0] = row; int j0 = 0;
        fixed (int* v = hv, way = hway, minv = hminv, um = hmask, dt = tgtDistT)
        {
            for (int j = 0; j < P; j += 4)
            {
                Vector128.Store(Vector128.Create(int.MaxValue), minv + j);
                Vector128.Store(Vector128<int>.Zero, um + j);
            }
            for (int j = m + 1; j < P; j++) um[j] = -1;
            var vmax = Vector128.Create(int.MaxValue);
            do
            {
                um[j0] = -1; usedList[nUsed] = j0; usedAt[nUsed++] = D;
                int i0 = p[j0];
                int* row0 = dt + boxes[i0 - 1] * P;
                var vu = Vector128.Create(D - u[i0]);        // cur + D = e - u - v + D
                var vj0 = Vector128.Create(j0);
                var dmin = vmax;
                for (int j = 0; j < P; j += 4)
                {
                    var used = Vector128.Load(um + j);
                    var cur = Vector128.Load(row0 + j) + vu - Vector128.Load(v + j);
                    var mv = Vector128.Load(minv + j);
                    var upd = Vector128.AndNot(Vector128.LessThan(cur, mv), used);
                    mv = Vector128.ConditionalSelect(upd, cur, mv);
                    Vector128.Store(mv, minv + j);
                    Vector128.Store(Vector128.ConditionalSelect(upd, vj0, Vector128.Load(way + j)), way + j);
                    dmin = Vector128.Min(dmin, Vector128.ConditionalSelect(used, vmax, mv));
                }
                int mn = int.MaxValue;
                for (int l = 0; l < 4; l++) { int x = Vector128.GetElement(dmin, l); if (x < mn) mn = x; }
                D = mn;
                int j1 = 0;
                var vm = Vector128.Create(mn);
                for (int j = 0; j < P; j += 4)
                {
                    var used = Vector128.Load(um + j);
                    var eq = Vector128.AndNot(Vector128.Equals(Vector128.Load(minv + j), vm), used);
                    uint bits = Vector128.ExtractMostSignificantBits(eq);
                    if (bits != 0) { j1 = j + System.Numerics.BitOperations.TrailingZeroCount(bits); break; }
                }
                j0 = j1;
            } while (p[j0] != 0);
            for (int k = 0; k < nUsed; k++)
            {
                int j = usedList[k], dj = D - usedAt[k];
                u[p[j]] += dj; v[j] -= dj;
            }
            do { int j1 = way[j0]; p[j0] = p[j1]; j0 = j1; } while (j0 != 0);
        }
    }

    long AssignmentCost(ReadOnlySpan<int> boxes)
    {
        long total = 0;
        for (int j = 1; j <= n; j++) total += Edge(boxes[hp[j] - 1], j);
        return total;
    }

    /// <summary>Go matchDist: min-cost matching of boxes to target squares by push distances (assignmentInf if none).</summary>
    int MatchDist(ReadOnlySpan<int> boxes)
    {
        Array.Clear(hu); Array.Clear(hv); Array.Clear(hp); Array.Clear(hway);
        for (int row = 1; row <= n; row++) AugmentRow(boxes, row);
        long total = AssignmentCost(boxes);
        return total < AssignmentInf ? (int)total : AssignmentInf;
    }

    int[] hfDualU = [], hfDualV = [], hfDualP = [], hfInv = []; long hfCost;

    /// <summary>Go hfMatchParent: matchDist, keeping the exact duals/assignment of the expanded node for hfMatchChild.</summary>
    int MatchParent(ReadOnlySpan<int> boxes)
    {
        int score = MatchDist(boxes);
        int m = n + 1;
        if (hfDualU.Length != m) { hfDualU = new int[m]; hfDualV = new int[m]; hfDualP = new int[m]; hfInv = new int[m]; }
        Array.Copy(hu, hfDualU, m); Array.Copy(hv, hfDualV, m); Array.Copy(hp, hfDualP, m);
        for (int g = 1; g < m; g++) hfInv[hp[g]] = g;
        hfCost = score == AssignmentInf ? AssignmentCost(boxes) : score;
        return score;
    }

    /// <summary>Go hfMatchChild: the child with box `moved` (index in boxes, now at boxes[moved], previously at old) repaired from the parent's saved solution.</summary>
    int MatchChild(ReadOnlySpan<int> boxes, int moved, int old)
    {
        int m = n + 1;
        int row = moved + 1;
        int goalCol = hfInv[row];
        int box = boxes[moved];
        int u = int.MaxValue;
        for (int j = 1; j < m; j++) u = Math.Min(u, Edge(box, j) - hfDualV[j]);
        int newCost = Edge(box, goalCol);
        if (u == newCost - hfDualV[goalCol])
        {
            int oldCost = Edge(old, goalCol);
            return (int)Math.Min(AssignmentInf, hfCost + newCost - oldCost);
        }
        Array.Copy(hfDualU, hu, m); Array.Copy(hfDualV, hv, m); Array.Copy(hfDualP, hp, m);
        hu[row] = u; hp[goalCol] = 0; hv[0] = 0;
        AugmentRow(boxes, row);
        return (int)Math.Min(AssignmentInf, AssignmentCost(boxes));
    }

    /// <summary>
    /// Go retargetTo (exp-299): re-key the live search graph toward a new target board without restarting from the root. Every stored node
    /// gets its HF cell/tuple against the new target and a fresh unopened frontier entry; nodes already on the new target complete the solution.
    /// </summary>
    internal string? RetargetTo(TowardTarget target)
    {
        SetTarget(target.Boxes); tgtFull = target;
        mCancelled = false; mReopen = -1; mLastImproveMs = clock.ElapsedMilliseconds; mStallTries = 0;
        meetPending.Clear();
        mBest = -1; mNext = 0; mTurn = 0;
        mcells.Clear(); mcellIDs.Clear(); mqueues.Clear();
        mmv.Clear(); mmvScore.Clear(); medges.Clear(); mNovWords.Clear(); mNovT0.Clear(); mNovT1.Clear(); mRevWords.Clear();
        hfOwn.Clear(); hfBest.Clear();
        int nNodes = nodes.Count;
        var boxes = new int[n];
        for (int id = 0; id < nNodes; id++)
        {
            nodes.Unpack(id, boxes);
            if (OnTarget(boxes))
            {
                string? sol = CompleteTarget(id);
                if (sol != null) return sol;
            }
            ref var a = ref maux[id];
            a.Dead = 0; a.Version++; nodes.W(id) = 0; a.EdgeHead = -1; a.LiveChildren = 0;
            a.MvStart = -1; a.MvLen = 0; a.MvNext = 0; a.MvLeft = 0; a.MvOff = 0; a.MvTotal = 0; a.NovOffset = 0; a.RevOffset = 0; a.RevBestExit = -1;
            foreach (int b in boxes) SetOcc(b, true);
            int reg = a.Reg;
            int onGoal = MPacked(), hot = HotRel(boxes), dist = MatchDist(boxes);
            foreach (int b in boxes) SetOcc(b, false);
            var cell = new FeatureCell(onGoal, hot, 0, reg + (opt.HfMatchFirst && (dist >= 1 << 20 || opt.HfFrozenMatch && HfFrozenOff(boxes)) ? HfNoMatch : 0));
            a.Actual = cell;
            hfAddDist = Math.Min(dist, 65535);
            int parent = nodes.Parent(id);
            int p = parent >= id || parent >= hfBest.Count ? -1 : parent;
            a.Bucket = (uint)HfSchedAdd(boxes, cell, p, reg);
            hfAddDist = -1;
            if (a.Dead == 0)
            {
                MNoteBest(id);
                mqueues[(int)a.Bucket].Push(new FessEntry { Node = id, Move = -1, Weight = 0, Version = a.Version, Score = LazyScore(id) });
            }
        }
        for (int id = 1; id < nNodes; id++)
        {
            int p = nodes.Parent(id);
            if (p >= 0 && p < nNodes && maux[id].Dead == 0) RecordEdge(p, id);
        }
        mRetargetNodes = nNodes;
        return null;
    }

    /// <summary>Go completeTarget: the prefix (to the child that reached the target board) plus the backward tail; verified on the real level.</summary>
    string? CompleteTarget(int id)
    {
        string prefix = MReconstruct(id);
        var rows = mRows!;
        if (Verifier.Check(rows, prefix) == null) return prefix;
        int at = MeetNet.EndCell(prefix, lv.Player, lv.W);
        string tail = MeetNet.Complete(wall, lv.W, tgtFull!.Boxes, at, tgtFull.Tail);
        string sol = prefix + tail;
        return (tail.Length > 0 || tgtFull.Tail.Length == 0) && Verifier.Check(rows, sol) == null ? sol : null;
    }

    // ---- Festival's HF scheduling (Go hfsched.go): every node keeps its own HF tuple and the best on its path ----------------------
    public struct HfTuple { public bool Inf; public int Reg, On, Hot, Dist; }
    readonly ChunkList<HfTuple> hfOwn = new(), hfBest = new();
    int hfAddDist = -1; // predicted matching distance of the move being applied (Go addHF); -1: none

    static bool HfBetter(in HfTuple a, in HfTuple b)
    {
        if (a.Inf != b.Inf) return !a.Inf;
        if (a.Reg != b.Reg) return a.Reg < b.Reg;
        if (a.On != b.On) return a.On > b.On;
        if (a.Hot != b.Hot) return a.Hot < b.Hot;
        return a.Dist < b.Dist;
    }

    int HfSchedAdd(ReadOnlySpan<int> boxes, in FeatureCell cell, int parent, int reg)
    {
        int dist = hfAddDist;
        if (dist < 0) dist = Math.Min(MatchDist(boxes), 65535);
        var own = new HfTuple { Inf = dist >= 65535, Reg = reg, On = cell.Packed, Hot = cell.Connectivity, Dist = dist };
        var best = own;
        if (parent >= 0 && parent < hfBest.Count && !HfBetter(own, hfBest[parent])) best = hfBest[parent];
        hfOwn.Add(own); hfBest.Add(best);
        return Project(new FeatureCell(best.On, 0, 0, best.Reg), -1);
    }

    int HfSchedReparent(int id, int parent)
    {
        var best = hfOwn[id];
        if (parent >= 0 && parent < hfBest.Count && !HfBetter(best, hfBest[parent])) best = hfBest[parent];
        hfBest[id] = best;
        return Project(new FeatureCell(best.On, 0, 0, best.Reg), -1);
    }

    /// <summary>Go hfFrozenOff: a box off the target squares sits in a 2x2 block of walls and boxes (occ holds the position).</summary>
    bool HfFrozenOff(ReadOnlySpan<int> boxes)
    {
        int w = W;
        foreach (int t in boxes)
        {
            if (tgt![t]) continue;
            Span<int> tls = [t, t - 1, t - w, t - w - 1];
            foreach (int tl in tls)
                if ((wall[tl] || occ[tl]) && (wall[tl + 1] || occ[tl + 1]) && (wall[tl + w] || occ[tl + w]) && (wall[tl + w + 1] || occ[tl + w + 1])) return true;
        }
        return false;
    }

    /// <summary>Go hfKey.better: a matching to the target exists, fewer regions, more boxes on the target, fewer hotspots, smaller matching distance.</summary>
    static bool HfKeyBetter(bool nmA, int regA, int onA, int hotA, int dA, bool nmB, int regB, int onB, int hotB, int dB)
    {
        if (nmA != nmB) return !nmA;
        if (regA != regB) return regA < regB;
        if (onA != onB) return onA > onB;
        if (hotA != hotB) return hotA < hotB;
        return dA < dB;
    }

    /// <summary>The best node of a finished pull search as a toward target (Go bestTarget).</summary>
    long mLastPubAt = -1000; int mLastPub = -1;
    /// <summary>Go publishPullBest (occ must be clear).</summary>
    void PublishPullBest()
    {
        int id = mBest;
        long now = clock.ElapsedMilliseconds;
        if (id < 0 || id == mLastPub || now - mLastPubAt < 100) return;
        mLastPub = id; mLastPubAt = now;
        var boxes = new int[n];
        nodes.Unpack(id, boxes);
        opt.PublishBest!(new TowardTarget { Boxes = boxes, Player = nodes.Player(id), Tail = ToMPush(PullTail(id)) });
    }

    TowardTarget? BestTarget()
    {
        if (mBest < 0) return null;
        var boxes = new int[n];
        nodes.Unpack(mBest, boxes);
        return new TowardTarget { Boxes = boxes, Player = nodes.Player(mBest), Tail = ToMPush(PullTail(mBest)) };
    }
}

public sealed unsafe partial class FullSolver
{
    /// <summary>
    /// Go's `-strategy hf` (exp-188): Festival's strategy B shape on one core: pull FESS for a third of the nodes (best state by FESS
    /// features), then FESS toward that state for the rest. Deterministic under NodeBudget (needs a positive budget).
    /// </summary>
    public static SolveResult SolveHf(string[] rows, FullOptions opt, double timeLimitSec)
    {
        long total = opt.NodeBudget;
        long pullNodes = total > 1 ? Math.Max(1, total / 3) : 0;
        if (pullNodes <= 0 || total > 0 && pullNodes >= total) return new SolveResult { Reason = "hf needs a positive pull and forward budget" };
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var sw = System.Diagnostics.Stopwatch.StartNew();
        // phase 1: backward search
        var pv = PullView(lv, out var roots, out var spread);
        var po = opt.Clone();
        po.Pull = true; po.Penalties = false; po.MacroStage = 5; po.NodeBudget = pullNodes; po.Net = null;
        po.MacroFeatures = opt.MacroFeatures & (1 | 8 | 64 | 128);
        TowardTarget? target; int pullStored;
        using (var ps = new FullSolver(pv, po) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots })
        {
            var pr = ps.RunMacro(timeLimitSec);
            if (pr.Solved) return pr;
            target = ps.BestTarget();
            pullStored = ps.nodes.Count;
        }
        if (target == null) return new SolveResult { Reason = "no backward state" };
        int onStart = 0;
        foreach (int b in target.Boxes) if (Array.IndexOf(lv.Boxes, b) >= 0) onStart++;
        // phase 2: toward
        var to = opt.Clone();
        to.Pull = false; to.NodeBudget = total - pullNodes; to.Net = null; to.MacroAlt = false; to.StallFinish = false; to.Novelty = 0;
        to.MacroFeatures = opt.MacroFeatures & ~(1 << 5); // no plan4
        double left = timeLimitSec > 0 ? Math.Max(0.001, timeLimitSec - sw.Elapsed.TotalSeconds) : 0;
        using var s = new FullSolver(lv, to) { mRows = rows };
        s.hfRank = true; s.tgtFull = target; s.SetTarget(target.Boxes);
        SolveResult r;
        if (s.OnTarget(lv.Boxes))
        {
            string tail = MeetNet.Complete(s.wall, lv.W, target.Boxes, lv.Player, target.Tail);
            r = (tail.Length > 0 || target.Tail.Length == 0) && Verifier.Check(rows, tail) == null ? new SolveResult { Solved = true, Solution = tail } : s.RunMacro(left);
        }
        else r = s.RunMacro(left);
        r.TimeMs = sw.Elapsed.TotalMilliseconds;
        r.Fingerprint = $"pull_stored={pullStored} toward_stored={s.nodes.Count} target_on_start={onStart} target_tail={target.Tail.Length}";
        if (!r.Solved && r.Reason is "timeout" or "") r.Reason = "hf ended";
        return r;
    }
}
