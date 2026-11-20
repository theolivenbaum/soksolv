namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- features ---------------------------------------------------------------------------------------------------
    int PackedCount()
    {
        int maxR = -1;
        foreach (int g in goals) if (!occ[g]) maxR = Math.Max(maxR, round[g]);
        int c = 0;
        foreach (int g in goals) if (occ[g] && round[g] >= maxR) c++;
        return c;
    }

    int Regions()
    {
        if (floorCells.Length == 0)
        {
            var l = new List<int>();
            for (int c = 0; c < cells; c++) if (!wall[c]) l.Add(c);
            floorCells = [.. l];
        }
        uint rs = ++rstamp;
        int cnt = 0;
        foreach (int c in floorCells)
        {
            if (occ[c] || rmark[c] == rs) continue;
            cnt++;
            rmark[c] = rs;
            var q = cqueue; int tail = 0; q[tail++] = c;
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (rmark[y] != rs && !wall[y] && !occ[y]) { rmark[y] = rs; q[tail++] = y; }
                }
        }
        return cnt;
    }

    // ---- PI-corral ---------------------------------------------------------------------------------------------------
    uint PiCorral(uint st)
    {
        if (cstamp > 1u << 31) { Array.Clear(cmark); cstamp = 0; }
        uint baseS = cstamp;
        uint best = 0; int bestBoxes = int.MaxValue;
        var q = cqueue;
        for (int c = 0; c < cells; c++)
        {
            if (wall[c] || occ[c] || mark[c] == st || cmark[c] > baseS) continue;
            uint cs = ++cstamp;
            cmark[c] = cs;
            int tail = 0; q[tail++] = c;
            int nbar = 0;
            bool unsolved = false;
            for (int i = 0; i < tail; i++)
            {
                int x = q[i];
                unsolved = unsolved || (goal[x] && !occ[x]);
                foreach (int d in dirs)
                {
                    int nb = x + d;
                    if (wall[nb]) { }
                    else if (cmark[nb] == cs || (mark[nb] == st && !occ[nb])) { }
                    else if (occ[nb])
                    {
                        cmark[nb] = cs; q[tail++] = nb;
                        unsolved = unsolved || !goal[nb];
                        if (TouchesReach(nb, st)) barrier[nbar++] = nb;
                    }
                    else { cmark[nb] = cs; q[tail++] = nb; }
                }
            }
            if (nbar >= bestBoxes) continue;
            bool ok = true;
            for (int bi = 0; bi < nbar; bi++)
            {
                int b = barrier[bi];
                bool pushable = false;
                foreach (int d in dirs)
                {
                    int t = b + d;
                    if (mark[b - d] != st || wall[t] || occ[t] || dist[t] < 0) continue;
                    if (cmark[t] != cs) { ok = false; break; }
                    pushable = true;
                }
                if (!ok || !pushable) { ok = false; break; }
            }
            if (ok && unsolved) { best = cs; bestBoxes = nbar; }
        }
        return best;
    }

    bool TouchesReach(int b, uint st)
    {
        foreach (int d in dirs) if (mark[b + d] == st) return true;
        return false;
    }

    // ---- two-box deadlocks ---------------------------------------------------------------------------------------------
    void PairOffsets()
    {
        var l = new List<int>();
        for (int dy = -2; dy <= 2; dy++) for (int dx = -2; dx <= 2; dx++) if (dx != 0 || dy != 0) l.Add(dy * W + dx);
        poff = [.. l];
        popp = new int[poff.Length];
        for (int k = 0; k < poff.Length; k++) popp[k] = Array.IndexOf(poff, -poff[k]);
        dpair = new sbyte[cells * poff.Length];
    }

    bool[]? pocc; uint[]? pmark, pseen; uint pst, pst2; int[]? pq;

    bool PairDead(int a, int b)
    {
        const int capStates = 200;
        pocc ??= new bool[cells]; pmark ??= new uint[cells]; pseen ??= new uint[cells]; pq ??= new int[cells];
        var po = pocc; int pqn = 0;
        (int min, uint st) PReach(int p)
        {
            uint st = ++pst;
            int tail = 0; pq[tail++] = p; pmark[p] = st; int m = p;
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int y = pq[i] + d;
                    if (pmark[y] != st && !wall[y] && !po[y]) { pmark[y] = st; pq[tail++] = y; if (y < m) m = y; }
                }
            pqn = tail;
            return (m, st);
        }
        po[a] = true; po[b] = true;
        try
        {
            uint seenStamp = ++pst2;
            var starts = new List<int>();
            foreach (int c in FloorCells())
            {
                if (po[c] || pseen![c] == seenStamp) continue;
                var (m, _) = PReach(c);
                starts.Add(m);
                for (int i = 0; i < pqn; i++) pseen![pq[i]] = seenStamp;
            }
            foreach (int p0 in starts)
            {
                var seen = new HashSet<(int, int, int)> { (a, b, p0) };
                var q = new List<(int x, int y, int p)> { (a, b, p0) };
                for (int i = 0; i < q.Count; i++)
                {
                    if (seen.Count > capStates) return false;
                    var cur = q[i];
                    po[a] = false; po[b] = false;
                    po[cur.x] = true; po[cur.y] = true;
                    var (_, st) = PReach(cur.p);
                    var moves = new List<(int box, int t, int other)>();
                    for (int side = 0; side < 2; side++)
                    {
                        int bx = side == 0 ? cur.x : cur.y, other = side == 0 ? cur.y : cur.x;
                        foreach (int d in dirs)
                        {
                            int t = bx + d;
                            if (pmark![bx - d] == st && !wall[t] && t != other && dist[t] >= 0) moves.Add((bx, t, other));
                        }
                    }
                    po[cur.x] = false; po[cur.y] = false;
                    foreach (var m in moves)
                    {
                        if (goal[m.t] && goal[m.other]) { po[a] = true; po[b] = true; return false; }
                        po[m.t] = true; po[m.other] = true;
                        var (np, _) = PReach(m.box);
                        po[m.t] = false; po[m.other] = false;
                        var nn = (Math.Min(m.t, m.other), Math.Max(m.t, m.other), np);
                        if (seen.Add(nn)) q.Add(nn);
                    }
                    po[a] = true; po[b] = true;
                }
            }
            return true;
        }
        finally { po[a] = false; po[b] = false; }
    }

    int[] FloorCells()
    {
        if (floorCells.Length == 0)
        {
            var l = new List<int>();
            for (int c = 0; c < cells; c++) if (!wall[c]) l.Add(c);
            floorCells = [.. l];
        }
        return floorCells;
    }

    bool PairDeadlock(int t)
    {
        if (goal[t] || dist[t] < 0) return false;
        int np = poff.Length;
        int baseI = t * np;
        for (int k = 0; k < np; k++)
        {
            int x = t + poff[k];
            if (x < 0 || x >= cells || !occ[x] || (goal[t] && goal[x])) continue;
            sbyte v = dpair[baseI + k];
            if (v == 0)
            {
                stats.PairsTried++;
                v = 2;
                if (PairDead(t, x)) { v = 1; stats.PairsDead++; }
                dpair[baseI + k] = v;
                dpair[x * np + popp[k]] = v;
            }
            if (v == 1) return true;
        }
        return false;
    }

    bool GoalAreaDeadlock(int t)
    {
        if (!goal[t]) return false;
        bool off = false;
        if (!Frozen(t, ref off)) return false;
        ga ??= new GoalArea(wall, goals, dirs);
        Span<int> fset = goals.Length <= 256 ? stackalloc int[goals.Length] : new int[goals.Length];
        int nf = 0;
        foreach (int g in goals)
            if (occ[g]) { bool o2 = false; if (Frozen(g, ref o2)) fset[nf++] = g; }
        return ga.Dead(fset[..nf], occ);
    }

    // ---- macros -------------------------------------------------------------------------------------------------------------
    Push[][]? MacroMoves(ReadOnlySpan<int> cur, int id, uint st)
    {
        int maxR = -1;
        foreach (int g in goals) if (!occ[g]) maxR = Math.Max(maxR, round[g]);
        if (maxR < 0 || nodes.Parent(id) < 0) return null;
        int moved = nodes.From(id) + dirs[nodes.Dir(id)];
        if (macros.TryGetValue(id, out var seq0)) { var last = seq0[^1]; moved = last.From + dirs[last.Dir]; }
        List<Push[]>? res = null;
        foreach (int b in cur)
        {
            if (b != moved || !entrance[b]) continue;
            var seq = BoxPath(b, maxR, st);
            if (seq != null) { stats.Macros++; (res ??= new()).Add(seq); }
        }
        return res?.ToArray();
    }

    Push[]? BoxPath(int b, int maxR, uint st)
    {
        const int limit = 500;
        if (pstamp > 1u << 31 || bstamp > 1u << 31) { Array.Clear(mark3); Array.Clear(bseenStamp); pstamp = bstamp = 0; }
        uint bs = ++bstamp;
        var q = new List<int>();
        for (int di = 0; di < 4; di++)
        {
            int d = dirs[di];
            if (mark[b - d] == st) { int k = b * 4 + di; bseenStamp[k] = bs; bprev[k] = -1; q.Add(k); }
        }
        occ[b] = false;
        try
        {
            for (int i = 0; i < q.Count && i < limit; i++)
            {
                int k = q[i];
                int c = k / 4, di = k % 4;
                int nc = c + dirs[di];
                if (wall[nc] || occ[nc] || dist[nc] < 0) continue;
                if (goal[nc] && round[nc] == maxR)
                {
                    var seq = new List<Push>();
                    for (; k >= 0; k = bprev[k]) seq.Add(new Push(k / 4, (sbyte)(k % 4)));
                    seq.Reverse();
                    return seq.ToArray();
                }
                occ[nc] = true;
                uint ps = ++pstamp;
                var pq = cqueue; int tail = 0; pq[tail++] = c; mark3[c] = ps;
                for (int j = 0; j < tail; j++)
                {
                    int x = pq[j];
                    foreach (int d in dirs)
                    {
                        int y = x + d;
                        if (mark3[y] != ps && !wall[y] && !occ[y]) { mark3[y] = ps; pq[tail++] = y; }
                    }
                }
                occ[nc] = false;
                for (int d2i = 0; d2i < 4; d2i++)
                {
                    int k2 = nc * 4 + d2i;
                    if (mark3[nc - dirs[d2i]] == ps && bseenStamp[k2] != bs) { bseenStamp[k2] = bs; bprev[k2] = k; q.Add(k2); }
                }
            }
            return null;
        }
        finally { occ[b] = true; }
    }

    // ---- move encoding --------------------------------------------------------------------------------------------------------
    static uint EncodeMove(int i, int dir, int wd, bool macro, int r)
    {
        uint m = (uint)i | (uint)dir << 9 | (uint)wd << 11 | (uint)Math.Min(r, 127) << 13;
        if (macro) m |= 1u << 12;
        return m;
    }
    static void DecodeMove(uint m, out int i, out int dir, out int wd, out bool macro, out int r)
    {
        i = (int)(m & 0x1FF); dir = (int)(m >> 9 & 3); wd = (int)(m >> 11 & 1); macro = (m >> 12 & 1) == 1; r = (int)(m >> 13 & 0x7F);
    }

    // ---- open -----------------------------------------------------------------------------------------------------------------
    void Open(int id, ReadOnlySpan<int> cur, int h, uint st)
    {
        int nc = 0;
        var op = new Opened { Start = mv.Count };
        int oi = opened.Add(op);
        nodes.MvIdx(id) = oi;
        try
        {
            long tf = Prof.Start();
            bool feas = Feasible(cur);
            Prof.Stop(10, tf);
            if (!feas) { stats.Assignment++; return; }
            var seqs = MacroMoves(cur, id, st);
            if (seqs != null)
            {
                nodeMacros[id] = seqs;
                for (int k = 0; k < seqs.Length; k++)
                    cands[nc++] = new Cand { Code = EncodeMove(k, 0, 0, true, 0), Wd = 0, Ch = h - Hc(seqs[k][0].From) };
            }
            int np = 0;
            int best = nodes.Reg(id), bestK = -1;
            long tc = Prof.Start();
            uint corral = PiCorral(st);
            Prof.Stop(5, tc);
            if (corral != 0) stats.Corrals++;
            for (int i = 0; i < cur.Length; i++)
            {
                int b = cur[i];
                if (corral != 0 && cmark[b] != corral) continue;
                for (int di = 0; di < 4; di++)
                {
                    int d = dirs[di], t = b + d;
                    if (mark[b - d] != st || wall[t] || occ[t]) continue;
                    if (dist[t] < 0) { stats.DeadSquare++; continue; }
                    occ[b] = false; occ[t] = true;
                    long t0 = Prof.Start();
                    bool f1 = Frozen2x2(t) ? (stats.Frozen2x2++ >= 0) : FreezeDeadlock(t) && (stats.Freeze++ >= 0);
                    Prof.Stop(2, t0); t0 = Prof.Start();
                    bool f2 = !f1 && GoalAreaDeadlock(t) && (stats.GoalArea++ >= 0);
                    Prof.Stop(3, t0); t0 = Prof.Start();
                    bool f3 = !f1 && !f2 && PairDeadlock(t) && (stats.PairDead++ >= 0);
                    Prof.Stop(4, t0); t0 = Prof.Start();
                    bool f4 = !f1 && !f2 && !f3 && cl != null && cl.Dead(occ, t, b, mark, st, b) && (stats.ClusterDead++ >= 0);
                    Prof.Stop(0, t0);
                    if (!(f1 || f2 || f3 || f4))
                    {
                        stats.Generated++;
                        t0 = Prof.Start();
                        int r = Regions();
                        Prof.Stop(1, t0);
                        int pen = 0;
                        if (opt.Penalties && cl != null && noise == null)
                        {
                            pen = cl.Pen;
                            if (pen > 0) stats.Penalized++;
                        }
                        pushes1[np++] = new Push1 { I = i, B = b, T = t, Dir = (sbyte)di, R = r, Pen = pen };
                        if (r < best) { best = r; bestK = np - 1; }
                    }
                    occ[b] = true; occ[t] = false;
                }
            }
            int last = -1;
            if (nodes.Parent(id) >= 0)
            {
                last = nodes.From(id) + dirs[nodes.Dir(id)];
                if (macros.TryGetValue(id, out var seq)) { var l = seq[^1]; last = l.From + dirs[l.Dir]; }
            }
            for (int k = 0; k < np; k++)
            {
                ref var p = ref pushes1[k];
                int wd = 1;
                if (k == bestK) { wd = 0; stats.ConnAdvisor++; }
                else if (p.B == last) wd = 0;
                cands[nc++] = new Cand { Code = EncodeMove(p.I, p.Dir, wd, false, p.R), Wd = wd, Ch = h - Hc(p.B) + Hc(p.T) + p.Pen };
            }
        }
        finally
        {
            // stable insertion sort by (wd, ch), as slices.SortStableFunc
            for (int i = 1; i < nc; i++)
            {
                var x = cands[i]; int j = i - 1;
                while (j >= 0 && (cands[j].Wd > x.Wd || (cands[j].Wd == x.Wd && cands[j].Ch > x.Ch))) { cands[j + 1] = cands[j]; j--; }
                cands[j + 1] = x;
            }
            for (int i = 0; i < nc; i++) mv.Add(cands[i].Code);
            ref var o = ref opened[oi];
            o.Len = (ushort)Math.Min(nc, 0xFFFF);
            o.Left = o.Len;
        }
    }

    (int ch, int w) MoveKeyAt(int id, int k, ReadOnlySpan<int> cur, int h)
    {
        DecodeMove(mv[k] & ~MoveUsed, out int i, out int dir, out int wd, out bool macro, out _);
        if (macro) return (h - Hc(nodeMacros[id][i][0].From), nodes.W(id));
        int b = cur[i];
        return (h - Hc(b) + Hc(b + dirs[dir]), nodes.W(id) + wd);
    }

    int NextMove(int id, int src, ReadOnlySpan<int> cur, int h)
    {
        ref var o = ref opened[nodes.MvIdx(id)];
        int start = o.Start, end = start + o.Len;
        if (src >= 0)
        {
            int k = start + o.Next;
            while ((mv[k] & MoveUsed) != 0) k++;
            o.Next = (ushort)(k - start);
            return k;
        }
        int best = -1, bh = int.MaxValue;
        for (int k = start; k < end; k++)
            if ((mv[k] & MoveUsed) == 0)
            {
                var (ch, _) = MoveKeyAt(id, k, cur, h);
                if (ch < bh) { best = k; bh = ch; }
            }
        return best;
    }

    // ---- apply / add ---------------------------------------------------------------------------------------------------------------
    /// <summary>Applies move m of node id; s.occ holds its boxes. Returns the child id (-1 if already stored) and h.</summary>
    int Apply(int id, uint m, ReadOnlySpan<int> cur, Span<int> child, int h, uint st, ulong parentHash, out int ch)
    {
        DecodeMove(m, out int i, out int dir, out int wd, out bool macro, out int r);
        ulong* pk = nodes.Key(id);
        int nw = nodes.Nw;
        for (int w = 0; w < nw; w++) ckey[w] = pk[w];
        int pp = nodes.Player(id);
        cur.CopyTo(child);
        if (macro)
        {
            var seq = nodeMacros[id][i];
            int b = seq[0].From;
            var last = seq[^1];
            int g = last.From + dirs[last.Dir];
            int ix = ((ReadOnlySpan<int>)cur).IndexOf(b);
            child[ix] = g;
            Flip(b); Flip(g);
            occ[b] = false; occ[g] = true;
            int np = Reach(g - dirs[last.Dir], mark2);
            ch = h - Hc(b);
            ulong zh = parentHash ^ nodes.ZBox(b) ^ nodes.ZBox(g) ^ nodes.ZPlayer(pp) ^ nodes.ZPlayer(np);
            int cid = Add(zh, np, id, b, last.Dir, seq, nodes.G(id) + seq.Length, ch, nodes.W(id), -1, child);
            occ[b] = true; occ[g] = false;
            return cid;
        }
        else
        {
            int b = cur[i];
            int t = b + dirs[dir];
            child[i] = t;
            Flip(b); Flip(t);
            occ[b] = false; occ[t] = true;
            int np = Normalize(b, t, nodes.Player(id), st);
            ch = h - Hc(b) + Hc(t);
            ulong zh = parentHash ^ nodes.ZBox(b) ^ nodes.ZBox(t) ^ nodes.ZPlayer(pp) ^ nodes.ZPlayer(np);
            int cid = Add(zh, np, id, b, (sbyte)dir, null, nodes.G(id) + 1, ch, nodes.W(id) + wd, r, child);
            occ[b] = true; occ[t] = false;
            return cid;
        }
    }

    void Flip(int cell) { int f = nodes.Fidx[cell]; ckey[f >> 6] ^= 1UL << (f & 63); }

    /// <summary>Inserts a new node, or records a shorter path to an existing one. The child's key is in ckey.</summary>
    int Add(ulong zh, int player, int parent, int from, sbyte dir, Push[]? seq, int g, int h, int w, int reg, ReadOnlySpan<int> boxes)
    {
        int id = nodes.Find(ckey, player, zh, out int slot, out uint fp);
        if (id >= 0)
        {
            stats.Duplicates++;
            if (nodes.G(id) <= g) return -1;
            nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g;
            if (seq != null) macros[id] = (Push[])seq.Clone(); else macros.Remove(id);
            return -1;
        }
        if (memFull) return -1;
        if (sd != null && parent >= 0)
            foreach (int c in boxes)
                if (!goal[c] && sd.Dead(c, player)) { stats.SideDead++; return -1; }
        id = nodes.Insert(slot, fp, ckey, player);
        if (seq != null) macros[id] = (Push[])seq.Clone();
        nodes.Parent(id) = parent; nodes.From(id) = (ushort)from; nodes.Dir(id) = dir; nodes.G(id) = g; nodes.W(id) = w;
        if (reg < 0) reg = Regions();
        nodes.Reg(id) = (sbyte)Math.Min(reg, 127);
        int onGoal = PackedCount();
        int bucket = onGoal * MaxRegions + Math.Min(reg, MaxRegions - 1);
        nodes.Bucket(id) = (ushort)bucket;
        PushNode(h, w, id, bucket);
        if (nodes.NeedsGrow)
        {
            if (opt.BudgetBytes > 0 && TrackedBytes() + (ulong)nodes.TableSlots * 16 > opt.BudgetBytes) memFull = true;
            else nodes.Grow();
        }
        return id;
    }
}
