using Soksolv.Model;
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
        Array.Sort(tgtList);
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
        hu = new int[n + 1]; hv = new int[n + 1]; hp = new int[n + 1]; hway = new int[n + 1]; hminv = new int[n + 1]; hused = new bool[n + 1];
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

    /// <summary>Go matchDist: min-cost matching of boxes to target squares by push distances (assignmentInf if none). Exact Hungarian: the value equals Go's incremental duals.</summary>
    int MatchDist(ReadOnlySpan<int> boxes)
    {
        int m = n;
        const int inf = AssignmentInf;
        Array.Clear(hu); Array.Clear(hv); Array.Clear(hp); Array.Clear(hway);
        var u = hu; var v = hv; var p = hp; var way = hway; var minv = hminv; var used = hused;
        for (int i = 1; i <= m; i++)
        {
            p[0] = i; int j0 = 0;
            Array.Fill(minv, int.MaxValue); Array.Clear(used);
            do
            {
                used[j0] = true;
                int i0 = p[j0], delta = int.MaxValue, j1 = 0;
                int box = boxes[i0 - 1];
                for (int j = 1; j <= m; j++)
                {
                    if (used[j]) continue;
                    int e = tgtDist[(j - 1) * cells + box]; if (e < 0) e = inf;
                    int cur = e - u[i0] - v[j];
                    if (cur < minv[j]) { minv[j] = cur; way[j] = j0; }
                    if (minv[j] < delta) { delta = minv[j]; j1 = j; }
                }
                for (int j = 0; j <= m; j++)
                    if (used[j]) { u[p[j]] += delta; v[j] -= delta; } else minv[j] -= delta;
                j0 = j1;
            } while (p[j0] != 0);
            do { int j1 = way[j0]; p[j0] = p[j1]; j0 = j1; } while (j0 != 0);
        }
        long total = 0;
        for (int j = 1; j <= m; j++)
        {
            int e = tgtDist[(j - 1) * cells + boxes[p[j] - 1]]; if (e < 0) e = inf;
            total += e;
        }
        return total < inf ? (int)total : inf;
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
