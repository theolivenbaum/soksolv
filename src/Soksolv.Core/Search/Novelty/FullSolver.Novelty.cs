namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    /// <summary>
    /// Go's `-strategy novelty` lane (exp-192): forward FESS (laneID 5) in which novel moves (a box onto a square not reached before
    /// in the node's FESS cell; width-2 pairs; access capabilities) cost nothing, ordered by tier. It plans with the v1 sink plan
    /// (plan4 and the alt engine belong to the macro lane, laneID 1).
    /// </summary>
    public static SolveResult SolveNovelty(string[] rows, FullOptions opt, double timeLimitSec)
    {
        var o = opt.Clone();
        o.Novelty = 100;
        o.MacroAlt = false;
        o.MacroFeatures &= ~(1 << 5); // no plan4
        return SolveMacro(rows, o, timeLimitSec);
    }
}
