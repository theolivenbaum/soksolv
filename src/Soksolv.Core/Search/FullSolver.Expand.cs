using System.Numerics;
namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- features ---------------------------------------------------------------------------------------------------
    int PackedCount()
    {
        int gw = packWords;
        int maxR = -1;
        for (int r = nRounds - 1; r >= 0 && maxR < 0; r--)
            for (int w = 0; w < gw; w++)
                if ((rdMask[r * gw + w] & ~gOcc[w]) != 0) { maxR = r; break; }
        int cnt = 0, k = maxR < 0 ? 0 : maxR;
        for (int w = 0; w < gw; w++) cnt += BitOperations.PopCount(gOcc[w] & geMask[k * gw + w]);
        return cnt;
    }
    int PackedCountSlow()
    {
        int maxR = -1;
        foreach (int g in goals) if (!occ[g]) maxR = Math.Max(maxR, round[g]);
        int c = 0;
        foreach (int g in goals) if (occ[g] && round[g] >= maxR) c++;
        return c;
    }

    /// <summary>Counts the connected components of free cells with word-parallel row floods (boards up to 64 wide).</summary>
    int RegionsBits()
    {
        int rows = frRows;
        ulong* rem = frRem; ulong* reg = frReg; ulong* fr = frR;
        for (int r = 0; r < rows; r++) rem[r] = fr[r];
        int cnt = 0, last = rows - 2;
        for (int r0 = 1; r0 <= last; )
        {
            if (rem[r0] == 0) { r0++; continue; }
            cnt++;
            int cp = BitOperations.TrailingZeroCount(rem[r0]);
            int lo = r0, hi = r0;
            ulong y0 = FillRowB(1UL << cp, rem[r0]);
            reg[r0] = y0;
            ulong dirty = 0;
            if ((y0 & rem[r0 - 1]) != 0) dirty |= 1UL << (r0 - 1);
            if ((y0 & rem[r0 + 1]) != 0) dirty |= 1UL << (r0 + 1);
            while (dirty != 0)
            {
                int r = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
                ulong cur = reg[r];
                ulong y = cur | (reg[r - 1] | reg[r + 1]) & rem[r];
                if (y == cur) continue;
                y = FillRowB(y, rem[r]);
                reg[r] = y;
                ulong nw = y & ~cur;
                if (r < lo) lo = r; if (r > hi) hi = r;
                if ((nw & rem[r - 1] & ~reg[r - 1]) != 0) dirty |= 1UL << (r - 1);
                if ((nw & rem[r + 1] & ~reg[r + 1]) != 0) dirty |= 1UL << (r + 1);
            }
            for (int r = lo; r <= hi; r++) { rem[r] &= ~reg[r]; reg[r] = 0; }
        }
        return cnt;
    }
    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    static ulong FillRowB(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }
    /// <summary>Go regions() under target pockets (exp-460): components made only of pocket cells do not count.</summary>
    int RegionsRel()
    {
        if (tgtPocket == null) return Regions();
        FloorCells();
        uint rs = ++rstamp;
        int cnt = 0, w = W;
        int* q = cqueueP;
        var pk = tgtPocket;
        foreach (int c in floorCells)
        {
            if (blk[c] != 0 || rmarkP[c] == rs) continue;
            rmarkP[c] = rs;
            int tail = 0; q[tail++] = c;
            bool rel = !pk[c];
            for (int i = 0; i < tail; i++)
            {
                int x = q[i];
                int y = x - w; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; rel = rel || !pk[y]; }
                y = x + w; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; rel = rel || !pk[y]; }
                y = x - 1; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; rel = rel || !pk[y]; }
                y = x + 1; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; rel = rel || !pk[y]; }
            }
            if (rel) cnt++;
        }
        return cnt;
    }

    int Regions()
    {
        if (fbits) return RegionsBits();
        if (floorCells.Length == 0)
        {
            var l = new List<int>();
            for (int c = 0; c < cells; c++) if (!wall[c]) l.Add(c);
            floorCells = [.. l];
        }
        uint rs = ++rstamp;
        int cnt = 0, w = W;
        int* q = cqueueP;
        foreach (int c in floorCells)
        {
            if (blk[c] != 0 || rmarkP[c] == rs) continue;
            cnt++;
            rmarkP[c] = rs;
            int tail = 0; q[tail++] = c;
            for (int i = 0; i < tail; i++)
            {
                int x = q[i];
                int y = x - w; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; }
                y = x + w; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; }
                y = x - 1; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; }
                y = x + 1; if (rmarkP[y] != rs && blk[y] == 0) { rmarkP[y] = rs; q[tail++] = y; }
            }
        }
        return cnt;
    }

    // ---- incremental child region counts (port of Go artic.go) -----------------------------------------------------
    // For the board with box b removed, one iterative Tarjan pass over the free cells gives the number of
    // empty-floor components and, for each free cell t, how many pieces t's component splits into when a box is
    // placed on t. A child moving b to t then has comps - 1 + pieces[t] regions: O(1) per candidate.
    int artComps;
    /// <summary>Test hook: verify every incremental region count against a full flood.</summary>
    public static bool CheckRegions = Environment.GetEnvironmentVariable("SOKSOLV_CHECK") == "1";

    void ComputeArticulation() => MArtic();   // one pointer Tarjan pass (MacroBits.cs), shared by full / macro / fa2

    int ChildRegions(int t) => MChildRegions(t);

    // ---- PI-corral ---------------------------------------------------------------------------------------------------
    uint PiCorral(uint st)
    {
        if (cstamp > 1u << 31) { Array.Clear(cmark); cstamp = 0; }
        uint baseS = cstamp;
        uint best = 0; int bestBoxes = int.MaxValue;
        var q = cqueue;
        for (int c = 0; c < cells; c++)
        {
            if (wall[c] || occ[c] || mark[c] == st || cmark[c] > baseS) continue;
            uint cs = ++cstamp;
            cmark[c] = cs;
            int tail = 0; q[tail++] = c;
            int nbar = 0;
            bool unsolved = false;
            for (int i = 0; i < tail; i++)
            {
                int x = q[i];
                unsolved = unsolved || (goal[x] && !occ[x]);
                foreach (int d in dirs)
                {
                    int nb = x + d;
                    if (wall[nb]) { }
                    else if (cmark[nb] == cs || (mark[nb] == st && !occ[nb])) { }
                    else if (occ[nb])
                    {
                        cmark[nb] = cs; q[tail++] = nb;
                        unsolved = unsolved || !goal[nb];
                        if (TouchesReach(nb, st)) barrier[nbar++] = nb;
                    }
                    else { cmark[nb] = cs; q[tail++] = nb; }
                }
            }
            if (nbar >= bestBoxes) continue;
            bool ok = true;
            for (int bi = 0; bi < nbar; bi++)
            {
                int b = barrier[bi];
                bool pushable = false;
                foreach (int d in dirs)
                {
                    int t = b + d;
                    if (mark[b - d] != st || wall[t] || occ[t] || dist[t] < 0) continue;
                    if (cmark[t] != cs) { ok = false; break; }
                    pushable = true;
                }
                if (!ok || !pushable) { ok = false; break; }
            }
            if (ok && unsolved) { best = cs; bestBoxes = nbar; }
        }
        return best;
    }

    bool TouchesReach(int b, uint st)
    {
        foreach (int d in dirs) if (mark[b + d] == st) return true;
        return false;
    }

    // ---- two-box deadlocks ---------------------------------------------------------------------------------------------
    void PairOffsets()
    {
        var l = new List<int>();
        for (int dy = -2; dy <= 2; dy++) for (int dx = -2; dx <= 2; dx++) if (dx != 0 || dy != 0) l.Add(dy * W + dx);
        poff = [.. l];
        popp = new int[poff.Length];
        for (int k = 0; k < poff.Length; k++) popp[k] = Array.IndexOf(poff, -poff[k]);
        dpair = new sbyte[cells * poff.Length];
    }

    // PairDead scratch: pinned, allocation-free (results identical to Go's pairDead / pairDeadFromBits).
    byte[]? pbA; byte* pb; uint[]? pmarkA, pseenA; uint* pmarkP, pseenP; int[]? pqA; int* pqP;
    uint pst, pst2;
    ulong[] pvis = new ulong[1024]; ushort pgen;
    int[] pqx = new int[512], pqy = new int[512], pqp = new int[512];
    int[] pstarts = [];

    int PReach(int p, out uint st)
    {
        st = ++pst;
        int w = W;
        int* q = pqP; int tail = 0; q[tail++] = p; pmarkP[p] = st; int m = p;
        for (int i = 0; i < tail; i++)
        {
            int c = q[i];
            int y = c - w; if (pmarkP[y] != st && pb[y] == 0) { pmarkP[y] = st; q[tail++] = y; if (y < m) m = y; }
            y = c + w; if (pmarkP[y] != st && pb[y] == 0) { pmarkP[y] = st; q[tail++] = y; }
            y = c - 1; if (pmarkP[y] != st && pb[y] == 0) { pmarkP[y] = st; q[tail++] = y; if (y < m) m = y; }
            y = c + 1; if (pmarkP[y] != st && pb[y] == 0) { pmarkP[y] = st; q[tail++] = y; }
        }
        pqN = tail;
        return m;
    }
    int pqN;

    bool PVisit(int x, int y, int p)
    {
        ulong k = (ushort)x | (ulong)(ushort)y << 16 | (ulong)(ushort)p << 32;
        ulong v = k | (ulong)pgen << 48;
        ulong mask = (ulong)(pvis.Length - 1);
        for (ulong i = (k * 0x9E3779B97F4A7C15UL >> 40) & mask; ; i = (i + 1) & mask)
        {
            ulong e = pvis[i];
            if ((ushort)(e >> 48) != pgen) { pvis[i] = v; return true; }
            if (e == v) return false;
        }
    }

    bool PairDead(int a, int b) => PairDeadFrom(a, b, -1);

    // ---- word-parallel pair search (Go pairbits.go: pairDeadFromBits; boards up to 64 wide) -------------------------------------------
    ulong[]? bbFloorA, bbFreeA, bbRegA, bbRemA;
    ulong* bbFloor, bbFree, bbReg, bbRem; int bbRows;
    public static bool PairBitsOff, PairBitsCheck = Environment.GetEnvironmentVariable("SOKSOLV_PAIR_CHECK") == "1";   // test hooks

    /// <summary>Row flood with a dirty-row worklist (rows ≤ 64): a row is re-examined only when a neighbouring row changed; passes alternate up/down like the sweeps they replace.</summary>
    internal static void FloodRowsDirty(ulong* free, ulong* reg, int rows, int rp, int cp, bool clear = true)
    {
        if (clear) for (int i = 0; i < rows; i++) reg[i] = 0;
        reg[rp] = FillRow(1UL << cp, free[rp]);
        int last = rows - 2;
        ulong valid = ((1UL << last) - 1) << 1;   // bits 1..last
        ulong cur = ((1UL << (rp - 1)) | (1UL << (rp + 1))) & valid;
        bool up = true;
        while (cur != 0)
        {
            ulong nxt = 0, m = cur;
            if (up)
                while (m != 0)
                {
                    int r = BitOperations.TrailingZeroCount(m);
                    m &= m - 1;
                    ulong cr = reg[r], f = free[r];
                    ulong y = cr | (reg[r - 1] | reg[r + 1]) & f;
                    if (y == cr) continue;
                    y = FillRow(y, f);
                    if (y == cr) continue;
                    reg[r] = y;
                    m |= (1UL << (r + 1)) & valid; nxt |= (1UL << (r - 1)) & valid;
                }
            else
                while (m != 0)
                {
                    int r = 63 - BitOperations.LeadingZeroCount(m);
                    m &= ~(1UL << r);
                    ulong cr = reg[r], f = free[r];
                    ulong y = cr | (reg[r - 1] | reg[r + 1]) & f;
                    if (y == cr) continue;
                    y = FillRow(y, f);
                    if (y == cr) continue;
                    reg[r] = y;
                    m |= (1UL << (r - 1)) & valid; nxt |= (1UL << (r + 1)) & valid;
                }
            cur = nxt; up = !up;
        }
    }

    static void FloodRows(ulong* free, ulong* reg, int rows, int rp, int cp)
    {
        if (rows <= 64) { FloodRowsDirty(free, reg, rows, rp, cp); return; }
        for (int i = 0; i < rows; i++) reg[i] = 0;
        if (rows <= 64)
        {
            // dirty-row worklist (no whole-board sweeps)
            ulong y0 = FillRow(1UL << cp, free[rp]);
            reg[rp] = y0;
            ulong dirty = 0;
            if ((y0 & free[rp - 1]) != 0) dirty |= 1UL << (rp - 1);
            if ((y0 & free[rp + 1]) != 0) dirty |= 1UL << (rp + 1);
            while (dirty != 0)
            {
                int r = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
                ulong cur = reg[r];
                ulong y = cur | (reg[r - 1] | reg[r + 1]) & free[r];
                if (y == cur) continue;
                y = FillRow(y, free[r]);
                reg[r] = y;
                ulong nw = y & ~cur;
                if ((nw & free[r - 1] & ~reg[r - 1]) != 0) dirty |= 1UL << (r - 1);
                if ((nw & free[r + 1] & ~reg[r + 1]) != 0) dirty |= 1UL << (r + 1);
            }
            return;
        }
        reg[rp] = FillRow(1UL << cp, free[rp]);
        int last = rows - 2;
        while (true)
        {
            bool changed = false;
            for (int r = 1; r <= last; r++)
            {
                ulong y = reg[r] | (reg[r - 1] | reg[r + 1]) & free[r];
                if (y == reg[r]) continue;
                y = FillRow(y, free[r]);
                if (y != reg[r]) { reg[r] = y; changed = true; }
            }
            if (!changed) return;
            changed = false;
            for (int r = last; r >= 1; r--)
            {
                ulong y = reg[r] | (reg[r - 1] | reg[r + 1]) & free[r];
                if (y == reg[r]) continue;
                y = FillRow(y, free[r]);
                if (y != reg[r]) { reg[r] = y; changed = true; }
            }
            if (!changed) return;
        }
    }
    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    static ulong FillRow(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }

    /// <summary>Floods the player's region from p over the free rows (the start cell counts as reachable even if blocked); returns the region's minimum cell (reg holds the region).</summary>
    int BbReach(int p)
    {
        int Wd = W, rp = p / Wd + 1, cp = p % Wd;
        ulong saved = bbFree[rp];
        bbFree[rp] |= 1UL << cp;
        FloodRows(bbFree, bbReg, bbRows, rp, cp);
        bbFree[rp] = saved;
        for (int r = 1; r <= bbRows - 2; r++) if (bbReg[r] != 0) return (r - 1) * Wd + BitOperations.TrailingZeroCount(bbReg[r]);
        return p;
    }

    // Pair-state shortcut: the region of two boxes on x, y is (static floor component) minus {x, y} whenever neither cell can split its
    // component locally (8-ring test) and they are not touching; then the region's minimum cell and every membership test are O(1).
    int[]? pfComp, pfMin3; bool[]? pfNsp; int[]? pfRow, pfCol;
    public static bool PairFastOff = Environment.GetEnvironmentVariable("SOKSOLV_PAIRFAST_OFF") == "1";   // test hook

    void PairFastInit()
    {
        int Wd = W;
        pfComp = new int[cells]; pfNsp = new bool[cells]; pfRow = new int[cells]; pfCol = new int[cells];
        Array.Fill(pfComp, -1);
        var q = new int[cells]; var min3 = new List<int>(); int nc = 0;
        for (int c = 0; c < cells; c++)
        {
            pfRow[c] = c / Wd; pfCol[c] = c % Wd;
            if (wall[c] || pfComp[c] >= 0) continue;
            int cnt = 0, tail = 0; q[tail++] = c; pfComp[c] = nc;
            min3.Add(c); cnt = 1; min3.Add(int.MaxValue); min3.Add(int.MaxValue);
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (y < 0 || y >= cells || wall[y] || pfComp[y] >= 0) continue;
                    pfComp[y] = nc; q[tail++] = y;
                }
            // three smallest cells of the component (cells are visited in BFS order, so collect by scanning the queue)
            Array.Sort(q, 0, tail);
            for (int j = 0; j < Math.Min(3, tail); j++) min3[nc * 3 + j] = q[j];
            nc++;
        }
        pfMin3 = [.. min3];
        Span<int> ring = [-Wd - 1, -Wd, -Wd + 1, 1, Wd + 1, Wd, Wd - 1, -1];   // circular, odd indices are orthogonal
        for (int c = 0; c < cells; c++)
        {
            if (wall[c] || pfRow[c] == 0 || pfRow[c] == lv.H + 1 || pfCol[c] == 0 || pfCol[c] == Wd - 1) continue;
            int mask = 0;
            for (int k = 0; k < 8; k++) if (!wall[c + ring[k]]) mask |= 1 << k;
            int start = -1;
            for (int k = 0; k < 8; k++) if ((mask >> k & 1) == 0) { start = k; break; }
            int runs = 0; bool inRun = false, orth = false;
            if (start < 0) runs = 1;
            else
            {
                for (int j = 1; j <= 8; j++)
                {
                    int k = (start + j) & 7;
                    if ((mask >> k & 1) != 0) { inRun = true; if ((k & 1) != 0 && !orth) { orth = true; runs++; } }
                    else { inRun = false; orth = false; }
                }
                _ = inRun;
            }
            pfNsp[c] = runs <= 1;
        }
    }

    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    bool PairFast(int x, int y) =>
        !PairFastOff && pfNsp![x] && pfNsp[y] && (Math.Abs(pfRow![x] - pfRow[y]) >= 2 || Math.Abs(pfCol![x] - pfCol[y]) >= 2);

    int PairFastMin(int start, int x, int y)
    {
        int ci = pfComp![start] * 3; var m3 = pfMin3!;
        int m = m3[ci];
        if (m == x || m == y) { m = m3[ci + 1]; if (m == x || m == y) m = m3[ci + 2]; }
        return m;
    }

    ulong[]? psregA; ulong* psregP; int psregCap;
    void PsregGrow(int need)
    {
        int cap = Math.Max(256, psregCap);
        while (cap < need) cap *= 2;
        var n = GC.AllocateArray<ulong>(cap * bbRows, pinned: true);
        if (psregA != null) Array.Copy(psregA, n, psregCap * bbRows);
        psregA = n; psregP = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref n[0]); psregCap = cap;
    }

    bool PairDeadFromBits(int a, int b, int from)
    {
        const int capStates = 200;
        int Wd = W, H = lv.H;
        if (bbFloorA == null)
        {
            bbRows = H + 2;
            bbFloorA = GC.AllocateArray<ulong>(bbRows, pinned: true); bbFreeA = GC.AllocateArray<ulong>(bbRows, pinned: true);
            bbRegA = GC.AllocateArray<ulong>(bbRows, pinned: true); bbRemA = GC.AllocateArray<ulong>(bbRows, pinned: true);
            bbFloor = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref bbFloorA[0]); bbFree = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref bbFreeA[0]);
            bbReg = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref bbRegA[0]); bbRem = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref bbRemA[0]);
            ulong all = Wd < 64 ? (1UL << Wd) - 1 : ~0UL;
            for (int r = 0; r < H; r++) { ulong wallRow = 0; for (int x = 0; x < Wd; x++) if (wall[r * Wd + x]) wallRow |= 1UL << x; bbFloor[r + 1] = all & ~wallRow; }
            if (pstarts.Length == 0) pstarts = new int[cells];
            PsregGrow(256);
            PairFastInit();
        }
        ulong* free = bbFree; ulong* reg = bbReg;
        for (int i = 0; i < bbRows; i++) free[i] = bbFloor[i];
        free[a / Wd + 1] ^= 1UL << (a % Wd); free[b / Wd + 1] ^= 1UL << (b % Wd);
        int nstarts = 0;
        if (from >= 0) pstarts[nstarts++] = BbReach(from);
        else
        {
            ulong* rem = bbRem;
            for (int i = 0; i < bbRows; i++) rem[i] = free[i];
            for (int r = 1; r <= H; r++)
                while (rem[r] != 0)
                {
                    int c = (r - 1) * Wd + BitOperations.TrailingZeroCount(rem[r]);
                    pstarts[nstarts++] = BbReach(c);
                    for (int k = 0; k < bbRows; k++) rem[k] &= ~reg[k];
                }
        }
        Span<int> mvBox = stackalloc int[8], mvT = stackalloc int[8], mvOther = stackalloc int[8];
        Span<ushort> succ = stackalloc ushort[24];
        for (int si = 0; si < nstarts; si++)
        {
            int p0 = pstarts[si];
            if (++pgen == 0) { Array.Clear(pvis); pgen = 1; }
            PVisit(a, b, p0);
            int nseen = 1, qn = 1;
            pqx[0] = a; pqy[0] = b; pqp[0] = p0; pqHas[0] = false;
            for (int i = 0; i < qn; i++)
            {
                if (nseen > capStates) return false;
                int cx = pqx[i], cy = pqy[i], cp = pqp[i];
                // successor memo: the expansion of an ordered state (cx, cy, region start cp) is a pure function of the state
                ulong mkey = (uint)cx | (ulong)(uint)cy << 16 | (ulong)(uint)cp << 32;
                int ms = PmFind(mkey);
                if (ms >= 0)
                {
                    uint inf = pmInfo![ms];
                    if ((inf & 16) != 0) return false;
                    int cnt = (int)(inf & 15), off = pmOff![ms];
                    for (int j = 0; j < cnt; j++)
                    {
                        int nx = pmArena![off + 3 * j], ny = pmArena[off + 3 * j + 1], np = pmArena[off + 3 * j + 2];
                        if (PVisit(nx, ny, np))
                        {
                            nseen++;
                            if (qn == pqx.Length) { Array.Resize(ref pqx, qn * 2); Array.Resize(ref pqy, qn * 2); Array.Resize(ref pqp, qn * 2); Array.Resize(ref pqHas, qn * 2); }
                            pqx[qn] = nx; pqy[qn] = ny; pqp[qn] = np; pqHas[qn] = false; qn++;
                        }
                    }
                    continue;
                }
                free[a / Wd + 1] ^= 1UL << (a % Wd); free[b / Wd + 1] ^= 1UL << (b % Wd);
                // a generated state's region was usually flooded when it was generated (same blocked set, same start): reuse its rows
                ulong* sr = null;
                bool fastS = PairFast(cx, cy);
                int cc = fastS ? pfComp![cp] : 0;
                if (!fastS)
                {
                    if (i == 0 || !pqHas[i]) { free[cx / Wd + 1] ^= 1UL << (cx % Wd); free[cy / Wd + 1] ^= 1UL << (cy % Wd); BbReach(cp); sr = reg; free[cx / Wd + 1] ^= 1UL << (cx % Wd); free[cy / Wd + 1] ^= 1UL << (cy % Wd); }
                    else sr = psregP + i * bbRows;
                }
                int nm = 0;
                for (int side = 0; side < 2; side++)
                {
                    int bx = side == 0 ? cx : cy, other = side == 0 ? cy : cx;
                    for (int di = 0; di < 4; di++)
                    {
                        int d = dirs[di], t = bx + d, q = bx - d;
                        if ((fastS ? pfComp![q] == cc && q != cx && q != cy : (sr[q / Wd + 1] >> (q % Wd) & 1) != 0) && !wall[t] && t != other && dist[t] >= 0) { mvBox[nm] = bx; mvT[nm] = t; mvOther[nm] = other; nm++; }
                    }
                }
                for (int mi = 0; mi < nm; mi++)
                {
                    int t = mvT[mi], other = mvOther[mi];
                    if (goal[t] && goal[other]) { PmInsert(ms, mkey, 16, null); return false; }
                    int np;
                    bool fastN = PairFast(t, other);
                    if (fastN) np = PairFastMin(mvBox[mi], t, other);
                    else
                    {
                        free[t / Wd + 1] ^= 1UL << (t % Wd); free[other / Wd + 1] ^= 1UL << (other % Wd);
                        np = BbReach(mvBox[mi]);
                        free[t / Wd + 1] ^= 1UL << (t % Wd); free[other / Wd + 1] ^= 1UL << (other % Wd);
                    }
                    int nx = Math.Min(t, other), ny = Math.Max(t, other);
                    succ[3 * mi] = (ushort)nx; succ[3 * mi + 1] = (ushort)ny; succ[3 * mi + 2] = (ushort)np;
                    if (PVisit(nx, ny, np))
                    {
                        nseen++;
                        if (qn == pqx.Length) { Array.Resize(ref pqx, qn * 2); Array.Resize(ref pqy, qn * 2); Array.Resize(ref pqp, qn * 2); Array.Resize(ref pqHas, qn * 2); }
                        pqHas[qn] = !fastN;
                        if (!fastN)
                        {
                            if (qn >= psregCap) PsregGrow(qn + 1);
                            for (int k = 0; k < bbRows; k++) psregP[qn * bbRows + k] = reg[k];
                        }
                        pqx[qn] = nx; pqy[qn] = ny; pqp[qn] = np; qn++;
                    }
                }
                PmInsert(ms, mkey, (uint)nm, succ);
                free[a / Wd + 1] ^= 1UL << (a % Wd); free[b / Wd + 1] ^= 1UL << (b % Wd);
            }
        }
        return true;
    }

    // ---- successor memo of the pair-state search (state = ordered box cells + region start; successors in generation order) ----------
    ulong[]? pmKey; int[]? pmOff; byte[]? pmInfo; ushort[]? pmArena; int pmMask, pmShift, pmCount, pmArenaN, pmLimit; bool[] pqHas = new bool[512];
    public static bool PairMemoOff = Environment.GetEnvironmentVariable("SOKSOLV_PAIRMEMO_OFF") == "1";   // test hook

    void PmInit()
    {
        int bits = 15;
        while (bits < 20 && (1L << bits) < (long)cells * 128) bits++;
        int slots = 1 << bits;
        pmKey = new ulong[slots]; pmOff = new int[slots]; pmInfo = new byte[slots]; pmArena = new ushort[slots * 10];
        pmMask = slots - 1; pmShift = 64 - bits; pmLimit = slots / 4 * 3;
    }

    /// <summary>Slot of key, or -1 - (insertion slot) when absent (-1 - 0 ... never aliases a hit).</summary>
    int PmFind(ulong key)
    {
        if (PairMemoOff) return -1;
        if (pmKey == null) PmInit();
        var keys = pmKey!;
        for (int h = (int)(key * 0x9E3779B97F4A7C15UL >> pmShift); ; h = (h + 1) & pmMask)
        {
            ulong k = keys[h];
            if (k == key) return h;
            if (k == 0) return -1 - h;
        }
    }

    void PmInsert(int missSlot, ulong key, uint info, ReadOnlySpan<ushort> succ)
    {
        if (PairMemoOff || pmCount >= pmLimit) return;
        int cnt = (int)(info & 15);
        if (pmArenaN + 3 * cnt > pmArena!.Length) return;
        int h = -1 - missSlot;
        pmKey![h] = key; pmInfo![h] = (byte)info; pmOff![h] = pmArenaN;
        for (int j = 0; j < 3 * cnt; j++) pmArena[pmArenaN + j] = succ[j];
        pmArenaN += 3 * cnt; pmCount++;
    }

    bool PairDeadFrom(int a, int b, int from)
    {
        if (W <= 64 && !PairBitsOff)
        {
            bool r = PairDeadFromBits(a, b, from);
            if (PairBitsCheck && r != PairDeadFromScan(a, b, from)) throw new InvalidOperationException($"pair search: bits {r} != scan for ({a},{b},{from})");
            return r;
        }
        return PairDeadFromScan(a, b, from);
    }

    void EnsurePairScratch()
    {
        if (pbA != null) return;
            pbA = GC.AllocateArray<byte>(cells, pinned: true); pmarkA = GC.AllocateArray<uint>(cells, pinned: true);
            pseenA = GC.AllocateArray<uint>(cells, pinned: true); pqA = GC.AllocateArray<int>(cells, pinned: true);
            pb = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref pbA[0]);
            pmarkP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref pmarkA![0]); pseenP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref pseenA![0]);
            pqP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref pqA![0]);
            for (int i = 0; i < cells; i++) pbA[i] = (byte)(wall[i] ? 1 : 0);
            pstarts = new int[cells];
    }

    bool PairDeadFromScan(int a, int b, int from)
    {
        const int capStates = 200;
        EnsurePairScratch();
        pb[a] = 1; pb[b] = 1;
        try
        {
            uint seenStamp = ++pst2;
            int nstarts = 0;
            if (from >= 0) pstarts[nstarts++] = PReach(from, out _);
            else
            foreach (int c in FloorCells())
            {
                if (pb[c] != 0 || pseenP[c] == seenStamp) continue;
                int m = PReach(c, out _);
                pstarts[nstarts++] = m;
                for (int i = 0; i < pqN; i++) pseenP[pqP[i]] = seenStamp;
            }
            Span<int> mvBox = stackalloc int[8], mvT = stackalloc int[8], mvOther = stackalloc int[8];
            for (int si = 0; si < nstarts; si++)
            {
                int p0 = pstarts[si];
                if (++pgen == 0) { Array.Clear(pvis); pgen = 1; }
                PVisit(a, b, p0);
                int nseen = 1, qn = 0;
                pqx[0] = a; pqy[0] = b; pqp[0] = p0; qn = 1;
                for (int i = 0; i < qn; i++)
                {
                    if (nseen > capStates) return false; // unknown: assume not dead
                    int cx = pqx[i], cy = pqy[i], cp = pqp[i];
                    pb[a] = 0; pb[b] = 0;
                    pb[cx] = 1; pb[cy] = 1;
                    PReach(cp, out uint st);
                    int nm = 0;
                    for (int side = 0; side < 2; side++)
                    {
                        int bx = side == 0 ? cx : cy, other = side == 0 ? cy : cx;
                        for (int di = 0; di < 4; di++)
                        {
                            int d = dirs[di];
                            int t = bx + d;
                            if (pmarkP[bx - d] == st && !wall[t] && t != other && dist[t] >= 0) { mvBox[nm] = bx; mvT[nm] = t; mvOther[nm] = other; nm++; }
                        }
                    }
                    pb[cx] = 0; pb[cy] = 0;
                    for (int mi = 0; mi < nm; mi++)
                    {
                        int t = mvT[mi], other = mvOther[mi];
                        if (goal[t] && goal[other]) { pb[a] = 1; pb[b] = 1; return false; }
                        pb[t] = 1; pb[other] = 1;
                        int np = PReach(mvBox[mi], out _);
                        pb[t] = 0; pb[other] = 0;
                        int nx = Math.Min(t, other), ny = Math.Max(t, other);
                        if (PVisit(nx, ny, np))
                        {
                            nseen++;
                            if (qn == pqx.Length) { Array.Resize(ref pqx, qn * 2); Array.Resize(ref pqy, qn * 2); Array.Resize(ref pqp, qn * 2); }
                            pqx[qn] = nx; pqy[qn] = ny; pqp[qn] = np; qn++;
                        }
                    }
                    pb[a] = 1; pb[b] = 1;
                }
            }
            return true;
        }
        finally { pb[a] = 0; pb[b] = 0; }
    }

    int[] FloorCells()
    {
        if (floorCells.Length == 0)
        {
            var l = new List<int>();
            for (int c = 0; c < cells; c++) if (!wall[c]) l.Add(c);
            floorCells = [.. l];
        }
        return floorCells;
    }

    sbyte[]? pairSide;

    /// <summary>pairSplits: boxes on a and b (alone on the board) leave the player's region from `from` smaller than all other floor.</summary>
    bool PairSplits(int a, int b, int from)
    {
        EnsurePairScratch();
        uint st = ++pst;
        int tail = 0; pqP[tail++] = from; pmarkP[from] = st;
        for (int i = 0; i < tail; i++)
            for (int di = 0; di < 4; di++)
            {
                int y = pqP[i] + dirs[di];
                if (pmarkP[y] != st && !wall[y] && y != a && y != b) { pmarkP[y] = st; pqP[tail++] = y; }
            }
        return tail < FloorCells().Length - 2;
    }

    /// <summary>pairSideDeadlock (solver.go): the box just pushed to t forms a dead pair with a neighbour given the player's actual side `from`.</summary>
    bool PairSideDeadlock(int t, int from)
    {
        if (goal[t] || dist[t] < 0) return false;
        int np = poff.Length;
        pairSide ??= new sbyte[cells * np * 4];
        int side = 0;
        while (side < 4 && t + dirs[side] != from) side++;
        if (side == 4) return false;
        int baseI = t * np;
        for (int k = 0; k < np; k++)
        {
            int x = t + poff[k];
            if (x < 0 || x >= cells || !occ[x] || x == from || (goal[t] && goal[x])) continue;
            ref sbyte v = ref pairSide[(baseI + k) * 4 + side];
            if (v == 0)
            {
                v = 2;
                SetOcc(t, false); SetOcc(x, false);
                if (PairSplits(t, x, from) && PairDeadFrom(t, x, from)) v = 1;
                SetOcc(t, true); SetOcc(x, true);
            }
            if (v == 1) return true;
        }
        return false;
    }

    bool PairDeadlock(int t)
    {
        if (goal[t] || dist[t] < 0) return false;
        int np = poff.Length;
        int baseI = t * np;
        for (int k = 0; k < np; k++)
        {
            int x = t + poff[k];
            if (x < 0 || x >= cells || !occ[x] || (goal[t] && goal[x])) continue;
            sbyte v = dpair[baseI + k];
            if (v == 0)
            {
                stats.PairsTried++;
                v = 2;
                if (PairDead(t, x)) { v = 1; stats.PairsDead++; }
                dpair[baseI + k] = v;
                dpair[x * np + popp[k]] = v;
            }
            if (v == 1) return true;
        }
        return false;
    }

    bool GoalAreaDeadlock(int t)
    {
        if (!goal[t]) return false;
        bool off = false;
        if (!Frozen(t, ref off)) return false;
        ga ??= new GoalArea(wall, goals, dirs);
        Span<int> fset = goals.Length <= 256 ? stackalloc int[goals.Length] : new int[goals.Length];
        int nf = 0;
        foreach (int g in goals)
            if (occ[g]) { bool o2 = false; if (Frozen(g, ref o2)) fset[nf++] = g; }
        return ga.Dead(fset[..nf], occ);
    }

    // ---- macros -------------------------------------------------------------------------------------------------------------
    Push[][]? MacroMoves(ReadOnlySpan<int> cur, int id, uint st)
    {
        int maxR = -1;
        foreach (int g in goals) if (!occ[g]) maxR = Math.Max(maxR, round[g]);
        if (maxR < 0 || nodes.Parent(id) < 0) return null;
        int moved = nodes.From(id) + dirs[nodes.Dir(id)];
        if (macros.TryGetValue(id, out var seq0)) { var last = seq0[^1]; moved = last.From + dirs[last.Dir]; }
        int nres = 0;
        mmScratch ??= new Push[]?[Math.Max(1, cur.Length)];
        foreach (int b in cur)
        {
            if (b != moved || !entrance[b]) continue;
            var seq = BoxPath(b, maxR, st);
            if (seq != null) { stats.Macros++; mmScratch[nres++] = seq; }
        }
        if (nres == 0) return null;
        var res = new Push[nres][];
        for (int k = 0; k < nres; k++) { res[k] = mmScratch[k]!; mmScratch[k] = null; }
        return res;
    }

    Push[]?[]? mmScratch; int[]? bpQ;
    Push[]? BoxPath(int b, int maxR, uint st)
    {
        const int limit = 500;
        if (pstamp > 1u << 31 || bstamp > 1u << 31) { Array.Clear(mark3); Array.Clear(bseenStamp); pstamp = bstamp = 0; }
        uint bs = ++bstamp;
        var q = bpQ ??= new int[4 * 500 + 8]; int qn = 0;
        for (int di = 0; di < 4; di++)
        {
            int d = dirs[di];
            if (mark[b - d] == st) { int k = b * 4 + di; bseenStamp[k] = bs; bprev[k] = -1; q[qn++] = k; }
        }
        SetOcc(b, false);
        try
        {
            for (int i = 0; i < qn && i < limit; i++)
            {
                int k = q[i];
                int c = k / 4, di = k % 4;
                int nc = c + dirs[di];
                if (wall[nc] || occ[nc] || dist[nc] < 0) continue;
                if (goal[nc] && round[nc] == maxR)
                {
                    int len = 0;
                    for (int kk = k; kk >= 0; kk = bprev[kk]) len++;
                    var seq = new Push[len];
                    for (int j = len - 1; k >= 0; k = bprev[k], j--) seq[j] = new Push(k / 4, (sbyte)(k % 4));
                    return seq;
                }
                SetOcc(nc, true);
                uint ps = ++pstamp;
                var pq = cqueue; int tail = 0; pq[tail++] = c; mark3[c] = ps;
                for (int j = 0; j < tail; j++)
                {
                    int x = pq[j];
                    foreach (int d in dirs)
                    {
                        int y = x + d;
                        if (mark3[y] != ps && !wall[y] && !occ[y]) { mark3[y] = ps; pq[tail++] = y; }
                    }
                }
                SetOcc(nc, false);
                for (int d2i = 0; d2i < 4; d2i++)
                {
                    int k2 = nc * 4 + d2i;
                    if (mark3[nc - dirs[d2i]] == ps && bseenStamp[k2] != bs) { bseenStamp[k2] = bs; bprev[k2] = k; q[qn++] = k2; }
                }
            }
            return null;
        }
        finally { SetOcc(b, true); }
    }

    // ---- move encoding --------------------------------------------------------------------------------------------------------
    static uint EncodeMove(int i, int dir, int wd, bool macro, int r)
    {
        uint m = (uint)i | (uint)dir << 9 | (uint)wd << 11 | (uint)Math.Min(r, 127) << 13;
        if (macro) m |= 1u << 12;
        return m;
    }
    static void DecodeMove(uint m, out int i, out int dir, out int wd, out bool macro, out int r)
    {
        i = (int)(m & 0x1FF); dir = (int)(m >> 9 & 3); wd = (int)(m >> 11 & 1); macro = (m >> 12 & 1) == 1; r = (int)(m >> 13 & 0x7F);
    }

    // ---- open -----------------------------------------------------------------------------------------------------------------
    void Open(int id, ReadOnlySpan<int> cur, int h, uint st)
    {
        int nc = 0;
        var op = new Opened { Start = mv.Count };
        int oi = opened.Add(op);
        nodes.MvIdx(id) = oi;
        try
        {
            long tf = Prof.Start();
            bool feas = Feasible(cur);
            Prof.Stop(10, tf);
            if (!feas) { stats.Assignment++; return; }
            var seqs = MacroMoves(cur, id, st);
            if (seqs != null)
            {
                nodeMacros[id] = seqs;
                for (int k = 0; k < seqs.Length; k++)
                    cands[nc++] = new Cand { Code = EncodeMove(k, 0, 0, true, 0), Wd = 0, Ch = h - Hc(seqs[k][0].From) };
            }
            int np = 0;
            int best = nodes.Reg(id), bestK = -1;
            long tc = Prof.Start();
            uint corral = PiCorral(st);
            Prof.Stop(5, tc);
            if (corral != 0) stats.Corrals++;
            for (int i = 0; i < cur.Length; i++)
            {
                int b = cur[i];
                if (corral != 0 && cmark[b] != corral) continue;
                int cands = 0, pend = -1, pendT = 0; // first candidate's region count is deferred: a 2nd candidate gives it via articulation, a lone one needs a flood
                for (int di = 0; di < 4; di++)
                {
                    int d = dirs[di], t = b + d;
                    if (mark[b - d] != st || wall[t] || occ[t]) continue;
                    if (dist[t] < 0) { stats.DeadSquare++; continue; }
                    SetOcc(b, false); SetOcc(t, true);
                    long t0 = Prof.Start();
                    bool f1 = Frozen2x2(t) ? (stats.Frozen2x2++ >= 0) : FreezeDeadlock(t) && (stats.Freeze++ >= 0);
                    Prof.Stop(2, t0); t0 = Prof.Start();
                    bool f2 = !f1 && GoalAreaDeadlock(t) && (stats.GoalArea++ >= 0);
                    Prof.Stop(3, t0); t0 = Prof.Start();
                    bool f3 = !f1 && !f2 && PairDeadlock(t) && (stats.PairDead++ >= 0);
                    Prof.Stop(4, t0); t0 = Prof.Start();
                    bool f4 = !f1 && !f2 && !f3 && cl != null && cl.Dead(occ, t, b, mark, st, b) && (stats.ClusterDead++ >= 0);
                    Prof.Stop(0, t0);
                    if (!(f1 || f2 || f3 || f4))
                    {
                        stats.Generated++;
                        t0 = Prof.Start();
                        int r;
                        switch (++cands)
                        {
                            case 1: r = 0; pend = np; pendT = t; break;
                            case 2:
                                SetOcc(t, false); ComputeArticulation(); SetOcc(t, true);
                                r = ChildRegions(t);
                                pushes1[pend].R = ChildRegions(pendT); pend = -1; break;
                            default: r = ChildRegions(t); break;
                        }
                        if (CheckRegions && cands > 1 && r != Regions()) throw new InvalidOperationException($"incremental region count {r} != flood {Regions()}");
                        Prof.Stop(1, t0);
                        int pen = 0;
                        if (opt.Penalties && cl != null && noise == null)
                        {
                            pen = cl.Pen;
                            if (pen > 0) stats.Penalized++;
                        }
                        pushes1[np++] = new Push1 { I = i, B = b, T = t, Dir = (sbyte)di, R = r, Pen = pen };
                    }
                    SetOcc(b, true); SetOcc(t, false);
                }
                if (pend >= 0)
                {
                    long t1 = Prof.Start();
                    SetOcc(b, false); SetOcc(pendT, true);
                    pushes1[pend].R = Regions();
                    SetOcc(b, true); SetOcc(pendT, false);
                    Prof.Stop(1, t1);
                }
            }
            for (int k = 0; k < np; k++) if (pushes1[k].R < best) { best = pushes1[k].R; bestK = k; }
            int last = -1;
            if (nodes.Parent(id) >= 0)
            {
                last = nodes.From(id) + dirs[nodes.Dir(id)];
                if (macros.TryGetValue(id, out var seq)) { var l = seq[^1]; last = l.From + dirs[l.Dir]; }
            }
            for (int k = 0; k < np; k++)
            {
                ref var p = ref pushes1[k];
                int wd = 1;
                if (k == bestK) { wd = 0; stats.ConnAdvisor++; }
                else if (p.B == last) wd = 0;
                cands[nc++] = new Cand { Code = EncodeMove(p.I, p.Dir, wd, false, p.R), Wd = wd, Ch = h - Hc(p.B) + Hc(p.T) + p.Pen };
            }
        }
        finally
        {
            // stable insertion sort by (wd, ch), as slices.SortStableFunc
            for (int i = 1; i < nc; i++)
            {
                var x = cands[i]; int j = i - 1;
                while (j >= 0 && (cands[j].Wd > x.Wd || (cands[j].Wd == x.Wd && cands[j].Ch > x.Ch))) { cands[j + 1] = cands[j]; j--; }
                cands[j + 1] = x;
            }
            for (int i = 0; i < nc; i++) mv.Add(cands[i].Code);
            ref var o = ref opened[oi];
            o.Len = (ushort)Math.Min(nc, 0xFFFF);
            o.Left = o.Len;
        }
    }

    (int ch, int w) MoveKeyAt(int id, int k, ReadOnlySpan<int> cur, int h)
    {
        DecodeMove(mv[k] & ~MoveUsed, out int i, out int dir, out int wd, out bool macro, out _);
        if (macro) return (h - Hc(nodeMacros[id][i][0].From), nodes.W(id));
        int b = cur[i];
        return (h - Hc(b) + Hc(b + dirs[dir]), nodes.W(id) + wd);
    }

    int NextMove(int id, int src, ReadOnlySpan<int> cur, int h)
    {
        ref var o = ref opened[nodes.MvIdx(id)];
        int start = o.Start, end = start + o.Len;
        if (src >= 0)
        {
            int k = start + o.Next;
            while ((mv[k] & MoveUsed) != 0) k++;
            o.Next = (ushort)(k - start);
            return k;
        }
        int best = -1, bh = int.MaxValue;
        for (int k = start; k < end; k++)
            if ((mv[k] & MoveUsed) == 0)
            {
                var (ch, _) = MoveKeyAt(id, k, cur, h);
                if (ch < bh) { best = k; bh = ch; }
            }
        return best;
    }

    // ---- apply / add ---------------------------------------------------------------------------------------------------------------
    /// <summary>Applies move m of node id; s.occ holds its boxes. Returns the child id (-1 if already stored) and h.</summary>
    int Apply(int id, uint m, ReadOnlySpan<int> cur, Span<int> child, int h, uint st, ulong parentHash, out int ch)
    {
        DecodeMove(m, out int i, out int dir, out int wd, out bool macro, out int r);
        ulong* pk = nodes.Key(id);
        int nw = nodes.Nw;
        for (int w = 0; w < nw; w++) ckey[w] = pk[w];
        int pp = nodes.Player(id);
        cur.CopyTo(child);
        if (macro)
        {
            var seq = nodeMacros[id][i];
            int b = seq[0].From;
            var last = seq[^1];
            int g = last.From + dirs[last.Dir];
            int ix = ((ReadOnlySpan<int>)cur).IndexOf(b);
            child[ix] = g;
            Flip(b); Flip(g);
            SetOcc(b, false); SetOcc(g, true);
            int np = Reach(g - dirs[last.Dir], mark2);
            ch = h - Hc(b);
            ulong zh = parentHash ^ nodes.ZBox(b) ^ nodes.ZBox(g) ^ nodes.ZPlayer(pp) ^ nodes.ZPlayer(np);
            int cid = Add(zh, np, id, b, last.Dir, seq, nodes.G(id) + seq.Length, ch, nodes.W(id), -1, child);
            SetOcc(b, true); SetOcc(g, false);
            return cid;
        }
        else
        {
            int b = cur[i];
            int t = b + dirs[dir];
            child[i] = t;
            Flip(b); Flip(t);
            SetOcc(b, false); SetOcc(t, true);
            int np = Normalize(b, t, nodes.Player(id), st);
            ch = h - Hc(b) + Hc(t);
            ulong zh = parentHash ^ nodes.ZBox(b) ^ nodes.ZBox(t) ^ nodes.ZPlayer(pp) ^ nodes.ZPlayer(np);
            int cid = Add(zh, np, id, b, (sbyte)dir, null, nodes.G(id) + 1, ch, nodes.W(id) + wd, r, child);
            SetOcc(b, true); SetOcc(t, false);
            return cid;
        }
    }

    void Flip(int cell) { int f = nodes.Fidx[cell]; ckey[f >> 6] ^= 1UL << (f & 63); }

    /// <summary>Inserts a new node, or records a shorter path to an existing one. The child's key is in ckey.</summary>
    int Add(ulong zh, int player, int parent, int from, sbyte dir, Push[]? seq, int g, int h, int w, int reg, ReadOnlySpan<int> boxes)
    {
        int id = nodes.Find(ckey, player, zh, out int slot, out uint fp);
        if (id >= 0)
        {
            stats.Duplicates++;
            if (nodes.G(id) <= g) return -1;
            nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g;
            if (seq != null) macros[id] = (Push[])seq.Clone(); else macros.Remove(id);
            return -1;
        }
        if (memFull) return -1;
        if (sd != null && parent >= 0)
            foreach (int c in boxes)
                if (!goal[c] && sd.Dead(c, player)) { stats.SideDead++; return -1; }
        id = nodes.Insert(slot, fp, ckey, player);
        if (seq != null) macros[id] = (Push[])seq.Clone();
        nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g; nodes.W(id) = w;
        if (reg < 0) reg = Regions();
        nodes.Reg(id) = (sbyte)Math.Min(reg, 127);
        int onGoal = PackedCount();
        if (CheckRegions && onGoal != PackedCountSlow()) throw new InvalidOperationException("packed count mismatch");
        int bucket = onGoal * MaxRegions + Math.Min(reg, MaxRegions - 1);
        nodes.Bucket(id) = (ushort)bucket;
        PushNode(h, w, id, bucket);
        if (opt.Net != null) MeetAdd(id, boxes, player);
        if (nodes.NeedsGrow)
        {
            if (opt.BudgetBytes > 0 && TrackedBytes() + (ulong)nodes.TableSlots * 16 > opt.BudgetBytes) memFull = true;
            else nodes.Grow();
        }
        return id;
    }
}
