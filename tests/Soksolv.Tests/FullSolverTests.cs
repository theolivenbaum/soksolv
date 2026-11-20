using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

static class TestData
{
    public static string Dir
    {
        get
        {
            var d = new DirectoryInfo(AppContext.BaseDirectory);
            while (d != null && !Directory.Exists(Path.Combine(d.FullName, "testdata"))) d = d.Parent;
            return Path.Combine(d!.FullName, "testdata");
        }
    }
    public static string[] Rows(string name) => XsbCollection.ReadFile(Path.Combine(Dir, name + ".xsb"))[0];
}

public class FullSolverTests
{
    static FullOptions Parity() => new() { SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };

    // Golden values produced by the Go solver: `soksolv -strategy full -procs 1` (nodes_expanded, pushes).
    [Theory]
    [InlineData("Microban_155_31", 20, 8)]
    [InlineData("Microban_155_65", 175, 49)]
    [InlineData("SokEvo_107_55", 645, 36)]
    [InlineData("Holland_81_27", 4309, 53)]
    [InlineData("Sven_1623_100", 3243, 35)]
    public void ParityMode_MatchesGoNodeAndPushCounts(string level, int nodes, int pushes)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var rows = TestData.Rows(level);
            var r = FullSolver.Solve(rows, Parity(), 60);
            Assert.True(r.Solved, r.Reason);
            Assert.Equal(nodes, r.Nodes);
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("SokEvo_107_82")]
    [InlineData("GrigrSpecial_40_3")]
    public void FastMode_SolvesAndVerifies(string level)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = true; ClusterChecker.SkipCapped = true;
        try
        {
            var rows = TestData.Rows(level);
            var r = FullSolver.Solve(rows, Parity(), 60);
            Assert.True(r.Solved, r.Reason);
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Fact]
    public void ClusterFastRegionNormalization_AgreesWithFlood()
    {
        bool a = ClusterChecker.AStar, c = ClusterChecker.CheckFast;
        ClusterChecker.CheckFast = true; ClusterChecker.AStar = true;
        try
        {
            foreach (var lv in new[] { "Holland_81_27", "SokHard_163_23" })
            {
                var r = FullSolver.Solve(TestData.Rows(lv), Parity(), 60); // throws on any disagreement
                Assert.True(r.Solved);
            }
        }
        finally { ClusterChecker.CheckFast = c; ClusterChecker.AStar = a; }
    }

    [Fact]
    public void Verifier_RejectsBadSolutions()
    {
        string[] rows = ["######", "#@$ .#", "######"];
        Assert.Null(Verifier.CheckSolution(rows, "RR"));
        Assert.NotNull(Verifier.CheckSolution(rows, "R"));      // box not on goal
        Assert.NotNull(Verifier.CheckSolution(rows, "r"));      // no push
        Assert.NotNull(Verifier.CheckSolution(rows, "LL"));     // walks into wall / bad
    }

    [Fact]
    public void XsbSplit_FloorSpacerRowDoesNotSplitLevel()
    {
        var lv = XsbCollection.Split("; 1\n### #\n-\n#####\n#@$.#\n#####\n\n; 2\n####\n#@$.#\n####\n");
        Assert.Equal(2, lv.Count);
        Assert.Equal(5, lv[0].Length);
        Assert.Equal(3, lv[1].Length);
    }
}
