using Soksolv.Model;
using Soksolv.Search;
using Soksolv.Search.Meet;

namespace Soksolv.Tests;

public class MeetTests
{
    [Fact]
    public void Table_KeyIsOrderIndependentAndNonZero_AddHasWork()
    {
        var t = new Table(200, 12, 2);
        int[] a = [10, 20, 30], b = [30, 10, 20];
        Assert.Equal(t.Key(a, 5), t.Key(b, 5));
        Assert.NotEqual(t.Key(a, 5), t.Key(a, 6));
        Assert.NotEqual(0UL, t.Key([], 0));
        ulong k = t.Key(a, 5);
        Assert.False(t.Has(k));
        t.Add(k);
        Assert.True(t.Has(k));
        for (int i = 0; i < 100000; i++) t.Add(t.Key([i % 200, (i * 7) % 200], i % 50));   // growth path
        Assert.True(t.Has(t.Key([0, 0], 0)));
    }

    [Fact]
    public void Table_MailboxesDeliverPerOwnerAndDropWhenFull()
    {
        var t = new Table(10, 10, 2);
        for (int i = 0; i < 70; i++) t.Post(new Meeting { Player = i });
        Assert.True(t.Dropped >= 12);               // 64-slot mailboxes, 2 owners
        int got = 0; while (t.Receive(0, out _)) got++;
        Assert.Equal(64, got);
        Assert.True(t.Receive(1, out var m)); Assert.Equal(0, m.Player);
    }

    [Fact]
    public void Complete_WalksBetweenPushesAndRejectsBlockedOnes()
    {
        string[] rows = ["#######", "#@ $ .#", "#######"];
        var lv = Level.Parse(rows, closeHoles: false);
        int box = lv.Boxes[0]; int w = lv.W;
        // push right twice: box at x=3 -> goal at x=5
        string s = MeetNet.Complete(lv.Wall, w, lv.Boxes, lv.Player, [new MPush(box, 3), new MPush(box + 1, 3)]);
        Assert.Equal("rRR", s);
        Assert.Null(Verifier.CheckSolution(rows, s));
        // pushing into a wall is rejected
        Assert.Equal("", MeetNet.Complete(lv.Wall, w, lv.Boxes, lv.Player, [new MPush(box, 0)]));
        Assert.Equal(lv.Player + 3, MeetNet.EndCell("rrr", lv.Player, w));
    }

    [Theory]
    [InlineData("Holland_81_27")]
    [InlineData("SokEvo_107_82")]
    [InlineData("GrigrSpecial_40_3")]
    public void PortfolioWithMeetings_SolvesAndVerifies(string level)
    {
        var rows = TestData.Rows(level);
        var r = Portfolio.Solve(rows, new PortfolioOptions { MemBytes = 2048UL << 20, Meet = true, Macro = false, Novelty = false, Pull = false }, 60);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }
}
