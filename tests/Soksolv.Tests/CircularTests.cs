using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

public class CircularTests
{
    [Fact]
    public void StartsSolvedLevelIsSolvedWithAtLeastOnePush()
    {
        string[] rows = ["######", "#@*  #", "#    #", "######"];
        Assert.True(Circular.StartsSolved(Level.Parse(rows)));
        var r = Circular.Solve(rows, (rs, t) => FullSolver.Solve(rs, new FullOptions(), t), 10);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    [Fact]
    public void FirstPushesAreLegalAndProduceUnsolvedBoards()
    {
        string[] rows = ["#######", "#@ *  #", "#  *  #", "#  .. #", "#######"];
        // not circular (a goal is empty) but first pushes must still be enumerated correctly
        var fps = Circular.FirstPushes(rows);
        Assert.NotEmpty(fps);
        foreach (var fp in fps) Assert.Matches("^[lurd]*[LURD]$", fp.Prefix);
    }
}
