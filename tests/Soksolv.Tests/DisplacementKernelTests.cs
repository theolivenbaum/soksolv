using System.Reflection;
using Soksolv.Model;
using Soksolv.Search;

namespace Soksolv.Tests;

/// <summary>The row-bit canWalk kernel of the displacement precompute must give the same pinned goal masks as the BFS version.</summary>
public class DisplacementKernelTests
{
    [Theory]
    [InlineData("Holland_81_27")]
    [InlineData("Sasquatch_VI_50_24")]
    [InlineData("SokEvo_107_55")]
    [InlineData("Grigr2002_40_24")]
    [InlineData("Dive_Sven_1623_197")]
    [InlineData("Microban_155_65")]
    public void RowKernelEqualsBfs(string level)
    {
        var lv = Level.Parse(TestData.Rows(level));
        int[] dirs = [-lv.W, lv.W, -1, 1];
        var goals = new List<int>();
        for (int c = 0; c < lv.Goal.Length; c++) if (lv.Goal[c]) goals.Add(c);
        var f = typeof(DisplacementLandmarks).GetField("pinnedGoalMask", BindingFlags.NonPublic | BindingFlags.Instance)!;
        DisplacementLandmarks.RowKernelOff = true;
        ulong[][] want;
        try { want = (ulong[][])f.GetValue(DisplacementLandmarks.Create(lv, dirs, goals.ToArray())!)!; }
        finally { DisplacementLandmarks.RowKernelOff = false; }
        var got = (ulong[][])f.GetValue(DisplacementLandmarks.Create(lv, dirs, goals.ToArray())!)!;
        Assert.Equal(want.Length, got.Length);
        for (int i = 0; i < want.Length; i++) Assert.True(want[i].AsSpan().SequenceEqual(got[i]), $"pin {i}");
    }
}
