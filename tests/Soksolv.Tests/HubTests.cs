using System.Text.Json;
using Soksolv.Model;
using Soksolv.Search.Hub;

namespace Soksolv.Tests;

public class HubTests
{
    static string Golden(string name)
    {
        var d = new DirectoryInfo(AppContext.BaseDirectory);
        while (d != null && !Directory.Exists(Path.Combine(d.FullName, "golden"))) d = d.Parent;
        return Path.Combine(d!.FullName, "golden", name);
    }

    /// <summary>
    /// Golden values dumped by Go (hubdump_test.go on a scratch copy of .reference: newHubReach on the pull view of
    /// each testdata level) — nCls/need/inU, checksums of every per-box reach set and class mask, matching probes,
    /// class counts at the start and Ok() pull-walk acceptance.
    /// </summary>
    [Fact]
    public void HubReach_MatchesGoGoldenOnAllTestdata()
    {
        using var doc = JsonDocument.Parse(File.ReadAllText(Golden("hubreach.json")));
        int checkedLevels = 0;
        foreach (var prop in doc.RootElement.EnumerateObject())
        {
            var rows = TestData.Rows(Path.GetFileNameWithoutExtension(prop.Name));
            var lv = Level.Parse(rows);
            var start = lv.Boxes.OrderBy(x => x).ToArray();
            var r = HubReach.Create(lv.Wall, lv.W, start, lv.Player);
            if (prop.Value.ValueKind == JsonValueKind.Null) { Assert.Null(r); continue; }
            Assert.NotNull(r);
            var g = prop.Value;
            Assert.Equal(g.GetProperty("nCls").GetInt32(), r!.NCls);
            Assert.Equal(g.GetProperty("need").EnumerateArray().Select(x => x.GetInt32()), r.Need);
            Assert.Equal(g.GetProperty("inU").GetInt32(), r.InU.Count(b => b));
            ulong sumRb = 0, pcRb = 0;
            for (int bi = 0; bi < r.Rb.Length; bi++)
                for (int w = 0; w < r.Rb[bi].Length; w++)
                {
                    sumRb += r.Rb[bi][w] * (ulong)(2 * w + 1) * (ulong)(bi + 3);
                    pcRb += (ulong)System.Numerics.BitOperations.PopCount(r.Rb[bi][w]);
                }
            Assert.Equal(g.GetProperty("sumRb").GetUInt64(), sumRb);
            Assert.Equal(g.GetProperty("pcRb").GetUInt64(), pcRb);
            ulong cm = 0; for (int c = 0; c < r.CellMask.Length; c++) cm += r.CellMask[c] * (ulong)(c + 1);
            Assert.Equal(g.GetProperty("cmSum").GetUInt64(), cm);

            var goals = Enumerable.Range(0, lv.Cells).Where(c => lv.Goal[c]).ToArray();
            var probes = g.GetProperty("probes");
            Assert.Equal(probes.GetProperty("goals").GetBoolean(), r.Feasible(goals));
            Assert.Equal(probes.GetProperty("start").GetBoolean(), r.Feasible(lv.Boxes));
            var shifted = (int[])goals.Clone();
            for (int c = lv.Cells - 1; c >= 0; c--) if (!lv.Wall[c] && !lv.Goal[c]) { shifted[0] = c; break; }
            Assert.Equal(probes.GetProperty("shifted").GetBoolean(), r.Feasible(shifted));

            Span<int> cnt = stackalloc int[64];
            r.Counts(lv.Boxes.Select(b => (ushort)b).ToArray(), cnt);
            Assert.Equal(g.GetProperty("cnt").EnumerateArray().Select(x => x.GetInt32()), cnt[..r.NCls].ToArray());
            int ok = 0;
            foreach (int b in lv.Boxes)
                foreach (int d in new[] { -lv.W, lv.W, -1, 1 })
                    if (!lv.Wall[b + d] && r.Ok(b, b + d, cnt)) ok++;
            Assert.Equal(g.GetProperty("okMoves").GetInt32(), ok);
            checkedLevels++;
        }
        Assert.True(checkedLevels >= 15);
    }
}
