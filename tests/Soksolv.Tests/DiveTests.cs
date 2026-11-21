using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class DiveTests
{
    static SolveResult Run(string level, long nodes)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try { return FullSolver.SolveDive(TestData.Rows(level), new FullOptions { NodeBudget = nodes }, 0); }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Fact] public void GateIsPeelDepthAtLeastTen()
    {
        Assert.True(FullSolver.DiveMaxPeel(Level.Parse(TestData.Rows("Dive_Sven_1623_197"))) >= 10);
        Assert.True(FullSolver.DiveMaxPeel(Level.Parse(TestData.Rows("Holland_81_27"))) < 10);
    }

    // Golden values from the Go solver: `soksolv -strategy dive -procs 1 -nodes N` (nodes_expanded, pushes).
    [Theory]
    [InlineData("Microban_155_31", 2000, true, 4, 8)]
    [InlineData("SokEvo_107_55", 2000, false, 1000, 0)]
    [InlineData("Dive_Sven_1623_197", 20000, true, 6893, 1016)]
    [InlineData("Dive_Grigr2001_100_48", 20000, true, 9084, 174)]
    public void ParityMode_MatchesGo(string level, long nodes, bool solved, long expNodes, int pushes)
    {
        var r = Run(level, nodes);
        Assert.Equal(solved, r.Solved);
        Assert.Equal(expNodes, r.Nodes);
        Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
        if (solved) Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }
}
