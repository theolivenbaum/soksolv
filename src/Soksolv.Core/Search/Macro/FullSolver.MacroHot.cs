namespace Soksolv.Search;

/// <summary>Stage 4 of the macro lane: hotspots (Go hotspots.go) — advisor information only, never a deadlock proof.</summary>
public sealed unsafe partial class FullSolver
{
    // row(y): floor-index bitset of box squares whose reachable-goal set shrinks if y becomes a permanent wall
    // (union over goals g of T_g \ T'_g(y) \ {y}, T_g = push-reachability tree of g, T' with y walled as cell and as support).
    ulong[]?[]? hotRowArr; // by floor index (Go hotspotTable.rows)
    bool hotSides;      // fa2: the player-side relation (Go -hot-sides)
    bool hotGoalFree;   // fa2: goals are neither blockers nor victims (Festival's relation); set before the first row is built
    uint[]? hotSeen; uint hotStamp; int[]? hotQ;

    ulong[] HotRow(int blocker)
    {
        hotRowArr ??= new ulong[]?[nodes.Fcell.Length];
        int hidx = nodes.Fidx[blocker];
        if (hotRowArr[hidx] is { } cached) return cached;
        if (hotSides) { var rs = HotRowSides(blocker); hotRowArr[hidx] = rs; return rs; }
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
        hotRowArr[hidx] = row;
        return row;
    }

    static bool RowHits(ulong[] row, int fidx) => (row[fidx >> 6] >> (fidx & 63) & 1) != 0;

    // Per-node scratch (Go hotspotTable.counts/after): one cached row per box, the boxes' floor indices, and lazily
    // built "column" bitsets (boxes whose row hits floor index x), so that a candidate move needs only word ops.
    ulong[]?[] hotNodeRows = new ulong[]?[16];
    int[] hotNodeIdx = new int[16];
    ulong[]? hotPos, hotMpos, hotLmask, hotCol;
    uint[]? hotColGen;
    int hotBw, hotMgen = -1, hotN0, hotNBoxes; uint hotGen;

    /// <summary>Per box: number of other boxes its blocking would hurt. Readies the scratch used by HotAfter (call it for the same boxes, until the next HotCounts).</summary>
    int[] HotCounts(ReadOnlySpan<int> boxes)
    {
        int nb = boxes.Length, nf = nodes.Fcell.Length, nw = Math.Max(1, nodes.Nw);
        var counts = new int[nb];
        if (hotNodeRows.Length < nb) { hotNodeRows = new ulong[]?[nb * 2]; hotNodeIdx = new int[nb * 2]; }
        if (hotPos == null) { hotPos = new ulong[nw]; hotMpos = new ulong[nw]; hotColGen = new uint[nf]; }
        Array.Clear(hotPos);
        hotNBoxes = nb;
        for (int i = 0; i < nb; i++)
        {
            hotNodeRows[i] = HotRow(boxes[i]);
            int fi = nodes.Fidx[boxes[i]]; hotNodeIdx[i] = fi;
            hotPos[fi >> 6] |= 1UL << (fi & 63);
        }
        for (int i = 0; i < nb; i++)
        {
            var row = hotNodeRows[i]!;
            for (int j = 0; j < nb; j++)
                if (i != j && RowHits(row, hotNodeIdx[j])) counts[i]++;
        }
        int bw = Math.Max((nb + 63) / 64, 1);
        if (bw != hotBw || hotCol == null)
        {
            hotBw = bw; hotCol = new ulong[nf * bw]; hotLmask = new ulong[bw];
            Array.Clear(hotColGen!); hotGen = 0;
        }
        hotGen++; hotMgen = -1;
        return counts;
    }

    /// <summary>Offset into hotCol of floor index x's column (boxes whose row hits x), built on first use this node.</summary>
    int HotColumn(int x)
    {
        int off = x * hotBw;
        if (hotColGen![x] != hotGen)
        {
            hotColGen[x] = hotGen;
            var col = hotCol!;
            for (int w = 0; w < hotBw; w++) col[off + w] = 0;
            for (int i = 0; i < hotNBoxes; i++)
                if (RowHits(hotNodeRows[i]!, x)) col[off + (i >> 6)] |= 1UL << (i & 63);
        }
        return off;
    }

    static int HotspotCount(int[] counts) { int n = 0; foreach (int c in counts) if (c > 0) n++; return n; }

    /// <summary>
    /// Hotspot boxes after replacing boxes[moved] with target. For a fixed moved box each other box's count without it is
    /// fixed, so a candidate only needs the boxes whose count would drop to zero (lmask) that the target hits, and whether
    /// the target's row hits any remaining box.
    /// </summary>
    int HotAfter(ReadOnlySpan<int> boxes, int[] counts, int moved, int target)
    {
        int old = hotNodeIdx[moved], now = nodes.Fidx[target];
        if (hotMgen != moved)
        {
            hotMgen = moved; hotN0 = 0;
            Array.Clear(hotLmask!);
            int oc = HotColumn(old);
            for (int i = 0; i < boxes.Length; i++)
            {
                if (i == moved) continue;
                int c = counts[i];
                if ((hotCol![oc + (i >> 6)] & (1UL << (i & 63))) != 0) c--;
                if (c > 0) hotN0++;
                else if (c == 0) hotLmask![i >> 6] |= 1UL << (i & 63);
            }
            Array.Copy(hotPos!, hotMpos!, hotPos!.Length);
            hotMpos![old >> 6] &= ~(1UL << (old & 63));
        }
        int n = hotN0;
        int nc = HotColumn(now);
        for (int k = 0; k < hotBw; k++) n += System.Numerics.BitOperations.PopCount(hotLmask![k] & hotCol![nc + k]);
        var row = HotRow(target);
        for (int k = 0; k < hotMpos!.Length; k++)
            if ((hotMpos[k] & row[k]) != 0) { n++; break; }
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
        if (!bbNode) return MMoveAccessScan(cur, moved, target);
        if (!CheckAccessBits) return MMoveAccessBits(cur, moved, target);
        int a0 = mAccN;
        var got = MMoveAccessBits(cur, moved, target);
        var gotFacts = mAccBuf.AsSpan(got.accStart, got.accLen).ToArray(); Array.Sort(gotFacts);
        mAccN = a0;
        var want = MMoveAccessScan(cur, moved, target);
        var wantFacts = mAccBuf.AsSpan(want.accStart, want.accLen).ToArray(); Array.Sort(wantFacts);
        if (got.mobility != want.mobility || got.explorer != want.explorer || !gotFacts.AsSpan().SequenceEqual(wantFacts))
            throw new InvalidOperationException($"moveAccessBits disagrees with scan: mobility {got.mobility}/{want.mobility} explorer {got.explorer}/{want.explorer} facts {gotFacts.Length}/{wantFacts.Length}");
        return want;
    }

    (int mobility, bool explorer, int accStart, int accLen) MMoveAccessScan(ReadOnlySpan<int> cur, int moved, int target)
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
