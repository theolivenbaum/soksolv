using System.Diagnostics;
using System.Numerics;
using Soksolv.Model;
using Soksolv.Search.Hub;
using Soksolv.Search.Meet;

namespace Soksolv.Search;

/// <summary>
/// The hub lane (Go hub.go, exp-451; `-strategy hub-gen` / `hub`): the generator (<see cref="HubGen"/>) nominates loose hub boards, a pool
/// ranks them (tier, trapped goal boxes, cut, pinned, matching distance from the start), and up to two forward toward searches are
/// committed to hubs in expansion-budget windows that double while the context keeps improving (-hub-race: window in forward
/// expansions). Backward (generator) work gets half the forward time. Timing: wall clock by default (hubUnit(T) = clamp(T/12, 2 s, 30 s),
/// slice q = U/4), or, with <see cref="FullOptions.HubNodeSlices"/> = K > 0, expansion counts (forward slice K, generator burst 4K,
/// generator slice = half the round's forward expansions; deterministic; opt.NodeBudget caps gen + forward expansions).
/// </summary>
public sealed unsafe partial class FullSolver
{
    const int HubPoolMax = 64, HubTriedMax = 64, HubSlots = 2;
    const byte HubFree = 0, HubInfeasible = 1;
    const int HubIdleK = 4;
    const bool HubFallbackCheck = true;

    internal struct HubCand
    {
        public int Id, Cut, Pinned, Dist, Lb, Trapped;
        public byte Tier, State;
        public bool Pocket;
        static int Len(int x) => 32 - BitOperations.LeadingZeroCount((uint)x);
        public bool KeyLess(in HubCand o)
        {
            if (Tier != o.Tier) return Tier < o.Tier;
            if (Trapped != o.Trapped) return Trapped < o.Trapped;
            int a = Len(Cut), b = Len(o.Cut);
            if (a != b) return a < b;
            if (Pinned != o.Pinned) return Pinned < o.Pinned;
            if (Dist != o.Dist) return Dist > o.Dist;
            return Id < o.Id;
        }
        public static bool PickLess(in HubCand a, in HubCand b)
        {
            if (a.Tier != b.Tier) return a.Tier < b.Tier;
            if (a.Trapped != b.Trapped) return a.Trapped < b.Trapped;
            int x = Len(a.Cut), y = Len(b.Cut);
            if (x != y) return x < y;
            if (a.Pinned != b.Pinned) return a.Pinned < b.Pinned;
            if (a.Lb != b.Lb) return a.Lb < b.Lb;
            if (a.Dist != b.Dist) return a.Dist > b.Dist;
            return a.Id < b.Id;
        }
    }

    sealed class HubCtx
    {
        public FullSolver Fwd = null!;
        public HubCand Cand;
        public long WinExp, LastImprExp;
        public int Scanned, BestOn, BestDist = 1 << 30;
        public bool Reached;
    }

    /// <summary>Go hubTier: 0 is Festival's endpoint signature (one region, no broken rooms, no hotspots) ... 4 the fallback.</summary>
    static byte HubTier(PfFest.Tuple t)
    {
        if (t.Reg != 1 || t.Dist < 0) return 4;
        if (t.Rooms == 0 && t.Hot == 0) return 0;
        if (t.Rooms == 0 && t.Hot <= 1) return 1;
        if (t.Rooms <= 1 && t.Hot <= 1) return 2;
        if (t.Rooms <= 1) return 3;
        return 4;
    }

    internal sealed class HubLane
    {
        readonly FullSolver ps; readonly string[] rows; readonly Level lv; readonly FullOptions opt;
        readonly bool nodeMode; readonly long slice;       // node mode: forward slice K
        readonly Stopwatch sw; readonly double limitSec;   // lane clock (shared with the ring) and the run's time limit (0: none)
        internal readonly HubGen gen;
        readonly HubCand[] pool = new HubCand[HubPoolMax]; int nPool;
        readonly int[] tried = new int[HubTriedMax]; int nTried;
        int scan; readonly int delta; readonly int[] maxD = new int[4];
        readonly int[] buf;
        readonly HubCtx?[] ctxs = new HubCtx?[HubSlots];
        // counters
        public int Nominated, Committed, Reached, Abandoned, Extensions, MemFullN, Exhausted, TailFail, Infeasible, ScanSkipped, Scanned, Eligible, ReachRej, FallbackRej;
        public int[] TierCount = new int[5], TierSeen = new int[5];
        public int NReg1, MinHot0 = 1 << 30, MinHot = 1 << 30, BestOnAll, Rounds, Idle, Rescans, NSeen;
        int lastHot, lastPocket;
        ulong[] candSeen = [];
        bool rescanOn; int idleCapRun;
        public string IdleEnd = "";
        public long FwdExpSum;
        double scanMs;
        ulong[]? dummy;
        // v0 signature scratch
        int[]? sigBuf, sigCnt; bool[]? sigGoal;

        public HubLane(FullSolver ps, string[] rows, Level lv, FullOptions opt, Stopwatch sw, double limitSec, bool nodeMode)
        {
            this.ps = ps; this.rows = rows; this.lv = lv; this.opt = opt; this.sw = sw; this.limitSec = limitSec;
            this.nodeMode = nodeMode; slice = opt.HubNodeSlices;
            if (ps.MacroStage >= 2 && ps.rmRooms.Length == 0) ps.MComputeRooms(); // Go builds the rooms in the solver constructor
            delta = Math.Max(2, ps.n / 8);
            buf = new int[ps.n];
            gen = HubGen.Create(ps)!;
            if (gen != null && opt.BudgetBytes > 0)
            {
                ulong b = opt.BudgetBytes;
                gen.CapBytes = b / 2 - Math.Min(0UL, b / 4);                           // genTracked is 0 at start (no festb prefix)
                if (HubGenFlags.Cap > 0) gen.CapBytes = (ulong)(HubGenFlags.Cap * b);
            }
        }

        bool Cancelled => opt.Cancel?.Stopped ?? false;
        double Left() => limitSec > 0 ? Math.Max(0.001, limitSec - sw.Elapsed.TotalSeconds) : 0;
        bool Live()
        {
            if (Cancelled) return false;
            if (nodeMode) return opt.NodeBudget <= 0 || gen.Expanded + FwdExpSum < opt.NodeBudget;
            return limitSec <= 0 || sw.Elapsed.TotalSeconds < limitSec;
        }
        long Room() => opt.NodeBudget > 0 ? Math.Max(1, opt.NodeBudget - (gen.Expanded + FwdExpSum)) : long.MaxValue;
        ulong GenTracked => gen.Base + gen.Bytes;
        ulong CtxBytes(int i) => ctxs[i]?.Fwd.RaceBytes ?? 0;

        static double HubUnitSec(double total) => total <= 0 ? 10 : Math.Min(Math.Max(total / 12, 2), 30);

        /// <summary>One slice of the generator: in node mode d expansions, else d seconds of wall time (clipped to the lane deadline). True: the generator is over.</summary>
        bool GenSlice(double d)
        {
            bool over;
            if (nodeMode) over = gen.Run(0, Math.Min((long)d, Room()), () => Cancelled);
            else
            {
                double sec = limitSec > 0 ? Math.Min(d, Left()) : d;
                long dl = Stopwatch.GetTimestamp() + (long)(sec * Stopwatch.Frequency);
                over = gen.Run(dl, 0, () => Cancelled);
            }
            return over;
        }

        public SolveResult Run()
        {
            var r = new SolveResult { Reason = "festb ended" };
            if (gen == null || !Live()) return r;
            double total = limitSec > 0 ? Left() : 0;
            double U = HubUnitSec(total);
            double q = nodeMode ? slice : U / 4 * 1000;   // ms (wall) or expansions (node mode)
            double lastRound = 1000;
            bool firstScan = true;
            double burst = nodeMode ? 4 * slice : (total > 0 ? total * HubGenFlags.Init : 10) ;
            bool backDone = GenSlice(burst);
            if (gen.Cand.Count == 0 && Live())
            {
                IdleEnd = "zero-cand";
                return new SolveResult { Reason = "hub idle", Nodes = gen.Expanded + FwdExpSum };
            }
            double idleWait = 0;
            for (int round = 0; Live(); round++)
            {
                var roundSw = Stopwatch.StartNew();
                long genBefore = gen.Expanded; bool ranFwd = false, paused = false;
                double budget = lastRound / 20;
                if (firstScan) { budget = 1500; firstScan = false; }
                if (rescanOn) budget = 500;
                HarvestG(nodeMode ? double.MaxValue : budget);
                for (int i = 0; i < HubSlots; i++)
                {
                    if (ctxs[i] != null) continue;
                    if (i == 1 && (ctxs[0] == null || ctxs[0]!.WinExp == HubRaceUnit)) continue;
                    var c = Nominate();
                    if (c != null) Commit(i, c.Value);
                }
                double sumFwd = 0;
                for (int i = 0; i < HubSlots; i++)
                {
                    var c = ctxs[i];
                    if (c == null) continue;
                    ranFwd = true;
                    long before = c.Fwd.mExpandedN;
                    var t0 = sw.Elapsed;
                    string? sol = Forward(c, q, out var fr);
                    long used = c.Fwd.mExpandedN - before;
                    FwdExpSum += used;
                    sumFwd += nodeMode ? used : (sw.Elapsed - t0).TotalMilliseconds;
                    if (sol != null) return new SolveResult { Solved = true, Solution = sol, Nodes = gen.Expanded + FwdExpSum };
                    WindowCheck(i, fr);
                    if (!Live()) break;
                }
                if (!backDone && Live())
                {
                    double bslice = sumFwd / 2;
                    if (sumFwd == 0) bslice = q;
                    if (opt.BudgetBytes > 0 && GenTracked >= opt.BudgetBytes / 2) paused = true;
                    else backDone = GenSlice(nodeMode ? bslice : bslice / 1000);
                }
                lastRound = roundSw.Elapsed.TotalMilliseconds;
                Rounds++;
                if (!ranFwd && gen.Expanded == genBefore && scan >= gen.Cand.Count)
                {
                    Idle++;
                    if (NSeen < gen.Cand.Count)
                    {
                        scan = 0; rescanOn = true; Rescans++;
                        idleWait = 0; idleCapRun = 0;
                        continue;
                    }
                    idleWait = Math.Min(Math.Max(2 * idleWait, 1), 64);
                    double w = idleWait;
                    if (!nodeMode)
                    {
                        if (limitSec > 0) w = Math.Min(w, Left() * 1000);
                        if (w > 0) Thread.Sleep((int)Math.Ceiling(w));
                    }
                    if (idleWait == 64) idleCapRun++;
                    if (idleCapRun >= HubIdleK)
                    {
                        IdleEnd = "idle";
                        return new SolveResult { Reason = "hub idle", Nodes = gen.Expanded + FwdExpSum };
                    }
                }
                else { idleWait = 0; idleCapRun = 0; }
                if (backDone && LiveCount() == 0 && NFreeCand() == 0)
                {
                    if (scan >= 0) break;   // Go: scan >= len(ps.player), and the hub's pull tree is never grown (0 nodes)
                }
            }
            return new SolveResult { Reason = "hub ended", Nodes = gen.Expanded + FwdExpSum };
        }

        int LiveCount() { int n = 0; foreach (var c in ctxs) if (c != null) n++; return n; }
        int NFreeCand() { int n = 0; for (int i = 0; i < nPool; i++) if (pool[i].State == HubFree) n++; return n; }

        /// <summary>Go forward: one forward slice of c (wall: q ms; node mode: q expansions); updates progress.</summary>
        string? Forward(HubCtx c, double q, out SolveResult r)
        {
            var f = c.Fwd;
            if (nodeMode) f.nodeLimitOverride = Math.Min(c.WinExp, f.mExpandedN + Math.Min((long)q, Room()));
            double sec = limitSec > 0 ? Math.Min(q / 1000, Left()) : q / 1000;
            string? sol = f.TowardRun(nodeMode ? 0 : sec, out r);
            Progress(c);
            return sol;
        }

        void Progress(HubCtx c)
        {
            var f = c.Fwd;
            for (int id = c.Scanned; id < f.hfOwn.Count; id++)
            {
                var t = f.hfOwn[id];
                if (t.On > c.BestOn || t.On == c.BestOn && !t.Inf && t.Dist < c.BestDist)
                {
                    c.BestOn = t.On; c.BestDist = t.Dist; c.LastImprExp = f.mExpandedN;
                }
            }
            if (f.hfOwn.Count > c.Scanned) c.Scanned = f.hfOwn.Count;
            if (!c.Reached && c.BestOn >= ps.n) { c.Reached = true; Reached++; }
            BestOnAll = Math.Max(BestOnAll, c.BestOn);
        }

        void WindowCheck(int i, SolveResult r)
        {
            var c = ctxs[i];
            if (c == null) return;
            string end = "";
            if (c.Fwd.mMemFull) { MemFullN++; end = "memfull"; }
            else if (r.Reason != "timeout" && r.Reason != "retarget" && r.Reason != "")
            {
                if (r.Reason == "target region unreachable") { TailFail++; end = "tail"; }
                else { Exhausted++; end = "exhausted"; }
            }
            else if (c.Fwd.mExpandedN >= c.WinExp)
            {
                long half = c.WinExp / 2;
                if (c.WinExp > HubRaceUnit) half = c.WinExp - c.WinExp / 4;
                if (c.LastImprExp >= half)
                {
                    c.WinExp *= 2; c.Fwd.nodeLimitOverride = c.WinExp; Extensions++;
                }
                else end = "stalled";
            }
            if (end != "") Drop(i);
        }

        void Drop(int i)
        {
            var c = ctxs[i]!;
            Abandoned++;
            if (c.Cand.Id >= 0)
            {
                float frac = 0;
                if (ps.n > 0) frac = (float)c.BestOn / ps.n;
                RaceSharedMark(SigOf(c.Cand.Id), frac);
                tried[nTried % HubTriedMax] = c.Cand.Id; nTried++;
            }
            c.Fwd.Dispose();
            ctxs[i] = null;
        }

        public void DisposeAll() { for (int i = 0; i < HubSlots; i++) { ctxs[i]?.Fwd.Dispose(); ctxs[i] = null; } }

        // ---- pool ---------------------------------------------------------------------------------------------------------
        bool NearDup(int a, int b) => gen.NearDup(a, b, delta);

        bool DupLive(int id)
        {
            foreach (var c in ctxs) if (c != null && c.Cand.Id >= 0 && NearDup(id, c.Cand.Id)) return true;
            return false;
        }

        bool InTried(int id)
        {
            if (DupLive(id)) return true;
            for (int k = 0; k < Math.Min(nTried, HubTriedMax); k++)
                if (tried[k] == id || NearDup(id, tried[k])) return true;
            return false;
        }

        bool InTriedExact(int id)
        {
            for (int k = 0; k < Math.Min(nTried, HubTriedMax); k++) if (tried[k] == id) return true;
            return false;
        }

        ulong SigOf(int id)
        {
            if (sigBuf == null)
            {
                sigBuf = new int[ps.n];
                sigCnt = new int[ps.rmRooms.Length + 1];
                sigGoal = new bool[ps.cells];
                foreach (int g in ps.lv.Boxes) sigGoal[g] = true;
            }
            gen.Unpack(id, sigBuf);
            return BoxSig(sigGoal!, ps.RoomCells, sigCnt!, sigBuf);
        }

        static bool SharedTriedSig(ulong sig)
        {
            lock (raceShared) return raceShared.TryGetValue(sig, out var r) && r.tries > 0;
        }

        int PoolCap => Math.Min(StrataMax, HubPoolMax);

        void HarvestG(double budgetMs)
        {
            long t0 = Stopwatch.GetTimestamp();
            double Since() => (Stopwatch.GetTimestamp() - t0) * 1000.0 / Stopwatch.Frequency;
            int end = gen.Cand.Count;
            if (scan >= end) { rescanOn = false; return; }
            int need = (end + 63) >> 6;
            if (candSeen.Length < need) Array.Resize(ref candSeen, need);
            if (rescanOn)
            {
                int visited = 0, k = scan;
                for (; k < end; k++)
                {
                    if ((candSeen[k >> 6] & 1UL << (k & 63)) != 0) continue;
                    if ((visited & 63) == 63 && Since() > budgetMs) break;
                    visited++;
                    HarvestOne(k);
                }
                scan = k;
                if (k >= end) rescanOn = false;
                scanMs += Since();
                return;
            }
            int stride = 1, vis2 = 0, kk = scan;
            for (; kk < end; kk += stride)
            {
                if ((vis2 & 63) == 63)
                {
                    double el = Since();
                    if (el > 3 * budgetMs) break;
                    double per = el / vis2;
                    if (per > 0)
                    {
                        double capN = budgetMs / per;
                        if (capN > 0 && (double)(end - kk) > capN) stride = (int)((end - kk) / capN) + 1;
                    }
                }
                vis2++;
                HarvestOne(kk);
            }
            ScanSkipped += Math.Min(kk, end) - scan - vis2;
            scan = Math.Min(kk, end);
            scanMs += Since();
        }

        void HarvestOne(int k)
        {
            if ((candSeen[k >> 6] & 1UL << (k & 63)) == 0) { candSeen[k >> 6] |= 1UL << (k & 63); NSeen++; }
            Scanned++;
            int id = gen.Cand[k];
            var t = gen.Tuple(id);
            bool pocket = t.Reg > 1;
            t = t with { Reg = 1 };
            byte tier = HubTier(t);
            TierSeen[tier]++;
            NReg1++;
            MinHot0 = Math.Min(MinHot0, t.Hot);
            MinHot = Math.Min(MinHot, t.Hot);
            if (tier > 3) return;
            if (t.Dist > maxD[tier]) maxD[tier] = t.Dist;
            if (t.Dist < maxD[tier] / 2) return;
            Eligible++;
            Score(id, out int cut, out int pinned, out int trapped);
            if (pocket)
            {
                if (lastPocket > HubPocketMax) return;
                t = t with { Hot = lastHot };
                tier = HubTier(t);
                if (tier > 3) return;
            }
            Admit(new HubCand { Id = id, Cut = cut, Pinned = pinned, Trapped = trapped, Dist = t.Dist, Tier = tier, Pocket = pocket });
        }

        /// <summary>Go hub.score: the cut proxy (free squares on no cycle) and the pinned / trapped boxes of node id, judged on the player's region.</summary>
        void Score(int id, out int cut, out int pinned, out int trapped)
        {
            var wall = ps.wall; var occ = ps.occ; var dirs = ps.dirs;
            gen.Invalidate();
            gen.Unpack(id, buf);
            foreach (int b in buf) occ[b] = true;
            var pocketCells = new List<int>();
            lastHot = 0; lastPocket = 0;
            gen.FloodAt(gen.Node(id).Player);
            for (int c = 0; c < wall.Length; c++)
                if (!wall[c] && !occ[c] && !gen.LabIs1(c)) pocketCells.Add(c);
            lastPocket = pocketCells.Count;
            foreach (int c in pocketCells) occ[c] = true;
            cut = ps.pff!.Bicon(occ);
            pinned = 0; trapped = 0;
            foreach (int b in buf)
            {
                bool ok = false;
                foreach (int d in dirs)
                    if (!wall[b + d] && !wall[b + 2 * d] && !occ[b + 2 * d] && gen.InPlayerRegion(b + d)) { ok = true; break; }
                if (!ok) lastHot++;
            }
            foreach (int qd in buf)
            {
                bool ok = false;
                foreach (int d in dirs)
                {
                    int a = qd - d, b = qd - 2 * d;
                    if (!wall[a] && !occ[a] && !wall[b] && !occ[b]) { ok = true; break; }
                }
                if (!ok) pinned++;
            }
            foreach (int b in buf)
            {
                if (!ps.pff!.IsGoal(b)) continue;
                bool ok = false;
                foreach (int d in dirs)
                    if (!wall[b + d] && !wall[b + 2 * d] && !occ[b + 2 * d] && gen.InPlayerRegion(b + d)) { ok = true; break; }
                if (!ok) trapped++;
            }
            foreach (int c in pocketCells) occ[c] = false;
            foreach (int b in buf) occ[b] = false;
        }

        void Admit(HubCand c)
        {
            if (InTried(c.Id)) return;
            int dup = -1;
            ulong csig = SigOf(c.Id);
            for (int i = 0; i < nPool; i++)
            {
                if (SigOf(pool[i].Id) == csig)
                {
                    if (!c.KeyLess(pool[i])) return;
                    dup = i;
                    break;
                }
            }
            if (dup >= 0) { pool[dup] = c; return; }
            if (nPool < PoolCap) { pool[nPool++] = c; return; }
            int nPk = 0;
            for (int i = 0; i < nPool; i++) if (pool[i].Pocket) nPk++;
            bool victimPocket = c.Pocket;
            if (nPk > PoolCap / 2 && !c.Pocket) victimPocket = true;
            else if (nPool - nPk > PoolCap / 2 && c.Pocket) victimPocket = false;
            int worst = -1;
            for (int i = 0; i < nPool; i++)
            {
                if (pool[i].Pocket != victimPocket) continue;
                if (worst < 0 || pool[worst].KeyLess(pool[i])) worst = i;
            }
            if (worst >= 0 && (c.KeyLess(pool[worst]) || c.Pocket != victimPocket)) pool[worst] = c;
        }

        void RemovePool(int i) { nPool--; pool[i] = pool[nPool]; }

        HubCand? Nominate()
        {
            Span<int> top = stackalloc int[4];
            int nTop = 0;
            bool wantPocket = HubGenFlags.Pockets > 1 && Nominated % 2 == 1;
            bool haveWanted = false;
            for (int i = 0; i < nPool; i++) if (pool[i].State == HubFree && pool[i].Pocket == wantPocket) haveWanted = true;
            if (!haveWanted) wantPocket = !wantPocket;
            for (int k = 0; k < 4; k++)
            {
                int bi = -1;
                for (int i = 0; i < nPool; i++)
                {
                    ref var p = ref pool[i];
                    if (p.State != HubFree || top[..nTop].Contains(i) || p.Pocket != wantPocket) continue;
                    if (DupLive(p.Id)) { p.State = HubInfeasible; continue; }
                    if (bi >= 0)
                    {
                        bool pt = SharedTriedSig(SigOf(p.Id)), bt = SharedTriedSig(SigOf(pool[bi].Id));
                        if (pt != bt)
                        {
                            if (!pt) bi = i;
                            continue;
                        }
                    }
                    if (bi < 0 || p.KeyLess(pool[bi])) bi = i;
                }
                if (bi < 0) break;
                top[nTop++] = bi;
            }
            for (int ti = 0; ti < nTop; ti++)
            {
                ref var p = ref pool[top[ti]];
                if (p.Lb != 0) continue;
                gen.Unpack(p.Id, buf);
                if (gen.Reach != null && !gen.Reach.Feasible(buf)) { p.State = HubInfeasible; ReachRej++; continue; }
                int lb = ps.AssignTo(buf, ps.goalDist);
                if (lb < 0) { p.State = HubInfeasible; Infeasible++; continue; }
                p.Lb = lb + 1;
            }
            int best = -1;
            for (int ti = 0; ti < nTop; ti++)
            {
                int i = top[ti];
                if (pool[i].State != HubFree) continue;
                if (best < 0 || HubCand.PickLess(pool[i], pool[best])) best = i;
            }
            HubCand picked = default;
            if (best >= 0) picked = pool[best];
            for (int i = 0; i < nPool;)
            {
                if (pool[i].State == HubInfeasible) { RemovePool(i); continue; }
                i++;
            }
            if (best >= 0)
            {
                var c = picked;
                c.Lb--;
                for (int i = 0; i < nPool; i++) if (pool[i].Id == c.Id) { RemovePool(i); break; }
                TierCount[c.Tier]++;
                Nominated++;
                return c;
            }
            int id = gen.Best;
            if (id < 0 || InTriedExact(id) || InTried(id)) return null;
            gen.Unpack(id, buf);
            if (gen.Reach != null && !gen.Reach.Feasible(buf) || ps.AssignTo(buf, ps.goalDist) < 0)
            {
                FallbackRej++;
                tried[nTried % HubTriedMax] = id; nTried++;
                return null;
            }
            var t = gen.Tuple(id);
            var cc = new HubCand { Id = id, Tier = 4, Dist = t.Dist };
            gen.Unpack(id, buf);
            cc.Lb = ps.AssignTo(buf, ps.goalDist);
            Score(id, out cc.Cut, out cc.Pinned, out cc.Trapped);
            TierCount[4]++;
            Nominated++;
            return cc;
        }

        TowardTarget TargetOf(int id)
        {
            var boxes = new int[ps.n];
            gen.Unpack(id, boxes);
            Array.Sort(boxes);
            return new TowardTarget { Boxes = boxes, Player = gen.Node(id).Player, Tail = gen.Tail(id) ?? [] };
        }

        void Commit(int i, HubCand c)
        {
            var tgt = TargetOf(c.Id);
            int other = 1 - i;
            var f = NewTowardReal(rows, lv, opt, tgt, HubRaceUnit, nodeMode, () => GenTracked + CtxBytes(other), regRel: true);
            ctxs[i] = new HubCtx { Fwd = f, Cand = c, WinExp = HubRaceUnit };
            Committed++;
        }

        // ---- diagnostics --------------------------------------------------------------------------------------------------
        public string Counters() =>
            $"nominated={Nominated} committed={Committed} reached={Reached} abandoned={Abandoned} ext={Extensions} memfull={MemFullN} exhausted={Exhausted} tail={TailFail} infeasible={Infeasible} reachrej={ReachRej} fallbackrej={FallbackRej} " +
            $"gen={gen.Expanded}/{gen.Created}/{gen.Dupes} fwd={FwdExpSum} rounds={Rounds} idle={Idle} rescans={Rescans} pool={nPool} scanned={Scanned} seen={NSeen} elig={Eligible} best_on={BestOnAll}";

        /// <summary>Go genOnly: the generator alone for maxExp expansions, then a full harvest; the fingerprint compares with the patched Go reference.</summary>
        public string GenOnlyFingerprint(long maxExp)
        {
            gen.Run(0, maxExp, () => Cancelled);
            HarvestG(double.MaxValue);
            var rows2 = new List<HubCand>();
            for (int i = 0; i < nPool; i++) rows2.Add(pool[i]);
            rows2.Sort((a, b) => a.KeyLess(b) ? -1 : b.KeyLess(a) ? 1 : 0);
            var sb = new System.Text.StringBuilder();
            sb.Append($"exp={gen.Expanded} cre={gen.Created} dup={gen.Dupes} nodes={gen.Count} cand={gen.Cand.Count} best={gen.Best} chk={gen.Checksum():x} full={(gen.Full ? 1 : 0)} reach={(gen.Reach != null ? gen.Reach.NCls : -1)} ");
            sb.Append($"pool={nPool} tierseen={string.Join(",", TierSeen)} scanned={Scanned} elig={Eligible}");
            for (int i = 0; i < Math.Min(5, rows2.Count); i++)
            {
                var c = rows2[i];
                ref var nd = ref gen.Node(c.Id);
                gen.Unpack(c.Id, buf);
                bool feas = gen.Reach == null || gen.Reach.Feasible(buf);
                sb.Append($" | {c.Id} cut={c.Cut} pin={c.Pinned} trap={c.Trapped} dist={c.Dist} tier={c.Tier} pk={(c.Pocket ? 1 : 0)} d={nd.Depth} reg={nd.Reg} trp={nd.Trap} mob={nd.Mob} bic={nd.Bic} far={nd.Far} g={nd.G} feas={(feas ? 1 : 0)}");
            }
            return sb.ToString();
        }
    }
}
