using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// Pull-mode FESS (Go fess_pull.go, exp-179): the macro engine run backward from the solved position toward the start
/// squares, which are then its "goals". The pull view of a level has the real goals as boxes and the start squares as goals;
/// front()/playerAfter()/supportCell() generalise the push geometry. Standalone (no perimeters) it solves by walking all boxes
/// to the start squares with the real player in the region, then reversing the pulls into a forward solution.
/// </summary>
public sealed unsafe partial class FullSolver
{
    /// <summary>Coarsens the pull search's spread feature (pushes per cell); Go SpreadBucket.</summary>
    public const int SpreadBucket = 4;

    int[] spreadDist = [];   // pull: push distance to the real goals (spread = sum over boxes, larger is better)
    int realPlayer;          // pull: the real level's player (the search ends in its region)
    int[] pullRoots = [];    // pull: root player cells, one per zone of the solved position

    /// <summary>Go newPullSolver: boxes on the goals, the start squares as goals, one root per player zone of the solved position.</summary>
    public static Level PullView(Level lv, out int[] roots, out int[] spread)
    {
        int cells = lv.Cells, w = lv.W;
        var start = new bool[cells];
        foreach (int b in lv.Boxes) start[b] = true;
        var goalsL = new List<int>();
        var occ0 = new bool[cells];
        for (int c = 0; c < cells; c++) if (lv.Goal[c]) { goalsL.Add(c); occ0[c] = true; }
        int[] dirs = [-w, w, -1, 1];
        var seen = new bool[cells];
        var rootsL = new List<int>();
        var q = new List<int>();
        for (int c = 0; c < cells; c++)
        {
            if (lv.Wall[c] || occ0[c] || seen[c]) continue;
            rootsL.Add(c);
            q.Clear(); q.Add(c); seen[c] = true;
            for (int i = 0; i < q.Count; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (!lv.Wall[y] && !occ0[y] && !seen[y]) { seen[y] = true; q.Add(y); }
                }
        }
        roots = [.. rootsL];
        var pv = new Level { W = lv.W, H = lv.H, Wall = lv.Wall, Goal = start, Boxes = [.. goalsL], Player = rootsL.Count > 0 ? rootsL[0] : lv.Player };
        // push distance from each cell to the real goals (0 on a goal, 0 where no goal is reachable): the spread feature
        spread = new int[cells]; Array.Fill(spread, -1);
        q.Clear();
        foreach (int g in goalsL) { spread[g] = 0; q.Add(g); }
        for (int i = 0; i < q.Count; i++)
        {
            int c = q[i];
            foreach (int d in dirs)
            {
                int b = c - d;
                if (!lv.Wall[b] && !lv.Wall[b - d] && spread[b] < 0) { spread[b] = spread[c] + 1; q.Add(b); }
            }
        }
        for (int i = 0; i < cells; i++) if (spread[i] < 0) spread[i] = 0;
        return pv;
    }

    /// <summary>Go's `-strategy pull` lane without perimeters: backward macro search, forward solution on reaching the start.</summary>
    public static SolveResult SolvePull(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var pv = PullView(lv, out var roots, out var spread);
        var o = opt.Clone();
        o.Pull = true; o.Penalties = false; o.MacroStage = 5;
        o.MacroFeatures = opt.MacroFeatures & (1 | 8 | 64 | 128); // windows, corridor flow (push-only), rev-groups, pair-side (push-only); no corral/displacement/plan features
        using var s = new FullSolver(pv, o) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots };
        var r = s.RunMacro(timeLimitSec);
        return r;
    }

    bool FeasibleNode(int id)
    {
        var boxes = new int[n];
        nodes.Unpack(id, boxes);
        return Feasible(boxes);
    }

    // ---- pull deadlocks (Go pullFreezeDead / pullFrozen / pullBlockedDir) -----------------------------------------------------------
    const int PullFreezeVisits = 64;
    uint[]? fzNo; uint fzEpoch; int fzLeft;

    bool PullFreezeDead(int t)
    {
        fzNo ??= new uint[cells];
        fzEpoch++;
        fzLeft = PullFreezeVisits;
        return PullFrozenB(t) == 3;   // frozen and off a goal
    }

    /// <summary>Go pullFrozen: 0 = not frozen, else bit 0 set (frozen) | bit 1 (some frozen box is off its goal).</summary>
    int PullFrozenB(int c)
    {
        var no = fzNo!;
        if (no[c] == fzEpoch || fzLeft <= 0) return 0;
        fzLeft--;
        fz[c] = true;
        int off = goal[c] ? 0 : 2;
        int w = W;
        bool frozen = true;
        int r = PullBlockedDirB(c, 1);
        if ((r & 1) != 0) { off |= r & 2; r = PullBlockedDirB(c, -1); }
        if ((r & 1) != 0) { off |= r & 2; r = PullBlockedDirB(c, w); }
        if ((r & 1) != 0) { off |= r & 2; r = PullBlockedDirB(c, -w); }
        if ((r & 1) != 0) off |= r & 2; else frozen = false;
        fz[c] = false;
        if (!frozen) { no[c] = fzEpoch; return 0; }
        return 1 | off;
    }

    /// <summary>Go pullBlockedDir: 0 = not blocked, else 1 | (2 when a frozen box on the way is off its goal).</summary>
    int PullBlockedDirB(int c, int d)
    {
        int x1 = c + d, x2 = c + 2 * d;
        if (wall[x1] || wall[x2] || fz[x1] || fz[x2] || dist[x1] < 0) return 1;
        byte* bk = blk;
        if (bk[x1] != 0) { int f = PullFrozenB(x1); if ((f & 1) != 0) return 1 | (f & 2); }
        if (bk[x2] != 0) { int f = PullFrozenB(x2); if ((f & 1) != 0) return 1 | (f & 2); }
        return 0;
    }

    // ---- solution (Go pullTail / pullSolution / meet.Complete) ----------------------------------------------------------------------
    /// <summary>The forward pushes taking node id's position to the solved position: its pull path undone in reverse (occ must be clear).</summary>
    List<Push> PullTail(int id)
    {
        Span<sbyte> opposite = [1, 0, 3, 2];
        var outL = new List<Push>();
        var pc = new int[n]; var cc = new int[n];
        for (; nodes.Parent(id) >= 0; id = nodes.Parent(id))
        {
            int p = nodes.Parent(id);
            nodes.Unpack(p, pc); nodes.Unpack(id, cc);
            int t = nodes.From(id);
            foreach (int x in cc) if (Array.IndexOf(pc, x) < 0) t = x;
            foreach (int x in pc) SetOcc(x, true);
            Reach(nodes.Player(p), mark);
            var seq = MBoxPathTo(nodes.From(id), t, stamp, nodes.Dir(id), pc);
            for (int j = seq.Count - 1; j >= 0; j--)
            {
                var pu = seq[j]; // box pulled from pu.From to pu.From+d: forward, pushed back
                outL.Add(new Push(pu.From + dirs[pu.Dir], opposite[pu.Dir]));
            }
            foreach (int x in pc) SetOcc(x, false);
        }
        return outL;
    }

    string PullSolution(int id)
    {
        var boxes = new int[n];
        nodes.Unpack(id, boxes);
        var tail = PullTail(id);
        return CompletePushes(boxes, realPlayer, tail);
    }

    /// <summary>Go meet.Complete: the LURD string making pushes with boxes on boxes and the player on player, walking by BFS between pushes ("" if some push is unreachable).</summary>
    string CompletePushes(int[] boxes, int player, List<Push> pushes)
    {
        var occ2 = new bool[cells];
        foreach (int b in boxes) occ2[b] = true;
        var prev = new sbyte[cells]; var seen = new uint[cells]; uint st = 0;
        var sb = new System.Text.StringBuilder();
        int p = player;
        var q = new int[cells];
        var walk = new List<char>();
        foreach (var pu in pushes)
        {
            int target = pu.From - dirs[pu.Dir];
            st++;
            int tail = 0; q[tail++] = p; seen[p] = st;
            for (int i = 0; i < tail && seen[target] != st; i++)
            {
                int c = q[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + dirs[k];
                    if (seen[nb] != st && !wall[nb] && !occ2[nb]) { seen[nb] = st; prev[nb] = (sbyte)k; q[tail++] = nb; }
                }
            }
            if (seen[target] != st || wall[pu.From + dirs[pu.Dir]] || occ2[pu.From + dirs[pu.Dir]] || !occ2[pu.From]) return "";
            walk.Clear();
            for (int c = target; c != p; c -= dirs[prev[c]]) walk.Add(DirChars[prev[c]]);
            for (int i = walk.Count - 1; i >= 0; i--) sb.Append(walk[i]);
            sb.Append("UDLR"[pu.Dir]);
            occ2[pu.From] = false; occ2[pu.From + dirs[pu.Dir]] = true;
            p = pu.From;
        }
        return sb.ToString();
    }
}
