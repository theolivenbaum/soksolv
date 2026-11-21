namespace Soksolv.Search;

/// <summary>Stage 4 of the macro lane: hotspots (Go hotspots.go) — advisor information only, never a deadlock proof.</summary>
public sealed unsafe partial class FullSolver
{
    // row(y): floor-index bitset of box squares whose reachable-goal set shrinks if y becomes a permanent wall
    // (union over goals g of T_g \ T'_g(y) \ {y}, T_g = push-reachability tree of g, T' with y walled as cell and as support).
    readonly Dictionary<int, ulong[]> hotRows = new();
    bool hotGoalFree;   // fa2: goals are neither blockers nor victims (Festival's relation); set before the first row is built
    uint[]? hotSeen; uint hotStamp; int[]? hotQ;

    ulong[] HotRow(int blocker)
    {
        if (hotRows.TryGetValue(blocker, out var cached)) return cached;
        var row = new ulong[Math.Max(1, nodes.Nw)];
        hotSeen ??= new uint[cells]; hotQ ??= new int[cells];
        for (int g = 0; g < goals.Length; g++)
        {
            int goalCell = goals[g];
            int gbase = g * cells;
            if (goalCell == blocker)
            {
                for (int c = 0; c < cells; c++)
                    if (c != goalCell && goalDist[gbase + c] >= 0) { int f = nodes.Fidx[c]; row[f >> 6] |= 1UL << (f & 63); }
                continue;
            }
            uint st = ++hotStamp; int tail = 0;
            hotQ[tail++] = goalCell; hotSeen[goalCell] = st;
            for (int i = 0; i < tail; i++)
            {
                int q = hotQ[i];
                foreach (int d in dirs)
                {
                    int b = q - d, sup = SupportCell(b, q, d);
                    if (wall[b] || hotSeen[b] == st || wall[sup] || b == blocker || sup == blocker) continue;
                    hotSeen[b] = st; hotQ[tail++] = b;
                }
            }
            for (int c = 0; c < cells; c++)
                if (c != blocker && goalDist[gbase + c] >= 0 && hotSeen[c] != st) { int f = nodes.Fidx[c]; row[f >> 6] |= 1UL << (f & 63); }
        }
        if (hotGoalFree)
        {
            if (goal[blocker]) Array.Clear(row);
            else foreach (int g in goals) { int fi = nodes.Fidx[g]; row[fi >> 6] &= ~(1UL << (fi & 63)); }
        }
        hotRows[blocker] = row;
        return row;
    }

    static bool RowHits(ulong[] row, int fidx) => (row[fidx >> 6] >> (fidx & 63) & 1) != 0;

    /// <summary>Per box: number of other boxes its blocking would hurt.</summary>
    int[] HotCounts(ReadOnlySpan<int> boxes)
    {
        var counts = new int[boxes.Length];
        for (int i = 0; i < boxes.Length; i++)
        {
            var row = HotRow(boxes[i]);
            for (int j = 0; j < boxes.Length; j++)
                if (i != j && RowHits(row, nodes.Fidx[boxes[j]])) counts[i]++;
        }
        return counts;
    }

    static int HotspotCount(int[] counts) { int n = 0; foreach (int c in counts) if (c > 0) n++; return n; }

    /// <summary>Hotspot boxes after replacing boxes[moved] with target.</summary>
    int HotAfter(ReadOnlySpan<int> boxes, int[] counts, int moved, int target)
    {
        int old = nodes.Fidx[boxes[moved]], now = nodes.Fidx[target];
        int n = 0;
        for (int i = 0; i < boxes.Length; i++)
        {
            if (i == moved) continue;
            var row = HotRow(boxes[i]);
            int c = counts[i] - (RowHits(row, old) ? 1 : 0) + (RowHits(row, now) ? 1 : 0);
            if (c > 0) n++;
        }
        var tr = HotRow(target);
        for (int j = 0; j < boxes.Length; j++)
            if (j != moved && RowHits(tr, nodes.Fidx[boxes[j]])) { n++; break; }
        return n;
    }

    // access facts recorded for the novelty lane (Go accBuf); entries are (tag << 40) | payload
    ulong[] mAccBuf = new ulong[256]; int mAccN;
    bool mUnpackedRegime;
    int NoveltyBudget() => pull ? 0 : opt.Novelty;

    /// <summary>
    /// Move access (Go moveAccessScan), for the child just flooded by the box search (m3 stamp mpstamp, occ = child):
    /// mobility = (box, direction) pairs whose support cell is in the child's player region; explorer = an unchanged
    /// box gains a legal push from support outside the parent's region (mark stamped mOpenSt). With AccessNovelty and a
    /// novelty budget it also records the access capability facts (tags 0-4) in mAccBuf[accStart..accStart+accLen).
    /// </summary>
    (int mobility, bool explorer, int accStart, int accLen) MMoveAccess(ReadOnlySpan<int> cur, int moved, int target)
    {
        uint cs = mpstamp;
        int mobility = 0; bool explorer = false;
        bool recordAcc = AccessNovelty && NoveltyBudget() > 0;
        int accStart = mAccN;
        int origB = cur[moved];
        bool vacated = (mCmdMask | mUnpinMask) != 0 || (goal[origB] && !goal[target]);
        bool displaced = recordAcc && (mUnpackedRegime || vacated);
        bool goalVacated = recordAcc && vacated;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = i == moved ? target : cur[i];
            for (int di = 0; di < 4; di++)
            {
                int d = dirs[di];
                int support = Front(b, d), to = pull ? b + 2 * d : b + d;
                if (m3[support] != cs) continue;
                mobility++;
                bool fresh = i != moved && mark[support] != mOpenSt;
                if (!goalVacated && (!fresh || explorer && !recordAcc)) continue;
                bool legal = !wall[to] && !occ[to] && dist[b + d] >= 0;
                if (fresh && legal)
                {
                    explorer = true;
                    if (recordAcc)
                    {
                        ulong psi = (uint)(b * 4 + di);
                        AccPush(psi);
                        AccPush((1UL << 40) | (psi << 20) | (uint)target);
                        if (displaced) AccPush((2UL << 40) | (psi << 20) | (uint)origB);
                    }
                }
                if (goalVacated && legal && (mark[support] != mOpenSt || b == target))
                {
                    ulong psi = (uint)(b * 4 + di);
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

    void AccPush(ulong v) { if (mAccN == mAccBuf.Length) Array.Resize(ref mAccBuf, mAccN * 2); mAccBuf[mAccN++] = v; }
}
