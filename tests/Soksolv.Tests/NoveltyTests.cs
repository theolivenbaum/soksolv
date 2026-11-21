using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class NoveltyTests
{
    static FullOptions Opt(long nodes) => new() { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16), SharedDeadZone = new Search.DeadZone.DeadZoneTable(18) };

    // Golden values from Go: `soksolv -strategy novelty -procs 1 -nodes 3000` (solved, pushes, nodes_expanded, stored, generated, duplicates).
    [Theory]
    [InlineData("Aymeric_Du_Peloux_282_22", true, 36, 152, 74, 442, 55)]
    [InlineData("Aymeric_Du_Peloux_282_273", false, 0, 3000, 397, 6784, 1157)]
    [InlineData("Holland_81_27", true, 109, 1808, 620, 14801, 475)]
    [InlineData("Microban_155_31", true, 6, 3, 4, 32, 0)]
    [InlineData("Microban_155_65", true, 41, 17, 15, 348, 3)]
    [InlineData("SokEvo_107_55", true, 38, 1526, 429, 4563, 785)]
    [InlineData("Sven_1623_100", true, 43, 119, 59, 655, 17)]
    [InlineData("Sasquatch_IV_50_42", false, 0, 3000, 1493, 40760, 1179)]
    public void Novelty_MatchesGoCounts(string level, bool solved, int pushes, int expanded, int stored, int generated, int duplicates)
    {
        bool a = ClusterChecker.AStar, k = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; // -parity: Go-identical cluster proofs
        try
        {
            var rows = TestData.Rows(level);
            var r = FullSolver.SolveNovelty(rows, Opt(3000), 0);
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
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = k; }
    }
}
