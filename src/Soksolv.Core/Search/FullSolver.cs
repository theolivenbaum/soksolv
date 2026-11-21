using System.Diagnostics;
using Soksolv.Model;

namespace Soksolv.Search;

public struct Push { public int From; public sbyte Dir; public Push(int f, sbyte d) { From = f; Dir = d; } }

public sealed class SolveStats
{
    public int Plan4Exp, Plan4Swap; public bool Plan4Pending, Plan4Complete;
    public long Generated, Duplicates, CorralDead, FrontierCorrals, DispAdvisor, CorridorFlow, PackingPrepQueries, PackingPrepPicks, PackingPrepClears, DeadSquare, Frozen2x2, GoalArea, Freeze, PairDead, ClusterDead, SideDead, Assignment, Macros, ConnAdvisor, Corrals, Penalized, PairsTried, PairsDead;
    public int Stored; public int LeanAt;
}

public sealed class SolveResult
{
    public bool Solved; public string Reason = ""; public string Solution = ""; public long Nodes; public SolveStats Stats = new();
    public double TimeMs; public long PeakBytes; public string Strategy = ""; public string Fingerprint = "";
}

/// <summary>Shared cancellation: set by the portfolio when one lane solved or the deadline passed.</summary>
public sealed class CancelFlag { volatile bool stopped; public bool Stopped => stopped; public void Cancel() => stopped = true; }

public sealed class FullOptions
{
    public bool SideDead = true, ClusterDead = true, Penalties = true, FreeGoal = true, CloseHoles = true;
    public long NodeBudget;           // > 0: stop after this many expansions, ignore the clock
    public ulong BudgetBytes;         // 0: unlimited (tracked data structures)
    public int Seed;
    public ClusterTable? SharedClusters, SharedPenalties;
    public Search.DeadZone.DeadZoneTable? SharedDeadZone;
    public volatile bool Stop;
    public CancelFlag? Cancel;
    /// <summary>Bidirectional meeting network (null: no meetings). Forward lanes add to Net.Fwd, probe Net.Back, read mailbox MeetOwner of Net.Fwd.</summary>
    public Meet.MeetNet? Net;
    public int MeetOwner;
    /// <summary>Macro lane: highest ported feature stage (1..5) and the stage-6 default-on feature mask (bit0 move windows, ...).</summary>
    public int MacroStage = 5, MacroFeatures = 255; // all ported stage-6 features (bits 0-4: windows, deadzone+frontier corral, displacement, corridor flow, obligations)
    /// <summary>Go StallFinish (timed runs only): when the best node stops improving, try a short private backward search toward it.
    /// StallScale = timeLimit/600 s when the limit is below 600 s (main.go -stall-scale), scaling the 1 s cap/floor.</summary>
    public bool StallFinish = true;
    /// <summary>Pull mode (Go pull lane): the macro engine run backward on a pull view of the level (see Pull/FullSolver.Pull.cs).</summary>
    public bool Pull;
    /// <summary>Novelty lane (Go solver.novelty = 100): at most this many novel moves per opened node cost nothing (0: off).</summary>
    public int Novelty;
    /// <summary>Macro alt engine (exp-769): plan4 with festsink + roomsep + keep-parks 1.</summary>
    public bool PlanAlt;
    /// <summary>Go -macro-alt-plan (default on): the macro lane runs a second engine on the alt plan4 config, alternating on a baton.</summary>
    public bool MacroAlt = true;
    /// <summary>fa2 lane settings (Go FA2* flags, defaults as in Go).</summary>
    public Fa2Options Fa2 = new();
    /// <summary>Toward (hf) search switches, Go's -hf-festival bundle (default on) and -diag-dead=target.</summary>
    public bool HfNoReturn = true, HfFestCorral = true, HfFrozenMatch = true, HfFestSched = true, HfUnweighted = true, HfMatchFirst = true, HfOneAdvisor = true, HfBestFest = true, HfFestIneff = true, HfDiagDead = true;
    public FullOptions Clone() => (FullOptions)MemberwiseClone();
    public double StallScale = 1.0;
}

/// <summary>
/// Best-first FESS search over push states (port of Go package full, default configuration).
/// States: bitset of box floor-indices + normalized player. Records live in unmanaged chunked arenas.
/// </summary>
public sealed unsafe partial class FullSolver : IDisposable
{
    const string DirChars = "udlr";
    const int MaxRegions = 16;
    const uint MoveUsed = 1u << 31;
    const int NoGoalCost = 4096;

    readonly Level lv;
    readonly FullOptions opt;
    readonly int n, cells, W;
    readonly int[] dirs;
    readonly bool[] wall, goal;
    int[] dist = [];
    int[] goals = [];
    int[] goalDist = []; // goalDist[g*cells+c]
    readonly StateTable nodes;
    readonly SolveStats stats = new();

    // scratch
    readonly bool[] occ;
    readonly byte[] blkArr; readonly byte* blk; // wall | box, pinned, used by flood kernels
    uint[] mark, mark2; readonly uint* markP, mark2P, rmarkP; readonly int* queueP, cqueueP;
    uint stamp;
    readonly int[] queue;
    readonly bool[] fz;
    readonly uint[] cmark; uint cstamp;
    readonly int[] cqueue, barrier;
    readonly ChunkList<uint> mv = new();
    struct Opened { public int Start; public ushort Len, Next, Left; }
    readonly ChunkList<Opened> opened = new();
    KeyHeap heap = new();
    KeyHeap?[]? buckets;
    int turn, rr;
    bool lean, memFull;
    GoalArea? ga;
    SideDead? sd;
    ClusterChecker? cl;
    readonly ulong* ckey; // scratch child key
    readonly Dictionary<int, Push[]> macros = new();
    readonly Dictionary<int, Push[][]> nodeMacros = new();
    int[] round = [];
    bool[] entrance = [];
    uint[] mark3; uint pstamp, bstamp;
    int[] bprev;
    // hc memo
    readonly int[] cc; readonly uint[] ccMark; uint ccStamp;
    readonly List<int> freeG = new();
    int[]? noise;
    // regions()
    readonly uint[] rmark; uint rstamp; int[] floorCells = [];
    // feasible()
    int[] matchOf = [];
    // pair deadlocks
    int[] poff = [], popp = [];
    sbyte[] dpair = [];
    // scratch for open()
    struct Cand { public uint Code; public int Wd, Ch; }
    struct Push1 { public int I, B, T; public sbyte Dir; public int R, Pen; }
    Cand[] cands; Push1[] pushes1;
    readonly Stopwatch clock = Stopwatch.StartNew();
    // pull mode (Go solver.pull): front()/playerAfter()/supportCell() generalise push geometry
    readonly bool pull;
    int Front(int b, int d) => pull ? b + d : b - d;
    int PlayerAfter(int nc, int d) => pull ? nc + d : nc - d;
    int SupportCell(int b, int c, int d) => pull ? c + d : b - d;

    public FullSolver(Level level, FullOptions options)
    {
        pull = options.Pull;
        lv = level; opt = options; n = lv.Boxes.Length; cells = lv.Cells; W = lv.W;
        dirs = [-W, W, -1, 1]; wall = lv.Wall; goal = lv.Goal;
        occ = new bool[cells];
        mark = GC.AllocateArray<uint>(cells, pinned: true); mark2 = GC.AllocateArray<uint>(cells, pinned: true);
        queue = GC.AllocateArray<int>(cells, pinned: true); cqueue = GC.AllocateArray<int>(cells, pinned: true);
        rmark = GC.AllocateArray<uint>(cells, pinned: true); blkArr = GC.AllocateArray<byte>(cells, pinned: true);
        markP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref mark[0]); mark2P = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref mark2[0]);
        queueP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref queue[0]); cqueueP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref cqueue[0]);
        rmarkP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmark[0]); blk = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref blkArr[0]);
        for (int i = 0; i < cells; i++) blkArr[i] = (byte)(wall[i] ? 1 : 0);
        fz = new bool[cells]; cmark = new uint[cells]; barrier = new int[cells];
        cc = new int[cells]; ccMark = new uint[cells];
        mark3 = new uint[cells]; bprev = new int[4 * cells];
        bseenStamp = new uint[4 * cells];
        nodes = new StateTable(wall);
        ckey = (ulong*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(Math.Max(1, nodes.Nw) * 8));
        cands = new Cand[4 * n + 8]; pushes1 = new Push1[4 * n + 8];
        ComputeDistances();
        ComputeGoalDistances();
        ComputePackingOrder();
        PerturbInit(opt.Seed);
    }

    uint[] bseenStamp;

    public void Dispose()
    {
        nodes.Dispose(); mv.Dispose(); opened.Dispose(); heap.Dispose();
        if (buckets != null) foreach (var b in buckets) b?.Dispose();
        System.Runtime.InteropServices.NativeMemory.Free(ckey);
    }

    // ---- static analysis ------------------------------------------------------------------------------------
    void ComputeDistances()
    {
        dist = new int[cells]; Array.Fill(dist, -1);
        var q = queue; int tail = 0;
        for (int c = 0; c < cells; c++) if (goal[c]) { dist[c] = 0; q[tail++] = c; }
        for (int i = 0; i < tail; i++)
        {
            int c = q[i];
            foreach (int d in dirs)
            {
                int b = c - d;
                if (!wall[b] && !wall[SupportCell(b, c, d)] && dist[b] < 0) { dist[b] = dist[c] + 1; q[tail++] = b; }
            }
        }
    }

    void ComputeGoalDistances()
    {
        var gl = new List<int>();
        for (int c = 0; c < cells; c++) if (goal[c]) gl.Add(c);
        goals = [.. gl];
        goalDist = new int[goals.Length * cells]; Array.Fill(goalDist, -1);
        for (int g = 0; g < goals.Length; g++)
        {
            var d = goalDist.AsSpan(g * cells, cells);
            d[goals[g]] = 0;
            int tail = 0; queue[tail++] = goals[g];
            for (int i = 0; i < tail; i++)
            {
                int c = queue[i];
                foreach (int dir in dirs)
                {
                    int b = c - dir;
                    if (!wall[b] && !wall[SupportCell(b, c, dir)] && d[b] < 0) { d[b] = d[c] + 1; queue[tail++] = b; }
                }
            }
        }
    }

    // Box->goal feasibility as bitsets: goalAdj[c*gw + w] bit g set iff a box on c can reach goal g (goalDist >= 0).
    ulong[] goalAdj = [];
    int gw;
    ulong[] matchedMask = [], visitedMask = [];

    void InitMatching()
    {
        gw = (goals.Length + 63) / 64;
        goalAdj = new ulong[cells * gw];
        for (int g = 0; g < goals.Length; g++)
            for (int c = 0; c < cells; c++)
                if (goalDist[g * cells + c] >= 0) goalAdj[c * gw + (g >> 6)] |= 1UL << (g & 63);
        matchedMask = new ulong[gw]; visitedMask = new ulong[gw];
        matchOf = new int[goals.Length];
    }

    /// <summary>Does every box have a distinct reachable goal (perfect bipartite matching)? Greedy bit-ops first, augmenting paths only on conflicts.</summary>
    bool Feasible(ReadOnlySpan<int> boxes)
    {
        if (goalAdj.Length == 0) InitMatching();
        Array.Fill(matchOf, -1);
        Array.Clear(matchedMask);
        for (int i = 0; i < boxes.Length; i++)
        {
            int b = boxes[i];
            int baseW = b * gw;
            int found = -1;
            for (int w = 0; w < gw; w++)
            {
                ulong free = goalAdj[baseW + w] & ~matchedMask[w];
                if (free != 0) { found = (w << 6) + System.Numerics.BitOperations.TrailingZeroCount(free); break; }
            }
            if (found >= 0) { matchOf[found] = i; matchedMask[found >> 6] |= 1UL << (found & 63); continue; }
            Array.Clear(visitedMask);
            if (!Augment(i, boxes)) return false;
        }
        return true;
    }

    bool Augment(int i, ReadOnlySpan<int> boxes)
    {
        int baseW = boxes[i] * gw;
        for (int w = 0; w < gw; w++)
        {
            ulong cand = goalAdj[baseW + w] & ~visitedMask[w];
            while (cand != 0)
            {
                int g = (w << 6) + System.Numerics.BitOperations.TrailingZeroCount(cand);
                cand &= cand - 1;
                if ((visitedMask[w] & (1UL << (g & 63))) != 0) continue;
                visitedMask[w] |= 1UL << (g & 63);
                if (matchOf[g] < 0 || Augment(matchOf[g], boxes)) { matchOf[g] = i; matchedMask[g >> 6] |= 1UL << (g & 63); return true; }
                cand &= ~visitedMask[w]; // recursion may have visited more goals in this word
            }
        }
        return false;
    }

    void SetOcc(int c, bool v) { occ[c] = v; blk[c] = (byte)(v ? 1 : 0); }

    /// <summary>Marks cells reachable from p (not crossing walls or boxes) in mk with a fresh stamp; returns the minimum cell.</summary>
    int Reach(int p, uint[] mk) => ReachP(p, ReferenceEquals(mk, mark) ? markP : mark2P);

    int ReachP(int p, uint* mk)
    {
        if (stamp > uint.MaxValue - 4) { Array.Clear(mark); Array.Clear(mark2); stamp = 0; }
        uint st = ++stamp;
        int w = W;
        int* q = queueP; int tail = 0; q[tail++] = p; mk[p] = st; int minC = p;
        for (int i = 0; i < tail; i++)
        {
            int c = q[i];
            int y = c - w; if (mk[y] != st && blk[y] == 0) { mk[y] = st; q[tail++] = y; if (y < minC) minC = y; }
            y = c + w; if (mk[y] != st && blk[y] == 0) { mk[y] = st; q[tail++] = y; }
            y = c - 1; if (mk[y] != st && blk[y] == 0) { mk[y] = st; q[tail++] = y; if (y < minC) minC = y; }
            y = c + 1; if (mk[y] != st && blk[y] == 0) { mk[y] = st; q[tail++] = y; }
        }
        return minC;
    }

    bool Blocked(int c) => wall[c] || occ[c];

    bool Frozen2x2(int t)
    {
        int w = W;
        Span<int> tls = [t, t - 1, t - w, t - w - 1];
        foreach (int tl in tls)
        {
            int c0 = tl, c1 = tl + 1, c2 = tl + w, c3 = tl + w + 1;
            if (Blocked(c0) && Blocked(c1) && Blocked(c2) && Blocked(c3))
                if ((occ[c0] && !goal[c0]) || (occ[c1] && !goal[c1]) || (occ[c2] && !goal[c2]) || (occ[c3] && !goal[c3])) return true;
        }
        return false;
    }

    bool FreezeDeadlock(int t) { bool off = false; return Frozen(t, ref off) && off; }

    bool Frozen(int c, ref bool offGoal)
    {
        fz[c] = true;
        bool res = false;
        if (BlockedAxis(c, 1, ref offGoal) && BlockedAxis(c, W, ref offGoal))
        {
            if (!goal[c]) offGoal = true;
            res = true;
        }
        fz[c] = false;
        return res;
    }

    bool BlockedAxis(int c, int d, ref bool offGoal)
    {
        int a = c - d, b = c + d;
        if (wall[a] || wall[b] || fz[a] || fz[b]) return true;
        if (dist[a] < 0 && dist[b] < 0) return true;
        return (occ[a] && Frozen(a, ref offGoal)) || (occ[b] && Frozen(b, ref offGoal));
    }

    int Normalize(int b, int t, int minR, uint st)
    {
        foreach (int d in dirs)
        {
            int x = b + d;
            if (x != t && !wall[x] && !occ[x] && mark[x] != st) return Reach(b, mark2);
        }
        if (mark[t] == st && (t == minR || !RingConnected(t))) return Reach(b, mark2);
        return Math.Min(minR, b);
    }

    bool RingConnected(int c)
    {
        int w = W;
        Span<int> ring = [c - w, c - w + 1, c + 1, c + w + 1, c + w, c + w - 1, c - 1, c - w - 1];
        int start = -1;
        for (int i = 0; i < 8; i++) if (wall[ring[i]] || occ[ring[i]]) { start = i; break; }
        if (start < 0) return true;
        int runs = 0; bool inRun = false, runHasOrth = false;
        for (int k = 1; k <= 8; k++)
        {
            int i = (start + k) % 8;
            int x = ring[i];
            if (!wall[x] && !occ[x]) { inRun = true; runHasOrth = runHasOrth || i % 2 == 0; continue; }
            if (inRun && runHasOrth) runs++;
            inRun = false; runHasOrth = false;
        }
        return runs <= 1;
    }

    void ComputePackingOrder()
    {
        round = new int[cells]; entrance = new bool[cells];
        var filled = (bool[])goal.Clone();
        var left = new List<int>();
        for (int c = 0; c < cells; c++)
        {
            if (goal[c]) { left.Add(c); continue; }
            round[c] = -1;
            if (!wall[c]) foreach (int d in dirs) if (goal[c + d]) entrance[c] = true;
        }
        var seen = new int[cells]; int stampL = 0;
        for (int r = 0; left.Count > 0; r++)
        {
            var outL = new List<int>(); var keep = new List<int>();
            foreach (int g in left)
            {
                stampL++;
                if (Extractable(g, filled, seen, stampL)) outL.Add(g); else keep.Add(g);
            }
            if (outL.Count == 0) { outL = keep; keep = new List<int>(); }
            foreach (int g in outL) { filled[g] = false; round[g] = r; }
            left = keep;
        }
    }

    bool Extractable(int g, bool[] filled, int[] seen, int stampL)
    {
        bool Free(int c) => !wall[c] && (!filled[c] || c == g);
        var q = new List<int> { g };
        seen[g] = stampL;
        for (int i = 0; i < q.Count; i++)
        {
            int c = q[i];
            foreach (int d in dirs)
            {
                int nc = c + d;
                if (seen[nc] == stampL || !Free(nc) || !Free(nc + d)) continue;
                if (!goal[nc]) return true;
                seen[nc] = stampL;
                q.Add(nc);
            }
        }
        return false;
    }

    // ---- heuristics --------------------------------------------------------------------------------------------
    void PerturbInit(int seed)
    {
        if (seed <= 0) return;
        noise = new int[cells];
        ulong x = (ulong)seed * 0x9E3779B97F4A7C15UL + 0x2545F4914F6CDD1DUL;
        for (int c = 0; c < cells; c++)
        {
            x ^= x << 13; x ^= x >> 7; x ^= x << 17;
            noise[c] = (int)(x % (ulong)(4 * seed));
        }
    }

    int SetCosts(ReadOnlySpan<int> cur)
    {
        if (++ccStamp == 0) { Array.Clear(ccMark); ccStamp = 1; }
        freeG.Clear();
        for (int g = 0; g < goals.Length; g++) if (!occ[goals[g]]) freeG.Add(g);
        int h = 0;
        foreach (int b in cur) h += Hc(b);
        return h;
    }

    int Hc(int c)
    {
        int b = HcBase(c);
        if (noise != null && !goal[c]) return b * 4 + noise[c];
        return b;
    }

    int HcBase(int c)
    {
        if (!opt.FreeGoal || goal[c]) return dist[c];
        if (ccMark[c] == ccStamp) return cc[c];
        int best = NoGoalCost;
        foreach (int g in freeG)
        {
            int d = goalDist[g * cells + c];
            if (d >= 0 && d < best) best = d;
        }
        ccMark[c] = ccStamp; cc[c] = best;
        return best;
    }

    // ---- heaps --------------------------------------------------------------------------------------------------
    static ulong MkKey(int major, int minor, int id) =>
        (Math.Min((ulong)(uint)major, 0xFFFFUL) << 48) | (Math.Min((ulong)(uint)minor, 0xFFFFUL) << 32) | (uint)id;

    void PushNode(int h, int w, int id, int bucket)
    {
        if (buckets == null) buckets = new KeyHeap?[(n + 1) * MaxRegions];
        heap.Push(MkKey(h, w, id));
        (buckets[bucket] ??= new KeyHeap()).Push(MkKey(w, h, id));
    }

    void PushTo(int src, int h, int w, int id)
    {
        if (src < 0) heap.Push(MkKey(h, w, id));
        else buckets![src]!.Push(MkKey(w, h, id));
    }

    bool Exhausted(int id)
    {
        int m = nodes.MvIdx(id);
        return m >= 0 && opened[m].Left == 0;
    }

    bool PopNode(out int id, out int src)
    {
        turn++;
        if (turn % 2 == 0 && buckets != null)
        {
            for (int c = 0; c < buckets.Length; c++)
            {
                int s = rr;
                var b = buckets[s];
                rr = (rr + 1) % buckets.Length;
                if (b == null) continue;
                while (b.Count > 0)
                {
                    int cand = (int)(uint)b.Pop();
                    if (!Exhausted(cand)) { id = cand; src = s; return true; }
                }
            }
        }
        while (heap.Count > 0)
        {
            int cand = (int)(uint)heap.Pop();
            if (!Exhausted(cand)) { id = cand; src = -1; return true; }
        }
        id = 0; src = 0; return false;
    }

    public ulong TrackedBytes() =>
        (ulong)(nodes.Bytes + mv.Bytes + opened.Bytes + heap.Bytes + BucketBytes() + dpair.Length + goalDist.Length * 4L);
    long BucketBytes() { long t = 0; if (buckets != null) foreach (var b in buckets) if (b != null) t += b.Bytes; return t; }
}
