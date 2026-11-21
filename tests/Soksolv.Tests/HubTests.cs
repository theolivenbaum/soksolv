using System.Text.Json;
using Soksolv.Model;
using Soksolv.Search;
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

    static FullOptions PfOpt(long nodes) => new() { NodeBudget = nodes, PfOn = true, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };

    // Golden values from the patched Go reference (bench/pf-ref/build.sh): `SOKSOLV_PFON=1 soksolv -strategy pull -procs 1 -nodes 3000`
    // (solved, pushes, nodes_expanded, stored, generated, duplicates): Festival-ordered pull search (pfOn + festXStyle D).
    [Theory]
    [InlineData("Aymeric_Du_Peloux_282_22", false, 0, 3000, 776, 4779, 2225)]
    [InlineData("Microban_155_31", true, 6, 64, 28, 268, 37)]
    [InlineData("Microban_155_65", false, 0, 3000, 810, 20795, 2191)]
    [InlineData("Holland_81_27", false, 0, 3000, 2271, 43109, 732)]
    [InlineData("SokEvo_107_55", false, 0, 3000, 1825, 22914, 1176)]
    [InlineData("Sasquatch_IV_50_42", false, 0, 3000, 1984, 77072, 1019)]
    [InlineData("Sven_1623_100", false, 0, 3000, 777, 8333, 2225)]
    [InlineData("Holland_81_43", false, 0, 3000, 792, 15576, 2209)]
    public void PullFest_MatchesGoCounts(string level, bool solved, int pushes, int expanded, int stored, int generated, int duplicates)
    {
        var rows = TestData.Rows(level);
        var r = FullSolver.SolvePull(rows, PfOpt(3000), 0);
        Assert.Equal(solved, r.Solved);
        Assert.Equal(expanded, r.Nodes);
        Assert.Equal(stored, r.Stats.Stored);
        Assert.Equal(generated, r.Stats.Generated);
        Assert.Equal(duplicates, r.Stats.Duplicates);
        if (solved)
        {
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
    }

    // Go `-strategy hub-strata -hub-strata-dump F -procs 1 -nodes 3000`: byte-identical dump (ids, signatures, tuples, boards).
    [Theory]
    [InlineData("Holland_81_43")]
    [InlineData("Microban_155_65")]
    public void HubStrata_DumpMatchesGo(string level)
    {
        var rows = TestData.Rows(level);
        string tmp = Path.GetTempFileName();
        try
        {
            var r = FullSolver.SolveHubStrata(rows, PfOpt(3000), 0, tmp);
            Assert.Equal("hub-strata done", r.Reason);
            Assert.Equal(File.ReadAllText(Golden($"strata_{level}_3000.txt")), File.ReadAllText(tmp));
        }
        finally { File.Delete(tmp); }
    }

    // Golden values from the patched Go reference (bench/hub-ref/build.sh):
    // `SOKSOLV_NODESLICES=512 soksolv -strategy hub-pull -procs 1 -nodes 30000 -no-meet -retarget-reuse=false -lane-revive=false` (solved, pushes, HUBRACE counters).
    [Theory]
    [InlineData("Aymeric_Du_Peloux_282_22", true, 48, "pull=512 base=212 trials=0 fwd=0 strata=13 added=13 reached=0 ext=0 retries=0 shared=0 baseRuns=1")]
    [InlineData("Microban_155_31", true, 6, "pull=62 base=0 trials=0 fwd=0 strata=3 added=3 reached=0 ext=0 retries=0 shared=0 baseRuns=0")]
    [InlineData("Microban_155_65", true, 41, "pull=512 base=4 trials=0 fwd=0 strata=23 added=23 reached=0 ext=0 retries=0 shared=0 baseRuns=1")]
    [InlineData("Holland_81_27", false, 0, "pull=1328 base=20480 trials=2 fwd=8192 strata=64 added=91 reached=0 ext=0 retries=0 shared=1 baseRuns=1")]
    [InlineData("SokEvo_107_55", true, 36, "pull=1024 base=7120 trials=1 fwd=4096 strata=19 added=19 reached=0 ext=0 retries=0 shared=0 baseRuns=1")]
    [InlineData("Sven_1623_100", true, 41, "pull=512 base=1266 trials=0 fwd=0 strata=17 added=17 reached=0 ext=0 retries=0 shared=0 baseRuns=1")]
    [InlineData("Holland_81_43", false, 0, "pull=1328 base=20480 trials=2 fwd=8192 strata=40 added=40 reached=0 ext=0 retries=0 shared=0 baseRuns=1")]
    [InlineData("Sasquatch_IV_50_42", false, 0, "pull=1328 base=20480 trials=2 fwd=8192 strata=64 added=180 reached=0 ext=0 retries=0 shared=1 baseRuns=2")]
    public void HubPull_NodeSlicesMatchGo(string level, bool solved, int pushes, string fingerprint)
    {
        var rows = TestData.Rows(level);
        var o = new FullOptions { NodeBudget = 30000, HubNodeSlices = 512, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };
        var r = FullSolver.SolveHubPull(rows, o, 0);
        Assert.Equal(solved, r.Solved);
        Assert.Equal(fingerprint, r.Fingerprint);
        if (solved)
        {
            Assert.Equal(pushes, r.Solution.Count(char.IsUpper));
            Assert.Null(Verifier.CheckSolution(rows, r.Solution));
        }
    }

    // Wall-clock slicing (the default): the lane solves and every solution replays.
    [Theory]
    [InlineData("Microban_155_31")]
    [InlineData("Microban_155_65")]
    [InlineData("Sven_1623_100")]
    public void HubPull_WallClockSolvesAndVerifies(string level)
    {
        var rows = TestData.Rows(level);
        var o = new FullOptions { SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };
        var r = FullSolver.SolveHubPull(rows, o, 30);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    [Fact]
    public void Portfolio_WithHubPullLaneSolves()
    {
        var rows = TestData.Rows("Microban_155_65");
        var r = Portfolio.Solve(rows, new PortfolioOptions { MemBytes = 2048UL << 20, HubPull = true }, 60);
        Assert.True(r.Solved, r.Reason);
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }

    // Retarget reuse (Go default): a changed pull best retargets the base forward tree (baseRuns=2); golden from the node-slice Go reference
    // `SOKSOLV_NODESLICES=4000 soksolv -strategy hub-pull -procs 1 -nodes 150000 -no-meet -lane-revive=false`.
    [Fact]
    public void HubPull_RetargetReuseMatchesGo()
    {
        var rows = TestData.Rows("SokEvo_107_82");
        var o = new FullOptions { NodeBudget = 150000, HubNodeSlices = 4000, SharedClusters = new ClusterTable(16), SharedPenalties = new ClusterTable(16) };
        var r = FullSolver.SolveHubPull(rows, o, 0);
        Assert.True(r.Solved, r.Reason);
        Assert.Equal("pull=16000 base=40094 trials=3 fwd=12288 strata=17 added=17 reached=0 ext=0 retries=0 shared=0 baseRuns=2", r.Fingerprint);
        Assert.Equal(51, r.Solution.Count(char.IsUpper));
        Assert.Null(Verifier.CheckSolution(rows, r.Solution));
    }
}
