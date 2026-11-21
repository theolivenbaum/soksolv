namespace Soksolv.Search;

/// <summary>Festival-ordered pull helpers used by the dive lane (Go pullfest.go pfInit / pushDistances, pullzone.go).</summary>
public sealed unsafe partial class FullSolver
{
    int[]? pfDist;
    public int PfZoneDeadLimit = 1000;     // Go PFZoneDead (default 1000)
    public bool PfReachOn = true;          // Go PFReach (default on)

    /// <summary>
    /// Push distances with the player's access (Festival distance_from_to): visit(src, c, d) = pushes of one box from src to c on the
    /// empty board, the player walking around the box between pushes.
    /// </summary>
    public static void PushDistances(bool[] wall, int[] dirs, Action<int, int, int> visit)
    {
        int cells = wall.Length;
        var side = new int[cells * 4];
        var mark = new int[cells];
        int stamp = 0;
        var q = new int[cells];
        for (int c = 0; c < cells; c++)
        {
            for (int k = 0; k < 4; k++) side[c * 4 + k] = -1;
            if (wall[c]) continue;
            for (int k = 0; k < 4; k++)
            {
                int nn = c + dirs[k];
                if (wall[nn] || side[c * 4 + k] >= 0) continue;
                stamp++;
                mark[nn] = stamp; mark[c] = stamp; // the box blocks c
                int tail = 0; q[tail++] = nn;
                for (int i = 0; i < tail; i++)
                    foreach (int d2 in dirs)
                    {
                        int m = q[i] + d2;
                        if (!wall[m] && mark[m] != stamp) { mark[m] = stamp; q[tail++] = m; }
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
        var bq = new int[cells * 4 + 4];
        for (int src = 0; src < cells; src++)
        {
            if (wall[src]) continue;
            Array.Fill(dist, -1);
            Array.Fill(best, -1);
            int bn = 0;
            for (int k = 0; k < 4; k++)
                if (side[src * 4 + k] >= 0) { dist[src * 4 + k] = 0; bq[bn++] = src * 4 + k; }
            best[src] = 0;
            for (int i = 0; i < bn; i++)
            {
                int st = bq[i];
                int c = st / 4;
                int r = side[st];
                for (int j = 0; j < 4; j++)
                {
                    // The player on side j of c pushes the box away from j.
                    if (side[c * 4 + j] != r) continue;
                    int nc = c - dirs[j];
                    if (wall[nc]) continue;
                    int ns = nc * 4 + j; // the player now stands on c: side j of nc
                    if (dist[ns] >= 0) continue;
                    dist[ns] = dist[st] + 1;
                    if (best[nc] < 0) best[nc] = dist[ns];
                    bq[bn++] = ns;
                }
            }
            for (int c = 0; c < cells; c++) if (best[c] >= 0) visit(src, c, best[c]);
        }
    }

    /// <summary>Push distances from every cell to every real goal (the pull view's boxes at the root), with player access (PFExactDist).</summary>
    void PfInit()
    {
        if (pfDist != null) return;
        var gl = (int[])lv.Boxes.Clone(); Array.Sort(gl);
        pfDist = new int[gl.Length * cells];
        Array.Fill(pfDist, -1);
        var gi = new int[cells]; Array.Fill(gi, -1);
        for (int g = 0; g < gl.Length; g++) gi[gl[g]] = g;
        var pd = pfDist;
        PushDistances(wall, dirs, (src, c, d) => { int g = gi[c]; if (g >= 0) pd[g * cells + src] = d; });
    }

    /// <summary>Intersects the pull solver's box-goal reach with the exact single-box reach of the walls-only board (Go pfReachInit, exp-619).</summary>
    void PfReachInit()
    {
        InitMatchingIfNeeded();
        var gl = new List<int>();
        for (int c = 0; c < cells; c++) if (goal[c]) gl.Add(c);
        var r = Hub.HubReach.Create(wall, W, gl.ToArray(), realPlayer);
        if (r == null || r.Rb.Length != goals.Length) return;
        for (int g = 0; g < goals.Length; g++)
        {
            var rb = r.Rb[g];
            for (int c = 0; c < cells; c++)
                if ((rb[c >> 6] & (1UL << (c & 63))) == 0) goalAdj[c * gw + (g >> 6)] &= ~(1UL << (g & 63));
        }
    }

    void InitMatchingIfNeeded() { if (goalAdj.Length == 0) InitMatching(); }

    /// <summary>
    /// Whether the pull view's position (boxes, player) can provably never put every box on a start square: a bounded exhaustive
    /// relaxed pull search (Go pullZoneDead; reversible pulls delete the box). Exceeding limit positions answers "not dead".
    /// </summary>
    public static bool PullZoneDead(bool[] wall, int w, bool[] start, int[] dist, int[] boxes, int player, int limit)
    {
        int cells = wall.Length;
        int[] dirs = [-w, w, -1, 1];
        var occ = new bool[cells];
        var mark = new int[cells];
        int stamp = 0;
        var q = new int[cells];
        int Reach(int p)
        {
            stamp++;
            mark[p] = stamp;
            int tail = 0; q[tail++] = p; int lo = p;
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (!wall[y] && !occ[y] && mark[y] != stamp) { mark[y] = stamp; q[tail++] = y; if (y < lo) lo = y; }
                }
            return lo;
        }
        bool OnStart(int[] bx) { foreach (int b in bx) if (!start[b]) return false; return true; }
        var seen = new HashSet<string>();
        string Enc(int[] bx, int p)
        {
            var sb = new System.Text.StringBuilder(bx.Length * 4 + 4);
            foreach (int b in bx) sb.Append((char)(b & 0xFFFF)).Append((char)(b >> 16));
            sb.Append((char)(p & 0xFFFF)).Append((char)(p >> 16));
            return sb.ToString();
        }
        var fifo = new List<(int[] boxes, int player)>();
        bool Push(int[] bx, int p)
        {
            string k = Enc(bx, p);
            if (seen.Contains(k)) return false;
            if (OnStart(bx)) return true;
            seen.Add(k);
            fifo.Add((bx, p));
            return false;
        }
        var root = (int[])boxes.Clone(); Array.Sort(root);
        foreach (int b in root) if (dist[b] < 0) return true; // a box already on a dead square
        foreach (int b in root) occ[b] = true;
        int rp = Reach(player);
        foreach (int b in root) occ[b] = false;
        if (Push(root, rp)) return false;
        for (int head = 0; head < fifo.Count; head++)
        {
            if (seen.Count > limit) return false; // not proven within the limit
            var cur = fifo[head];
            foreach (int b in cur.boxes) occ[b] = true;
            Reach(cur.player);
            int region = stamp;
            var pulls = new List<(int i, int to, int np)>();
            int wobbler = -1;
            for (int i = 0; i < cur.boxes.Length && wobbler < 0; i++)
            {
                int b = cur.boxes[i];
                foreach (int d in dirs)
                {
                    // The player on b+d steps to b+2d, pulling the box onto b+d.
                    int p1 = b + d, p2 = b + 2 * d;
                    if (mark[p1] != region || wall[p2] || occ[p2] || dist[p1] < 0) continue;
                    occ[b] = false; occ[p1] = true;
                    int np = Reach(p2);
                    bool back = mark[b] == stamp && !wall[b - d] && !occ[b - d]; // the player can reach b and pull the box back onto it
                    occ[p1] = false; occ[b] = true;
                    Reach(cur.player); // restore the parent's region marks
                    region = stamp;
                    if (back) { wobbler = i; break; }
                    pulls.Add((i, p1, np));
                }
            }
            if (wobbler >= 0)
            {
                var bx = cur.boxes.Where((_, ix) => ix != wobbler).ToArray();
                occ[cur.boxes[wobbler]] = false;
                int np = Reach(cur.player);
                foreach (int b in cur.boxes) occ[b] = false;
                if (Push(bx, np)) return false;
                continue;
            }
            foreach (int b in cur.boxes) occ[b] = false;
            foreach (var pl in pulls)
            {
                var bx = (int[])cur.boxes.Clone();
                bx[pl.i] = pl.to;
                Array.Sort(bx);
                if (Push(bx, pl.np)) return false;
            }
        }
        return true;
    }

    /// <summary>Drops the pull roots whose zone is proven dead (PFZoneDead positions each); keeps all if every one is dead.</summary>
    int[] LiveZoneRoots(int[] roots)
    {
        var start = (int[])lv.Boxes.Clone();
        var live = new List<int>();
        foreach (int r in roots)
            if (!PullZoneDead(wall, W, goal, dist, start, r, PfZoneDeadLimit)) live.Add(r);
        return live.Count == 0 ? roots : [.. live];
    }
}
