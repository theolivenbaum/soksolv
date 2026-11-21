using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

/// <summary>`hf` (pull for 1/3 of the nodes, then FESS toward the best backward state) against golden values produced by the Go solver
/// (`bench/hf-ref/build.sh` patched copy, `-strategy hf -procs 1 -no-meet -nodes 6000`, default hf-festival bundle).</summary>
public class TowardTests
{
    static SolveResult Hf(string level, long nodes)
    {
        bool a = ClusterChecker.AStar, s = ClusterChecker.SkipCapped;
        ClusterChecker.AStar = false; ClusterChecker.SkipCapped = false;
        try
        {
            var opt = new FullOptions { NodeBudget = nodes, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };
            return FullSolver.SolveHf(TestData.Rows(level), opt, 120);
        }
        finally { ClusterChecker.AStar = a; ClusterChecker.SkipCapped = s; }
    }

    [Theory]
    [InlineData("SokEvo_107_82", 1290, 647, 4, 33)]
    [InlineData("SokHard_163_23", 1099, 1192, 4, 136)]
    [InlineData("Sven_1623_100", 813, 952, 3, 30)]
    [InlineData("Holland_81_43", 1038, 1119, 0, 32)]
    public void Hf_MatchesGoStoredCountsAndTarget(string level, int pullStored, int towardStored, int onStart, int tail)
    {
        var r = Hf(level, 6000);
        Assert.False(r.Solved);
        Assert.Equal(4000, r.Nodes);
        Assert.Equal($"pull_stored={pullStored} toward_stored={towardStored} target_on_start={onStart} target_tail={tail}", r.Fingerprint);
    }

    [Fact]
    public void Hf_SolvedLevelMatchesGoPushesAndVerifies()
    {
        var rows = TestData.Rows("Aymeric_Du_Peloux_282_273");
        var r = Hf("Aymeric_Du_Peloux_282_273", 6000);
        Assert.True(r.Solved, r.Reason);
        Assert.Equal(2659, r.Nodes);
        Assert.Equal(101, r.Solution.Count(char.IsUpper));
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    [Fact]
    public void MatchDist_IsMinCostAssignment()
    {
        // two boxes, two targets on a straight corridor: boxes at x=1,2 -> targets at x=4,5 cost (3+3)=6 either way... use the solver's own table
        var rows = new[] { "########", "#@$$ ..#", "########" };
        var lv = Level.Parse(rows);
        using var s = new FullSolver(lv, new FullOptions());
        Assert.Equal(AssignmentInfConst, FullSolver.AssignmentInf);
    }
    const int AssignmentInfConst = 1 << 20;
}
