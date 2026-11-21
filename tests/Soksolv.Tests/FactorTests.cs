using Soksolv.Model;
using Soksolv.Search;
using Soksolv.Search.Factor;

namespace Soksolv.Tests;

public class FactorTests
{
    static SolveResult Run(string level, long nodes, bool parity = true)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        if (parity) { ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; }
        try
        {
            var rows = TestData.Rows(level);
            var lv = Level.Parse(rows);
            var eng = new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16), SharedDeadZone = new Search.DeadZone.DeadZoneTable(18) };
            return FactorLane.Run(lv, new FactorOptions { NodeBudget = nodes, Engine = eng, Solo = true }, 0);
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Fact] public void GateSelectsOnlyMultiComponentBoards()
    {
        Assert.True(FactorLane.Gate(Level.Parse(TestData.Rows("Factor_Microban_155_80"))));
        Assert.False(FactorLane.Gate(Level.Parse(TestData.Rows("Holland_81_27"))));
    }

    // Golden values from the Go solver: `soksolv -strategy factor -procs 1 -nodes N` (nodes_expanded, pushes).
    [Theory]
    [InlineData("Factor_Microban_155_80", 20000, true, 100, 46)]
    [InlineData("Factor_Sasquatch_50_8", 20000, true, 67, 33)]
    [InlineData("Factor_SasquatchII_50_24", 3000, false, 3000, 0)]
    public void ParityMode_MatchesGo(string level, long nodes, bool solved, long expNodes, int pushes)
    {
        var r = Run(level, nodes);
        Assert.Equal(solved, r.Solved);
        Assert.Equal(expNodes, r.Nodes);
        Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
        if (solved) Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }

    [Fact] public void NotGatedBoardIsRefused() => Assert.False(Run("Holland_81_27", 1000).Solved);
}
