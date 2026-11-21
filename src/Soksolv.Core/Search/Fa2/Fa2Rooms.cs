using System.Numerics;

namespace Soksolv.Search;

/// <summary>
/// fa2 room-pair score (Go rooms_fx.go festRoomsFull = Festival's score_rooms): one point per broken unordered baseline room
/// pair. Go's fa2rooms.go computes the same number incrementally per child (and checks it against the full recompute under
/// -fa2-rooms-check); here every candidate is recomputed in full (exact, slower; the incremental scheme is a later speed step).
/// </summary>
public sealed unsafe partial class FullSolver
{
    ulong[] fa2Lost = [];
    int fa2RoomsFull;       // the parent's score
    bool Fa2RoomsOn => opt.Fa2.Rooms && rmRooms.Length >= 2;

    /// <summary>labelCorridors with the boundary rule: a room cell next to the corridor component is touched whether or not it holds a box.</summary>
    void LabelCorridorsFX()
    {
        int nw = rmNw;
        foreach (int c in rmCorr) rmLab[c] = -1;
        int k = 0;
        foreach (int c in rmCorr)
        {
            if (occ[c] || rmLab[c] >= 0) continue;
            int tb = k * nw;
            for (int w = 0; w < nw; w++) rmTouch[tb + w] = 0;
            rmLab[c] = k;
            int sp = 0; rmStack[sp++] = c;
            while (sp > 0)
            {
                int x = rmStack[--sp];
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (wall[y]) continue;
                    int cy = rmCell[y];
                    if (cy >= 0) rmTouch[tb + (cy >> 6)] |= 1UL << (cy & 63);
                    else if (!occ[y] && rmLab[y] < 0) { rmLab[y] = k; rmStack[sp++] = y; }
                }
            }
            k++;
        }
        rmK = k;
    }

    /// <summary>Marks in fa2Lost the baseline pairs of room r that one of its free fragments no longer reaches (Go roomBrokenFXN).</summary>
    void RoomBrokenFX(int r)
    {
        int nw = rmNw;
        var cellsR = rmRooms[r]; int bse = rmRoomBase[r];
        int eb = r * nw;
        if (++rmRepStamp == 0) { Array.Clear(rmCompRepSt); rmRepStamp = 1; }
        uint rs = rmRepStamp;
        int free = 0;
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
                    int k = rmLab[y];
                    int ri = RmFind(i) * nw;
                    for (int w = 0; w < nw; w++) rmFragTouch[ri + w] |= rmTouch[k * nw + w];
                    if (rmCompRepSt[k] == rs) RmUnion(i, rmCompRep[k]);
                    else { rmCompRepSt[k] = rs; rmCompRep[k] = i; }
                }
            }
        }
        void Mark(int o)
        {
            fa2Lost[r * nw + (o >> 6)] |= 1UL << (o & 63);
            fa2Lost[o * nw + (r >> 6)] |= 1UL << (r & 63);
        }
        if (free == 0)
        {
            for (int w = 0; w < nw; w++)
                for (ulong e = rmEdgeBits[eb + w]; e != 0; e &= e - 1) Mark(w << 6 | BitOperations.TrailingZeroCount(e));
            return;
        }
        for (int i = 0; i < cellsR.Length; i++)
        {
            if (occ[cellsR[i]] || rmDsu[i] != i) continue;
            for (int w = 0; w < nw; w++)
                for (ulong miss = rmEdgeBits[eb + w] & ~rmFragTouch[i * nw + w]; miss != 0; miss &= miss - 1) Mark(w << 6 | BitOperations.TrailingZeroCount(miss));
        }
    }

    /// <summary>The exp-483 room score of occ (Go festRoomsFull).</summary>
    int FestRoomsFull()
    {
        int nr = rmRooms.Length;
        if (nr < 2) return 0;
        if (fa2Lost.Length != nr * rmNw) fa2Lost = new ulong[nr * rmNw];
        Array.Clear(fa2Lost);
        LabelCorridorsFX();
        for (int r = 0; r < nr; r++) RoomBrokenFX(r);
        int k = 0;
        foreach (ulong w in fa2Lost) k += BitOperations.PopCount(w);
        return k / 2;
    }

    int Fa2RoomsChild(int b, int t) => !Fa2RoomsOn ? 0 : b == t ? fa2RoomsFull : FestRoomsFull();
    void Fa2RoomsParent() { if (Fa2RoomsOn) fa2RoomsFull = FestRoomsFull(); }
    int Fa2RoomsNow() => Fa2RoomsOn ? FestRoomsFull() : 0;
}
