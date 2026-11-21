using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// Circular levels (every box starts on a goal): the community rule demands at least one push, so the start
/// does not count as solved. Port of Go circular.go: try each legal first push and solve the resulting
/// (unsolved) position, taking turns with doubling time slices. In-process instead of child processes.
/// </summary>
public static class Circular
{
    public readonly record struct FirstPush(string Prefix, string[] Rows);

    public static bool StartsSolved(Level lv)
    {
        if (lv.Boxes.Length == 0) return false;
        foreach (int b in lv.Boxes) if (!lv.Goal[b]) return false;
        return true;
    }

    /// <summary>Every legal first push of the board drawn by rows: walk to the box then push; plus the board after it.</summary>
    public static List<FirstPush> FirstPushes(string[] rows)
    {
        int h = rows.Length, w = 0;
        foreach (var r in rows) w = Math.Max(w, r.Length);
        var grid = new char[h][];
        for (int y = 0; y < h; y++) grid[y] = rows[y].PadRight(w).ToCharArray();
        int px = -1, py = -1;
        for (int y = 0; y < h; y++) for (int x = 0; x < w; x++) if (grid[y][x] is '@' or '+') (px, py) = (x, y);
        var res = new List<FirstPush>();
        if (px < 0) return res;
        bool Inside(int x, int y) => x >= 0 && y >= 0 && x < w && y < h;
        bool Wall(int x, int y) => !Inside(x, y) || grid[y][x] == '#';
        bool Box(int x, int y) => Inside(x, y) && grid[y][x] is '$' or '*';
        bool Goal(int x, int y) => grid[y][x] is '.' or '*' or '+';
        (int dx, int dy, char walk, char push)[] dirs = [(-1, 0, 'l', 'L'), (1, 0, 'r', 'R'), (0, -1, 'u', 'U'), (0, 1, 'd', 'D')];

        var path = new Dictionary<(int, int), string> { [(px, py)] = "" };
        var queue = new Queue<(int x, int y)>(); queue.Enqueue((px, py));
        while (queue.Count > 0)
        {
            var p = queue.Dequeue();
            foreach (var d in dirs)
            {
                var q = (p.x + d.dx, p.y + d.dy);
                if (path.ContainsKey(q) || Wall(q.Item1, q.Item2) || Box(q.Item1, q.Item2)) continue;
                path[q] = path[(p.x, p.y)] + d.walk;
                queue.Enqueue(q);
            }
        }
        for (int y = 0; y < h; y++)
            for (int x = 0; x < w; x++)
            {
                if (!Box(x, y)) continue;
                foreach (var d in dirs)
                {
                    int tx = x + d.dx, ty = y + d.dy;
                    if (!path.TryGetValue((x - d.dx, y - d.dy), out var walk) || Wall(tx, ty) || Box(tx, ty)) continue;
                    var g = new char[h][];
                    for (int i = 0; i < h; i++) g[i] = (char[])grid[i].Clone();
                    char Floor(int fx, int fy) => Goal(fx, fy) ? '.' : ' ';
                    g[py][px] = Floor(px, py);
                    g[ty][tx] = Goal(tx, ty) ? '*' : '$';
                    g[y][x] = Goal(x, y) ? '+' : '@';
                    res.Add(new FirstPush(walk + d.push, g.Select(r => new string(r).TrimEnd(' ')).ToArray()));
                }
            }
        return res;
    }

    /// <summary>
    /// Solves a starts-solved level. solve(rows, seconds) runs one sub-search with a time limit.
    /// Total limit in seconds (0 = unlimited).
    /// </summary>
    public static SolveResult Solve(string[] rows, Func<string[], double, SolveResult> solve, double totalSec)
    {
        var sw = Stopwatch.StartNew();
        var pushes = FirstPushes(rows);
        if (pushes.Count == 0) return new SolveResult { Reason = "circular: no legal push" };
        long nodes = 0;
        for (double slice = 1; ; slice *= 2)
            foreach (var fp in pushes)
            {
                double left = totalSec > 0 ? totalSec - sw.Elapsed.TotalSeconds : double.MaxValue;
                if (left < 0.2) return new SolveResult { Reason = "timeout", Nodes = nodes, TimeMs = sw.Elapsed.TotalMilliseconds };
                var r = solve(fp.Rows, Math.Min(slice, left));
                nodes += r.Nodes;
                if (r.Solved) return new SolveResult { Solved = true, Solution = fp.Prefix + r.Solution, Nodes = nodes, Stats = r.Stats, PeakBytes = r.PeakBytes, TimeMs = sw.Elapsed.TotalMilliseconds };
            }
    }
}
