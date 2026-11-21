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

    public static bool HotRowsDefinition = Environment.GetEnvironmentVariable("SOKSOLV_HOT_DEF") == "1";   // reference builder (tests)
    public static bool CheckHotRows = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_HOT") == "1";

    ulong[] HotRow(int blocker)
    {
        hotRowArr ??= new ulong[]?[nodes.Fcell.Length];
        int hidx = nodes.Fidx[blocker];
        if (hotRowArr[hidx] is { } cached) return cached;
        if (hotSides) { var rs = HotRowSides(blocker); hotRowArr[hidx] = rs; return rs; }
        if (!HotRowsDefinition)
        {
            var tr = HotRowTree(blocker);
            if (CheckHotRows) { var df = HotRowDef(blocker); if (!tr.AsSpan().SequenceEqual(df)) throw new InvalidOperationException($"hot row tree != definition for blocker {blocker}"); }
            hotRowArr[hidx] = tr;
            return tr;
        }
        var rowD = HotRowDef(blocker);
        hotRowArr[hidx] = rowD;
        return rowD;
    }

    /// <summary>Definition-based row (reference): union over goals of T_g \ T'_g(y) \ {y}.</summary>
    ulong[] HotRowDef(int blocker)
    {
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
        return row;
    }


    // ---- tree-based rows (Go hotspots.go: buildTrees / row) -----------------------------------------------------------------------
    // Per goal: BFS tree of the pull reachability (box squares from which the goal is reachable) with an Euler tour, so a
    // blocker's effect is found by re-searching only the subtrees it cuts.
    bool hotTrees;
    int[]? hPar, hTin, hSize, hByT, hRs, hRq; uint hRstamp;
    readonly int[] hRgLo = new int[10], hRgHi = new int[10];

    void HotBuildTrees()
    {
        hotTrees = true;
        int nf = nodes.Fcell.Length, ng = goals.Length;
        hPar = new int[ng * nf]; hTin = new int[ng * nf]; hSize = new int[ng * nf]; hByT = new int[ng * nf]; hRs = new int[nf]; hRq = new int[cells];
        Array.Fill(hTin, -1);
        var fidx = nodes.Fidx; var fcell = nodes.Fcell;
        hotSeen ??= new uint[cells]; hotQ ??= new int[cells];
        var next = new int[nf];
        for (int g = 0; g < ng; g++)
        {
            int baseG = g * nf, goalCell = goals[g];
            uint st = ++hotStamp;
            int tail = 0; hotQ![tail++] = goalCell; hotSeen![goalCell] = st;
            hPar[baseG + fidx[goalCell]] = -1;
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int q = hotQ[i], b = q - d;
                    if (wall[b] || hotSeen[b] == st || wall[SupportCell(b, q, d)] || goalDist[g * cells + b] < 0) continue;
                    hotSeen[b] = st; hPar[baseG + fidx[b]] = q; hotQ[tail++] = b;
                }
            for (int i = tail - 1; i >= 0; i--)
            {
                int fi = fidx[hotQ[i]];
                hSize[baseG + fi]++;
                int p = hPar[baseG + fi];
                if (p >= 0 && i > 0) hSize[baseG + fidx[p]] += hSize[baseG + fi];
            }
            int gf = fidx[goalCell];
            hTin[baseG + gf] = 0; hByT[baseG] = gf; next[gf] = 1;
            for (int i = 1; i < tail; i++)
            {
                int c = hotQ[i];
                int fi = fidx[c], pf = fidx[hPar[baseG + fi]];
                hTin[baseG + fi] = next[pf];
                next[pf] += hSize[baseG + fi];
                next[fi] = hTin[baseG + fi] + 1;
                hByT[baseG + hTin[baseG + fi]] = fi;
            }
        }
    }

    ulong[] HotRowTree(int blocker)
    {
        if (!hotTrees) HotBuildTrees();
        var fidx = nodes.Fidx; var fcell = nodes.Fcell;
        int idx = fidx[blocker], nf = fcell.Length;
        var row = new ulong[Math.Max(1, nodes.Nw)];
        int[] rgLo = hRgLo, rgHi = hRgHi;
        for (int g = 0; g < goals.Length; g++)
        {
            int baseG = g * nf, goalCell = goals[g];
            int nr = 0;
            if (goalCell == blocker)
            {
                int sz = hSize[baseG + fidx[goalCell]];
                for (int t = 1; t < sz; t++) { int fi = hByT[baseG + t]; row[fi >> 6] |= 1UL << (fi & 63); }
                continue;
            }
            if (hTin[baseG + idx] >= 0) { rgLo[nr] = hTin[baseG + idx]; rgHi[nr] = hTin[baseG + idx] + hSize[baseG + idx]; nr++; }
            // tree edges whose support square is the blocker are cut too
            foreach (int d in dirs)
                for (int v = 0; v < 2; v++)
                {
                    int b = v == 0 ? blocker + d : blocker - 2 * d;
                    if ((uint)b >= (uint)cells) continue;
                    int fi = fidx[b];
                    if (fi < 0 || hTin[baseG + fi] < 0 || hPar[baseG + fi] < 0) continue;
                    int q = hPar[baseG + fi];
                    if (SupportCell(b, q, q - b) == blocker) { rgLo[nr] = hTin[baseG + fi]; rgHi[nr] = hTin[baseG + fi] + hSize[baseG + fi]; nr++; }
                }
            if (nr == 0) continue;
            for (int i = 1; i < nr; i++)
                for (int j = i; j > 0 && rgLo[j] < rgLo[j - 1]; j--)
                {
                    (rgLo[j], rgLo[j - 1]) = (rgLo[j - 1], rgLo[j]); (rgHi[j], rgHi[j - 1]) = (rgHi[j - 1], rgHi[j]);
                }
            int m = 0;
            for (int i = 0; i < nr; i++)
            {
                if (m > 0 && rgLo[i] < rgHi[m - 1]) rgHi[m - 1] = Math.Max(rgHi[m - 1], rgHi[i]);
                else { rgLo[m] = rgLo[i]; rgHi[m] = rgHi[i]; m++; }
            }
            uint rs = ++hRstamp;
            int qn = 0;
            var rq = hRq!;
            // in S: DFS-entry time within a cut subtree
            bool InS(int fi) { int t = hTin[baseG + fi]; for (int i = 0; i < m; i++) if (t >= rgLo[i] && t < rgHi[i]) return true; return false; }
            bool Reach(int b, int from, int d) { int sup = SupportCell(b, from, d); return b != blocker && sup != blocker && !wall[b] && !wall[sup]; }
            for (int i = 0; i < m; i++)
                for (int t = rgLo[i]; t < rgHi[i]; t++)
                {
                    int fi = hByT[baseG + t], b = fcell[fi];
                    if (b == blocker) continue;
                    foreach (int d in dirs)
                    {
                        int from = b + d;
                        int ff = fidx[from];
                        if (ff >= 0 && hTin[baseG + ff] >= 0 && !InS(ff) && Reach(b, from, d)) { hRs![fi] = (int)rs; rq[qn++] = b; break; }
                    }
                }
            for (int k = 0; k < qn; k++)
                foreach (int d in dirs)
                {
                    int b = rq[k] - d;
                    int fb = fidx[b];
                    if (fb >= 0 && hTin[baseG + fb] >= 0 && hRs![fb] != (int)rs && InS(fb) && Reach(b, rq[k], d)) { hRs[fb] = (int)rs; rq[qn++] = b; }
                }
            for (int i = 0; i < m; i++)
                for (int t = rgLo[i]; t < rgHi[i]; t++)
                {
                    int fi = hByT[baseG + t];
                    if (hRs![fi] != (int)rs && fcell[fi] != blocker) row[fi >> 6] |= 1UL << (fi & 63);
                }
        }
        if (hotGoalFree)
        {
            if (goal[blocker]) Array.Clear(row);
            else foreach (int g in goals) { int fi = fidx[g]; row[fi >> 6] &= ~(1UL << (fi & 63)); }
        }
        return row;
    }

    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    static bool RowHits(ulong[] row, int fidx) => (row[fidx >> 6] >> (fidx & 63) & 1) != 0;

    // Per-node scratch (Go hotspotTable.counts/after): one cached row per box, the boxes' floor indices, and lazily
    // built "column" bitsets (boxes whose row hits floor index x), so that a candidate move needs only word ops.
    ulong[]?[] hotNodeRows = new ulong[]?[16];
    int[] hotNodeIdx = new int[16];
    ulong[]? hotPos, hotMpos, hotLmask, hotCol;
    uint[]? hotColGen;
    int hotBw, hotMgen = -1, hotN0, hotNBoxes; uint hotGen;

    /// <summary>Per box: number of other boxes its blocking would hurt. Readies the scratch used by HotAfter (call it for the same boxes, until the next HotCounts).</summary>
    int[] hotCountsBuf = [];
    int[] HotCounts(ReadOnlySpan<int> boxes)
    {
        int nb = boxes.Length, nf = nodes.Fcell.Length, nw = Math.Max(1, nodes.Nw);
        var counts = hotCountsBuf.Length == nb ? hotCountsBuf : (hotCountsBuf = new int[nb]);   // the counts are only used while the node is open
        Array.Clear(counts);
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
        // counts[i] = other boxes whose floor index is in row i (rows never contain their own blocker, boxes are distinct)
        var pos = hotPos;
        for (int i = 0; i < nb; i++)
        {
            var row = hotNodeRows[i]!;
            int c = 0;
            for (int w = 0; w < nw; w++) c += System.Numerics.BitOperations.PopCount(row[w] & pos[w]);
            if (RowHits(row, hotNodeIdx[i])) c--;
            counts[i] = c;
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
            int xw = x >> 6; ulong xb = 1UL << (x & 63);
            for (int i = 0; i < hotNBoxes; i++)
                if ((hotNodeRows[i]![xw] & xb) != 0) col[off + (i >> 6)] |= 1UL << (i & 63);
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
