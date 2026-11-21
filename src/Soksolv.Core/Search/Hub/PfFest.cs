namespace Soksolv.Search.Hub;

/// <summary>
/// Leaf functions of Go pullfest.go (Festival-ordered backward packing search, exp-275/281/285), verified against
/// Go goldens (tests/Soksolv.Tests/golden/pffest.json): Festival's player-aware push distances (distance_from_to),
/// the matching-distance table to the real goals, hotspot count (boxes no player can pull), biconnectivity count and
/// spread sum. The solver hooks (pfAdd/pfReparent, advisor, move scoring) are NOT ported yet — see docs/lanes.md.
/// </summary>
public sealed class PfFest
{
    readonly bool[] wall;
    readonly int w, cells;
    readonly int[] dirs;
    readonly bool[] isGoal;
    /// <summary>PfDist[g*cells + c]: pushes to bring a box on c to real goal g (-1 never); goals ascending.</summary>
    public readonly int[] PfDist;
    public readonly int NGoals;
    int[]? scored;
    // bicon scratch
    readonly int[] disc, low; readonly uint[] bcOn; uint bcStamp; int[] bcStack = new int[0];

    public PfFest(bool[] wall, int w, ReadOnlySpan<int> goalsSorted, bool exactDist = true)
    {
        this.wall = wall; this.w = w; cells = wall.Length; dirs = [-w, w, -1, 1];
        NGoals = goalsSorted.Length;
        isGoal = new bool[cells];
        foreach (int g in goalsSorted) isGoal[g] = true;
        disc = new int[cells]; low = new int[cells]; bcOn = new uint[cells];
        PfDist = new int[NGoals * cells];
        Array.Fill(PfDist, -1);
        if (exactDist)
        {
            var gi = new int[cells]; Array.Fill(gi, -1);
            for (int g = 0; g < NGoals; g++) gi[goalsSorted[g]] = g;
            PushDistances(wall, dirs, (src, c, d) => { int g = gi[c]; if (g >= 0) PfDist[g * cells + src] = d; });
            return;
        }
        for (int g = 0; g < NGoals; g++)
        {
            int goal = goalsSorted[g];
            var d = PfDist.AsSpan(g * cells, cells);
            d[goal] = 0;
            var q = new List<int> { goal };
            for (int i = 0; i < q.Count; i++)
            {
                int c = q[i];
                foreach (int dir in dirs)
                {
                    int b = c - dir; // a box on b pushed by a player on b-dir lands on c
                    if (!wall[b] && !wall[b - dir] && d[b] < 0) { d[b] = d[c] + 1; q.Add(b); }
                }
            }
        }
    }

    /// <summary>Boxes no player can pull: no side with a floor neighbour and a free cell beyond it (occ marks the boxes).</summary>
    public int Hot(ReadOnlySpan<int> boxes, bool[] occ)
    {
        int n = 0;
        foreach (int b in boxes)
        {
            bool ok = false;
            foreach (int d in dirs)
                if (!wall[b + d] && !wall[b + 2 * d] && !occ[b + 2 * d]) { ok = true; break; }
            if (!ok) n++;
        }
        return n;
    }

    /// <summary>Free floor squares on no cycle of the free-floor graph (occ = boxes), skipping free real goals (Festival biconnect_score).</summary>
    public int Bicon(bool[] occ)
    {
        bcStamp += 2;
        uint vis = bcStamp, on = bcStamp + 1;
        int t = 0;
        for (int root = 0; root < cells; root++)
        {
            if (wall[root] || occ[root] || bcOn[root] >= vis) continue;
            int sp = 0;
            void Push(int a, int b, int c) { if (sp + 3 > bcStack.Length) Array.Resize(ref bcStack, Math.Max(64, bcStack.Length * 2)); bcStack[sp++] = a; bcStack[sp++] = b; bcStack[sp++] = c; }
            Push(root, -1, 0);
            bcOn[root] = vis; disc[root] = low[root] = t; t++;
            while (sp > 0)
            {
                int k = sp - 3;
                int u = bcStack[k], par = bcStack[k + 1], d = bcStack[k + 2];
                if (d < 4)
                {
                    bcStack[k + 2]++;
                    int v = u + dirs[d];
                    if (wall[v] || occ[v] || v == par) continue;
                    if (bcOn[v] >= vis)
                    {
                        low[u] = Math.Min(low[u], disc[v]);
                        bcOn[u] = on; bcOn[v] = on;
                        continue;
                    }
                    bcOn[v] = vis; disc[v] = low[v] = t; t++;
                    Push(v, u, 0);
                    continue;
                }
                sp = k;
                if (par >= 0)
                {
                    low[par] = Math.Min(low[par], low[u]);
                    if (low[u] <= disc[par]) { bcOn[u] = on; bcOn[par] = on; }
                }
            }
        }
        int n = 0;
        for (int c = 0; c < cells; c++)
            if (!wall[c] && !occ[c] && bcOn[c] != on && !isGoal[c]) n++;
        return n;
    }

    /// <summary>The scored distance of boxes: each cell's positive distances to every real goal, summed, computed once.</summary>
    public int SpreadSum(ReadOnlySpan<int> boxes)
    {
        if (scored == null)
        {
            scored = new int[cells];
            for (int g = 0; g < NGoals; g++)
                for (int c = 0; c < cells; c++) { int d = PfDist[g * cells + c]; if (d > 0) scored[c] += d; }
        }
        int sum = 0;
        foreach (int b in boxes) sum += scored[b];
        return sum;
    }

    /// <summary>Festival's pull HF order tuple (dist: larger is better; -1 = no matching).</summary>
    public readonly record struct Tuple(int Reg, int Rooms, int Hot, int Dist, int Bic)
    {
        public static bool Better(Tuple a, Tuple b)
        {
            if (a.Reg != b.Reg) return a.Reg < b.Reg;
            if (a.Rooms != b.Rooms) return a.Rooms < b.Rooms;
            if (a.Reg == 1 && a.Hot != b.Hot) return a.Hot < b.Hot;
            if (a.Bic != b.Bic) return a.Bic < b.Bic;
            return a.Dist > b.Dist;
        }
        /// <summary>Packed mobility key of Go pfScore: biconnectivity count (fewer first) above matching distance (larger first, shifted by one).</summary>
        public int Mobility => (4095 - Math.Min(Bic, 4095)) << 16 | Math.Min(Dist + 1, 65535);
        public static Tuple FromMove(int oop, int room, int hot, int mobility) => new(oop, room, hot, (mobility & 0xffff) - 1, 4095 - (mobility >> 16));
    }

    /// <summary>
    /// For every floor cell src and every cell c a lone box on src can be pushed to, d pushes away, calls visit(src, c, d):
    /// a BFS over (box cell, player side) on the empty board; the player walks around the box between pushes.
    /// </summary>
    public static void PushDistances(bool[] wall, int[] dirs, Action<int, int, int> visit)
    {
        int cells = wall.Length;
        var side = new int[cells * 4];
        var mark = new int[cells];
        int stamp = 0;
        var q = new List<int>();
        for (int c = 0; c < cells; c++)
        {
            for (int k = 0; k < 4; k++) side[c * 4 + k] = -1;
            if (wall[c]) continue;
            for (int k = 0; k < 4; k++)
            {
                int n = c + dirs[k];
                if (wall[n] || side[c * 4 + k] >= 0) continue;
                stamp++;
                mark[n] = stamp; mark[c] = stamp; // the box blocks c
                q.Clear(); q.Add(n);
                for (int i = 0; i < q.Count; i++)
                    foreach (int d2 in dirs)
                    {
                        int m = q[i] + d2;
                        if (!wall[m] && mark[m] != stamp) { mark[m] = stamp; q.Add(m); }
                    }
                for (int k2 = 0; k2 < 4; k2++)
                {
                    int n2 = c + dirs[k2];
                    if (!wall[n2] && mark[n2] == stamp && n2 != c) side[c * 4 + k2] = k;
                }
            }
        }
        var dist = new int[cells * 4];
        var best = new int[cells];
        var bq = new List<int>();
        for (int src = 0; src < cells; src++)
        {
            if (wall[src]) continue;
            Array.Fill(dist, -1); Array.Fill(best, -1);
            bq.Clear();
            for (int k = 0; k < 4; k++)
                if (side[src * 4 + k] >= 0) { dist[src * 4 + k] = 0; bq.Add(src * 4 + k); }
            best[src] = 0;
            for (int i = 0; i < bq.Count; i++)
            {
                int st = bq[i];
                int c = st / 4;
                int r = side[st];
                for (int j = 0; j < 4; j++)
                {
                    if (side[c * 4 + j] != r) continue; // the player on side j of c pushes the box away from j
                    int nc = c - dirs[j];
                    if (wall[nc]) continue;
                    int ns = nc * 4 + j;
                    if (dist[ns] >= 0) continue;
                    dist[ns] = dist[st] + 1;
                    if (best[nc] < 0) best[nc] = dist[ns];
                    bq.Add(ns);
                }
            }
            for (int c = 0; c < cells; c++) if (best[c] >= 0) visit(src, c, best[c]);
        }
    }
}
