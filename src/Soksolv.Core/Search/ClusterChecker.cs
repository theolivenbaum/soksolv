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
public sealed unsafe class ClusterChecker
{
    public static int MaxBoxes = 5;
    public static int StateCap = 1000;
    /// <summary>A* proof search (goal-directed; same minimal cost, far fewer states for alive clusters). false = Go-identical BFS.</summary>
    public static bool AStar = false;
    /// <summary>Remember box sets whose proof search hit the state cap and skip them for other player sides (alive: always safe, may lose a proof).</summary>
    public static bool SkipCapped = false;

    public ClusterTable? Shared, SharedPen;
    public bool Private;
    public long Checked;
    public HashSet<ulong> CapSets = new(), AllSets = new(); public long CapRepeat, CapSkipped;
    public long StDead, StAlive, StCap, NDead, NAlive, NCap, SplitFloods, DeadCalls, SplitFail;
    public int Pen { get; private set; }

    readonly ClusterBoard b;
    readonly ulong[] proven = new ulong[1 << 18];
    readonly ulong[] cappedSets = new ulong[1 << 14];
    // pinned (POH) scratch so hot loops use raw pointers: blk = wall | cluster box, mark = flood stamps
    readonly byte[] blkArr; readonly uint[] markArr; readonly int[] queueArr; readonly byte[] deadArr, goalArr;
    readonly byte* blk; readonly uint* mark; readonly int* queue; readonly byte* deadP, goalP;
    uint stamp;
    int qlen;
    readonly int[] queue2Arr; readonly uint[] mark2Arr;
    public static bool CheckFast = false;

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
        blkArr = GC.AllocateArray<byte>(n, pinned: true); markArr = GC.AllocateArray<uint>(n, pinned: true); queueArr = GC.AllocateArray<int>(n, pinned: true);
        deadArr = GC.AllocateArray<byte>(n, pinned: true); goalArr = GC.AllocateArray<byte>(n, pinned: true);
        for (int i = 0; i < n; i++) { blkArr[i] = (byte)(board.Wall[i] ? 1 : 0); deadArr[i] = (byte)(board.Dead[i] ? 1 : 0); goalArr[i] = (byte)(board.Goal[i] ? 1 : 0); }
        blk = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref blkArr[0]); mark = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref markArr[0]);
        queue = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref queueArr[0]);
        queue2Arr = GC.AllocateArray<int>(n, pinned: true); mark2Arr = GC.AllocateArray<uint>(n, pinned: true);
        queue2 = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref queue2Arr[0]); mark2 = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref mark2Arr[0]);
        bits = UseBits && board.W <= 64 && n / board.W <= 64 && n % board.W == 0;
        if (bits)
        {
            int W = board.W; rows = n / W;
            var fa = keepA = GC.AllocateArray<ulong>(rows * 4 + 8, pinned: true);
            freeR = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref fa[0]); regR = freeR + rows + 2; pkR = regR + rows + 2; mkR = pkR + rows + 2;
            var ra = keepB = GC.AllocateArray<byte>(2 * n, pinned: true);
            rowOf = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref ra[0]); colOf = rowOf + n;
            for (int c = 0; c < n; c++) { rowOf[c] = (byte)(c / W); colOf[c] = (byte)(c % W); if (!board.Wall[c]) freeR[c / W] |= 1UL << (c % W); }
            regLo = 1; regHi = 0;
            scrArr = GC.AllocateArray<ulong>(rows * 3 + 8, pinned: true);
            scrR = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref scrArr[0]); pkB = scrR + rows + 2; chkR = pkB + rows + 2;
        }
        deadP = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref deadArr[0]); goalP = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref goalArr[0]);
    }

    // ---- bit-row region kernel (boards up to 64 wide): free/region as one ulong per grid row --------------------------
    public static bool UseBits = true;
    readonly bool bits;
    readonly int rows;
    readonly ulong* freeR, regR, pkR, mkR;
    readonly byte* rowOf, colOf;
    int regLo, regHi; readonly ulong[]? keepA; readonly byte[]? keepB; // keep the pinned arrays alive

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    void Block(int c) { blk[c] = 1; if (bits) freeR[rowOf[c]] &= ~(1UL << colOf[c]); }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    void Unblock(int c) { blk[c] = 0; if (bits) freeR[rowOf[c]] |= 1UL << colOf[c]; }
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    bool InR(int c, uint rs) => bits ? (regR[rowOf[c]] >> colOf[c] & 1) != 0 : mark[c] == rs;

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static ulong FillRow(ulong y, ulong f)
    {
        while (true)
        {
            ulong z = (y | y << 1 | y >> 1) & f;
            if (z == y) return y;
            y = z;
        }
    }

    /// <summary>Floods the player's region from p over freeR (the start cell counts as free); regR holds it. Returns the region's minimum cell.</summary>
    int FloodBits(int p) => FloodBitsTo(p, regR, ref regLo, ref regHi);

    /// <summary>FloodBits into an arbitrary row buffer (zero outside its dirty span [lo0,hi0], which is cleared first and updated).</summary>
    int FloodBitsTo(int p, ulong* rg, ref int lo0, ref int hi0)
    {
        ulong* fr = freeR;
        for (int r = lo0; r <= hi0; r++) rg[r] = 0;
        int rp = rowOf[p]; int cp = colOf[p];
        ulong saved = fr[rp];
        fr[rp] = saved | 1UL << cp;
        int lo = rp, hi = rp;
        FloodDirty(fr, rg, rp, 1UL << cp, ref lo, ref hi);
        fr[rp] = saved;
        lo0 = lo; hi0 = hi;
        for (int r = lo; ; r++) if (rg[r] != 0) return r * b.W + System.Numerics.BitOperations.TrailingZeroCount(rg[r]);
    }

    /// <summary>Row flood from the seed bits in row rp over free rows f into reg (zero on entry); a bitmask of dirty rows replaces whole-board sweeps (needs &lt;= 64 rows).</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static void FloodDirty(ulong* f, ulong* reg, int rp, ulong seed, ref int lo, ref int hi)
    {
        ulong y0 = FillRow(seed, f[rp]) | reg[rp];
        ulong nw0 = y0 & ~reg[rp];
        reg[rp] = y0;
        ulong dirty = 0;
        if ((nw0 & f[rp - 1] & ~reg[rp - 1]) != 0) dirty |= 1UL << (rp - 1);
        if ((nw0 & f[rp + 1] & ~reg[rp + 1]) != 0) dirty |= 1UL << (rp + 1);
        while (dirty != 0)
        {
            int r = System.Numerics.BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
            ulong cur = reg[r];
            ulong y = cur | (reg[r - 1] | reg[r + 1]) & f[r];
            if (y == cur) continue;
            y = FillRow(y, f[r]);
            reg[r] = y;
            ulong nw = y & ~cur;
            if (r < lo) lo = r; if (r > hi) hi = r;
            if ((nw & f[r - 1] & ~reg[r - 1]) != 0) dirty |= 1UL << (r - 1);
            if ((nw & f[r + 1] & ~reg[r + 1]) != 0) dirty |= 1UL << (r + 1);
        }
    }

    int RegionCount()
    {
        if (!bits) return qlen;
        int c = 0;
        for (int r = regLo; r <= regHi; r++) c += System.Numerics.BitOperations.PopCount(regR[r]);
        return c;
    }

    /// <summary>Minimum cell of the last region without cell t (int.MaxValue if empty).</summary>
    int RegionMinExcluding(int t)
    {
        int tr = rowOf[t]; ulong tb = 1UL << colOf[t];
        for (int r = regLo; r <= regHi; r++)
        {
            ulong v = regR[r];
            if (r == tr) v &= ~tb;
            if (v != 0) return r * b.W + System.Numerics.BitOperations.TrailingZeroCount(v);
        }
        return int.MaxValue;
    }

    /// <summary>Pockets behind x (cells outside the parent region R, not boxes, not t) reachable from x's neighbours: lowers min to their minimum cell.</summary>
    int PocketsBits(int x, int t, int min)
    {
        bool built = false;
        for (int di = 0; di < 4; di++)
        {
            int y0 = x + b.Dirs[di];
            if (y0 == t || blk[y0] != 0) continue;
            int r0 = rowOf[y0]; ulong bit0 = 1UL << colOf[y0];
            if ((regR[r0] & bit0) != 0) continue;
            if (!built)
            {
                for (int r = 0; r < rows; r++) mkR[r] = freeR[r] & ~regR[r];
                mkR[rowOf[t]] &= ~(1UL << colOf[t]);
                for (int r = 0; r < rows; r++) pkR[r] = 0;
                built = true;
            }
            if ((pkR[r0] & bit0) != 0) continue;
            // flood the pocket from y0 over mkR (pk accumulates earlier closed pockets; they are never re-dirtied)
            int plo = r0, phi = r0;
            FloodDirty(mkR, pkR, r0, bit0, ref plo, ref phi);
        }
        if (built)
        {
            for (int r = 0; r < rows; r++)
                if (pkR[r] != 0) { int m = r * b.W + System.Numerics.BitOperations.TrailingZeroCount(pkR[r]); if (m < min) min = m; break; }
        }
        return min;
    }


    // ---- BFS with per-state stored regions (bit-row boards): the child's region is derived from the parent's, never re-flooded ------------
    ulong[]? storeArr, scrArr; ulong* store, scrR, pkB, chkR; int storeCap; int scrLo = 1, scrHi = 0, chkLo = 1, chkHi = 0, pkLo, pkHi = -1;

    void EnsureStore(int states)
    {
        if (storeCap >= states) return;
        storeCap = states + 256;
        storeArr = GC.AllocateArray<ulong>(storeCap * rows, pinned: true);
        store = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref storeArr[0]);
    }

    (bool dead, int cost) SearchBfsBits(ReadOnlySpan<int> start, int side, int capStates)
    {
        Checked++; if (Prof.On) Prof.Calls[13]++;
        int n = start.Length;
        int rows = this.rows;
        EnsureStore(capStates + 80);
        for (int i = 0; i < n; i++) Block(start[i]);
        new Span<ulong>(store, rows).Clear();
        int rlo = 1, rhi = 0;
        int p0 = FloodBitsTo(side, store, ref rlo, ref rhi);
        for (int i = 0; i < n; i++) Unblock(start[i]);

        int tsize = 2048;
        while (tsize < capStates * 2 + 64) tsize <<= 1;
        if (seenSlots.Length < tsize) seenSlots = new ulong[tsize];
        int tmask = tsize - 1;
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

        TryAdd(MkKey(start, p0), 0);
        int* dd = stackalloc int[4];
        for (int k = 0; k < 4; k++) dd[k] = b.Dirs[k];
        ushort* lanes = stackalloc ushort[8]; ushort* nl = stackalloc ushort[8];
        byte* ro = rowOf; byte* co = colOf; byte* gl = goalP; byte* dp = deadP; byte* bk = blk;
        for (int head = 0; head < qn; head++)
        {
            if (seenCount > capStates) { NCap++; StCap += head; return (false, 0); }
            if (Prof.On) Prof.Calls[14]++;
            var st = cq[head]; int depth = cdepth[head];
            *(ulong*)lanes = st.Lo; *(ulong*)(lanes + 4) = st.Hi;
            int off = 0;
            for (int i = 0; i < n; i++) { if (gl[lanes[i]] == 0) off++; Block(lanes[i]); }
            int minR = lanes[7]; // the stored player is the region's minimum cell
            ulong* reg = store + (long)head * rows;
            for (int i = 0; i < n; i++)
            {
                int x = lanes[i];
                int offx = off - (gl[x] == 0 ? 1 : 0);
                for (int k = 0; k < 4; k++)
                {
                    int d = dd[k];
                    int to = x + d;
                    int px = x - d;
                    if ((reg[ro[px]] >> co[px] & 1) == 0 || bk[to] != 0 || dp[to] != 0) continue;
                    if (offx + (gl[to] == 0 ? 1 : 0) == 0)
                    {
                        for (int j = 0; j < n; j++) Unblock(lanes[j]);
                        NAlive++; StAlive += head; return (false, depth + 1);
                    }
                    int np = FastPlayerB(x, to, minR, reg);
                    bool viaScr = np < 0;
                    if (viaScr)
                    {
                        Unblock(x); Block(to);
                        np = FloodBitsTo(x, scrR, ref scrLo, ref scrHi);
                        Unblock(to); Block(x);
                    }
                    // child lanes: box i replaced by `to`, kept ascending; player in lane 7
                    *(ulong*)nl = st.Lo; *(ulong*)(nl + 4) = st.Hi;
                    {
                        ushort v = (ushort)to; int j = i;
                        if (v > nl[i]) { while (j + 1 < n && nl[j + 1] < v) { nl[j] = nl[j + 1]; j++; } }
                        else { while (j > 0 && nl[j - 1] > v) { nl[j] = nl[j - 1]; j--; } }
                        nl[j] = v;
                    }
                    nl[7] = (ushort)np;
                    var key = new Key { Lo = *(ulong*)nl, Hi = *(ulong*)(nl + 4) };
                    if (TryAdd(key, depth + 1))
                    {
                        ulong* dst = store + (long)(qn - 1) * rows;
                        if (viaScr) Buffer.MemoryCopy(scrR, dst, rows * 8L, rows * 8L);
                        else
                        {
                            Buffer.MemoryCopy(reg, dst, rows * 8L, rows * 8L);
                            for (int r = pkLo; r <= pkHi; r++) dst[r] |= pkB[r];
                            dst[ro[to]] &= ~(1UL << co[to]); dst[ro[x]] |= 1UL << co[x];
                        }
                        if (CheckFast) CheckDerived(x, to, dst);
                    }
                    if (!viaScr && pkHi >= pkLo) { for (int r = pkLo; r <= pkHi; r++) pkB[r] = 0; pkLo = 0; pkHi = -1; }
                }
            }
            for (int i = 0; i < n; i++) Unblock(lanes[i]);
        }
        NDead++; StDead += qn;
        return (true, 0);
    }

    void CheckDerived(int x, int to, ulong* dst)
    {
        Unblock(x); Block(to);
        FloodBitsTo(x, chkR, ref chkLo, ref chkHi);
        Unblock(to); Block(x);
        for (int r = 0; r < rows; r++) if (chkR[r] != dst[r]) throw new InvalidOperationException($"cluster derived region mismatch row {r}: flood {chkR[r]:x} derived {dst[r]:x} x={x} to={to}");
    }

    /// <summary>FastPlayer for the stored-region BFS (parent region reg; pockets accumulate in pkB, rows pkLo..pkHi, cleared by the caller).</summary>
    int FastPlayerB(int x, int t, int minR, ulong* reg)
    {
        int tr = rowOf[t], tc = colOf[t];
        ulong tb = 1UL << tc;
        bool tInR = (reg[tr] & tb) != 0;
        if (tInR)
        {
            ulong* fr = freeR;
            uint m = (uint)(fr[tr - 1] >> (tc - 1)) & 7u;
            ulong bot = fr[tr + 1] >> (tc - 1);
            m |= (uint)(bot & 4) << 2 | (uint)(bot & 2) << 4 | (uint)(bot & 1) << 6;
            ulong mid = fr[tr] >> (tc - 1);
            m |= (uint)(mid & 4) << 1 | (uint)(mid & 1) << 7;
            int dx = x - t;
            m |= dx == -b.W ? 2u : dx == 1 ? 8u : dx == b.W ? 32u : 128u;
            if (SplitTab[m]) return -1;
        }
        int min = x;
        if (tInR && t == minR)
        {
            int m2 = int.MaxValue;
            ulong v = reg[tr] & ~tb;
            int r = tr;
            while (true)
            {
                if (v != 0) { m2 = r * b.W + System.Numerics.BitOperations.TrailingZeroCount(v); break; }
                if (++r >= rows) break;
                v = reg[r];
            }
            if (m2 < min) min = m2;
        }
        else if (minR < min) min = minR;
        // pockets opened behind x: cells outside the parent region, not blocked, not t, reachable from x's neighbours
        ulong* f = freeR; ulong* pk = pkB;
        bool started = false; ulong fsave = 0;
        for (int di = 0; di < 4; di++)
        {
            int y0 = x + b.Dirs[di];
            if (blk[y0] != 0 || y0 == t) continue;
            int r0 = rowOf[y0]; ulong bit0 = 1UL << colOf[y0];
            if ((reg[r0] & bit0) != 0 || (pk[r0] & bit0) != 0) continue;
            if (!started) { started = true; pkLo = rows; pkHi = -1; fsave = f[tr]; f[tr] = fsave & ~tb; }
            if (r0 < pkLo) pkLo = r0; if (r0 > pkHi) pkHi = r0;
            // dirty-row flood over cells free and outside the parent region
            ulong y1 = FillRow(bit0 | pk[r0], f[r0] & ~reg[r0]);
            ulong nw0 = y1 & ~pk[r0];
            pk[r0] = y1;
            ulong dirty = 0;
            if ((nw0 & f[r0 - 1] & ~reg[r0 - 1] & ~pk[r0 - 1]) != 0) dirty |= 1UL << (r0 - 1);
            if ((nw0 & f[r0 + 1] & ~reg[r0 + 1] & ~pk[r0 + 1]) != 0) dirty |= 1UL << (r0 + 1);
            while (dirty != 0)
            {
                int r = System.Numerics.BitOperations.TrailingZeroCount(dirty); dirty &= dirty - 1;
                ulong cu = pk[r];
                ulong g = f[r] & ~reg[r];
                ulong y = cu | (pk[r - 1] | pk[r + 1]) & g;
                if (y == cu) continue;
                y = FillRow(y, g);
                pk[r] = y;
                ulong nw = y & ~cu;
                if (r < pkLo) pkLo = r; if (r > pkHi) pkHi = r;
                if ((nw & f[r - 1] & ~reg[r - 1] & ~pk[r - 1]) != 0) dirty |= 1UL << (r - 1);
                if ((nw & f[r + 1] & ~reg[r + 1] & ~pk[r + 1]) != 0) dirty |= 1UL << (r + 1);
            }
        }
        if (started)
        {
            f[tr] = fsave;
            for (int r = pkLo; r <= pkHi; r++)
                if (pk[r] != 0) { int m = r * b.W + System.Numerics.BitOperations.TrailingZeroCount(pk[r]); if (m < min) min = m; break; }
        }
        return min;
    }

    /// <summary>SplitTab[mask]: ring (bit k = ring cell k free, k odd = orthogonal) has more than one run touching an orthogonal neighbour.</summary>
    static readonly bool[] SplitTab = MakeSplitTab();
    static bool[] MakeSplitTab()
    {
        var t = new bool[256];
        for (int m = 0; m < 256; m++)
        {
            int start = -1;
            for (int k = 0; k < 8; k++) if ((m >> k & 1) == 0) { start = k; break; }
            if (start < 0) continue;
            int runs = 0; bool inRun = false, hasOrth = false;
            for (int i = 1; i <= 8; i++)
            {
                int k = (start + i) % 8;
                if ((m >> k & 1) != 0) { if (!inRun) { inRun = true; hasOrth = false; } if (k % 2 == 1) hasOrth = true; }
                else if (inRun) { if (hasOrth) runs++; inRun = false; }
            }
            if (inRun && hasOrth) runs++;
            t[m] = runs > 1;
        }
        return t;
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
        Pen = 0; DeadCalls++;
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
        InsertionSort(cells);
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
        for (int i = 0; i < n; i++) Block(cells[i]);
        Region(side);
        SplitFloods++;
        bool split = RegionCount() < b.NFloor - n;
        if (!split) SplitFail++;
        for (int i = 0; i < n; i++) Unblock(cells[i]);
        if (!split) { SetLocal(k, 0); return false; }
        ulong setKey = 0;
        if (SkipCapped)
        {
            setKey = KeyOf(cells, 0) ^ (b.Salt & ~0xFFUL);
            if (cappedSets[(int)(setKey & (ulong)(cappedSets.Length - 1))] == setKey) { SetLocal(k, 0); CapSkipped++; return false; }
        }
        var (dead, cost) = SearchCap(cells, side, StateCap);
        if (SkipCapped && !dead && cost == 0) cappedSets[(int)(setKey & (ulong)(cappedSets.Length - 1))] = setKey;
        if (Prof.On) { ulong ks = KeyOf(cells, 0); AllSets.Add(ks); if (!dead && cost == 0) { if (!CapSets.Add(ks)) CapRepeat++; } }
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

    /// <summary>Floods the player's region from p (blocked = wall | box); returns its minimum cell. Cells hold the new stamp in mark.</summary>
    int Region(int p)
    {
        if (Prof.On) Prof.Calls[15]++;
        if (bits) { ++stamp; return FloodBits(p); }
        uint st = ++stamp;
        int w = b.W;
        int* q = queue; int tail = 0; q[tail++] = p; mark[p] = st; int m = p;
        for (int i = 0; i < tail; i++)
        {
            int c = q[i];
            int y = c - w; if (mark[y] != st && blk[y] == 0) { mark[y] = st; q[tail++] = y; if (y < m) m = y; }
            y = c + w; if (mark[y] != st && blk[y] == 0) { mark[y] = st; q[tail++] = y; }
            y = c - 1; if (mark[y] != st && blk[y] == 0) { mark[y] = st; q[tail++] = y; if (y < m) m = y; }
            y = c + 1; if (mark[y] != st && blk[y] == 0) { mark[y] = st; q[tail++] = y; }
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

    /// <summary>Tiny insertion sort (Span.Sort() allocates a comparison delegate per call under Native AOT).</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    public static void InsertionSort(Span<int> a)
    {
        for (int i = 1; i < a.Length; i++)
        {
            int x = a[i], j = i - 1;
            while (j >= 0 && a[j] > x) { a[j + 1] = a[j]; j--; }
            a[j + 1] = x;
        }
    }

    /// <summary>a is strictly ascending; replaces a[i] by v (not present in a) and keeps it ascending.</summary>
    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static void MoveSorted(Span<int> a, int i, int v)
    {
        int n = a.Length, j = i;
        if (v > a[i]) { while (j + 1 < n && a[j + 1] < v) { a[j] = a[j + 1]; j++; } }
        else { while (j > 0 && a[j - 1] > v) { a[j] = a[j - 1]; j--; } }
        a[j] = v;
    }

    [MethodImpl(MethodImplOptions.AggressiveInlining)]
    static Key MkKey(ReadOnlySpan<int> bx, int player)
    {
        // boxes sorted ascending; cells < 32768 fit 16 bits; unused slots 0xFFFF; player in slot 7
        int n = bx.Length;
        ulong lo = 0, hi = 0;
        if (n <= 4)
        {
            for (int i = 0; i < n; i++) lo |= (ulong)(ushort)bx[i] << (i * 16);
            lo |= ulong.MaxValue << (n * 16 & 63) & (n == 4 ? 0UL : ulong.MaxValue);
            hi = 0x0000FFFFFFFFFFFFUL;
        }
        else
        {
            for (int i = 0; i < 4; i++) lo |= (ulong)(ushort)bx[i] << (i * 16);
            for (int i = 4; i < n; i++) hi |= (ulong)(ushort)bx[i] << ((i - 4) * 16);
            hi |= 0x0000FFFFFFFFFFFFUL << ((n - 4) * 16) & 0x0000FFFFFFFFFFFFUL;
        }
        hi |= (ulong)(ushort)player << 48;
        return new Key { Lo = lo, Hi = hi };
    }

    /// <summary>Exhaustive BFS over cluster positions (other boxes removed). Returns (dead, cost): cost = BFS depth of the first all-on-goals state when alive, else 0.</summary>
    public (bool dead, int cost) SearchCap(ReadOnlySpan<int> start, int side, int capStates)
        => AStar && b.Dist != null ? SearchAStar(start, side, capStates) : SearchBfs(start, side, capStates);

    (bool dead, int cost) SearchBfs(ReadOnlySpan<int> start, int side, int capStates)
        => bits && start.Length <= 7 ? SearchBfsBits(start, side, capStates) : SearchBfsQ(start, side, capStates);

    (bool dead, int cost) SearchBfsQ(ReadOnlySpan<int> start, int side, int capStates)
    {
        Checked++; if (Prof.On) Prof.Calls[13]++;
        int n = start.Length;
        if (n > 7) return (false, 0);
        for (int i = 0; i < n; i++) Block(start[i]);
        int p0 = Region(side);
        for (int i = 0; i < n; i++) Unblock(start[i]);

        int tsize = 2048;
        while (tsize < capStates * 2 + 64) tsize <<= 1;   // load <= ~0.5 at the cap; small enough to stay in L1
        if (seenSlots.Length < tsize) seenSlots = new ulong[tsize];
        int tmask = tsize - 1;
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
        var dirs = b.Dirs;
        for (int head = 0; head < qn; head++)
        {
            if (seenCount > capStates) { NCap++; StCap += head; return (false, 0); }
            if (Prof.On) Prof.Calls[14]++;
            var st = cq[head]; int depth = cdepth[head];
            int off = 0;
            for (int i = 0; i < n; i++) { cur[i] = (int)((i < 4 ? st.Lo >> (i * 16) : st.Hi >> ((i - 4) * 16)) & 0xFFFF); if (goalP[cur[i]] == 0) off++; }
            int stp = (int)((st.Hi >> 48) & 0xFFFF);
            for (int i = 0; i < n; i++) Block(cur[i]);
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
                    if (InR(x - d, rs) && blk[to] == 0 && deadP[to] == 0)
                        moves[nm++] = new Move { I = i, To = to, From = x, Np = FastPlayer(x, to, minR, rs) };
                }
            }
            for (int i = 0; i < n; i++) Unblock(cur[i]);
            for (int mi = 0; mi < nm; mi++)
            {
                ref var m = ref moves[mi];
                cur.CopyTo(nb);
                if (off - (goalP[m.From] == 0 ? 1 : 0) + (goalP[m.To] == 0 ? 1 : 0) == 0) { NAlive++; StAlive += head; return (false, depth + 1); }
                MoveSorted(nb, m.I, m.To);
                int np = m.Np >= 0 && !CheckFast ? m.Np : ResolveNp(m.Np, m.From, nb);
                TryAdd(MkKey(nb, np), depth + 1);
            }
        }
        NDead++; StDead += qn;
        return (true, 0);
    }


    int[] ch = new int[2048];       // per queued state: h
    int[] cgv = new int[2048];      // per queued state: best g
    ulong[] open = new ulong[4096]; int openN;

    void OpenPush(ulong k)
    {
        if (openN == open.Length) Array.Resize(ref open, openN * 2);
        int i = openN++;
        while (i > 0) { int p = (i - 1) >> 1; if (k >= open[p]) break; open[i] = open[p]; i = p; }
        open[i] = k;
    }
    ulong OpenPop()
    {
        ulong top = open[0]; ulong x = open[--openN]; int i = 0;
        while (true)
        {
            int l = 2 * i + 1; if (l >= openN) break;
            int m = l; if (l + 1 < openN && open[l + 1] < open[l]) m = l + 1;
            if (open[m] >= x) break;
            open[i] = open[m]; i = m;
        }
        if (openN > 0) open[i] = x;
        return top;
    }

    /// <summary>
    /// A* over cluster positions with h = sum of per-box push distance to the nearest goal (admissible and
    /// consistent), so the first all-on-goals state popped has the minimal push count (same cost as BFS).
    /// Dead proofs are still exhaustive. Entry key: f(16) | (0xFFFF-g)(16) | idx(32); stale entries skipped by g.
    /// </summary>
    (bool dead, int cost) SearchAStar(ReadOnlySpan<int> start, int side, int capStates)
    {
        Checked++; if (Prof.On) Prof.Calls[13]++;
        int n = start.Length;
        if (n > 7) return (false, 0);
        var dist = b.Dist!;
        for (int i = 0; i < n; i++) Block(start[i]);
        int p0 = Region(side);
        for (int i = 0; i < n; i++) Unblock(start[i]);

        int tsize = 2048;
        while (tsize < capStates * 2 + 64) tsize <<= 1;   // load <= ~0.5 at the cap; small enough to stay in L1
        if (seenSlots.Length < tsize) seenSlots = new ulong[tsize];
        int tmask = tsize - 1;
        if (++seenStamp == 0) { Array.Clear(seenSlots); seenStamp = 1; }
        uint ss = seenStamp;
        int qn = 0; openN = 0;

        // returns state index, adding if new; improves g if better
        int Touch(in Key k, int g, int h, out bool isNew)
        {
            int i = (int)(k.Hash() & (ulong)tmask);
            while (true)
            {
                ulong sl = seenSlots[i];
                if ((uint)(sl >> 32) != ss) break;
                int idx = (int)(uint)sl - 1;
                if (cq[idx].Equals(k)) { isNew = false; if (g < cgv[idx]) { cgv[idx] = g; return idx; } return -1; }
                i = (i + 1) & tmask;
            }
            if (qn == cq.Length) { Array.Resize(ref cq, qn * 2); Array.Resize(ref cdepth, qn * 2); Array.Resize(ref ch, qn * 2); Array.Resize(ref cgv, qn * 2); }
            cq[qn] = k; ch[qn] = h; cgv[qn] = g; qn++;
            seenSlots[i] = ((ulong)ss << 32) | (uint)qn;
            isNew = true;
            return qn - 1;
        }

        Span<int> cur = stackalloc int[8];
        Span<int> nb = stackalloc int[8];
        cur = cur[..n]; nb = nb[..n];
        int h0 = 0; for (int i = 0; i < n; i++) h0 += dist[start[i]];
        int s0 = Touch(MkKey(start, p0), 0, h0, out _);
        OpenPush(((ulong)Math.Min(h0, 0xFFFF) << 48) | ((ulong)0xFFFF << 32) | (uint)s0);
        Span<Move> moves = stackalloc Move[32];
        var dirs = b.Dirs;
        while (openN > 0)
        {
            ulong e = OpenPop();
            int idx = (int)(uint)e;
            int g = 0xFFFF - (int)((e >> 32) & 0xFFFF);
            if (g != cgv[idx]) continue; // stale
            if (qn > capStates) { NCap++; StCap += qn; return (false, 0); }
            if (Prof.On) Prof.Calls[14]++;
            int hh = ch[idx];
            if (hh == 0) { NAlive++; StAlive += qn; return (false, g); }
            var st = cq[idx];
            for (int i = 0; i < n; i++) cur[i] = (int)((i < 4 ? st.Lo >> (i * 16) : st.Hi >> ((i - 4) * 16)) & 0xFFFF);
            int stp = (int)((st.Hi >> 48) & 0xFFFF);
            for (int i = 0; i < n; i++) Block(cur[i]);
            Region(stp);
            uint rs = stamp;
            int nm = 0;
            for (int i = 0; i < n; i++)
            {
                int x = cur[i];
                foreach (int d in dirs)
                {
                    int to = x + d;
                    if (InR(x - d, rs) && blk[to] == 0 && deadP[to] == 0)
                        moves[nm++] = new Move { I = i, To = to, From = x, Np = FastPlayer(x, to, stp, rs) };
                }
            }
            for (int i = 0; i < n; i++) Unblock(cur[i]);
            for (int mi = 0; mi < nm; mi++)
            {
                ref var m = ref moves[mi];
                cur.CopyTo(nb);
                MoveSorted(nb, m.I, m.To);
                int np = ResolveNp(m.Np, m.From, nb);
                int ng = g + 1, nh = hh - dist[m.From] + dist[m.To];
                int ci = Touch(MkKey(nb, np), ng, nh, out _);
                if (ci >= 0) OpenPush(((ulong)Math.Min(ng + nh, 0xFFFF) << 48) | ((ulong)(0xFFFF - ng) << 32) | (uint)ci);
            }
        }
        NDead++; StDead += qn;
        return (true, 0);
    }

    int ResolveNp(int fast, int from, ReadOnlySpan<int> nb)
    {
        if (fast >= 0 && !CheckFast) return fast;
        uint saved = stamp;
        for (int i = 0; i < nb.Length; i++) Block(nb[i]);
        int slow = Region(from);
        for (int i = 0; i < nb.Length; i++) Unblock(nb[i]);
        if (fast >= 0 && fast != slow) throw new InvalidOperationException($"cluster fast normalization {fast} != flood {slow}");
        return slow;
    }

    struct Move { public int I, To, From, Np; }

    // second flood scratch for pocket floods (must not disturb the parent's region stamp/queue)
    int* queue2; uint* mark2; uint stamp2;

    /// <summary>
    /// Child's normalized player (minimum cell of the child's region) after a box moves x -> t with the player
    /// ending on x, computed from the parent's region R (stamped rs, minimum minR, cells in queue[0..qlen)):
    ///  - if t is in R and its removal might split R (8-ring has more than one run touching an orthogonal
    ///    neighbour) the caller must flood (-1);
    ///  - cells behind the old box position (neighbours of x outside R) form pockets that merge into the region
    ///    through x: flood only those pockets (t blocked);
    ///  - if t was R's minimum the new minimum is rescanned from R's cell list.
    /// </summary>
    int FastPlayer(int x, int t, int minR, uint rs)
    {
        int w = b.W;
        bool tInR = InR(t, rs);
        if (tInR)
        {
            if (bits)
            {
                int tr = rowOf[t], tc = colOf[t];
                uint m = (uint)(freeR[tr - 1] >> (tc - 1)) & 7u;                      // k = 0,1,2
                ulong bot = freeR[tr + 1] >> (tc - 1);
                m |= (uint)(bot & 4) << 2 | (uint)(bot & 2) << 4 | (uint)(bot & 1) << 6; // c+1 -> k4, c -> k5, c-1 -> k6
                ulong mid = freeR[tr] >> (tc - 1);
                m |= (uint)(mid & 4) << 1 | (uint)(mid & 1) << 7;                       // c+1 -> k3, c-1 -> k7
                int dx = x - t;
                m |= dx == -b.W ? 2u : dx == 1 ? 8u : dx == b.W ? 32u : 128u;           // x counts as free
                if (SplitTab[m]) return -1;
            }
            else
            {
            Span<int> ring = [-w - 1, -w, -w + 1, 1, w + 1, w, w - 1, -1]; // circular; odd indices are orthogonal
            Span<bool> free = stackalloc bool[8];
            int start = -1;
            for (int k = 0; k < 8; k++)
            {
                int y = t + ring[k];
                free[k] = blk[y] == 0 || y == x;
                if (!free[k] && start < 0) start = k;
            }
            if (start >= 0)
            {
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
            }
            }
        }
        int min = x;
        if (tInR && t == minR)
        {
            int m2 = int.MaxValue;
            if (bits) m2 = RegionMinExcluding(t);
            else for (int i = 0; i < qlen; i++) { int c = queue[i]; if (c != t && c < m2) m2 = c; }
            min = Math.Min(min, m2);
        }
        else min = Math.Min(min, minR);
        // pockets opened behind x
        if (bits) return PocketsBits(x, t, min);
        uint st2 = 0;
        for (int di = 0; di < 4; di++)
        {
            int y0 = x + b.Dirs[di];
            if (y0 == t || blk[y0] != 0 || InR(y0, rs)) continue;
            if (st2 == 0) st2 = ++stamp2;
            if (mark2[y0] == st2) continue;
            int tail = 0; queue2[tail++] = y0; mark2[y0] = st2;
            for (int i = 0; i < tail; i++)
            {
                int c = queue2[i];
                if (c < min) min = c;
                for (int dj = 0; dj < 4; dj++)
                {
                    int y = c + b.Dirs[dj];
                    if (mark2[y] != st2 && blk[y] == 0 && y != t && mark[y] != rs) { mark2[y] = st2; queue2[tail++] = y; }
                }
            }
        }
        return min;
    }
}
