using System.Runtime.CompilerServices;

namespace Soksolv.Search;

/// <summary>Static board data for the cluster checker (walls, goals, dead squares). Port of Go cluster.Board.</summary>
public sealed class ClusterBoard
{
    public readonly bool[] Wall, Goal, Dead;
    public int[]? Dist;
    public readonly int W;
    public readonly int[] Dirs;
    public readonly int NFloor;
    public readonly ulong Salt;
    public ClusterBoard(bool[] wall, bool[] goal, bool[] dead, int w, bool salt)
    {
        Wall = wall; Goal = goal; Dead = dead; W = w; Dirs = [-w, w, -1, 1];
        foreach (bool x in wall) if (!x) NFloor++;
        Salt = salt ? BoardSalt(wall, goal, dead) : 0;
    }
    static ulong BoardSalt(bool[] wall, bool[] goal, bool[] dead)
    {
        ulong h = (ulong)wall.Length * 0x9E3779B97F4A7C15UL;
        for (int i = 0; i < wall.Length; i++)
        {
            ulong v = 0;
            if (wall[i]) v |= 1;
            if (i < goal.Length && goal[i]) v |= 2;
            if (i < dead.Length && dead[i]) v |= 4;
            h = (h ^ v) * 0xBF58476D1CE4E5B9UL;
            h ^= h >> 29;
        }
        return h;
    }
}

/// <summary>Lossy direct-mapped table of 64-bit keys, shareable between searchers (port of cluster.Table).</summary>
public sealed class ClusterTable
{
    readonly ulong[] slots;
    public long Proven, Hits;
    public ClusterTable(int log) { slots = new ulong[1 << log]; }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    ulong Load(ulong k) => Volatile.Read(ref slots[(int)(k & (ulong)(slots.Length - 1))]);
    public bool Has(ulong k) => Load(k) == k;
    public void Add(ulong k) => Put(k, k);
    void Put(ulong v, ulong k)
    {
        Volatile.Write(ref slots[(int)(k & (ulong)(slots.Length - 1))], v);
        Interlocked.Increment(ref Proven);
    }
    public bool Penalty(ulong k, out int pen)
    {
        ulong v = Load(k);
        if ((v & ~0xFFUL) != (k & ~0xFFUL)) { pen = 0; return false; }
        pen = (int)(v & 0xFF); return true;
    }
    public void AddPenalty(ulong k, int pen) => Put((k & ~0xFFUL) | (uint)Math.Min(pen, 255), k);
}

/// <summary>
/// Cluster deadlock checker (port of Go package cluster, default configuration). A cluster is the box just
/// pushed plus the boxes touching it (8-neighbourhood, up to MaxBoxes). With every other box removed, an
/// exhaustive capped BFS over the cluster's pushes decides whether all its boxes can ever stand on goals.
/// The proof-search state set is a packed open-addressing table (no per-state allocation).
/// </summary>
public sealed class ClusterChecker
{
    public static int MaxBoxes = 5;
    public static int StateCap = 1000;

    public ClusterTable? Shared, SharedPen;
    public bool Private;
    public long Checked;
    public int Pen { get; private set; }

    readonly ClusterBoard b;
    readonly ulong[] proven = new ulong[1 << 18];
    readonly bool[] occ;
    readonly uint[] mark;
    uint stamp;
    readonly int[] queue;
    int qlen;

    // proof-search scratch
    Key[] cq = new Key[2048];
    int[] cdepth = new int[2048];
    ulong[] seenSlots = new ulong[8192]; // (stamp<<32) | (queueIndex+1)
    uint seenStamp;

    struct Key : IEquatable<Key>
    {
        public ulong Lo, Hi; // up to 7 box cells + player, 16 bits each
        public bool Equals(Key o) => Lo == o.Lo && Hi == o.Hi;
        public override bool Equals(object? o) => o is Key k && Equals(k);
        public override int GetHashCode() => (int)Hash();
        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        public ulong Hash() { ulong h = (Lo ^ (Hi * 0x9E3779B97F4A7C15UL)) * 0xBF58476D1CE4E5B9UL; return h ^ (h >> 32); }
    }

    public ClusterChecker(ClusterBoard board)
    {
        b = board;
        int n = board.Wall.Length;
        occ = new bool[n]; mark = new uint[n]; queue = new int[n];
    }

    static ulong KeyOf(ReadOnlySpan<int> cells, int side)
    {
        ulong h = (ulong)side * 0x9E3779B97F4A7C15UL + (ulong)cells.Length;
        foreach (int c in cells)
        {
            h = (h ^ (ulong)(long)c) * 0xBF58476D1CE4E5B9UL;
            h ^= h >> 31;
        }
        return h | 1;
    }

    /// <summary>
    /// Whether the box just pushed to t, with the boxes touching it (occ marks every box of the state), is a
    /// dead cluster given the player stands on side. regionMark/regionStamp/extra define "in the player's region":
    /// regionMark[y]==regionStamp || y==extra.
    /// </summary>
    public bool Dead(bool[] occIn, int t, int side, uint[] regionMark, uint regionStamp, int extra)
    {
        Pen = 0;
        Span<int> cl = stackalloc int[8];
        int n = 1; cl[0] = t;
        int w = b.W;
        Span<int> ring = [-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1];
        for (int i = 0; i < n && n < MaxBoxes; i++)
            foreach (int o in ring)
            {
                int x = cl[i] + o;
                if (x >= 0 && x < occIn.Length && occIn[x] && n < MaxBoxes && !cl[..n].Contains(x)) cl[n++] = x;
            }
        if (n < 3) return false; // pairs are handled by the pair checks
        bool allGoals = true;
        for (int i = 0; i < n; i++) if (!b.Goal[cl[i]]) { allGoals = false; break; }
        if (allGoals) return false;
        bool cut = false;
        for (int i = 0; i < n && !cut; i++)
            foreach (int d in b.Dirs)
            {
                int y = cl[i] + d;
                if (!b.Wall[y] && !occIn[y] && !(regionMark[y] == regionStamp || y == extra)) { cut = true; break; }
            }
        if (!cut) return false;
        var cells = cl[..n];
        cells.Sort();
        ulong k = KeyOf(cells, side) ^ (b.Salt & ~0xFFUL);
        ClusterTable? shared = null, penalties = null;
        if (!Private) { shared = Shared; penalties = SharedPen; }
        if (Local(k, out int lp))
        {
            Pen = lp;
            if (lp == 0 && b.Dist != null && penalties != null && penalties.Penalty(k, out int p2)) Pen = p2;
            return false;
        }
        if (shared != null && shared.Has(k)) { Interlocked.Increment(ref shared.Hits); return true; }
        for (int i = 0; i < n; i++) occ[cells[i]] = true;
        Region(side);
        bool split = qlen < b.NFloor - n;
        for (int i = 0; i < n; i++) occ[cells[i]] = false;
        if (!split) { SetLocal(k, 0); return false; }
        var (dead, cost) = SearchCap(cells, side, StateCap);
        if (dead) shared?.Add(k);
        else
        {
            SetLocal(k, 0);
            if (b.Dist != null && cost > 0)
            {
                int est = 0;
                for (int i = 0; i < n; i++) est += b.Dist[cells[i]];
                int p = cost - est;
                if (p > 0) { SetLocal(k, p); Pen = p; penalties?.AddPenalty(k, p); }
            }
        }
        return dead;
    }

    /// <summary>Floods the player's region from p (per occ); returns its minimum cell. Cells hold stamp in mark.</summary>
    int Region(int p)
    {
        uint st = ++stamp;
        var q = queue; var wall = b.Wall; var dirs = b.Dirs;
        int tail = 0; q[tail++] = p; mark[p] = st; int m = p;
        for (int i = 0; i < tail; i++)
        {
            int c = q[i];
            foreach (int d in dirs)
            {
                int y = c + d;
                if (mark[y] != st && !wall[y] && !occ[y]) { mark[y] = st; q[tail++] = y; if (y < m) m = y; }
            }
        }
        qlen = tail;
        return m;
    }

    bool Local(ulong k, out int pen)
    {
        ulong v = proven[(int)(k & (ulong)(proven.Length - 1))];
        if ((v & 0x80) == 0 || (v & ~0xFFUL) != (k & ~0xFFUL)) { pen = 0; return false; }
        pen = (int)(v & 0x7F); return true;
    }
    void SetLocal(ulong k, int pen) => proven[(int)(k & (ulong)(proven.Length - 1))] = (k & ~0xFFUL) | 0x80 | (uint)Math.Min(pen, 127);

    static Key MkKey(ReadOnlySpan<int> bx, int player)
    {
        // boxes sorted ascending; cells < 32768 fit 16 bits; unused slots 0xFFFF
        ulong lo = ulong.MaxValue, hi = ulong.MaxValue;
        for (int i = 0; i < bx.Length; i++)
        {
            int sh = (i & 3) * 16;
            if (i < 4) lo = (lo & ~(0xFFFFUL << sh)) | ((ulong)(ushort)bx[i] << sh);
            else hi = (hi & ~(0xFFFFUL << sh)) | ((ulong)(ushort)bx[i] << sh);
        }
        // player in the last slot (index 7)
        hi = (hi & ~(0xFFFFUL << 48)) | ((ulong)(ushort)player << 48);
        return new Key { Lo = lo, Hi = hi };
    }

    /// <summary>Exhaustive BFS over cluster positions (other boxes removed). Returns (dead, cost): cost = BFS depth of the first all-on-goals state when alive, else 0.</summary>
    public (bool dead, int cost) SearchCap(ReadOnlySpan<int> start, int side, int capStates)
    {
        Checked++;
        int n = start.Length;
        if (n > 7) return (false, 0);
        for (int i = 0; i < n; i++) occ[start[i]] = true;
        int p0 = Region(side);
        for (int i = 0; i < n; i++) occ[start[i]] = false;

        int tsize = 8192;
        while (tsize < capStates * 4 + 128) tsize <<= 1;
        if (seenSlots.Length < tsize) seenSlots = new ulong[tsize];
        int tmask = seenSlots.Length - 1;
        if (++seenStamp == 0) { Array.Clear(seenSlots); seenStamp = 1; }
        uint ss = seenStamp;
        int qn = 0;
        int seenCount = 0;

        bool TryAdd(in Key k, int depth)
        {
            int i = (int)(k.Hash() & (ulong)tmask);
            while (true)
            {
                ulong s = seenSlots[i];
                if ((uint)(s >> 32) != ss) break;
                if (cq[(int)(uint)s - 1].Equals(k)) return false;
                i = (i + 1) & tmask;
            }
            if (qn == cq.Length) { Array.Resize(ref cq, qn * 2); Array.Resize(ref cdepth, qn * 2); }
            cq[qn] = k; cdepth[qn] = depth; qn++;
            seenSlots[i] = ((ulong)ss << 32) | (uint)qn;
            seenCount++;
            return true;
        }

        Span<int> cur = stackalloc int[8];
        Span<int> nb = stackalloc int[8];
        cur = cur[..n]; nb = nb[..n];
        TryAdd(MkKey(start, p0), 0);
        Span<Move> moves = stackalloc Move[32];
        var goal = b.Goal; var wall = b.Wall; var dead = b.Dead; var dirs = b.Dirs;
        for (int head = 0; head < qn; head++)
        {
            if (seenCount > capStates) return (false, 0);
            var st = cq[head]; int depth = cdepth[head];
            for (int i = 0; i < n; i++) cur[i] = (int)((i < 4 ? st.Lo >> (i * 16) : st.Hi >> ((i - 4) * 16)) & 0xFFFF);
            int stp = (int)((st.Hi >> 48) & 0xFFFF);
            for (int i = 0; i < n; i++) occ[cur[i]] = true;
            Region(stp);
            uint rs = stamp;
            int minR = stp; // the stored player is the region's minimum cell
            int nm = 0;
            for (int i = 0; i < n; i++)
            {
                int x = cur[i];
                foreach (int d in dirs)
                {
                    int to = x + d;
                    if (mark[x - d] == rs && !wall[to] && !occ[to] && !dead[to])
                        moves[nm++] = new Move { I = i, To = to, From = x, Np = FastPlayer(x, to, minR, rs) };
                }
            }
            for (int i = 0; i < n; i++) occ[cur[i]] = false;
            for (int mi = 0; mi < nm; mi++)
            {
                ref var m = ref moves[mi];
                cur.CopyTo(nb);
                nb[m.I] = m.To;
                nb.Sort();
                bool done = true;
                for (int i = 0; i < n; i++) if (!goal[nb[i]]) { done = false; break; }
                if (done) return (false, depth + 1);
                int np = m.Np;
                if (np < 0)
                {
                    for (int i = 0; i < n; i++) occ[nb[i]] = true;
                    np = Region(m.From);
                    for (int i = 0; i < n; i++) occ[nb[i]] = false;
                }
                TryAdd(MkKey(nb, np), depth + 1);
            }
        }
        return (true, 0);
    }

    struct Move { public int I, To, From, Np; }

    /// <summary>See Go cluster.fastPlayer: child's normalized player after a box moves x -> t, or -1 if a flood is needed.</summary>
    int FastPlayer(int x, int t, int minR, uint rs)
    {
        foreach (int d in b.Dirs)
        {
            int y = x + d;
            if (y != t && !b.Wall[y] && !occ[y] && mark[y] != rs) return -1;
        }
        if (t == minR) return -1;
        int w = b.W;
        Span<int> ring = [-w - 1, -w, -w + 1, 1, w + 1, w, w - 1, -1]; // circular; odd indices are orthogonal
        Span<bool> free = stackalloc bool[8];
        int start = -1;
        for (int k = 0; k < 8; k++)
        {
            int y = t + ring[k];
            free[k] = !b.Wall[y] && (y == x || !occ[y]);
            if (!free[k] && start < 0) start = k;
        }
        if (start < 0) return Math.Min(minR, x);
        int runsWithOrth = 0;
        bool inRun = false, hasOrth = false;
        for (int i = 1; i <= 8; i++)
        {
            int k = (start + i) % 8;
            if (free[k])
            {
                if (!inRun) { inRun = true; hasOrth = false; }
                if (k % 2 == 1) hasOrth = true;
            }
            else if (inRun)
            {
                if (hasOrth) runsWithOrth++;
                inRun = false;
            }
        }
        if (inRun && hasOrth) runsWithOrth++;
        if (runsWithOrth > 1) return -1;
        return Math.Min(minR, x);
    }
}
