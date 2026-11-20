namespace Soksolv.Search;

/// <summary>
/// Goal-area deadlocks (port of Go full/goalarea.go): boxes frozen on goals are walls; greedily pull each
/// goal box away per possible final player zone; if every zone leaves an empty surviving goal, the position is dead.
/// </summary>
public sealed class GoalArea
{
    readonly bool[] wall;
    readonly int[] goals;
    readonly int[] dirs;
    readonly uint[] mark;
    uint stamp;
    int[] q;
    int qn;
    readonly int[] wl;
    int wn;
    readonly uint[] fz, gone;
    uint fst, zst;
    readonly bool[] isGoal;
    readonly int[] fset;
    int nf;
    readonly int[] seeds;
    readonly int[] survStart;
    int nzones;

    public GoalArea(bool[] wall, int[] goals, int[] dirs)
    {
        this.wall = wall; this.goals = goals; this.dirs = dirs;
        int n = wall.Length;
        mark = new uint[n]; fz = new uint[n]; gone = new uint[n]; isGoal = new bool[n];
        q = new int[n]; wl = new int[n * 8 + 16]; fset = new int[goals.Length]; seeds = new int[n];
        survStart = new int[n + 2];
        foreach (int g in goals) isGoal[g] = true;
        survBufList = new List<int>();
    }
    readonly List<int> survBufList;

    /// <summary>Compute survivors for the frozen goal set fset[0..nf). Returns false (no claim) if no zones.</summary>
    public bool Dead(ReadOnlySpan<int> frozenGoals, bool[] occ)
    {
        nf = 0;
        foreach (int f in frozenGoals) fset[nf++] = f;
        Survivors();
        // zones listed in survStart[0..nzones], each covered check
        for (int z = 0; z < nzones; z++)
        {
            bool covered = true;
            for (int i = survStart[z]; i < survStart[z + 1]; i++)
                if (!occ[survBufList[i]]) { covered = false; break; }
            if (covered) return false;
        }
        return nzones > 0;
    }

    bool IsWall(int c) => wall[c] || fz[c] == fst;
    bool HasBox(int c) => isGoal[c] && fz[c] != fst && gone[c] != zst;
    bool Free(int c) => !IsWall(c) && !HasBox(c);
    int Reset() { zst = ++zstCounter; return goals.Length - nf; }
    uint zstCounter;

    void Survivors()
    {
        nzones = 0; survBufList.Clear(); survStart[0] = 0;
        fst++;
        for (int i = 0; i < nf; i++) fz[fset[i]] = fst;
        if (Reset() == 0) return;
        stamp++;
        uint zoneStamp = stamp;
        int ns = 0;
        for (int c = 0; c < wall.Length; c++)
            if (Free(c) && mark[c] != zoneStamp) { seeds[ns++] = c; Flood(c, zoneStamp); }
        for (int si = 0; si < ns; si++)
        {
            int seed = seeds[si];
            int left = Reset();
            stamp++;
            uint st = stamp;
            wn = 0;
            Reach(seed, st);
            while (wn > 0 && left > 0)
            {
                int b = wl[--wn];
                if (!HasBox(b)) continue;
                bool removable = false;
                foreach (int d in dirs)
                {
                    int p = b + d;
                    if (mark[p] == st && Free(p + d)) { removable = true; break; }
                }
                if (!removable) continue;
                gone[b] = zst;
                left--;
                foreach (int d in dirs)
                {
                    int c = b - 2 * d;
                    if (c >= 0 && c < wall.Length && HasBox(c)) wl[wn++] = c;
                }
                bool adjacent = false;
                foreach (int d in dirs) if (mark[b + d] == st) { adjacent = true; break; }
                if (adjacent && mark[b] != st) Reach(b, st);
            }
            int start = survBufList.Count;
            foreach (int g in goals) if (HasBox(g)) survBufList.Add(g);
            if (survBufList.Count == start)
            {
                // always covered: never dead
                survBufList.Clear(); survStart[0] = 0; survStart[1] = 0; nzones = 1;
                return;
            }
            survStart[++nzones] = survBufList.Count;
        }
    }

    void Reach(int c, uint st)
    {
        int n = 0; q[n++] = c; mark[c] = st;
        for (int i = 0; i < n; i++)
        {
            int x = q[i];
            foreach (int d in dirs)
            {
                int b = x - d;
                if (HasBox(b)) wl[wn++] = b;
                int y = x + d;
                if (Free(y) && mark[y] != st) { mark[y] = st; q[n++] = y; }
            }
        }
    }

    void Flood(int c, uint st)
    {
        int n = 0; q[n++] = c; mark[c] = st;
        for (int i = 0; i < n; i++)
        {
            int x = q[i];
            foreach (int d in dirs)
            {
                int y = x + d;
                if (Free(y) && mark[y] != st) { mark[y] = st; q[n++] = y; }
            }
        }
    }
}
