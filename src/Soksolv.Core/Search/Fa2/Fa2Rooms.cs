using System.Numerics;

namespace Soksolv.Search;

/// <summary>
/// fa2 room-pair score (Go rooms_fx.go festRoomsFull = Festival's score_rooms): one point per broken unordered baseline room
/// pair. Go's fa2rooms.go computes the same number incrementally per child (and checks it against the full recompute under
/// -fa2-rooms-check); here every candidate is recomputed in full (exact, slower; the incremental scheme is a later speed step).
/// </summary>
public sealed unsafe partial class FullSolver
{
    // Incremental room score (Go fa2rooms.go, simplified): room r's evaluation yields its own row P_r (the rooms whose pair
    // with r it breaks). The parent keeps every row; a child re-evaluates only the rooms whose reads can change (the rooms of
    // b and t, rooms adjacent to either cell, and the rooms touched by the corridor components that split or merge) and
    // takes every other row from the parent. Score = unordered pairs {r,o} with o in P_r or r in P_o.
    ulong[] fa2PRow = [], fa2CRow = [], fa2Sym = [], fa2Sm = [];
    int[] fa2PLab = [], fa2CLab = []; ulong[] fa2PTouch = [], fa2CTouch = [];
    int fa2RoomsFull;       // the parent's score
    bool Fa2RoomsOn => opt.Fa2.Rooms && rmRooms.Length >= 2;

    /// <summary>labelCorridors with the boundary rule: a room cell next to the corridor component is touched whether or not it holds a box.</summary>
    void LabelCorridorsFX(int[] lab, ulong[] touch)
    {
        int nw = rmNw;
        foreach (int c in rmCorr) lab[c] = -1;
        int k = 0;
        foreach (int c in rmCorr)
        {
            if (occ[c] || lab[c] >= 0) continue;
            int tb = k * nw;
            for (int w = 0; w < nw; w++) touch[tb + w] = 0;
            lab[c] = k;
            int sp = 0; rmStack[sp++] = c;
            while (sp > 0)
            {
                int x = rmStack[--sp];
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (wall[y]) continue;
                    int cy = rmCell[y];
                    if (cy >= 0) touch[tb + (cy >> 6)] |= 1UL << (cy & 63);
                    else if (!occ[y] && lab[y] < 0) { lab[y] = k; rmStack[sp++] = y; }
                }
            }
            k++;
        }
        rmK = k;
    }

    /// <summary>Writes into row r of dst the baseline pairs of room r that one of its free fragments no longer reaches (Go roomBrokenFXN).</summary>
    void RoomRowFX(int r, int[] lab, ulong[] touch, ulong[] dst)
    {
        int nw = rmNw;
        var cellsR = rmRooms[r]; int bse = rmRoomBase[r];
        int eb = r * nw;
        if (++rmRepStamp == 0) { Array.Clear(rmCompRepSt); rmRepStamp = 1; }
        uint rs = rmRepStamp;
        int free = 0;
        for (int w = 0; w < nw; w++) dst[eb + w] = 0;
        for (int i = 0; i < cellsR.Length; i++)
        {
            if (occ[cellsR[i]]) continue;
            free++;
            rmDsu[i] = i;
            for (int w = 0; w < nw; w++) rmFragTouch[i * nw + w] = 0;
            for (int e = rmFbStart[bse + i]; e < rmFbStart[bse + i + 1]; e++)
            {
                var (y, kd) = rmFbEdge[e];
                if (kd <= -2) { int o = -2 - kd; rmFragTouch[RmFind(i) * nw + (o >> 6)] |= 1UL << (o & 63); continue; } // another room: touched even when occupied
                if (occ[y]) continue;
                if (kd >= 0) { if (kd < i) RmUnion(i, kd); }
                else
                {
                    int k = lab[y];
                    int ri = RmFind(i) * nw;
                    for (int w = 0; w < nw; w++) rmFragTouch[ri + w] |= touch[k * nw + w];
                    if (rmCompRepSt[k] == rs) RmUnion(i, rmCompRep[k]);
                    else { rmCompRepSt[k] = rs; rmCompRep[k] = i; }
                }
            }
        }
        if (free == 0)
        {
            for (int w = 0; w < nw; w++) dst[eb + w] = rmEdgeBits[eb + w];
            return;
        }
        for (int i = 0; i < cellsR.Length; i++)
        {
            if (occ[cellsR[i]] || rmDsu[i] != i) continue;
            for (int w = 0; w < nw; w++) dst[eb + w] |= rmEdgeBits[eb + w] & ~rmFragTouch[i * nw + w];
        }
    }

    /// <summary>Unordered broken pairs of the per-room rows.</summary>
    int RoomsScore(ulong[] rows)
    {
        int nr = rmRooms.Length, nw = rmNw;
        var sym = fa2Sym;
        Array.Copy(rows, sym, nr * nw);
        for (int r = 0; r < nr; r++)
        {
            int rw = r >> 6; ulong rb = 1UL << (r & 63);
            for (int w = 0; w < nw; w++)
                for (ulong e = rows[r * nw + w]; e != 0; e &= e - 1) sym[(w << 6 | BitOperations.TrailingZeroCount(e)) * nw + rw] |= rb;
        }
        int k = 0;
        for (int i = 0, n = nr * nw; i < n; i++) k += BitOperations.PopCount(sym[i]);
        return k / 2;
    }

    void Fa2RoomsInit()
    {
        int nr = rmRooms.Length, nw = rmNw;
        if (fa2PRow.Length == nr * nw) return;
        fa2PRow = new ulong[nr * nw]; fa2CRow = new ulong[nr * nw]; fa2Sym = new ulong[nr * nw]; fa2Sm = new ulong[nw];
        fa2PLab = new int[cells]; fa2CLab = new int[cells];
        fa2PTouch = new ulong[rmTouch.Length]; fa2CTouch = new ulong[rmTouch.Length];
    }

    void Fa2RoomsParent()
    {
        if (!Fa2RoomsOn) return;
        Fa2RoomsInit();
        LabelCorridorsFX(fa2PLab, fa2PTouch);
        for (int r = 0; r < rmRooms.Length; r++) RoomRowFX(r, fa2PLab, fa2PTouch, fa2PRow);
        fa2RoomsFull = RoomsScore(fa2PRow);
    }

    /// <summary>The exp-483 room score of occ (Go festRoomsFull), scratch labels.</summary>
    int FestRoomsFull()
    {
        int nr = rmRooms.Length;
        if (nr < 2) return 0;
        Fa2RoomsInit();
        LabelCorridorsFX(fa2CLab, fa2CTouch);
        for (int r = 0; r < nr; r++) RoomRowFX(r, fa2CLab, fa2CTouch, fa2CRow);
        return RoomsScore(fa2CRow);
    }

    void SmAddRoomsAdjacent(int c)
    {
        foreach (int d in dirs) { int r = rmCell[c + d]; if (r >= 0) fa2Sm[r >> 6] |= 1UL << (r & 63); }
    }
    void SmAddTouch(int lab)
    {
        if (lab < 0) return;
        int nw = rmNw;
        for (int w = 0; w < nw; w++) fa2Sm[w] |= fa2PTouch[lab * nw + w];
    }

    /// <summary>Room score of the child that moves box b to t (occ is the child; the parent's rows and labels are in fa2P*).</summary>
    int Fa2RoomsChild(int b, int t)
    {
        if (!Fa2RoomsOn) return 0;
        if (b == t) return fa2RoomsFull;
        int nw = rmNw, nr = rmRooms.Length;
        var sm = fa2Sm;
        for (int w = 0; w < nw; w++) sm[w] = 0;
        int rb = rmCell[b], rt = rmCell[t];
        if (rb >= 0) sm[rb >> 6] |= 1UL << (rb & 63);
        if (rt >= 0) sm[rt >> 6] |= 1UL << (rt & 63);
        SmAddRoomsAdjacent(b); SmAddRoomsAdjacent(t);
        bool corr = rb < 0 || rt < 0;
        if (rt < 0) SmAddTouch(fa2PLab[t]);
        if (rb < 0) foreach (int d in dirs) { int y = b + d; if (!wall[y] && rmCell[y] < 0) SmAddTouch(fa2PLab[y]); }
        Array.Copy(fa2PRow, fa2CRow, nr * nw);
        int[] lab = fa2PLab; ulong[] touch = fa2PTouch;
        if (corr) { LabelCorridorsFX(fa2CLab, fa2CTouch); lab = fa2CLab; touch = fa2CTouch; }
        for (int w = 0; w < nw; w++)
            for (ulong e = sm[w]; e != 0; e &= e - 1) RoomRowFX(w << 6 | BitOperations.TrailingZeroCount(e), lab, touch, fa2CRow);
        return RoomsScore(fa2CRow);
    }
    int Fa2RoomsNow() => Fa2RoomsOn ? FestRoomsFull() : 0;
}
