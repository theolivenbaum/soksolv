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
    int[] rmFlat = [], rmFbCell = [], rmFbKind = [], rmFbStartPin = [], rmRoomLen = [];
    int* rmLabP, rmRepP, rmDsuP, rmFlatP, rmFbCellP, rmFbKindP, rmFbStartP; ulong* rmTouchP, rmFragP; uint* rmRepStP;
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
        rmLab = GC.AllocateArray<int>(cells, pinned: true);
        foreach (var rc in rmRooms) { maxc = Math.Max(maxc, rc.Length); for (int i = 0; i < rc.Length; i++) rmLoc[rc[i]] = i; }
        var corr = new List<int>();
        for (int c = 0; c < cells; c++) if (rmCell[c] < 0 && !wall[c]) corr.Add(c);
        rmCorr = corr.ToArray();
        rmTouch = GC.AllocateArray<ulong>((rmCorr.Length + 16) * Math.Max(1, rmNw), pinned: true);
        rmCompRep = GC.AllocateArray<int>(rmCorr.Length + 16, pinned: true); rmCompRepSt = GC.AllocateArray<uint>(rmCorr.Length + 16, pinned: true);
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
        rmDsu = GC.AllocateArray<int>(Math.Max(1, maxc), pinned: true); rmFragTouch = GC.AllocateArray<ulong>(Math.Max(1, maxc) * Math.Max(1, rmNw), pinned: true);
        // flat pinned views for the single-word room kernel (<= 64 rooms)
        rmFlat = GC.AllocateArray<int>(Math.Max(1, rmRooms.Sum(x => x.Length)), pinned: true);
        { int o = 0; for (int r = 0; r < nr; r++) foreach (int c in rmRooms[r]) rmFlat[o++] = c; }
        rmFbCell = GC.AllocateArray<int>(Math.Max(1, rmFbEdge.Length), pinned: true); rmFbKind = GC.AllocateArray<int>(Math.Max(1, rmFbEdge.Length), pinned: true);
        for (int e = 0; e < rmFbEdge.Length; e++) { rmFbCell[e] = rmFbEdge[e].cell; rmFbKind[e] = rmFbEdge[e].kind; }
        rmFbStartPin = GC.AllocateArray<int>(rmFbStart.Length, pinned: true); Array.Copy(rmFbStart, rmFbStartPin, rmFbStart.Length);
        rmRoomLen = new int[nr]; for (int r = 0; r < nr; r++) rmRoomLen[r] = rmRooms[r].Length;
        rmLabP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmLab[0]); rmTouchP = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmTouch[0]);
        rmRepP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmCompRep[0]); rmRepStP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmCompRepSt[0]);
        rmDsuP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmDsu[0]); rmFragP = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmFragTouch[0]);
        rmFlatP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmFlat[0]); rmFbCellP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmFbCell[0]);
        rmFbKindP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmFbKind[0]); rmFbStartP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmFbStartPin[0]);
        rmSeen = new uint[cells]; rmQueue = new int[cells]; rmStack = new int[cells];
        rmIn4 = new bool[cells];
        for (int c = 0; c < cells; c++)
        {
            int r0 = rmCell[c];
            if (r0 < 0) continue;
            rmIn4[c] = true;
            foreach (int d in dirs) { int y = c + d; if (!wall[y] && rmCell[y] != r0) rmIn4[c] = false; }
        }
        rmFragRoot = new int[nbStart.Count - 1]; rmFragRow = new ulong[nbStart.Count - 1];
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
    /// <summary>RoomBroken for at most 64 rooms (one touch word per fragment): pointer kernel with inlined find/union (Go roomFrag1).</summary>
    int RoomBroken1(int r)
    {
        int bse = rmRoomBase[r], n = rmRoomLen[r];
        int* cl = rmFlatP + bse; int* dsu = rmDsuP; ulong* rows = rmFragP; byte* bk = blk;
        int* fbs = rmFbStartP + bse;
        int* lab = rmLabP; ulong* touch = rmTouchP; int* rep = rmRepP; uint* repSt = rmRepStP;
        uint rs = ++rmRepStamp;
        if (rs == 0) { Array.Clear(rmCompRepSt); rs = rmRepStamp = 1; }
        int free = 0;
        for (int i = 0; i < n; i++)
        {
            if (bk[cl[i]] != 0) continue;
            free++;
            dsu[i] = i; rows[i] = 0;
            for (int e = fbs[i], eEnd = fbs[i + 1]; e < eEnd; e++)
            {
                int y = rmFbCellP[e];
                if (bk[y] != 0) continue;
                int kd = rmFbKindP[e];
                if (kd >= 0)
                {
                    if (kd < i)
                    {
                        int ra = i, rb = kd;
                        while (dsu[ra] != ra) { dsu[ra] = dsu[dsu[ra]]; ra = dsu[ra]; }
                        while (dsu[rb] != rb) { dsu[rb] = dsu[dsu[rb]]; rb = dsu[rb]; }
                        if (ra != rb) { dsu[ra] = rb; rows[rb] |= rows[ra]; }
                    }
                }
                else if (kd == -1)
                {
                    int k = lab[y];
                    int ri = i;
                    while (dsu[ri] != ri) { dsu[ri] = dsu[dsu[ri]]; ri = dsu[ri]; }
                    rows[ri] |= touch[k];
                    if (repSt[k] == rs)
                    {
                        int ra = i, rb = rep[k];
                        while (dsu[ra] != ra) { dsu[ra] = dsu[dsu[ra]]; ra = dsu[ra]; }
                        while (dsu[rb] != rb) { dsu[rb] = dsu[dsu[rb]]; rb = dsu[rb]; }
                        if (ra != rb) { dsu[ra] = rb; rows[rb] |= rows[ra]; }
                    }
                    else { repSt[k] = rs; rep[k] = i; }
                }
                else
                {
                    int ri = i;
                    while (dsu[ri] != ri) { dsu[ri] = dsu[dsu[ri]]; ri = dsu[ri]; }
                    rows[ri] |= 1UL << (-2 - kd);
                }
            }
        }
        ulong edge = rmEdgeBits[r];
        if (free == 0) return BitOperations.PopCount(edge);
        int broken = 0;
        for (int i = 0; i < n; i++)
        {
            if (bk[cl[i]] != 0 || dsu[i] != i) continue;
            broken += BitOperations.PopCount(edge & ~rows[i]);
        }
        return broken;
    }

    int RoomBroken(int r)
    {
        if (rmNw == 1) return RoomBroken1(r);
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

    // ---- incremental room connectivity (Go roomConnParent / roomConnChild) ----------------------------------------------------------
    bool[] rmIn4 = []; int[] rmFragRoot = []; ulong[] rmFragRow = [];   // interior-move shortcut (Go in4 / fragRoot / fragRow)
    int[] rmShare = []; int rmParentTotal; ulong[] rmAff = []; int[] rmUndo = new int[64]; int rmUndoN;
    public static bool CheckRooms = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_ROOMS") == "1";

    /// <summary>Room connectivity of the opened node (occ holds it), remembering the corridor labelling and each room's share for <see cref="MRoomConnChild"/>.</summary>
    int MRoomConnParent()
    {
        if (!HasRooms || rmRooms.Length < 2) return 0;
        if (rmShare.Length != rmRooms.Length) { rmShare = new int[rmRooms.Length]; rmAff = new ulong[Math.Max(1, rmNw)]; }
        LabelCorridors();
        int total = 0;
        for (int r = 0; r < rmRooms.Length; r++)
        {
            rmShare[r] = RoomBroken(r); total += rmShare[r];
            if (rmNw == 1) RmKeepFrags(r);
        }
        rmParentTotal = total;
        return total;
    }

    /// <summary>Records room r's fragments just computed by RoomBroken (Go keepFrags).</summary>
    void RmKeepFrags(int r)
    {
        var cellsR = rmRooms[r]; int bse = rmRoomBase[r];
        for (int i = 0; i < cellsR.Length; i++)
        {
            if (occ[cellsR[i]]) continue;
            int x = i;
            while (rmDsu[x] != x) x = rmDsu[x];
            rmFragRoot[bse + i] = x;
            if (x == i) rmFragRow[bse + i] = rmFragTouch[i];
        }
    }

    /// <summary>Go roomInteriorMove: the box on b moved to t, both interior cells of one room (occ holds the child): only that room changes, settled from the opened node's fragments; false when a split is possible.</summary>
    bool RmInteriorMove(int b, int t, out int result)
    {
        result = 0;
        int r = rmCell[t];
        ulong edge = rmEdgeBits[r];
        int bse = rmRoomBase[r];
        int w = W;
        Span<bool> fr = stackalloc bool[8];
        Span<int> ring = stackalloc int[8];
        ring[0] = t - w; ring[1] = t - w + 1; ring[2] = t + 1; ring[3] = t + w + 1; ring[4] = t + w; ring[5] = t + w - 1; ring[6] = t - 1; ring[7] = t - w - 1;
        int nfree = 0;
        for (int k = 0; k < 8; k++)
        {
            int c = ring[k];
            fr[k] = c != b && !occ[c] && !wall[c] && rmCell[c] == r;
            if ((k & 1) == 0 && fr[k]) nfree++;
        }
        int share = rmShare[r];
        if (nfree == 0) share -= BitOperations.PopCount(edge);
        else if (nfree > 1)
        {
            int start = -1;
            for (int k = 0; k < 8; k++) if (!fr[k]) { start = k; break; }
            if (start >= 0)
            {
                int runs = 0;
                for (int j = 1; j <= 8; j++)
                {
                    int k = (start + j) & 7;
                    if (!fr[k]) continue;
                    if (!fr[(k + 7) & 7])
                    {
                        bool has = false;
                        for (int q = k; fr[q]; q = (q + 1) & 7) if ((q & 1) == 0) { has = true; break; }
                        if (has) runs++;
                    }
                }
                if (runs > 1) return false;
            }
        }
        Span<int> roots = stackalloc int[4];
        ulong rows = 0; int nr = 0, contrib = 0;
        foreach (int d in dirs)
        {
            int c = b + d;
            if (occ[c] || wall[c]) continue;
            int x = rmFragRoot[bse + rmLoc[c]];
            bool dup = false;
            for (int j = 0; j < nr; j++) dup |= roots[j] == x;
            if (dup) continue;
            roots[nr++] = x;
            ulong row = rmFragRow[bse + x];
            rows |= row;
            contrib += BitOperations.PopCount(edge & ~row);
        }
        if (nr == 0) share += BitOperations.PopCount(edge);
        else if (nr > 1) share += BitOperations.PopCount(edge & ~rows) - contrib;
        result = rmParentTotal - rmShare[r] + share;
        return true;
    }

    void RmFlood(int c, ref int next, int k0)
    {
        int nw = rmNw, tb = next * nw;
        for (int w = 0; w < nw; w++) rmTouch[tb + w] = 0;
        if (rmUndoN + 2 > rmUndo.Length) Array.Resize(ref rmUndo, rmUndo.Length * 2);
        rmUndo[rmUndoN++] = c; rmUndo[rmUndoN++] = rmLab[c];
        rmLab[c] = next;
        int sp = 0; rmStack[sp++] = c;
        while (sp > 0)
        {
            int x = rmStack[--sp];
            foreach (int d in dirs)
            {
                int y = x + d;
                if (wall[y] || occ[y]) continue;
                int cy = rmCell[y];
                if (cy >= 0) rmTouch[tb + (cy >> 6)] |= 1UL << (cy & 63);
                else if (rmLab[y] < k0)
                {
                    if (rmUndoN + 2 > rmUndo.Length) Array.Resize(ref rmUndo, rmUndo.Length * 2);
                    rmUndo[rmUndoN++] = y; rmUndo[rmUndoN++] = rmLab[y];
                    rmLab[y] = next; rmStack[sp++] = y;
                }
            }
        }
        next++;
    }

    /// <summary>Room connectivity after the box on b moved to t (occ holds the child), given <see cref="MRoomConnParent"/> for the parent: only the corridor components next to b or t are re-labelled (overlay undone before returning) and only the rooms that can see the change are re-evaluated.</summary>
    int MRoomConnChild(int b, int t)
    {
        if (!HasRooms || rmRooms.Length < 2) return 0;
        if (b == t) return rmParentTotal;
        if (rmNw == 1 && rmIn4[b] && rmIn4[t] && rmCell[b] == rmCell[t] && RmInteriorMove(b, t, out int fast))
        {
            if (CheckRooms)
            {
                int want = MRoomConnectivity();
                if (want != fast) throw new InvalidOperationException($"interior room move {fast} != full {want} (b {b} t {t})");
                SetOcc(t, false); SetOcc(b, true); LabelCorridors(); SetOcc(b, false); SetOcc(t, true);
            }
            return fast;
        }
        int nw = rmNw;
        var aff = rmAff;
        for (int w = 0; w < nw; w++) aff[w] = 0;
        for (int zi = 0; zi < 2; zi++)
        {
            int z = zi == 0 ? b : t;
            int r0 = rmCell[z];
            if (r0 >= 0) aff[r0 >> 6] |= 1UL << (r0 & 63);
            foreach (int d in dirs)
            {
                int n = z + d;
                if (wall[n]) continue;
                int r = rmCell[n];
                if (r >= 0) aff[r >> 6] |= 1UL << (r & 63);
                else { int k = rmLab[n]; if (k >= 0) for (int w = 0; w < nw; w++) aff[w] |= rmTouch[k * nw + w]; }
            }
        }
        int k0 = rmK, next = k0;
        rmUndoN = 0;
        if (rmCell[b] < 0) RmFlood(b, ref next, k0);
        for (int zi = 0; zi < 2; zi++)
        {
            int z = zi == 0 ? b : t;
            foreach (int d in dirs)
            {
                int n = z + d;
                if (!wall[n] && !occ[n] && rmCell[n] < 0 && rmLab[n] < k0) RmFlood(n, ref next, k0);
            }
        }
        for (int zi = 0; zi < 2; zi++)
        {
            int z = zi == 0 ? b : t;
            foreach (int d in dirs)
            {
                int n = z + d;
                if (!wall[n] && !occ[n] && rmCell[n] < 0)
                {
                    int k = rmLab[n];
                    for (int w = 0; w < nw; w++) aff[w] |= rmTouch[k * nw + w];
                }
            }
        }
        int total = rmParentTotal;
        for (int w = 0; w < nw; w++)
            for (ulong v = aff[w]; v != 0; v &= v - 1)
            {
                int r = w << 6 | BitOperations.TrailingZeroCount(v);
                total += RoomBroken(r) - rmShare[r];
            }
        for (int i = rmUndoN - 2; i >= 0; i -= 2) rmLab[rmUndo[i]] = rmUndo[i + 1];
        rmK = k0;
        if (CheckRooms)
        {
            int want = MRoomConnectivity();   // clobbers the labelling: restore the parent's (occ with the box back on b)
            if (want != total) throw new InvalidOperationException($"incremental room connectivity {total} != full {want} (b {b} t {t})");
            SetOcc(t, false); SetOcc(b, true); LabelCorridors(); SetOcc(b, false); SetOcc(t, true);
        }
        return total;
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

    /// <summary>MOutOfPlan(cur, phase, i, t) for a plan child: the parent's count for this phase (cached per opened node) corrected for the moved box.</summary>
    int MOutOfPlanChild(ReadOnlySpan<int> cur, int phase, int i, int t)
    {
        if (basinInside == null) return 0;
        var plan = mPlan!;
        int slot = phase + 1;
        int baseCount = plan.OopCache[slot];
        if (baseCount < 0)
        {
            baseCount = 0;
            for (int j = 0; j < cur.Length; j++) baseCount += OopContrib(plan, phase, cur[j]);
            plan.OopCache[slot] = baseCount;
        }
        return baseCount - OopContrib(plan, phase, cur[i]) + OopContrib(plan, phase, t);
    }
    int OopContrib(PackingPlan plan, int phase, int b) => basinInside![b] || goal[b] || plan.PlacedRow(phase, b) ? 0 : 1;

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
