using Soksolv.Model;
using Soksolv.Search.Hub;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- open(): evaluate all macro moves of a node once (Go fess_moves.go open, Stage 1 configuration) ----------------------------
    // novelty lane (Go exp-192/194/289): per-FESS-cell novelty memory
    const bool AccessNovelty = true, Novelty2 = true;
    readonly List<ulong[]?> mNovSeen = new(), mNovPushSeen = new();
    readonly FlatSet64 mNovAccess = new();
    PairBook? mNovBook; nint[] mNovRows = new nint[64];
    readonly List<ulong> mNovWords = new(), mNovT0 = new(), mNovT1 = new();
    long mParentSpread; bool mParentFrozen;
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
        int parentPacked = MPackedBoxes(cur);
        int parentConn = maux[id].Reg;
        mCmdMask = 0; mUnpinMask = 0;
        long tdl = Prof.Start();
        if (Feat(2) && !pull && !hfRank && goals.Length <= 64) // Go: DisplacementLandmarksOn && !s.pull && len(goals) <= 64
        {
            if (!dlInit) { dlInit = true; dl = DisplacementLandmarks.Create(lv, dirs, goals); }
            if (dl != null)
            {
                mCmdMask = dl.ComputeSinglePinCMD(cur, nodes.Player(id));
                if (mCmdMask == 0) mUnpinMask = dl.ComputeCollectiveUnpacking(cur, nodes.Player(id)).candidateUnpins;
            }
        }
        Prof.Stop(24, tdl);
        mParentConn = parentConn; mParentPackedF = parentPacked;
        long trc = Prof.Start();
        if (CandMemoOn || pull) { ulong bh = 0; foreach (int bx in cur) bh ^= nodes.ZBox(bx); mBoxHash = bh; }
        int parentRoom = pfOn ? 0 : MRoomConnParent();
        int parentOop = pfOn ? 0 : MOutOfPlan(cur);
        Prof.Stop(27, trc);
        long thc = Prof.Start();
        int[]? counts = MacroStage >= 4 && !pfOn ? HotCounts(cur) : null;
        Prof.Stop(26, thc);
        int parentHot = counts != null ? HotspotCount(counts) : 0;
        int parentMob = 0;
        if (pfOn)
        {
            PfOnInit();
            int pd = MatchParent(cur); // also primes the one-row updates
            if (pd >= AssignmentInf) pd = -1;
            var pt = new PfFest.Tuple(maux[id].Reg, 0, 0, pd, 0);
            PfFill(ref pt, cur);
            parentOop = pt.Reg; parentPacked = 0; parentRoom = pt.Rooms; parentHot = pt.Hot; parentMob = pt.Mobility;
            parentConn = pt.Reg; mParentConn = parentConn; mParentPackedF = 0;
        }
        if (hfRank)
        {
            // Festival's forward HF order: connectivity, boxes on target, hotspots against the target, min-cost matching distance
            parentOop = maux[id].Reg; parentRoom = HotRel(cur); parentHot = MatchParent(cur);
            mParentFrozen = false;
            if (opt.HfFrozenMatch && HfFrozenOff(cur)) { parentHot = Math.Max(parentHot, 1 << 20); mParentFrozen = true; }
            if (opt.HfMatchFirst && parentHot >= 1 << 20) parentOop += HfNoMatch;
        }
        int parentPhase = mPlan != null ? mPlan.Status(occ).phase : -1;
        long tc = Prof.Start();
        uint corral = 0;
        if (!pull) // PI-corral pruning is a push argument
        {
            if (hfRank && opt.HfFestCorral) { corral = FestCorral(nodes.Player(id), st); if (corral != 0) stats.Corrals++; }
            else {
            if (Feat(1)) corral = FirstPushCorral(nodes.Player(id), st); // FrontierCorral (corral_frontier.go)
            if (corral == 0) corral = PiCorral(st);
            if (corral != 0) stats.Corrals++;
            }
        }
        packPrepared = false;
        if (Feat(4)) PreparePackingObligations();
        mPlan?.NodeSetup(cur);
        if (mPlan?.Fest != null) mPlan.FestMiss = mPlan.Fest.PermMissing(occ);
        Prof.Stop(5, tc);
        mAccN = 0;
        if (pull) { long sp = 0; foreach (int bx in cur) sp += spreadDist[bx]; mParentSpread = sp; }
        { int bk0 = (int)maux[id].Bucket; mUnpackedRegime = bk0 < mcells.Count && parentPacked < mcells[bk0].Packed; }
        long tbs = Prof.Start();
        if (BbEnsure()) { bbNode = true; bbNodeBuilt = false; bbParentBuilt = false; }
        mOpenId = id; mOpenSt = st; mParentOop = parentOop; mParentRoom = parentRoom; mHotCounts = counts;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = cur[i];
            if (corral != 0 && cmark[b] != corral) continue;
            mtst++;
            mOpenCands = 0; mCompsF = -1; mArtDone = false; mSplitSeen = 0;
            mVisitI = i;
            MBoxSearch(b, st, false, 0, 0, cur);
        }
        Prof.Stop(25, tbs);
        bbNode = false;

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
        if (pfOn)
        {
            // Festival's backward search: one advisor, the best child better than the parent (ties: board order of box, then destination)
            int bestA = -1;
            for (int k = 0; k < nm; k++)
            {
                ref var m = ref mCands[k];
                if (Feat(6) && m.RevBad) continue;
                if (m.Stored) continue; // exp-609: Festival's advisor skips children already expanded
                if (PfCmp(ref m, parentOop, parentConn, parentRoom, parentHot, parentMob) >= 0) continue;
                if (bestA < 0) { bestA = k; continue; }
                int c = MScoreCmp(ref m, ref mCands[bestA]);
                if (c < 0 || c == 0 && (m.B < mCands[bestA].B || m.B == mCands[bestA].B && m.T < mCands[bestA].T)) bestA = k;
            }
            packingK = bestA;
        }
        else if (hfRank && opt.HfOneAdvisor)
        {
            // Festival's HF search has one advisor: the best child strictly better than the parent in its HF order
            int bestA = -1;
            for (int k = 0; k < nm; k++)
            {
                ref var m = ref mCands[k];
                if (Feat(6) && m.RevBad) continue;
                if (HfKeyBetter(m.Hot >= 1 << 20, m.R, m.Packed, m.Room, m.Hot, parentHot >= 1 << 20, parentConn, parentPacked, parentRoom, parentHot)
                    && (bestA < 0 || HfKeyBetter(m.Hot >= 1 << 20, m.R, m.Packed, m.Room, m.Hot, mCands[bestA].Hot >= 1 << 20, mCands[bestA].R, mCands[bestA].Packed, mCands[bestA].Room, mCands[bestA].Hot)))
                    bestA = k;
            }
            packingK = bestA;
        }
        else
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
            mPackPick = -1;
            bool clearing = PackingObligationAdvice(mPackPickCb ??= k => MChoose(ref mPackPick, k));   // cached delegate: no closure per open
            int pk = mPackPick;
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
        if (hfRank && opt.HfUnweighted && (mNodeLimit <= 0 || mExpandedN < mNodeLimit * 3 / 4)) Array.Clear(mWeights, 0, nm); // Festival's HF search runs its first 3/4 unweighted
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
                    if (!hasDistinctAdvisorExit && !pfOn) // exp-609: Festival's only zero-weight move is its advisor
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
        int kNeed = (int)Math.Min(maux[id].MvOff, (uint)nm);
        { int w0 = nm - kNeed; if (MoveWindow > 0 && w0 > MoveWindow) w0 = MoveWindow; kNeed += w0; }   // only the first off+win places of the order are ever read in order
        if (!pfOn && TryPackedSort(nm, useTier, kNeed)) { }
        else {
        for (int k = 0; k < nm; k++) mKeys[k] = new MKey { Tier = useTier ? mCands[k].Tier : 0, Bad = Feat(6) && mCands[k].RevBad ? 1 : 0, W = mWeights[k], Oop = mCands[k].Oop, Packed = mCands[k].Packed, R = mCands[k].R, Room = mCands[k].Room, Hot = mCands[k].Hot, Mobility = mCands[k].Mobility, Plen = pfOn ? mCands[k].B : mCands[k].Plen, Y = pfOn ? mCands[k].T : 0, Idx = k };
        MSortKeys(nm);
        }
        int off = (int)Math.Min(maux[id].MvOff, (uint)nm);
        int win = nm - off;
        if (MoveWindow > 0 && win > MoveWindow) win = MoveWindow;
        for (int j = off; j < off + win; j++)
        {
            int k = mKeys[j].Idx;
            ref var m = ref mCands[k];
            ulong word = (ulong)m.I | (ulong)m.T << 15 | (ulong)mWeights[k] << 30 | (ulong)m.Side << 31 | (ulong)m.R << 33 | (m.Plen < 0x8000 ? (ulong)m.Plen << 48 : 0UL);
            mmv.Add(m.AlreadyUsed ? word | MMoveUsed : word);
            mmvScore.Add(new MoveScore { Oop = (ushort)m.Oop, Packed = (ushort)m.Packed, Conn = (ushort)m.R, Hot = (ushort)Math.Min(m.Hot, 65535), Room = (uint)m.Room, Mobility = (uint)m.Mobility });
        }
        ref var a = ref maux[id];
        int left = 0;
        for (int j = off; j < nm; j++) if (!mCands[mKeys[j].Idx].AlreadyUsed) left++;
        a.MvLen = (uint)win; a.MvNext = 0; a.MvTotal = (uint)nm; a.MvLeft = (uint)left;
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
            ulong hiKey = (ulong)bk << 20;
            if (Novelty2)
            {
                mNovBook ??= new PairBook(nodes.Nw);
                if (mNovRows.Length < cur.Length) mNovRows = new nint[cur.Length * 2];
                for (int j = 0; j < cur.Length; j++) mNovRows[j] = (nint)mNovBook.Find(hiKey | (uint)cur[j]);
            }
            int nov = 0, nwd = (nm + 63) / 64, novOff = mNovWords.Count;
            for (int x = 0; x < nwd; x++) { mNovWords.Add(0); if (AccessNovelty) { mNovT0.Add(0); mNovT1.Add(0); } }
            maux[id].NovOffset = novOff + 1;
            for (int k = 0; k < nm; k++)
            {
                ref var m = ref mCands[k];
                int w = m.T / 64; ulong bit = 1UL << (m.T % 64);
                // the novelty facts only matter for a move that can still take a free slot (budget left, not a rev-group undo)
                bool canNovel = nov < novelty && !(Feat(6) && m.RevBad);
                bool w1Novel = canNovel && (seen[w] & bit) == 0, w2Novel = false;
                bool accessNovel = false;
                if (canNovel && AccessNovelty && m.AccLen > 0)
                    for (int e = m.AccStart; e < m.AccStart + m.AccLen; e++)
                    {
                        ulong raw = mAccBuf[e];
                        ulong tag = raw >> 40 & 0xF;
                        if (tag == 0) { if ((pushSeen![raw / 64] & 1UL << (int)(raw % 64)) == 0) { accessNovel = true; break; } }
                        else if (!mNovAccess.Contains((ulong)bk << 44 | raw)) { accessNovel = true; break; }
                    }
                if (canNovel && Novelty2 && !w1Novel && !accessNovel)   // the pair test only decides when neither of the others already made the move novel
                {
                    // one bit test per box in the box's row of the book: pair {T, c} seen <=> bit floorIdx(T) in row(bk, c)
                    int ft = nodes.Fidx[m.T], fw = ft >> 6; ulong fbit = 1UL << (ft & 63);
                    for (int j = 0; j < cur.Length; j++)
                    {
                        if (j == m.I) continue;
                        ulong* row = (ulong*)mNovRows[j];
                        if (row == null || (row[fw] & fbit) == 0) { w2Novel = true; break; }
                    }
                }
                bool novel = w1Novel || w2Novel || accessNovel;
                if (novel)
                {
                    seen[w] |= bit;
                    if (Novelty2)
                    {
                        int ft = nodes.Fidx[m.T];
                        ulong* trow = mNovBook!.GetOrAdd(hiKey | (uint)m.T);
                        for (int j = 0; j < cur.Length; j++)
                        {
                            if (j == m.I) continue;
                            ulong* xr = (ulong*)mNovRows[j];
                            if (xr == null) { xr = mNovBook.GetOrAdd(hiKey | (uint)cur[j]); mNovRows[j] = (nint)xr; }
                            xr[ft >> 6] |= 1UL << (ft & 63);
                            int fx = nodes.Fidx[cur[j]];
                            trow[fx >> 6] |= 1UL << (fx & 63);
                        }
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

    /// <summary>Go m.score().compare(parent) with the parent's score (oop, packed 0, conn, room, hot, mobility).</summary>
    static int PfCmp(ref MCand m, int oop, int conn, int room, int hot, int mob) =>
        m.Oop != oop ? (m.Oop < oop ? -1 : 1) : m.Packed != 0 ? (m.Packed > 0 ? -1 : 1) : m.R != conn ? (m.R < conn ? -1 : 1) : m.Room != room ? (m.Room < room ? -1 : 1) : m.Hot != hot ? (m.Hot < hot ? -1 : 1) : m.Mobility != mob ? (m.Mobility > mob ? -1 : 1) : 0;

    static int MScoreCmp(ref MCand m, ref MCand o) =>
        m.Oop != o.Oop ? (m.Oop < o.Oop ? -1 : 1) : m.Packed != o.Packed ? (m.Packed > o.Packed ? -1 : 1) : m.R != o.R ? (m.R < o.R ? -1 : 1) : m.Room != o.Room ? (m.Room < o.Room ? -1 : 1) : m.Hot != o.Hot ? (m.Hot < o.Hot ? -1 : 1) : m.Mobility != o.Mobility ? (m.Mobility > o.Mobility ? -1 : 1) : 0;

    static int MKeyCmp(in MKey x, in MKey y)
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
        if (x.Y != y.Y) return x.Y < y.Y ? -1 : 1;
        return x.Idx < y.Idx ? -1 : 1;
    }

    struct PKey { public ulong Hi, Lo; public int Idx; }
    PKey[] mPKey = new PKey[64], mPKeyTmp = new PKey[64]; uint[] mPSeen = new uint[64]; uint mPStamp;

    /// <summary>
    /// The move order of MKeyCmp on two packed words (high: bad, weight, tier, oop, -packed, conn; low: room, hot, -mobility, path length) with the
    /// candidate index as the last tie-break. Only when every field fits its 14/16-bit lane; false = use the general comparator.
    /// Result: mKeys[0..nm).Idx in sorted order.
    /// </summary>
    bool TryPackedSort(int nm, bool useTier, int kNeed)
    {
        if (mPKey.Length < nm) { mPKey = new PKey[nm * 2]; mPKeyTmp = new PKey[nm * 2]; }
        var a = mPKey; bool f6 = Feat(6);
        for (int k = 0; k < nm; k++)
        {
            ref var m = ref mCands[k];
            int tier = useTier ? m.Tier : 0, w = mWeights[k];
            if ((uint)w > 3 || (uint)tier > 3 || (uint)m.Oop > 0x3FFF || (uint)m.Packed > 0xFFFF || (uint)m.R > 0xFFFF || (uint)m.Room > 0xFFFF
                || (uint)m.Hot > 0xFFFF || (uint)m.Mobility > 0xFFFF || (uint)m.Plen > 0xFFFF) return false;
            a[k].Hi = (f6 && m.RevBad ? 1UL << 50 : 0UL) | (ulong)w << 48 | (ulong)tier << 46 | (ulong)m.Oop << 32 | (ulong)(0xFFFF - m.Packed) << 16 | (ulong)m.R;
            a[k].Lo = (ulong)m.Room << 48 | (ulong)m.Hot << 32 | (ulong)(0xFFFF - m.Mobility) << 16 | (ulong)m.Plen;
            a[k].Idx = k;
        }
        if (kNeed * 3 <= nm && kNeed <= 64)
        {
            // partial selection: the kNeed smallest in order; the remaining candidates follow in index order (only their number of unused moves is read)
            var top = mPKeyTmp; int cnt = 0;
            for (int k = 0; k < nm; k++)
            {
                if (cnt == kNeed && !PLess(a[k], top[kNeed - 1])) continue;
                var x = a[k]; int j = cnt < kNeed ? cnt++ : kNeed - 1;
                while (j > 0 && PLess(x, top[j - 1])) { top[j] = top[j - 1]; j--; }
                top[j] = x;
            }
            if (mPSeen.Length < nm) mPSeen = new uint[nm * 2];
            uint stp = ++mPStamp; if (stp == 0) { Array.Clear(mPSeen); stp = mPStamp = 1; }
            for (int j = 0; j < kNeed; j++) { mKeys[j].Idx = top[j].Idx; mPSeen[top[j].Idx] = stp; }
            int o = kNeed;
            for (int k = 0; k < nm; k++) if (mPSeen[k] != stp) mKeys[o++].Idx = k;
            return true;
        }
        var src = a; var dst = mPKeyTmp;
        for (int i = 0; i < nm; i += 8)
        {
            int e = Math.Min(i + 8, nm);
            for (int j = i + 1; j < e; j++)
            {
                var x = src[j]; int k = j - 1;
                while (k >= i && PLess(x, src[k])) { src[k + 1] = src[k]; k--; }
                src[k + 1] = x;
            }
        }
        for (int w = 8; w < nm; w <<= 1)
        {
            for (int lo = 0; lo < nm; lo += 2 * w)
            {
                int mid = Math.Min(lo + w, nm), hi = Math.Min(lo + 2 * w, nm), i = lo, j = mid, o = lo;
                while (i < mid && j < hi) dst[o++] = PLess(src[j], src[i]) ? src[j++] : src[i++];
                while (i < mid) dst[o++] = src[i++];
                while (j < hi) dst[o++] = src[j++];
            }
            (src, dst) = (dst, src);
        }
        for (int j = 0; j < nm; j++) mKeys[j].Idx = src[j].Idx;
        return true;
    }
    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.AggressiveInlining)]
    static bool PLess(in PKey x, in PKey y) => x.Hi != y.Hi ? x.Hi < y.Hi : x.Lo != y.Lo ? x.Lo < y.Lo : x.Idx < y.Idx;

    MKey[] mKeyTmp = [];
    /// <summary>Bottom-up merge sort of mKeys[0..n) (a total order: Idx breaks ties), no delegate.</summary>
    void MSortKeys(int n)
    {
        if (mKeyTmp.Length < mKeys.Length) mKeyTmp = new MKey[mKeys.Length];
        var src = mKeys; var dst = mKeyTmp;
        for (int i = 0; i < n; i += 8)
        {
            int e = Math.Min(i + 8, n);
            for (int j = i + 1; j < e; j++)
            {
                var x = src[j]; int k = j - 1;
                while (k >= i && MKeyCmp(src[k], x) > 0) { src[k + 1] = src[k]; k--; }
                src[k + 1] = x;
            }
        }
        for (int w = 8; w < n; w <<= 1)
        {
            for (int lo = 0; lo < n; lo += 2 * w)
            {
                int mid = Math.Min(lo + w, n), hi = Math.Min(lo + 2 * w, n), a = lo, b = mid, o = lo;
                while (a < mid && b < hi) dst[o++] = MKeyCmp(src[b], src[a]) < 0 ? src[b++] : src[a++];
                while (a < mid) dst[o++] = src[a++];
                while (b < hi) dst[o++] = src[b++];
            }
            (src, dst) = (dst, src);
        }
        if (!ReferenceEquals(src, mKeys)) Array.Copy(src, mKeys, n);
    }

    int mPackPick; Action<int>? mPackPickCb;
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
            if (InPR(t - dirs[side])) { canonical = side; break; }
        if (t == cur[i] && hfRank && opt.HfNoReturn) return true; // exp-268: no macro returning its box to its square
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
            int plen = mbdep[k];
            tp = Prof.Start();
            CandMemo* cm = null;
            if (CandMemoOn)
            {
                ulong ck = mBoxHash ^ nodes.ZBox(cur[i]) ^ nodes.ZBox(t);
                cm = CandMemoSlot(ck);
            }
            int r;
            if (cm != null && cm->R != 0xFFFF) { r = cm->R; ++mOpenCands; }
            else
            {
                r = MChildRegionCount(t, ++mOpenCands);
                if (cm != null && r < 0xFFFF) cm->R = (ushort)r;
            }
            Prof.Stop(4, tp);
            if (tgtPocket != null && r > 1) r = RegionsRel(); // exp-460: regions relative to the target (occ holds the child)
            if (CheckRegions && r != Regions()) throw new InvalidOperationException($"incremental region count {r} != flood {Regions()}");
            if (mCandN == mCands.Length) Array.Resize(ref mCands, mCandN * 2);
            tp = Prof.Start();
            var (mob, expl, accS, accL) = MacroStage >= 5 && !pfOn ? MMoveAccess(cur, i, t) : (0, false, 0, 0);
            Prof.Stop(3, tp);
            tp = Prof.Start();
            int room, packed, oop, progress = -2;
            if (pfOn || hfRank) room = 0;
            else if (t == cur[i]) room = mParentRoom;
            else if (cm != null && cm->Room >= 0) room = cm->Room;
            else { room = MRoomConnChild(cur[i], t); if (cm != null) cm->Room = room; }
            Prof.Stop(1, tp);
            if (mPlan != null)
            {
                if (cm != null && cm->Phase != short.MinValue) { progress = cm->Phase; packed = cm->Packed; oop = cm->Oop; }
                else
                {
                    var (cphase, cpacked) = mPlan.ChildStatus(cur[i], t);
                    packed = mPlan.Fest != null && mPlan.FestPacked ? mPlan.Fest.PackedChild(occ, cur[i], t, mPlan.FestMiss) : cpacked; progress = cphase;
                    oop = MOutOfPlanChild(cur, cphase, i, t);
                    if (cm != null && cphase > short.MinValue && cphase <= short.MaxValue && packed >= 0 && packed < 0xFFFF && oop >= 0 && oop < 0xFFFF) { cm->Phase = (short)cphase; cm->Packed = (ushort)packed; cm->Oop = (ushort)oop; }
                }
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
            int hotOverride = -1;
            if (pfOn)
            {
                cur.CopyTo(hfBoxes);
                hfBoxes[i] = t;
                int cd = MatchChild(hfBoxes, i, cur[i]);
                if (cd >= AssignmentInf) cd = -1;
                var pt = new PfFest.Tuple(r, 0, 0, cd, 0);
                PfFill(ref pt, hfBoxes);
                oop = pt.Reg; packed = 0; room = pt.Rooms; hotOverride = pt.Hot; mob = pt.Mobility; r = pt.Reg;
            }
            else if (hfRank)
            {
                cur.CopyTo(hfBoxes);
                hfBoxes[i] = t;
                oop = r; room = HotRel(hfBoxes); hotOverride = MatchChild(hfBoxes, i, cur[i]);
                // Festival predicts a child's distance with only the moved box (and the parent's frozen boxes) pinned
                if (opt.HfFrozenMatch && (mParentFrozen || HfFrozenOff(hfBoxes.AsSpan(i, 1)))) hotOverride = Math.Max(hotOverride, 1 << 20);
                if (opt.HfMatchFirst && hotOverride >= 1 << 20) oop += HfNoMatch;
            }
            bool cUndo = false, cInternal = false, cDeadChild = false, cGroupDup = false, cDistinct = false, cStored = false, cAlready = false; ulong cExit = 0;
            bool reuse = opt.RetargetReuse && mRetargetNodes > 0;
            if (Feat(6) && (!mReopening || reuse))
            {
                int b = cur[i], p = mRevP;
                if (!mReopening && mPB0 >= 0 && b == mPT0 && t == mPB0 && mCandNP == mPPlayer) { MUnionRevGroup(p, mOpenId); mGid = MFindRevGroup(mOpenId); cUndo = true; }
                int fb = nodes.Fidx[b], ft = nodes.Fidx[t];
                ulong zh = mRevParentHash ^ nodes.ZPlayer(nodes.Player(mOpenId)) ^ nodes.ZPlayer(mCandNP);
                if (b != t)
                {
                    mRevKey[fb >> 6] &= ~(1UL << (fb & 63)); mRevKey[ft >> 6] |= 1UL << (ft & 63);
                    zh ^= nodes.ZBox(b) ^ nodes.ZBox(t);
                }
                int existing = nodes.Find(mRevKey, mCandNP, zh, out _, out uint fpx);
                cStored = existing >= 0;
                if (reuse && existing >= 0 && existing < mRetargetNodes)
                {
                    bool improves = hfRank && opt.HfFestSched && mOpenId < hfBest.Count && existing < hfBest.Count && HfBetter(hfBest[mOpenId], hfBest[existing]);
                    if (maux[existing].Dead != 0 || (!improves && nodes.G(mOpenId) + plen >= nodes.G(existing)))
                    {
                        cAlready = true;
                        if (!mReopening && maux[existing].Dead == 0 && nodes.Parent(existing) != mOpenId) RecordEdge(mOpenId, existing);
                    }
                }
                if (b != t) { mRevKey[ft >> 6] &= ~(1UL << (ft & 63)); mRevKey[fb >> 6] |= 1UL << (fb & 63); }
                if (mReopening) { }
                else if (existing >= 0)
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
            mCands[mCandN++] = new MCand { Undo = cUndo, Internal = cInternal, DeadChild = cDeadChild, GroupDupExit = cGroupDup, DistinctExit = cDistinct, Stored = cStored, AlreadyUsed = cAlready, ExitKey = cExit, I = i, B = cur[i], T = t, R = r, Packed = packed, Plen = plen, Side = k & 3, Room = room, Oop = oop, Progress = progress, Hot = hotOverride >= 0 ? hotOverride : mHotCounts != null ? HotAfterMemo(cm, cur, i, t) : 0, Mobility = mob, Explorer = expl, AccStart = accS, AccLen = accL };
        }
        SetOcc(t, false);
        return true;
    }

    bool mSdBuilt; int mSdStatic, mSdNSplit; bool[] mSdFlag = new bool[64]; int[] mSdSplit = new int[64];
    void MSdBuild(ReadOnlySpan<int> cur)
    {
        if (mSdFlag.Length < cur.Length) { mSdFlag = new bool[cur.Length * 2]; mSdSplit = new int[cur.Length * 2]; }
        mSdStatic = 0; mSdNSplit = 0;
        for (int j = 0; j < cur.Length; j++)
        {
            int c = cur[j]; mSdFlag[j] = false;
            if (goal[c]) continue;
            if (sd!.IsSplitter(c)) mSdSplit[mSdNSplit++] = j;
            else if (sd.StaticDead(c)) { mSdFlag[j] = true; mSdStatic++; }
        }
        mSdBuilt = true;
    }

    /// <summary>Pruning rules for cur[i] pushed to t (occ holds the child). 0 = alive.</summary>
    int MPushDead(ReadOnlySpan<int> cur, int i, int t, int canonical)
    {
        if (pull) // the dead-square table (pull distance to a start square) already filtered t; the push deadlock family does not apply
        {
            if (PullFreezeMemo(cur[i], t)) { stats.Freeze++; return 2; }
            if (pfOn && PfInefficient(t, cur[i])) return 10; // exp-696
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
            if (InPR(p))
            {
                // per box search: boxes on cells whose side-dead verdict does not depend on the player's side are counted once; only
                // boxes on separator cells (and the moved box) are asked per candidate
                if (!mSdBuilt) MSdBuild(cur);
                if (mSdStatic - (mSdFlag[i] ? 1 : 0) > 0) dead = true;
                else
                {
                    for (int s2 = 0; s2 < mSdNSplit; s2++)
                    {
                        int j = mSdSplit[s2];
                        if (j != i && sd.Dead(cur[j], p)) { dead = true; break; }
                    }
                    if (!dead && !goal[t] && sd.Dead(t, p)) dead = true;
                }
            }
            Prof.Stop(14, tq);
            if (dead) { stats.SideDead++; return 5; }
        }
        if (hfRank && opt.HfFestIneff && FestInefficient(t)) return 8;
        if (hfRank && dd != null && dd.Dead(occ, t, t - dirs[canonical])) return 9;
        if (Feat(7) && InPR(t - dirs[canonical]) && PairSideDeadlock(t, t - dirs[canonical])) { stats.PairDead++; return 7; }
        if (Feat(3) && CorridorFlowDeadlock(t)) { stats.CorridorFlow++; return 6; }
        return 0;
    }

    // ---- apply ----------------------------------------------------------------------------------------------------------------------
    int MApply(int id, ulong m, int[] cur, int[] child, int h, uint st, ulong parentHash, out int ch)
    {
        mLastAdded = -1;
        int i = (int)(m & 0x7FFF), t = (int)(m >> 15 & 0x7FFF), wd = (int)(m >> 30 & 1), r = (int)(m >> 33 & 0x7FFF), side = (int)(m >> 31 & 3);
        int b = cur[i];
        ch = h;
        // the move word carries the path length of the box search that generated it (the last push direction is `side`): no second box search
        int plenW = (int)(m >> 48 & 0x7FFF), pathLen, lastDir;
        if (plenW > 0) { pathLen = plenW; lastDir = side; }
        else
        {
            var seq = MBoxPathTo(b, t, st, side, cur);
            if (seq.Count == 0) return -1;
            pathLen = seq.Count; lastDir = seq[^1].Dir;
        }
        cur.AsSpan().CopyTo(child);
        child[i] = t;
        SortUtil.Insertion(child);
        SetOcc(b, false); SetOcc(t, true);
        int sideC = PlayerAfter(t, dirs[lastDir]);
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
        int cid = MAdd(zh, np, id, b, t, lastDir, nodes.G(id) + pathLen, ch, w, r, child);
        SetOcc(t, false); SetOcc(b, true);
        return cid;
    }

    // ---- search loop (Go solve + loop) -----------------------------------------------------------------------------------------------
    SolveResult MacroSearch(double timeLimitSec, System.Diagnostics.Stopwatch sw)
    {
        mNodeLimit = nodeLimitOverride > 0 ? nodeLimitOverride : opt.NodeBudget;
        mDeadlineMs = opt.NodeBudget > 0 || timeLimitSec <= 0 ? 0 : (mStarted ? clock.ElapsedMilliseconds : 0) + timeLimitSec * 1000;
        long expanded = 0;
        if (mStarted) mCancelled = false; // exp-278: a later round of the same search (Resumable)
        else
        {
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
        if (pfOn)
        {
            PfReachInit(); // exp-619: exact single-box reach in the start-square matching
            if (rootCells.Length > 1) rootCells = LiveZoneRoots(rootCells); // exp-609: Festival drops start zones proven dead
        }
        foreach (int rp in rootCells)
        {
            int p0 = Reach(rp, mark);
            uint st0 = stamp;
            for (int x = 0; x < nodes.Nw; x++) ckey[x] = 0;
            foreach (int b in start) Flip(b);
            int root = MAdd(nodes.HashOf(start, p0), p0, -1, -1, -1, -1, 0, h0, 0, -1, start);
            if (root >= 0) { MOpen(root, start, h0, st0); PropagateDead(root); MEnqueue(root); DeferReopen(root); }
        }
        mBest = 0;
        mSearchStartMs = mLastImproveMs = clock.ElapsedMilliseconds;
        foreach (int b in start) SetOcc(b, false);
        mStarted = true;
        }
        var cur = mCur; var child = mChild;
        meetMacro = true;
        while (true)
        {
            if (MExpired()) return new SolveResult { Reason = "timeout", Nodes = expanded };
            if (opt.PublishBest != null && pull && (expanded & 127) == 0) PublishPullBest();
            if (opt.Net != null && (expanded & 255) == 0 && (pull ? PullMeetCheckMail() : MeetCheckMail()) is { } met) return new SolveResult { Solved = true, Solution = met, Nodes = expanded };
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
            if ((expanded & 255) == 0 && opt.BudgetBytes > 0 && (ulong)(nodes.Bytes + mmv.Bytes + mmvScore.Bytes + maux.Bytes) + (opt.PeerBytes?.Invoke() ?? 0) > opt.BudgetBytes / (ulong)(bat?.Live() ?? 1))
                return new SolveResult { Reason = "memory", Nodes = expanded };
            int pl = nodes.Player(id);
            Reach(pl, mark);
            uint st = stamp;
            ulong mv = mmv[e.Move];
            mmv[e.Move] = mv | MMoveUsed;
            maux[id].MvLeft--;
            ulong parentHash = nodes.HashOf(cur, pl);
            long tap = Prof.Start();
            hfAddDist = hfRank ? mmvScore[(int)e.Move].Hot : -1;
            if (pfOn) { var ms = mmvScore[(int)e.Move]; pfNext = PfFest.Tuple.FromMove(ms.Oop, (int)ms.Room, ms.Hot, (int)ms.Mobility); pfHave = true; }
            mAddRoom = pfOn || hfRank ? -1 : (int)mmvScore[(int)e.Move].Room;
            int cid = MApply(id, mv & ~MMoveUsed, cur, child, h, st, parentHash, out int ch);
            mAddRoom = -1;
            hfAddDist = -1; pfHave = false;
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
            DeferReopen(id);
            foreach (int b in cur) SetOcc(b, false);
            if (meetPending.Count > 0) { if (pull) PullMeetPost(cur); else MeetPost(cur); }
            if (cid < 0) continue;
            MNoteBest(cid);
            if (tgt != null && ch != 0 && OnTarget(child))
            {
                string? tsol = CompleteTarget(cid);
                if (tsol != null) return new SolveResult { Solved = true, Solution = tsol, Nodes = expanded };
            }
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
            DeferReopen(cid);
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

    int HotAfterMemo(CandMemo* cm, ReadOnlySpan<int> cur, int i, int t)
    {
        if (cm == null) return HotAfterP(cur, i, t);
        if (cm->Hot != 0xFFFF) return cm->Hot;
        int h = HotAfterP(cur, i, t);
        if (h < 0xFFFF) cm->Hot = (ushort)h;
        return h;
    }

    // the freeze proof of a pulled box is a pure function of (child box set, t): remembered by the Zobrist hash of both (bit 0 = verdict)
    ulong* fzMemo; const int FzMemoBits = 17;
    bool PullFreezeMemo(int b, int t)
    {
        if (CandMemoOff) return PullFreezeDead(t);
        if (fzMemo == null) fzMemo = (ulong*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(8 << FzMemoBits));
        ulong key = (mBoxHash ^ nodes.ZBox(b) ^ nodes.ZBox(t) ^ ((ulong)t + 1) * 0x9E3779B97F4A7C15UL) | 2;   // bit 1 marks an entry
        ulong* e = fzMemo + ((int)(key >> 41) & ((1 << FzMemoBits) - 1));
        ulong v = *e;
        if ((v ^ key) >> 1 == 0 && (v & 2) != 0) return (v & 1) != 0;
        bool dead = PullFreezeDead(t);
        *e = (key & ~1UL) | (dead ? 1UL : 0UL);
        return dead;
    }

    // ---- candidate memo: region count, room connectivity and hotspot count of a child are pure functions of its box set (and the static level), keyed by the xor of the Zobrist box keys ----
    struct CandMemo { public ulong Key; public int Room; public ushort R, Hot, Packed, Oop; public short Phase; }
    const int CandMemoBits = 16;
    CandMemo* candMemo; ulong mBoxHash;
    bool CandMemoOn => !pfOn && !hfRank && tgtPocket == null && !CandMemoOff;
    static readonly bool CandMemoOff = Environment.GetEnvironmentVariable("SOKSOLV_CANDMEMO_OFF") == "1";
    CandMemo* CandMemoSlot(ulong key)
    {
        if (candMemo == null) candMemo = (CandMemo*)System.Runtime.InteropServices.NativeMemory.AllocZeroed((nuint)(sizeof(CandMemo) << CandMemoBits));
        CandMemo* e = candMemo + ((int)(key >> 40) & ((1 << CandMemoBits) - 1));
        if (e->Key != key) { e->Key = key; e->Room = -1; e->R = 0xFFFF; e->Hot = 0xFFFF; e->Phase = short.MinValue; }
        return e;
    }

    int HotAfterP(ReadOnlySpan<int> cur, int i, int t) { long tp = Prof.Start(); int h = HotAfter(cur, mHotCounts!, i, t); Prof.Stop(2, tp); return h; }
}
