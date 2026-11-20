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
/// position is dead. Floods are pointer-based over pinned buffers with a merged blocked map; the escape fixpoint
/// uses Go's pass-start snapshot semantics (see EscapeScan), which is what Go's word-parallel path computes.
/// </summary>
public sealed unsafe class DeadZoneChecker
{
    public static bool Groups = true, Pairs = true, LocalFixed = true, Wide = true, Kill = true, Shrink = true, ShrinkAll = true,
        Escape = true, Matching = true, FastKey = true;
    public static readonly bool Trace = Environment.GetEnvironmentVariable("DZTRACE") == "1";

    public bool Private, ReadOnly;
    public DeadZoneTable? Shared;
    public int Cap;
    public long CFloods, PFloods, EscCalls, EscPasses, Moves;
    public long Calls, Corrals, Searches, CacheHits, Rechecks, DeadCount, Frozen, Unknown, Opened, States, MatchingDead;

    const byte BWall = 1, BWall2 = 2, BOcc = 4;
    readonly DeadZoneBoard b;
    readonly int n, W;
    readonly int[] dirs;
    // pinned scratch (raw pointers in hot loops)
    readonly byte[] blkA; readonly uint[] rmarkA, pmarkA, cmarkA, compA, zmarkA, areaA; readonly int[] qA, zqA;
    readonly byte* blk;                      // bit0 wall, bit1 fixed-box wall, bit2 live zone box
    readonly uint* rmark, pmark, cmark, comp, zmark, area;
    readonly int* q, zq;
    readonly bool[] fixedB, fz;
    uint cst, rst, pst, cmst, zst;
    int realMin;
    // lists as arrays + counts
    int[] zone, cells, allCells, cellsTmp, zoneAll, nodesTmp;
    int zoneN, cellsN, allCellsN, cellsTmpN, zoneAllN;
    int[] corA, corB, znA, znB; int corN;
    int[] uf = [], owner, members = new int[16];
    int membersN;
    readonly int floorN;
    int fixedN;
    uint prs;
    // search
    short[] arena = new short[1024];
    int arenaLen;
    int[] slots = [];
    int[] cur = [], nxt = [], nx0Arr = [], remIdx = [];
    readonly ulong[] cache = new ulong[1 << 16];
    readonly ushort[] cnt = new ushort[1 << 16];
    int[] goals = [];
    int gw;
    Dictionary<ulong, ulong[]>? reachFix;
    int[] mOf = [];
    ulong[] mSeen = [];
    readonly ulong[] zob;
    // word-parallel path (board width <= 64): one ulong per row
    readonly bool bitsOK;
    readonly int H;
    readonly int[] rowOf; readonly byte[] colOf;
    readonly ulong[] baseB, freeB, rbits, cbits, ain, bx, rm, deadB, baseStatic;
    bool pOK; int pm, psz, spn, ssz;

    public DeadZoneChecker(DeadZoneBoard board, int capStates)
    {
        b = board; Cap = capStates; W = b.W; dirs = b.Dirs;
        n = b.Wall.Length;
        blkA = GC.AllocateArray<byte>(n, pinned: true);
        rmarkA = GC.AllocateArray<uint>(n, pinned: true); pmarkA = GC.AllocateArray<uint>(n, pinned: true); cmarkA = GC.AllocateArray<uint>(n, pinned: true);
        compA = GC.AllocateArray<uint>(n, pinned: true); zmarkA = GC.AllocateArray<uint>(n, pinned: true); areaA = GC.AllocateArray<uint>(n, pinned: true);
        qA = GC.AllocateArray<int>(n, pinned: true); zqA = GC.AllocateArray<int>(n, pinned: true);
        for (int i = 0; i < n; i++) blkA[i] = (byte)(b.Wall[i] ? BWall : 0);
        blk = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref blkA[0]);
        rmark = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rmarkA[0]); pmark = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref pmarkA[0]);
        cmark = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref cmarkA[0]); comp = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref compA[0]);
        zmark = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref zmarkA[0]); area = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref areaA[0]);
        q = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref qA[0]); zq = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref zqA[0]);
        fixedB = new bool[n]; fz = new bool[n]; owner = new int[n];
        zone = new int[64]; cells = new int[n]; allCells = new int[n]; cellsTmp = new int[n]; zoneAll = new int[256]; nodesTmp = [];
        corA = new int[64]; corB = new int[64]; znA = new int[64]; znB = new int[64];
        zob = new ulong[2 * n];
        ulong x = 0x9E3779B97F4A7C15UL;
        for (int i = 0; i < zob.Length; i++) { x ^= x << 13; x ^= x >> 7; x ^= x << 17; zob[i] = x; }
        foreach (bool w in b.Wall) if (!w) floorN++;
        if (W <= 64 && BitFlood)
        {
            bitsOK = true; H = n / W;
            rowOf = new int[n]; colOf = new byte[n];
            for (int x2 = 0; x2 < n; x2++) { rowOf[x2] = x2 / W; colOf[x2] = (byte)(x2 % W); }
            baseB = new ulong[H]; freeB = new ulong[H]; rbits = new ulong[H]; cbits = new ulong[H];
            ain = new ulong[H]; bx = new ulong[H]; rm = new ulong[H]; deadB = new ulong[H]; baseStatic = new ulong[H];
            for (int x2 = 0; x2 < n; x2++)
            {
                if (!b.Wall[x2]) baseStatic[rowOf[x2]] |= 1UL << colOf[x2];
                if (b.Dead[x2]) deadB[rowOf[x2]] |= 1UL << colOf[x2];
            }
        }
    }

    /// <summary>Go deadzone.BitFlood (word-parallel floods and escape); false = scan paths everywhere.</summary>
    public static bool BitFlood = true;

    uint StampR() { if (++rst == 0) { Array.Clear(rmarkA); rst = 1; } return rst; }
    bool Occ(int x) => (blk[x] & BOcc) != 0;
    bool Wall2(int x) => (blk[x] & BWall2) != 0;
    void SetOcc(int x, bool v) { if (v) blk[x] |= BOcc; else blk[x] &= unchecked((byte)~BOcc); }
    void SetWall2(int x, bool v) { if (v) blk[x] |= BWall2; else blk[x] &= unchecked((byte)~BWall2); }
    void AddZone1(int y) { if (zoneN == zone.Length) Array.Resize(ref zone, zoneN * 2); zone[zoneN++] = y; }
    void AddZoneAll(int y) { if (zoneAllN == zoneAll.Length) Array.Resize(ref zoneAll, zoneAllN * 2); zoneAll[zoneAllN++] = y; }

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
                bool v = Blk(x - W) || Blk(x + W) || (b.Dead[x - W] && b.Dead[x + W]);
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
        bool r;
        try { r = DeadAfterCore(occIn, boxes, player, t); }
        finally { foreach (int x in boxes) fixedB[x] = false; }
        if (Trace) Console.Error.WriteLine($"DZ {Calls} t={t} p={player} r={(r ? "true" : "false")} corrals={Corrals} searches={Searches} frozen={Frozen} matching={MatchingDead}");
        return r;
    }

    void AddCorral(int a, int bb)
    {
        if (corN == corA.Length) { Array.Resize(ref corA, corN * 2); Array.Resize(ref corB, corN * 2); Array.Resize(ref znA, corN * 2); Array.Resize(ref znB, corN * 2); }
        corA[corN] = a; corB[corN] = bb; corN++;
    }

    bool DeadAfterCore(bool[] occIn, ReadOnlySpan<int> boxes, int player, int t)
    {
        if (FixedBoxes(boxes)) { Frozen++; return true; }
        if (Matching && !MatchFixed(boxes)) { MatchingDead++; return true; }
        // the player's region on the real board (occIn, plain walls)
        if (++pst == 0) { Array.Clear(pmarkA); pst = 1; }
        uint rs = pst;
        int qn = 0; q[qn++] = player;
        pmark[player] = rs; realMin = player;
        for (int i = 0; i < qn; i++)
            foreach (int d in dirs)
            {
                int y = q[i] + d;
                if (pmark[y] != rs && !b.Wall[y] && !occIn[y]) { pmark[y] = rs; q[qn++] = y; if (y < realMin) realMin = y; }
            }
        if (++cst == 0) { Array.Clear(compA); cst = 1; }
        uint cs0 = cst;
        // every corral (a free region the player can't reach) touches a box
        allCellsN = 0; corN = 0;
        foreach (int x0 in boxes)
            foreach (int d0 in dirs)
            {
                int start = x0 + d0;
                if (b.Wall[start] || occIn[start] || pmark[start] == rs || comp[start] >= cs0) continue;
                cst++;
                uint cs = cst;
                comp[start] = cs;
                int c0 = allCellsN;
                allCells[allCellsN++] = start;
                for (int i = c0; i < allCellsN; i++)
                    foreach (int d in dirs)
                    {
                        int y = allCells[i] + d;
                        if (!b.Wall[y] && !occIn[y] && comp[y] != cs) { comp[y] = cs; allCells[allCellsN++] = y; }
                    }
                AddCorral(c0, allCellsN);
            }
        // single corrals, then pairs sharing a zone box
        zoneAllN = 0;
        for (int i = 0; i < corN; i++)
        {
            bool work = BuildZone(occIn, corA[i], corB[i], 0, 0);
            int s0 = zoneAllN;
            for (int k = 0; k < zoneN; k++) AddZoneAll(zone[k]);
            znA[i] = s0; znB[i] = zoneAllN;
            if (TryZone(work, boxes, player)) return true;
        }
        if (Pairs)
        {
            for (int i = 0; i < corN; i++)
            {
                zst++;
                uint zs = zst;
                for (int k = znA[i]; k < znB[i]; k++) zmark[zoneAll[k]] = zs;
                for (int j = i + 1; j < corN; j++)
                {
                    bool shared = false;
                    for (int k = znA[j]; k < znB[j]; k++) if (zmark[zoneAll[k]] == zs) { shared = true; break; }
                    if (!shared) continue;
                    bool work = BuildZone(occIn, corA[i], corB[i], corA[j], corB[j]);
                    if (TryZone(work, boxes, player)) return true;
                    zst++; // BuildZone used new stamps: re-mark corral i's zone
                    zs = zst;
                    for (int k = znA[i]; k < znB[i]; k++) zmark[zoneAll[k]] = zs;
                }
            }
        }
        if (Groups && corN > 2 && GroupsDead(occIn, boxes, player)) return true;
        return false;
    }

    bool GroupsDead(bool[] occIn, ReadOnlySpan<int> boxes, int player)
    {
        int nn = corN;
        if (uf.Length < nn) uf = new int[nn];
        for (int i = 0; i < nn; i++) uf[i] = i;
        int Find(int x) { while (uf[x] != x) { uf[x] = uf[uf[x]]; x = uf[x]; } return x; }
        zst++;
        uint zs = zst;
        for (int i = 0; i < nn; i++)
            for (int k = znA[i]; k < znB[i]; k++)
            {
                int z = zoneAll[k];
                if (zmark[z] == zs)
                {
                    int a = Find(owner[z]), c = Find(i);
                    if (a != c) uf[a] = c;
                }
                else { zmark[z] = zs; owner[z] = i; }
            }
        for (int root = 0; root < nn; root++)
        {
            if (Find(root) != root) continue;
            membersN = 0;
            for (int i = 0; i < nn; i++)
                if (Find(i) == root) { if (membersN == members.Length) Array.Resize(ref members, membersN * 2); members[membersN++] = i; }
            if (membersN < 3 || membersN > 64) continue;
            cellsTmpN = 0;
            for (int mi = 0; mi < membersN; mi++)
            {
                int i = members[mi];
                for (int k = corA[i]; k < corB[i]; k++) cellsTmp[cellsTmpN++] = allCells[k];
            }
            if (TryZone(BuildZoneCells(occIn), boxes, player)) return true;
        }
        return false;
    }

    bool NearArea(int x)
    {
        int w = W;
        if (area[x - w - 1] == zst || area[x - w] == zst || area[x - w + 1] == zst || area[x - 1] == zst || area[x] == zst || area[x + 1] == zst
            || area[x + w - 1] == zst || area[x + w] == zst || area[x + w + 1] == zst) return true;
        return false;
    }

    bool TryZone(bool work, ReadOnlySpan<int> boxes, int player)
    {
        if (!work || zoneN == 0 || zoneN > 32000) return false;
        Corrals++;
        if (ZoneDead(boxes, player)) { DeadCount++; return true; }
        return false;
    }

    bool BuildZone(bool[] occIn, int a0, int a1, int b0, int b1)
    {
        cellsTmpN = 0;
        for (int k = a0; k < a1; k++) cellsTmp[cellsTmpN++] = allCells[k];
        for (int k = b0; k < b1; k++) cellsTmp[cellsTmpN++] = allCells[k];
        return BuildZoneCells(occIn);
    }

    /// <summary>Builds zone/cells/area from cellsTmp[0..cellsTmpN).</summary>
    bool BuildZoneCells(bool[] occIn)
    {
        if (++zst == 0) { Array.Clear(zmarkA); Array.Clear(areaA); zst = 1; }
        uint zs = zst;
        cellsN = cellsTmpN;
        Array.Copy(cellsTmp, cells, cellsN);
        zoneN = 0;
        bool work = false;
        int w = W;
        for (int ci = 0; ci < cellsN; ci++)
        {
            int x = cells[ci];
            foreach (int d in dirs)
            {
                int y = x + d;
                if (occIn[y])
                {
                    if (!fixedB[y] && zmark[y] != zs) { zmark[y] = zs; AddZone1(y); work = work || !b.Goal[y]; }
                    int y2 = y + d;
                    if (Wide && occIn[y2] && !fixedB[y2] && !Sideways(occIn, y2, d) && zmark[y2] != zs) { zmark[y2] = zs; AddZone1(y2); work = work || !b.Goal[y2]; }
                }
            }
            if (Wide)
            {
                int d1 = x - w - 1, d2 = x - w + 1, d3 = x + w - 1, d4 = x + w + 1;
                if (!b.Wall[d1] && occIn[d1] && !fixedB[d1] && zmark[d1] != zs) { zmark[d1] = zs; AddZone1(d1); work = work || !b.Goal[d1]; }
                if (!b.Wall[d2] && occIn[d2] && !fixedB[d2] && zmark[d2] != zs) { zmark[d2] = zs; AddZone1(d2); work = work || !b.Goal[d2]; }
                if (!b.Wall[d3] && occIn[d3] && !fixedB[d3] && zmark[d3] != zs) { zmark[d3] = zs; AddZone1(d3); work = work || !b.Goal[d3]; }
                if (!b.Wall[d4] && occIn[d4] && !fixedB[d4] && zmark[d4] != zs) { zmark[d4] = zs; AddZone1(d4); work = work || !b.Goal[d4]; }
            }
            area[x - w - 1] = zs; area[x - w] = zs; area[x - w + 1] = zs; area[x - 1] = zs; area[x] = zs; area[x + 1] = zs;
            area[x + w - 1] = zs; area[x + w] = zs; area[x + w + 1] = zs;
        }
        for (int i = 0; i < zoneN; i++) area[zone[i]] = zs;
        return work;
    }

    bool Sideways(bool[] occIn, int x, int d)
    {
        bool Free(int y) => !b.Wall[y] && !occIn[y];
        foreach (int e in dirs)
        {
            if (e == d || e == -d) continue;
            if (Free(x + e) && Free(x + 2 * e) && Free(x - e)) return true;
        }
        return false;
    }

    // ---- floods on the reduced board (blk: walls + fixed boxes + live zone boxes) -------------------------------------------------------
    (int min, int n) Flood(int p, uint* mark, uint rs)
    {
        int tail = 0; zq[tail++] = p;
        mark[p] = rs; int m = p;
        int w = W;
        for (int i = 0; i < tail; i++)
        {
            int c = zq[i];
            int y = c - w; if (mark[y] != rs && blk[y] == 0) { mark[y] = rs; zq[tail++] = y; if (y < m) m = y; }
            y = c + w; if (mark[y] != rs && blk[y] == 0) { mark[y] = rs; zq[tail++] = y; }
            y = c - 1; if (mark[y] != rs && blk[y] == 0) { mark[y] = rs; zq[tail++] = y; if (y < m) m = y; }
            y = c + 1; if (mark[y] != rs && blk[y] == 0) { mark[y] = rs; zq[tail++] = y; }
        }
        return (m, tail);
    }
    (int, int) ParentFlood(int p, int[]? boxes = null, int nb = 0)
    {
        PFloods++;
        if (bitsOK) return FloodBits(p, boxes!, nb, rbits);
        uint rs = StampR(); var r = Flood(p, rmark, rs); prs = rs; return r;
    }
    (int, int) ChildFlood(int p) { CFloods++; if (++cmst == 0) { Array.Clear(cmarkA); cmst = 1; } return Flood(p, cmark, cmst); }
    bool InParent(int x) => bitsOK ? ((rbits[rowOf[x]] >> colOf[x]) & 1) != 0 : rmark[x] == prs;
    bool InChild(int x) => cmark[x] == cmst;

    bool Fencing(int y)
    {
        int w = W; uint cm = cmst;
        int u;
        u = y - w - 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y - w; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y - w + 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y - 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y + 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y + w - 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y + w; if (blk[u] == 0 && cmark[u] != cm) return true;
        u = y + w + 1; if (blk[u] == 0 && cmark[u] != cm) return true;
        return false;
    }

    /// <summary>Go escapeScan on boards wider than 64 (no word-parallel path): boxes are judged and removed one at a time, live.</summary>
    (int pn, int size, int live) EscapeScanLive(int[] nx, int p)
    {
        uint zs = zst;
        while (true)
        {
            var (pn, size) = ChildFlood(p);
            int live = 0; bool removed = false;
            for (int j = 0; j < nx.Length; j++)
            {
                int y = nx[j];
                if (y == 0) continue;
                bool outB = area[y] != zs || (ShrinkAll && !Fencing(y));
                if (!outB)
                    foreach (int d in dirs)
                    {
                        int t = y + d;
                        if (cmark[y - d] != cmst || blk[t] != 0 || b.Dead[t]) continue;
                        if (area[t] != zs) { outB = true; break; }
                    }
                if (outB) { SetOcc(y, false); nx[j] = 0; removed = true; continue; }
                live++;
            }
            if (!removed) return (pn, size, live);
        }
    }

    (int pn, int size, int live) DoEscape(int[] nx, int p, int seed)
        => bitsOK && ShrinkAll ? EscapeBits(nx, p, seed) : EscapeScanLive(nx, p);

    // ---- word-parallel path (Go escapeBits / floodFree / seedChild), board width <= 64 ------------------------------------------------
    void SetBase(ReadOnlySpan<int> boxes)
    {
        if (!bitsOK) return;
        Array.Copy(baseStatic, baseB, H);
        foreach (int x in boxes) if (Wall2(x)) baseB[rowOf[x]] &= ~(1UL << colOf[x]);
    }

    void BuildArea()
    {
        Array.Clear(ain);
        int w = W;
        for (int i = 0; i < cellsN; i++)
        {
            int x = cells[i];
            for (int dy = -1; dy <= 1; dy++)
                for (int dx = -1; dx <= 1; dx++)
                {
                    int y = x + dy * w + dx;
                    ain[rowOf[y]] |= 1UL << colOf[y];
                }
        }
        for (int i = 0; i < zoneN; i++) ain[rowOf[zone[i]]] |= 1UL << colOf[zone[i]];
    }

    (int, int) FloodBits(int p, int[] boxes, int nb, ulong[] outB)
    {
        Array.Copy(baseB, freeB, H);
        for (int i = 0; i < nb; i++) { int x = boxes[i]; if (x != 0) freeB[rowOf[x]] &= ~(1UL << colOf[x]); }
        return FloodFree(p, outB, false);
    }

    static ulong FillRow(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }

    (int, int) FloodFree(int p, ulong[] outB, bool seeded) => FloodFreeMode(p, outB, seeded, false);

    (int, int) FloodFreeMode(int p, ulong[] outB, bool seeded, bool closed)
    {
        var F = freeB;
        int Hh = outB.Length;
        int prow = rowOf[p];
        int lo = prow, hi = prow;
        int flo = Hh, fhi = -1;
        if (seeded)
        {
            lo = Hh; hi = -1;
            for (int r = 0; r < Hh; r++)
            {
                ulong w0 = outB[r];
                if (w0 != 0)
                {
                    if (!closed) outB[r] = FillRow(w0, F[r]);
                    if (lo == Hh) lo = r;
                    hi = r;
                }
            }
            if (closed) { flo = lo; fhi = hi; lo = prow; hi = prow; }
        }
        else
        {
            Array.Clear(outB);
            outB[prow] = 1UL << colOf[p];
        }
        while (true)
        {
            bool changed = false;
            for (int r = Math.Max(lo - 1, 1); r <= hi + 1 && r < Hh - 1; r++)
            {
                ulong y = outB[r] | ((outB[r - 1] | outB[r + 1]) & F[r]);
                if (y == outB[r] && (r != prow || outB[r] == 0)) continue;
                y = FillRow(y, F[r]);
                if (y != outB[r]) { outB[r] = y; changed = true; lo = Math.Min(lo, r); hi = Math.Max(hi, r); }
            }
            if (!changed) break;
            changed = false;
            for (int r = Math.Min(hi + 1, Hh - 2); r >= 1 && r >= lo - 1; r--)
            {
                ulong y = outB[r] | ((outB[r - 1] | outB[r + 1]) & F[r]);
                if (y == outB[r]) continue;
                y = FillRow(y, F[r]);
                outB[r] = y; changed = true; lo = Math.Min(lo, r); hi = Math.Max(hi, r);
            }
            if (!changed) break;
        }
        int m = -1, size = 0;
        lo = Math.Min(lo, flo); hi = Math.Max(hi, fhi);
        for (int r = Math.Max(lo, 0); r <= hi && r < Hh; r++)
        {
            ulong w0 = outB[r];
            if (w0 != 0)
            {
                if (m < 0) m = r * W + System.Numerics.BitOperations.TrailingZeroCount(w0);
                size += System.Numerics.BitOperations.PopCount(w0);
            }
        }
        return (m, size);
    }

    static readonly bool[] RingOK = BuildRingOK();
    static bool[] BuildRingOK()
    {
        var t = new bool[256];
        for (int m = 0; m < 256; m++)
        {
            int start = -1; bool ok = true;
            for (int i = 0; i < 8; i += 2)
            {
                if (((m >> i) & 1) == 0) continue;
                int j = i;
                for (int k = 0; k < 8 && ((m >> ((j + 7) & 7)) & 1) != 0; k++) j = (j + 7) & 7;
                if (m == 255) j = 0;
                if (start >= 0 && j != start) ok = false;
                start = j;
            }
            t[m] = ok;
        }
        return t;
    }

    bool SeedChild(int p, int seed)
    {
        var R = rbits;
        if (seed != 0)
        {
            int r = rowOf[seed]; int col = colOf[seed];
            if (r == 0 || r == R.Length - 1 || col == 0) return false;
            if (((R[r] >> col) & 1) != 0)
            {
                ulong u = (R[r - 1] >> (col - 1)) & 7, m = (R[r] >> (col - 1)) & 7, d = (R[r + 1] >> (col - 1)) & 7;
                int ring = (int)(((u >> 1) & 1) | (((u >> 2) & 1) << 1) | (((m >> 2) & 1) << 2) | (((d >> 2) & 1) << 3) | (((d >> 1) & 1) << 4) | ((d & 1) << 5) | ((m & 1) << 6) | ((u & 1) << 7));
                if (!RingOK[ring]) return false;
            }
        }
        Array.Copy(R, cbits, R.Length);
        if (seed != 0) cbits[rowOf[seed]] &= ~(1UL << colOf[seed]);
        cbits[rowOf[p]] |= 1UL << colOf[p];
        return true;
    }

    bool SeedFinal(int p, int seed)
    {
        if (!pOK) return false;
        var F = freeB; var R = cbits;
        foreach (int d in dirs)
        {
            int u = p + d;
            int r = rowOf[u], col = colOf[u];
            if (((F[r] >> col) & 1) != 0 && ((R[r] >> col) & 1) == 0) return false;
        }
        int size = psz + 1, m = pm;
        if (seed != 0 && ((rbits[rowOf[seed]] >> colOf[seed]) & 1) != 0)
        {
            size--;
            if (seed == m) return false;
        }
        spn = Math.Min(m, p); ssz = size;
        return true;
    }

    (int pn, int size, int live) EscapeBits(int[] nx, int p, int seed)
    {
        int Hh = H;
        Array.Clear(bx);
        int live = 0, blo = Hh, bhi = -1;
        foreach (int y in nx)
            if (y != 0)
            {
                int r = rowOf[y];
                bx[r] |= 1UL << colOf[y];
                live++;
                blo = Math.Min(blo, r); bhi = Math.Max(bhi, r);
            }
        var F = freeB;
        Array.Copy(baseB, F, Hh);
        for (int r = blo; r <= bhi; r++) F[r] &= ~bx[r];
        int pn, size;
        if (seed >= 0 && SeedChild(p, seed))
        {
            if (SeedFinal(p, seed)) { pn = spn; size = ssz; }
            else (pn, size) = FloodFreeMode(p, cbits, true, true);
        }
        else (pn, size) = FloodFree(p, cbits, false);
        while (true)
        {
            var R = cbits;
            bool any = false;
            for (int r = Math.Max(blo, 1); r <= bhi && r < Hh - 1; r++)
            {
                ulong B = bx[r];
                rm[r] = 0;
                if (B == 0) continue;
                ulong uP = F[r - 1] & ~R[r - 1], u0 = F[r] & ~R[r], uN = F[r + 1] & ~R[r + 1];
                ulong fm = uP | uP << 1 | uP >> 1 | u0 | u0 << 1 | u0 >> 1 | uN | uN << 1 | uN >> 1;
                ulong tP = F[r - 1] & ~deadB[r - 1] & ~ain[r - 1], t0 = F[r] & ~deadB[r] & ~ain[r], tN = F[r + 1] & ~deadB[r + 1] & ~ain[r + 1];
                ulong push = ((R[r] << 1) & (t0 >> 1)) | ((R[r] >> 1) & (t0 << 1)) | (R[r - 1] & tN) | (R[r + 1] & tP);
                ulong rem = B & (~ain[r] | ~fm | push);
                if (rem != 0)
                {
                    rm[r] = rem;
                    bx[r] = B & ~rem;
                    live -= System.Numerics.BitOperations.PopCount(rem);
                    any = true;
                }
            }
            if (!any) return (pn, size, live);
            for (int j = 0; j < nx.Length; j++)
            {
                int y = nx[j];
                if (y != 0 && ((rm[rowOf[y]] >> colOf[y]) & 1) != 0)
                {
                    SetOcc(y, false);
                    nx[j] = 0;
                    F[rowOf[y]] |= baseB[rowOf[y]] & (1UL << colOf[y]);
                }
            }
            (pn, size) = FloodFree(p, cbits, true);
        }
    }

    bool FrozenOff(int x) { bool off = false; return Frozen_(x, ref off) && off; }
    bool Frozen_(int x, ref bool off)
    {
        fz[x] = true;
        bool ok = BlockedAxis(x, 1, ref off) && BlockedAxis(x, W, ref off);
        fz[x] = false;
        if (ok && !b.Goal[x]) off = true;
        return ok;
    }
    bool BlockedAxis(int x, int d, ref bool off)
    {
        int l = x - d, r = x + d;
        if ((blk[l] & (BWall | BWall2)) != 0 || (blk[r] & (BWall | BWall2)) != 0 || fz[l] || fz[r]) return true;
        if (b.Dead[l] && b.Dead[r]) return true;
        return (Occ(l) && Frozen_(l, ref off)) || (Occ(r) && Frozen_(r, ref off));
    }

    ulong Key(ReadOnlySpan<int> boxes, int pnorm)
    {
        ulong h = (ulong)(uint)pnorm * 0xD6E8FEB86659FD93UL + (ulong)zoneN;
        for (int i = 0; i < zoneN; i++) h ^= zob[zone[i]];
        foreach (int x in boxes) if (Wall2(x)) h ^= zob[n + x];
        return (h ^ b.Salt) & ~3UL;
    }

    bool ZoneDead(ReadOnlySpan<int> boxes, int player)
    {
        int k = zoneN;
        Array.Sort(zone, 0, zoneN);
        for (int i = 0; i < k; i++) SetOcc(zone[i], true);
        fixedN = 0;
        foreach (int x in boxes)
            if (fixedB[x] && (!LocalFixed || NearArea(x))) { SetWall2(x, true); fixedN++; }
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
                if (hit) { for (int i = 0; i < k; i++) SetOcc(zone[i], false); return dead; }
            }
            SetBase(boxes);
            var (p0, size) = ParentFlood(player, zone, k);
            for (int i = 0; i < k; i++) SetOcc(zone[i], false);
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
        finally { foreach (int x in boxes) SetWall2(x, false); }
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
        if (bitsOK) BuildArea();
        var nx0 = nx0Arr;
        for (int i = 0; i < k; i++) nx0[i] = zone[i];
        foreach (int z in nx0) SetOcc(z, true);
        var (pn0, size0, live0) = DoEscape(nx0, p0, -1);
        foreach (int z in nx0) if (z != 0) SetOcc(z, false);
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
            int nId = baseOff / stride;
            var st = arena.AsSpan(baseOff, stride);
            for (ulong i = HashAt(st) & mask; ; i = (i + 1) & mask)
            {
                if (slots[i] == 0) { slots[i] = nId + 1; return true; }
                int o = (slots[i] - 1) * stride;
                if (arena.AsSpan(o, stride).SequenceEqual(st)) return false;
            }
        }
        Insert(0);
        int states = 1;
        var curA = cur; var nx = nxt;
        uint zs = zst;
        for (int head = 0; head * stride < arenaLen; head++)
        {
            int o = head * stride;
            for (int i = 0; i < k; i++)
            {
                curA[i] = arena[o + i];
                if (curA[i] != 0) SetOcc(curA[i], true);
            }
            int p = arena[o + k];
            (pm, psz) = ParentFlood(p, curA, k);
            pOK = bitsOK;
            for (int i = 0; i < k; i++)
            {
                int x = curA[i];
                if (x == 0) continue;
                foreach (int d in dirs)
                {
                    int t = x + d;
                    if (!InParent(x - d) || blk[t] != 0 || b.Dead[t]) continue;
                    Array.Copy(curA, nx, k);
                    bool gone = Kill && area[t] != zs;
                    SetOcc(x, false);
                    if (gone) nx[i] = 0; else { nx[i] = t; SetOcc(t, true); }
                    if (!gone && FrozenOff(t)) { SetOcc(t, false); SetOcc(x, true); continue; }
                    var (pn, size, live) = DoEscape(nx, x, gone ? 0 : t);
                    bool done = size == floorN - fixedN - live || AllGoals(nx);
                    foreach (int y in nx) if (y != 0) SetOcc(y, false);
                    for (int j = 0; j < k; j++) if (curA[j] != 0) SetOcc(curA[j], true);
                    if (done)
                    {
                        for (int j = 0; j < k; j++) if (curA[j] != 0) SetOcc(curA[j], false);
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
                            for (int j = 0; j < k; j++) if (curA[j] != 0) SetOcc(curA[j], false);
                            States += states;
                            return 3;
                        }
                    }
                    else arenaLen = baseOff;
                }
            }
            for (int i = 0; i < k; i++) if (curA[i] != 0) SetOcc(curA[i], false);
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
            R = new ulong[n * gw];
            bool Free(int x) => !b.Wall[x] && !fixedB[x];
            for (int gi = 0; gi < goals.Length; gi++)
            {
                int g = goals[gi];
                if (fixedB[g]) continue;
                uint rs = StampR();
                int qn = 0; q[qn++] = g;
                rmark[g] = rs;
                for (int i = 0; i < qn; i++)
                {
                    int y = q[i];
                    R[y * gw + (gi >> 6)] |= 1UL << (gi & 63);
                    foreach (int d in dirs)
                    {
                        int x = y - d; // a box on y-d pushed by a player on y-2d reaches y
                        if (rmark[x] != rs && Free(x) && Free(x - d)) { rmark[x] = rs; q[qn++] = x; }
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
                int gi = w << 6 | System.Numerics.BitOperations.TrailingZeroCount(cand);
                if (mOf[gi] < 0 || Augment(R, mOf[gi])) { mOf[gi] = x; return true; }
            }
        }
        return false;
    }
}
