using Soksolv.Model;
using Soksolv.Search.Hub;

namespace Soksolv.Search;

/// <summary>
/// Hub strata (Go hubstrata.go, exp-686): a stratum is a coarse signature of a pull node; the table keeps the best node (pull lane order,
/// pfBetter) per signature, at most StrataMax, so the hub lanes can try many distinct branches of the pull tree instead of one.
///   v0: hash(boxes on real goals, boxes per room, boxes in corridors)     v1: id of the node's ancestor at half its depth.
/// </summary>
public sealed unsafe partial class FullSolver
{
    public const int StrataMax = 64;

    public sealed class Strata
    {
        public readonly int Ver;
        public readonly List<ulong> Sig = new();
        public readonly List<int> Best = new();
        public readonly List<bool> Failed = new();   // race: the stratum's tip failed a trial (replaced first when full)
        public readonly bool[] Goal;                  // real goals (the pull solver's root boxes)
        public readonly int[] Buf;
        public int[]? Cnt;                            // per-room scratch (sized when the rooms exist)
        public int Added;                             // stratum creations (including replacements)
        public Strata(FullSolver s, int ver)
        {
            Ver = ver; Goal = new bool[s.cells]; Buf = new int[s.n];
            foreach (int g in s.lv.Boxes) Goal[g] = true;
        }
    }

    Strata[] strataT = [];
    public Strata[] StrataTables => strataT;

    int DepthOf(int id) { int d = 0; for (int p = nodes.Parent(id); p >= 0; p = nodes.Parent(p)) d++; return d; }

    ulong StrataSignature(Strata t, int id)
    {
        if (t.Ver == 1)
        {
            int d = DepthOf(id), a = id;
            for (int k = d - d / 2; k > 0; k--) a = nodes.Parent(a);
            return (uint)a;
        }
        nodes.Unpack(id, t.Buf);
        return BoxSig(t.Goal, rmCell, t.Cnt ??= new int[rmRooms.Length + 1], t.Buf);
    }

    /// <summary>Go boxSig: the v0 signature of a box set (rooms depend only on walls; room null/empty: none).</summary>
    public static ulong BoxSig(bool[] goal, int[]? room, int[] cnt, ReadOnlySpan<int> boxes)
    {
        Array.Clear(cnt);
        int on = 0;
        foreach (int b in boxes)
        {
            if (goal[b]) on++;
            int r = room != null && room.Length > 0 ? room[b] : -1;
            if (r >= 0) cnt[r + 1]++; else cnt[0]++;
        }
        ulong h = 14695981039346656037UL ^ (ulong)on;
        h *= 1099511628211UL;
        foreach (int c in cnt) { h ^= (uint)c; h *= 1099511628211UL; }
        return h;
    }

    public int[] RoomCells => rmCell;

    /// <summary>Go strata.offer: consider a new pull node for its stratum.</summary>
    void StrataOffer(Strata t, int id)
    {
        if (id < 0 || maux[id].Dead != 0) return;
        ulong sg = StrataSignature(t, id);
        int at = -1;
        for (int i = 0; i < t.Sig.Count; i++) if (t.Sig[i] == sg) { at = i; break; }
        if (at >= 0)
        {
            int b = t.Best[at];
            if (maux[b].Dead == 0 && !PfFest.Tuple.Better(pfBest[id], pfBest[b])) return;
        }
        else if (t.Sig.Count >= StrataMax)
        {
            int w = 0, wf = -1;
            for (int i = 1; i < t.Best.Count; i++)
                if (maux[t.Best[i]].Dead != 0 || maux[t.Best[w]].Dead == 0 && PfFest.Tuple.Better(pfBest[t.Best[w]], pfBest[t.Best[i]])) w = i;
            for (int i = 0; i < t.Failed.Count; i++)
                if (t.Failed[i] && (wf < 0 || PfFest.Tuple.Better(pfBest[t.Best[wf]], pfBest[t.Best[i]]))) wf = i;
            if (wf >= 0) w = wf;
            else if (maux[t.Best[w]].Dead == 0 && !PfFest.Tuple.Better(pfBest[id], pfBest[t.Best[w]])) return;
            at = w;
        }
        if (!FeasibleNode(id)) return; // PullBestFeasible
        if (at < 0) { t.Sig.Add(sg); t.Best.Add(id); t.Failed.Add(false); t.Added++; return; }
        if (t.Sig[at] != sg) t.Added++;
        t.Sig[at] = sg; t.Best[at] = id; t.Failed[at] = false;
    }
}

public sealed unsafe partial class FullSolver
{
    /// <summary>Go strataDumpRun (-strategy hub-strata -hub-strata-dump FILE): hub-pull's pull solver to NodeBudget, then every stratum's best node (v0, v1) and the frontier best as HUB blocks.</summary>
    public static SolveResult SolveHubStrata(string[] rows, FullOptions opt, double timeLimitSec, string dumpPath)
    {
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var pv = PullView(lv, out var roots, out var spread);
        var o = opt.Clone();
        o.Pull = true; o.PfOn = true; o.Penalties = false; o.MacroStage = 5;
        o.MacroFeatures = opt.MacroFeatures & (1 | 8 | 64 | 128);
        o.RngSeed = RevSeed(0);
        using var ps = new FullSolver(pv, o) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots, mRows = rows };
        ps.strataT = [new Strata(ps, 0), new Strata(ps, 1)];
        var r = ps.RunMacro(timeLimitSec);
        if (r.Solved) return r;
        var sb = new System.Text.StringBuilder();
        var goalOf = ps.strataT[0].Goal;
        var boxes = new int[ps.n];
        void Block(string tag, int k, ulong sg, int id)
        {
            ps.nodes.Unpack(id, boxes);
            int on = 0;
            foreach (int b in boxes) if (goalOf[b]) on++;
            var p = ps.pfBest[id];
            sb.Append($"HUB strata={tag} k={k} sig={sg:x} id={id} depth={ps.DepthOf(id)} on_goal={on} reg={p.Reg} rooms={p.Rooms} hot={p.Hot} dist={p.Dist}\n");
            var isBox = new HashSet<int>(boxes);
            int player = ps.nodes.Player(id);
            for (int y = 1; y < lv.H - 1; y++)
            {
                for (int x = 1; x < lv.W - 1; x++)
                {
                    int c = y * lv.W + x;
                    sb.Append(lv.Wall[c] ? '#' : isBox.Contains(c) ? '$' : c == player ? '@' : ' ');
                }
                sb.Append('\n');
            }
        }
        if (ps.mBest >= 0) Block("base", 0, 0, ps.mBest);
        foreach (var t in ps.strataT)
            for (int k = 0; k < t.Sig.Count; k++) Block($"v{t.Ver}", k, t.Sig[k], t.Best[k]);
        File.WriteAllText(dumpPath, sb.ToString());
        Console.Error.WriteLine($"STRATA nodes={r.Nodes} stored={ps.nodes.Count} reason={r.Reason} v0={ps.strataT[0].Sig.Count}/{ps.strataT[0].Added} v1={ps.strataT[1].Sig.Count}/{ps.strataT[1].Added}");
        return new SolveResult { Reason = "hub-strata done", Nodes = r.Nodes };
    }

    public static ulong RevSeed(int k) => 0x2545f4914f6cdd1dUL + (ulong)k * 0x9E3779B97F4A7C15UL;
}
