using Soksolv.Model;

namespace Soksolv.Search.Reverse;

/// <summary>
/// Go reverse.SolveToward: a private backward search from the solved position toward a forward searcher's state (boxes + player cell);
/// the solution is the forward prefix plus the reversed pulls. Used by the macro lane's StallFinish.
/// </summary>
public static class ReverseToward
{
    public static SolveResult Solve(string[] rows, int[] boxes, int player, string prefix, double seconds, double cellsScale, CancelFlag? cancel)
    {
        Level lv;
        try { lv = Level.Parse(rows); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        lv.Boxes = (int[])boxes.Clone(); lv.Player = player;
        var o = new RevOptions { Cells = true, Supply = 0, CellsAfterSec = 2.0 * cellsScale, Cancel = cancel };
        using var s = new ReverseSolver(lv, o);
        var r = s.Run(Math.Max(seconds, 0.001));
        if (r.Solved) r.Solution = prefix + r.Solution;
        return r;
    }

    /// <summary>meet.EndCell: the player's cell after walking moves from start on a grid w cells wide.</summary>
    public static int EndCell(string moves, int start, int w)
    {
        int p = start;
        foreach (char ch in moves)
            switch (char.ToLowerInvariant(ch))
            {
                case 'u': p -= w; break;
                case 'd': p += w; break;
                case 'l': p--; break;
                case 'r': p++; break;
            }
        return p;
    }
}
