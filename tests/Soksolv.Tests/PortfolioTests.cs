using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class PortfolioTests
{
    [Theory]
    [InlineData("Microban_155_65")]
    [InlineData("Holland_81_27")]
    [InlineData("SokEvo_107_82")]
    public void Portfolio_SolvesAndVerifies(string level)
    {
        var rows = TestData.Rows(level);
        var r = Portfolio.Solve(rows, new PortfolioOptions { MemBytes = 2048UL << 20 }, 60);
        Assert.True(r.Solved, r.Reason);
        Assert.Contains(r.Strategy, new[] { "full", "reverse" });
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    [Fact]
    public void Portfolio_HandlesStartsSolvedLevel()
    {
        string[] rows = ["######", "#@*  #", "#    #", "######"];
        var r = Portfolio.Solve(rows, new PortfolioOptions { MemBytes = 1024UL << 20 }, 30);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    [Fact]
    public void Portfolio_TimeoutReportsReasonsFromBothLanes()
    {
        var rows = TestData.Rows("Holland_81_57");
        var r = Portfolio.Solve(rows, new PortfolioOptions { MemBytes = 2048UL << 20, NodeBudget = 200 }, 5);
        Assert.False(r.Solved);
        Assert.Contains("full", r.Reason);
        Assert.Contains("reverse", r.Reason);
    }
}
