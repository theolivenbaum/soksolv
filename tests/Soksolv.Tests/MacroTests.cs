using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

/// <summary>
/// Macro lane Stage 1 (rooms/hotspots/access/plan off) vs the patched Go reference (SOKSOLV_STAGE1=1, see docs/macro.md).
/// The fingerprint covers every applied (node, local move, child) of the whole run.
/// </summary>
public class MacroTests
{
    static SolveResult Run(string level, long nodes, int stage = 1, int features = 0)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped, f = FullSolver.MacroFingerprint;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false; FullSolver.MacroFingerprint = true;
        try
        {
            return FullSolver.SolveMacro(TestData.Rows(level), new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16), MacroStage = stage, MacroFeatures = features }, 120);
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; FullSolver.MacroFingerprint = f; }
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

    // Stage 3: + sink packing plan (packed/progress from plan phases, plan-aware out-of-plan); Go reference SOKSOLV_STAGE=3.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=10 generated=39 duplicates=1 hash=9e8711526d82ca62")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=77 generated=1359 duplicates=23 hash=c62fd279ed17922d")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=507 generated=9048 duplicates=444 hash=7844f166ce20aba4")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=6778 generated=110399 duplicates=11376 hash=acf94e923ba8ed9e")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=440 stored=124 generated=763 duplicates=286 hash=269b90c120e8bca2")]
    [InlineData("Microban_155_65", 20000, "expanded=20 stored=13 generated=157 duplicates=3 hash=7dcbfcd07930fd48")]
    [InlineData("SokEvo_107_55", 20000, "expanded=18964 stored=2211 generated=21307 duplicates=14130 hash=4e7fccb43fb6f845")]
    [InlineData("Sven_1623_100", 20000, "expanded=227 stored=80 generated=764 duplicates=127 hash=612fb2c43ab5c06c")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5335 generated=31561 duplicates=11904 hash=3ee364e894258ab3")]
    public void Stage3_MatchesGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 3).Fingerprint);

    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("SokEvo_107_55")]
    [InlineData("Aymeric_Du_Peloux_282_22")]
    public void Stage3_SolutionsVerify(string level)
    {
        var r = Run(level, 0, 3);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }

    // Stage 4: + hotspots (hot term, hotspot + opener advisors); Go reference SOKSOLV_STAGE=4.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=11 generated=63 duplicates=0 hash=c809ab948f6f2e4a")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=70 generated=1172 duplicates=27 hash=59d4eb9faf5afa55")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=515 generated=8797 duplicates=427 hash=596966dd718db7f8")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=7562 generated=139397 duplicates=11102 hash=295e83ef1fb54c42")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=588 stored=161 generated=914 duplicates=382 hash=af10ee5f1d3ae17d")]
    [InlineData("Microban_155_65", 20000, "expanded=15 stored=9 generated=151 duplicates=3 hash=accefd3249743d0c")]
    [InlineData("SokEvo_107_55", 20000, "expanded=10689 stored=1476 generated=13823 duplicates=7597 hash=393f5482e09534c7")]
    [InlineData("Sven_1623_100", 20000, "expanded=401 stored=113 generated=1039 duplicates=234 hash=9db947c73a0eb5b7")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5567 generated=32668 duplicates=11626 hash=ef5b3ef1595a9537")]
    public void Stage4_MatchesGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 4).Fingerprint);

    // Stage 5: + move access (mobility term, explorer advisor); Go reference SOKSOLV_STAGE=5.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=11 generated=93 duplicates=0 hash=bf6382948a89f0cc")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=79 generated=1994 duplicates=22 hash=040cc00bd626b404")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=440 generated=10345 duplicates=476 hash=641059e8c9882a11")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=8492 generated=179938 duplicates=10209 hash=1e42520139ed84b7")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=456 stored=136 generated=767 duplicates=287 hash=f8efdd694d980645")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=3dad979af95d13b7")]
    [InlineData("SokEvo_107_55", 20000, "expanded=11051 stored=1531 generated=14197 duplicates=7852 hash=8da225e53f0080ad")]
    [InlineData("Sven_1623_100", 20000, "expanded=223 stored=83 generated=832 duplicates=120 hash=97a34e29aca97af0")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5322 generated=31376 duplicates=11894 hash=c759168c4b7d1a38")]
    public void Stage5_MatchesGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5).Fingerprint);

    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("SokEvo_107_55")]
    [InlineData("Aymeric_Du_Peloux_282_22")]
    public void Stage5_SolutionsVerify(string level)
    {
        var r = Run(level, 0, 5);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }

    // Stage 6a: + move windows / reopen (feature bit 0); Go reference SOKSOLV_STAGE=5 SOKSOLV_FEAT=1.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=11 generated=93 duplicates=0 hash=bf6382948a89f0cc")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=79 generated=1994 duplicates=22 hash=040cc00bd626b404")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=440 generated=10897 duplicates=476 hash=300e374db99fb681")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=8498 generated=192081 duplicates=10205 hash=bc8d3b43159ffbd0")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=456 stored=136 generated=815 duplicates=287 hash=9817808ec55500e5")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=3dad979af95d13b7")]
    [InlineData("SokEvo_107_55", 20000, "expanded=10879 stored=1540 generated=24461 duplicates=7683 hash=43a63d1a4554efd3")]
    [InlineData("Sven_1623_100", 20000, "expanded=223 stored=83 generated=936 duplicates=120 hash=3a60def86b029920")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=5349 generated=45598 duplicates=11840 hash=6deb6265aa6b16bf")]
    public void Stage6a_MoveWindows_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 1).Fingerprint);
}
