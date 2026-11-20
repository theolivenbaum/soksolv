using Soksolv.Model;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- open(): evaluate all macro moves of a node once (Go fess_moves.go open, Stage 1 configuration) ----------------------------
    int mOpenId, mOpenCands, mParentOop; uint mOpenSt; int[]? mHotCounts;

    void MOpen(int id, int[] cur, int h, uint st)
    {
        ref var a0 = ref maux[id];
        a0.MvStart = mmv.Count;
        mCandN = 0;
        if (!Feasible(cur))
        {
            stats.Assignment++;
            ref var af = ref maux[id]; af.MvLen = 0; af.MvLeft = 0; af.MvNext = 0;
            return;
        }
        int parentPacked = MPacked();
        int parentConn = maux[id].Reg;
        int parentRoom = MRoomConnectivity();
        int parentOop = MOutOfPlan(cur);
        int[]? counts = MacroStage >= 4 ? HotCounts(cur) : null;
        int parentHot = counts != null ? HotspotCount(counts) : 0;
        int parentPhase = mPlan != null ? mPlan.Status(occ).phase : -1;
        uint corral = PiCorral(st);
        if (corral != 0) stats.Corrals++;
        mPlan?.NodeSetup(cur);
        mOpenId = id; mOpenSt = st; mParentOop = parentOop; mHotCounts = counts;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = cur[i];
            if (corral != 0 && cmark[b] != corral) continue;
            mtst++;
            mOpenCands = 0;
            mVisitI = i;
            MBoxSearch(b, st, false, 0, 0, cur);
        }

        int nm = mCandN;
        // advisors: packing, connectivity, room, out-of-plan (each picks its best-scored eligible move)
        int packingK = -1, connK = -1, roomK = -1, oopK = -1, hotK = -1, openerK = -1, explorerK = -1;
        int hottest = -1;
        if (counts != null) for (int i = 0; i < counts.Length; i++) if (counts[i] > 0 && (hottest < 0 || counts[i] > counts[hottest])) hottest = i;
        int hotFrom = hottest >= 0 ? cur[hottest] : 0, openerNear = 1 << 30;
        int nearest = -1;
        if (basinInside != null)
            for (int i = 0; i < cur.Length; i++)
            {
                int b = cur[i];
                if (!basinInside[b] && !(mPlan != null ? mPlan.Placed(b, parentPhase) : goal[b]) && (nearest < 0 || basinDist![b] < basinDist[cur[nearest]])) nearest = i;
            }
        int oopFrom = nearest >= 0 ? cur[nearest] : 0, oopNear = 1 << 30;
        for (int k = 0; k < nm; k++)
        {
            ref var m = ref mCands[k];
            if (m.Packed > parentPacked || mPlan != null && m.Progress > parentPhase) MChoose(ref packingK, k);
            if (m.R < parentConn) MChoose(ref connK, k);
            if (m.Room < parentRoom) MChoose(ref roomK, k);
            if (m.Hot < parentHot) MChoose(ref hotK, k);
            if (m.Explorer) MChoose(ref explorerK, k);
            if (hottest >= 0 && m.I != hottest && m.R <= parentConn && MWalk(hotFrom, m.T) > MWalk(hotFrom, m.B))
            {
                int dd = MWalk(hotFrom, m.B);
                if (dd < openerNear) { openerNear = dd; openerK = -1; }
                if (dd == openerNear) MChoose(ref openerK, k);
            }
            if (nearest >= 0 && m.R <= parentConn && basinInside![m.T] && !basinInside[m.B])
            {
                int d = MWalk(oopFrom, m.B);
                if (d < oopNear) { oopNear = d; oopK = -1; }
                if (d == oopNear) MChoose(ref oopK, k);
            }
        }
        if (mWeights.Length < nm) { mWeights = new int[nm * 2]; mKeys = new MKey[nm * 2]; }
        for (int k = 0; k < nm; k++) mWeights[k] = 1;
        if (packingK >= 0) { mWeights[packingK] = 0; stats.Macros++; }
        if (connK >= 0) { mWeights[connK] = 0; stats.ConnAdvisor++; }
        if (roomK >= 0) mWeights[roomK] = 0;
        if (hotK >= 0) mWeights[hotK] = 0;
        if (explorerK >= 0) mWeights[explorerK] = 0;
        if (openerK >= 0) mWeights[openerK] = 0;
        if (oopK >= 0) mWeights[oopK] = 0;
        for (int k = 0; k < nm; k++) mKeys[k] = new MKey { W = mWeights[k], Oop = mCands[k].Oop, Packed = mCands[k].Packed, R = mCands[k].R, Room = mCands[k].Room, Hot = mCands[k].Hot, Mobility = mCands[k].Mobility, Plen = mCands[k].Plen, Idx = k };
        mKeys.AsSpan(0, nm).Sort(static (x, y) =>
        {
            if (x.W != y.W) return x.W < y.W ? -1 : 1;
            if (x.Oop != y.Oop) return x.Oop < y.Oop ? -1 : 1;
            if (x.Packed != y.Packed) return x.Packed > y.Packed ? -1 : 1; // -packed
            if (x.R != y.R) return x.R < y.R ? -1 : 1;
            if (x.Room != y.Room) return x.Room < y.Room ? -1 : 1;
            if (x.Hot != y.Hot) return x.Hot < y.Hot ? -1 : 1;
            if (x.Mobility != y.Mobility) return x.Mobility > y.Mobility ? -1 : 1;
            if (x.Plen != y.Plen) return x.Plen < y.Plen ? -1 : 1;
            return x.Idx < y.Idx ? -1 : 1;
        });
        for (int j = 0; j < nm; j++)
        {
            int k = mKeys[j].Idx;
            ref var m = ref mCands[k];
            ulong word = (ulong)m.I | (ulong)m.T << 15 | (ulong)mWeights[k] << 30 | (ulong)m.Side << 31 | (ulong)m.R << 33;
            mmv.Add(word);
            mmvScore.Add(new MoveScore { Oop = (ushort)m.Oop, Packed = (ushort)m.Packed, Conn = (ushort)m.R, Hot = (ushort)Math.Min(m.Hot, 65535), Room = (uint)m.Room, Mobility = (uint)m.Mobility });
        }
        ref var a = ref maux[id];
        a.MvLen = (uint)nm; a.MvNext = 0; a.MvTotal = (uint)nm; a.MvLeft = (uint)nm;
    }

    void MChoose(ref int slot, int k)
    {
        if (slot < 0) { slot = k; return; }
        ref var m = ref mCands[k]; ref var o = ref mCands[slot];
        int c = m.Oop != o.Oop ? (m.Oop < o.Oop ? -1 : 1) : m.Packed != o.Packed ? (m.Packed > o.Packed ? -1 : 1) : m.R != o.R ? (m.R < o.R ? -1 : 1) : m.Room != o.Room ? (m.Room < o.Room ? -1 : 1) : m.Hot != o.Hot ? (m.Hot < o.Hot ? -1 : 1) : m.Mobility != o.Mobility ? (m.Mobility > o.Mobility ? -1 : 1) : 0;
        if (c < 0 || c == 0 && m.Plen < o.Plen) slot = k;
    }

    /// <summary>One reachable box destination t of the box being searched (occ: box lifted from b).</summary>
    bool MVisit(int k, int t, ReadOnlySpan<int> cur)
    {
        int i = mVisitI;
        int canonical = 0;
        for (int side = 0; side < 4; side++)
            if (m3[t - dirs[side]] == mpstamp) { canonical = side; break; }
        int endpoint = t * 4 + canonical;
        if (mtseen[endpoint] == mtst) return true;
        mtseen[endpoint] = mtst;
        SetOcc(t, true);
        int dead = MPushDead(cur, i, t, canonical);
        if (dead == 0)
        {
            stats.Generated++;
            int plen = 1;
            for (int x = mbprev[k]; x >= 0; x = mbprev[x]) plen++;
            int r;
            switch (++mOpenCands)
            {
                case 1: r = Regions(); break;
                case 2:
                    SetOcc(t, false); ComputeArticulation(); SetOcc(t, true);
                    r = ChildRegions(t); break;
                default: r = ChildRegions(t); break;
            }
            if (CheckRegions && r != Regions()) throw new InvalidOperationException($"incremental region count {r} != flood {Regions()}");
            if (mCandN == mCands.Length) Array.Resize(ref mCands, mCandN * 2);
            var (mob, expl) = MacroStage >= 5 ? MMoveAccess(cur, i, t) : (0, false);
            int room = MRoomConnectivity(), packed, oop, progress = -2;
            if (mPlan != null)
            {
                var (cphase, cpacked) = mPlan.ChildStatus(cur[i], t);
                packed = cpacked; progress = cphase;
                oop = MOutOfPlan(cur, cphase, i, t);
            }
            else { packed = MPacked(); oop = mParentOop - OopOne(cur[i]) + OopOne(t); }
            mCands[mCandN++] = new MCand { I = i, B = cur[i], T = t, R = r, Packed = packed, Plen = plen, Side = k & 3, Room = room, Oop = oop, Progress = progress, Hot = mHotCounts != null ? HotAfter(cur, mHotCounts, i, t) : 0, Mobility = mob, Explorer = expl };
        }
        SetOcc(t, false);
        return true;
    }

    /// <summary>Pruning rules for cur[i] pushed to t (occ holds the child). 0 = alive.</summary>
    int MPushDead(ReadOnlySpan<int> cur, int i, int t, int canonical)
    {
        if (Frozen2x2(t)) { stats.Frozen2x2++; return 1; }
        if (FreezeDeadlock(t)) { stats.Freeze++; return 2; }
        if (GoalAreaDeadlock(t)) { stats.GoalArea++; return 3; }
        if (PairDeadlock(t)) { stats.PairDead++; return 4; }
        if (sd != null)
        {
            int p = t - dirs[canonical];
            if (m3[p] == mpstamp)
                for (int j = 0; j < cur.Length; j++)
                {
                    int c = j == i ? t : cur[j];
                    if (!goal[c] && sd.Dead(c, p)) { stats.SideDead++; return 5; }
                }
        }
        return 0;
    }

    // ---- apply ----------------------------------------------------------------------------------------------------------------------
    int MApply(int id, ulong m, int[] cur, int[] child, int h, uint st, ulong parentHash, out int ch)
    {
        mLastAdded = -1;
        int i = (int)(m & 0x7FFF), t = (int)(m >> 15 & 0x7FFF), wd = (int)(m >> 30 & 1), r = (int)(m >> 33 & 0x7FFF), side = (int)(m >> 31 & 3);
        int b = cur[i];
        var seq = MBoxPathTo(b, t, st, side, cur);
        ch = h;
        if (seq.Count == 0) return -1;
        var last = seq[^1];
        cur.AsSpan().CopyTo(child);
        child[i] = t;
        Array.Sort(child);
        SetOcc(b, false); SetOcc(t, true);
        int sideC = t - dirs[last.Dir];
        int np = Reach(sideC, mark2);
        if (cl != null && cl.Dead(occ, t, sideC, mark2, stamp, -1))
        {
            stats.ClusterDead++;
            SetOcc(t, false); SetOcc(b, true);
            return -1;
        }
        ch = h - dist[b] + dist[t];
        int w = wd + nodes.W(id);
        ulong* pk = nodes.Key(id);
        for (int x = 0; x < nodes.Nw; x++) ckey[x] = pk[x];
        ulong zh = parentHash ^ nodes.ZPlayer(nodes.Player(id)) ^ nodes.ZPlayer(np);
        if (b != t) { Flip(b); Flip(t); zh ^= nodes.ZBox(b) ^ nodes.ZBox(t); }
        int cid = MAdd(zh, np, id, b, last.Dir, nodes.G(id) + seq.Count, ch, w, r, child);
        SetOcc(t, false); SetOcc(b, true);
        return cid;
    }

    // ---- search loop (Go solve + loop) -----------------------------------------------------------------------------------------------
    SolveResult MacroSearch(double timeLimitSec, System.Diagnostics.Stopwatch sw)
    {
        mNodeLimit = opt.NodeBudget;
        mDeadlineMs = opt.NodeBudget > 0 ? 0 : timeLimitSec * 1000;
        long expanded = 0;
        if (MExpired()) return new SolveResult { Reason = "timeout" };
        PairOffsets();
        var start = (int[])lv.Boxes.Clone();
        Array.Sort(start);
        int h0 = 0;
        foreach (int b in start)
        {
            if (dist[b] < 0) return new SolveResult { Reason = "box on dead square" };
            h0 += dist[b];
            SetOcc(b, true);
        }
        if (h0 == 0) { Array.Clear(occ); return new SolveResult { Solved = true }; }
        int p0 = Reach(lv.Player, mark);
        uint st0 = stamp;
        for (int x = 0; x < nodes.Nw; x++) ckey[x] = 0;
        foreach (int b in start) Flip(b);
        int root = MAdd(nodes.HashOf(start, p0), p0, -1, -1, -1, 0, h0, 0, -1, start);
        if (root >= 0) { MOpen(root, start, h0, st0); PropagateDead(root); MEnqueue(root); }
        mBest = 0;
        foreach (int b in start) SetOcc(b, false);
        var cur = mCur; var child = mChild;
        while (true)
        {
            if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
            if (!MSelect(out var e))
            {
                if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
                return new SolveResult { Reason = "exhausted", Nodes = expanded };
            }
            int id = e.Node;
            nodes.Unpack(id, cur);
            int h = 0;
            foreach (int b in cur) { SetOcc(b, true); h += dist[b]; }
            expanded++; mExpandedN++;
            int pl = nodes.Player(id);
            Reach(pl, mark);
            uint st = stamp;
            ulong mv = mmv[e.Move];
            mmv[e.Move] = mv | MMoveUsed;
            maux[id].MvLeft--;
            ulong parentHash = nodes.HashOf(cur, pl);
            int cid = MApply(id, mv & ~MMoveUsed, cur, child, h, st, parentHash, out int ch);
            if (s1On) S1Step(expanded, id, (int)(e.Move - maux[id].MvStart), cid);
            RecordEdge(id, mLastAdded);
            PropagateDead(id);
            MEnqueue(id);
            foreach (int b in cur) SetOcc(b, false);
            if (cid < 0) continue;
            MNoteBest(cid);
            if (ch == 0)
            {
                string sol = MReconstruct(cid);
                if (sol.Length > 0) return new SolveResult { Solved = true, Solution = sol, Nodes = expanded };
                if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
                return new SolveResult { Solved = true, Solution = sol, Nodes = expanded };
            }
            if (maux[cid].MvStart < 0)
            {
                foreach (int b in child) SetOcc(b, true);
                Reach(nodes.Player(cid), mark);
                MOpen(cid, child, ch, stamp);
                PropagateDead(cid);
                foreach (int b in child) SetOcc(b, false);
            }
            MEnqueue(cid);
        }
    }

    string MReconstruct(int id)
    {
        var chain = new List<int>();
        for (; nodes.Parent(id) >= 0; id = nodes.Parent(id))
        {
            if (chain.Count > nodes.Count) return "";
            chain.Add(id);
        }
        chain.Reverse();
        var pc = new int[n]; var cc = new int[n];
        var pushes = new List<(int from, int dir)>();
        foreach (int c in chain)
        {
            int p = nodes.Parent(c);
            nodes.Unpack(p, pc); nodes.Unpack(c, cc);
            int t = nodes.From(c);
            foreach (int x in cc) if (Array.IndexOf(pc, x) < 0) t = x;
            foreach (int x in pc) SetOcc(x, true);
            Reach(nodes.Player(p), mark);
            var seq = MBoxPathTo(nodes.From(c), t, stamp, nodes.Dir(c), pc);
            foreach (var pu in seq) pushes.Add((pu.From, pu.Dir));
            foreach (int x in pc) SetOcc(x, false);
        }
        foreach (int b in lv.Boxes) SetOcc(b, true);
        var prev = new sbyte[cells];
        var sb = new System.Text.StringBuilder();
        int pp = lv.Player;
        var walk = new List<char>();
        foreach (var (from, di) in pushes)
        {
            int target = from - dirs[di];
            uint st = ++stamp;
            int tail = 0; queue[tail++] = pp; mark[pp] = st;
            for (int i = 0; i < tail && mark[target] != st; i++)
            {
                int c = queue[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + dirs[k];
                    if (mark[nb] != st && !wall[nb] && !occ[nb]) { mark[nb] = st; prev[nb] = (sbyte)k; queue[tail++] = nb; }
                }
            }
            if (mark[target] != st) { for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false); return ""; }
            walk.Clear();
            for (int c = target; c != pp; c -= dirs[prev[c]]) walk.Add(DirChars[prev[c]]);
            for (int i = walk.Count - 1; i >= 0; i--) sb.Append(walk[i]);
            sb.Append((char)(DirChars[di] - 'a' + 'A'));
            SetOcc(from, false); SetOcc(from + dirs[di], true);
            pp = from;
        }
        for (int c = 0; c < cells; c++) if (occ[c]) SetOcc(c, false);
        return sb.ToString();
    }
}
