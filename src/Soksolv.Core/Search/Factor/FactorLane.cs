using System.Diagnostics;
using System.Text;
using Soksolv.Model;
using Soksolv.Search.Reverse;

namespace Soksolv.Search.Factor;

public sealed class FactorOptions
{
    public long NodeBudget;                  // -nodes: the lane's node pool (deterministic mode)
    public ulong BudgetBytes;
    public int FreeMax = 300;                // groups with at most this many box configurations are searched exactly by the scheduler
    public long Pool = 200000;               // timed mode: first node pool; doubled per attempt
    public int Sched = 300000;               // scheduler states per call
    public double PlanCap = 0.02;            // exp-645b: a sub-solve's planner may use this fraction of the remaining time
    public bool Solo = true;                 // -strategy factor alone (in the portfolio the other lanes own the real board)
    public CancelFlag? Cancel;
    public FullOptions Engine = new();       // base options for the sub-engines (cluster tables, stall scale, macro features...)
    public double TimeLimitSec;              // -t (for time-scaled constants)
}

internal sealed class FGroup { public int[] Cells = [], Boxes = [], Goals = []; }
internal readonly record struct FPush(int Box, int Dir);

/// <summary>A group's push list; Pos[j*Nb..(j+1)*Nb) = its boxes (sorted) after j pushes (offsets let a plan be advanced without copying).</summary>
internal sealed class FPlan
{
    public FPush[] P = []; public int PO, Len;           // pushes P[PO..PO+Len)
    public int[] Pos = []; public int PosOff; public int Nb;
    public FPush Push(int j) => P[PO + j];
    public ReadOnlySpan<int> At(int j) => Pos.AsSpan(PosOff + j * Nb, Nb);
    public FPlan From(int j) => new() { P = P, PO = PO + j, Len = Len - j, Pos = Pos, PosOff = PosOff + j * Nb, Nb = Nb };
    public bool SamePushes(FPlan o)
    {
        if (Len != o.Len) return false;
        for (int i = 0; i < Len; i++) if (Push(i) != o.Push(i)) return false;
        return true;
    }
}

/// <summary>
/// Factored component lane (port of Go factor.go, exp-645): boards whose live squares split into >= 2 non-trivial box
/// components are solved component by component (sub-levels with the other boxes absent, engines macro / novelty / fa2 / reverse
/// under escalating node caps), the plans are scheduled on the real board by a depth-first search, stuck groups are re-planned or merged.
/// </summary>
public static class FactorLane
{
    public static bool Gate(Level lv) => BoxComponents(lv).nt.Count >= 2;

    internal static (List<FGroup> nt, List<FGroup> triv) BoxComponents(Level lv)
    {
        int n = lv.W * lv.H;
        int[] d = [-lv.W, lv.W, -1, 1];
        var live = new bool[n];
        var q = new List<int>(n);
        for (int c = 0; c < n; c++) if (lv.Goal[c] && !lv.Wall[c]) { live[c] = true; q.Add(c); }
        for (int i = 0; i < q.Count; i++) // pull flood: box at c pulled to c+d by a player standing at c+2d
        {
            int c = q[i];
            foreach (int dd in d)
            {
                int nb = c + dd, p = c + 2 * dd;
                if (!lv.Wall[nb] && !lv.Wall[p] && !live[nb]) { live[nb] = true; q.Add(nb); }
            }
        }
        var par = new int[n];
        for (int i = 0; i < n; i++) par[i] = i;
        int Find(int x) { while (par[x] != x) { par[x] = par[par[x]]; x = par[x]; } return x; }
        for (int c = 0; c < n; c++)
        {
            if (!live[c]) continue;
            foreach (int dd in d)
            {
                int nb = c + dd, p = c - dd;
                if (live[nb] && !lv.Wall[p]) { int a = Find(c), b = Find(nb); if (a != b) par[a] = b; }
            }
        }
        var isBox = new bool[n];
        foreach (int b in lv.Boxes) isBox[b] = true;
        var byRoot = new Dictionary<int, int>();
        var all = new List<(List<int> cells, List<int> boxes, List<int> goals)>();
        for (int c = 0; c < n; c++)
        {
            if (!live[c]) continue;
            int r = Find(c);
            if (!byRoot.TryGetValue(r, out int k)) { k = all.Count; byRoot[r] = k; all.Add(([], [], [])); }
            var g = all[k];
            g.cells.Add(c);
            if (isBox[c]) g.boxes.Add(c);
            if (lv.Goal[c]) g.goals.Add(c);
        }
        var nt = new List<FGroup>(); var triv = new List<FGroup>();
        foreach (var g in all)
        {
            if (g.boxes.Count == 0) continue;
            var fg = new FGroup { Cells = [.. g.cells], Boxes = [.. g.boxes], Goals = [.. g.goals] };
            if (fg.Boxes.AsSpan().SequenceEqual(fg.Goals)) triv.Add(fg); else nt.Add(fg);
        }
        return (nt, triv);
    }

    public static SolveResult Run(Level lv, FactorOptions fo, double timeLimitSec)
    {
        var (nt, triv) = BoxComponents(lv);
        if (nt.Count < 2) return new SolveResult { Reason = "factor: fewer than 2 box components" };
        var sw = Stopwatch.StartNew();
        using var f = new Runner(lv, fo, timeLimitSec, sw);
        long pool = fo.Pool;
        if (fo.NodeBudget > 0) pool = fo.NodeBudget;
        int n = lv.Wall.Length;
        for (long scale = 1; ; scale *= 2)
        {
            f.CapScale = scale; f.PoolN = pool * scale; f.Used = 0; f.Reserve = 40000 * scale;
            if (f.Attempt(nt, triv)) return new SolveResult { Solved = true, Solution = f.Lurd.ToString(), Nodes = f.Used, TimeMs = sw.Elapsed.TotalMilliseconds, PeakBytes = f.PeakBytes };
            if (fo.Solo && f.PoolN - f.Used >= 1000 && !f.Expired())
            {
                // last resort (alone only; in the portfolio other lanes own the real board)
                var rows = f.Rows(new bool[n], f.Occ, lv.Goal, f.Pl);
                var (sol, _) = f.SubSolve(rows, "macro", f.PoolN - f.Used);
                if (sol != "" && f.ApplyLurd(sol) && f.AllDone()) return new SolveResult { Solved = true, Solution = f.Lurd.ToString(), Nodes = f.Used, TimeMs = sw.Elapsed.TotalMilliseconds, PeakBytes = f.PeakBytes };
            }
            if (fo.NodeBudget > 0 || f.Expired()) return new SolveResult { Reason = "factor: unsolved", Nodes = f.Used, TimeMs = sw.Elapsed.TotalMilliseconds, PeakBytes = f.PeakBytes };
        }
    }

    // ---- the lane ------------------------------------------------------------------------------------------------------
    sealed class Fa2Runner
    {
        public readonly FullSolver S; readonly Baton bat = new([true, true, false], 0); readonly Thread th;
        SolveResult? result; bool holding; public bool Done;
        public Fa2Runner(FullSolver s, double sec)
        {
            S = s; s.SubAttachBaton(bat, 0);
            th = new Thread(() =>
            {
                bat.Wait(0);
                try { result = s.SubRunFa2(sec); }
                catch (Exception e) { result = new SolveResult { Reason = "fa2: " + e.GetType().Name + " " + e.Message }; }
                finally { bat.Done(0, result?.Solved ?? false); }
            }, 256 << 20) { IsBackground = true, Name = "factor-fa2" };
            th.Start();
            bat.Release(0);
        }
        /// <summary>Lets the engine expand until it has done target expansions in total (to the next 1024-expansion yield) or ends.</summary>
        public (SolveResult r, bool finished) RunTo(long target)
        {
            while (true)
            {
                if (holding) { holding = false; bat.Release(0); }
                bat.Wait(1);
                if (!bat.IsAlive(0)) { Done = true; th.Join(); return (result!, true); }
                holding = true;
                if (S.SubExpanded >= target) return (new SolveResult { Reason = "timeout" }, false);
            }
        }
        public void Stop()
        {
            if (Done) return;
            bat.RequestStop();
            if (holding) { holding = false; bat.Release(0); }
            th.Join(); Done = true;
        }
    }

    static readonly string[] Engines = ["fa2", "macro", "reverse", "novelty"];
    const int FactorSteps = 3;
    const int RevNodeWeight = 16;

    readonly record struct SubCall(string Lane, long Cap);

    static List<SubCall> StepCalls(int k)
    {
        var outp = new List<SubCall>();
        long lo = new long[] { 1000, 16000, 64000 }[k], hi = new long[] { 8000, 32000, 128000 }[k];
        for (long c = lo; c <= hi; c *= 2) foreach (var l in Engines) outp.Add(new(l, c));
        return outp;
    }
    static List<SubCall> ReplanCalls()
    {
        var outp = new List<SubCall>();
        for (long c = 1000; c <= 32000; c *= 2) foreach (var l in Engines) outp.Add(new(l, c));
        return outp;
    }

    internal sealed class Runner : IDisposable
    {
        readonly Level lv; readonly FactorOptions fo; readonly double limitSec; readonly Stopwatch clock;
        readonly int[] d; readonly int n;
        public long CapScale, PoolN, Used, Reserve, PeakBytes;
        void NotePeak(FullSolver s) { try { PeakBytes = Math.Max(PeakBytes, s.SubBytes); } catch { } }
        List<FGroup> groups = [], triv = [];
        readonly Dictionary<string, FPlan> cache = new();
        Dictionary<string, FPlan> live = new();
        public readonly bool[] Occ; readonly bool[] occ2; List<int> boxes = [];
        public int Pl; public readonly StringBuilder Lurd = new();
        readonly uint[] mark; uint stamp; readonly int[] q; readonly int[] prev;
        readonly HashSet<string> infeasible = new();
        string keepKey = ""; Dictionary<string, FullSolver> keep = new(); Dictionary<string, long> keepN = new(); Fa2Runner? keepFa2;
        Dictionary<string, HashSet<string>> planEng = new();
        static readonly char[] DirCh = ['u', 'd', 'l', 'r'];

        public Runner(Level lv, FactorOptions fo, double limitSec, Stopwatch clock)
        {
            this.lv = lv; this.fo = fo; this.limitSec = limitSec; this.clock = clock;
            n = lv.Wall.Length; d = [-lv.W, lv.W, -1, 1];
            Occ = new bool[n]; occ2 = new bool[n]; mark = new uint[n]; q = new int[n + 8]; prev = new int[n];
        }
        public void Dispose() => DropKept();

        public bool Expired()
        {
            if (fo.Cancel?.Stopped ?? false) return true;
            return fo.NodeBudget == 0 && limitSec > 0 && clock.Elapsed.TotalSeconds > limitSec;
        }
        double Remaining() => limitSec > 0 ? Math.Max(limitSec - clock.Elapsed.TotalSeconds, 0.001) : 0;

        // ---- kept (paused) sub-searches ---------------------------------------------------------------------------------
        void DropKept()
        {
            keepFa2?.Stop(); keepFa2 = null;
            foreach (var s in keep.Values) s.Dispose();
            keepKey = ""; keep = new(); keepN = new();
        }

        void KeepFa2(string rkey, Fa2Runner fr, bool done, long cnt)
        {
            KeepSolver(rkey, "fa2", null, new SolveResult(), 0);
            if (done) return;
            keepFa2 = fr; keepN["fa2"] = cnt;
        }

        void KeepSolver(string rkey, string lane, FullSolver? s, SolveResult r, long cnt)
        {
            if (rkey != keepKey) { DropKept(); keepKey = rkey; keep = new(); keepN = new(); }
            if (keep.Remove(lane, out var old)) old.Dispose();
            if (lane == "fa2") { keepFa2?.Stop(); keepFa2 = null; return; }
            if (s != null && r.Reason == "timeout" && s.SubStarted && s.SubNodeLimit > 0 && s.SubExpanded >= s.SubNodeLimit) { keep[lane] = s; keepN[lane] = cnt; }
            else s?.Dispose();
        }

        static bool InfeasibleWhy(string why) => why is "exhausted" or "box on dead square" or "goal unreachable from start squares";

        (string sol, string why, bool ok) ResumeSub(string[] rows, string lane, long cap)
        {
            string rkey = string.Join("\n", rows);
            if (rkey != keepKey || (lane != "fa2" && !keep.ContainsKey(lane)) || (lane == "fa2" && keepFa2 == null)) return ("", "", false);
            if (lane == "fa2") return ResumeFa2(rkey, cap);
            if (infeasible.Contains(rkey)) return ("", "infeasible", true);
            var s = keep[lane]; long spent = keepN[lane];
            long delta = Math.Min(cap - spent, PoolN - Used);
            if (delta <= 0 || Expired())
            {
                if (delta <= 0 && cap - spent > 0) return ("", "pool", true);
                return ("", "timeout", true);
            }
            long before = s.SubExpanded;
            s.SubSetNodeLimit(before + delta);
            var r = s.SubRunMacro(Remaining());
            long cnt = s.SubExpanded - before;
            Used += Math.Min(cnt, delta);
            keep.Remove(lane);
            if (r.Solved && r.Solution != "") { s.Dispose(); return (r.Solution, "", true); }
            if (InfeasibleWhy(r.Reason)) { infeasible.Add(rkey); s.Dispose(); return ("", "infeasible", true); }
            if (r.Reason == "timeout" && s.SubExpanded >= s.SubNodeLimit) { keep[lane] = s; keepN[lane] = spent + cnt; }
            else s.Dispose();
            return ("", r.Reason, true);
        }

        (string sol, string why, bool ok) ResumeFa2(string rkey, long cap)
        {
            var fr = keepFa2!; long spent = keepN["fa2"];
            long delta = Math.Min(cap - spent, PoolN - Used);
            if (delta <= 0 || Expired())
            {
                if (delta <= 0 && cap - spent > 0) return ("", "pool", true);
                return ("", "timeout", true);
            }
            long before = fr.S.SubExpanded;
            var (r, done) = fr.RunTo(before + delta);
            long cnt = fr.S.SubExpanded - before;
            Used += Math.Min(cnt, delta);
            keepN["fa2"] = spent + cnt;
            if (done)
            {
                keepFa2 = null;
                fr.S.Dispose();
                if (r.Solved && r.Solution != "") return (r.Solution, "", true);
                if (InfeasibleWhy(r.Reason)) { infeasible.Add(rkey); return ("", "infeasible", true); }
                return ("", r.Reason, true);
            }
            return ("", "timeout", true);
        }

        // ---- real-board helpers ----------------------------------------------------------------------------------------------
        int Reach(int p, bool[] ob)
        {
            if (++stamp == 0) { Array.Clear(mark); stamp = 1; }
            int tail = 0; q[tail++] = p; mark[p] = stamp; int mn = p;
            for (int i = 0; i < tail; i++)
            {
                int c = q[i];
                foreach (int dd in d)
                {
                    int nb = c + dd;
                    if (!lv.Wall[nb] && !ob[nb] && mark[nb] != stamp) { mark[nb] = stamp; q[tail++] = nb; if (nb < mn) mn = nb; }
                }
            }
            return mn;
        }

        bool Walk(int t)
        {
            if (Pl == t) return true;
            if (++stamp == 0) { Array.Clear(mark); stamp = 1; }
            int tail = 0; q[tail++] = Pl; mark[Pl] = stamp;
            for (int i = 0; i < tail; i++)
            {
                int c = q[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + d[k];
                    if (lv.Wall[nb] || Occ[nb] || mark[nb] == stamp) continue;
                    mark[nb] = stamp; prev[nb] = c * 4 + k;
                    if (nb == t)
                    {
                        var rev = new List<char>();
                        for (int x = t; x != Pl; x = prev[x] / 4) rev.Add(DirCh[prev[x] % 4]);
                        for (int j = rev.Count - 1; j >= 0; j--) Lurd.Append(rev[j]);
                        Pl = t;
                        return true;
                    }
                    q[tail++] = nb;
                }
            }
            return false;
        }

        bool PushReal(FPush p)
        {
            int dd = d[p.Dir];
            if (!Occ[p.Box] || lv.Wall[p.Box + dd] || Occ[p.Box + dd] || !Walk(p.Box - dd)) return false;
            Lurd.Append((char)(DirCh[p.Dir] - 'a' + 'A'));
            Occ[p.Box] = false; Occ[p.Box + dd] = true;
            int i = boxes.IndexOf(p.Box);
            boxes[i] = p.Box + dd;
            Pl = p.Box;
            return true;
        }

        public bool ApplyLurd(string s)
        {
            for (int i = 0; i < s.Length; i++)
            {
                int k = "udlr".IndexOf((char)(s[i] | 0x20));
                if (k < 0) return false;
                int dd = d[k], nb = Pl + dd;
                if (lv.Wall[nb]) return false;
                if (Occ[nb])
                {
                    if (lv.Wall[nb + dd] || Occ[nb + dd]) return false;
                    Occ[nb] = false; Occ[nb + dd] = true;
                    boxes[boxes.IndexOf(nb)] = nb + dd;
                }
                Pl = nb;
            }
            Lurd.Append(s);
            return true;
        }

        /// <summary>Renders a sub-level in the original rows' coordinates (lv cell (y+1)*W+x+1... as Go f.rows).</summary>
        public string[] Rows(bool[] walls, bool[] bx, bool[] goals, int pl)
        {
            var outp = new string[lv.H - 2];
            var b = new char[lv.W - 2];
            for (int y = 1; y < lv.H - 1; y++)
            {
                for (int x = 1; x < lv.W - 1; x++)
                {
                    int c = y * lv.W + x;
                    char ch = ' ';
                    if (lv.Wall[c] || walls[c]) ch = '#';
                    else if (c == pl && goals[c]) ch = '+';
                    else if (c == pl) ch = '@';
                    else if (bx[c] && goals[c]) ch = '*';
                    else if (bx[c]) ch = '$';
                    else if (goals[c]) ch = '.';
                    b[x - 1] = ch;
                }
                outp[y - 1] = new string(b);
            }
            return outp;
        }

        bool[] CellSet(params int[][] cs)
        {
            var m = new bool[n];
            foreach (var c in cs) foreach (int x in c) m[x] = true;
            return m;
        }

        // ---- sub-solves ---------------------------------------------------------------------------------------------------------
        FullSolver MakeSub(Level sub, string[] rows, FullSolver.SubKind kind, long cap)
        {
            var eo = fo.Engine.Clone();
            eo.Cancel = fo.Cancel; eo.BudgetBytes = fo.BudgetBytes; eo.NodeBudget = fo.NodeBudget;
            if (fo.NodeBudget == 0 && fo.PlanCap > 0) eo.PlanCapMs = Math.Max(Remaining() * 1000 * fo.PlanCap, 1);
            else eo.PlanCapMs = 0;
            return FullSolver.NewSub(sub, rows, eo, kind, kind == FullSolver.SubKind.Fa2 ? 1L << 62 : cap);
        }

        public (string sol, string why) SubSolve(string[] rows, string lane, long cap)
        {
            if (lane is "macro" or "novelty" or "fa2")
            {
                var (s0, w0, ok) = ResumeSub(rows, lane, cap);
                if (ok) return (s0, w0);
            }
            cap = Math.Min(cap, PoolN - Used);
            if (cap < 200 || Expired()) return ("", "pool");
            string rkey = string.Join("\n", rows);
            if (infeasible.Contains(rkey)) return ("", "infeasible");
            Level sub;
            try { sub = Level.ParseRaw(rows); } catch (InvalidDataException) { return ("", "parse"); } // a sealed or unbalanced sub-level is infeasible
            SolveResult r; long cnt;
            switch (lane)
            {
                case "macro":
                    {
                        var s = MakeSub(sub, rows, FullSolver.SubKind.Macro, cap);
                        r = s.SubRunMacro(Remaining());
                        cnt = s.SubExpanded; NotePeak(s);
                        while (r.Reason == "replan" && !Expired() && cnt < cap) // as the macro lane (exp-563)
                        {
                            var pre = s.SubPrePlan; int at = s.SubPrePlanAt;
                            s.Dispose();
                            s = MakeSub(sub, rows, FullSolver.SubKind.Macro, cap - cnt);
                            s.SubSetPre(pre, at);
                            r = s.SubRunMacro(Remaining());
                            cnt += s.SubExpanded;
                        }
                        KeepSolver(rkey, lane, s, r, cnt);
                        break;
                    }
                case "novelty":
                    {
                        var s = MakeSub(sub, rows, FullSolver.SubKind.Novelty, cap);
                        r = s.SubRunMacro(Remaining());
                        cnt = s.SubExpanded; NotePeak(s);
                        KeepSolver(rkey, lane, s, r, cnt);
                        break;
                    }
                case "fa2":
                    {
                        var s = MakeSub(sub, rows, FullSolver.SubKind.Fa2, cap);
                        var fr = new Fa2Runner(s, Remaining()); // a pausable engine (baton slices), resumed by the next larger cap
                        var (rr, done) = fr.RunTo(cap);
                        r = rr; cnt = s.SubExpanded; NotePeak(s);
                        KeepFa2(rkey, fr, done, cnt);
                        if (done) s.Dispose();
                        break;
                    }
                default: // "reverse": the backward engine, private (no perimeters)
                    {
                        Level rl;
                        try { rl = Level.Parse(rows, true); } catch (InvalidDataException) { return ("", "parse"); }
                        var ro = new RevOptions
                        {
                            Cells = true, Novelty = true, Types = true, Walks = true, Supply = 0, NodeBudget = fo.NodeBudget, MaxNodes = cap * RevNodeWeight,
                            BudgetBytes = fo.BudgetBytes, Cancel = fo.Cancel,
                        };
                        if (limitSec > 0 && limitSec < 600) ro.CellsAfterSec *= limitSec / 600; // main -stall-scale
                        using var rs = new ReverseSolver(rl, ro);
                        r = rs.Run(Remaining());
                        cnt = (r.Nodes + RevNodeWeight - 1) / RevNodeWeight;
                        break;
                    }
            }
            Used += Math.Min(cnt, cap);
            if (r.Solved && r.Solution != "") return (r.Solution, "");
            if (InfeasibleWhy(r.Reason)) { infeasible.Add(rkey); return ("", "infeasible"); }
            return ("", r.Reason);
        }

        List<FPush>? PushesOf(string lurd, int[] bxs, bool[] walls, int pl)
        {
            var oc = CellSet(bxs);
            var outp = new List<FPush>();
            for (int i = 0; i < lurd.Length; i++)
            {
                int k = "udlr".IndexOf((char)(lurd[i] | 0x20));
                if (k < 0) return null;
                int dd = d[k], nb = pl + dd;
                if (lv.Wall[nb] || walls[nb]) return null;
                if (oc[nb])
                {
                    if (lv.Wall[nb + dd] || walls[nb + dd] || oc[nb + dd]) return null;
                    oc[nb] = false; oc[nb + dd] = true;
                    outp.Add(new FPush(nb, k));
                }
                pl = nb;
            }
            return outp;
        }

        static FPlan NewFPlan(int[] start, List<FPush> pushes, int[] d)
        {
            var p = new FPlan { P = [.. pushes], PO = 0, Len = pushes.Count, Nb = start.Length };
            var cur = (int[])start.Clone(); Array.Sort(cur);
            var pos = new List<int>(cur);
            foreach (var ps in pushes)
            {
                int i = Array.IndexOf(cur, ps.Box);
                cur[i] = ps.Box + d[ps.Dir];
                Array.Sort(cur);
                pos.AddRange(cur);
            }
            p.Pos = [.. pos];
            return p;
        }

        // ---- planning -----------------------------------------------------------------------------------------------------------------
        FPlan? RelaxedPlan(FGroup g, int step) => RelaxedPlanBy(g, step, "");

        FPlan? RelaxedPlanBy(FGroup g, int step, string only)
        {
            var tw = new List<int>();
            foreach (var t in triv) tw.AddRange(t.Boxes);
            foreach (bool walls in new[] { true, false })
            {
                bool[] w = walls ? CellSet(tw.ToArray()) : new bool[n];
                var rows = Rows(w, CellSet(g.Boxes), CellSet(g.Goals), lv.Player);
                bool timedOut = false;
                foreach (var c in StepCalls(step))
                {
                    if (only != "" && c.Lane != only) continue;
                    var (sol, why) = SubSolve(rows, c.Lane, c.Cap * CapScale);
                    if (sol != "")
                    {
                        var ps = PushesOf(sol, g.Boxes, w, lv.Player);
                        if (ps != null)
                        {
                            string gk = GKey(g);
                            if (!planEng.TryGetValue(gk, out var set)) planEng[gk] = set = new();
                            set.Add(c.Lane);
                            return NewFPlan(g.Boxes, ps, d);
                        }
                    }
                    if (why == "pool") return null;
                    if (why == "infeasible") { timedOut = false; break; } // proved unsolvable: try the next variant
                    timedOut = timedOut || why == "timeout";
                }
                if (walls && timedOut) break;
            }
            return null;
        }

        static string GKey(FGroup g)
        {
            var sb = new StringBuilder(4 * g.Boxes.Length);
            foreach (int x in g.Boxes) { sb.Append((char)(x & 0xFF)).Append((char)((x >> 8) & 0xFF)).Append((char)((x >> 16) & 0xFF)).Append((char)((x >> 24) & 0xFF)); }
            return sb.ToString();
        }

        static bool CombLE(int nn, int k, int lim)
        {
            long c = 1;
            for (int i = 0; i < k; i++) { c = c * (nn - i) / (i + 1); if (c > lim) return false; }
            return true;
        }
        bool IsFree(FGroup g) => CombLE(g.Cells.Length, g.Boxes.Length, fo.FreeMax);
        bool GroupDone(FGroup g) { foreach (int x in g.Goals) if (!Occ[x]) return false; return true; }
        int[] CurBoxes(FGroup g) { var o = new List<int>(); foreach (int c in g.Cells) if (Occ[c]) o.Add(c); return [.. o]; }

        // ---- scheduler -------------------------------------------------------------------------------------------------------------------
        sealed class SGroup { public FPlan? Plan; public FGroup G = null!; public bool[] In = [], Goal = []; public int Off, Width, Rank; }
        struct Succ { public double Pri; public int G; public FPush Mv; }

        (bool ok, List<FPush> path, int[] end) Schedule(List<SGroup> sg, int cap)
        {
            int w = 1; // slot 0: canonical player cell
            foreach (var s in sg) { s.Off = w; s.Width = s.Plan != null ? 1 : s.G.Boxes.Length; w += s.Width; }
            var stat = (bool[])Occ.Clone();
            foreach (var s in sg) foreach (int c in s.G.Cells) stat[c] = false;
            var occ = occ2;
            void SetOcc(ReadOnlySpan<int> st)
            {
                Array.Copy(stat, occ, stat.Length);
                foreach (var s in sg)
                {
                    if (s.Plan != null) { foreach (int c in s.Plan.At(st[s.Off])) occ[c] = true; }
                    else for (int i = 0; i < s.Width; i++) occ[st[s.Off + i]] = true;
                }
            }
            var arena = new List<int>(w * 1024);
            var parent = new List<int> { -1 };
            var moveP = new List<FPush> { default };
            var index = new Dictionary<ulong, List<int>>();
            ulong Hash(ReadOnlySpan<int> st)
            {
                ulong h = 1469598103934665603UL;
                foreach (int x in st) h = (h ^ (uint)x) * 1099511628211UL;
                return h;
            }
            bool Find(ReadOnlySpan<int> st, ulong h)
            {
                if (!index.TryGetValue(h, out var l)) return false;
                foreach (int id in l) if (CollectionsSpan(arena, id * w, w).SequenceEqual(st)) return true;
                return false;
            }
            var start = new int[w];
            foreach (var s in sg) if (s.Plan == null) CurBoxes(s.G).CopyTo(start, s.Off);
            SetOcc(start);
            start[0] = Reach(Pl, occ);
            arena.AddRange(start);
            index[Hash(start)] = [0];
            var stack = new List<(int id, int last)> { (0, -1) };
            bool Done(ReadOnlySpan<int> st)
            {
                foreach (var s in sg)
                {
                    if (s.Plan != null) { if (st[s.Off] != s.Plan.Len) return false; }
                    else for (int i = 0; i < s.Width; i++) if (!s.Goal[st[s.Off + i]]) return false;
                }
                return true;
            }
            int Score(ReadOnlySpan<int> st)
            {
                int t = 0;
                foreach (var s in sg)
                {
                    if (s.Plan != null) t += st[s.Off];
                    else for (int i = 0; i < s.Width; i++) if (s.Goal[st[s.Off + i]]) t++;
                }
                return t;
            }
            int best = Score(start), bestId = 0, found = -1;
            var succs = new List<Succ>();
            var child = new int[w];
            int expanded = 0;
            while (stack.Count > 0 && expanded < cap)
            {
                var it = stack[^1]; stack.RemoveAt(stack.Count - 1);
                expanded++;
                var st = CollectionsSpan(arena, it.id * w, w).ToArray();
                int sc0 = Score(st);
                if (sc0 > best) { best = sc0; bestId = it.id; }
                if (Done(st)) { found = it.id; break; }
                SetOcc(st);
                Reach(st[0], occ); // marks the region
                succs.Clear();
                for (int gi = 0; gi < sg.Count; gi++)
                {
                    var s = sg[gi];
                    if (s.Plan != null)
                    {
                        int j = st[s.Off];
                        if (j >= s.Plan.Len) continue;
                        var p = s.Plan.Push(j);
                        int dd = d[p.Dir];
                        if (mark[p.Box - dd] != stamp || occ[p.Box + dd]) continue;
                        double pri = 1.0 + 0.01 * (sg.Count - s.Rank);
                        if (gi == it.last) pri += 1;
                        succs.Add(new Succ { Pri = pri, G = gi, Mv = p });
                        continue;
                    }
                    bool allHome = true;
                    for (int i = 0; i < s.Width; i++) allHome = allHome && s.Goal[st[s.Off + i]];
                    if (allHome) continue;
                    for (int i = 0; i < s.Width; i++)
                    {
                        int b = st[s.Off + i];
                        for (int k = 0; k < 4; k++)
                        {
                            int dd = d[k], t = b + dd;
                            if (!s.In[t] || occ[t] || mark[b - dd] != stamp) continue;
                            double pri = 0.0;
                            if (s.Goal[t] && !s.Goal[b]) pri = 3;
                            succs.Add(new Succ { Pri = pri, G = gi, Mv = new FPush(b, k) });
                        }
                    }
                }
                // stable sort by priority
                var arr = succs.ToArray();
                var order = Enumerable.Range(0, arr.Length).OrderBy(i => arr[i].Pri).ToArray(); // OrderBy is stable
                foreach (int oi in order)
                {
                    var sc = arr[oi];
                    var s = sg[sc.G];
                    Array.Copy(st, child, w);
                    if (s.Plan != null) child[s.Off]++;
                    else
                    {
                        var seg = child.AsSpan(s.Off, s.Width);
                        seg[seg.IndexOf(sc.Mv.Box)] = sc.Mv.Box + d[sc.Mv.Dir];
                        seg.Sort();
                    }
                    child[0] = 0;
                    SetOcc(child);
                    child[0] = Reach(sc.Mv.Box, occ);
                    ulong h = Hash(child);
                    if (Find(child, h)) continue;
                    int id = parent.Count;
                    arena.AddRange(child);
                    parent.Add(it.id);
                    moveP.Add(sc.Mv);
                    if (!index.TryGetValue(h, out var l)) index[h] = l = new();
                    l.Add(id);
                    stack.Add((id, sc.G));
                }
            }
            int end = found >= 0 ? found : bestId;
            var path = new List<FPush>();
            for (int id = end; parent[id] >= 0; id = parent[id]) path.Add(moveP[id]);
            path.Reverse();
            return (found >= 0, path, CollectionsSpan(arena, end * w, w).ToArray());
        }

        static Span<int> CollectionsSpan(List<int> l, int start, int len) => System.Runtime.InteropServices.CollectionsMarshal.AsSpan(l).Slice(start, len);

        static void Permute(List<int> xs, int k, List<List<int>> outp)
        {
            if (k == xs.Count) { outp.Add([.. xs]); return; }
            for (int i = k; i < xs.Count; i++)
            {
                (xs[k], xs[i]) = (xs[i], xs[k]);
                Permute(xs, k + 1, outp);
                (xs[k], xs[i]) = (xs[i], xs[k]);
            }
        }

        (bool ok, int moved) TrySchedule()
        {
            var sg = new List<SGroup>();
            bool need = false;
            void Add(FGroup g)
            {
                if (live.TryGetValue(GKey(g), out var p) && p != null) { sg.Add(new SGroup { Plan = p, G = g }); need = true; }
                else if (IsFree(g)) { sg.Add(new SGroup { G = g, In = CellSet(g.Cells), Goal = CellSet(g.Goals) }); need = need || !GroupDone(g); }
            }
            foreach (var g in groups) if (!GroupDone(g)) Add(g);
            foreach (var g in triv) if (IsFree(g)) Add(g);
            if (!need) return (false, 0);
            var pk = new List<int>();
            for (int i = 0; i < sg.Count; i++) if (sg[i].Plan != null) pk.Add(i);
            pk = pk.OrderBy(i => sg[i].Plan!.Len).ToList(); // stable
            var prefs = new List<List<int>>();
            if (pk.Count <= 4) Permute(pk, 0, prefs);
            else for (int r = 0; r < pk.Count; r++) prefs.Add([.. pk.Skip(r), .. pk.Take(r)]);
            if (prefs.Count == 0) prefs.Add([]);
            int cap = Math.Max(20000, fo.Sched / prefs.Count);
            bool bOK = false; List<FPush> bPath = []; int[] bEnd = []; bool first = true;
            foreach (var pref in prefs)
            {
                foreach (var s in sg) s.Rank = sg.Count;
                for (int r = 0; r < pref.Count; r++) sg[pref[r]].Rank = r;
                var (ok, path, end) = Schedule(sg, cap);
                if (first || ok || path.Count > bPath.Count) { bOK = ok; bPath = path; bEnd = end; first = false; }
                if (ok || Expired()) break;
            }
            foreach (var p in bPath) if (!PushReal(p)) return (false, 0); // cannot happen: every step was checked
            foreach (var s in sg)
            {
                if (s.Plan == null) continue;
                string key = GKey(s.G);
                int j = bEnd[s.Off];
                if (j == s.Plan.Len) live.Remove(key);
                else if (j > 0) live[key] = s.Plan.From(j);
            }
            return (bOK, bPath.Count);
        }

        void ResetBoard()
        {
            Array.Clear(Occ);
            boxes = [.. lv.Boxes];
            foreach (int b in boxes) Occ[b] = true;
            Pl = lv.Player;
            Lurd.Clear();
        }

        public bool AllDone() { foreach (int b in boxes) if (!lv.Goal[b]) return false; return true; }

        (int idx, bool isTriv) Blocker(FGroup g)
        {
            var want = new List<int>();
            if (live.TryGetValue(GKey(g), out var p) && p != null && p.Len > 0) want.Add(p.Push(0).Box - d[p.Push(0).Dir]);
            else foreach (int b in CurBoxes(g)) foreach (int dd in d) want.Add(b + dd);
            var cs = new List<(int i, bool triv, int nb)>();
            for (int i = 0; i < groups.Count; i++) if (!ReferenceEquals(groups[i], g)) cs.Add((i, false, groups[i].Boxes.Length));
            for (int i = 0; i < triv.Count; i++) cs.Add((i, true, triv[i].Boxes.Length));
            cs = cs.OrderBy(c => c.nb).ToList(); // stable
            var occ = occ2;
            foreach (var c in cs)
            {
                var h = c.triv ? triv[c.i] : groups[c.i];
                Array.Copy(Occ, occ, Occ.Length);
                foreach (int x in h.Cells) occ[x] = false;
                Reach(Pl, occ);
                foreach (int x in want) if (!lv.Wall[x] && mark[x] == stamp) return (c.i, c.triv);
            }
            return (-1, false);
        }

        static FGroup Merge(FGroup a, FGroup b)
        {
            int[] U(int[] x, int[] y) { var z = x.Concat(y).ToArray(); Array.Sort(z); return z; }
            return new FGroup { Cells = U(a.Cells, b.Cells), Boxes = U(a.Boxes, b.Boxes), Goals = U(a.Goals, b.Goals) };
        }

        string LurdKey()
        {
            var bx = boxes.ToArray(); Array.Sort(bx);
            var sb = new StringBuilder();
            foreach (int x in bx.Append(Pl)) sb.Append((char)(x & 0xFF)).Append((char)((x >> 8) & 0xFF)).Append((char)((x >> 16) & 0xFF)).Append((char)((x >> 24) & 0xFF));
            return sb.ToString();
        }

        bool AltEngine()
        {
            foreach (var g in groups)
            {
                if (GroupDone(g) || IsFree(g)) continue;
                string key = GKey(g);
                foreach (var e in Engines)
                {
                    planEng.TryGetValue(key, out var set);
                    if ((set != null && set.Contains(e)) || Expired() || PoolN - Used < Reserve / 2) continue;
                    if (!planEng.TryGetValue(key, out set)) planEng[key] = set = new();
                    set.Add(e);
                    for (int step = 0; step < FactorSteps; step++)
                    {
                        var p = RelaxedPlanBy(g, step, e);
                        if (p != null)
                        {
                            cache.TryGetValue(key, out var old);
                            if (old == null || !old.SamePushes(p)) { cache[key] = p; return true; }
                            break;
                        }
                    }
                }
            }
            return false;
        }

        /// <summary>One run with the current pool: plan, schedule, re-plan, merge.</summary>
        public bool Attempt(List<FGroup> nt, List<FGroup> trivIn)
        {
            groups = [.. nt]; triv = [.. trivIn];
            while (true)
            {
                ResetBoard();
                live = new();
                for (int step = 0; step < FactorSteps; step++)
                {
                    var order = Enumerable.Range(0, groups.Count).OrderBy(i => groups[i].Boxes.Length).ToList();
                    foreach (int i in order)
                    {
                        var g = groups[i];
                        if (IsFree(g) || cache.ContainsKey(GKey(g)) || PoolN - Used < Reserve + 1000 || Expired()) continue;
                        var p = RelaxedPlan(g, step);
                        if (p != null) cache[GKey(g)] = p;
                    }
                }
                foreach (var g in groups)
                {
                    if (IsFree(g)) continue;
                    if (!cache.TryGetValue(GKey(g), out var p) || p == null) return false;
                    live[GKey(g)] = p;
                }
                var tried = new HashSet<string>();
                bool solved = false;
                for (int round = 1; ; round++)
                {
                    if (AllDone()) { solved = true; break; }
                    if (round > 12 || PoolN - Used < 1000 || Expired()) break;
                    var (ok, moved) = TrySchedule();
                    if (ok) continue;
                    bool progressed = false;
                    var left = new List<int>();
                    for (int i = 0; i < groups.Count; i++) if (!GroupDone(groups[i])) left.Add(i);
                    int Off(int i) { int c = 0; foreach (int b in CurBoxes(groups[i])) if (!lv.Goal[b]) c++; return c; }
                    left = left.OrderBy(Off).ToList(); // stable
                    foreach (int i in left)
                    {
                        var g = groups[i];
                        if (IsFree(g)) continue;
                        string key = GKey(g) + LurdKey();
                        if (!tried.Add(key)) continue;
                        var mine = CurBoxes(g);
                        var walls = (bool[])Occ.Clone();
                        foreach (int b in mine) walls[b] = false;
                        var rows = Rows(walls, CellSet(mine), CellSet(g.Goals), Pl);
                        foreach (var c in ReplanCalls())
                        {
                            var (sol, why) = SubSolve(rows, c.Lane, c.Cap * CapScale);
                            if (sol != "" && ApplyLurd(sol)) { live.Remove(GKey(g)); progressed = true; break; }
                            if (why is "pool" or "parse" or "infeasible") break;
                        }
                        if (progressed) break;
                    }
                    if (!progressed && moved == 0) break;
                }
                if (solved) return true;
                if (PoolN - Used < Reserve / 2 || Expired()) return false;
                if (AltEngine()) continue; // another engine's plan for an unfinished group: schedule again from the start
                // merge the first stuck group with its blocker; restart from the initial position
                int bi = -1, si = -1; bool bt = false;
                for (int i = 0; i < groups.Count; i++)
                {
                    if (GroupDone(groups[i])) continue;
                    var (j, t) = Blocker(groups[i]);
                    if (j >= 0) { bi = j; bt = t; si = i; break; }
                }
                if (si < 0 || (!bt && groups.Count == 2 && triv.Count == 0)) return false; // no blocker, or the merge would be the whole board
                FGroup other;
                if (bt) { other = triv[bi]; triv.RemoveAt(bi); }
                else other = groups[bi];
                var m = Merge(groups[si], other);
                var keepG = new List<FGroup>();
                for (int i = 0; i < groups.Count; i++) if (i != si && (bt || i != bi)) keepG.Add(groups[i]);
                keepG.Add(m);
                groups = keepG;
            }
        }
    }
}
