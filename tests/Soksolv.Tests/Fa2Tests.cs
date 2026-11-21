using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class Fa2Tests
{
    static FullOptions Opt(long nodes = 0) => new() { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedDeadZone = new Search.DeadZone.DeadZoneTable(18) };

    // Golden values from the Go solver: `soksolv -strategy fa2 -procs 1 -nodes 20000` (solved, nodes_expanded, pushes), default fa2 configuration.
    // Generated with bench/lane-parity.py (see docs/lanes.md).
    [Theory]
    [InlineData("Microban_155_31")]
    [InlineData("Holland_81_27")]
    [InlineData("SokEvo_107_55")]
    [InlineData("GrigrSpecial_40_3")]
    public void Fa2_SolvesAndVerifies(string level)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var rows = TestData.Rows(level);
            var r = FullSolver.SolveFa2(rows, Opt(), 60);
            Assert.True(r.Solved, r.Reason);
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Theory]
    [InlineData("Microban_155_31", 4, 6)]
    [InlineData("Holland_81_27", 4260, 71)]
    [InlineData("SokEvo_107_55", 665, 42)]
    public void Fa2_DefaultConfig_MatchesGoCounts(string level, int nodes, int pushes)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var r = FullSolver.SolveFa2(TestData.Rows(level), Opt(20000), 60);
            Assert.True(r.Solved, r.Reason);
            Assert.Equal(nodes, r.Nodes);
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Theory]
    [InlineData("Microban_155_31", 4, 6)]
    [InlineData("Holland_81_27", 270, 83)]
    [InlineData("SokEvo_107_55", 695, 48)]
    public void Fa2_PlanOldConfig_MatchesGoCounts(string level, int nodes, int pushes)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var o = Opt(20000);
            o.Fa2.Plan = "old"; o.Fa2.Rooms = false; o.Fa2.Hall = false; o.Fa2.HotSides = false;   // Go: -fa2-plan old -fa2-rooms=false -fa2-hall=false -hot-sides=false
            var r = FullSolver.SolveFa2(TestData.Rows(level), o, 60);
            Assert.True(r.Solved, r.Reason);
            Assert.Equal(nodes, r.Nodes);
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }
}
