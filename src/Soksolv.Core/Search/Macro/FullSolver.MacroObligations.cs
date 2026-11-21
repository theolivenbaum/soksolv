namespace Soksolv.Search;

/// <summary>Order ideal of route-conditioned packing events inferred from the plan's witness (Go packing_causal.go).</summary>
public sealed class PackingDag
{
    public struct Ev { public int To, Prev, Next; }
    public List<Ev> Events = new();
    public int[] Start = [], Consumers = [], DependencyStart = [], Dependencies = [], ReadStart = [], ReadCells = [];
    public bool[] Credit = [], Live = [];
    public List<int> Queue = new();
    public int Edges;

    /// <summary>Greatest prerequisite-closed event set consistent with occupancy (credit[i]).</summary>
    public int Infer(bool[] occ)
    {
        for (int i = 0; i < Credit.Length; i++) Credit[i] = true;
        Array.Clear(Live);
        Queue.Clear();
        void Fail(int i) { if (Credit[i]) { Credit[i] = false; Queue.Add(i); } }
        for (int i = 0; i < Events.Count; i++)
        {
            var e = Events[i];
            if (!occ[e.To] && e.Next < 0) Fail(i);
        }
        for (int head = 0; head < Queue.Count; head++)
        {
            int i = Queue[head];
            for (int k = Start[i]; k < Start[i + 1]; k++) Fail(Consumers[k]);
            int p = Events[i].Prev;
            if (p >= 0 && !occ[Events[p].To]) Fail(p);
        }
        for (int i = 0; i < Events.Count; i++)
        {
            var e = Events[i];
            if (Credit[i] && occ[e.To] && (e.Next < 0 || !Credit[e.Next])) Live[e.To] = true;
        }
        return Events.Count - Queue.Count;
    }
}

public sealed unsafe partial class FullSolver
{
    // ---- packing obligations (Go packing_route.go + packing_causal.go + packing_obligations.go; PackingObligations) ---------------------------------
    const int PackingRouteVisitCap = 8_000_000;

    sealed class PackingRoute
    {
        readonly FullSolver s;
        public readonly bool[] Fixed, Touched;
        readonly uint[] mark, seen; uint stamp, generation;
        readonly int[] prev, walkPrev;
        readonly List<int> q = new(), states = new(), path = new();
        public readonly List<int> Used = new();
        public int Visits;
        public PackingRoute(FullSolver s)
        {
            this.s = s; int n = s.cells;
            Fixed = new bool[n]; Touched = new bool[n]; seen = new uint[4 * n]; prev = new int[4 * n];
            mark = GC.AllocateArray<uint>(n, pinned: true); walkPrev = GC.AllocateArray<int>(n, pinned: true);
            blkArr = GC.AllocateArray<byte>(n, pinned: true); qArr = GC.AllocateArray<int>(n + 4, pinned: true);
            markP = (uint*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref mark[0]); walkPrevP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref walkPrev[0]);
            blkP = (byte*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref blkArr[0]); qP = (int*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref qArr[0]);
            for (int c = 0; c < n; c++) blkP[c] = (byte)(s.wall[c] ? 1 : 0);
            if (s.W <= 64 && s.lv.H + 2 <= 64 && !RowsOff)
            {
                rrows = s.lv.H + 2;
                rfreeA = GC.AllocateArray<ulong>(rrows, pinned: true); rregA = GC.AllocateArray<ulong>(rrows, pinned: true);
                rfreeP = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rfreeA[0]); rregP = (ulong*)System.Runtime.CompilerServices.Unsafe.AsPointer(ref rregA[0]);
                rowsOK = true; rlo = rrows; rhi = -1;
            }
        }
        public static bool RowsOff = Environment.GetEnvironmentVariable("SOKSOLV_PACKROWS_OFF") == "1";   // test hook: queue floods everywhere
        public static bool RowsCheck = Environment.GetEnvironmentVariable("SOKSOLV_CHECK_PACKROWS") == "1";
        readonly bool rowsOK; readonly int rrows; readonly ulong[]? rfreeA, rregA; readonly ulong* rfreeP, rregP; int rlo, rhi;

        /// <summary>Free rows (floor minus walls minus the fixed boxes) for <see cref="FloodF"/>; called after blkP changed.</summary>
        void BuildRows()
        {
            int w = s.W, h = s.lv.H;
            for (int r = 0; r < h; r++)
            {
                ulong m = 0; int b = r * w;
                for (int x = 0; x < w; x++) if (blkP[b + x] == 0) m |= 1UL << x;
                rfreeP[r + 1] = m;
            }
            rfreeP[0] = 0; rfreeP[h + 1] = 0;
        }

        /// <summary>Same result and visit accounting as <see cref="Flood"/>, as a row flood (no BFS tree, so <see cref="Walk"/> keeps the queue version); membership via <see cref="Reach"/>.</summary>
        (int norm, bool ok) FloodF(int box, int player)
        {
            if (!rowsOK) return Flood(box, player);
            if (player < 0 || s.wall[player] || Fixed[player] || player == box) return (0, false);
            int w = s.W;
            int rp = player / w + 1, cp = player - (rp - 1) * w;
            ulong* fr = rfreeP; ulong* rg = rregP;
            int rb = 0; ulong saved = 0;
            if (box >= 0) { rb = box / w + 1; saved = fr[rb]; fr[rb] = saved & ~(1UL << (box - (rb - 1) * w)); }
            if (rhi >= 0) for (int r = rlo; r <= rhi; r++) rg[r] = 0;
            int lo = rp, hi = rp;
            RowK.Flood(fr, rg, rp, 1UL << cp, ref lo, ref hi);
            if (box >= 0) fr[rb] = saved;
            rlo = lo; rhi = hi;
            int size = 0;
            for (int r = lo; r <= hi; r++) size += System.Numerics.BitOperations.PopCount(rg[r]);
            int visits = Visits;
            if (visits + size >= PackingRouteVisitCap) { Visits = PackingRouteVisitCap; return (0, false); }
            if (((visits + size) >> 10) != (visits >> 10) && (s.opt.Stop || (s.opt.Cancel?.Stopped ?? false))) { Visits = visits + size; return (0, false); }
            Visits = visits + size;
            if (RowsCheck) CheckRows(box, player, (lo - 1) * w + System.Numerics.BitOperations.TrailingZeroCount(rg[lo]), size);
            return ((lo - 1) * w + System.Numerics.BitOperations.TrailingZeroCount(rg[lo]), true);
        }

        void CheckRows(int box, int player, int norm, int size)
        {
            int saveVisits = Visits;
            Visits = 0;
            var (n2, ok2) = Flood(box, player);
            int got = Visits; Visits = saveVisits;
            if (!ok2 || n2 != norm || got != size) throw new InvalidOperationException($"packing route row flood: queue (norm {n2}, visits {got}) != rows (norm {norm}, visits {size})");
            int w = s.W;
            for (int c = 0; c < s.cells; c++) if ((mark[c] == stamp) != Reach(c)) throw new InvalidOperationException($"packing route row flood: membership differs at {c}");
        }

        /// <summary>Is cell c in the region of the last <see cref="FloodF"/> / <see cref="Flood"/>?</summary>
        bool Reach(int c)
        {
            if (!rowsOK) return mark[c] == stamp;
            int r = c / s.W + 1;
            return (rregP[r] >> (c - (r - 1) * s.W) & 1) != 0;
        }
        readonly byte[] blkArr; readonly int[] qArr; readonly uint* markP; readonly int* walkPrevP; readonly byte* blkP; readonly int* qP;


        (int norm, bool ok) Flood(int box, int player)
        {
            if (player < 0 || s.wall[player] || Fixed[player] || player == box) return (0, false);
            uint st = ++stamp;
            uint* mk = markP; byte* bk = blkP; int* qq = qP; int* wp = walkPrevP;
            int w = s.W;
            mk[player] = st;
            int tail = 0; qq[tail++] = player;
            int norm = player;
            int visits = Visits;
            for (int head = 0; head < tail; head++)
            {
                int x = qq[head];
                if (x < norm) norm = x;
                visits++;
                if (visits >= PackingRouteVisitCap) { Visits = visits; return (0, false); }
                if ((visits & 1023) == 0 && (s.opt.Stop || (s.opt.Cancel?.Stopped ?? false))) { Visits = visits; return (0, false); }
                int y = x - w; if (bk[y] == 0 && y != box && mk[y] != st) { mk[y] = st; wp[y] = x; qq[tail++] = y; }
                y = x + w; if (bk[y] == 0 && y != box && mk[y] != st) { mk[y] = st; wp[y] = x; qq[tail++] = y; }
                y = x - 1; if (bk[y] == 0 && y != box && mk[y] != st) { mk[y] = st; wp[y] = x; qq[tail++] = y; }
                y = x + 1; if (bk[y] == 0 && y != box && mk[y] != st) { mk[y] = st; wp[y] = x; qq[tail++] = y; }
            }
            Visits = visits;
            return (norm, true);
        }

        void Note(int c) { if (!Touched[c]) { Touched[c] = true; Used.Add(c); } }

        bool Walk(int box, int from, int to)
        {
            var (_, ok) = Flood(box, from);
            if (!ok || mark[to] != stamp) return false;
            for (int x = to; ; x = walkPrev[x]) { Note(x); if (x == from) break; }
            return true;
        }

        /// <summary>Reconstructs a primitive pull witness (other boxes fixed) and records its swept box/support/walking cells.</summary>
        public bool Compile(int[] parentRequired, int parentPlayer, PMove m, int childPlayer)
        {
            Array.Clear(Fixed); Array.Clear(Touched);
            Used.Clear();
            for (int c = 0; c < blkArr.Length; c++) blkP[c] = (byte)(s.wall[c] ? 1 : 0);
            foreach (int b in parentRequired) if (b != m.From) { Fixed[b] = true; blkP[b] = 1; }
            if (rowsOK) BuildRows();
            if (!FloodF(m.From, parentPlayer).ok) return false;
            uint g = ++generation;
            states.Clear();
            for (int di = 0; di < 4; di++)
                if (Reach(m.From + s.dirs[di]))
                {
                    int k = m.From * 4 + di;
                    seen[k] = g; prev[k] = -1; states.Add(k);
                }
            int last = -2;
            if (m.From == m.To)
            {
                int box = m.From;
                if (m.Remove) box = -1;
                var (norm, ok) = FloodF(box, parentPlayer);
                if (!ok) return false;
                if (norm == childPlayer) last = -1;
            }
            for (int head = 0; last == -2 && head < states.Count; head++)
            {
                int k = states[head];
                int b = k / 4, d = s.dirs[k % 4];
                int to = b + d, player = b + 2 * d;
                if (s.wall[to] || Fixed[to] || s.wall[player] || Fixed[player]) continue;
                var (norm, ok) = FloodF(to, player);
                if (!ok) return false;
                if (to == m.To)
                {
                    if (m.Remove)
                    {
                        (norm, ok) = FloodF(-1, player);
                        if (!ok) return false;
                    }
                    if (norm == childPlayer) { last = k; break; }
                    if (m.Remove && !FloodF(to, player).ok) return false;
                }
                for (int di = 0; di < 4; di++)
                {
                    int kk = to * 4 + di;
                    if (Reach(to + s.dirs[di]) && seen[kk] != g) { seen[kk] = g; prev[kk] = k; states.Add(kk); }
                }
            }
            if (last == -2) return false;
            path.Clear();
            for (int k = last; k >= 0; k = prev[k]) path.Add(k);
            path.Reverse();
            int plr = parentPlayer, bx = m.From;
            Note(bx);
            foreach (int k in path)
            {
                if (bx != k / 4) return false;
                int d = s.dirs[k % 4];
                if (!Walk(bx, plr, bx + d)) return false;
                Note(bx + d); Note(bx + 2 * d);
                plr = bx + 2 * d;
                bx += d;
            }
            if (bx != m.To) return false;
            if (m.Remove) bx = -1;
            return Walk(bx, plr, childPlayer);
        }
    }

    /// <summary>Go compilePackingDAG: box lifetimes and swept resources of the witness become a partial order.</summary>
    PackingDag? CompilePackingDag(PackingPlan p)
    {
        int np = p.Required.Length;
        if (np < 2 || p.Expanded == 0) return null;
        int n = p.Required[0].Length + np - 1;
        if (n > 512) return null;
        int words = (n + 63) / 64;
        var deps = new ulong[n * words];
        var readers = new ulong[cells * words];
        var owner = new int[cells]; var cleared = new int[cells];
        Array.Fill(owner, -1); Array.Fill(cleared, -1);
        var d = new PackingDag { Credit = new bool[n], Live = new bool[cells] };
        var readCells = new List<int>();
        var readStart = new List<int> { 0 };
        foreach (int c in p.Required[0])
        {
            owner[c] = d.Events.Count;
            d.Events.Add(new PackingDag.Ev { To = c, Prev = -1, Next = -1 });
            readCells.Add(c);
            readStart.Add(readCells.Count);
        }
        var r = new PackingRoute(this);
        for (int phase = 0; phase + 1 < np; phase++)
        {
            if (!r.Compile(p.Required[phase + 1], p.Players[phase + 1], p.Moves[phase], p.Players[phase])) { p.CausalVisits = r.Visits; return null; }
            int i = d.Events.Count;
            readCells.AddRange(r.Used);
            if (readCells.Count > 1_000_000) { p.CausalVisits = r.Visits; return null; }
            readStart.Add(readCells.Count);
            var m = p.Moves[phase];
            int src = -1;
            if (!m.Remove) src = m.To;
            int dst = m.From;
            int rowOff = i * words;
            void Add(int j) { if (j >= 0 && j != i) deps[rowOff + (j >> 6)] |= 1UL << (j & 63); }
            int prev = -1;
            if (src >= 0)
            {
                prev = owner[src];
                if (prev < 0) return null;
                Add(prev);
            }
            if (owner[dst] >= 0 && dst != src) return null;
            foreach (int c in r.Used)
            {
                if (c == src) continue;
                Add(cleared[c]);
            }
            // every earlier empty-resource reader must precede its next writer
            for (int w = 0; w < words; w++) deps[rowOff + w] |= readers[dst * words + w];
            foreach (int c in r.Used)
                if (c != src && c != dst) readers[c * words + i / 64] |= 1UL << (i & 63);
            if (src >= 0)
            {
                var pe = d.Events[prev]; pe.Next = i; d.Events[prev] = pe;
                owner[src] = -1;
                cleared[src] = i;
                Array.Clear(readers, src * words, words);
            }
            owner[dst] = i;
            Array.Clear(readers, dst * words, words);
            d.Events.Add(new PackingDag.Ev { To = dst, Prev = prev, Next = -1 });
        }
        p.CausalVisits = r.Visits;
        d.Start = new int[n + 1];
        d.DependencyStart = new int[n + 1];
        var dependencies = new List<int>();
        for (int i = 0; i < n; i++)
        {
            d.DependencyStart[i] = dependencies.Count;
            for (int w = 0; w < words; w++)
            {
                ulong v = deps[i * words + w];
                while (v != 0)
                {
                    int j = w * 64 + System.Numerics.BitOperations.TrailingZeroCount(v);
                    v &= v - 1;
                    if (j >= i) return null;
                    d.Start[j + 1]++;
                    dependencies.Add(j);
                }
            }
        }
        d.DependencyStart[n] = dependencies.Count;
        d.Dependencies = [.. dependencies];
        for (int i = 1; i <= n; i++) d.Start[i] += d.Start[i - 1];
        d.Edges = d.Start[n];
        d.Consumers = new int[d.Edges];
        var next = (int[])d.Start.Clone();
        for (int i = 0; i < n; i++)
            for (int w = 0; w < words; w++)
            {
                ulong v = deps[i * words + w];
                while (v != 0)
                {
                    int j = w * 64 + System.Numerics.BitOperations.TrailingZeroCount(v);
                    v &= v - 1;
                    d.Consumers[next[j]++] = i;
                }
            }
        d.ReadCells = [.. readCells]; d.ReadStart = [.. readStart];
        return d;
    }

    // per-open scratch (Go packReady/packUse/packBlock)
    bool[]? packReady; int[]? packUse, packBlock; bool packPrepared;

    void PreparePackingObligations()
    {
        packPrepared = false;
        var d = mPlan?.Dag;
        if (d == null) return;
        if (packReady == null) { packReady = new bool[cells]; packUse = new int[cells]; packBlock = new int[cells]; }
        Array.Clear(packReady); Array.Clear(packUse!); Array.Clear(packBlock!);
        d.Infer(occ);
        for (int i = 0; i < d.Events.Count; i++)
        {
            var e = d.Events[i];
            if (d.Credit[i]) continue;
            bool ready = true;
            for (int k = d.DependencyStart[i]; k < d.DependencyStart[i + 1]; k++)
                if (!d.Credit[d.Dependencies[k]]) { ready = false; break; }
            if (!ready) continue;
            packReady[e.To] = true;
            int src = -1;
            if (e.Prev >= 0) src = d.Events[e.Prev].To;
            for (int k = d.ReadStart[i]; k < d.ReadStart[i + 1]; k++)
            {
                int c = d.ReadCells[k];
                if (c != src) { packUse![c]++; if (occ[c]) packBlock![c]++; }
            }
        }
        packPrepared = true;
        stats.PackingPrepQueries++;
    }

    /// <summary>Fallback packing advice: an enabled placement, else the move that most reduces blockage of the ready routes. true = "clearing".</summary>
    bool PackingObligationAdvice(Action<int> choose)
    {
        if (!packPrepared) return false;
        bool found = false;
        for (int i = 0; i < mCandN; i++)
            if (packReady![mCands[i].T]) { choose(i); found = true; }
        if (found) return false;
        int best = 0;
        for (int i = 0; i < mCandN; i++) best = Math.Max(best, packBlock![mCands[i].B] - packUse![mCands[i].T]);
        if (best == 0) return false;
        for (int i = 0; i < mCandN; i++)
            if (packBlock![mCands[i].B] - packUse![mCands[i].T] == best) choose(i);
        return true;
    }
}
