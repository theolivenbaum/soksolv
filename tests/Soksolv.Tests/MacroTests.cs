using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

/// <summary>
/// Macro lane Stage 1 (rooms/hotspots/access/plan off) vs the patched Go reference (SOKSOLV_STAGE1=1, see docs/macro.md).
/// The fingerprint covers every applied (node, local move, child) of the whole run.
/// </summary>
public class MacroTests
{
    static SolveResult Run(string level, long nodes, int stage = 1)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped, f = FullSolver.MacroFingerprint; int st0 = FullSolver.MacroStage;
        FullSolver.MacroStage = stage;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; FullSolver.MacroFingerprint = true;
        try
        {
            return FullSolver.SolveMacro(TestData.Rows(level), new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16) }, 120);
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; FullSolver.MacroFingerprint = f; FullSolver.MacroStage = st0; }
    }

    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=11 generated=91 duplicates=0 hash=55e285e4d0d5b214")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=66 generated=963 duplicates=22 hash=8c4103d62227f4f5")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=472 generated=6879 duplicates=425 hash=d412d36a2ef065b6")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=5509 generated=74543 duplicates=11607 hash=230b9cb99ecc355f")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=1820 stored=342 generated=1936 duplicates=1348 hash=e2b8f211fa9b173e")]
    [InlineData("Microban_155_65", 20000, "expanded=142 stored=74 generated=1740 duplicates=48 hash=807b33d6a3fcd690")]
    [InlineData("SokEvo_107_55", 20000, "expanded=12504 stored=1704 generated=15926 duplicates=8947 hash=d11cec631d43448f")]
    [InlineData("Sven_1623_100", 20000, "expanded=20000 stored=3624 generated=22905 duplicates=15694 hash=be55d4dd9f056224")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5342 generated=29035 duplicates=12009 hash=aea20e6d564b5ccb")]
    public void Stage1_MatchesGoReferenceFingerprint(string level, long nodes, string golden)
    {
        var r = Run(level, nodes);
        Assert.Equal(golden, r.Fingerprint);
    }

    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("Aymeric_Du_Peloux_282_22")]
    [InlineData("SokEvo_107_55")]
    public void Stage1_SolutionsVerify(string level)
    {
        var r = Run(level, 0);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }

    // Stage 2: + rooms (room connectivity term/advisor) and packing basin (out-of-plan term/advisor); Go reference SOKSOLV_STAGE=2.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=11 generated=88 duplicates=0 hash=55e285e4d0d5b214")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=70 generated=1102 duplicates=25 hash=44bfbe5e9657d122")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=408 generated=6186 duplicates=513 hash=75fcab7ceb697060")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=6145 generated=87118 duplicates=10983 hash=d67ab24a6c34668a")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=1837 stored=337 generated=1905 duplicates=1361 hash=9023fa724156f7db")]
    [InlineData("Microban_155_65", 20000, "expanded=288 stored=81 generated=1980 duplicates=146 hash=a9df6a75cf1922b8")]
    [InlineData("SokEvo_107_55", 20000, "expanded=11270 stored=1589 generated=14404 duplicates=8048 hash=3658202955f8572b")]
    [InlineData("Sven_1623_100", 20000, "expanded=12242 stored=2548 generated=16317 duplicates=9219 hash=f6698aa3bdc58832")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5209 generated=26450 duplicates=12187 hash=9b5fd3efbdfb7f19")]
    public void Stage2_MatchesGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 2).Fingerprint);

    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("SokEvo_107_55")]
    public void Stage2_SolutionsVerify(string level)
    {
        var r = Run(level, 0, 2);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }
}
