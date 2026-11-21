using System.Numerics;
using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// Certified Mandatory Displacement landmarks (port of Go displacement.go). Theorem 1: if the box on goal p never moved,
/// every other box's pushes are single-box pushes on Board \ (Walls U {p}); if the other boxes cannot be matched to
/// Goals \ {p}, p MUST move. Theorem 2: collective unpacking when unpacked boxes cannot reach the empty goals with all
/// occupied goals as walls.
/// </summary>
public sealed unsafe class DisplacementLandmarks
{
    readonly bool[] wall;
    readonly int[] dirs;
    readonly int cells, numGoals;
    readonly int[] goals;
    public readonly int[] GoalIdx;
    readonly ulong[][] pinnedGoalMask;   // [pIdx][sq*4+d]: goals in Goals \ {p} reachable by 1-box pushes from (sq,d) with p a wall
    readonly ushort[][] pinComp;         // [pIdx][cell]: static component of Board \ (Walls U {p})
    uint stamp; readonly uint[] visited; readonly int[] q;
    readonly int[] matchOf;
    readonly ulong[] reachMask;
    readonly List<int> uBoxes = new(), eGoals = new(), unpackedOthers = new();
    readonly bool[] isGoalBox;
    readonly ulong[] canReach = new ulong[64];
    // row-bit kernel of the static canWalk precompute (boards <= 64 wide and <= 62 tall): free rows of Board \ (Walls U {p}) and a region scratch
    readonly int gw, gh; readonly ulong[]? rowFree, rowReg; readonly ulong[]? rowWall;
    public static bool RowKernelOff = Environment.GetEnvironmentVariable("SOKSOLV_DISP_ROWS_OFF") == "1";   // test hook

    public static DisplacementLandmarks? Create(Level lv, int[] dirs, int[] goals)
        => goals.Length == 0 || goals.Length > 64 ? null : new DisplacementLandmarks(lv, dirs, goals);

    DisplacementLandmarks(Level lv, int[] dirs, int[] goals)
    {
        wall = lv.Wall; this.dirs = dirs; cells = wall.Length; numGoals = goals.Length; this.goals = goals;
        gw = lv.W; gh = lv.H;
        if (gw <= 64 && gh + 2 <= 64 && !RowKernelOff)
        {
            rowFree = new ulong[gh + 2]; rowReg = new ulong[gh + 2]; rowWall = new ulong[gh + 2];
            for (int r = 0; r < gh; r++)
                for (int x = 0; x < gw; x++) if (!wall[r * gw + x]) rowWall[r + 1] |= 1UL << x;
        }
        GoalIdx = new int[cells]; Array.Fill(GoalIdx, -1);
        for (int g = 0; g < goals.Length; g++) GoalIdx[goals[g]] = g;
        pinnedGoalMask = new ulong[numGoals][]; pinComp = new ushort[numGoals][];
        visited = new uint[cells]; q = new int[cells * 4 + 4];
        matchOf = new int[numGoals];
        reachMask = new ulong[lv.Boxes.Length];
        isGoalBox = new bool[cells];

        // static player components for each pinned goal p
        for (int pIdx = 0; pIdx < numGoals; pIdx++)
        {
            int p = goals[pIdx];
            var comp = pinComp[pIdx] = new ushort[cells];
            ushort cid = 0;
            for (int c = 0; c < cells; c++)
            {
                if (wall[c] || c == p || comp[c] > 0) continue;
                cid++;
                comp[c] = cid;
                int head = 0, tail = 0; q[tail++] = c;
                while (head < tail)
                {
                    int curr = q[head++];
                    foreach (int dir in dirs)
                    {
                        int nxt = curr + dir;
                        if (nxt < 0 || nxt >= cells || wall[nxt] || nxt == p || comp[nxt] > 0) continue;
                        comp[nxt] = cid; q[tail++] = nxt;
                    }
                }
            }
        }
        var canWalk = new bool[cells * 16];
        var walkComputed = new uint[cells];
        var walkVisited = new uint[cells];
        var walkQ = new int[cells];
        uint walkStamp = 0, computeStamp = 0;
        var bfsQ = new int[4 * cells + 4];
        for (int pIdx = 0; pIdx < numGoals; pIdx++)
        {
            pinnedGoalMask[pIdx] = new ulong[4 * cells];
            computeStamp++;
            PrecomputeGoalPin(pIdx, goals[pIdx], canWalk, walkComputed, walkVisited, walkQ, computeStamp, ref walkStamp, bfsQ);
        }
    }

    /// <summary>canWalk[to][d1][d2]: neighbours to-dirs[d1] and to-dirs[d2] are connected in rowFree minus {to} (row-bit flood per component).</summary>
    void CanWalkRows(int to, bool[] canWalk)
    {
        int tr = to / gw + 1; ulong tbit = 1UL << (to % gw);
        Span<int> comp = [-1, -1, -1, -1];
        int cid = 0;
        ulong[] fr = rowFree!, rg = rowReg!;
        fixed (ulong* free = fr, reg = rg)
        {
            free[tr] &= ~tbit;
            for (int d = 0; d < 4; d++)
            {
                int n = to - dirs[d];
                int nr = n / gw + 1, nc = n % gw;
                if ((free[nr] >> nc & 1) == 0 || comp[d] >= 0) continue;
                cid++;
                comp[d] = cid;
                bool more = false;
                for (int d2 = d + 1; d2 < 4; d2++)
                {
                    int n2 = to - dirs[d2], r2 = n2 / gw + 1;
                    if ((free[r2] >> (n2 % gw) & 1) != 0 && comp[d2] < 0) { more = true; break; }
                }
                if (!more) continue;
                FullSolver.FloodRowsDirty(free, reg, gh + 2, nr, nc);
                for (int d2 = d + 1; d2 < 4; d2++)
                {
                    int n2 = to - dirs[d2], r2 = n2 / gw + 1;
                    if ((free[r2] >> (n2 % gw) & 1) != 0 && (reg[r2] >> (n2 % gw) & 1) != 0) comp[d2] = cid;
                }
            }
            free[tr] |= tbit;
        }
        for (int d1 = 0; d1 < 4; d1++)
            for (int d2 = 0; d2 < 4; d2++)
                if (comp[d1] > 0 && comp[d1] == comp[d2]) canWalk[to * 16 + d1 * 4 + d2] = true;
    }

    void PrecomputeGoalPin(int pIdx, int p, bool[] canWalk, uint[] walkComputed, uint[] walkVisited, int[] walkQ, uint computeStamp, ref uint walkStamp, int[] bfsQ)
    {
        var mask = pinnedGoalMask[pIdx];
        bool IsBlocked(int c) => c < 0 || c >= cells || wall[c] || c == p;
        uint ws = walkStamp;

        if (rowFree != null)
        {
            Array.Copy(rowWall!, rowFree, rowFree.Length);
            int pr = p / gw + 1; rowFree[pr] &= ~(1UL << (p % gw));
        }

        void ComputeCanWalk(int to)
        {
            if (walkComputed[to] == computeStamp) return;
            walkComputed[to] = computeStamp;
            for (int i = 0; i < 16; i++) canWalk[to * 16 + i] = false;
            if (rowFree != null) { CanWalkRows(to, canWalk); return; }
            Span<int> comp = [-1, -1, -1, -1];
            int cid = 0;
            for (int d = 0; d < 4; d++)
            {
                int n = to - dirs[d];
                if (IsBlocked(n) || comp[d] >= 0) continue;
                cid++;
                comp[d] = cid;
                ws++;
                walkVisited[to] = ws; walkVisited[n] = ws;
                // only the not-yet-labelled sibling neighbours matter: stop as soon as all of them were seen
                int remaining = 0, t0 = -1, t1 = -1, t2 = -1;
                for (int d2 = d + 1; d2 < 4; d2++)
                {
                    int n2 = to - dirs[d2];
                    if (IsBlocked(n2) || comp[d2] >= 0) continue;
                    if (remaining == 0) t0 = n2; else if (remaining == 1) t1 = n2; else t2 = n2;
                    remaining++;
                }
                int head = 0, tail = 0; walkQ[tail++] = n;
                while (remaining > 0 && head < tail)
                {
                    int curr = walkQ[head++];
                    foreach (int dir in dirs)
                    {
                        int nxt = curr + dir;
                        if (IsBlocked(nxt) || walkVisited[nxt] == ws) continue;
                        walkVisited[nxt] = ws; walkQ[tail++] = nxt;
                        if (nxt == t0 || nxt == t1 || nxt == t2) remaining--;
                    }
                }
                for (int d2 = d + 1; d2 < 4; d2++)
                {
                    int n2 = to - dirs[d2];
                    if (!IsBlocked(n2) && walkVisited[n2] == ws) comp[d2] = cid;
                }
            }
            for (int d1 = 0; d1 < 4; d1++)
                for (int d2 = 0; d2 < 4; d2++)
                    if (comp[d1] > 0 && comp[d1] == comp[d2]) canWalk[to * 16 + d1 * 4 + d2] = true;
        }

        int qh = 0, qt = 0;
        for (int gIdx = 0; gIdx < numGoals; gIdx++)
        {
            if (gIdx == pIdx) continue;
            int g = goals[gIdx];
            ulong targetBit = 1UL << gIdx;
            for (int d = 0; d < 4; d++)
            {
                int sq = g - dirs[d], supp = sq - dirs[d];
                if (IsBlocked(sq) || IsBlocked(supp)) continue;
                int sid = sq * 4 + d;
                if ((mask[sid] & targetBit) == 0) { mask[sid] |= targetBit; bfsQ[qt++] = sid; }
            }
        }
        while (qh < qt)
        {
            int curr = bfsQ[qh++];
            int to = curr / 4, dNext = curr % 4;
            ulong toMask = mask[curr];
            ComputeCanWalk(to);
            for (int d = 0; d < 4; d++)
            {
                int sq = to - dirs[d], supp = sq - dirs[d];
                if (IsBlocked(sq) || IsBlocked(supp)) continue;
                if (!canWalk[to * 16 + d * 4 + dNext]) continue;
                int prevSid = sq * 4 + d;
                ulong oldMask = mask[prevSid], newMask = oldMask | toMask;
                if (newMask != oldMask)
                {
                    mask[prevSid] = newMask;
                    if (qt == bfsQ.Length) Array.Resize(ref bfsQ, qt * 2);
                    bfsQ[qt++] = prevSid;
                }
            }
        }
        walkStamp = ws;
    }

    /// <summary>Theorem 1 on (boxes, player): mask of goal indices that are Certified Mandatory Displacements.</summary>
    public ulong ComputeSinglePinCMD(ReadOnlySpan<int> boxes, int player)
    {
        int numBoxes = boxes.Length;
        if (numBoxes <= 1 || numGoals != numBoxes) return 0;
        ulong cmdMask = 0;
        ulong allGoals = numGoals == 64 ? ulong.MaxValue : (1UL << numGoals) - 1;
        Array.Clear(isGoalBox);
        uBoxes.Clear();
        ulong emptyGoalsMask = allGoals;
        foreach (int b in boxes)
        {
            int gIdx = GoalIdx[b];
            if (gIdx >= 0) { isGoalBox[b] = true; emptyGoalsMask &= ~(1UL << gIdx); }
            else uBoxes.Add(b);
        }
        foreach (int p in boxes)
        {
            int pIdx = GoalIdx[p];
            if (pIdx < 0) continue;
            ushort pComp = pinComp[pIdx][player];
            if (pComp == 0) continue;
            ulong targetGoalsMask = allGoals & ~(1UL << pIdx);
            var pinnedMask = pinnedGoalMask[pIdx];
            var pc = pinComp[pIdx];

            if (uBoxes.Count > 0)
            {
                bool uCanDirectMatch = true;
                int uIdx = 0;
                foreach (int u in uBoxes)
                {
                    ulong reach = 0;
                    for (int d = 0; d < 4; d++)
                    {
                        int supp = u - dirs[d];
                        if (supp >= 0 && supp < cells && pc[supp] == pComp) reach |= pinnedMask[u * 4 + d];
                    }
                    reach &= emptyGoalsMask;
                    reachMask[uIdx] = reach;
                    if (reach == 0) { uCanDirectMatch = false; break; }
                    uIdx++;
                }
                if (uCanDirectMatch && MatchUnpackedDirect(uBoxes.Count, emptyGoalsMask)) continue;
            }

            Array.Fill(matchOf, -1);
            unpackedOthers.Clear();
            int otherIdx = 0;
            ulong unionMask = 0;
            bool anyZero = false;
            foreach (int bPrime in boxes)
            {
                if (bPrime == p) continue;
                ulong reach = 0;
                int gPrimeIdx = GoalIdx[bPrime];
                if (gPrimeIdx >= 0 && gPrimeIdx != pIdx) { reach |= 1UL << gPrimeIdx; matchOf[gPrimeIdx] = otherIdx; }
                else unpackedOthers.Add(otherIdx);
                for (int d = 0; d < 4; d++)
                {
                    int supp = bPrime - dirs[d];
                    if (supp >= 0 && supp < cells && pc[supp] == pComp) reach |= pinnedMask[bPrime * 4 + d];
                }
                reach &= targetGoalsMask;
                reachMask[otherIdx] = reach;
                unionMask |= reach;
                if (reach == 0) { anyZero = true; break; }
                otherIdx++;
            }
            if (anyZero || (unionMask & targetGoalsMask) != targetGoalsMask) { cmdMask |= 1UL << pIdx; continue; }
            if (!MatchUnpackedAugment(targetGoalsMask)) cmdMask |= 1UL << pIdx;
        }
        return cmdMask;
    }

    // Bipartite matching boxes -> goals with goal-bit masks: free goals are taken greedily, the seen set is one word.
    ulong mMatched, mSeen;
    bool Augment(ulong[] reach, int i, ulong allowed)
    {
        ulong m = reach[i] & allowed;
        ulong fr = m & ~mMatched;
        if (fr != 0) { int j = BitOperations.TrailingZeroCount(fr); matchOf[j] = i; mMatched |= 1UL << j; return true; }
        while ((m &= ~mSeen) != 0)
        {
            int j = BitOperations.TrailingZeroCount(m);
            mSeen |= 1UL << j;
            if (Augment(reach, matchOf[j], allowed)) { matchOf[j] = i; return true; }
        }
        return false;
    }

    bool MatchUnpackedDirect(int nUnpacked, ulong emptyGoalsMask)
    {
        mMatched = 0;
        for (int i = 0; i < nUnpacked; i++)
        {
            mSeen = 0;
            if (!Augment(reachMask, i, emptyGoalsMask)) return false;
        }
        return true;
    }

    bool MatchUnpackedAugment(ulong targetGoalsMask)
    {
        mMatched = 0;
        for (int j = 0; j < numGoals; j++) if (matchOf[j] >= 0) mMatched |= 1UL << j;
        foreach (int i in unpackedOthers)
        {
            mSeen = 0;
            if (!Augment(reachMask, i, targetGoalsMask)) return false;
        }
        return true;
    }

    /// <summary>Theorem 2: do the unpacked boxes need an occupied goal to be unpacked? candidateUnpins = pushable boxes on goals.</summary>
    public (bool needs, ulong candidateUnpins) ComputeCollectiveUnpacking(ReadOnlySpan<int> boxes, int player)
    {
        uBoxes.Clear(); eGoals.Clear();
        Array.Clear(isGoalBox);
        int nGoalBoxes = 0;
        foreach (int b in boxes)
        {
            int gIdx = GoalIdx[b];
            if (gIdx >= 0) { isGoalBox[b] = true; nGoalBoxes++; }
            else uBoxes.Add(b);
        }
        if (nGoalBoxes == 0 || uBoxes.Count == 0) return (false, 0);
        for (int gIdx = 0; gIdx < numGoals; gIdx++) if (!isGoalBox[goals[gIdx]]) eGoals.Add(gIdx);
        int k = uBoxes.Count;
        if (eGoals.Count != k) return (false, 0);
        bool IsWallAll(int c) => c < 0 || c >= cells || wall[c] || isGoalBox[c];

        stamp++;
        uint multiStamp = stamp;
        int head = 0, tail = 0;
        foreach (int gIdx in eGoals) { int g = goals[gIdx]; visited[g] = multiStamp; q[tail++] = g; }
        while (head < tail)
        {
            int c = q[head++];
            foreach (int d in dirs)
            {
                int b = c - d, supp = b - d;
                if (!IsWallAll(b) && !IsWallAll(supp) && visited[b] != multiStamp) { visited[b] = multiStamp; q[tail++] = b; }
            }
        }
        bool needs = false;
        foreach (int u in uBoxes) if (visited[u] != multiStamp) { needs = true; break; }
        if (!needs)
        {
            Array.Clear(canReach);
            for (int eIdx = 0; eIdx < eGoals.Count; eIdx++)
            {
                int g = goals[eGoals[eIdx]];
                stamp++;
                visited[g] = stamp;
                head = 0; tail = 0; q[tail++] = g;
                while (head < tail)
                {
                    int c = q[head++];
                    foreach (int d in dirs)
                    {
                        int b = c - d, supp = b - d;
                        if (!IsWallAll(b) && !IsWallAll(supp) && visited[b] != stamp) { visited[b] = stamp; q[tail++] = b; }
                    }
                }
                for (int uIdx = 0; uIdx < uBoxes.Count; uIdx++)
                    if (visited[uBoxes[uIdx]] == stamp) canReach[uIdx] |= 1UL << eIdx;
            }
            ulong unionMask = 0;
            ulong allEmptyMask = k == 64 ? ulong.MaxValue : (1UL << k) - 1;
            for (int uIdx = 0; uIdx < k; uIdx++)
            {
                if (canReach[uIdx] == 0) { needs = true; break; }
                unionMask |= canReach[uIdx];
            }
            if (!needs && (unionMask & allEmptyMask) != allEmptyMask) needs = true;
            if (!needs)
            {
                mMatched = 0;
                bool matched = true;
                for (int i = 0; i < k; i++)
                {
                    mSeen = 0;
                    if (!Augment(canReach, i, allEmptyMask)) { matched = false; break; }
                }
                if (!matched) needs = true;
            }
        }
        ulong cand = 0;
        if (needs)
            foreach (int b in boxes)
            {
                if (!isGoalBox[b]) continue;
                int gIdx = GoalIdx[b];
                foreach (int d in dirs)
                {
                    int supp = b - d, to = b + d;
                    if (!IsWallAll(supp) && !IsWallAll(to)) { cand |= 1UL << gIdx; break; }
                }
            }
        return (needs, cand);
    }
}
