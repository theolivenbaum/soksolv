using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>Sub-solve entry points for the factor lane: resumable macro / novelty / fa2 engines on a sub-level (Go factor.go subSolve).</summary>
public sealed unsafe partial class FullSolver
{
    public enum SubKind { Macro = 1, Novelty = 5, Fa2 = 12 }

    /// <summary>A solver on sub-level lv (rows = its drawing) with the given engine's lane configuration.</summary>
    public static FullSolver NewSub(Level sub, string[] rows, FullOptions baseOpt, SubKind kind, long nodeLimit)
    {
        var o = baseOpt.Clone();
        o.Net = null; o.Penalties = false; o.CloseHoles = false;
        switch (kind)
        {
            case SubKind.Macro: o.MacroAlt = false; break;
            case SubKind.Novelty: o.Novelty = 100; o.MacroAlt = false; o.MacroFeatures &= ~(1 << 5); break;
        }
        var s = new FullSolver(sub, o) { mRows = rows, nodeLimitOverride = nodeLimit };
        return s;
    }

    /// <summary>Expansions so far (Go s.expandedN).</summary>
    public long SubExpanded => fa2On ? fa2ExpandedN : mExpandedN;
    /// <summary>The search has started (Go s.resume != nil): a later SubRun continues it.</summary>
    public bool SubStarted => mStarted;
    internal object? SubPrePlan => prePlan;
    public int SubPrePlanAt => prePlanAt;
    internal void SubSetPre(object? p, int at) { prePlan = (PackingPlan?)p; prePlanAt = at; }
    public void SubSetNodeLimit(long limit) => nodeLimitOverride = limit;
    public long SubNodeLimit => nodeLimitOverride;
    /// <summary>Bytes held by this sub-search's tracked structures (for the factor lane's peak-memory report).</summary>
    public long SubBytes => fa2On ? (long)Fa2Bytes() : (long)(nodes.Bytes + mmv.Bytes + mmvScore.Bytes + maux.Bytes);

    /// <summary>Macro / novelty engine: runs (or resumes) until nodeLimit expansions in total or the clock runs out.</summary>
    public SolveResult SubRunMacro(double remainingSec) => RunMacro(remainingSec);

    /// <summary>fa2 engine body (run on its own thread with a baton; pauses every 1024 expansions).</summary>
    public SolveResult SubRunFa2(double remainingSec) => RunFa2(remainingSec);
    internal void SubAttachBaton(Baton b, int me) { bat = b; batMe = me; }
}
