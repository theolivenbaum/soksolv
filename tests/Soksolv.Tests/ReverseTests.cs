using Soksolv.Model;
using Soksolv.Search;
using Soksolv.Search.Reverse;

namespace Soksolv.Tests;

public class ReverseTests
{
    // Golden node counts come from the Go reference binary (`-strategy reverse ... -nodes`), see docs/reverse.md.
    [Theory]
    [InlineData("Microban_155_31", 1, 0, 14, 6)]
    [InlineData("Sven_1623_100", 1, 0, 253, 35)]
    [InlineData("SokEvo_107_55", 1, 0, 29401, 36)]
    [InlineData("Microban_155_31", 1, 4, 4, 6)]
    [InlineData("Sven_1623_100", 1, 4, 210, 37)]
    [InlineData("Microban_155_31", 2, 4, 4, 6)]
    [InlineData("Sven_1623_100", 2, 4, 211, 37)]
    public void MatchesGoNodesAndPushes(string level, int lanes, int supply, long nodes, int pushes)
    {
        var rows = TestPaths.Level(level);
        var o = ReverseRunner.Config(lanes, supply);
        o.NodeBudget = 200000; // deterministic mode: cell scheduling switches on expansion count like Go's -nodes
        var r = ReverseRunner.Solve(rows, o, 60);
        Assert.True(r.Solved, r.Reason);
        Assert.Equal(nodes, r.Nodes);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
    }

    // Node-capped runs of the default lane (supply, macros, novelty, types, walks): expansions equal Go's (bench/lane-parity.py).
    [Theory]
    [InlineData("Holland_81_27", 99193)]
    [InlineData("Factor_SasquatchII_50_24", 97675)]
    [InlineData("SokHard_163_23", 98035)]
    [InlineData("Dive_Sven_1623_197", 95874)]
    public void CappedRunsMatchGoExpansions(string level, long nodes)
    {
        var o = ReverseRunner.Config(2, 4);
        o.NodeBudget = 100000;
        var r = ReverseRunner.Solve(TestPaths.Level(level), o, 120);
        Assert.False(r.Solved);
        Assert.Equal(nodes, r.Nodes);
    }

    // The search itself does not allocate on the GC heap: doubling the node cap adds (almost) no managed bytes.
    [Fact]
    public void SteadyStateDoesNotAllocate()
    {
        var rows = TestPaths.Level("Holland_81_27");
        long Run(long cap)
        {
            var o = ReverseRunner.Config(2, 4);
            o.NodeBudget = cap;
            long before = GC.GetAllocatedBytesForCurrentThread();
            ReverseRunner.Solve(rows, o, 120);
            return GC.GetAllocatedBytesForCurrentThread() - before;
        }
        Run(2000); // warm-up (JIT, statics)
        long small = Run(20000), large = Run(40000);
        Assert.True(large - small < 600_000, $"{small} B at 20k nodes, {large} B at 40k nodes");
    }

    [Theory]
    [InlineData("Holland_81_43")]
    [InlineData("GrigrSpecial_40_39")]
    [InlineData("Sasquatch_VI_50_24")]
    public void SolvesAndVerifies(string level)
    {
        var rows = TestPaths.Level(level);
        var r = ReverseRunner.Solve(rows, ReverseRunner.Config(2, 4), 60);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }
}

public class FullLaneTests
{
    [Theory]
    [InlineData("Microban_155_31", 20, 8)]
    [InlineData("SokEvo_107_55", 645, 36)]
    [InlineData("Holland_81_27", 4309, 53)]
    public void ParityModeMatchesGo(string level, long nodes, int pushes)
    {
        var rows = TestPaths.Level(level);
        var saved = (ClusterChecker.AStar, ClusterChecker.SkipCapped);
        try
        {
            ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
            var opt = new FullOptions { SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };
            var r = FullSolver.Solve(rows, opt, 60);
            Assert.True(r.Solved);
            Assert.Equal(nodes, r.Nodes);
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
        finally { (ClusterChecker.AStar, ClusterChecker.SkipCapped) = saved; }
    }
}

public class ModelTests
{
    [Fact]
    public void SplitsCollectionWithFloorSpacerRow()
    {
        var lv = XsbCollection.Split("; 1\n### #\n-\n#####\n#@$.#\n#####\n\n; 2\n####\n#@$.#\n####\n");
        Assert.Equal(2, lv.Count);
        Assert.Equal(5, lv[0].Length);
    }

    [Fact]
    public void VerifierRejectsNoPushAndAcceptsSolution()
    {
        string[] rows = ["######", "#@*  #", "#    #", "######"];
        Assert.NotNull(Verifier.CheckSolution(rows, ""));
        Assert.Null(Verifier.CheckSolution(rows, "RdrruL"));
    }
}
