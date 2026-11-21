namespace Soksolv.Search;

/// <summary>
/// -hot-sides (Go hotsides.go, exp-694): the player-side hotspot relation used by fa2 engines (Festival's (cell, side) push
/// graph). A lone box at x reaches goal g iff some state (x,*) reaches some (g,*); blocker y hits x when y, a wall for box and
/// player alike, removes a goal x reached without it.
/// </summary>
public sealed unsafe partial class FullSolver
{
    ulong[][]? hsBase;                 // per goal: floor-index bitset of cells that reach it (no blocker)
    int[]? hsComp; uint[]? hsCstamp; uint hsCs; int[] hsQ = []; uint[]? hsSeen; uint hsSs;
    int[]? hsZone; ulong[]? hsWith;

    bool HsWall(int blocker, int c) => c < 0 || c >= cells || wall[c] || c == blocker;

    void HsZonesFlood(int blocker, int[] zone)
    {
        Array.Clear(zone);
        foreach (int c in nodes.Fcell)
        {
            if (c == blocker) continue;
            hsCs++;
            int lab = 0;
            for (int j = 0; j < 4; j++)
            {
                int nn = c + dirs[j];
                if (HsWall(blocker, nn)) continue;
                if (hsCstamp![nn] != hsCs)
                {
                    lab++;
                    hsCstamp[nn] = hsCs; hsComp![nn] = lab;
                    int tail = 0; hsQ[tail++] = nn;
                    for (int k = 0; k < tail; k++)
                        foreach (int d2 in dirs)
                        {
                            int m = hsQ[k] + d2;
                            if (HsWall(blocker, m) || m == c || hsCstamp[m] == hsCs) continue;
                            hsCstamp[m] = hsCs; hsComp![m] = lab;
                            hsQ[tail++] = m;
                        }
                }
                zone[c * 4 + j] = hsComp![nn];
            }
        }
    }

    /// <summary>Sets in output the floor index of every cell a lone box reaches goal from (reverse search from all of the goal's states).</summary>
    void HsReachGoalFlood(int[] zone, int goalCell, ulong[] output)
    {
        uint st = ++hsSs;
        int qn = 0;
        for (int j = 0; j < 4; j++)
            if (zone[goalCell * 4 + j] != 0) { hsSeen![goalCell * 4 + j] = st; hsQ[qn++] = goalCell * 4 + j; }
        for (int k = 0; k < qn; k++)
        {
            int c = hsQ[k] / 4, j = hsQ[k] % 4;
            int fi = nodes.Fidx[c];
            output[fi / 64] |= 1UL << (fi % 64);
            int zc = zone[c * 4 + j];
            for (int t = 0; t < 4; t++)
                if (t != j && zone[c * 4 + t] == zc && hsSeen![c * 4 + t] != st) { hsSeen[c * 4 + t] = st; hsQ[qn++] = c * 4 + t; }
            int p = c + dirs[j];
            if (p >= 0 && p < cells && zone[p * 4 + j] != 0 && hsSeen![p * 4 + j] != st) { hsSeen[p * 4 + j] = st; hsQ[qn++] = p * 4 + j; }
        }
    }

    static readonly bool HsCheck = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_HS") == "1";   // compare with the flood reference
    CellPieces? hsPieces; byte[]? hsWallBlk; ulong[]? hsGm, hsBaseMask; byte[]? hsInq; int[]? hsQq; int hsGw;

    /// <summary>zone[c*4+j] = component (1, 2, ... by first occurrence in direction order) of c's neighbour j in the floor without the blocker and c; 0 = wall.
    /// One low-link DFS per blocker instead of a flood per (cell, neighbour) (Go hsZones).</summary>
    void HsZones(int blocker, int[] zone)
    {
        hsPieces ??= new CellPieces(cells, dirs);
        if (hsWallBlk == null) { hsWallBlk = new byte[cells]; for (int c = 0; c < cells; c++) hsWallBlk[c] = (byte)(wall[c] ? 1 : 0); }
        Buffer.BlockCopy(hsWallBlk, 0, hsPieces.Blk, 0, cells);
        if (blocker >= 0) hsPieces.Blk[blocker] = 1;
        hsPieces.Build();
        Array.Clear(zone);
        int* lab = stackalloc int[4];
        foreach (int c in nodes.Fcell)
        {
            if (c == blocker) continue;
            hsPieces.Labels(c, lab);
            for (int j = 0; j < 4; j++) zone[c * 4 + j] = lab[j] < 0 ? 0 : lab[j] + 1;
        }
        if (HsCheck)
        {
            var ref0 = new int[zone.Length]; HsZonesFlood(blocker, ref0);
            for (int i = 0; i < zone.Length; i++) if (ref0[i] != zone[i]) throw new InvalidOperationException($"hsZones blocker {blocker} state {i}: dfs {zone[i]} != flood {ref0[i]}");
        }
    }

    /// <summary>gm[state*gw + w] = bitset of the goals whose reverse search reaches the state (c*4+j) (Go hsReachGoal, all goals at once:
    /// goal bits flow along the same successor relation until a fixpoint; identical to one reverse BFS per goal).</summary>
    void HsPropagate(int[] zone, ulong[] gm)
    {
        int gw = hsGw, ns = 4 * cells;
        hsInq ??= new byte[ns]; hsQq ??= new int[ns + 1];
        var inq = hsInq; var qq = hsQq; Array.Clear(inq);
        int head = 0, tail = 0, cnt = 0, cap = qq.Length;
        for (int gi = 0; gi < goals.Length; gi++)
            for (int j = 0; j < 4; j++)
            {
                int st = goals[gi] * 4 + j;
                if (zone[st] == 0) continue;
                gm[st * gw + (gi >> 6)] |= 1UL << (gi & 63);
                if (inq[st] == 0) { inq[st] = 1; qq[tail] = st; if (++tail == cap) tail = 0; cnt++; }
            }
        while (cnt > 0)
        {
            int st = qq[head]; if (++head == cap) head = 0; cnt--; inq[st] = 0;
            int c = st >> 2, j = st & 3, zc = zone[st];
            for (int t = 0; t < 4; t++)
            {
                if (t == j) continue;
                int s2 = c * 4 + t;
                if (zone[s2] == zc) HsMerge(gm, st, s2, gw, inq, qq, ref tail, ref cnt, cap);
            }
            int p = c + dirs[j];
            if (p >= 0 && p < cells)
            {
                int s3 = p * 4 + j;
                if (zone[s3] != 0) HsMerge(gm, st, s3, gw, inq, qq, ref tail, ref cnt, cap);
            }
        }
    }
    static void HsMerge(ulong[] gm, int a, int b, int gw, byte[] inq, int[] qq, ref int tail, ref int cnt, int cap)
    {
        bool changed = false;
        for (int w = 0; w < gw; w++)
        {
            ulong nw = gm[a * gw + w] & ~gm[b * gw + w];
            if (nw != 0) { gm[b * gw + w] |= nw; changed = true; }
        }
        if (changed && inq[b] == 0) { inq[b] = 1; qq[tail] = b; if (++tail == qq.Length) tail = 0; cnt++; }
    }

    void HsInit()
    {
        hsComp = new int[cells]; hsCstamp = new uint[cells]; hsSeen = new uint[4 * cells]; hsQ = new int[4 * cells + 4];
        hsZone = new int[4 * cells]; hsWith = new ulong[nodes.Nw];
        hsGw = Math.Max(1, (goals.Length + 63) >> 6);
        HsZones(-1, hsZone);
        hsGm = new ulong[4 * cells * hsGw];
        HsPropagate(hsZone, hsGm);
        hsBaseMask = new ulong[cells * hsGw];
        for (int c = 0; c < cells; c++)
            for (int j = 0; j < 4; j++)
                for (int w = 0; w < hsGw; w++) hsBaseMask[c * hsGw + w] |= hsGm[(c * 4 + j) * hsGw + w];
        hsBase = new ulong[goals.Length][];
        for (int g = 0; g < goals.Length; g++) hsBase[g] = [];
        if (HsCheck)
            for (int g = 0; g < goals.Length; g++)
            {
                var o = new ulong[nodes.Nw]; HsReachGoalFlood(hsZone, goals[g], o);
                foreach (int c in nodes.Fcell)
                {
                    bool got = (hsBaseMask[c * hsGw + (g >> 6)] >> (g & 63) & 1) != 0, exp = (o[nodes.Fidx[c] / 64] >> (nodes.Fidx[c] % 64) & 1) != 0;
                    if (got != exp) throw new InvalidOperationException($"hs base goal {g} cell {c}: {got} != {exp}");
                }
            }
    }

    ulong[] HotRowSides(int blocker)
    {
        if (hsBase == null) HsInit();
        int idx = nodes.Fidx[blocker];
        var row = new ulong[Math.Max(1, nodes.Nw)];
        if (!goal[blocker])
        {
            HsZones(blocker, hsZone!);
            Array.Clear(hsGm!); HsPropagate(hsZone!, hsGm!);
            int gw = hsGw;
            for (int fi = 0; fi < nodes.Fcell.Length; fi++)
            {
                int x = nodes.Fcell[fi]; bool hit = false;
                for (int w = 0; w < gw && !hit; w++)
                {
                    ulong with = 0;
                    for (int j = 0; j < 4; j++) with |= hsGm![(x * 4 + j) * gw + w];
                    if ((hsBaseMask![x * gw + w] & ~with) != 0) hit = true;
                }
                if (hit) row[fi / 64] |= 1UL << (fi % 64);
            }
            row[idx / 64] &= ~(1UL << (idx % 64));
            foreach (int g in goals) { int fi = nodes.Fidx[g]; row[fi / 64] &= ~(1UL << (fi % 64)); }
            for (int fi = 0; fi < nodes.Fcell.Length; fi++)
                if (dist[nodes.Fcell[fi]] < 0) row[fi / 64] &= ~(1UL << (fi % 64));
            if (HsCheck) HsCheckRow(blocker, row, idx);
        }
        return row;
    }

    /// <summary>Reference row by the original flood-per-goal construction (SOKSOLV_CHECK_HS=1).</summary>
    void HsCheckRow(int blocker, ulong[] row, int idx)
    {
        var zone = new int[4 * cells]; HsZonesFlood(blocker, zone);
        var r2 = new ulong[row.Length]; var with = new ulong[Math.Max(1, nodes.Nw)];
        for (int g = 0; g < goals.Length; g++)
        {
            Array.Clear(with);
            HsReachGoalFlood(zone, goals[g], with);
            var bs = new ulong[Math.Max(1, nodes.Nw)];
            HsReachGoalFlood(hsZoneBase(), goals[g], bs);
            for (int k = 0; k < r2.Length; k++) r2[k] |= bs[k] & ~with[k];
        }
        r2[idx / 64] &= ~(1UL << (idx % 64));
        foreach (int g in goals) { int fi = nodes.Fidx[g]; r2[fi / 64] &= ~(1UL << (fi % 64)); }
        for (int fi = 0; fi < nodes.Fcell.Length; fi++)
            if (dist[nodes.Fcell[fi]] < 0) r2[fi / 64] &= ~(1UL << (fi % 64));
        for (int k = 0; k < r2.Length; k++) if (r2[k] != row[k]) throw new InvalidOperationException($"hotRowSides blocker {blocker}: {row[k]:x} != {r2[k]:x}");
    }
    int[]? hsZoneBase0;
    int[] hsZoneBase() { if (hsZoneBase0 == null) { hsZoneBase0 = new int[4 * cells]; HsZonesFlood(-1, hsZoneBase0); } return hsZoneBase0; }
}
