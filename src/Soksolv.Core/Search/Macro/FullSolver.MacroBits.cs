using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search;

/// <summary>
/// Word-parallel box search support (Go boxflood.go bitBoard + moveaccess.go moveAccessBits), boards up to 64 wide:
/// one uint64 per board row. Results (region minimum, mark3 stamps, mobility, explorer, access facts as a set) equal the
/// queue-flood / per-box scan versions, which remain for wide boards and as the test reference (<see cref="CheckAccessBits"/>).
/// </summary>
public sealed unsafe partial class FullSolver
{
    public static bool BitBoardOff = Environment.GetEnvironmentVariable("SOKSOLV_BITS_OFF") == "1";
    public static bool CheckAccessBits = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_BITS") == "1";   // test hooks

    const int BoxBitAfter = 3;   // Go boxBitAfter: queue-flood pushes before the occupancy rows are built (non-node mode)

    ulong[]? bbWallA, bbNFreeA, bbNRegA, bbParA, bbAliveA;
    ulong* bbWallP, bbNFreeP, bbNRegP, bbParP, bbAliveP;
    int bbH; ulong bbAll; int bbRegLo, bbRegHi;   // rows (1-based) of the last flooded region
    bool bbInit, bbOn, bbNode, bbNodeBuilt, bbParentBuilt;
    // per direction access geometry (Go accessGeo)
    int[] gSupR = new int[4], gSupX = new int[4], gLandR = new int[4], gLandX = new int[4], gFreeR = new int[4], gFreeX = new int[4];

    bool BbEnsure()
    {
        if (bbInit) return bbOn;
        bbInit = true;
        if (W > 64 || BitBoardOff) { bbOn = false; return false; }
        bbOn = true; bbH = lv.H; int rows = bbH + 2;
        bbWallA = GC.AllocateArray<ulong>(rows, pinned: true); bbNFreeA = GC.AllocateArray<ulong>(rows, pinned: true);
        bbNRegA = GC.AllocateArray<ulong>(rows, pinned: true); bbParA = GC.AllocateArray<ulong>(rows, pinned: true); bbAliveA = GC.AllocateArray<ulong>(rows, pinned: true);
        bbWallP = (ulong*)Unsafe.AsPointer(ref bbWallA[0]); bbNFreeP = (ulong*)Unsafe.AsPointer(ref bbNFreeA[0]); bbNRegP = (ulong*)Unsafe.AsPointer(ref bbNRegA[0]);
        bbParP = (ulong*)Unsafe.AsPointer(ref bbParA[0]); bbAliveP = (ulong*)Unsafe.AsPointer(ref bbAliveA[0]);
        bbAll = W < 64 ? (1UL << W) - 1 : ~0UL;
        divM = (uint)((1UL << 32) / (uint)W + 1); bbRegLo = bbH + 2; bbRegHi = -1;
        for (int r = 0; r < bbH; r++)
            for (int x = 0; x < W; x++)
            {
                if (wall[r * W + x]) bbWallP[r + 1] |= 1UL << x;
                if (dist[r * W + x] >= 0) bbAliveP[r + 1] |= 1UL << x;
            }
        for (int k = 0; k < 4; k++)
        {
            int d = dirs[k], dr = 0, dx = 0;
            if (d == -W) dr = -1; else if (d == W) dr = 1; else if (d == -1) dx = -1; else dx = 1;
            gLandR[k] = dr; gLandX[k] = dx;
            if (pull) { gSupR[k] = dr; gSupX[k] = dx; gFreeR[k] = 2 * dr; gFreeX[k] = 2 * dx; }
            else { gSupR[k] = -dr; gSupX[k] = -dx; gFreeR[k] = dr; gFreeX[k] = dx; }
        }
        return true;
    }

    /// <summary>Fills the node's free rows from the occupancy (free = floor and not occupied).</summary>
    void BbBuild()
    {
        if (fbits && !ReachRowsOff)
        {
            // frR mirrors blk (free = not wall, not box) row by row, kept current by SetOcc: copy instead of rebuilding from occ cell by cell
            for (int r = 0; r < bbH; r++) bbNFreeP[r + 1] = frR[r];
            return;
        }
        for (int r = 0; r < bbH; r++)
        {
            ulong m = 0;
            int baseI = r * W;
            for (int x = 0; x < W; x++) if (occ[baseI + x]) m |= 1UL << x;
            bbNFreeP[r + 1] = bbAll & ~bbWallP[r + 1] & ~m;
        }
    }

    /// <summary>Row-bitset flood. Stamps m3 with ps for every cell reachable from pc with nc blocked unless <see cref="mBitsOnly"/> (then the region
    /// lives only in the row bitset bbNRegP; ask <see cref="InPR"/>). Returns the region's minimum cell.</summary>
    int BbFlood(int pc, int nc, uint ps)
    {
        int w = W;
        int rn = nc / w + 1, rp = pc / w + 1, cp = pc % w;
        ulong* free = bbNFreeP, reg = bbNRegP;
        ulong s1 = free[rn];
        free[rn] &= ~(1UL << (nc % w));
        ulong s2 = free[rp];
        free[rp] |= 1UL << cp;
        // the previous region's rows are the only non-zero ones: clear just those (a connected region spans consecutive rows)
        int lo = rp, hi = rp;
        if (bbH + 2 <= 64)
        {
            if (bbRegHi >= 0) for (int r = bbRegLo; r <= bbRegHi; r++) reg[r] = 0;
            FloodRowsSpan(free, reg, bbH + 2, rp, cp, ref lo, ref hi);
        }
        else
        {
            FloodRows(free, reg, bbH + 2, rp, cp);
            while (reg[lo - 1] != 0) lo--;
            while (reg[hi + 1] != 0) hi++;
        }
        free[rp] = s2; free[rn] = s1;
        bbRegLo = lo; bbRegHi = hi;
        int minC = (lo - 1) * w + BitOperations.TrailingZeroCount(reg[lo]);
        if (!mBitsOnly)
            for (int r = lo; r <= hi; r++)
            {
                ulong x = reg[r];
                int baseC = (r - 1) * w;
                for (; x != 0; x &= x - 1) m3P[baseC + BitOperations.TrailingZeroCount(x)] = ps;
            }
        return minC;
    }

    bool mBitsOnly; uint divM;
    /// <summary>Is cell c in the player region of the last flood (bit-only mode) / stamped ps (m3 mode)?</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    bool InPR(int c)
    {
        if (!mBitsOnly) return m3P[c] == mpstamp;
        int r = (int)(((ulong)(uint)c * divM) >> 32);
        return (bbNRegP[r + 1] >> (c - r * W) & 1) != 0;
    }

    /// <summary>FloodRowsDirty on rows whose reg is already zero (the caller cleared them).</summary>
    static void FloodRowsDirtyC(ulong* free, ulong* reg, int rows, int rp, int cp) => FloodRowsDirty(free, reg, rows, rp, cp, false);

    byte[]? accCntA; byte* accCnt; int accMp, accParLo, accParHi; int[] accSupOff = new int[4];
    /// <summary>Per-node tables of the delta move-access terms: the parent's region rows, the number of (box, direction) pairs
    /// supported by each cell (<c>accCnt</c>), the parent's mobility <c>accMp</c> = sum of accCnt over the parent's region.</summary>
    void BbBuildParent(uint st, ReadOnlySpan<int> cur)
    {
        int lo = bbH + 2, hi = -1;
        for (int r = 0; r < bbH; r++)
        {
            ulong m = 0;
            int baseI = r * W;
            for (int x = 0; x < W; x++) if (mark[baseI + x] == st) m |= 1UL << x;
            bbParP[r + 1] = m;
            if (m != 0) { if (lo > r + 1) lo = r + 1; hi = r + 1; }
        }
        accParLo = lo; accParHi = hi;
        if (accCntA == null) { accCntA = GC.AllocateArray<byte>(cells, pinned: true); accCnt = (byte*)Unsafe.AsPointer(ref accCntA[0]); }
        Array.Clear(accCntA);
        for (int k = 0; k < 4; k++) accSupOff[k] = pull ? dirs[k] : -dirs[k];
        int mp = 0;
        foreach (int c in cur)
            for (int k = 0; k < 4; k++)
            {
                int sc = c + accSupOff[k];
                accCnt[sc]++;
                int rr = (int)(((ulong)(uint)sc * divM) >> 32);
                if ((bbParP[rr + 1] >> (sc - rr * W) & 1) != 0) mp++;
            }
        accMp = mp;
        bbParentBuilt = true;
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    ulong RowAt(ulong* rows, int i) => (uint)i >= (uint)(bbH + 2) ? 0 : rows[i];
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong AlignX(ulong v, int dx) => dx >= 0 ? v >> dx : v << -dx;

    // per-direction access geometry as shifts: bit x of AlignX(v, dx) = bit x+dx of v = (v >> sr) << sl (one of them 0). Row offsets are relative to r+1.
    struct AccGeo { public int SupR, SupSr, SupSl, LandR, LandSr, LandSl, FreeR, FreeSr, FreeSl; }
    AccGeo[]? accGeoA; AccGeo* accGeo;
    void BbGeoInit()
    {
        accGeoA = GC.AllocateArray<AccGeo>(4, pinned: true); accGeo = (AccGeo*)Unsafe.AsPointer(ref accGeoA[0]);
        for (int k = 0; k < 4; k++)
        {
            ref var g = ref accGeo[k];
            g.SupR = gSupR[k]; g.SupSr = gSupX[k] > 0 ? gSupX[k] : 0; g.SupSl = gSupX[k] < 0 ? -gSupX[k] : 0;
            g.LandR = gLandR[k]; g.LandSr = gLandX[k] > 0 ? gLandX[k] : 0; g.LandSl = gLandX[k] < 0 ? -gLandX[k] : 0;
            g.FreeR = gFreeR[k]; g.FreeSr = gFreeX[k] > 0 ? gFreeX[k] : 0; g.FreeSl = gFreeX[k] < 0 ? -gFreeX[k] : 0;
        }
    }

    public static bool AccessRowsOnly = Environment.GetEnvironmentVariable("SOKSOLV_ACCESS_ROWS") == "1";   // test hook: skip the delta path

    /// <summary>
    /// Move access as a delta of the parent's per-node tables (no recorded facts): mobility(child) = Mp + sum(cnt over D) - sum(cnt over E) + the
    /// 4+4 supports of the lifted and the landed box that lie in the child's region, with D = child region \ parent region, E = parent \ child;
    /// explorer = an unchanged box with a legal push whose support lies in D. Returns mobility -1 when D/E are too large (the row kernel runs instead).
    /// </summary>
    (int mobility, bool explorer, int accStart, int accLen) MMoveAccessDelta(ReadOnlySpan<int> cur, int target, int origB, bool rec = false)
    {
        const int Lim = 160;
        ulong* reg = bbNRegP, par = bbParP, free = bbNFreeP, wallR = bbWallP, alive = bbAliveP;
        int lo = Math.Min(bbRegLo, accParLo), hi = Math.Max(bbRegHi, accParHi);
        int w = W; uint dm = divM;
        byte* cnt = accCnt;
        int mob = accMp, nb = 0;
        bool explorer = false;
        // fact recording (novelty lane): the facts of the row kernel, enumerated over the same D cells
        int accStart = rec ? mAccN : 0;
        bool vacated = rec && ((mCmdMask | mUnpinMask) != 0 || (goal[origB] && !goal[target]));
        bool displaced = rec && (mUnpackedRegime || vacated);
        for (int r = lo; r <= hi; r++)
        {
            ulong d = reg[r] & ~par[r], e = par[r] & ~reg[r];
            if ((d | e) == 0) continue;
            nb += BitOperations.PopCount(d) + BitOperations.PopCount(e);
            if (nb > Lim) { if (rec) mAccN = accStart; return (-1, false, 0, 0); }
            int baseC = (r - 1) * w;
            for (ulong x = e; x != 0; x &= x - 1) mob -= cnt[baseC + BitOperations.TrailingZeroCount(x)];
            for (ulong x = d; x != 0; x &= x - 1)
            {
                int sc = baseC + BitOperations.TrailingZeroCount(x);
                mob += cnt[sc];
                if (explorer && !rec) continue;
                if (cnt[sc] == 0) continue;   // no (box, direction) is supported by this cell
                for (int k = 0; k < 4; k++)
                {
                    int cb = sc - accSupOff[k];
                    int rb = (int)(((ulong)(uint)cb * dm) >> 32);
                    int colb = cb - rb * w;
                    if (((bbAll & ~free[rb + 1] & ~wallR[rb + 1]) >> colb & 1) == 0) continue;   // not a box
                    int dk = dirs[k];
                    int to = pull ? cb + 2 * dk : cb + dk;
                    if ((uint)to >= (uint)cells || to == target) continue;
                    int rt = (int)(((ulong)(uint)to * dm) >> 32);
                    if ((free[rt + 1] >> (to - rt * w) & 1) == 0) continue;
                    if (dist[cb + dk] < 0) continue;
                    explorer = true;
                    if (!rec) break;
                    ulong psi = (uint)(cb * 4 + k);
                    AccPush(psi);
                    AccPush((1UL << 40) | (psi << 20) | (uint)target);
                    if (displaced) AccPush((2UL << 40) | (psi << 20) | (uint)origB);
                    if (vacated) AccPush((4UL << 40) | ((ulong)(uint)origB << 20) | psi);
                }
            }
        }
        for (int k = 0; k < 4; k++)
        {
            int sb = origB + accSupOff[k], st = target + accSupOff[k];
            int rb = (int)(((ulong)(uint)sb * dm) >> 32), rs = (int)(((ulong)(uint)st * dm) >> 32);
            if ((reg[rb + 1] >> (sb - rb * w) & 1) != 0) mob--;
            bool tIn = (reg[rs + 1] >> (st - rs * w) & 1) != 0;
            if (tIn) mob++;
            if (vacated && tIn)
            {
                // the moved box itself: a legal push from a support in the child's region (fact 4 covers it even when the support is not fresh)
                int dk = dirs[k];
                int to = pull ? target + 2 * dk : target + dk;
                if ((uint)to < (uint)cells && dist[target + dk] >= 0)
                {
                    int rt = (int)(((ulong)(uint)to * dm) >> 32);
                    if ((free[rt + 1] >> (to - rt * w) & 1) != 0)
                        AccPush((4UL << 40) | ((ulong)(uint)origB << 20) | (uint)(target * 4 + k));
                }
            }
        }
        int accLen = 0;
        if (rec)
        {
            if (mCandNP >= 0 && mark[mCandNP] != mOpenSt) AccPush((3UL << 40) | ((ulong)(uint)mCandNP << 20) | (uint)target);
            accLen = mAccN - accStart;
        }
        return (mob, explorer, accStart, accLen);
    }

    /// <summary>Go moveAccessBits: every term is a row-bitset expression over the rows holding boxes.</summary>
    (int mobility, bool explorer, int accStart, int accLen) MMoveAccessBits(ReadOnlySpan<int> cur, int moved, int target)
    {
        if (!bbParentBuilt) BbBuildParent(mOpenSt, cur);
        if (accGeoA == null) BbGeoInit();
        int origB = cur[moved];
        bool recordAcc = AccessNovelty && NoveltyBudget() > 0;
        if (!AccessRowsOnly)
        {
            var dr = MMoveAccessDelta(cur, target, origB, recordAcc);
            if (dr.mobility >= 0) return dr;
        }
        int accStart = recordAcc ? mAccN : 0;
        bool vacated = (mCmdMask | mUnpinMask) != 0 || (goal[origB] && !goal[target]);
        bool displaced = recordAcc && (mUnpackedRegime || vacated);
        bool goalVacated = recordAcc && vacated;
        int mobility = 0; bool explorer = false;
        int w = W;
        int tr = target / w; ulong tbit = 1UL << (target % w);
        ulong* free = bbNFreeP, wallR = bbWallP, reg = bbNRegP, par = bbParP, alive = bbAliveP;
        AccGeo* geo = accGeo;
        ulong all = bbAll;
        // only box rows next to the region's rows can have a supporting cell in it
        for (int r = Math.Max(0, bbRegLo - 2), rEnd = Math.Min(bbH - 1, bbRegHi); r <= rEnd; r++)
        {
            ulong boxes = all & ~free[r + 1] & ~wallR[r + 1];
            ulong tm = 0;
            if (r == tr) { boxes |= tbit; tm = tbit; }
            if (boxes == 0) continue;
            if (explorer && !recordAcc)
            {
                // legality no longer matters: mobility only
                for (int k = 0; k < 4; k++)
                {
                    AccGeo* g = geo + k;
                    mobility += BitOperations.PopCount(boxes & ((reg[r + g->SupR + 1] >> g->SupSr) << g->SupSl));
                }
                continue;
            }
            for (int k = 0; k < 4; k++)
            {
                AccGeo* g = geo + k;
                ulong reach = (reg[r + g->SupR + 1] >> g->SupSr) << g->SupSl;
                ulong sup = boxes & reach;
                if (sup == 0) continue;
                mobility += BitOperations.PopCount(sup);
                if (explorer && !recordAcc) continue; // legality only matters for the first explorer or for recorded facts
                ulong fresh = sup & ~((par[r + g->SupR + 1] >> g->SupSr) << g->SupSl);
                if (fresh == 0 && !goalVacated) continue;
                int fr = r + g->FreeR;
                ulong freeRow = RowAt(free, fr + 1) & ~(fr == tr ? tbit : 0UL);
                ulong legal = sup & ((freeRow >> g->FreeSr) << g->FreeSl) & ((alive[r + g->LandR + 1] >> g->LandSr) << g->LandSl);
                if (legal == 0) continue;
                ulong x = fresh & legal & ~tm;
                if (x != 0)
                {
                    explorer = true;
                    if (recordAcc)
                        for (; x != 0; x &= x - 1)
                        {
                            int b = r * w + BitOperations.TrailingZeroCount(x);
                            ulong psi = (uint)(b * 4 + k);
                            AccPush(psi);
                            AccPush((1UL << 40) | (psi << 20) | (uint)target);
                            if (displaced) AccPush((2UL << 40) | (psi << 20) | (uint)origB);
                        }
                }
                if (goalVacated)
                    for (ulong y = legal & (fresh | tm); y != 0; y &= y - 1)
                    {
                        int b = r * w + BitOperations.TrailingZeroCount(y);
                        ulong psi = (uint)(b * 4 + k);
                        AccPush((4UL << 40) | ((ulong)(uint)origB << 20) | psi);
                    }
            }
        }
        int accLen = 0;
        if (recordAcc)
        {
            if (mCandNP >= 0 && mark[mCandNP] != mOpenSt) AccPush((3UL << 40) | ((ulong)(uint)mCandNP << 20) | (uint)target);
            accLen = mAccN - accStart;
        }
        return (mobility, explorer, accStart, accLen);
    }

    // ---- pointer-based articulation pass for the box search (same result as ComputeArticulation, interleaved per-cell record) ----------
    struct ArtC { public int Disc, Low, Pieces; public uint Seen; }
    struct ArtFr { public int C, P, N; }
    ArtC[]? maArr; ArtC* maP; ArtFr[]? mfArr; ArtFr* mfP; uint maStamp; int[]? mDirsPin; int* mDirsP;

    /// <summary>One iterative Tarjan pass over the free cells (blk = walls and boxes): component count in <c>artComps</c>, per-cell piece counts for <see cref="MChildRegions"/>.</summary>
    int maBase;
    void MArtic()
    {
        if (maArr == null)
        {
            maArr = GC.AllocateArray<ArtC>(cells, pinned: true); maP = (ArtC*)Unsafe.AsPointer(ref maArr[0]);
            mfArr = GC.AllocateArray<ArtFr>(cells + 1, pinned: true); mfP = (ArtFr*)Unsafe.AsPointer(ref mfArr[0]);
            mDirsPin = GC.AllocateArray<int>(4, pinned: true); mDirsP = (int*)Unsafe.AsPointer(ref mDirsPin[0]);
            for (int i = 0; i < 4; i++) mDirsP[i] = dirs[i];
        }
        ArtC* ac = maP; ArtFr* stack = mfP; int* dr = mDirsP; byte* bk = blk;
        // discovery numbers grow across calls (a cell is unseen in this pass iff Disc <= b0), so nothing is cleared per pass
        if (maBase > int.MaxValue - 4 * cells) { Array.Clear(maArr); maBase = 0; }
        int b0 = maBase, timer = b0, comps = 0;
        var floor = FloorCells();
        for (int fi = 0; fi < floor.Length; fi++)
        {
            int root = floor[fi];
            if (bk[root] != 0 || ac[root].Disc > b0) continue;
            comps++;
            timer++;
            ac[root].Disc = timer; ac[root].Pieces = 0;
            int c = root, p = -1, n = 0, low = timer, sp = 0;   // current frame in registers, suspended frames on the stack
            while (true)
            {
                if (n < 4)
                {
                    int y = c + dr[n++];
                    if (bk[y] != 0) continue;
                    ArtC* ay = ac + y;
                    int dy = ay->Disc;
                    if (dy <= b0)
                    {
                        timer++;
                        ay->Disc = timer; ay->Pieces = 1;
                        ac[c].Low = low;
                        stack[sp].C = c; stack[sp].P = p; stack[sp].N = n; sp++;
                        p = c; c = y; n = 0; low = timer;
                        continue;
                    }
                    if (y != p && dy < low) low = dy;
                    continue;
                }
                if (sp == 0) break;
                sp--;
                int pc = stack[sp].C;
                int plow = ac[pc].Low;
                if (low >= ac[pc].Disc) ac[pc].Pieces++;
                if (low < plow) plow = low;
                p = stack[sp].P; n = stack[sp].N; c = pc; low = plow;
            }
        }
        maBase = timer;
        artComps = comps;
    }

    int MChildRegions(int t) => artComps - 1 + maP[t].Pieces;

    static readonly bool[] RingNsp = BuildRingNsp();
    /// <summary>ring (bit k = ring cell k free; order up-left, up, up-right, right, down-right, down, down-left, left; odd = orthogonal):
    /// the free orthogonal neighbours all lie in one run of free ring cells, so removing the centre cannot disconnect them.</summary>
    static bool[] BuildRingNsp()
    {
        var t = new bool[256];
        for (int mask = 0; mask < 256; mask++)
        {
            int start = -1;
            for (int k = 0; k < 8; k++) if ((mask >> k & 1) == 0) { start = k; break; }
            int runs = 0; bool orth = false;
            if (start < 0) runs = 1;
            else
                for (int j = 1; j <= 8; j++)
                {
                    int k = (start + j) & 7;
                    if ((mask >> k & 1) != 0) { if ((k & 1) != 0 && !orth) { orth = true; runs++; } }
                    else orth = false;
                }
            t[mask] = runs <= 1;
        }
        return t;
    }

    int mCompsF, mSplitSeen, mPiecesUsed; bool mArtDone;
    const int PiecesLimit = 6;   // separator candidates per box search served by local floods before the Tarjan pass takes over
    public static bool PiecesOff = Environment.GetEnvironmentVariable("SOKSOLV_PIECES_OFF") == "1";   // test hook   // per box search: components of the free floor (searched box lifted), and whether MArtic ran
    public static bool RegionRingOff = Environment.GetEnvironmentVariable("SOKSOLV_REGRING_OFF") == "1";   // test hook

    /// <summary>
    /// Number of empty-floor regions of the child (t occupied, the searched box lifted) for the 1st, 2nd, ... alive candidate of a box search.
    /// A candidate whose 8-ring shows it cannot split its component removes 0 or 1 pieces (an isolated cell vanishes): the child's count follows from the
    /// component count of the floor without t, learned from an earlier such candidate; only separator cells need the Tarjan pass (<see cref="MArtic"/>).
    /// </summary>
    int MChildRegionCount(int t, int nth)
    {
        if (!RegionRingOff)
        {
            int w = W; byte* bk = blk;
            int mask = (bk[t - w - 1] == 0 ? 1 : 0) | (bk[t - w] == 0 ? 2 : 0) | (bk[t - w + 1] == 0 ? 4 : 0) | (bk[t + 1] == 0 ? 8 : 0)
                | (bk[t + w + 1] == 0 ? 16 : 0) | (bk[t + w] == 0 ? 32 : 0) | (bk[t + w - 1] == 0 ? 64 : 0) | (bk[t - 1] == 0 ? 128 : 0);
            if (RingNsp[mask])
            {
                int pieces = (mask & 0xAA) != 0 ? 1 : 0;
                if (mCompsF >= 0) return mCompsF - 1 + pieces;
                int r0 = Regions();
                mCompsF = r0 + 1 - pieces;
                return r0;
            }
        }
        // separator candidate. Node mode: the child's count is (components of the floor without t, learned from an earlier candidate) - 1 + pieces(t), and
        // pieces(t) is a local row flood (a handful of floods per box search) instead of the whole-board Tarjan pass.
        if (fbits && !RegionRingOff && !PiecesOff && !mArtDone && mPiecesUsed < PiecesLimit)
        {
            if (mCompsF < 0 && mSplitSeen == 0) { mSplitSeen = 1; return Regions(); }   // a lone separator candidate costs just one flood
            mPiecesUsed++;
            int pc = MPieces(t);
            if (mCompsF >= 0) return mCompsF - 1 + pc;
            int r1 = Regions();
            mCompsF = r1 + 1 - pc;
            return r1;
        }
        if (!mArtDone && mSplitSeen++ < 2) return Regions();   // a lone separator candidate is cheaper by one flood than a whole-board Tarjan pass
        if (!mArtDone) { SetOcc(t, false); MArtic(); SetOcc(t, true); mArtDone = true; mCompsF = artComps; }
        return MChildRegions(t);
    }

    /// <summary>Number of connected groups among the free neighbours of t (t occupied in <c>blk</c>), i.e. the pieces its component falls apart into when t is
    /// blocked: row floods over the free-row mirror <c>frR</c> from the neighbours (the last group needs no flood). Needs <c>fbits</c>.</summary>
    int MPieces(int t)
    {
        int w = W;
        ulong* F = frR; ulong* g = frReg;
        int* nb = stackalloc int[4]; int cnt = 0;
        for (int k = 0; k < 4; k++) { int n = t + dirs[k]; if (blk[n] == 0) nb[cnt++] = n; }
        int groups = 0, lo = frRows, hi = -1;
        for (int i = 0; i < cnt; i++)
        {
            int n = nb[i]; int r = frRow[n]; ulong bit = 1UL << frCol[n];
            if ((g[r] & bit) != 0) continue;
            groups++;
            if (i == cnt - 1) break;
            int l = r, h = r;
            RowK.Flood(F, g, r, bit, ref l, ref h);
            if (l < lo) lo = l; if (h > hi) hi = h;
        }
        for (int r = lo; r <= hi; r++) g[r] = 0;
        return groups;
    }
}
