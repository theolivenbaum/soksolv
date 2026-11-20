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
            return FullSolver.SolveMacro(TestData.Rows(level), new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedDeadZone = new Soksolv.Search.DeadZone.DeadZoneTable(18), MacroStage = stage, MacroFeatures = features }, 120);
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

    // 6b: corral deadzone proofs + frontier corral (feature bit 1) on top of move windows (bit 0): goldens from the patched Go reference (SOKSOLV_FEAT=3).
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=8 generated=45 duplicates=0 hash=160b92e1ba625730")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=39 generated=465 duplicates=7 hash=e49077e8804a11ac")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=311 generated=6400 duplicates=261 hash=0c3197838916ad90")]
    [InlineData("Holland_81_27", 20000, "expanded=3580 stored=1195 generated=23546 duplicates=1086 hash=86fc489e460dc5d2")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=348 stored=94 generated=622 duplicates=192 hash=6295dfc3805dc2e8")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=3dad979af95d13b7")]
    [InlineData("SokEvo_107_55", 20000, "expanded=4226 stored=655 generated=11342 duplicates=2552 hash=d6e2f8aa390a80f8")]
    [InlineData("Sven_1623_100", 20000, "expanded=67 stored=35 generated=462 duplicates=17 hash=3b715637ed5c8c10")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=2771 generated=57220 duplicates=8631 hash=76bb9f01711fde5d")]
    public void Stage6b_DeadzoneAndFrontierCorral_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 3).Fingerprint);

    [Theory]
    [InlineData("Holland_81_27")]
    [InlineData("SokEvo_107_55")]
    [InlineData("Microban_155_65")]
    public void Stage6b_SolutionsVerify(string level)
    {
        var r = Run(level, 200000, 5, 3);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(TestData.Rows(level), r.Solution));
    }

    // 6c: displacement landmarks (feature bit 2) on top of bits 0-1: goldens from the patched Go reference (SOKSOLV_FEAT=7).
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=7 generated=33 duplicates=0 hash=d8636b47459d9b38")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=44 generated=666 duplicates=16 hash=52d7ccc19d97487e")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=328 generated=6771 duplicates=316 hash=633e5fc8d7352114")]
    [InlineData("Holland_81_27", 20000, "expanded=5826 stored=1711 generated=37843 duplicates=2271 hash=a32d5bfd9cb845b5")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=326 stored=92 generated=611 duplicates=175 hash=849ac3741c78b81b")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=3dad979af95d13b7")]
    [InlineData("SokEvo_107_55", 20000, "expanded=4221 stored=655 generated=11309 duplicates=2550 hash=0a983bbc3be269b6")]
    [InlineData("Sven_1623_100", 20000, "expanded=67 stored=35 generated=462 duplicates=18 hash=3b715637ed5c8c10")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=2592 generated=58520 duplicates=9010 hash=054149d412b1441d")]
    [InlineData("Sasquatch_VI_50_24", 20000, "expanded=20000 stored=6906 generated=235264 duplicates=6852 hash=f47e39482c4ebba0")]
    public void Stage6c_Displacement_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 7).Fingerprint);

    // 6d (corridor flow, bit 3) + 6e (packing obligations/causal DAG, bit 4) on top of bits 0-2: goldens from the patched Go reference (SOKSOLV_FEAT=31).
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=6 generated=26 duplicates=1 hash=5d67c10129207748")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=48 generated=713 duplicates=16 hash=0efab8aaa13fa41c")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=338 generated=7265 duplicates=294 hash=4f2477acb8105e90")]
    [InlineData("Holland_81_27", 20000, "expanded=3438 stored=1154 generated=27096 duplicates=1193 hash=27f76d42d3dc2866")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=302 stored=94 generated=620 duplicates=157 hash=a05bca34a5467b35")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=3dad979af95d13b7")]
    [InlineData("SokEvo_107_55", 20000, "expanded=4357 stored=578 generated=10261 duplicates=2780 hash=dd13a9e18955a485")]
    [InlineData("Sven_1623_100", 20000, "expanded=73 stored=37 generated=473 duplicates=21 hash=4d2db01fc1479bcd")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=2701 generated=59635 duplicates=8717 hash=36037fa03fb8c8a7")]
    [InlineData("Sasquatch_VI_50_24", 20000, "expanded=20000 stored=8550 generated=298977 duplicates=5049 hash=da7919fa75ea1dc4")]
    [InlineData("CorridorFlow_synth", 5000, "expanded=21 stored=12 generated=99 duplicates=8 hash=6173fae931a53589")]
    public void Stage6e_ObligationsAndCorridor_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 31).Fingerprint);

    // Stage 6f: + plan4 (Festival-prefix packed from the BASE_SEARCH-style planner); Go reference SOKSOLV_STAGE=5 SOKSOLV_FEAT=63.
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=6 generated=26 duplicates=1 hash=8855f1ebf3adc5ff")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=50 generated=1202 duplicates=12 hash=2a004d7c92efb4a5")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=354 generated=8359 duplicates=292 hash=186c5266c28fd8d7")]
    [InlineData("Holland_81_27", 20000, "expanded=20000 stored=5575 generated=125087 duplicates=7867 hash=692e9cfd78d74f17")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=542 stored=111 generated=1031 duplicates=284 hash=3ff720138d701752")]
    [InlineData("Microban_155_65", 20000, "expanded=14 stored=8 generated=147 duplicates=3 hash=c0342cf2f581c497")]
    [InlineData("SokEvo_107_55", 20000, "expanded=4048 stored=575 generated=9989 duplicates=2573 hash=1b323aae99f8211d")]
    [InlineData("Sven_1623_100", 20000, "expanded=3514 stored=785 generated=8920 duplicates=1625 hash=0652c58cb5a98115")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=3011 generated=55400 duplicates=7959 hash=9c9d35a4f13bb4a1")]
    [InlineData("Sasquatch_VI_50_24", 20000, "expanded=20000 stored=6430 generated=210105 duplicates=5550 hash=37baccdb8b2cd587")]
    [InlineData("GrigrSpecial_40_3", 20000, "expanded=20000 stored=2741 generated=74606 duplicates=10973 hash=19a2ea290919e1e6")]
    [InlineData("Holland_81_43", 20000, "expanded=20000 stored=2692 generated=56913 duplicates=10330 hash=cdc4403074e988fa")]
    public void Stage6f_Plan4_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 63).Fingerprint);

    // Stage 6g: + pair-side deadlocks (bit 7) and reversible preparation groups (bit 6): Go reference SOKSOLV_STAGE=5 SOKSOLV_FEAT=255 (single engine).
    [Theory]
    [InlineData("Holland_81_27", 1, "expanded=1 stored=2 generated=8 duplicates=0 hash=a944444f3a9250ce")]
    [InlineData("Holland_81_27", 10, "expanded=10 stored=6 generated=26 duplicates=1 hash=8855f1ebf3adc5ff")]
    [InlineData("Holland_81_27", 100, "expanded=100 stored=56 generated=1119 duplicates=4 hash=8287704de7a44127")]
    [InlineData("Holland_81_27", 1000, "expanded=1000 stored=418 generated=10252 duplicates=208 hash=699323bb5f10640c")]
    [InlineData("Holland_81_27", 14873, "expanded=14873 stored=4266 generated=97216 duplicates=5766 hash=0b58ea6c59b68815")]
    [InlineData("Holland_81_27", 20000, "expanded=14873 stored=4266 generated=97216 duplicates=5766 hash=0b58ea6c59b68815")]
    [InlineData("Aymeric_Du_Peloux_282_22", 20000, "expanded=514 stored=108 generated=1002 duplicates=275 hash=a268ff38650d9706")]
    [InlineData("Microban_155_65", 20000, "expanded=7 stored=8 generated=133 duplicates=0 hash=b745719bde76385e")]
    [InlineData("SokEvo_107_55", 20000, "expanded=3807 stored=667 generated=9574 duplicates=2456 hash=0c40c04b2b9a65ed")]
    [InlineData("Sven_1623_100", 20000, "expanded=3384 stored=783 generated=8328 duplicates=1555 hash=087fe4aca71c946a")]
    [InlineData("SokHard_163_36", 20000, "expanded=20000 stored=3167 generated=57597 duplicates=7504 hash=01d5cef93ea1048c")]
    [InlineData("Sasquatch_VI_50_24", 20000, "expanded=20000 stored=8235 generated=279590 duplicates=4005 hash=add3f11d6ea8befb")]
    [InlineData("GrigrSpecial_40_3", 20000, "expanded=20000 stored=2839 generated=66582 duplicates=11902 hash=5b6e80fb05115ced")]
    [InlineData("Holland_81_43", 20000, "expanded=20000 stored=3241 generated=53936 duplicates=11682 hash=33c3d1a83cfbeaad")]
    public void Stage6g_RevGroupsPairSide_MatchGoReferenceFingerprint(string level, long nodes, string golden)
        => Assert.Equal(golden, Run(level, nodes, 5, 255).Fingerprint);

    // The whole default lane (two engines on the baton, alt plan4 config, every feature) vs unpatched `soksolv -strategy macro -procs 1 -nodes N`.
    [Theory]
    [InlineData("Microban_155_65", 4000, true, 41, 14)]
    [InlineData("SokEvo_107_55", 4000, true, 38, 3304)]
    [InlineData("SokEvo_107_55", 40000, true, 38, 4376)]
    [InlineData("Holland_81_27", 4000, false, 0, 4000)]
    [InlineData("Holland_81_27", 40000, true, 57, 22041)]
    [InlineData("Sven_1623_100", 40000, true, 35, 6456)]
    [InlineData("Holland_81_43", 4000, false, 0, 4000)]
    [InlineData("Aymeric_Du_Peloux_282_22", 40000, true, 50, 998)]
    public void MacroLane_EndToEnd_MatchesGoDefaultLane(string level, long nodes, bool solved, int pushes, int expanded)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var rows = TestData.Rows(level);
            var r = FullSolver.SolveMacroLane(rows, new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedDeadZone = new Soksolv.Search.DeadZone.DeadZoneTable(18) }, 120);
            Assert.Equal(solved, r.Solved);
            Assert.Equal(expanded, r.Nodes);
            if (solved) { Assert.Equal(pushes, r.Solution.Count(char.IsUpper)); Assert.Null(Verifier.CheckSolution(rows, r.Solution)); }
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }
}
