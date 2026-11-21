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

public class PfFestTests
{
    static string Golden(string name)
    {
        var d = new DirectoryInfo(AppContext.BaseDirectory);
        while (d != null && !Directory.Exists(Path.Combine(d.FullName, "golden"))) d = d.Parent;
        return Path.Combine(d!.FullName, "golden", name);
    }

    /// <summary>Golden values from Go (pfdump_test.go in a scratch copy): pfDist checksum, and pfHot/pfBicon/pfSpreadSum on 8 pseudo-random box sets per level.</summary>
    [Fact]
    public void PullFest_LeafFunctions_MatchGo()
    {
        using var doc = System.Text.Json.JsonDocument.Parse(File.ReadAllText(Golden("pffest.json")));
        int levels = 0, samples = 0;
        foreach (var prop in doc.RootElement.EnumerateObject())
        {
            var lv = Soksolv.Model.Level.Parse(TestData.Rows(Path.GetFileNameWithoutExtension(prop.Name)));
            // pull view: boxes start on the goals (ascending)
            var goals = Enumerable.Range(0, lv.Cells).Where(c => lv.Goal[c]).ToArray();
            var pf = new Soksolv.Search.Hub.PfFest(lv.Wall, lv.W, goals);
            var g = prop.Value;
            Assert.Equal(g.GetProperty("ngoals").GetInt32(), pf.NGoals);
            long sum = 0, nn = 0;
            for (int i = 0; i < pf.PfDist.Length; i++) if (pf.PfDist[i] >= 0) { nn++; sum += (long)pf.PfDist[i] * (i % 997 + 1); }
            Assert.Equal(g.GetProperty("nonneg").GetInt64(), nn);
            Assert.Equal(g.GetProperty("sumDist").GetInt64(), sum);
            var occ = new bool[lv.Cells];
            foreach (var s in g.GetProperty("samples").EnumerateArray())
            {
                Array.Clear(occ);
                var boxes = s.GetProperty("boxes").EnumerateArray().Select(x => x.GetInt32()).ToArray();
                foreach (int b in boxes) occ[b] = true;
                Assert.Equal(s.GetProperty("hot").GetInt32(), pf.Hot(boxes, occ));
                Assert.Equal(s.GetProperty("bicon").GetInt32(), pf.Bicon(occ));
                Assert.Equal(s.GetProperty("spread").GetInt32(), pf.SpreadSum(boxes));
                samples++;
            }
            levels++;
        }
        Assert.True(levels >= 18 && samples >= 144);
    }

    [Fact]
    public void PullFest_TupleOrder_MatchesGo()
    {
        var a = new Soksolv.Search.Hub.PfFest.Tuple(1, 0, 2, 10, 0);
        var b = new Soksolv.Search.Hub.PfFest.Tuple(2, 0, 0, 99, 0);
        Assert.True(Soksolv.Search.Hub.PfFest.Tuple.Better(a, b));   // fewer regions first
        var c = new Soksolv.Search.Hub.PfFest.Tuple(1, 0, 1, 5, 0);
        Assert.True(Soksolv.Search.Hub.PfFest.Tuple.Better(c, a));   // one region: fewer hotspots
        var d = new Soksolv.Search.Hub.PfFest.Tuple(1, 0, 2, 11, 0);
        Assert.True(Soksolv.Search.Hub.PfFest.Tuple.Better(d, a));   // larger matching distance is better
        var m = a.Mobility;
        Assert.Equal(a, Soksolv.Search.Hub.PfFest.Tuple.FromMove(a.Reg, a.Rooms, a.Hot, m) with { });
    }
}
