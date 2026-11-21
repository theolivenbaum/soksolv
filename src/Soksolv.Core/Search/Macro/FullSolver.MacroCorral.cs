namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- frontier corral (Go corral_frontier.go: FrontierCorral) ------------------------------------------------------------------
    uint[]? cfZone, cfBarrier, cfReach; uint cfZstamp, cfRstamp;
    int[]? cfQ, cfRq, cfBoxes, cfBest; int cfBestN;

    /// <summary>
    /// Returns a stamp on the selected barrier boxes (in cmark), or 0. Let B fence an inaccessible empty-floor component U.
    /// Remove all other boxes; reject if the player can then enter U; require every legal first B push to go into U and be
    /// executable on the full board now (parent's region stamped st in mark). Then B pushes suffice.
    /// </summary>
    uint FirstPushCorral(int player, uint st)
    {
        if (cfZone == null)
        {
            cfZone = new uint[cells]; cfBarrier = new uint[cells]; cfReach = new uint[cells];
            cfQ = new int[cells]; cfRq = new int[cells]; cfBoxes = new int[cells]; cfBest = new int[cells];
        }
        if (cfZstamp > 1u << 31) { Array.Clear(cfZone); Array.Clear(cfBarrier); cfZstamp = 0; }
        uint baseS = cfZstamp;
        cfBestN = 0;
        for (int start = 0; start < cells; start++)
        {
            if (wall[start] || occ[start] || mark[start] == st || cfZone[start] > baseS) continue;
            uint zs = ++cfZstamp;
            cfZone[start] = zs;
            int qn = 0; cfQ![qn++] = start;
            int nb = 0;
            bool unsolved = false;
            for (int head = 0; head < qn; head++)
            {
                int x = cfQ[head];
                unsolved = unsolved || goal[x];
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (wall[y]) continue;
                    if (occ[y])
                    {
                        if (cfBarrier![y] != zs) { cfBarrier[y] = zs; cfBoxes![nb++] = y; unsolved = unsolved || !goal[y]; }
                    }
                    else if (cfZone[y] != zs) { cfZone[y] = zs; cfQ[qn++] = y; }
                }
            }
            if (!unsolved || nb == 0 || (cfBestN > 0 && nb >= cfBestN)) continue;
            if (cfRstamp > 1u << 31) { Array.Clear(cfReach); cfRstamp = 0; }
            uint rs = ++cfRstamp;
            cfReach![player] = rs;
            int rn = 0; cfRq![rn++] = player;
            bool ok = true;
            for (int head = 0; head < rn; head++)
            {
                int x = cfRq[head];
                if (cfZone[x] == zs) { ok = false; break; }
                foreach (int d in dirs)
                {
                    int y = x + d;
                    if (!wall[y] && cfBarrier![y] != zs && cfReach[y] != rs) { cfReach[y] = rs; cfRq[rn++] = y; }
                }
            }
            int pushes = 0;
            if (ok)
            {
                for (int bi = 0; bi < nb && ok; bi++)
                {
                    int b = cfBoxes![bi];
                    foreach (int d in dirs)
                    {
                        int t = b + d, stand = b - d;
                        if (wall[t] || cfBarrier![t] == zs || cfReach[stand] != rs || dist[t] < 0) continue;
                        if (cfZone[t] != zs || mark[stand] != st || occ[t]) { ok = false; break; }
                        pushes++;
                    }
                }
            }
            if (ok && pushes > 0) { Array.Copy(cfBoxes!, cfBest!, nb); cfBestN = nb; }
        }
        if (cfBestN == 0) return 0;
        if (cstamp > 1u << 31) { Array.Clear(cmark); cstamp = 0; }
        cstamp++;
        for (int i = 0; i < cfBestN; i++) cmark[cfBest![i]] = cstamp;
        stats.FrontierCorrals++;
        return cstamp;
    }
}
