using System.Numerics;
using System.Runtime.CompilerServices;
using Soksolv.Search.Hub;

namespace Soksolv.Search;

/// <summary>
/// Word-parallel, incremental Festival score_rooms (<see cref="FestRooms"/>) for boards at most 64 wide and 62 tall with a walled ring.
/// Per room r: the free cells of r are the starts; the walk from a start crosses free cells of r and room-less cells (rmCell &lt; 0) only
/// (a bit-row flood, rows indexed y+1), and touches every other room that owns a cell 4-adjacent to the walked set (free or occupied).
/// A neighbouring room not touched by some walked component of r is "lost" for the pair (bit set L[r]); the score is the number of adjacent
/// room pairs lost in either direction. Room r only depends on the occupancy of D_r (the cells its walk can reach plus their neighbours),
/// so a state that differs from the last fully evaluated one (the expanded parent) in one box is evaluated by re-walking just the rooms
/// whose D_r holds the box's old or new cell. Exactly equal to the queue version (<c>SOKSOLV_CHECK_FESTROOMS=1</c> verifies every call).
/// </summary>
public sealed unsafe partial class FullSolver
{
    static readonly bool CheckFestRooms = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_FESTROOMS") == "1";
    bool frbInit, frbOk;
    int frbRows, frbNr, frbNw;
    ulong frbMask;
    ulong[] frbWall = [], frbN = [], frbRoom = [], frbSE = [], frbEdge = [], frbScr = [], frbL = [], frbM = [], frbUpper = [], frbAff = [], frbBaseL = [];
    int[] frbLo = [], frbHi = [], frbBase = [];
    int frbBaseK = -1;
    ulong* pWall, pN, pRoom, pSE, pEdge, pScr, pL, pM, pUp, pAff, pBaseL;
    int* pLo, pHi;
    internal long FrbFull, FrbInc, FrbRooms;

    void FrbPrepare()
    {
        frbInit = true;
        int nr = rmRooms.Length;
        if (W > 64 || cells % W != 0 || cells / W > 62 || nr < 2) return;
        int h = cells / W;
        for (int x = 0; x < W; x++) if (!wall[x] || !wall[(h - 1) * W + x]) return;
        for (int y = 0; y < h; y++) if (!wall[y * W] || !wall[y * W + W - 1]) return;
        int rows = h + 2, nw = rmNw;
        frbRows = rows; frbNr = nr; frbNw = nw; frbMask = W == 64 ? ~0UL : (1UL << W) - 1;
        frbWall = GC.AllocateArray<ulong>(rows, pinned: true); frbN = GC.AllocateArray<ulong>(rows, pinned: true);
        frbRoom = GC.AllocateArray<ulong>(nr * rows, pinned: true);
        frbLo = GC.AllocateArray<int>(nr, pinned: true); frbHi = GC.AllocateArray<int>(nr, pinned: true);
        for (int r = 0; r < nr; r++) { frbLo[r] = rows; frbHi[r] = -1; }
        for (int c = 0; c < cells; c++)
        {
            int y = c / W + 1; ulong b = 1UL << (c % W);
            if (wall[c]) { frbWall[y] |= b; continue; }
            int rc = rmCell[c];
            if (rc < 0) frbN[y] |= b;
            else { frbRoom[rc * rows + y] |= b; if (y < frbLo[rc]) frbLo[rc] = y; if (y > frbHi[rc]) frbHi[rc] = y; }
        }
        frbEdge = GC.AllocateArray<ulong>(nr * nw, pinned: true); frbSE = GC.AllocateArray<ulong>(nr * nw, pinned: true);
        for (int r = 0; r < nr; r++)
            for (int w2 = 0; w2 < nw; w2++) frbEdge[r * nw + w2] = rmEdgeBits[r * nw + w2];
        for (int r = 0; r < nr; r++)
            for (int o = 0; o < nr; o++)
                if ((frbEdge[r * nw + (o >> 6)] >> (o & 63) & 1) != 0) { frbSE[r * nw + (o >> 6)] |= 1UL << (o & 63); frbSE[o * nw + (r >> 6)] |= 1UL << (r & 63); }
        frbUpper = GC.AllocateArray<ulong>(nr * nw, pinned: true);
        for (int r = 0; r < nr; r++)
            for (int o = r + 1; o < nr; o++) frbUpper[r * nw + (o >> 6)] |= 1UL << (o & 63);
        // D_r per cell: the rooms whose walk (R_r plus the room-less cells reachable from it, other rooms' cells block) or its 4-neighbours holds the cell
        frbAff = GC.AllocateArray<ulong>(cells * nw, pinned: true);
        var seen = new int[cells]; var q = new int[cells];
        for (int r = 0; r < nr; r++)
        {
            int tail = 0, stamp = r + 1;
            foreach (int c in rmRooms[r]) { seen[c] = stamp; q[tail++] = c; }
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int x = q[i] + d;
                    if (wall[x] || seen[x] == stamp || rmCell[x] >= 0 && rmCell[x] != r) continue;
                    seen[x] = stamp; q[tail++] = x;
                }
            for (int i = 0; i < tail; i++)
            {
                int c = q[i];
                frbAff[c * nw + (r >> 6)] |= 1UL << (r & 63);
                foreach (int d in dirs) if (!wall[c + d]) frbAff[(c + d) * nw + (r >> 6)] |= 1UL << (r & 63);
            }
        }
        frbScr = GC.AllocateArray<ulong>(rows * 6 + nw + 8, pinned: true);
        frbL = GC.AllocateArray<ulong>(nr * nw, pinned: true); frbM = GC.AllocateArray<ulong>(nr * nw, pinned: true); frbBaseL = GC.AllocateArray<ulong>(nr * nw, pinned: true);
        frbBase = new int[n];
        pWall = (ulong*)Unsafe.AsPointer(ref frbWall[0]); pN = (ulong*)Unsafe.AsPointer(ref frbN[0]); pRoom = (ulong*)Unsafe.AsPointer(ref frbRoom[0]);
        pSE = (ulong*)Unsafe.AsPointer(ref frbSE[0]); pEdge = (ulong*)Unsafe.AsPointer(ref frbEdge[0]); pScr = (ulong*)Unsafe.AsPointer(ref frbScr[0]);
        pL = (ulong*)Unsafe.AsPointer(ref frbL[0]); pM = (ulong*)Unsafe.AsPointer(ref frbM[0]); pUp = (ulong*)Unsafe.AsPointer(ref frbUpper[0]);
        pAff = (ulong*)Unsafe.AsPointer(ref frbAff[0]); pBaseL = (ulong*)Unsafe.AsPointer(ref frbBaseL[0]);
        pLo = (int*)Unsafe.AsPointer(ref frbLo[0]); pHi = (int*)Unsafe.AsPointer(ref frbHi[0]);
        frbOk = true;
    }

    /// <summary>Score_rooms of the board holding exactly <paramref name="boxes"/>; incremental against the last fully evaluated board.</summary>
    int FestRoomsBits(ReadOnlySpan<int> boxes)
    {
        int nb = boxes.Length;
        if (nb == frbBase.Length && frbBaseK >= 0)
        {
            int diff = -1, nd = 0;
            for (int j = 0; j < nb; j++)
                if (boxes[j] != frbBase[j]) { diff = j; if (++nd > 1) break; }
            if (nd == 0) return frbBaseK;
            if (nd == 1)
            {
                FrbInc++;
                return FrbIncremental(boxes, frbBase[diff], boxes[diff]);
            }
        }
        FrbFull++;
        for (int i = 0; i < frbNr * frbNw; i++) pL[i] = 0;
        FrbRows(boxes);
        for (int r = 0; r < frbNr; r++) FrbRoom(r);
        int k = FrbCount();
        if (frbBase.Length != nb) frbBase = new int[nb];
        boxes.CopyTo(frbBase);
        for (int i = 0; i < frbNr * frbNw; i++) pBaseL[i] = pL[i];
        frbBaseK = k;
        return k;
    }

    int FrbIncremental(ReadOnlySpan<int> boxes, int from, int to)
    {
        int nr = frbNr, nw = frbNw;
        for (int i = 0; i < nr * nw; i++) pL[i] = pBaseL[i];
        FrbRows(boxes);
        ulong* af = pAff + from * nw, at = pAff + to * nw;
        for (int w2 = 0; w2 < nw; w2++)
        {
            ulong m = af[w2] | at[w2];
            while (m != 0)
            {
                int r = (w2 << 6) + BitOperations.TrailingZeroCount(m); m &= m - 1;
                FrbRooms++;
                for (int x = 0; x < nw; x++) pL[r * nw + x] = 0;
                FrbRoom(r);
            }
        }
        return FrbCount();
    }

    /// <summary>occ / free / fn rows of the board (scratch rows 0..2).</summary>
    void FrbRows(ReadOnlySpan<int> boxes)
    {
        int rows = frbRows, h = rows - 2;
        ulong* occ = pScr, free = pScr + rows, fn = pScr + 2 * rows;
        for (int i = 0; i < rows; i++) occ[i] = 0;
        foreach (int b in boxes) occ[frRow[b] + 1] |= 1UL << frCol[b];
        free[0] = 0; free[rows - 1] = 0; fn[0] = 0; fn[rows - 1] = 0;
        ulong mask = frbMask;
        for (int i = 1; i <= h; i++) { free[i] = ~(pWall[i] | occ[i]) & mask; fn[i] = free[i] & pN[i]; }
    }

    /// <summary>Walks room r (rows prepared by <see cref="FrbRows"/>) and sets its lost bits in L[r] (zeroed by the caller).</summary>
    void FrbRoom(int r)
    {
        int rows = frbRows, nw = frbNw, h = rows - 2;
        ulong* free = pScr + rows, fn = pScr + 2 * rows, frr = pScr + 3 * rows, todo = pScr + 4 * rows, reg = pScr + 5 * rows, tch = pScr + 6 * rows;
        ulong* rr = pRoom + r * rows;
        int rlo = pLo[r], rhi = pHi[r];
        ulong* lr = pL + r * nw;
        bool any = false;
        for (int i = rlo; i <= rhi; i++) { ulong f = free[i] & rr[i]; frr[i] = f; todo[i] = f; if (f != 0) any = true; }
        if (!any)
        {
            for (int w2 = 0; w2 < nw; w2++) lr[w2] = ~0UL;
            for (int i = rlo; i <= rhi; i++) { frr[i] = 0; todo[i] = 0; }
            return;
        }
        ulong valid = ((1UL << h) - 1) << 1;
        ulong* er = pEdge + r * nw;
        // frr / todo / reg are zero outside the room's rows and between components
        for (int s = rlo; s <= rhi; s++)
        {
            while (todo[s] != 0)
            {
                ulong seed = todo[s] & (0UL - todo[s]);
                ulong y0 = RowFlood.Fill(seed, fn[s] | frr[s]);
                reg[s] = y0;
                int lo = s, hi = s;
                ulong dirty = 0;
                if ((y0 & (fn[s - 1] | frr[s - 1])) != 0) dirty |= 1UL << (s - 1);
                if ((y0 & (fn[s + 1] | frr[s + 1])) != 0) dirty |= 1UL << (s + 1);
                dirty &= valid;
                while (dirty != 0)
                {
                    int i = BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
                    ulong al = fn[i] | frr[i];
                    ulong cur = reg[i];
                    ulong y = cur | (reg[i - 1] | reg[i + 1]) & al;
                    if (y == cur) continue;
                    y = RowFlood.Fill(y, al);
                    reg[i] = y;
                    ulong nwb = y & ~cur;
                    if (i < lo) lo = i; if (i > hi) hi = i;
                    if ((nwb & (fn[i - 1] | frr[i - 1]) & ~reg[i - 1]) != 0) dirty |= 1UL << (i - 1);
                    if ((nwb & (fn[i + 1] | frr[i + 1]) & ~reg[i + 1]) != 0) dirty |= 1UL << (i + 1);
                    dirty &= valid;
                }
                // rooms touched: neighbouring rooms owning a cell 4-adjacent to the walked set
                for (int w2 = 0; w2 < nw; w2++) tch[w2] = 0;
                for (int w2 = 0; w2 < nw; w2++)
                {
                    ulong e = er[w2];
                    while (e != 0)
                    {
                        int o = (w2 << 6) + BitOperations.TrailingZeroCount(e); e &= e - 1;
                        ulong* ro = pRoom + o * rows;
                        int a = Math.Max(lo - 1, pLo[o]), b = Math.Min(hi + 1, pHi[o]);
                        for (int i = a; i <= b; i++)
                        {
                            ulong d = reg[i] | reg[i] << 1 | reg[i] >> 1 | reg[i - 1] | reg[i + 1];
                            if ((d & ro[i]) != 0) { tch[o >> 6] |= 1UL << (o & 63); break; }
                        }
                    }
                }
                for (int w2 = 0; w2 < nw; w2++) lr[w2] |= er[w2] & ~tch[w2];
                for (int i = lo; i <= hi; i++) { todo[i] &= ~reg[i]; reg[i] = 0; }
            }
        }
        for (int i = rlo; i <= rhi; i++) { frr[i] = 0; todo[i] = 0; }
    }

    /// <summary>Pairs (r &lt; o) adjacent in either direction and lost in either direction.</summary>
    int FrbCount()
    {
        int nr = frbNr, nw = frbNw;
        for (int i = 0; i < nr * nw; i++) pM[i] = 0;
        for (int r = 0; r < nr; r++)
        {
            ulong* lr = pL + r * nw;
            ulong* ser = pSE + r * nw;
            for (int w2 = 0; w2 < nw; w2++)
            {
                ulong b = lr[w2] & ser[w2];
                while (b != 0)
                {
                    int o = (w2 << 6) + BitOperations.TrailingZeroCount(b); b &= b - 1;
                    pM[o * nw + (r >> 6)] |= 1UL << (r & 63);
                    pM[r * nw + w2] |= 1UL << (o & 63);
                }
            }
        }
        int k = 0;
        for (int i = 0; i < nr * nw; i++) k += BitOperations.PopCount(pM[i] & pUp[i]);
        return k;
    }
}
