using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class PullTests
{
    static FullOptions Opt(long nodes) => new() { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };

    // Golden values from Go: `soksolv -strategy pull -procs 1 -nodes 5000` (solved, pushes, nodes_expanded, stored, generated, duplicates).
    [Theory]
    [InlineData("Aymeric_Du_Peloux_282_22", true, 36, 3281, 824, 5125, 2461)]
    [InlineData("Microban_155_31", true, 10, 47, 20, 170, 28)]
    [InlineData("Microban_155_65", false, 0, 5000, 1053, 23821, 3948)]
    [InlineData("Holland_81_27", false, 0, 5000, 3686, 48475, 1320)]
    [InlineData("SokEvo_107_55", false, 0, 5000, 2456, 20806, 2547)]
    [InlineData("Sasquatch_IV_50_42", false, 0, 5000, 3237, 111296, 1767)]
    [InlineData("Sven_1623_100", false, 0, 5000, 1692, 14329, 3310)]
    public void Pull_MatchesGoCounts(string level, bool solved, int pushes, int expanded, int stored, int generated, int duplicates)
    {
        var rows = TestData.Rows(level);
        var r = FullSolver.SolvePull(rows, Opt(5000), 0);
        Assert.Equal(solved, r.Solved);
        Assert.Equal(expanded, r.Nodes);
        Assert.Equal(stored, r.Stats.Stored);
        Assert.Equal(generated, r.Stats.Generated);
        Assert.Equal(duplicates, r.Stats.Duplicates);
        if (solved)
        {
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
    }
}
