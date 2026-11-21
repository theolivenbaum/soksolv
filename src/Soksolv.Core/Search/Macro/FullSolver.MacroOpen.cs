using Soksolv.Model;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- open(): evaluate all macro moves of a node once (Go fess_moves.go open, Stage 1 configuration) ----------------------------
    // novelty lane (Go exp-192/194/289): per-FESS-cell novelty memory
    const bool AccessNovelty = true, Novelty2 = true;
    readonly List<ulong[]?> mNovSeen = new(), mNovPushSeen = new();
    readonly HashSet<ulong> mNovPairs = new(), mNovAccess = new();
    readonly List<ulong> mNovWords = new(), mNovT0 = new(), mNovT1 = new();
    long mParentSpread;
    int mOpenId, mOpenCands, mParentOop, mParentRoom, mParentConn, mParentPackedF; uint mOpenSt; int[]? mHotCounts;
    DisplacementLandmarks? dl; bool dlInit; ulong mCmdMask, mUnpinMask;

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
        mReopening = maux[id].MvOff > 0;
        mGid = -1; mPB0 = mPT0 = mPPlayer = -1; mRevP = nodes.Parent(id);
        if (Feat(6))
        {
            if (mRevKey == null) mRevKey = (ulong*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(Math.Max(1, nodes.Nw) * 8));
            ulong* pk0 = nodes.Key(id);
            for (int x = 0; x < nodes.Nw; x++) mRevKey[x] = pk0[x];
            mRevParentHash = nodes.HashOf(cur, nodes.Player(id));
            mGid = MFindRevGroup(id);
            int p0 = mRevP;
            if (p0 >= 0 && maux[p0].Bucket == maux[id].Bucket) { mPB0 = nodes.From(id); mPT0 = maux[id].PushTo; mPPlayer = nodes.Player(p0); }
        }
        int parentPacked = MPacked();
        int parentConn = maux[id].Reg;
        mCmdMask = 0; mUnpinMask = 0;
        if (Feat(2) && !pull && goals.Length <= 64) // Go: DisplacementLandmarksOn && !s.pull && len(goals) <= 64
        {
            if (!dlInit) { dlInit = true; dl = DisplacementLandmarks.Create(lv, dirs, goals); }
            if (dl != null)
            {
                mCmdMask = dl.ComputeSinglePinCMD(cur, nodes.Player(id));
                if (mCmdMask == 0) mUnpinMask = dl.ComputeCollectiveUnpacking(cur, nodes.Player(id)).candidateUnpins;
            }
        }
        mParentConn = parentConn; mParentPackedF = parentPacked;
        int parentRoom = MRoomConnectivity();
        int parentOop = MOutOfPlan(cur);
        int[]? counts = MacroStage >= 4 ? HotCounts(cur) : null;
        int parentHot = counts != null ? HotspotCount(counts) : 0;
        int parentPhase = mPlan != null ? mPlan.Status(occ).phase : -1;
        long tc = Prof.Start();
        uint corral = 0;
        if (!pull) // PI-corral pruning is a push argument
        {
            if (Feat(1)) corral = FirstPushCorral(nodes.Player(id), st); // FrontierCorral (corral_frontier.go)
            if (corral == 0) corral = PiCorral(st);
            if (corral != 0) stats.Corrals++;
        }
        packPrepared = false;
        if (Feat(4)) PreparePackingObligations();
        mPlan?.NodeSetup(cur);
        Prof.Stop(5, tc);
        mAccN = 0;
        if (pull) { long sp = 0; foreach (int bx in cur) sp += spreadDist[bx]; mParentSpread = sp; }
        { int bk0 = (int)maux[id].Bucket; mUnpackedRegime = bk0 < mcells.Count && parentPacked < mcells[bk0].Packed; }
        mOpenId = id; mOpenSt = st; mParentOop = parentOop; mParentRoom = parentRoom; mHotCounts = counts;
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
        int packingK = -1, connK = -1, roomK = -1, oopK = -1, hotK = -1, openerK = -1, explorerK = -1, dispK = -1;
        ulong displaceMask = mCmdMask | mUnpinMask;
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
            if (displaceMask != 0 && dl != null && m.T != m.B && m.R <= parentConn)
            {
                int gIdxB = dl.GoalIdx[m.B];
                if (gIdxB >= 0 && (displaceMask & (1UL << gIdxB)) != 0)
                {
                    int gIdxT = dl.GoalIdx[m.T];
                    if (gIdxT < 0 || (displaceMask & (1UL << gIdxT)) == 0) MChoose(ref dispK, k);
                }
            }
        }
        if (packingK < 0 && packPrepared)
        {
            int pk = -1;
            bool clearing = PackingObligationAdvice(k => MChoose(ref pk, k));
            if (pk >= 0) { packingK = pk; stats.PackingPrepPicks++; if (clearing) stats.PackingPrepClears++; }
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
        if (dispK >= 0) { mWeights[dispK] = 0; stats.DispAdvisor++; }
        int noveltyN = NoveltyBudget();
        for (int k = 0; k < nm; k++) mCands[k].Tier = noveltyN > 0 ? 3 : 0;
        if (noveltyN > 0)
        {
            // Go: for advisor, k := range chosen (advisor index order; a later advisor overrides an earlier tier on the same move)
            ReadOnlySpan<int> chosenK = [packingK, connK, roomK, hotK, explorerK, openerK, oopK, dispK];
            for (int adv = 0; adv < 8; adv++)
                if (chosenK[adv] >= 0) mCands[chosenK[adv]].Tier = adv is 0 or 1 or 2 or 7 ? 0 : 1;
        }
        if (Feat(6))
        {
            // advisor picks that are distinct group exits are remembered (Go: revExits.insert for every chosen advisor)
            foreach (int ck in (ReadOnlySpan<int>)[packingK, connK, roomK, hotK, explorerK, openerK, oopK, dispK])
                if (ck >= 0 && mCands[ck].DistinctExit) mRevExits.Add(mCands[ck].ExitKey);
            int bestExit = -1;
            if (!mReopening)
            {
                if (mGid >= 0 && mGid < maux.Count && maux[mGid].RevSize >= 2)
                {
                    bool hasDistinctAdvisorExit = false;
                    foreach (int ck in (ReadOnlySpan<int>)[packingK, connK, roomK, hotK, explorerK, openerK, oopK, dispK])
                        if (ck >= 0 && mCands[ck].DistinctExit) { hasDistinctAdvisorExit = true; break; }
                    if (!hasDistinctAdvisorExit)
                    {
                        for (int k = 0; k < nm; k++)
                        {
                            ref var m = ref mCands[k];
                            if (!m.DistinctExit || m.Undo || m.Internal || m.DeadChild) continue;
                            bool progress = m.Packed > parentPacked || m.R < parentConn || m.Room < parentRoom || m.Hot < parentHot || m.Explorer;
                            if (!progress && displaceMask != 0 && dl != null && m.R <= parentConn && m.T != m.B)
                            {
                                int gIdxB = dl.GoalIdx[m.B];
                                if (gIdxB >= 0 && (displaceMask & (1UL << gIdxB)) != 0)
                                {
                                    int gIdxT = dl.GoalIdx[m.T];
                                    if (gIdxT < 0 || (displaceMask & (1UL << gIdxT)) == 0) progress = true;
                                }
                            }
                            if (progress)
                            {
                                if (bestExit < 0) { bestExit = k; continue; }
                                int c = MScoreCmp(ref m, ref mCands[bestExit]);
                                if (c < 0 || c == 0 && m.Plen < mCands[bestExit].Plen) bestExit = k;
                            }
                        }
                        if (bestExit >= 0)
                        {
                            mWeights[bestExit] = 0;
                            if (noveltyN > 0) { ref var be0 = ref mCands[bestExit]; be0.Tier = be0.Packed > parentPacked || be0.R < parentConn || be0.Room < parentRoom ? 0 : 1; }
                            mRevExits.Add(mCands[bestExit].ExitKey);
                        }
                    }
                }
                maux[id].RevBestExit = (short)bestExit;
                int revOff = mRevWords.Count;
                int nwd = (nm + 63) / 64;
                for (int x = 0; x < nwd; x++) mRevWords.Add(0);
                maux[id].RevOffset = revOff + 1;
                for (int k = 0; k < nm; k++)
                    if (mCands[k].RevBad) mRevWords[revOff + k / 64] |= 1UL << (k % 64);
            }
            else
            {
                if (maux[id].RevOffset > 0)
                {
                    int revOff = maux[id].RevOffset - 1;
                    for (int k = 0; k < nm; k++)
                        if ((mRevWords[revOff + k / 64] & (1UL << (k % 64))) != 0) mCands[k].Undo = true;
                }
                if (maux[id].RevBestExit >= 0)
                {
                    int be = maux[id].RevBestExit;
                    if (be < nm)
                    {
                        mWeights[be] = 0;
                        if (noveltyN > 0) { ref var bm = ref mCands[be]; bm.Tier = bm.Packed > parentPacked || bm.R < parentConn || bm.Room < parentRoom ? 0 : 1; }
                    }
                }
            }
        }
        if (noveltyN > 0) MNoveltyBlock(id, cur, nm, noveltyN, parentConn);
        bool useTier = noveltyN > 0 && AccessNovelty;
        for (int k = 0; k < nm; k++) mKeys[k] = new MKey { Tier = useTier ? mCands[k].Tier : 0, Bad = Feat(6) && mCands[k].RevBad ? 1 : 0, W = mWeights[k], Oop = mCands[k].Oop, Packed = mCands[k].Packed, R = mCands[k].R, Room = mCands[k].Room, Hot = mCands[k].Hot, Mobility = mCands[k].Mobility, Plen = mCands[k].Plen, Idx = k };
        mKeys.AsSpan(0, nm).Sort(static (x, y) =>
        {
            if (x.Bad != y.Bad) return x.Bad < y.Bad ? -1 : 1;
            if (x.W != y.W) return x.W < y.W ? -1 : 1;
            if (x.Tier != y.Tier) return x.Tier < y.Tier ? -1 : 1;
            if (x.Oop != y.Oop) return x.Oop < y.Oop ? -1 : 1;
            if (x.Packed != y.Packed) return x.Packed > y.Packed ? -1 : 1; // -packed
            if (x.R != y.R) return x.R < y.R ? -1 : 1;
            if (x.Room != y.Room) return x.Room < y.Room ? -1 : 1;
            if (x.Hot != y.Hot) return x.Hot < y.Hot ? -1 : 1;
            if (x.Mobility != y.Mobility) return x.Mobility > y.Mobility ? -1 : 1;
            if (x.Plen != y.Plen) return x.Plen < y.Plen ? -1 : 1;
            return x.Idx < y.Idx ? -1 : 1;
        });
        int off = (int)Math.Min(maux[id].MvOff, (uint)nm);
        int win = nm - off;
        if (MoveWindow > 0 && win > MoveWindow) win = MoveWindow;
        for (int j = off; j < off + win; j++)
        {
            int k = mKeys[j].Idx;
            ref var m = ref mCands[k];
            ulong word = (ulong)m.I | (ulong)m.T << 15 | (ulong)mWeights[k] << 30 | (ulong)m.Side << 31 | (ulong)m.R << 33;
            mmv.Add(word);
            mmvScore.Add(new MoveScore { Oop = (ushort)m.Oop, Packed = (ushort)m.Packed, Conn = (ushort)m.R, Hot = (ushort)Math.Min(m.Hot, 65535), Room = (uint)m.Room, Mobility = (uint)m.Mobility });
        }
        ref var a = ref maux[id];
        a.MvLen = (uint)win; a.MvNext = 0; a.MvTotal = (uint)nm; a.MvLeft = (uint)(nm - off);
    }

    /// <summary>Go open(): novelty advisor with stable windows (StableWindows, exp-192/194/289): novel moves cost nothing; tiers order them.</summary>
    void MNoveltyBlock(int id, int[] cur, int nm, int novelty, int parentConn)
    {
        if (!mReopening)
        {
            int bk = (int)maux[id].Bucket;
            while (mNovSeen.Count <= bk) mNovSeen.Add(null);
            var seen = mNovSeen[bk] ??= new ulong[(cells + 63) / 64];
            ulong[]? pushSeen = null;
            if (AccessNovelty)
            {
                while (mNovPushSeen.Count <= bk) mNovPushSeen.Add(null);
                pushSeen = mNovPushSeen[bk] ??= new ulong[(4 * cells + 63) / 64];
            }
            int nov = 0, nwd = (nm + 63) / 64, novOff = mNovWords.Count;
            for (int x = 0; x < nwd; x++) { mNovWords.Add(0); if (AccessNovelty) { mNovT0.Add(0); mNovT1.Add(0); } }
            maux[id].NovOffset = novOff + 1;
            for (int k = 0; k < nm; k++)
            {
                ref var m = ref mCands[k];
                int w = m.T / 64; ulong bit = 1UL << (m.T % 64);
                bool w1Novel = (seen[w] & bit) == 0, w2Novel = false;
                if (Novelty2)
                    for (int j = 0; j < cur.Length; j++)
                    {
                        if (j == m.I) continue;
                        int a = m.T, c = cur[j];
                        if (a > c) (a, c) = (c, a);
                        if (!mNovPairs.Contains((ulong)bk << 40 | (ulong)a << 20 | (ulong)c)) { w2Novel = true; break; }
                    }
                bool accessNovel = false;
                if (AccessNovelty && m.AccLen > 0)
                    for (int e = m.AccStart; e < m.AccStart + m.AccLen; e++)
                    {
                        ulong raw = mAccBuf[e];
                        ulong tag = raw >> 40 & 0xF;
                        if (tag == 0) { if ((pushSeen![raw / 64] & 1UL << (int)(raw % 64)) == 0) { accessNovel = true; break; } }
                        else if (!mNovAccess.Contains((ulong)bk << 44 | raw)) { accessNovel = true; break; }
                    }
                bool novel = w1Novel || w2Novel || accessNovel;
                if (Feat(6) && m.RevBad) novel = false;
                if (novel && nov < novelty)
                {
                    seen[w] |= bit;
                    if (Novelty2)
                        for (int j = 0; j < cur.Length; j++)
                        {
                            if (j == m.I) continue;
                            int a = m.T, c = cur[j];
                            if (a > c) (a, c) = (c, a);
                            mNovPairs.Add((ulong)bk << 40 | (ulong)a << 20 | (ulong)c);
                        }
                    if (AccessNovelty && m.AccLen > 0)
                        for (int e = m.AccStart; e < m.AccStart + m.AccLen; e++)
                        {
                            ulong raw = mAccBuf[e];
                            if ((raw >> 40 & 0xF) == 0) pushSeen![raw / 64] |= 1UL << (int)(raw % 64);
                            else mNovAccess.Add((ulong)bk << 44 | raw);
                        }
                    mWeights[k] = 0;
                    nov++;
                    mNovWords[novOff + k / 64] |= 1UL << (k % 64);
                    bool connMaintained = m.R <= parentConn;
                    int tier = 2;
                    if (m.Tier == 0) tier = 0;
                    else if (AccessNovelty && accessNovel && connMaintained) tier = 0;
                    else if (w1Novel && connMaintained) tier = 1;
                    else if (m.Tier == 1) tier = 1;
                    if (Feat(6) && m.GroupDupExit) tier = 2;
                    m.Tier = tier;
                    if (AccessNovelty)
                    {
                        if (tier == 0) mNovT0[novOff + k / 64] |= 1UL << (k % 64);
                        else if (tier == 1) mNovT1[novOff + k / 64] |= 1UL << (k % 64);
                    }
                }
                else if (m.Tier > 2) m.Tier = 3;
            }
        }
        else if (maux[id].NovOffset > 0)
        {
            int novOff = maux[id].NovOffset - 1;
            for (int k = 0; k < nm; k++)
            {
                ref var m = ref mCands[k];
                if ((mNovWords[novOff + k / 64] & 1UL << (k % 64)) != 0)
                {
                    mWeights[k] = 0;
                    if (AccessNovelty)
                    {
                        if (mNovT0.Count > 0 && (mNovT0[novOff + k / 64] & 1UL << (k % 64)) != 0) m.Tier = 0;
                        else if (mNovT1.Count > 0 && (mNovT1[novOff + k / 64] & 1UL << (k % 64)) != 0) m.Tier = 1;
                        else m.Tier = 2;
                    }
                    else m.Tier = 2;
                }
                else if (m.Tier > 2) m.Tier = 3;
            }
        }
    }

    static int MScoreCmp(ref MCand m, ref MCand o) =>
        m.Oop != o.Oop ? (m.Oop < o.Oop ? -1 : 1) : m.Packed != o.Packed ? (m.Packed > o.Packed ? -1 : 1) : m.R != o.R ? (m.R < o.R ? -1 : 1) : m.Room != o.Room ? (m.Room < o.Room ? -1 : 1) : m.Hot != o.Hot ? (m.Hot < o.Hot ? -1 : 1) : m.Mobility != o.Mobility ? (m.Mobility > o.Mobility ? -1 : 1) : 0;

    void MChoose(ref int slot, int k)
    {
        if (Feat(6) && mCands[k].RevBad) return;
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
        long tp;
        for (int side = 0; side < 4; side++)
            if (m3[t - dirs[side]] == mpstamp) { canonical = side; break; }
        int endpoint = t * 4 + canonical;
        if (mtseen[endpoint] == mtst) return true;
        mtseen[endpoint] = mtst;
        SetOcc(t, true);
        tp = Prof.Start();
        int dead = MPushDead(cur, i, t, canonical);
        Prof.Stop(0, tp);
        if (dead == 0)
        {
            stats.Generated++;
            int plen = 1;
            for (int x = mbprev[k]; x >= 0; x = mbprev[x]) plen++;
            tp = Prof.Start();
            int r;
            switch (++mOpenCands)
            {
                case 1: r = Regions(); break;
                case 2:
                    SetOcc(t, false); ComputeArticulation(); SetOcc(t, true);
                    r = ChildRegions(t); break;
                default: r = ChildRegions(t); break;
            }
            Prof.Stop(4, tp);
            if (CheckRegions && r != Regions()) throw new InvalidOperationException($"incremental region count {r} != flood {Regions()}");
            if (mCandN == mCands.Length) Array.Resize(ref mCands, mCandN * 2);
            tp = Prof.Start();
            var (mob, expl, accS, accL) = MacroStage >= 5 ? MMoveAccess(cur, i, t) : (0, false, 0, 0);
            Prof.Stop(3, tp);
            tp = Prof.Start();
            int room = t == cur[i] ? mParentRoom : MRoomConnectivity(), packed, oop, progress = -2;
            Prof.Stop(1, tp);
            if (mPlan != null)
            {
                var (cphase, cpacked) = mPlan.ChildStatus(cur[i], t);
                packed = mPlan.Fest != null && mPlan.FestPacked ? mPlan.Fest.Packed(occ) : cpacked; progress = cphase;
                oop = MOutOfPlan(cur, cphase, i, t);
            }
            else { packed = pull ? (int)((mParentSpread - spreadDist[cur[i]] + spreadDist[t]) / SpreadBucket) : MPacked(); oop = mParentOop - OopOne(cur[i]) + OopOne(t); }
            if (mCmdMask != 0 && dl != null && r <= mParentConn)
            {
                int gIdxB = dl.GoalIdx[cur[i]];
                if (gIdxB >= 0 && (mCmdMask & (1UL << gIdxB)) != 0)
                {
                    int gIdxT = dl.GoalIdx[t];
                    if (gIdxT < 0 || (mCmdMask & (1UL << gIdxT)) == 0)
                    {
                        packed = Math.Max(packed, mParentPackedF);
                        oop = Math.Min(oop, mParentOop);
                    }
                }
            }
            bool cUndo = false, cInternal = false, cDeadChild = false, cGroupDup = false, cDistinct = false; ulong cExit = 0;
            if (Feat(6) && !mReopening)
            {
                int b = cur[i], p = mRevP;
                if (mPB0 >= 0 && b == mPT0 && t == mPB0 && mCandNP == mPPlayer) { MUnionRevGroup(p, mOpenId); mGid = MFindRevGroup(mOpenId); cUndo = true; }
                int fb = nodes.Fidx[b], ft = nodes.Fidx[t];
                ulong zh = mRevParentHash ^ nodes.ZPlayer(nodes.Player(mOpenId)) ^ nodes.ZPlayer(mCandNP);
                if (b != t)
                {
                    mRevKey[fb >> 6] &= ~(1UL << (fb & 63)); mRevKey[ft >> 6] |= 1UL << (ft & 63);
                    zh ^= nodes.ZBox(b) ^ nodes.ZBox(t);
                }
                int existing = nodes.Find(mRevKey, mCandNP, zh, out _, out uint fpx);
                if (b != t) { mRevKey[ft >> 6] &= ~(1UL << (ft & 63)); mRevKey[fb >> 6] |= 1UL << (fb & 63); }
                if (existing >= 0)
                {
                    if (maux[existing].Dead != 0) cDeadChild = true;
                    else if (maux[existing].Bucket == maux[mOpenId].Bucket && nodes.W(mOpenId) >= nodes.W(existing))
                    {
                        if (existing == p || MFindRevGroup(existing) == mGid) cInternal = true;
                        else
                        {
                            bool isAnc = false;
                            for (int anc = p, depth = 0; anc >= 0 && depth < 8; anc = nodes.Parent(anc), depth++)
                            {
                                if (maux[anc].Bucket != maux[mOpenId].Bucket) break;
                                if (anc == existing) { isAnc = true; break; }
                            }
                            if (isAnc) { MUnionRevGroup(existing, mOpenId); mGid = MFindRevGroup(mOpenId); cInternal = true; }
                        }
                    }
                }
                else if (mGid >= 0 && mGid < maux.Count && maux[mGid].RevSize >= 2)
                {
                    cExit = ((ulong)mGid << 40) | fpx;
                    if (mRevExits.Contains(cExit)) cGroupDup = true; else cDistinct = true;
                }
            }
            mCands[mCandN++] = new MCand { Undo = cUndo, Internal = cInternal, DeadChild = cDeadChild, GroupDupExit = cGroupDup, DistinctExit = cDistinct, ExitKey = cExit, I = i, B = cur[i], T = t, R = r, Packed = packed, Plen = plen, Side = k & 3, Room = room, Oop = oop, Progress = progress, Hot = mHotCounts != null ? HotAfterP(cur, i, t) : 0, Mobility = mob, Explorer = expl, AccStart = accS, AccLen = accL };
        }
        SetOcc(t, false);
        return true;
    }

    /// <summary>Pruning rules for cur[i] pushed to t (occ holds the child). 0 = alive.</summary>
    int MPushDead(ReadOnlySpan<int> cur, int i, int t, int canonical)
    {
        if (pull) // the dead-square table (pull distance to a start square) already filtered t; the push deadlock family does not apply
        {
            if (PullFreezeDead(t)) { stats.Freeze++; return 2; }
            return 0;
        }
        long tq = Prof.Start();
        bool f22 = Frozen2x2(t);
        bool fz = !f22 && FreezeDeadlock(t);
        Prof.Stop(6, tq);
        if (f22) { stats.Frozen2x2++; return 1; }
        if (fz) { stats.Freeze++; return 2; }
        tq = Prof.Start();
        bool ga = GoalAreaDeadlock(t);
        Prof.Stop(7, tq);
        if (ga) { stats.GoalArea++; return 3; }
        tq = Prof.Start();
        bool pd = PairDeadlock(t);
        Prof.Stop(10, tq);
        if (pd) { stats.PairDead++; return 4; }
        if (sd != null)
        {
            tq = Prof.Start();
            int p = t - dirs[canonical];
            bool dead = false;
            if (m3[p] == mpstamp)
                for (int j = 0; j < cur.Length; j++)
                {
                    int c = j == i ? t : cur[j];
                    if (!goal[c] && sd.Dead(c, p)) { dead = true; break; }
                }
            Prof.Stop(14, tq);
            if (dead) { stats.SideDead++; return 5; }
        }
        if (Feat(7) && m3[t - dirs[canonical]] == mpstamp && PairSideDeadlock(t, t - dirs[canonical])) { stats.PairDead++; return 7; }
        if (Feat(3) && CorridorFlowDeadlock(t)) { stats.CorridorFlow++; return 6; }
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
        int sideC = PlayerAfter(t, dirs[last.Dir]);
        int np = Reach(sideC, mark2);
        long tcl = Prof.Start();
        bool clDead = cl != null && cl.Dead(occ, t, sideC, mark2, stamp, -1);
        Prof.Stop(12, tcl);
        if (clDead)
        {
            stats.ClusterDead++;
            SetOcc(t, false); SetOcc(b, true);
            return -1;
        }
        long tdz = Prof.Start();
        bool dzDead = dz != null && dz.DeadAfter(occ, child, sideC, t);
        Prof.Stop(11, tdz);
        if (dzDead)
        {
            stats.CorralDead++;
            SetOcc(t, false); SetOcc(b, true);
            return -1;
        }
        ch = h - dist[b] + dist[t];
        int w = wd + nodes.W(id);
        ulong* pk = nodes.Key(id);
        for (int x = 0; x < nodes.Nw; x++) ckey[x] = pk[x];
        ulong zh = parentHash ^ nodes.ZPlayer(nodes.Player(id)) ^ nodes.ZPlayer(np);
        if (b != t) { Flip(b); Flip(t); zh ^= nodes.ZBox(b) ^ nodes.ZBox(t); }
        int cid = MAdd(zh, np, id, b, t, last.Dir, nodes.G(id) + seq.Count, ch, w, r, child);
        SetOcc(t, false); SetOcc(b, true);
        return cid;
    }

    // ---- search loop (Go solve + loop) -----------------------------------------------------------------------------------------------
    SolveResult MacroSearch(double timeLimitSec, System.Diagnostics.Stopwatch sw)
    {
        mNodeLimit = nodeLimitOverride > 0 ? nodeLimitOverride : opt.NodeBudget;
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
        int[] rootCells = pull ? pullRoots : [lv.Player]; // pull: one root per player zone of the solved position
        foreach (int rp in rootCells)
        {
            int p0 = Reach(rp, mark);
            uint st0 = stamp;
            for (int x = 0; x < nodes.Nw; x++) ckey[x] = 0;
            foreach (int b in start) Flip(b);
            int root = MAdd(nodes.HashOf(start, p0), p0, -1, -1, -1, -1, 0, h0, 0, -1, start);
            if (root >= 0) { MOpen(root, start, h0, st0); PropagateDead(root); MEnqueue(root); }
        }
        mBest = 0;
        mSearchStartMs = mLastImproveMs = clock.ElapsedMilliseconds;
        foreach (int b in start) SetOcc(b, false);
        var cur = mCur; var child = mChild;
        while (true)
        {
            if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
            if (opt.StallFinish && opt.NodeBudget == 0 && mRows != null && mStallTries < 3 && mBest > 0)
            {
                long now = clock.ElapsedMilliseconds;
                double stalled = now - mLastImproveMs;
                if (stalled > Math.Max(1000.0 * opt.StallScale, (now - mSearchStartMs) * 15 / 100))
                {
                    mStallTries++;
                    mLastImproveMs = clock.ElapsedMilliseconds;
                    var sol = MFinishFromBest();
                    if (sol != null) return new SolveResult { Solved = true, Solution = sol, Nodes = expanded };
                }
            }
            if (!MSelect(out var e))
            {
                if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
                return new SolveResult { Reason = "exhausted", Nodes = expanded };
            }
            if (p4pend != null && StepPlan4()) return new SolveResult { Reason = "replan", Nodes = expanded };
            int id = e.Node;
            nodes.Unpack(id, cur);
            int h = 0;
            foreach (int b in cur) { SetOcc(b, true); h += dist[b]; }
            expanded++; mExpandedN++;
            if (bat != null && (mExpandedN & 1023) == 0 && !bat.Yield(batMe)) return new SolveResult { Reason = "stopped", Nodes = expanded };
            if ((expanded & 255) == 0 && opt.BudgetBytes > 0 && (ulong)(nodes.Bytes + mmv.Bytes + mmvScore.Bytes + maux.Bytes) > opt.BudgetBytes / (ulong)(bat?.Live() ?? 1))
                return new SolveResult { Reason = "memory", Nodes = expanded };
            int pl = nodes.Player(id);
            Reach(pl, mark);
            uint st = stamp;
            ulong mv = mmv[e.Move];
            mmv[e.Move] = mv | MMoveUsed;
            maux[id].MvLeft--;
            ulong parentHash = nodes.HashOf(cur, pl);
            long tap = Prof.Start();
            int cid = MApply(id, mv & ~MMoveUsed, cur, child, h, st, parentHash, out int ch);
            Prof.Stop(9, tap);
            if (s1On) S1Step(expanded, id, (int)(e.Move - maux[id].MvStart), cid);
            RecordEdge(id, mLastAdded);
            PropagateDead(id);
            mReopen = -1;
            MEnqueue(id);
            while (mReopen == id && maux[id].Dead == 0 && !MExpired())
            {
                maux[id].MvOff += maux[id].MvLen;
                Reach(pl, mark);
                long tmo = Prof.Start(); MOpen(id, cur, h, stamp); Prof.Stop(8, tmo);
                mReopen = -1;
                MEnqueue(id);
            }
            foreach (int b in cur) SetOcc(b, false);
            if (cid < 0) continue;
            MNoteBest(cid);
            if (ch == 0 && pull)
            {
                // all boxes on start squares: solved if the real player is in this state's region too
                foreach (int b in child) SetOcc(b, true);
                Reach(nodes.Player(cid), mark);
                bool home = mark[realPlayer] == stamp;
                foreach (int b in child) SetOcc(b, false);
                if (home)
                {
                    string psol = PullSolution(cid);
                    if (psol.Length > 0) return new SolveResult { Solved = true, Solution = psol, Nodes = expanded };
                }
                ch = -1; // not the start position; keep searching
            }
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
                long tmo2 = Prof.Start(); MOpen(cid, child, ch, stamp); Prof.Stop(8, tmo2);
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

    int HotAfterP(ReadOnlySpan<int> cur, int i, int t) { long tp = Prof.Start(); int h = HotAfter(cur, mHotCounts!, i, t); Prof.Stop(2, tp); return h; }
}
