namespace Soksolv.Search;

/// <summary>Festival's out-of-plan zones for fa2 (Go oopfest.go): one zone bitset per plan step, counting is one bit test per box.</summary>
public sealed unsafe partial class FullSolver
{
    sealed class OopZones { public int W, N; public ulong[] Z = []; }
    OopZones? oopz;

    /// <summary>Every cell from which a box can be pushed onto one of targets with only blocked cells as obstacles (Go sourcesOf).</summary>
    bool[] SourcesOf(bool[] blocked, List<int> targets)
    {
        var inside = new bool[cells];
        var seen = new bool[4 * cells];
        var mk = new uint[cells];
        uint stampL = 0;
        var q = new List<int>(); var pq = new List<int>();
        bool Free(int c, int box) => !blocked[c] && c != box;
        foreach (int g in targets)
        {
            if (blocked[g]) continue;
            inside[g] = true;
            for (int di = 0; di < 4; di++)
                if (Free(g + dirs[di], g)) { int k = g * 4 + di; if (!seen[k]) { seen[k] = true; q.Add(k); } }
        }
        for (int i = 0; i < q.Count; i++)
        {
            int k = q[i];
            int b = k / 4, d = dirs[k % 4];
            int to = b + d, p = b + 2 * d;
            if (!Free(to, b) || !Free(p, b)) continue;
            inside[to] = true;
            stampL++;
            pq.Clear(); pq.Add(p); mk[p] = stampL;
            for (int j = 0; j < pq.Count; j++)
                foreach (int off in dirs)
                {
                    int x = pq[j] + off;
                    if (mk[x] != stampL && Free(x, to)) { mk[x] = stampL; pq.Add(x); }
                }
            for (int di = 0; di < 4; di++)
            {
                int k2 = to * 4 + di;
                if (mk[to + dirs[di]] == stampL && !seen[k2]) { seen[k2] = true; q.Add(k2); }
            }
        }
        return inside;
    }

    OopZones BuildOopZones(FestPlan fp)
    {
        var z = new OopZones { W = (cells + 63) >> 6 };
        var goalList = new List<int>();
        for (int c = 0; c < cells; c++) if (goal[c]) goalList.Add(c);
        bool[] possible = SourcesOf(wall, goalList);
        z.N = fp.E.Count;
        var parked = new List<int>[z.N]; var targets = new List<int>[z.N];
        for (int i = 0; i < z.N; i++)
        {
            var pk = new List<int>(); var tg = new List<int>();
            for (int j = 0; j <= i; j++) if (fp.E[j].Until == -1) pk.Add(fp.E[j].To);
            for (int j = i + 1; j < z.N; j++) tg.Add(fp.E[j].To);
            foreach (var (cell, until) in fp.OopPark) if (until < 0 || i < until) tg.Add(cell);
            parked[i] = pk; targets[i] = tg;
        }
        if (z.N < 2) { z.N = 0; return z; }
        var raw = new ulong[z.N][];
        for (int i = 0; i < z.N - 1; i++)
        {
            var blocked = (bool[])wall.Clone();
            foreach (int c in parked[i]) blocked[c] = true;
            var tg = new List<int>();
            foreach (int c in targets[i]) if (!blocked[c]) tg.Add(c);
            bool[] src = SourcesOf(blocked, tg);
            var row = new ulong[z.W];
            for (int c = 0; c < cells; c++)
                if (!blocked[c] && possible[c] && !src[c]) row[c >> 6] |= 1UL << (c & 63);
            raw[i] = row;
        }
        z.Z = new ulong[z.N * z.W];
        var cur = new ulong[z.W];
        int last = 0;
        for (int i = 0; i < z.N - 1; i++)
        {
            bool same = true;
            for (int k = 0; k < cur.Length; k++) if (raw[i][k] != cur[k]) { same = false; break; }
            if (same) continue;
            Array.Copy(raw[i], cur, cur.Length);
            for (int j = last; j <= i; j++) Array.Copy(cur, 0, z.Z, j * z.W, z.W);
            last = i;
        }
        if (opt.Fa2.OopTail)
            for (int j = last + 1; j < z.N - 1; j++) Array.Copy(raw[j], 0, z.Z, j * z.W, z.W);
        return z;
    }

    /// <summary>Boxes in the zone of the plan's current step (Go oopFestCount, plan-file/Festival packed path).</summary>
    int OopFestCount(ReadOnlySpan<int> boxes, int packed)
    {
        oopz ??= BuildOopZones(fa2Fp!);
        var z = oopz;
        if (z.N == 0) return 0;
        int step = packed < 0 ? fa2Fp!.Packed(occ) : packed;
        if (step >= z.N - 1) return 0;
        int baseI = step * z.W, cnt = 0;
        foreach (int b in boxes) cnt += (int)(z.Z[baseI + (b >> 6)] >> (b & 63) & 1);
        return cnt;
    }
}
