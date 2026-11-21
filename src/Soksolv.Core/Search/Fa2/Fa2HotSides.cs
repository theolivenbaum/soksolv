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

    void HsZones(int blocker, int[] zone)
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
    void HsReachGoal(int[] zone, int goalCell, ulong[] output)
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

    void HsInit()
    {
        hsComp = new int[cells]; hsCstamp = new uint[cells]; hsSeen = new uint[4 * cells]; hsQ = new int[4 * cells + 4];
        hsZone = new int[4 * cells]; hsWith = new ulong[nodes.Nw];
        HsZones(-1, hsZone);
        hsBase = new ulong[goals.Length][];
        for (int g = 0; g < goals.Length; g++) { hsBase[g] = new ulong[nodes.Nw]; HsReachGoal(hsZone, goals[g], hsBase[g]); }
    }

    ulong[] HotRowSides(int blocker)
    {
        if (hsBase == null) HsInit();
        int idx = nodes.Fidx[blocker];
        var row = new ulong[Math.Max(1, nodes.Nw)];
        if (!goal[blocker])
        {
            HsZones(blocker, hsZone!);
            for (int g = 0; g < goals.Length; g++)
            {
                Array.Clear(hsWith!);
                HsReachGoal(hsZone!, goals[g], hsWith!);
                for (int k = 0; k < row.Length; k++) row[k] |= hsBase![g][k] & ~hsWith![k];
            }
            row[idx / 64] &= ~(1UL << (idx % 64));
            foreach (int g in goals) { int fi = nodes.Fidx[g]; row[fi / 64] &= ~(1UL << (fi % 64)); }
            for (int fi = 0; fi < nodes.Fcell.Length; fi++)
                if (dist[nodes.Fcell[fi]] < 0) row[fi / 64] &= ~(1UL << (fi % 64));
        }
        return row;
    }
}
