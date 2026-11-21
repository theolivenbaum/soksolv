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
        for (int r = 0; r < bbH; r++)
        {
            ulong m = 0;
            int baseI = r * W;
            for (int x = 0; x < W; x++) if (occ[baseI + x]) m |= 1UL << x;
            bbNFreeP[r + 1] = bbAll & ~bbWallP[r + 1] & ~m;
        }
    }

    /// <summary>Row-bitset flood: stamps m3 with ps for every cell reachable from pc with nc blocked; returns the minimum cell.</summary>
    int BbFlood(int pc, int nc, uint ps)
    {
        int w = W;
        int rn = nc / w + 1, rp = pc / w + 1, cp = pc % w;
        ulong s1 = bbNFreeP[rn];
        bbNFreeP[rn] &= ~(1UL << (nc % w));
        ulong s2 = bbNFreeP[rp];
        bbNFreeP[rp] |= 1UL << cp;
        FloodRows(bbNFreeP, bbNRegP, bbH + 2, rp, cp);
        bbNFreeP[rp] = s2; bbNFreeP[rn] = s1;
        int minC = -1; bbRegLo = bbH + 2; bbRegHi = -1;
        for (int r = 1; r <= bbH; r++)
        {
            ulong x = bbNRegP[r];
            if (x == 0) continue;
            if (minC < 0) bbRegLo = r;
            bbRegHi = r;
            int baseC = (r - 1) * w;
            if (minC < 0) minC = baseC + BitOperations.TrailingZeroCount(x);
            for (; x != 0; x &= x - 1) m3P[baseC + BitOperations.TrailingZeroCount(x)] = ps;
        }
        return minC;
    }

    void BbBuildParent(uint st)
    {
        for (int r = 0; r < bbH; r++)
        {
            ulong m = 0;
            int baseI = r * W;
            for (int x = 0; x < W; x++) if (mark[baseI + x] == st) m |= 1UL << x;
            bbParP[r + 1] = m;
        }
        bbParentBuilt = true;
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    ulong RowAt(ulong* rows, int i) => (uint)i >= (uint)(bbH + 2) ? 0 : rows[i];
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong AlignX(ulong v, int dx) => dx >= 0 ? v >> dx : v << -dx;

    /// <summary>Go moveAccessBits: every term is a row-bitset expression over the rows holding boxes.</summary>
    (int mobility, bool explorer, int accStart, int accLen) MMoveAccessBits(ReadOnlySpan<int> cur, int moved, int target)
    {
        if (!bbParentBuilt) BbBuildParent(mOpenSt);
        int origB = cur[moved];
        bool recordAcc = AccessNovelty && NoveltyBudget() > 0;
        int accStart = recordAcc ? mAccN : 0;
        bool vacated = (mCmdMask | mUnpinMask) != 0 || (goal[origB] && !goal[target]);
        bool displaced = recordAcc && (mUnpackedRegime || vacated);
        bool goalVacated = recordAcc && vacated;
        int mobility = 0; bool explorer = false;
        int w = W;
        int tr = target / w; ulong tbit = 1UL << (target % w);
        ulong* free = bbNFreeP, wallR = bbWallP, reg = bbNRegP, par = bbParP, alive = bbAliveP;
        // only box rows next to the region's rows can have a supporting cell in it
        for (int r = Math.Max(0, bbRegLo - 2), rEnd = Math.Min(bbH - 1, bbRegHi); r <= rEnd; r++)
        {
            ulong boxes = bbAll & ~free[r + 1] & ~wallR[r + 1];
            ulong tm = 0;
            if (r == tr) { boxes |= tbit; tm = tbit; }
            if (boxes == 0) continue;
            for (int k = 0; k < 4; k++)
            {
                ulong reach = AlignX(RowAt(reg, r + gSupR[k] + 1), gSupX[k]);
                ulong sup = boxes & reach;
                if (sup == 0) continue;
                mobility += BitOperations.PopCount(sup);
                if (explorer && !recordAcc) continue; // legality only matters for the first explorer or for recorded facts
                ulong fresh = sup & ~AlignX(RowAt(par, r + gSupR[k] + 1), gSupX[k]);
                if (fresh == 0 && !goalVacated) continue;
                int fr = r + gFreeR[k];
                ulong freeRow = RowAt(free, fr + 1) & ~(fr == tr ? tbit : 0UL);
                ulong legal = sup & AlignX(freeRow, gFreeX[k]) & AlignX(RowAt(alive, r + gLandR[k] + 1), gLandX[k]);
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
}
