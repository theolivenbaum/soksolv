using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

public struct DiveItem { public int K0, K1, K2, K3; public uint Seq; public int Id; }

/// <summary>Min-heap on the lexicographic key (smaller first), newest first on ties (Go diveHeap).</summary>
internal sealed class DiveHeap
{
    DiveItem[] a = new DiveItem[8]; int n;
    public int Count => n;
    public long Bytes => a.Length * 24L;
    static bool Less(in DiveItem x, in DiveItem y)
    {
        if (x.K0 != y.K0) return x.K0 < y.K0;
        if (x.K1 != y.K1) return x.K1 < y.K1;
        if (x.K2 != y.K2) return x.K2 < y.K2;
        if (x.K3 != y.K3) return x.K3 < y.K3;
        return x.Seq > y.Seq;
    }
    public void Push(in DiveItem it)
    {
        if (n == a.Length) Array.Resize(ref a, n * 2);
        a[n] = it; int i = n++;
        while (i > 0)
        {
            int p = (i - 1) / 2;
            if (!Less(a[i], a[p])) break;
            (a[i], a[p]) = (a[p], a[i]); i = p;
        }
    }
    public DiveItem Pop()
    {
        var top = a[0];
        n--; a[0] = a[n];
        int i = 0;
        while (true)
        {
            int l = 2 * i + 1, r = 2 * i + 2, m = i;
            if (l < n && Less(a[l], a[m])) m = l;
            if (r < n && Less(a[r], a[m])) m = r;
            if (m == i) break;
            (a[i], a[m]) = (a[m], a[i]); i = m;
        }
        return top;
    }
}

/// <summary>One return order's FESS cells: cell key (U, -R), sorted; best cell and round robin alternate (Go diveAxis).</summary>
internal sealed class DiveAxis
{
    readonly Dictionary<(int, int), int> ids = new();
    public readonly List<(int, int)> Keys = new();
    readonly List<DiveHeap> heaps = new();
    readonly List<int> order = new();
    int turn, rr;
    public int Size;
    public long Bytes => Keys.Capacity * 8L + order.Capacity * 8L + ids.Count * 48L + heaps.Sum(h => h.Bytes);

    public void Add((int, int) cell, in DiveItem it)
    {
        if (!ids.TryGetValue(cell, out int c))
        {
            c = Keys.Count; ids[cell] = c; Keys.Add(cell); heaps.Add(new DiveHeap());
            int lo = 0, hi = order.Count;                       // first position with key >= cell
            while (lo < hi)
            {
                int mid = (lo + hi) / 2; var kx = Keys[order[mid]];
                int cmp = kx.Item1 != cell.Item1 ? kx.Item1 - cell.Item1 : kx.Item2 - cell.Item2;
                if (cmp < 0) lo = mid + 1; else hi = mid;
            }
            order.Insert(lo, c);
        }
        heaps[c].Push(it);
        Size++;
    }

    /// <summary>Alternates the best non-empty cell and round robin over the non-empty cells.</summary>
    public DiveItem Pop()
    {
        turn++;
        int pick = -1;
        if (turn % 2 == 1)
        {
            foreach (int c in order) if (heaps[c].Count > 0) { pick = c; break; }
        }
        else
        {
            int live = 0;
            foreach (int c in order) if (heaps[c].Count > 0) live++;
            rr++;
            int k = rr % live;
            foreach (int c in order)
                if (heaps[c].Count > 0) { if (k == 0) { pick = c; break; } k--; }
        }
        Size--;
        return heaps[pick].Pop();
    }
}

internal sealed class DiveDual { public int[] Dual = []; public long Cost; }

/// <summary>Dive lane state (Go diveState).</summary>
internal sealed class DiveState
{
    public bool[] Land = [], Start = [];
    public int[] Chain = []; public int ChainLen;
    public readonly DiveHeap Dive = new();
    public readonly DiveAxis[] Axes = [new(), new()];
    public bool[] AxisOn = [true, true];
    public readonly List<ulong> Done = new();
    public readonly List<int> Depth = new();
    public uint Seq; public int Turn, Ret;
    public readonly DiveDual DD = new(), DR = new();
    public int FreeExp = -1, FreeDepth, FreeDist, BestE;
    public int PopsDive, PopsRet;
    public long Bytes => Dive.Bytes + Axes[0].Bytes + Axes[1].Bytes + Done.Capacity * 8L + Depth.Capacity * 4L;
}

public sealed unsafe partial class FullSolver
{
    /// <summary>Dive engine options (Go: exp-691/693 defaults).</summary>
    public sealed class DiveOptions
    {
        public string Ret = "both";        // return axes: both, r1 or r2
        public bool A;                      // strategy A's dive terms (the paired A engine)
        public bool FastApply = true;       // store children without a second box search
        public long ShareMask = 1023;       // a sharing engine yields every ShareMask+1 expansions
        public int MinPeel = 10;            // activation peel depth (the gate)
    }

    DiveState? dv;
    bool diveA;                             // this engine uses strategy A's terms (Go diveAOn)
    List<DiveCand> diveCands = new();
    uint divePrimeDummy;

    struct DiveCand { public int I, T, Side, Plen; }

    // ---- static analysis: peel depth gate (Go diveMaxPeel / divePeel) --------------------------------------------------------------
    public static int DiveMaxPeel(Level lv)
    {
        var peel = DivePeel(lv.Wall, lv.Goal, lv.W, lv.Player);
        var start = new bool[lv.Wall.Length];
        foreach (int b in lv.Boxes) start[b] = true;
        int best = 0;
        for (int c = 0; c < lv.Goal.Length; c++) if (lv.Goal[c] && !start[c] && peel[c] > best) best = peel[c];
        return best;
    }

    static int[] DivePeel(bool[] wall, bool[] occ0, int w, int player)
    {
        int n = wall.Length;
        int[] dirs = [-w, w, -1, 1];
        var floor = new bool[n];
        var q = new List<int> { player };
        floor[player] = true;
        for (int i = 0; i < q.Count; i++)
            foreach (int d in dirs)
            {
                int y = q[i] + d;
                if (y >= 0 && y < n && !wall[y] && !floor[y]) { floor[y] = true; q.Add(y); }
            }
        var occ = (bool[])occ0.Clone();
        var lay = new int[n];
        int left = 0;
        for (int c = 0; c < n; c++) { lay[c] = -1; if (occ[c]) left++; }
        bool Free(int c) => c >= 0 && c < n && floor[c] && !occ[c];
        int L = 0;
        var cur = new List<int>();
        while (left > 0)
        {
            cur.Clear();
            for (int c = 0; c < n; c++)
            {
                if (!occ[c]) continue;
                foreach (int d in dirs) if (Free(c + d) && Free(c + 2 * d)) { cur.Add(c); break; }
            }
            if (cur.Count == 0) break;
            foreach (int c in cur) { lay[c] = L; occ[c] = false; left--; }
            L++;
        }
        for (int c = 0; c < n; c++) if (occ[c]) lay[c] = L + 5;
        return lay;
    }

    /// <summary>Strategy A's term for a board in occ (smaller is better; 0 when off).</summary>
    int DiveATerm(ReadOnlySpan<int> boxes)
    {
        if (!diveA) return 0;
        int w = W, crowd = 0, gap = 0;
        bool Full(int c) => wall[c] || occ[c];
        Span<int> ring = [-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1];
        foreach (int b in boxes)
        {
            int nb = 0;
            foreach (int o in ring) { int c = b + o; if (c >= 0 && c < occ.Length && Full(c)) nb++; }
            if (nb >= 6) crowd++;
            foreach (int d in (ReadOnlySpan<int>)[1, w])
            {
                int c = b + 2 * d;
                if (c < occ.Length && occ[c] && !Full(b + d)) gap++;
            }
        }
        return (crowd << 12) - Math.Min(gap, 4095);
    }

    // ---- matching distances (matchPrime / matchChild) ---------------------------------------------------------------------------------------
    int MatchPrime(ReadOnlySpan<int> boxes, int[] dist, DiveDual dd)
    {
        int score = AssignTo(boxes, dist);
        if (score < 0) score = AssignmentInf;
        int m = n + 1;
        if (dd.Dual.Length != 4 * m) dd.Dual = new int[4 * m];
        Array.Copy(dhU, 0, dd.Dual, 0, m);
        Array.Copy(dhV, 0, dd.Dual, m, m);
        Array.Copy(dhP, 0, dd.Dual, 2 * m, m);
        for (int g = 1; g < m; g++) dd.Dual[3 * m + dhP[g]] = g;
        dd.Cost = score;
        if (score == AssignmentInf) dd.Cost = AssignmentCost(boxes, dist);
        return score;
    }

    /// <summary>The matching cost with boxes[moved] replaced (old was there).</summary>
    int MatchChild(ReadOnlySpan<int> boxes, int moved, int old, int[] dist, DiveDual dd)
    {
        int m = n + 1;
        int row = moved + 1;
        int goalCol = dd.Dual[3 * m + row];
        int box = boxes[moved];
        int u = int.MaxValue;
        for (int j = 1; j < m; j++) u = Math.Min(u, AssignEdge(dist, box, j) - dd.Dual[m + j]);
        int newCost = AssignEdge(dist, box, goalCol);
        if (u == newCost - dd.Dual[m + goalCol])
        {
            int oldCost = AssignEdge(dist, old, goalCol);
            return (int)Math.Min(AssignmentInf, dd.Cost + newCost - oldCost);
        }
        Array.Copy(dd.Dual, 0, dhU, 0, m);
        Array.Copy(dd.Dual, m, dhV, 0, m);
        Array.Copy(dd.Dual, 2 * m, dhP, 0, m);
        dhU[row] = u; dhP[goalCol] = 0; dhV[0] = 0;
        AugmentAssignment(boxes, dist, row);
        return (int)Math.Min(AssignmentInf, AssignmentCost(boxes, dist));
    }

    // ---- setup / features ------------------------------------------------------------------------------------------------------------------------
    void DiveSetup(DiveOptions dopt)
    {
        var d = new DiveState { Land = new bool[cells], Start = (bool[])goal.Clone(), FreeExp = -1 };
        switch (dopt.Ret)
        {
            case "r1": d.AxisOn = [true, false]; break;
            case "r2": d.AxisOn = [false, true]; break;
            default: d.AxisOn = [true, true]; break;
        }
        var realGoal = new bool[cells];
        foreach (int g in lv.Boxes) { realGoal[g] = true; if (!goal[g]) d.Land[g] = true; }
        d.Chain = new int[cells]; Array.Fill(d.Chain, -1);
        foreach (int g in lv.Boxes)
        {
            if (!d.Land[g]) continue;
            d.Chain[g] = 0;
            var q = new List<int> { g };
            for (int i = 0; i < q.Count; i++)
                foreach (int dir in dirs)
                {
                    int y = q[i] + dir;
                    if (realGoal[y] && d.Chain[y] < 0) { d.Chain[y] = d.Chain[q[i]] + 1; d.ChainLen = Math.Max(d.ChainLen, d.Chain[y] + 1); q.Add(y); }
                }
            break;
        }
        d.BestE = int.MaxValue;
        dv = d;
    }

    /// <summary>Child-only features of a stored node (occ holds its boxes): U, prefix and whether the real player is in its region.</summary>
    (int u, int prefix, bool home) DiveFeat(int id, ReadOnlySpan<int> boxes)
    {
        var d = dv!;
        Reach(nodes.Player(id), mark2);
        uint st = stamp;
        int u = 0;
        foreach (int b in boxes)
        {
            bool touched = false;
            foreach (int dir in dirs) if (mark2[b + dir] == st) { touched = true; break; }
            if (!touched) u++;
        }
        bool home = mark2[realPlayer] == st;
        int prefix = 0;
        if (d.ChainLen > 0)
        {
            prefix = d.ChainLen;
            foreach (int g in lv.Boxes) { int v = d.Chain[g]; if (v >= 1 && !occ[g] && v < prefix) prefix = v; }
        }
        return (u, prefix, home);
    }

    void DivePush(int id, int e, int u, int a, int dist, int onStart, int revh, int prefix, int depth)
    {
        var d = dv!;
        while (id >= d.Depth.Count) d.Depth.Add(0);
        d.Depth[id] = depth;
        d.Seq++;
        if (dist >= AssignmentInf) dist = -1;
        if (e > 0) { d.Dive.Push(new DiveItem { K0 = e, K1 = u, K2 = a, K3 = -dist, Seq = d.Seq, Id = id }); return; }
        if (d.FreeExp < 0) { d.FreeExp = (int)mExpandedN; d.FreeDepth = depth; d.FreeDist = dist; }
        if (d.AxisOn[0]) d.Axes[0].Add((u, -onStart), new DiveItem { K0 = revh, Seq = d.Seq, Id = id });
        if (d.AxisOn[1]) d.Axes[1].Add((u, -prefix), new DiveItem { K0 = -dist, Seq = d.Seq, Id = id });
    }

    /// <summary>Next node to expand (never one expanded before).</summary>
    bool DivePop(out int id)
    {
        var d = dv!;
        while (true)
        {
            bool ret = d.Axes[0].Size + d.Axes[1].Size > 0;
            d.Turn++;
            DiveItem it; bool ok = false; it = default;
            if (d.Dive.Count > 0 && (!ret || d.Turn % 4 == 0)) { it = d.Dive.Pop(); ok = true; d.PopsDive++; }
            else if (ret)
            {
                d.Ret++;
                int ax = d.Ret % 2;
                if (d.Axes[ax].Size == 0) ax = 1 - ax;
                it = d.Axes[ax].Pop(); ok = true; d.PopsRet++;
            }
            if (!ok) { id = -1; return false; }
            int w = it.Id >> 6;
            while (w >= d.Done.Count) d.Done.Add(0);
            if ((d.Done[w] & (1UL << (it.Id & 63))) != 0) continue;
            d.Done[w] |= 1UL << (it.Id & 63);
            id = it.Id; return true;
        }
    }

    /// <summary>Visitor of the box search for the dive: one reachable box destination t of the box being searched.</summary>
    bool DiveVisit(int k, int t, ReadOnlySpan<int> cur)
    {
        int i = mVisitI;
        int canonical = 0;
        for (int side = 0; side < 4; side++)
            if (m3[t - dirs[side]] == mpstamp) { canonical = side; break; }
        int endpoint = t * 4 + canonical;
        if (mtseen[endpoint] == mtst) return true;
        mtseen[endpoint] = mtst;
        SetOcc(t, true);
        if (MPushDead(cur, i, t, canonical) == 0)
        {
            int plen = 1;
            for (int x = mbprev[k]; x >= 0; x = mbprev[x]) plen++;
            diveCands.Add(new DiveCand { I = i, T = t, Side = k % 4, Plen = plen });
            stats.Generated++;
        }
        else stats.Freeze++;
        SetOcc(t, false);
        return true;
    }

    /// <summary>apply without the second box search: the candidate's final push direction and length come from move generation.</summary>
    int DiveApply(int id, in DiveCand c, ReadOnlySpan<int> cur, Span<int> child, ulong parentHash)
    {
        mLastAdded = -1;
        int b = cur[c.I];
        cur.CopyTo(child);
        { // cur is sorted: slide the moved box to its place (Span.Sort allocates in the AOT runtime)
            int k = c.I, nn = child.Length, tv = c.T;
            while (k + 1 < nn && child[k + 1] < tv) { child[k] = child[k + 1]; k++; }
            while (k > 0 && child[k - 1] > tv) { child[k] = child[k - 1]; k--; }
            child[k] = tv;
        }
        ulong* pk = nodes.Key(id);
        for (int x = 0; x < nodes.Nw; x++) ckey[x] = pk[x];
        Flip(b); Flip(c.T);
        SetOcc(b, false); SetOcc(c.T, true);
        int np = Reach(PlayerAfter(c.T, dirs[c.Side]), mark2);
        ulong zh = parentHash ^ nodes.ZBox(b) ^ nodes.ZBox(c.T) ^ nodes.ZPlayer(nodes.Player(id)) ^ nodes.ZPlayer(np);
        int cid = DiveAdd(zh, np, id, b, (sbyte)c.Side, nodes.G(id) + c.Plen);
        SetOcc(c.T, false); SetOcc(b, true);
        return cid;
    }

    /// <summary>add for the dive: only what the pull path reads (transposition table, tail). A duplicate reached by a shorter path is reparented.</summary>
    int DiveAdd(ulong zh, int player, int parent, int from, sbyte dir, int g)
    {
        int id = nodes.Find(ckey, player, zh, out int slot, out uint fp);
        if (id >= 0)
        {
            stats.Duplicates++;
            mLastAdded = id;
            if (g >= nodes.G(id) || MCreatesCycle(id, parent)) return -1;
            nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g;
            return -1;
        }
        if (mMemFull) { mLastAdded = -1; return -1; }
        id = nodes.Insert(slot, fp, ckey, player);
        mLastAdded = id;
        nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g;
        if (nodes.NeedsGrow)
        {
            if (opt.BudgetBytes > 0 && DiveBytes() + (ulong)nodes.TableSlots * 16 > opt.BudgetBytes / (ulong)(bat?.Live() ?? 1)) mMemFull = true;
            else nodes.Grow();
        }
        return id;
    }

    bool mMemFull;
    ulong DiveBytes() => (ulong)(nodes.Bytes + (dv?.Bytes ?? 0));

    // ---- entry points ------------------------------------------------------------------------------------------------------------------------------
    /// <summary>Go `-strategy dive`: the paired base + A engines alternating on a baton (1024-expansion slices), each with half of -nodes.</summary>
    public static SolveResult SolveDive(string[] rows, FullOptions opt, double timeLimitSec, DiveOptions? dopt = null, bool pair = true)
    {
        dopt ??= new DiveOptions();
        Level lv;
        try { lv = Level.Parse(rows, opt.CloseHoles); }
        catch (InvalidDataException e) { return new SolveResult { Reason = e.Message }; }
        var sw = Stopwatch.StartNew();
        FullSolver NewEngine(bool a, Baton? baton, int me, long div)
        {
            var pv = PullView(lv, out var roots, out var spread);
            var o = opt.Clone();
            o.Pull = true; o.Penalties = false; o.MacroStage = 1; o.MacroFeatures = 0; o.Net = null;
            var s = new FullSolver(pv, o) { spreadDist = spread, realPlayer = lv.Player, pullRoots = roots, diveOn = true, diveA = a, bat = baton, batMe = me };
            if (opt.NodeBudget > 0 && div > 1) s.nodeLimitOverride = Math.Max(opt.NodeBudget / div, 1);
            s.diveOpt = dopt;
            return s;
        }
        if (!pair)
        {
            using var s = NewEngine(false, null, 0, 1);
            var r1 = s.RunDive(timeLimitSec);
            r1.TimeMs = sw.Elapsed.TotalMilliseconds;
            return r1;
        }
        var pb = new Baton([true, true, false], 0);
        SolveResult ra = new() { Reason = "not run" };
        var t = new Thread(() =>
        {
            pb.Wait(1);
            try { using var sa = NewEngine(true, pb, 1, 2); ra = sa.RunDive(timeLimitSec); }
            catch (Exception e) { ra = new SolveResult { Reason = "dive-a: " + e.GetType().Name + " " + e.Message }; }
            finally { pb.Done(1, ra.Solved); }
        }, 256 << 20) { IsBackground = true, Name = "dive-a" };
        t.Start();
        pb.Release(0);
        pb.Wait(0);
        SolveResult r;
        try { using var sb = NewEngine(false, pb, 0, 2); r = sb.RunDive(timeLimitSec); }
        catch (Exception e) { r = new SolveResult { Reason = "dive: " + e.GetType().Name + " " + e.Message }; }
        pb.Done(0, r.Solved);
        t.Join();
        var res = !r.Solved && ra.Solved ? ra : r;
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        return res;
    }

    DiveOptions diveOpt = new();

    SolveResult RunDive(double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        mNodeLimit = nodeLimitOverride > 0 ? nodeLimitOverride : opt.NodeBudget;
        mDeadlineMs = opt.NodeBudget > 0 || timeLimitSec <= 0 ? 0 : timeLimitSec * 1000;
        m3 = GC.AllocateArray<uint>(cells, pinned: true);
        m3P = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref m3[0]);
        mpost = new uint[4 * cells]; mseen = new uint[4 * cells]; mtseen = new uint[4 * cells];
        mbprev = new int[4 * cells]; mbq = new int[4 * cells + 4];
        mCur = new int[n]; mChild = new int[n];
        if (MExpired()) return Fin(new SolveResult { Reason = "timeout" }, sw);
        var start = (int[])lv.Boxes.Clone();
        Array.Sort(start);
        int h0 = 0;
        foreach (int b in start)
        {
            if (dist[b] < 0) return Fin(new SolveResult { Reason = "box on dead square" }, sw);
            h0 += dist[b];
            SetOcc(b, true);
        }
        if (h0 == 0) { Array.Clear(occ); return Fin(new SolveResult { Solved = true }, sw); }
        int[] roots = pullRoots;
        if (PfReachOn) PfReachInit();
        if (PfZoneDeadLimit > 0 && roots.Length > 1) roots = LiveZoneRoots(roots);
        foreach (int rp in roots)
        {
            int p0 = Reach(rp, mark);
            for (int x = 0; x < nodes.Nw; x++) ckey[x] = 0;
            foreach (int b in start) Flip(b);
            DiveAdd(nodes.HashOf(start, p0), p0, -1, -1, -1, 0);
        }
        foreach (int b in start) SetOcc(b, false);
        mStarted = true;
        var res = DiveLoop(sw);
        return Fin(res, sw);
    }

    SolveResult Fin(SolveResult r, Stopwatch sw)
    {
        r.TimeMs = sw.Elapsed.TotalMilliseconds;
        r.Stats = stats; stats.Stored = nodes.Count;
        r.PeakBytes = (long)DiveBytes();
        return r;
    }

    SolveResult DiveLoop(Stopwatch sw)
    {
        PfInit();
        DiveSetup(diveOpt);
        var d = dv!;
        int[] cur = new int[n], child = new int[n];
        // roots
        for (int id = 0; id < nodes.Count; id++)
        {
            nodes.Unpack(id, cur);
            foreach (int b in cur) SetOcc(b, true);
            int e = 0, on = 0;
            foreach (int b in cur) { if (d.Land[b]) e++; if (d.Start[b]) on++; }
            var (u, pre, _) = DiveFeat(id, cur);
            int dst = AssignTo(cur, pfDist!); if (dst < 0) dst = AssignmentInf;
            int revh = AssignTo(cur, goalDist); if (revh < 0) revh = AssignmentInf;
            foreach (int b in cur) SetOcc(b, false);
            DivePush(id, e, u, DiveATerm(cur), dst, on, revh, pre, 0); // Go evaluates diveA after clearing occ (a quirk, mirrored)
        }
        long expanded = 0, lastMem = 0;
        while (true)
        {
            if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
            if (mMemFull) return new SolveResult { Reason = "memory", Nodes = expanded };
            long now = sw.ElapsedMilliseconds;
            if (now - lastMem >= 50)
            {
                lastMem = now;
                if (opt.BudgetBytes > 0 && DiveBytes() > opt.BudgetBytes / (ulong)(bat?.Live() ?? 1)) return new SolveResult { Reason = "memory", Nodes = expanded };
            }
            if (!DivePop(out int id)) return new SolveResult { Reason = "exhausted", Nodes = expanded };
            expanded++; mExpandedN++;
            if (bat != null && (mExpandedN & diveOpt.ShareMask) == 0 && !bat.Yield(batMe)) return new SolveResult { Reason = "stopped", Nodes = expanded };
            nodes.Unpack(id, cur);
            int pE = 0, pOn = 0;
            foreach (int b in cur) { SetOcc(b, true); if (d.Land[b]) pE++; if (d.Start[b]) pOn++; }
            if (!Feasible(cur)) { foreach (int b in cur) SetOcc(b, false); continue; }
            Reach(nodes.Player(id), mark);
            uint st = stamp;
            diveCands.Clear();
            for (int i = 0; i < n; i++)
            {
                mtst++;
                mVisitI = i;
                MBoxSearch(cur[i], st, false, 0, 0, cur);
            }
            if (diveCands.Count > 0)
            {
                MatchPrime(cur, pfDist!, d.DD);
                bool primedR = false;
                int pDepth = d.Depth[id];
                int solved = -1;
                ulong parentHash = nodes.HashOf(cur, nodes.Player(id));
                var cands = diveCands.ToArray();
                foreach (var c in cands)
                {
                    int b = cur[c.I];
                    cur[c.I] = c.T;
                    int dst = MatchChild(cur, c.I, b, pfDist!, d.DD);
                    int revh = 0;
                    if (d.AxisOn[0] && pE - B2I(d.Land[b]) + B2I(d.Land[c.T]) == 0)
                    {
                        if (!primedR) { cur[c.I] = b; MatchPrime(cur, goalDist, d.DR); cur[c.I] = c.T; primedR = true; }
                        revh = MatchChild(cur, c.I, b, goalDist, d.DR);
                    }
                    cur[c.I] = b;
                    int e = pE, on = pOn;
                    if (d.Land[b]) e--;
                    if (d.Land[c.T]) e++;
                    if (d.Start[b]) on--;
                    if (d.Start[c.T]) on++;
                    int cid = DiveApply(id, c, cur, child, parentHash);
                    if (cid < 0 || cid < d.Depth.Count) continue; // stored before (a reparent keeps its filing)
                    SetOcc(b, false); SetOcc(c.T, true);
                    var (u, pre, home) = DiveFeat(cid, child);
                    SetOcc(c.T, false); SetOcc(b, true); // in this order: a macro may end on its own square
                    d.BestE = Math.Min(d.BestE, e);
                    int a = 0;
                    if (diveA && e > 0)
                    {
                        SetOcc(b, false); SetOcc(c.T, true);
                        a = DiveATerm(child);
                        SetOcc(c.T, false); SetOcc(b, true);
                    }
                    DivePush(cid, e, u, a, dst, on, revh, pre, pDepth + 1);
                    if (on == n && home) { if (Environment.GetEnvironmentVariable("DIVE_DEBUG") != null) Console.Error.WriteLine($"allstart id={cid} exp={mExpandedN} me={batMe}"); solved = cid; break; }
                }
                if (solved >= 0)
                {
                    foreach (int b in cur) SetOcc(b, false);
                    string sol = PullSolution(solved);
                    if (sol != "") return new SolveResult { Solved = true, Solution = sol, Nodes = expanded };
                    continue;
                }
            }
            foreach (int b in cur) SetOcc(b, false);
        }
    }

    static int B2I(bool b) => b ? 1 : 0;
}
