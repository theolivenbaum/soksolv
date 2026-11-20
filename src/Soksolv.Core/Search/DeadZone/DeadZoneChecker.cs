using System.Numerics;

namespace Soksolv.Search.DeadZone;

/// <summary>Static board for the corral deadlock prover (port of Go deadzone.Board).</summary>
public sealed class DeadZoneBoard
{
    public readonly int W;
    public readonly bool[] Wall, Goal, Dead;
    public readonly int[] Dirs;
    public readonly ulong Salt;
    public DeadZoneBoard(bool[] wall, bool[] goal, bool[]? dead, int w, bool salt = true)
    {
        W = w; Wall = wall; Goal = goal; Dead = dead ?? new bool[wall.Length]; Dirs = [-w, w, -1, 1];
        if (salt)
        {
            ulong h = (ulong)wall.Length * 0x9E3779B97F4A7C15UL;
            for (int i = 0; i < wall.Length; i++)
            {
                ulong v = 0;
                if (wall[i]) v |= 1;
                if (i < goal.Length && goal[i]) v |= 2;
                if (i < Dead.Length && Dead[i]) v |= 4;
                h = (h ^ v) * 0xBF58476D1CE4E5B9UL;
                h ^= h >> 29;
            }
            Salt = h;
        }
    }
}

/// <summary>Verdict cache shared between searchers of one level: key | verdict (1 dead, 2 alive).</summary>
public sealed class DeadZoneTable
{
    internal readonly ulong[] Slots;
    public DeadZoneTable(int log) { Slots = new ulong[1 << log]; }
}

/// <summary>
/// Corral deadlock prover (port of Go package deadzone, default configuration: Wide, Kill, Shrink, ShrinkAll,
/// Escape, Pairs, Groups, LocalFixed, Matching, FastKey; Near off). A corral is empty floor the player cannot
/// reach. Keep only the boxes fencing it (the zone), turn immovable boxes into walls, remove every other box
/// and search the zone boxes' pushes alone; if the search exhausts without all zone boxes on goals the
/// position is dead. Scan (non-bitset) floods: result-identical to Go's word-parallel path.
/// </summary>
public sealed class DeadZoneChecker
{
    public static bool Groups = true, Pairs = true, LocalFixed = true, Wide = true, Kill = true, Shrink = true, ShrinkAll = true,
        Escape = true, Matching = true, FastKey = true;

    public static readonly bool Trace = Environment.GetEnvironmentVariable("DZTRACE") == "1";
    public bool Private, ReadOnly;
    public DeadZoneTable? Shared;
    public int Cap;
    public long Calls, Corrals, Searches, CacheHits, Rechecks, DeadCount, Frozen, Unknown, Opened, States, MatchingDead;

    readonly DeadZoneBoard b;
    readonly bool[] fixedB, wall2, occ, fz;
    readonly uint[] comp, rmark, pmark, cmark, zmark, area;
    uint cst, rst, pst, cmst, zst;
    readonly List<int> q = new(), zq = new();
    readonly List<int> zone = new();
    readonly List<int> cells = new();
    int realMin;
    readonly List<int> allCells = new();
    readonly List<(int a, int b)> corrals = new();
    readonly List<int> zoneAll = new();
    readonly List<(int a, int b)> zones = new();
    readonly List<int> cellsTmp = new();
    int[] uf = [];
    int[] owner;
    readonly List<int> members = new();
    readonly int floorN;
    int fixedN;
    uint prs;
    // search
    short[] arena = new short[1024];
    int arenaLen;
    int[] slots = [];
    int[] cur = [], nxt = [], nx0Arr = [];
    readonly ulong[] cache = new ulong[1 << 16];
    readonly ushort[] cnt = new ushort[1 << 16];
    int[] goals = [];
    int gw;
    Dictionary<ulong, ulong[]>? reachFix;
    int[] mOf = [];
    ulong[] mSeen = [];
    readonly ulong[] zob;

    public DeadZoneChecker(DeadZoneBoard board, int capStates)
    {
        b = board; Cap = capStates;
        int n = b.Wall.Length;
        fixedB = new bool[n]; wall2 = new bool[n]; occ = new bool[n]; fz = new bool[n];
        comp = new uint[n]; rmark = new uint[n]; pmark = new uint[n]; cmark = new uint[n]; zmark = new uint[n]; area = new uint[n];
        owner = new int[n];
        zob = new ulong[2 * n];
        ulong x = 0x9E3779B97F4A7C15UL;
        for (int i = 0; i < zob.Length; i++) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; zob[i] = x; }
        foreach (bool w in b.Wall) if (!w) floorN++;
    }

    uint StampR() { if (++rst == 0) { Array.Clear(rmark); rst = 1; } return rst; }

    /// <summary>Computes the fixed set (fixedB); true if a fixed box is off goal (freeze deadlock).</summary>
    bool FixedBoxes(ReadOnlySpan<int> boxes)
    {
        foreach (int x in boxes) fixedB[x] = true;
        bool Blk(int x) => b.Wall[x] || fixedB[x];
        for (bool changed = true; changed;)
        {
            changed = false;
            foreach (int x in boxes)
            {
                if (!fixedB[x]) continue;
                bool h = Blk(x - 1) || Blk(x + 1) || (b.Dead[x - 1] && b.Dead[x + 1]);
                bool v = Blk(x - b.W) || Blk(x + b.W) || (b.Dead[x - b.W] && b.Dead[x + b.W]);
                if (!h || !v) { fixedB[x] = false; changed = true; }
            }
        }
        foreach (int x in boxes) if (fixedB[x] && !b.Goal[x]) return true;
        return false;
    }

    /// <summary>
    /// Whether the position just reached by pushing a box onto t (occIn marks the boxes, listed ascending in boxes)
    /// with the player on cell player is a proved corral or freeze deadlock.
    /// </summary>
    public bool DeadAfter(bool[] occIn, ReadOnlySpan<int> boxes, int player, int t)
    {
        Calls++;
        try { bool r = DeadAfterCore(occIn, boxes, player, t); if (Trace) Console.Error.WriteLine($"DZ {Calls} t={t} p={player} r={(r ? "True" : "False")} corrals={Corrals} searches={Searches}"); return r; }
        finally { foreach (int x in boxes) fixedB[x] = false; }
    }

    bool DeadAfterCore(bool[] occIn, ReadOnlySpan<int> boxes, int player, int t)
    {
        if (FixedBoxes(boxes)) { Frozen++; return true; }
        if (Matching && !MatchFixed(boxes)) { MatchingDead++; return true; }
        // the player's region on the real board
        if (++pst == 0) { Array.Clear(pmark); pst = 1; }
        uint rs = pst;
        q.Clear(); q.Add(player);
        pmark[player] = rs; realMin = player;
        var dirs = b.Dirs;
        for (int i = 0; i < q.Count; i++)
            foreach (int d in dirs)
            {
                int y = q[i] + d;
                if (pmark[y] != rs && !b.Wall[y] && !occIn[y]) { pmark[y] = rs; q.Add(y); if (y < realMin) realMin = y; }
            }
        if (++cst == 0) { Array.Clear(comp); cst = 1; }
        uint cs0 = cst;
        // every corral (a free region the player can't reach) touches a box
        allCells.Clear(); corrals.Clear();
        foreach (int x0 in boxes)
            foreach (int d0 in dirs)
            {
                int start = x0 + d0;
                if (b.Wall[start] || occIn[start] || pmark[start] == rs || comp[start] >= cs0) continue;
                cst++;
                uint cs = cst;
                comp[start] = cs;
                int c0 = allCells.Count;
                allCells.Add(start);
                for (int i = c0; i < allCells.Count; i++)
                    foreach (int d in dirs)
                    {
                        int y = allCells[i] + d;
                        if (!b.Wall[y] && !occIn[y] && comp[y] != cs) { comp[y] = cs; allCells.Add(y); }
                    }
                corrals.Add((c0, allCells.Count));
            }
        // single corrals, then pairs sharing a zone box
        zones.Clear(); zoneAll.Clear();
        for (int i = 0; i < corrals.Count; i++)
        {
            bool work = BuildZone(occIn, corrals[i], (0, 0));
            int s0 = zoneAll.Count;
            zoneAll.AddRange(zone);
            zones.Add((s0, zoneAll.Count));
            if (TryZone(work, boxes, player)) return true;
        }
        if (Pairs)
        {
            for (int i = 0; i < corrals.Count; i++)
            {
                zst++;
                uint zs = zst;
                for (int k = zones[i].a; k < zones[i].b; k++) zmark[zoneAll[k]] = zs;
                for (int j = i + 1; j < corrals.Count; j++)
                {
                    bool shared = false;
                    for (int k = zones[j].a; k < zones[j].b; k++) if (zmark[zoneAll[k]] == zs) { shared = true; break; }
                    if (!shared) continue;
                    bool work = BuildZone(occIn, corrals[i], corrals[j]);
                    if (TryZone(work, boxes, player)) return true;
                    zst++; // BuildZone used new stamps: re-mark corral i's zone
                    zs = zst;
                    for (int k = zones[i].a; k < zones[i].b; k++) zmark[zoneAll[k]] = zs;
                }
            }
        }
        if (Groups && corrals.Count > 2 && GroupsDead(occIn, boxes, player)) return true;
        return false;
    }

    bool GroupsDead(bool[] occIn, ReadOnlySpan<int> boxes, int player)
    {
        int n = corrals.Count;
        if (uf.Length < n) uf = new int[n];
        for (int i = 0; i < n; i++) uf[i] = i;
        int Find(int x) { while (uf[x] != x) { uf[x] = uf[uf[x]]; x = uf[x]; } return x; }
        zst++;
        uint zs = zst;
        for (int i = 0; i < n; i++)
            for (int k = zones[i].a; k < zones[i].b; k++)
            {
                int z = zoneAll[k];
                if (zmark[z] == zs)
                {
                    int a = Find(owner[z]), c = Find(i);
                    if (a != c) uf[a] = c;
                }
                else { zmark[z] = zs; owner[z] = i; }
            }
        for (int root = 0; root < n; root++)
        {
            if (Find(root) != root) continue;
            members.Clear();
            for (int i = 0; i < n; i++) if (Find(i) == root) members.Add(i);
            if (members.Count < 3 || members.Count > 64) continue;
            cellsTmp.Clear();
            foreach (int i in members)
            {
                var cr = corrals[i];
                for (int k = cr.a; k < cr.b; k++) cellsTmp.Add(allCells[k]);
            }
            bool dead = TryZone(BuildZoneCells(occIn, cellsTmp), boxes, player);
            if (dead) return true;
        }
        return false;
    }

    bool NearArea(int x)
    {
        int w = b.W;
        ReadOnlySpan<int> ds = [-w - 1, -w, -w + 1, -1, 0, 1, w - 1, w, w + 1];
        foreach (int d in ds) if (area[x + d] == zst) return true;
        return false;
    }

    bool TryZone(bool work, ReadOnlySpan<int> boxes, int player)
    {
        if (!work || zone.Count == 0 || zone.Count > 32000) return false;
        Corrals++;
        if (ZoneDead(boxes, player)) { DeadCount++; return true; }
        return false;
    }

    bool BuildZone(bool[] occIn, (int a, int b) a, (int a, int b) bb)
    {
        cellsTmp.Clear();
        for (int k = a.a; k < a.b; k++) cellsTmp.Add(allCells[k]);
        for (int k = bb.a; k < bb.b; k++) cellsTmp.Add(allCells[k]);
        return BuildZoneCells(occIn, cellsTmp);
    }

    bool BuildZoneCells(bool[] occIn, List<int> cs)
    {
        if (++zst == 0) { Array.Clear(zmark); Array.Clear(area); zst = 1; }
        uint zs = zst;
        cells.Clear(); cells.AddRange(cs);
        zone.Clear();
        bool work = false;
        void AddZone(int y)
        {
            if (occIn[y] && !fixedB[y] && zmark[y] != zs) { zmark[y] = zs; zone.Add(y); work = work || !b.Goal[y]; }
        }
        int w = b.W;
        foreach (int x in cells)
        {
            foreach (int d in b.Dirs)
            {
                int y = x + d;
                if (occIn[y])
                {
                    AddZone(y);
                    if (Wide && occIn[y + d] && !fixedB[y + d] && !Sideways(occIn, y + d, d)) AddZone(y + d);
                }
            }
            if (Wide)
            {
                ReadOnlySpan<int> dg = [-w - 1, -w + 1, w - 1, w + 1];
                foreach (int d in dg) if (!b.Wall[x + d]) AddZone(x + d);
            }
            ReadOnlySpan<int> ds = [-w - 1, -w, -w + 1, -1, 0, 1, w - 1, w, w + 1];
            foreach (int d in ds) area[x + d] = zs;
        }
        foreach (int z in zone) area[z] = zs;
        return work;
    }

    bool Sideways(bool[] occIn, int x, int d)
    {
        bool Free(int y) => !b.Wall[y] && !occIn[y];
        foreach (int e in b.Dirs)
        {
            if (e == d || e == -d) continue;
            if (Free(x + e) && Free(x + 2 * e) && Free(x - e)) return true;
        }
        return false;
    }

    // ---- floods on the reduced board (c.occ = live zone boxes, wall2 = fixed walls) -------------------------------------------------
    (int min, int n) Flood(int p, uint[] mark, uint rs)
    {
        zq.Clear(); zq.Add(p);
        mark[p] = rs; int m = p;
        var dirs = b.Dirs;
        for (int i = 0; i < zq.Count; i++)
            foreach (int d in dirs)
            {
                int y = zq[i] + d;
                if (mark[y] != rs && !b.Wall[y] && !wall2[y] && !occ[y]) { mark[y] = rs; zq.Add(y); if (y < m) m = y; }
            }
        return (m, zq.Count);
    }
    (int, int) ParentFlood(int p) { uint rs = StampR(); var r = Flood(p, rmark, rs); prs = rs; return r; }
    (int, int) ChildFlood(int p) { if (++cmst == 0) { Array.Clear(cmark); cmst = 1; } return Flood(p, cmark, cmst); }
    bool InParent(int x) => rmark[x] == prs;
    bool InChild(int x) => cmark[x] == cmst;

    bool Fencing(int y)
    {
        int w = b.W;
        ReadOnlySpan<int> ds = [-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1];
        foreach (int d in ds)
        {
            int u = y + d;
            if (!b.Wall[u] && !wall2[u] && !occ[u] && !InChild(u)) return true;
        }
        return false;
    }

    (int pn, int size, int live) EscapeScan(int[] nx, int p)
    {
        while (true)
        {
            var (pn, size) = ChildFlood(p);
            int live = 0; bool removed = false;
            for (int j = 0; j < nx.Length; j++)
            {
                int y = nx[j];
                if (y == 0) continue;
                bool outB = area[y] != zst || (ShrinkAll && !Fencing(y));
                foreach (int d in b.Dirs)
                {
                    if (outB) break;
                    int t = y + d;
                    if (!InChild(y - d) || b.Wall[t] || wall2[t] || occ[t] || b.Dead[t]) continue;
                    outB = area[t] != zst;
                }
                if (outB) { occ[y] = false; nx[j] = 0; removed = true; continue; }
                live++;
            }
            if (!removed) return (pn, size, live);
        }
    }

    bool FrozenOff(int x) { bool off = false; return Frozen_(x, ref off) && off; }
    bool Frozen_(int x, ref bool off)
    {
        fz[x] = true;
        bool ok = BlockedAxis(x, 1, ref off) && BlockedAxis(x, b.W, ref off);
        fz[x] = false;
        if (ok && !b.Goal[x]) off = true;
        return ok;
    }
    bool BlockedAxis(int x, int d, ref bool off)
    {
        int l = x - d, r = x + d;
        if (b.Wall[l] || b.Wall[r] || wall2[l] || wall2[r] || fz[l] || fz[r]) return true;
        if (b.Dead[l] && b.Dead[r]) return true;
        return (occ[l] && Frozen_(l, ref off)) || (occ[r] && Frozen_(r, ref off));
    }

    ulong Key(ReadOnlySpan<int> boxes, int pnorm)
    {
        ulong h = (ulong)(uint)pnorm * 0xD6E8FEB86659FD93UL + (ulong)zone.Count;
        foreach (int z in zone) h ^= zob[z];
        foreach (int x in boxes) if (wall2[x]) h ^= zob[b.Wall.Length + x];
        return (h ^ b.Salt) & ~3UL;
    }

    bool ZoneDead(ReadOnlySpan<int> boxes, int player)
    {
        int k = zone.Count;
        zone.Sort();
        foreach (int z in zone) occ[z] = true;
        fixedN = 0;
        foreach (int x in boxes)
            if (fixedB[x] && (!LocalFixed || NearArea(x))) { wall2[x] = true; fixedN++; }
        try
        {
            int open = floorN - fixedN - k;
            ulong key = 0; int ci = 0; int capN = Cap;
            DeadZoneTable? sh = Private ? null : Shared;
            // lookup: cached verdict (hit) or the budget to search with
            (bool hit, bool dead) Lookup()
            {
                ci = (int)(key >> 2 & (ulong)(cache.Length - 1));
                if (sh != null && (cache[ci] & ~3UL) != key)
                {
                    ulong v = Volatile.Read(ref sh.Slots[(int)(key >> 2 & (ulong)(sh.Slots.Length - 1))]);
                    if ((v & ~3UL) == key && (v & 3) != 0) { CacheHits++; cache[ci] = v; return (true, (v & 3) == 1); }
                }
                if ((cache[ci] & ~3UL) != key) { cnt[ci] = 0; return (false, false); }
                CacheHits++;
                if ((cache[ci] & 3) != 3) return (true, (cache[ci] & 3) == 1);
                cnt[ci]++;
                switch (cnt[ci])
                {
                    case 8: capN *= 4; break;
                    case 64: capN *= 16; break;
                    case 512: capN *= 64; break;
                    default: return (true, false);
                }
                Rechecks++;
                return (false, false);
            }
            if (FastKey)
            {
                key = Key(boxes, realMin);
                var (hit, dead) = Lookup();
                if (hit) { foreach (int z in zone) occ[z] = false; return dead; }
            }
            var (p0, size) = ParentFlood(player);
            foreach (int z in zone) occ[z] = false;
            if (size == open)
            {
                Opened++;
                if (FastKey) cache[ci] = key | 2;
                return false; // nothing is fenced off once the other boxes are gone
            }
            if (!FastKey)
            {
                key = Key(boxes, p0);
                var (hit, dead) = Lookup();
                if (hit) return dead;
            }
            Searches++;
            uint res = Search(k, p0, capN);
            cache[ci] = key | res;
            if (sh != null && res != 3 && !ReadOnly) Volatile.Write(ref sh.Slots[(int)(key >> 2 & (ulong)(sh.Slots.Length - 1))], key | res);
            if (res == 3) Unknown++;
            return res == 1;
        }
        finally { foreach (int x in boxes) wall2[x] = false; }
    }

    static ulong HashAt(ReadOnlySpan<short> st)
    {
        ulong h = 0xCBF29CE484222325UL;
        foreach (short v in st) h = (h ^ (ushort)v) * 0x100000001B3UL;
        return h ^ (h >> 29);
    }

    bool AllGoals(int[] c) { foreach (int x in c) if (x != 0 && !b.Goal[x]) return false; return true; }

    /// <summary>BFS over zone positions: 1 dead, 2 alive, 3 unknown (cap).</summary>
    uint Search(int k, int p0, int capN)
    {
        int stride = k + 1;
        arenaLen = 0;
        if (nxt.Length != k) { nxt = new int[k]; cur = new int[k]; nx0Arr = new int[k]; }
        void Push(short v) { if (arenaLen == arena.Length) Array.Resize(ref arena, arena.Length * 2); arena[arenaLen++] = v; }
        var nx0 = nx0Arr;
        for (int i = 0; i < k; i++) nx0[i] = zone[i];
        foreach (int z in nx0) occ[z] = true;
        var (pn0, size0, live0) = EscapeScan(nx0, p0);
        foreach (int z in nx0) occ[z] = false;
        if (size0 == floorN - fixedN - live0 || AllGoals(nx0)) { Opened++; return 2; }
        Array.Sort(nx0);
        foreach (int z in nx0) Push((short)z);
        Push((short)pn0);

        int need = 1;
        while (need < 4 * capN) need <<= 1;
        if (slots.Length < need) slots = new int[need]; else Array.Clear(slots, 0, need);
        ulong mask = (ulong)(need - 1);
        bool Insert(int baseOff)
        {
            int n = baseOff / stride;
            var st = arena.AsSpan(baseOff, stride);
            for (ulong i = HashAt(st) & mask; ; i = (i + 1) & mask)
            {
                if (slots[i] == 0) { slots[i] = n + 1; return true; }
                int o = (slots[i] - 1) * stride;
                if (arena.AsSpan(o, stride).SequenceEqual(st)) return false;
            }
        }
        Insert(0);
        int states = 1;
        var curA = cur; var nx = nxt;
        for (int head = 0; head * stride < arenaLen; head++)
        {
            int o = head * stride;
            for (int i = 0; i < k; i++)
            {
                curA[i] = arena[o + i];
                if (curA[i] == 0) continue;
                occ[curA[i]] = true;
            }
            int p = arena[o + k];
            ParentFlood(p);
            for (int i = 0; i < k; i++)
            {
                int x = curA[i];
                foreach (int d in b.Dirs)
                {
                    int t = x + d;
                    if (x == 0 || !InParent(x - d) || b.Wall[t] || wall2[t] || occ[t] || b.Dead[t]) continue;
                    Array.Copy(curA, nx, k);
                    bool gone = Kill && area[t] != zst;
                    occ[x] = false;
                    if (gone) nx[i] = 0; else { nx[i] = t; occ[t] = true; }
                    if (!gone && FrozenOff(t)) { occ[t] = false; occ[x] = true; continue; }
                    var (pn, size, live) = EscapeScan(nx, x);
                    bool done = size == floorN - fixedN - live || AllGoals(nx);
                    foreach (int y in nx) occ[y] = false;
                    for (int j = 0; j < k; j++) if (curA[j] != 0) occ[curA[j]] = true;
                    if (done)
                    {
                        for (int j = 0; j < k; j++) occ[curA[j]] = false;
                        occ[0] = false;
                        Opened++;
                        States += states;
                        return 2;
                    }
                    int baseOff = arenaLen;
                    for (int j = 0; j < stride; j++) Push(0);
                    SortedState(arena.AsSpan(baseOff, k), nx, i);
                    arena[baseOff + k] = (short)pn;
                    if (Insert(baseOff))
                    {
                        states++;
                        if (states > capN)
                        {
                            for (int j = 0; j < k; j++) occ[curA[j]] = false;
                            occ[0] = false;
                            States += states;
                            return 3;
                        }
                    }
                    else arenaLen = baseOff;
                }
            }
            for (int i = 0; i < k; i++) occ[curA[i]] = false;
        }
        States += states;
        return 1;
    }

    /// <summary>Writes the boxes of nx into st ascending: nx is the parent's sorted state with box i moved and further boxes removed (0).</summary>
    static void SortedState(Span<short> st, int[] nx, int i)
    {
        int z = 0;
        foreach (int v in nx) if (v == 0) z++;
        st[..z].Clear();
        int pos = z;
        int tv = nx[i];
        bool placed = tv == 0;
        for (int j = 0; j < nx.Length; j++)
        {
            int v = nx[j];
            if (j == i || v == 0) continue;
            if (!placed && tv < v) { st[pos++] = (short)tv; placed = true; }
            st[pos++] = (short)v;
        }
        if (!placed) st[pos] = (short)tv;
    }

    // ---- matching with fixed boxes as walls ---------------------------------------------------------------------------------------
    bool MatchFixed(ReadOnlySpan<int> boxes)
    {
        ulong key = 0; bool any = false;
        foreach (int x in boxes) if (fixedB[x]) { key ^= zob[x]; any = true; }
        if (!any) return true; // the static matching already covers this
        if (reachFix == null)
        {
            var g = new List<int>();
            for (int x = 0; x < b.Goal.Length; x++) if (b.Goal[x] && !b.Wall[x]) g.Add(x);
            goals = [.. g]; gw = (goals.Length + 63) / 64;
            reachFix = new(); mOf = new int[goals.Length]; mSeen = new ulong[gw];
        }
        if (!reachFix.TryGetValue(key, out var R))
        {
            if (reachFix.Count > 4096) reachFix.Clear();
            int n = b.Wall.Length;
            R = new ulong[n * gw];
            bool Free(int x) => !b.Wall[x] && !fixedB[x];
            for (int gi = 0; gi < goals.Length; gi++)
            {
                int g = goals[gi];
                if (fixedB[g]) continue;
                uint rs = StampR();
                q.Clear(); q.Add(g);
                rmark[g] = rs;
                for (int i = 0; i < q.Count; i++)
                {
                    int y = q[i];
                    R[y * gw + (gi >> 6)] |= 1UL << (gi & 63);
                    foreach (int d in b.Dirs)
                    {
                        int x = y - d; // a box on y-d pushed by a player on y-2d reaches y
                        if (rmark[x] != rs && Free(x) && Free(x - d)) { rmark[x] = rs; q.Add(x); }
                    }
                }
            }
            reachFix[key] = R;
        }
        Array.Fill(mOf, -1);
        foreach (int x in boxes)
        {
            if (fixedB[x]) continue;
            Array.Clear(mSeen);
            if (!Augment(R, x)) return false;
        }
        return true;
    }

    bool Augment(ulong[] R, int x)
    {
        for (int w = 0; w < gw; w++)
        {
            ulong rw = R[x * gw + w];
            while (true)
            {
                ulong cand = rw & ~mSeen[w];
                if (cand == 0) break;
                ulong bit = cand & (~cand + 1);
                mSeen[w] |= bit;
                int gi = w << 6 | BitOperations.TrailingZeroCount(cand);
                if (mOf[gi] < 0 || Augment(R, mOf[gi])) { mOf[gi] = x; return true; }
            }
        }
        return false;
    }
}
