using Soksolv.Search.Hub;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

/// <summary>
/// Festival-ordered pull search (Go pullfest.go + the pfOn hooks in solver.go / fess_moves.go / advisors.go / fess_search.go):
/// the backward solver of the hub lanes (festXStyle: spread distance key, type exploration every 8th selection).
/// Pull states rank by fewer floor regions, fewer broken room pairs, then (one region) fewer unpullable boxes, then the largest
/// matching distance back to the real goals. One advisor (the best child better than its parent) costs 0, every other move 1.
/// Frontier cells are (regions, rooms) of the best score on the path; the best-so-far node compares those scores.
/// </summary>
public sealed unsafe partial class FullSolver
{
    PfFest? pff;
    readonly ChunkList<PfFest.Tuple> pfOwn = new(), pfBest = new();
    PfFest.Tuple pfNext; bool pfHave;
    const int PfTypes = 8;            // Go PFTypes: one selection in this many is type-based exploration
    ulong mRng;

    void PfOnInit()
    {
        if (pff != null) return;
        var g = (int[])lv.Boxes.Clone(); // the pull view's boxes are the real goals
        Array.Sort(g);
        pff = new PfFest(wall, W, g, true);
        hu = new int[n + 1]; hv = new int[n + 1]; hp = new int[n + 1]; hway = new int[n + 1]; hminv = new int[n + 1]; hused = new bool[n + 1];
        hfBoxes = new int[n];
        tgtDist = pff.PfDist; // matchingDistances(): the pull lane's matching to the real goals
    }

    ulong Rnd()
    {
        if (mRng == 0) mRng = 0x9e3779b97f4a7c15UL;
        mRng ^= mRng << 13; mRng ^= mRng >> 7; mRng ^= mRng << 17;
        return mRng;
    }

    /// <summary>Go pfFill: rooms (Festival's score_rooms), spread key, and for one region the hotspot count (occ holds the board).</summary>
    void PfFill(ref PfFest.Tuple pt, ReadOnlySpan<int> boxes)
    {
        pt = pt with { Rooms = FestRooms() };
        if (opt.PfSpread && pt.Dist >= 0) pt = pt with { Dist = pff!.SpreadSum(boxes) }; // festXStyle: spread
        if (pt.Reg != 1) return;
        pt = pt with { Hot = pff!.Hot(boxes, occ) };
    }

    static int PfOop(in PfFest.Tuple t) => t.Reg;

    /// <summary>Go pfAdd: a new pull node's tuple (a root scored on its own board, others the move's stored score) and its frontier cell.</summary>
    int PfAdd(ReadOnlySpan<int> boxes, int parent, int reg)
    {
        PfFest.Tuple own;
        if (pfHave) own = pfNext;
        else
        {
            PfOnInit();
            int d = MatchDist(boxes);
            own = new PfFest.Tuple(reg, MRoomConnectivity(), 0, d >= AssignmentInf ? -1 : d, 0);
            PfFill(ref own, boxes);
        }
        var best = own;
        if (parent >= 0 && !PfFest.Tuple.Better(own, pfBest[parent])) best = pfBest[parent];
        pfOwn.Add(own); pfBest.Add(best);
        return Project(new FeatureCell(0, best.Rooms, 0, best.Reg), -1);
    }

    int PfReparent(int id, int parent)
    {
        var best = pfOwn[id];
        if (parent >= 0 && !PfFest.Tuple.Better(best, pfBest[parent])) best = pfBest[parent];
        pfBest[id] = best;
        return Project(new FeatureCell(0, best.Rooms, 0, best.Reg), -1);
    }

    // ---- Festival's score_rooms (Go festRooms / festRoomsFlood) ---------------------------------------------------------------------
    uint[]? frCovered, frTouched; uint frCoverStamp, frTouchStamp; bool[]? frLost;

    int FestRooms()
    {
        if (!HasRooms || rmRooms.Length < 2) return 0;
        int nr = rmRooms.Length, nw = rmNw;
        if (frLost == null || frLost.Length != nr * nr) frLost = new bool[nr * nr];
        frCovered ??= new uint[cells]; frTouched ??= new uint[nr];
        var lost = frLost; Array.Clear(lost);
        if (++frCoverStamp == 0) { Array.Clear(frCovered); frCoverStamp = 1; }
        uint cs = frCoverStamp;
        for (int r = 0; r < nr; r++)
        {
            var rc = rmRooms[r];
            bool any = false;
            foreach (int c in rc)
            {
                if (occ[c] || frCovered[c] == cs) continue;
                any = true;
                if (++frTouchStamp == 0) { Array.Clear(frTouched); frTouchStamp = 1; }
                uint ts = frTouchStamp;
                RoomWalkTouch(r, c, frTouched, ts);
                foreach (int x in rc) if (rmSeen[x] == rmStamp) frCovered[x] = cs;
                for (int o = 0; o < nr; o++)
                    if ((rmEdgeBits[r * nw + (o >> 6)] >> (o & 63) & 1) != 0 && frTouched[o] != ts) lost[r * nr + o] = true;
            }
            if (!any) for (int o = 0; o < nr; o++) lost[r * nr + o] = true;
        }
        int k = 0;
        for (int r = 0; r < nr; r++)
            for (int o = r + 1; o < nr; o++)
                if (((rmEdgeBits[r * nw + (o >> 6)] >> (o & 63) & 1) != 0 || (rmEdgeBits[o * nw + (r >> 6)] >> (r & 63) & 1) != 0) && (lost[r * nr + o] || lost[o * nr + r])) k++;
        return k;
    }

    /// <summary>Go roomMap.walk (PFRoomsOcc): flood the empty cells from start, touching every other room met (a box on its boundary still links).</summary>
    void RoomWalkTouch(int room, int start, uint[] touched, uint ts)
    {
        if (++rmStamp == 0) { Array.Clear(rmSeen); rmStamp = 1; }
        uint st = rmStamp;
        int tail = 0; rmQueue[tail++] = start; rmSeen[start] = st;
        for (int i = 0; i < tail; i++)
            foreach (int d in dirs)
            {
                int x = rmQueue[i] + d;
                if (!wall[x] && occ[x])
                {
                    int r0 = rmCell[x];
                    if (r0 >= 0 && r0 != room) touched[r0] = ts;
                    continue;
                }
                if (wall[x] || occ[x] || rmSeen[x] == st) continue;
                rmSeen[x] = st;
                int r = rmCell[x];
                if (r >= 0 && r != room) { touched[r] = ts; continue; }
                rmQueue[tail++] = x;
            }
    }

    // ---- inefficient pulls (Go pfInefficient, Festival's pull-mode is_inefficient_position) ----------------------------------------------
    bool PfInefficient(int t, int from)
    {
        if (goal[t]) return false; // start squares are exempt
        if (FestTunnels()[t]) return true;
        int w = W;
        Span<int> d8 = [-w, -w + 1, 1, w + 1, w, w - 1, -1, -w - 1];
        for (int j = 0; j < 8; j += 2)
        {
            int f = t + d8[j];
            if (wall[f] || occ[f] && f != from) continue;
            int f2 = f + d8[j];
            if (f2 < 0 || f2 >= cells || !wall[f2]) continue;
            if (wall[t + d8[(j + 1) & 7]] && wall[t + d8[(j + 3) & 7]] && wall[t + d8[(j + 4) & 7]]) return true;
            if (wall[t + d8[(j + 4) & 7]] && wall[t + d8[(j + 5) & 7]] && wall[t + d8[(j + 7) & 7]]) return true;
        }
        return false;
    }

    /// <summary>Go heap.Remove on a fessHeap (random removal for type exploration).</summary>
    // (implemented in FessHeap.Remove)

    /// <summary>Go selectMove's type exploration (exp-279, pfOn only): every PfTypes-th selection takes a random move of a random non-empty cell. Returns 1 (found), 0 (none), -1 (slice expired while reopening a window).</summary>
    int MSelectType(out FessEntry result)
    {
        for (int rep = 0; rep < 8; rep++)
        {
            int cell = (int)(Rnd() % (ulong)mqueues.Count);
            var q = mqueues[cell];
            if (q.Count == 0) continue;
            var e = q.Remove((int)(Rnd() % (ulong)q.Count));
            if (e.Version == maux[e.Node].Version && (uint)cell == maux[e.Node].Bucket)
            {
                if (e.Move < 0) { if (OpenFrontierEntry(e)) { result = default; return -1; } continue; }
                if ((mmv[e.Move] & MMoveUsed) == 0) { result = e; return 1; }
            }
        }
        result = default; return 0;
    }
}
