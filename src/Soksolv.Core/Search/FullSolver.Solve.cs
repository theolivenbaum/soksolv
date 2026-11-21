using System.Diagnostics;
using System.Text;
using Soksolv.Model;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    /// <summary>Convenience: parse rows and solve with the given deadline (seconds; 0 = none).</summary>
    public static SolveResult Solve(string[] rows, FullOptions opt, double timeLimitSec)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        using var s = new FullSolver(lv, opt);
        return s.Run(timeLimitSec);
    }

    public SolveResult Run(double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        if (opt.SideDead) sd = new SideDead(wall, goal, W);
        if (opt.ClusterDead)
        {
            var deadSq = new bool[cells];
            for (int c = 0; c < cells; c++) deadSq[c] = dist[c] < 0;
            var bd = new ClusterBoard(wall, goal, deadSq, W, salt: true);
            if (opt.Penalties) bd.Dist = dist;
            cl = new ClusterChecker(bd) { Shared = opt.SharedClusters, SharedPen = opt.SharedPenalties };
        }
        var res = Search(timeLimitSec, sw);
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        if (cl != null && Prof.On) Console.Error.WriteLine($"cluster: calls {cl.DeadCalls} splitFloods {cl.SplitFloods} (nosplit {cl.SplitFail}) searches dead {cl.NDead} ({cl.StDead} st) alive {cl.NAlive} ({cl.StAlive} st) cap {cl.NCap} ({cl.StCap} st) | distinct sets {cl.AllSets.Count}, capped distinct {cl.CapSets.Count}, capped repeats of a set {cl.CapRepeat}, skipped {cl.CapSkipped}");
        Prof.Report(res.TimeMs);
        res.Stats = stats; stats.Stored = nodes.Count;
        res.PeakBytes = (long)TrackedBytes();
        return res;
    }

    SolveResult Search(double timeLimitSec, Stopwatch sw)
    {
        long expanded = 0;
        PairOffsets();
        var start = (int[])lv.Boxes.Clone();
        Array.Sort(start);
        int h0 = 0;
        foreach (int b in start)
        {
            if (dist[b] < 0) return new SolveResult { Reason = "box on dead square" };
            h0 += dist[b];
        }
        foreach (int b in start) SetOcc(b, true);
        int p0 = Reach(lv.Player, mark);
        // root: key from boxes
        for (int w = 0; w < nodes.Nw; w++) ckey[w] = 0;
        foreach (int b in start) Flip(b);
        Add(nodes.HashOf(start, p0), p0, -1, -1, -1, null, 0, h0, 0, -1, start);
        foreach (int b in start) SetOcc(b, false);
        if (h0 == 0) return new SolveResult { Solved = true, Solution = "", Nodes = 0 };

        var cur = new int[n]; var child = new int[n];
        long lastMemCheck = 0;
        double limitMs = timeLimitSec * 1000;
        while (true)
        {
            if (memFull) return new SolveResult { Reason = "memory", Nodes = expanded };
            if (!PopNode(out int id, out int src)) break;
            expanded++;
            if (opt.NodeBudget > 0 && expanded >= opt.NodeBudget) return new SolveResult { Reason = "timeout", Nodes = expanded };
            if ((expanded & 255) == 0)
            {
                long now = sw.ElapsedMilliseconds;
                if (opt.Net != null && MeetCheckMail() is { } met) return new SolveResult { Solved = true, Solution = met, Nodes = expanded };
                if ((opt.NodeBudget == 0 && limitMs > 0 && now > limitMs) || opt.Stop || (opt.Cancel?.Stopped ?? false)) return new SolveResult { Reason = "timeout", Nodes = expanded };
                if (now - lastMemCheck > 50)
                {
                    lastMemCheck = now;
                    ulong tracked = TrackedBytes();
                    if (opt.BudgetBytes > 0 && tracked > opt.BudgetBytes) return new SolveResult { Reason = "memory", Nodes = expanded };
                    if (!lean && opt.BudgetBytes > 0 && tracked > opt.BudgetBytes / 100 * 60) { lean = true; stats.LeanAt = (int)expanded; }
                }
            }
            nodes.Unpack(id, cur);
            foreach (int b in cur) SetOcc(b, true);
            int h = SetCosts(cur);
            int pl = nodes.Player(id);
            Reach(pl, mark);
            uint st = stamp;
            if (nodes.MvIdx(id) < 0) { long to = Prof.Start(); Open(id, cur, h, st); Prof.Stop(8, to); }
            ulong parentHash = nodes.HashOf(cur, pl);
            int oi = nodes.MvIdx(id);
            int solved = -1;
            for (bool added = false; opened[oi].Left > 0 && (!lean || !added);)
            {
                int k = NextMove(id, src, cur, h);
                uint m = mv[k];
                mv[k] |= MoveUsed;
                opened[oi].Left--;
                long ta = Prof.Start();
                int cid = Apply(id, m, cur, child, h, st, parentHash, out int ch);
                Prof.Stop(9, ta);
                if (cid >= 0)
                {
                    added = true;
                    if (ch == 0) { solved = cid; break; }
                }
            }
            if (opened[oi].Left > 0)
            {
                var (nh, nw) = MoveKeyAt(id, NextMove(id, src, cur, h), cur, h);
                PushTo(src, nh, nw, id);
            }
            foreach (int b in cur) SetOcc(b, false);
            if (meetPending.Count > 0) MeetPost(child);
            if (solved >= 0) return new SolveResult { Solved = true, Solution = Reconstruct(solved), Nodes = expanded };
        }
        return new SolveResult { Reason = "exhausted", Nodes = expanded };
    }

    string Reconstruct(int id)
    {
        var pushes = new List<(int from, int dir)>();
        for (; nodes.Parent(id) >= 0; id = nodes.Parent(id))
        {
            if (macros.TryGetValue(id, out var seq))
            {
                for (int k = seq.Length - 1; k >= 0; k--) pushes.Add((seq[k].From, seq[k].Dir));
                continue;
            }
            pushes.Add((nodes.From(id), nodes.Dir(id)));
        }
        pushes.Reverse();
        foreach (int b in lv.Boxes) SetOcc(b, true);
        var prev = new sbyte[cells];
        var sb = new StringBuilder();
        int p = lv.Player;
        var walk = new List<char>();
        foreach (var (from, di) in pushes)
        {
            int target = from - dirs[di];
            uint st = ++stamp;
            int tail = 0; queue[tail++] = p; mark[p] = st;
            for (int i = 0; i < tail && mark[target] != st; i++)
            {
                int c = queue[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + dirs[k];
                    if (mark[nb] != st && !wall[nb] && !occ[nb]) { mark[nb] = st; prev[nb] = (sbyte)k; queue[tail++] = nb; }
                }
            }
            walk.Clear();
            for (int c = target; c != p; c -= dirs[prev[c]]) walk.Add(DirChars[prev[c]]);
            for (int i = walk.Count - 1; i >= 0; i--) sb.Append(walk[i]);
            sb.Append((char)(DirChars[di] - 'a' + 'A'));
            SetOcc(from, false); occ[from + dirs[di]] = true;
            p = from;
        }
        for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
        return sb.ToString();
    }
}
