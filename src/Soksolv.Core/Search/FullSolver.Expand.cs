using System.Numerics;
namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- features ---------------------------------------------------------------------------------------------------
    int PackedCount()
    {
        int maxR = -1;
        foreach (int g in goals) if (!occ[g]) maxR = Math.Max(maxR, round[g]);
        int c = 0;
        foreach (int g in goals) if (occ[g] && round[g] >= maxR) c++;
        return c;
    }

    /// <summary>Go regions() under target pockets (exp-460): components made only of pocket cells do not count.</summary>
    int RegionsRel()
    {
        if (tgtPocket == null) return Regions();
        if (floorCells.Length == 0) Regions();
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
    int[]? artDisc, artLow, artPieces; uint[]? artSeen; uint artStamp; int artComps;
    int[]? artStackC, artStackP; sbyte[]? artStackNext;
    /// <summary>Test hook: verify every incremental region count against a full flood.</summary>
    public static bool CheckRegions;

    void ComputeArticulation()
    {
        if (artDisc == null)
        {
            artDisc = new int[cells]; artLow = new int[cells]; artPieces = new int[cells]; artSeen = new uint[cells];
            artStackC = new int[cells + 1]; artStackP = new int[cells + 1]; artStackNext = new sbyte[cells + 1];
        }
        var disc = artDisc; var low = artLow!; var pieces = artPieces!; var seen = artSeen!;
        var sc = artStackC!; var sp = artStackP!; var sn = artStackNext!;
        uint st = ++artStamp;
        artComps = 0;
        int timer = 0;
        foreach (int root in FloorCells())
        {
            if (blk[root] != 0 || seen[root] == st) continue;
            artComps++;
            timer++;
            disc[root] = timer; low[root] = timer; pieces[root] = 0; seen[root] = st;
            int sz = 0; sc[0] = root; sp[0] = -1; sn[0] = 0; sz = 1;
            while (sz > 0)
            {
                int top = sz - 1;
                if (sn[top] < 4)
                {
                    int y = sc[top] + dirs[sn[top]++];
                    if (blk[y] != 0) continue;
                    if (seen[y] != st)
                    {
                        timer++;
                        disc[y] = timer; low[y] = timer; pieces[y] = 1; seen[y] = st;
                        sc[sz] = y; sp[sz] = sc[top]; sn[sz] = 0; sz++;
                    }
                    else if (y != sp[top])
                    {
                        int c = sc[top];
                        if (disc[y] < low[c]) low[c] = disc[y];
                    }
                    continue;
                }
                int cc = sc[top], pp = sp[top];
                sz--;
                if (pp < 0) continue;
                if (low[cc] < low[pp]) low[pp] = low[cc];
                if (low[cc] >= disc[pp]) pieces[pp]++;
            }
        }
    }

    int ChildRegions(int t) => artComps - 1 + artPieces![t];

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
    public static bool PairBitsOff, PairBitsCheck;   // test hooks

    static void FloodRows(ulong* free, ulong* reg, int rows, int rp, int cp)
    {
        for (int i = 0; i < rows; i++) reg[i] = 0;
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
        for (int si = 0; si < nstarts; si++)
        {
            int p0 = pstarts[si];
            if (++pgen == 0) { Array.Clear(pvis); pgen = 1; }
            PVisit(a, b, p0);
            int nseen = 1, qn = 1;
            pqx[0] = a; pqy[0] = b; pqp[0] = p0;
            for (int i = 0; i < qn; i++)
            {
                if (nseen > capStates) return false;
                int cx = pqx[i], cy = pqy[i], cp = pqp[i];
                free[a / Wd + 1] ^= 1UL << (a % Wd); free[b / Wd + 1] ^= 1UL << (b % Wd);
                free[cx / Wd + 1] ^= 1UL << (cx % Wd); free[cy / Wd + 1] ^= 1UL << (cy % Wd);
                BbReach(cp);
                int nm = 0;
                for (int side = 0; side < 2; side++)
                {
                    int bx = side == 0 ? cx : cy, other = side == 0 ? cy : cx;
                    for (int di = 0; di < 4; di++)
                    {
                        int d = dirs[di], t = bx + d, q = bx - d;
                        if ((reg[q / Wd + 1] >> (q % Wd) & 1) != 0 && !wall[t] && t != other && dist[t] >= 0) { mvBox[nm] = bx; mvT[nm] = t; mvOther[nm] = other; nm++; }
                    }
                }
                free[cx / Wd + 1] ^= 1UL << (cx % Wd); free[cy / Wd + 1] ^= 1UL << (cy % Wd);
                for (int mi = 0; mi < nm; mi++)
                {
                    int t = mvT[mi], other = mvOther[mi];
                    if (goal[t] && goal[other]) return false;
                    free[t / Wd + 1] ^= 1UL << (t % Wd); free[other / Wd + 1] ^= 1UL << (other % Wd);
                    int np = BbReach(mvBox[mi]);
                    free[t / Wd + 1] ^= 1UL << (t % Wd); free[other / Wd + 1] ^= 1UL << (other % Wd);
                    int nx = Math.Min(t, other), ny = Math.Max(t, other);
                    if (PVisit(nx, ny, np))
                    {
                        nseen++;
                        if (qn == pqx.Length) { Array.Resize(ref pqx, qn * 2); Array.Resize(ref pqy, qn * 2); Array.Resize(ref pqp, qn * 2); }
                        pqx[qn] = nx; pqy[qn] = ny; pqp[qn] = np; qn++;
                    }
                }
                free[a / Wd + 1] ^= 1UL << (a % Wd); free[b / Wd + 1] ^= 1UL << (b % Wd);
            }
        }
        return true;
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
        List<Push[]>? res = null;
        foreach (int b in cur)
        {
            if (b != moved || !entrance[b]) continue;
            var seq = BoxPath(b, maxR, st);
            if (seq != null) { stats.Macros++; (res ??= new()).Add(seq); }
        }
        return res?.ToArray();
    }

    Push[]? BoxPath(int b, int maxR, uint st)
    {
        const int limit = 500;
        if (pstamp > 1u << 31 || bstamp > 1u << 31) { Array.Clear(mark3); Array.Clear(bseenStamp); pstamp = bstamp = 0; }
        uint bs = ++bstamp;
        var q = new List<int>();
        for (int di = 0; di < 4; di++)
        {
            int d = dirs[di];
            if (mark[b - d] == st) { int k = b * 4 + di; bseenStamp[k] = bs; bprev[k] = -1; q.Add(k); }
        }
        SetOcc(b, false);
        try
        {
            for (int i = 0; i < q.Count && i < limit; i++)
            {
                int k = q[i];
                int c = k / 4, di = k % 4;
                int nc = c + dirs[di];
                if (wall[nc] || occ[nc] || dist[nc] < 0) continue;
                if (goal[nc] && round[nc] == maxR)
                {
                    var seq = new List<Push>();
                    for (; k >= 0; k = bprev[k]) seq.Add(new Push(k / 4, (sbyte)(k % 4)));
                    seq.Reverse();
                    return seq.ToArray();
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
                    if (mark3[nc - dirs[d2i]] == ps && bseenStamp[k2] != bs) { bseenStamp[k2] = bs; bprev[k2] = k; q.Add(k2); }
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
                int cands = 0;
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
                            case 1: r = Regions(); break;
                            case 2:
                                SetOcc(t, false); ComputeArticulation(); SetOcc(t, true);
                                r = ChildRegions(t); break;
                            default: r = ChildRegions(t); break;
                        }
                        if (CheckRegions && r != Regions()) throw new InvalidOperationException($"incremental region count {r} != flood {Regions()}");
                        Prof.Stop(1, t0);
                        int pen = 0;
                        if (opt.Penalties && cl != null && noise == null)
                        {
                            pen = cl.Pen;
                            if (pen > 0) stats.Penalized++;
                        }
                        pushes1[np++] = new Push1 { I = i, B = b, T = t, Dir = (sbyte)di, R = r, Pen = pen };
                        if (r < best) { best = r; bestK = np - 1; }
                    }
                    SetOcc(b, true); SetOcc(t, false);
                }
            }
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
