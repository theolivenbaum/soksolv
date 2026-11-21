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
    const int PfZoneDeadLimit = 1000; // Go PFZoneDead
    ulong mRng;

    void PfInit()
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
        if (pt.Dist >= 0) pt = pt with { Dist = pff!.SpreadSum(boxes) }; // festXStyle: spread
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
            PfInit();
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

    // ---- start-zone / reach filtering of the roots (Go pfReachInit, liveZoneRoots) -----------------------------------------------------
    /// <summary>Go pfReachInit: intersect the start-square matching with the exact (player-aware) single-box reach of hubReach.</summary>
    void PfReachInit()
    {
        if (goalAdj.Length == 0) InitMatching();
        var r = HubReach.Create(wall, W, goals, realPlayer);
        if (r == null || r.Rb.Length != goals.Length) return;
        for (int g = 0; g < goals.Length; g++)
        {
            var rb = r.Rb[g];
            for (int c = 0; c < cells; c++)
                if ((rb[c >> 6] & 1UL << (c & 63)) == 0) goalAdj[c * gw + (g >> 6)] &= ~(1UL << (g & 63));
        }
    }

    int[] LiveZoneRoots(int[] roots)
    {
        var start = (int[])lv.Boxes.Clone();
        var live = new List<int>();
        foreach (int r in roots)
            if (!PullZoneDead(wall, W, goal, dist, start, r, PfZoneDeadLimit)) live.Add(r);
        return live.Count == 0 ? roots : [.. live];
    }

    /// <summary>Go pullZoneDead: can the pull view's position provably never put every box on a start square (bounded relaxed pull search with wobbler deletion)?</summary>
    static bool PullZoneDead(bool[] wall, int w, bool[] start, int[] dist, int[] boxes, int player, int limit)
    {
        int cells = wall.Length;
        int[] dirs = [-w, w, -1, 1];
        var occ = new bool[cells];
        var mark = new int[cells];
        int stamp = 0;
        var q = new List<int>();
        int Reach(int p)
        {
            stamp++;
            mark[p] = stamp;
            q.Clear(); q.Add(p);
            int lo = p;
            for (int i = 0; i < q.Count; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (!wall[y] && !occ[y] && mark[y] != stamp) { mark[y] = stamp; q.Add(y); lo = Math.Min(lo, y); }
                }
            return lo;
        }
        bool OnStart(int[] bx) { foreach (int b in bx) if (!start[b]) return false; return true; }
        var seen = new HashSet<string>();
        string Enc(int[] bx, int p)
        {
            var ch = new char[(bx.Length + 1) * 2];
            for (int i = 0; i < bx.Length; i++) { ch[2 * i] = (char)(bx[i] & 0xffff); ch[2 * i + 1] = (char)(bx[i] >>> 16); }
            ch[2 * bx.Length] = (char)(p & 0xffff); ch[2 * bx.Length + 1] = (char)(p >>> 16);
            return new string(ch);
        }
        var fifo = new List<(int[] boxes, int player)>();
        bool Push(int[] bx, int p)
        {
            string k = Enc(bx, p);
            if (seen.Contains(k)) return false;
            if (OnStart(bx)) return true;
            seen.Add(k);
            fifo.Add((bx, p));
            return false;
        }
        var root = (int[])boxes.Clone();
        Array.Sort(root);
        foreach (int b in root) if (dist[b] < 0) return true; // a box already on a dead square
        foreach (int b in root) occ[b] = true;
        int rp = Reach(player);
        foreach (int b in root) occ[b] = false;
        if (Push(root, rp)) return false;
        for (int head = 0; head < fifo.Count; head++)
        {
            if (seen.Count > limit) return false; // not proven within the limit
            var cur = fifo[head];
            foreach (int b in cur.boxes) occ[b] = true;
            Reach(cur.player);
            int region = stamp;
            var pulls = new List<(int i, int to, int np)>();
            int wobbler = -1;
            for (int i = 0; i < cur.boxes.Length; i++)
            {
                int b = cur.boxes[i];
                foreach (int d in dirs)
                {
                    int p1 = b + d, p2 = b + 2 * d;
                    if (mark[p1] != region || wall[p2] || occ[p2] || dist[p1] < 0) continue;
                    occ[b] = false; occ[p1] = true;
                    int np = Reach(p2);
                    bool back = mark[b] == stamp && !wall[b - d] && !occ[b - d]; // the player can reach b and pull the box back onto it
                    occ[p1] = false; occ[b] = true;
                    Reach(cur.player); // restore the parent's region marks
                    region = stamp;
                    if (back) { wobbler = i; break; }
                    pulls.Add((i, p1, np));
                }
                if (wobbler >= 0) break;
            }
            if (wobbler >= 0)
            {
                var bx = cur.boxes.Where((_, idx) => idx != wobbler).ToArray();
                occ[cur.boxes[wobbler]] = false;
                int np = Reach(cur.player);
                foreach (int b in cur.boxes) occ[b] = false;
                if (Push(bx, np)) return false;
                continue;
            }
            foreach (int b in cur.boxes) occ[b] = false;
            foreach (var pl in pulls)
            {
                var bx = (int[])cur.boxes.Clone();
                bx[pl.i] = pl.to;
                Array.Sort(bx);
                if (Push(bx, pl.np)) return false;
            }
        }
        return true;
    }

    /// <summary>Go heap.Remove on a fessHeap (random removal for type exploration).</summary>
    // (implemented in FessHeap.Remove)

    /// <summary>Go selectMove's type exploration (exp-279, pfOn only): every PfTypes-th selection takes a random move of a random non-empty cell.</summary>
    bool MSelectType(out FessEntry result)
    {
        for (int rep = 0; rep < 8; rep++)
        {
            int cell = (int)(Rnd() % (ulong)mqueues.Count);
            var q = mqueues[cell];
            if (q.Count == 0) continue;
            var e = q.Remove((int)(Rnd() % (ulong)q.Count));
            if (e.Version == maux[e.Node].Version && (uint)cell == maux[e.Node].Bucket && (mmv[e.Move] & MMoveUsed) == 0) { result = e; return true; }
        }
        result = default; return false;
    }
}
