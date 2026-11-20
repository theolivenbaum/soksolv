using System.Diagnostics;
using System.Numerics;
using System.Runtime.CompilerServices;
using System.Text;
using Soksolv.Model;

namespace Soksolv.Search.Reverse;

/// <summary>Reverse-lane configuration (port of Go reverse.Lane for the default portfolio configuration).</summary>
public sealed class RevOptions
{
    public int Phase;             // perturbation phase (restarts after a memory fill)
    public bool Cells = true;     // FESS-style cell scheduling
    public bool Novelty, Types, Walks;
    public bool Connected;        // best state: lowest h among states whose player reaches every free cell (walk start)
    public int Supply = 4;        // supply-aware ordering weight (0 = off); Go default 4
    public bool SupplyInherit, NoMacros;
    public long NodeBudget;       // > 0: stop after this many expansions, ignore the clock (deterministic screening)
    public ulong BudgetBytes;     // 0 = unlimited
    public ulong MemLimitBytes;   // process backstop (0 = off)
    public bool CloseHoles = true;
    public double CellsAfterSec = 2.0;
    public long CellsAfterExp = 40000;
    public int TypesEvery = 4;
    public long WalkAfter = 5000; public int WalkCount = 16, WalkLen = 50;
    public volatile bool Stop;
    public CancelFlag? Cancel;
}

/// <summary>
/// Backward search (port of Go package reverse): from the solved position pull boxes until they stand on their
/// start squares with the player able to reach its start square. Pulls reversed are the forward pushes.
/// </summary>
public sealed unsafe partial class ReverseSolver : IDisposable
{
    const string DirChars = "udlr";
    const int SupMark = 1 << 30;
    const int UnreachableCost = 8 * 4096;

    readonly Level lv;
    readonly RevOptions opt;
    readonly int n, cells, W;
    readonly int[] dirs;
    readonly bool[] wall;
    int[] dist = [];            // pushes needed to bring a box from some start square to the cell; -1 unreachable
    readonly bool[] start;      // start squares
    int[] startIdx = [];
    bool[] canReach = [];       // canReach[j*cells+c]
    int[] key8 = [];
    readonly RevStore store;
    readonly int[] fidx, fcell;
    readonly int floorN;

    // scratch
    readonly bool[] occ;
    readonly byte[] blkArr; readonly byte* blk;
    readonly uint[] mark, mark2; readonly uint* markP, mark2P;
    readonly int[] queue; readonly int* queueP;
    uint stamp;
    int lastRegionSize;
    readonly ulong* key, pkey;
    int[] matchOf; uint[] gseen; uint gstamp;
    long pruned;

    // queues
    readonly BucketQueue q = new(), nq = new();
    CellQueue? cellq;
    TypeQueue? types;
    ulong[] expandedBits = [];
    int typeTurn, novTurn;
    ulong[][] novSeen = [];
    long expandedTotal, lastImpExp, walkNext;
    ulong wrng;
    bool memFull, popMarked;
    int provU;
    byte[] supU = [];
    Supply? sup;
    readonly Stopwatch clock = Stopwatch.StartNew();
    public SolveStats Stats = new();

    public ReverseSolver(Level level, RevOptions options)
    {
        lv = level; opt = options; n = lv.Boxes.Length; cells = lv.Cells; W = lv.W; wall = lv.Wall;
        dirs = [-W, W, -1, 1];
        occ = new bool[cells];
        blkArr = GC.AllocateArray<byte>(cells, pinned: true);
        mark = GC.AllocateArray<uint>(cells, pinned: true); mark2 = GC.AllocateArray<uint>(cells, pinned: true);
        queue = GC.AllocateArray<int>(cells, pinned: true);
        blk = (byte*)Unsafe.AsPointer(ref blkArr[0]); markP = (uint*)Unsafe.AsPointer(ref mark[0]);
        mark2P = (uint*)Unsafe.AsPointer(ref mark2[0]); queueP = (int*)Unsafe.AsPointer(ref queue[0]);
        for (int i = 0; i < cells; i++) blkArr[i] = (byte)(wall[i] ? 1 : 0);
        start = new bool[cells];
        foreach (int b in lv.Boxes) start[b] = true;

        // dist: forward push BFS from all start squares (a box at c can be pushed to c+d if c-d and c+d are floor)
        dist = new int[cells]; Array.Fill(dist, -1);
        {
            var qq = new List<int>();
            foreach (int b in lv.Boxes) { dist[b] = 0; qq.Add(b); }
            for (int i = 0; i < qq.Count; i++)
            {
                int c = qq[i];
                foreach (int d in dirs)
                {
                    int t = c + d;
                    if (!wall[c - d] && !wall[t] && dist[t] < 0) { dist[t] = dist[c] + 1; qq.Add(t); }
                }
            }
        }
        // box bitsets index only cells a box can occupy in some reverse state (dist >= 0)
        fidx = new int[cells];
        var fc = new List<int>();
        for (int c = 0; c < cells; c++) { fidx[c] = -1; if (dist[c] >= 0) { fidx[c] = fc.Count; fc.Add(c); } }
        fcell = [.. fc];
        foreach (bool w in wall) if (!w) floorN++;
        store = new RevStore(cells, fidx, fcell);
        key = (ulong*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(Math.Max(1, store.Nw) * 8));
        pkey = (ulong*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(Math.Max(1, store.Nw) * 8));

        canReach = new bool[n * cells];
        startIdx = new int[cells]; Array.Fill(startIdx, -1);
        for (int j = 0; j < n; j++)
        {
            int b = lv.Boxes[j];
            startIdx[b] = j;
            var r = canReach.AsSpan(j * cells, cells);
            r[b] = true;
            var qq = new List<int> { b };
            for (int i = 0; i < qq.Count; i++)
            {
                int c = qq[i];
                foreach (int d in dirs)
                {
                    int t = c + d;
                    if (!wall[c - d] && !wall[t] && !r[t]) { r[t] = true; qq.Add(t); }
                }
            }
        }
        matchOf = new int[n]; gseen = new uint[n];
        Perturb(opt.Phase);
        if (opt.Supply > 0 && n <= 256) sup = new Supply(this, opt.Supply);
        if (opt.Cells) cellq = new CellQueue();
        if (opt.Types) types = new TypeQueue();
    }

    public void Dispose()
    {
        store.Dispose(); q.Dispose(); nq.Dispose(); cellq?.Dispose();
        System.Runtime.InteropServices.NativeMemory.Free(key); System.Runtime.InteropServices.NativeMemory.Free(pkey);
    }

    void SetOcc(int c, bool v) { occ[c] = v; blk[c] = (byte)(v ? 1 : 0); }

    /// <summary>Ordering key per cell: 8 x pull distance, plus for phase p > 0 a pseudo-random offset in [0, 8p) off the start squares.</summary>
    void Perturb(int phase)
    {
        key8 = new int[dist.Length];
        ulong x = (ulong)phase * 0x9E3779B97F4A7C15UL + 0x2545F4914F6CDD1DUL;
        for (int c = 0; c < dist.Length; c++)
        {
            int d = dist[c];
            if (d < 0) { key8[c] = -1; continue; }
            key8[c] = d * 8;
            if (phase > 0 && d > 0)
            {
                x ^= x << 13; x ^= x >> 7; x ^= x << 17;
                key8[c] += (int)(x % (ulong)(8 * phase));
            }
        }
    }

    // ---- flood / normalization -------------------------------------------------------------------------------
    /// <summary>Marks the player's region from p (per occ) in mk with a fresh stamp; returns its minimum cell.</summary>
    int Reach(int p, uint* mk)
    {
        if (stamp > uint.MaxValue - 4) { Array.Clear(mark); Array.Clear(mark2); stamp = 0; }
        uint st = ++stamp;
        int w = W;
        int* qp = queueP; int tail = 0; qp[tail++] = p; mk[p] = st; int m = p;
        for (int i = 0; i < tail; i++)
        {
            int x = qp[i];
            int y = x - w; if (mk[y] != st && blk[y] == 0) { mk[y] = st; qp[tail++] = y; if (y < m) m = y; }
            y = x + w; if (mk[y] != st && blk[y] == 0) { mk[y] = st; qp[tail++] = y; }
            y = x - 1; if (mk[y] != st && blk[y] == 0) { mk[y] = st; qp[tail++] = y; if (y < m) m = y; }
            y = x + 1; if (mk[y] != st && blk[y] == 0) { mk[y] = st; qp[tail++] = y; }
        }
        lastRegionSize = tail;
        return m;
    }

    /// <summary>Child's normalized player after pulling a box from b to p (occ holds the child; parent region stamped st in mark, minimum minR).</summary>
    int Normalize(int b, int p, int minR, uint st)
    {
        foreach (int d in dirs)
        {
            int x = b + d;
            if (x != p && !wall[x] && !occ[x] && mark[x] != st) return Reach(p + (p - b), mark2P);
        }
        if (p == minR || !RingConnected(p)) return Reach(p + (p - b), mark2P);
        return Math.Min(minR, b);
    }

    bool RingConnected(int c)
    {
        int w = W;
        Span<int> ring = [c - w, c - w + 1, c + 1, c + w + 1, c + w, c + w - 1, c - 1, c - w - 1];
        int startI = -1;
        for (int i = 0; i < 8; i++) if (wall[ring[i]] || occ[ring[i]]) { startI = i; break; }
        if (startI < 0) return true;
        int runs = 0; bool inRun = false, runHasOrth = false;
        for (int k = 1; k <= 8; k++)
        {
            int i = (startI + k) % 8; int x = ring[i];
            if (!wall[x] && !occ[x]) { inRun = true; runHasOrth = runHasOrth || i % 2 == 0; continue; }
            if (inRun && runHasOrth) runs++;
            inRun = false; runHasOrth = false;
        }
        return runs <= 1;
    }

    // ---- matching / inheritance ----------------------------------------------------------------------------------
    bool Feasible(ReadOnlySpan<int> boxes)
    {
        Array.Fill(matchOf, -1);
        for (int i = 0; i < boxes.Length; i++)
        {
            if (++gstamp == 0) { Array.Clear(gseen); gstamp = 1; }
            if (!TryMatch(i, boxes)) return false;
        }
        return true;
    }
    bool TryMatch(int i, ReadOnlySpan<int> boxes)
    {
        int b = boxes[i];
        for (int j = 0; j < n; j++)
        {
            if (gseen[j] == gstamp || !canReach[j * cells + b]) continue;
            gseen[j] = gstamp;
            if (matchOf[j] < 0 || TryMatch(matchOf[j], boxes)) { matchOf[j] = i; return true; }
        }
        return false;
    }

    /// <summary>The pull that produced node id: box moved b -> t.</summary>
    void Pulled(int id, out int b, out int t)
    {
        store.Pulled(id, out int f, out int dir);
        b = fcell[f]; t = b + dirs[dir];
    }

    /// <summary>Node id is certainly matchable because its parent was: every start square reachable from b is reachable from t.</summary>
    bool Inherits(int id)
    {
        if (store.ParentOf(id) < 0) return false;
        Pulled(id, out int b, out int t);
        for (int j = 0; j < n; j++)
            if (canReach[j * cells + b] && !canReach[j * cells + t]) return false;
        return true;
    }

    // ---- node insertion -----------------------------------------------------------------------------------------
    void BuildKey(ReadOnlySpan<int> boxes, ulong* k)
    {
        for (int w = 0; w < store.Nw; w++) k[w] = 0;
        foreach (int b in boxes) { int f = fidx[b]; k[f >> 6] |= 1UL << (f & 63); }
    }

    int Add(ReadOnlySpan<int> boxes, int player, int parent, int from, int dir, int h)
    {
        BuildKey(boxes, key);
        return AddKeyed(boxes, player, parent, from, dir, h, store.HashOfBoxes(boxes, player));
    }

    /// <summary>Adds the state whose box bitset is in key. Returns the node id (the existing id for a duplicate; -1 if memory is full).</summary>
    int AddKeyed(ReadOnlySpan<int> boxes, int player, int parent, int from, int dir, int h, ulong zh)
    {
        int id = store.Find(key, player, zh, out int slot, out uint fp);
        if (id >= 0 || memFull) return id;
        id = store.Count;
        store.Append(parent, player, key, parent >= 0 ? fidx[from] : 0, dir, slot, fp);
        q.Push(h, id);
        int k = 0;
        if (opt.Types || opt.Novelty || cellq != null)
            foreach (int b in boxes) if (start[b]) k++;
        if (opt.Types) types!.Push((ulong)h << 16 | (uint)k, id, h);
        if (opt.Novelty)
        {
            // best-first width search's novelty: a state with a box on a square no box has stood on before, among states
            // with as many boxes back home, also enters the novelty queue
            while (novSeen.Length <= k) Array.Resize(ref novSeen, novSeen.Length + 1);
            novSeen[k] ??= new ulong[(cells + 63) / 64];
            var seen = novSeen[k]; bool novel = false;
            foreach (int b in boxes)
            {
                int w = b / 64; ulong bit = 1UL << (b % 64);
                if ((seen[w] & bit) == 0) { seen[w] |= bit; novel = true; }
            }
            if (novel) nq.Push(h, id);
        }
        if (provU > 0)
        {
            SetSupU(id, provU + 127);
            k = Math.Max(0, k - provU);
        }
        cellq?.Push(k, h, id);
        if (store.NeedsGrow)
        {
            ulong needed = TrackedBytes() + (ulong)store.TableSlots * 16;
            ulong b = opt.BudgetBytes;
            if (b > 0 && needed > b) memFull = store.OverSevenEighths;
            else store.Grow();
        }
        return id;
    }

    public ulong TrackedBytes() => (ulong)(store.Bytes + q.Bytes + nq.Bytes + (cellq?.Bytes ?? 0) + expandedBits.Length * 8L + (types?.Bytes ?? 0) + supU.Length + (sup?.Bytes() ?? 0));

    // ---- queues ---------------------------------------------------------------------------------------------------
    /// <summary>Greedy (lowest h) until CellsAfter, then 1/2 greedy, 1/4 round-robin cell, 1/4 best cell; entries for already expanded nodes skipped.</summary>
    bool PopNext(out int id, out int h)
    {
        while (true)
        {
            id = 0; h = 0;
            bool fromCells = false;
            if (cellq != null && ((opt.NodeBudget == 0 && clock.Elapsed.TotalSeconds > opt.CellsAfterSec) || (opt.NodeBudget > 0 && expandedTotal > opt.CellsAfterExp)))
            {
                cellq.Turn++;
                switch (cellq.Turn % 4)
                {
                    case 1: (id, h) = cellq.PopCell(false); fromCells = id >= 0; break;
                    case 3: (id, h) = cellq.PopCell(true); fromCells = id >= 0; break;
                }
            }
            if (!fromCells && types != null)
            {
                typeTurn++;
                if (typeTurn % opt.TypesEvery == 0 && types.Pop(out int tid, out int th)) { id = tid; h = th; fromCells = true; }
            }
            if (!fromCells && opt.Novelty && nq.N > 0)
            {
                novTurn++;
                if (novTurn % 3 == 0) { (id, h) = nq.Pop(); fromCells = true; }
            }
            if (!fromCells)
            {
                if (q.N == 0) { id = -1; return false; }
                (id, h) = q.Pop();
            }
            popMarked = (id & SupMark) != 0;
            id &= ~SupMark;
            if (cellq == null && !opt.Novelty && types == null) return true;
            int w = id >> 6; ulong bit = 1UL << (id & 63);
            if (w >= expandedBits.Length) Array.Resize(ref expandedBits, Math.Max(w + 1, expandedBits.Length * 2));
            if ((expandedBits[w] & bit) != 0) continue; // already expanded through the other queue
            expandedBits[w] |= bit;
            return true;
        }
    }

    void Unexpand(int id) { int w = id >> 6; if (w < expandedBits.Length) expandedBits[w] &= ~(1UL << (id & 63)); }

    void SetSupU(int id, int u)
    {
        int cnt = store.Count;
        if (supU.Length < cnt) Array.Resize(ref supU, Math.Max(cnt, supU.Length * 2));
        supU[id] = (byte)(u + 1);
    }

    // ---- main loop ------------------------------------------------------------------------------------------------
    public SolveResult Run(double timeLimitSec)
    {
        var sw = Stopwatch.StartNew();
        var res = Search(timeLimitSec);
        res.TimeMs = sw.Elapsed.TotalMilliseconds;
        Prof.Report(res.TimeMs);
        if (Prof.On && sup != null) Console.Error.WriteLine($"supply: evals {sup.Evals} deferred {sup.Deferred} cache-misses {sup.Misses} graphs {sup.Graphs} macros {sup.Macros} ok {sup.MacroOK}; stored {store.Count}");
        res.PeakBytes = (long)TrackedBytes();
        Stats.Stored = store.Count;
        res.Stats = Stats;
        return res;
    }

    SolveResult Search(double timeLimitSec)
    {
        long expanded = 0;
        var goals = new List<int>();
        for (int c = 0; c < cells; c++) if (lv.Goal[c]) goals.Add(c);
        int h0 = 0;
        foreach (int g in goals)
        {
            if (dist[g] < 0) return new SolveResult { Reason = "goal unreachable from start squares" };
            h0 += key8[g];
        }
        var goalArr = goals.ToArray();
        foreach (int g in goalArr) SetOcc(g, true);
        // Roots: boxes on goals, one per player region.
        uint seenRegion = ++stamp;
        var regionMark = new uint[cells];
        for (int c = 0; c < cells; c++)
        {
            if (wall[c] || occ[c] || regionMark[c] == seenRegion) continue;
            int p = Reach(c, markP);
            for (int i = 0; i < lastRegionSize; i++) regionMark[queue[i]] = seenRegion;
            Add(goalArr, p, -1, -1, -1, h0);
        }
        foreach (int g in goalArr) SetOcc(g, false);

        var cur = new int[n]; var child = new int[n];
        int bestId = -1, bestH = 0;
        double limitMs = timeLimitSec * 1000;
        long lastMem = 0;
        while (q.N > 0)
        {
            if (memFull) return new SolveResult { Reason = "memory", Nodes = expanded };
            if (opt.Walks && bestId >= 0 && expandedTotal - lastImpExp > opt.WalkAfter && expandedTotal >= walkNext)
            {
                walkNext = expandedTotal + opt.WalkAfter;
                long tw = Prof.Start();
                RandomWalks(bestId);
                Prof.Stop(22, tw);
            }
            long tp = Prof.Start();
            bool got = PopNext(out int id, out int h);
            Prof.Stop(19, tp);
            if (!got) break;
            bool supEval = false;
            int prov = 0, curU = 0;
            if (sup != null)
            {
                int u = id < supU.Length ? supU[id] : 0;
                if (popMarked) { curU = u - 1; if (h < BucketQueue.MaxBuckets - 2) h -= curU * sup.W * 8; else h = BucketQueue.MaxBuckets - 1; }
                else if (u >= 128) { prov = u - 128; supEval = true; if (h < BucketQueue.MaxBuckets - 2) h -= prov * sup.W * 8; else h = BucketQueue.MaxBuckets - 1; }
                else if (u > 1) { Unexpand(id); continue; }
                else if (u == 0) supEval = true;
            }
            long tu = Prof.Start();
            store.Unpack(id, cur);
            foreach (int b in cur) SetOcc(b, true);
            if (h == BucketQueue.MaxBuckets - 1)
            {
                h = 0;
                foreach (int b in cur) h += key8[b];
            }
            Reach(store.PlayerOf(id), markP);
            uint st = stamp;
            Prof.Stop(23, tu);
            if (h == 0 && mark[lv.Player] == st)
            {
                foreach (int b in cur) SetOcc(b, false);
                return new SolveResult { Solved = true, Solution = Reconstruct(id), Nodes = expanded };
            }
            long tf = Prof.Start();
            bool infeasible = h > 0 && !Inherits(id) && !Feasible(cur);
            Prof.Stop(17, tf);
            if (infeasible)
            {
                pruned++;
                foreach (int b in cur) SetOcc(b, false);
                continue;
            }
            bool supZero = false;
            if (supEval && h > 0 && h < BucketQueue.MaxBuckets - 1)
            {
                long ts = Prof.Start();
                var (u, k) = SupplyU(cur, store.PlayerOf(id));
                Prof.Stop(16, ts);
                u = Math.Min(u, 126);
                SetSupU(id, u);
                curU = u;
                if (u > prov)
                {
                    sup!.Deferred++;
                    int nh = Math.Min(h + u * sup.W * 8, BucketQueue.MaxBuckets - 2);
                    q.Push(nh, id | SupMark);
                    cellq?.Push(Math.Max(0, k - u), nh, id | SupMark);
                    Unexpand(id);
                    foreach (int b in cur) SetOcc(b, false);
                    continue;
                }
                supZero = u == 0 && k < n;
            }
            if (sup == null || !opt.SupplyInherit) curU = 0;
            expanded++;
            if ((bestId < 0 || h < bestH) && (!opt.Connected || lastRegionSize == floorN - n))
            {
                bestId = id; bestH = h; lastImpExp = expandedTotal;
            }
            expandedTotal++;
            if ((opt.NodeBudget > 0 && expandedTotal >= opt.NodeBudget))
                return new SolveResult { Reason = "timeout", Nodes = expanded };
            if ((expanded & 255) == 0)
            {
                long now = clock.ElapsedMilliseconds;
                if ((limitMs > 0 && now > limitMs && opt.NodeBudget == 0) || opt.Stop || (opt.Cancel?.Stopped ?? false)) return new SolveResult { Reason = "timeout", Nodes = expanded };
                if (now - lastMem > 50)
                {
                    lastMem = now;
                    ulong tb = TrackedBytes();
                    if (opt.BudgetBytes > 0 && tb > opt.BudgetBytes) return new SolveResult { Reason = "memory", Nodes = expanded };
                    if (opt.MemLimitBytes > 0 && (ulong)Environment.WorkingSet > opt.MemLimitBytes / 100 * 76) return new SolveResult { Reason = "memory", Nodes = expanded };
                }
            }
            // Pull: player on b+d (reachable) steps to b+2d (free), box b -> b+d. A child's key is the parent's with one bit moved.
            long tl = Prof.Start();
            BuildKey(cur, pkey);
            ulong parentHash = store.HashOfBoxes(cur, store.PlayerOf(id));
            int pplayer = store.PlayerOf(id);
            for (int i = 0; i < cur.Length; i++)
            {
                int b = cur[i];
                for (int di = 0; di < 4; di++)
                {
                    int d = dirs[di];
                    int p = b + d, p2 = b + 2 * d;
                    if (mark[p] != st || wall[p2] || occ[p2] || dist[p] < 0) continue;
                    SetOcc(b, false); SetOcc(p, true);
                    int np = Normalize(b, p, pplayer, st);
                    // child = cur with box i moved to p, kept ascending
                    cur.CopyTo(child, 0);
                    int j = i;
                    while (j + 1 < child.Length && child[j + 1] < p) { child[j] = child[j + 1]; j++; }
                    while (j > 0 && child[j - 1] > p) { child[j] = child[j - 1]; j--; }
                    child[j] = p;
                    for (int w = 0; w < store.Nw; w++) key[w] = pkey[w];
                    int fb = fidx[b], fp = fidx[p];
                    key[fb >> 6] &= ~(1UL << (fb & 63));
                    key[fp >> 6] |= 1UL << (fp & 63);
                    int ch = h - key8[b] + key8[p];
                    if (curU > 1 && startIdx[b] < 0)
                    {
                        provU = curU - 1;
                        ch += provU * sup!.W * 8;
                    }
                    ulong zh = parentHash ^ store.ZFloor(fb) ^ store.ZFloor(fp) ^ store.ZPlayer(pplayer) ^ store.ZPlayer(np);
                    AddKeyed(child, np, id, b, di, ch, zh);
                    provU = 0;
                    SetOcc(b, true); SetOcc(p, false);
                }
            }
            Prof.Stop(18, tl);
            long tm = Prof.Start();
            if (supZero && !opt.NoMacros) PlacementMacros(id, cur, child);
            Prof.Stop(21, tm);
            foreach (int b in cur) SetOcc(b, false);
        }
        return new SolveResult { Reason = "exhausted", Nodes = expanded };
    }

    // ---- reconstruction -------------------------------------------------------------------------------------------
    /// <summary>The pulls on the chain from node id to its root, reversed, are the forward pushes from the start position.</summary>
    string Reconstruct(int id)
    {
        Span<int> opposite = [1, 0, 3, 2];
        var pushes = new List<(int from, int dir)>();
        for (; store.ParentOf(id) >= 0; id = store.ParentOf(id))
        {
            Pulled(id, out int b, out int t);
            int d = 0;
            for (int k = 0; k < 4; k++) if (b + dirs[k] == t) d = k;
            pushes.Add((t, opposite[d]));
        }
        for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
        foreach (int b in lv.Boxes) SetOcc(b, true);
        var prev = new sbyte[cells];
        var sb = new StringBuilder();
        int p = lv.Player;
        var walk = new List<char>();
        foreach (var (from, dir) in pushes)
        {
            int target = from - dirs[dir];
            uint st = ++stamp;
            int tail = 0; queue[tail++] = p; mark[p] = st;
            for (int i = 0; i < tail && mark[target] != st; i++)
            {
                int c = queue[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + dirs[k];
                    if (mark[nb] != st && !wall[nb] && !occ[nb]) { mark[nb] = st; prev[nb] = (sbyte)k; queue[tail++] = nb; }
                }
            }
            walk.Clear();
            for (int c = target; c != p; c -= dirs[prev[c]]) walk.Add(DirChars[prev[c]]);
            for (int i = walk.Count - 1; i >= 0; i--) sb.Append(walk[i]);
            sb.Append((char)(DirChars[dir] - 'a' + 'A'));
            SetOcc(from, false); SetOcc(from + dirs[dir], true);
            p = from;
        }
        for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
        return sb.ToString();
    }

    // ---- random walks ---------------------------------------------------------------------------------------------------
    ulong WalkRand()
    {
        if (wrng == 0) wrng = 0x2545F4914F6CDD1DUL;
        wrng ^= wrng << 13; wrng ^= wrng >> 7; wrng ^= wrng << 17;
        return wrng;
    }

    /// <summary>Random-walk escape from a plateau (Arvand): WalkCount random pull walks of WalkLen steps from the best state.</summary>
    void RandomWalks(int from)
    {
        var cur = new int[n]; var child = new int[n];
        var opts = new List<(int i, int di)>();
        for (int w = 0; w < opt.WalkCount; w++)
        {
            int id = from;
            for (int step = 0; step < opt.WalkLen; step++)
            {
                store.Unpack(id, cur);
                foreach (int b in cur) SetOcc(b, true);
                Reach(store.PlayerOf(id), markP);
                uint st = stamp;
                opts.Clear();
                for (int i = 0; i < cur.Length; i++)
                {
                    int b = cur[i];
                    for (int di = 0; di < 4; di++)
                    {
                        int d = dirs[di], p = b + d, p2 = b + 2 * d;
                        if (mark[p] == st && !wall[p2] && !occ[p2] && dist[p] >= 0) opts.Add((i, di));
                    }
                }
                if (opts.Count == 0) { foreach (int b in cur) SetOcc(b, false); break; }
                var o = opts[(int)(WalkRand() % (ulong)opts.Count)];
                int bb = cur[o.i];
                int pp = bb + dirs[o.di];
                SetOcc(bb, false); SetOcc(pp, true);
                int np = Normalize(bb, pp, store.PlayerOf(id), st);
                cur.CopyTo(child, 0);
                child[o.i] = pp;
                Array.Sort(child);
                int ch = 0;
                foreach (int x in child) ch += key8[x];
                int nid = Add(child, np, id, bb, o.di, ch);
                foreach (int x in child) SetOcc(x, false);
                expandedTotal++;
                if (nid < 0) break;
                id = nid;
            }
        }
    }
}
