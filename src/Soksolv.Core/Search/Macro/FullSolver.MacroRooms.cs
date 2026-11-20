using System.Numerics;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

/// <summary>Stage 2 of the macro lane: room map (rooms.go), room connectivity, packing basin / out-of-plan (basin.go).</summary>
public sealed unsafe partial class FullSolver
{
    int MacroStage => opt.MacroStage;
    bool Feat(int bit) => (opt.MacroFeatures & (1 << bit)) != 0;
    int MoveWindow => Feat(0) ? 8 : 0;

    // ---- room map (Go roomMap) -------------------------------------------------------------------------------------------------
    int[] rmCell = [];                 // -1 none, -2 shared by rooms (corridor), r >= 0 room id
    int[][] rmRooms = [];              // cells of each room (ascending)
    int rmNw; ulong[] rmEdgeBits = []; // room r's direct neighbours, bit-packed
    int[] rmCorr = [];                 // corridor cells (cell < 0, not wall)
    int[] rmLoc = [];                  // local index of a room cell
    int[] rmLab = [];                  // corridor component label (cur)
    ulong[] rmTouch = [];              // per component: rooms with a free cell adjacent
    int rmK;
    int[] rmRoomBase = [], rmFbStart = []; (int cell, int kind)[] rmFbEdge = [];
    int[] rmCompRep = []; uint[] rmCompRepSt = []; uint rmRepStamp;
    int[] rmDsu = []; ulong[] rmFragTouch = [];
    uint[] rmSeen = []; uint rmStamp; int[] rmQueue = [], rmStack = [];

    bool HasRooms => MacroStage >= 2 && rmRooms.Length > 0;

    void MComputeRooms()
    {
        var parent = new int[cells]; Array.Fill(parent, -1);
        for (int y = 0; y + 1 < lv.H; y++)
            for (int x = 0; x + 1 < lv.W; x++)
            {
                int c = y * W + x;
                if (!wall[c] && !wall[c + 1] && !wall[c + W] && !wall[c + W + 1]) parent[c] = c;
            }
        int Root(int c) { while (parent[c] != c) { parent[c] = parent[parent[c]]; c = parent[c]; } return c; }
        for (int c = 0; c < parent.Length; c++)
            if (parent[c] >= 0)
                foreach (int d in (ReadOnlySpan<int>)[1, W])
                    if (c + d < parent.Length && parent[c + d] >= 0) parent[Root(c + d)] = Root(c);
        var count = new int[parent.Length];
        for (int c = 0; c < parent.Length; c++) if (parent[c] >= 0) count[Root(c)]++;
        rmCell = new int[cells]; Array.Fill(rmCell, -1);
        var ids = new Dictionary<int, int>(); var roomLists = new List<List<int>>();
        for (int c = 0; c < parent.Length; c++)
            if (parent[c] >= 0 && count[Root(c)] > 1)
            {
                int r = Root(c);
                if (!ids.TryGetValue(r, out int id)) { id = ids.Count; ids[r] = id; roomLists.Add(new()); }
                foreach (int d in (ReadOnlySpan<int>)[0, 1, W, W + 1])
                {
                    int x = c + d;
                    if (rmCell[x] == -1) rmCell[x] = id; else if (rmCell[x] != id) rmCell[x] = -2;
                }
            }
        for (int c = 0; c < cells; c++) if (rmCell[c] >= 0) roomLists[rmCell[c]].Add(c);
        rmRooms = roomLists.Select(l => l.ToArray()).ToArray();
        int nr = rmRooms.Length;
        rmNw = (nr + 63) / 64;
        rmEdgeBits = new ulong[Math.Max(1, nr * rmNw)];
        // initFast
        int maxc = 0;
        rmLoc = new int[cells];
        rmLab = new int[cells];
        foreach (var rc in rmRooms) { maxc = Math.Max(maxc, rc.Length); for (int i = 0; i < rc.Length; i++) rmLoc[rc[i]] = i; }
        var corr = new List<int>();
        for (int c = 0; c < cells; c++) if (rmCell[c] < 0 && !wall[c]) corr.Add(c);
        rmCorr = corr.ToArray();
        rmTouch = new ulong[(rmCorr.Length + 16) * Math.Max(1, rmNw)];
        rmCompRep = new int[rmCorr.Length + 16]; rmCompRepSt = new uint[rmCorr.Length + 16];
        var nbStart = new List<int>(); var nbCell = new List<int>(); var nbKind = new List<int>(); var roomBase = new List<int>();
        for (int r = 0; r < nr; r++)
        {
            roomBase.Add(nbStart.Count);
            foreach (int c in rmRooms[r])
            {
                nbStart.Add(nbCell.Count);
                foreach (int d in dirs)
                {
                    int y = c + d;
                    if (wall[y]) continue;
                    int kd = -1, cy = rmCell[y];
                    if (cy == r) kd = rmLoc[y]; else if (cy >= 0) kd = -2 - cy;
                    nbCell.Add(y); nbKind.Add(kd);
                }
            }
        }
        nbStart.Add(nbCell.Count);
        rmRoomBase = roomBase.ToArray();
        var fbStart = new List<int>(); var fbEdge = new List<(int, int)>();
        for (int r = 0; r < nr; r++)
            for (int i = 0; i < rmRooms[r].Length; i++)
            {
                int g = rmRoomBase[r] + i;
                fbStart.Add(fbEdge.Count);
                for (int e = nbStart[g]; e < nbStart[g + 1]; e++)
                    if (nbKind[e] < 0 || nbKind[e] < i) fbEdge.Add((nbCell[e], nbKind[e]));
            }
        fbStart.Add(fbEdge.Count);
        rmFbStart = fbStart.ToArray(); rmFbEdge = fbEdge.ToArray();
        rmDsu = new int[Math.Max(1, maxc)]; rmFragTouch = new ulong[Math.Max(1, maxc) * Math.Max(1, rmNw)];
        rmSeen = new uint[cells]; rmQueue = new int[cells]; rmStack = new int[cells];
        // direct room neighbours with no boxes (occ is empty here)
        for (int r = 0; r < nr; r++)
            if (rmRooms[r].Length > 0) RoomWalk(r, rmRooms[r][0]);
    }

    void RoomWalk(int room, int start)
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
                    if (r0 >= 0 && r0 != room) rmEdgeBits[room * rmNw + (r0 >> 6)] |= 1UL << (r0 & 63);
                    continue;
                }
                if (wall[x] || occ[x] || rmSeen[x] == st) continue;
                rmSeen[x] = st;
                int r = rmCell[x];
                if (r >= 0 && r != room) { rmEdgeBits[room * rmNw + (r >> 6)] |= 1UL << (r & 63); continue; }
                rmQueue[tail++] = x;
            }
    }

    void LabelCorridors()
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
                    if (cy >= 0) { if (!occ[y]) rmTouch[tb + (cy >> 6)] |= 1UL << (cy & 63); }
                    else if (!occ[y] && rmLab[y] < 0) { rmLab[y] = k; rmStack[sp++] = y; }
                }
            }
            k++;
        }
        rmK = k;
    }

    int RmFind(int x) { while (rmDsu[x] != x) { rmDsu[x] = rmDsu[rmDsu[x]]; x = rmDsu[x]; } return x; }
    void RmUnion(int a, int b)
    {
        int ra = RmFind(a), rb = RmFind(b);
        if (ra == rb) return;
        rmDsu[ra] = rb;
        int nw = rmNw;
        for (int w = 0; w < nw; w++) rmFragTouch[rb * nw + w] |= rmFragTouch[ra * nw + w];
    }

    /// <summary>Broken directed edges from the empty fragments of room r (Go roomBrokenFast), given the current corridor labelling.</summary>
    int RoomBroken(int r)
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
                if (occ[y]) continue;
                if (kd >= 0) { if (kd < i) RmUnion(i, kd); }
                else if (kd == -1)
                {
                    int k = rmLab[y];
                    int ri = RmFind(i) * nw;
                    for (int w = 0; w < nw; w++) rmFragTouch[ri + w] |= rmTouch[k * nw + w];
                    if (rmCompRepSt[k] == rs) RmUnion(i, rmCompRep[k]);
                    else { rmCompRepSt[k] = rs; rmCompRep[k] = i; }
                }
                else { int o = -2 - kd; rmFragTouch[RmFind(i) * nw + (o >> 6)] |= 1UL << (o & 63); }
            }
        }
        int broken = 0;
        if (free == 0)
        {
            for (int w = 0; w < nw; w++) broken += BitOperations.PopCount(rmEdgeBits[eb + w]);
            return broken;
        }
        for (int i = 0; i < cellsR.Length; i++)
        {
            if (occ[cellsR[i]] || rmDsu[i] != i) continue;
            for (int w = 0; w < nw; w++) broken += BitOperations.PopCount(rmEdgeBits[eb + w] & ~rmFragTouch[i * nw + w]);
        }
        return broken;
    }

    /// <summary>Room connectivity of the board in occ (Go roomConnectivity): total broken edges over all rooms.</summary>
    int MRoomConnectivity()
    {
        if (!HasRooms || rmRooms.Length < 2) return 0;
        LabelCorridors();
        int broken = 0;
        for (int r = 0; r < rmRooms.Length; r++) broken += RoomBroken(r);
        return broken;
    }

    // ---- packing basin (Go basin.go) --------------------------------------------------------------------------------------------------
    bool[]? basinInside; int[]? basinDist;

    bool[]? TargetBasin(int goalCell)
    {
        var inside = new bool[cells]; inside[goalCell] = true;
        var seen = new bool[4 * cells]; var mk = new uint[cells]; uint stamp2 = 0;
        var q = new List<int>(); var pq = new List<int>();
        bool Free(int c, int box) => !wall[c] && c != box && (!goal[c] || c == goalCell);
        for (int di = 0; di < 4; di++)
            if (Free(goalCell + dirs[di], goalCell)) { int k = goalCell * 4 + di; seen[k] = true; q.Add(k); }
        for (int i = 0; i < q.Count; i++)
        {
            if ((i & 63) == 0 && MSetupExpired()) return null;
            int k = q[i];
            int b = k / 4, d = dirs[k % 4];
            int to = b + d, p = b + 2 * d;
            if (!Free(to, b) || !Free(p, b)) continue;
            inside[to] = true;
            stamp2++;
            pq.Clear(); pq.Add(p); mk[p] = stamp2;
            for (int j = 0; j < pq.Count; j++)
                foreach (int off in dirs)
                {
                    int x = pq[j] + off;
                    if (mk[x] != stamp2 && Free(x, to)) { mk[x] = stamp2; pq.Add(x); }
                }
            for (int di = 0; di < 4; di++)
            {
                int k2 = to * 4 + di;
                if (mk[to + dirs[di]] == stamp2 && !seen[k2]) { seen[k2] = true; q.Add(k2); }
            }
        }
        return inside;
    }

    long mSetupDeadlineMs = long.MaxValue;
    bool MSetupExpired() => MExpired() || (mNodeLimit == 0 && clock.ElapsedMilliseconds > mSetupDeadlineMs);

    void MComputeBasin()
    {
        basinInside = null; basinDist = null;
        if (rmRooms.Length == 0) return;
        bool[]? best = null; int bestBoxes = -1, bestRoom = -1, bestSink = -1;
        foreach (int g in goals)
        {
            var inside = TargetBasin(g);
            if (inside == null) break;
            int boxes = 0;
            foreach (int b in lv.Boxes) if (inside[b]) boxes++;
            var rcounts = new int[rmRooms.Length];
            for (int c = 0; c < cells; c++) if (inside[c] && rmCell[c] >= 0) rcounts[rmCell[c]]++;
            int sink = -1, roomSize = 0;
            for (int r = 0; r < rcounts.Length; r++) if (rcounts[r] > roomSize) { sink = r; roomSize = rcounts[r]; }
            if (sink < 0) continue;
            if (boxes > bestBoxes || boxes == bestBoxes && roomSize > bestRoom) { bestBoxes = boxes; bestRoom = roomSize; best = inside; bestSink = sink; }
        }
        if (best == null) return;
        basinInside = best; basinSink = bestSink;
        var d = new int[cells]; Array.Fill(d, -1);
        var q = new List<int>();
        for (int c = 0; c < cells; c++) if (best[c]) { d[c] = 0; q.Add(c); }
        for (int i = 0; i < q.Count; i++)
            foreach (int off in dirs)
            {
                int x = q[i] + off;
                if (!wall[x] && d[x] < 0) { d[x] = d[q[i]] + 1; q.Add(x); }
            }
        basinDist = d;
    }

    /// <summary>One box's out-of-plan term without a plan (boxes outside the basin count unless on a goal).</summary>
    int OopOne(int c) => basinInside != null && !basinInside[c] && !goal[c] ? 1 : 0;

    /// <summary>Go outOfPlanStep: boxes outside the basin that are neither placed in the plan's phase row nor on a goal. phase -2: look up from occ.</summary>
    int MOutOfPlan(ReadOnlySpan<int> boxes, int phase = -2, int replaceIdx = -1, int replaceCell = 0)
    {
        if (basinInside == null) return 0;
        if (mPlan != null && phase == -2) phase = mPlan.Status(occ).phase;
        int n2 = 0;
        for (int j = 0; j < boxes.Length; j++)
        {
            int b = j == replaceIdx ? replaceCell : boxes[j];
            if (basinInside[b]) continue;
            bool ok = mPlan != null ? mPlan.PlacedRow(phase, b) : goal[b] && occ[b];
            if (!ok && !goal[b]) n2++;
        }
        return n2;
    }

    // walking distance (walls only) from a source, cached per source (opener and out-of-plan advisors use fixed sources per node)
    readonly Dictionary<int, int[]> mWalkCache = new();
    int MWalk(int from, int to)
    {
        if (!mWalkCache.TryGetValue(from, out var d))
        {
            if (mWalkCache.Count > 256) mWalkCache.Clear();
            d = new int[cells]; Array.Fill(d, -1);
            var q = new List<int> { from }; d[from] = 0;
            for (int i = 0; i < q.Count; i++)
                foreach (int dd in dirs)
                {
                    int x = q[i] + dd;
                    if (!wall[x] && d[x] < 0) { d[x] = d[q[i]] + 1; q.Add(x); }
                }
            mWalkCache[from] = d;
        }
        return d[to];
    }
}
