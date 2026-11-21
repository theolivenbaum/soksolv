using System.Numerics;
using Soksolv.Search.Macro;

namespace Soksolv.Search;

/// <summary>Stage 3 of the macro lane: the sink packing plan (Go packing_plan.go; cutscan replaced by exact floods).</summary>
public sealed unsafe partial class FullSolver
{
    // ---- plan (Go packingPlan) ---------------------------------------------------------------------------------------------------
    sealed class PackingPlan
    {
        public int[][] Required = [];          // per phase (best first, root last as built by the planner chain walk)
        public int[][] Check = [];
        public int[] Players = [];             // per phase: normalized player cell
        public PMove[] Moves = [];             // per phase: witnessed pull move from the next phase to this one
        public PackingDag? Dag; public int CausalVisits;
        public ulong[] In = [];                // per phase bitset of required cells
        public int InW;
        public bool Complete; public int Expanded, Parkings, Remaining;
        public FestPlan? Fest; public bool FestPacked; // plan4: Festival-prefix packed feature (Go plan.fest / plan.festPacked)
        public ulong[] OccBits = []; public int[] Miss = []; public int[] Cand = []; public int CandN;   // phases (best first) with at most one missing cell
        public int[] OopCache = []; public int FestMiss;

        public void Prepare()
        {
            int maxc = 0;
            foreach (var ph in Required) foreach (int c in ph) maxc = Math.Max(maxc, c);
            InW = (maxc >> 6) + 1;
            In = new ulong[Required.Length * InW];
            for (int i = 0; i < Required.Length; i++) foreach (int c in Required[i]) In[i * InW + (c >> 6)] |= 1UL << (c & 63);
            Check = new int[Required.Length][];
            for (int i = 0; i < Required.Length; i++)
            {
                var prev = i > 0 ? Required[i - 1] : [];
                var fresh = new List<int>(); var old = new List<int>();
                foreach (int c in Required[i]) (Array.IndexOf(prev, c) >= 0 ? old : fresh).Add(c);
                fresh.AddRange(old);
                Check[i] = fresh.ToArray();
            }
        }

        public (int phase, int packed) Status(bool[] occ)
        {
            if (Check.Length == 0 && Required.Length > 0) Prepare();
            for (int i = Required.Length - 1; i >= 0; i--)
            {
                bool ok = true;
                foreach (int c in Check[i]) if (!occ[c]) { ok = false; break; }
                if (ok) return (i, Required[i].Length);
            }
            int n = 0;
            if (Required.Length > 0) foreach (int c in Required[0]) if (occ[c]) n++;
            return (-1, n);
        }

        public void NodeSetup(ReadOnlySpan<int> boxes)
        {
            if (In.Length == 0 && Required.Length > 0) Prepare();
            if (OccBits.Length != InW) OccBits = new ulong[InW];
            Array.Clear(OccBits);
            foreach (int b in boxes) { int w = b >> 6; if (w < InW) OccBits[w] |= 1UL << (b & 63); }
            if (Miss.Length != Required.Length) Miss = new int[Required.Length];
            for (int i = 0; i < Required.Length; i++)
            {
                int m = 0;
                for (int w = 0; w < InW; w++) m += BitOperations.PopCount(In[i * InW + w] & ~OccBits[w]);
                Miss[i] = m;
            }
            if (Cand.Length != Required.Length) Cand = new int[Required.Length];
            CandN = 0;
            for (int i = Required.Length - 1; i >= 0; i--) if (Miss[i] <= 1) Cand[CandN++] = i;
            if (OopCache.Length != Required.Length + 1) OopCache = new int[Required.Length + 1];
            Array.Fill(OopCache, -1);
        }

        int InBit(int i, int c) { int w = c >> 6; return w < InW ? (int)(In[i * InW + w] >> (c & 63) & 1) : 0; }

        public (int phase, int packed) ChildStatus(int b, int t)
        {
            for (int ci = 0; ci < CandN; ci++)
            {
                int i = Cand[ci];
                if (Miss[i] + InBit(i, b) - InBit(i, t) == 0) return (i, Required[i].Length);
            }
            if (Required.Length > 0) return (-1, Required[0].Length - (Miss[0] + InBit(0, b) - InBit(0, t)));
            return (-1, 0);
        }

        public bool Placed(int cell, int phase)
        {
            if (phase < 0) { if (Required.Length == 0) return false; phase = 0; }
            if (In.Length == 0) Prepare();
            int w = cell >> 6;
            return w < InW && (In[phase * InW + w] >> (cell & 63) & 1) != 0;
        }

        /// <summary>placed bit of cell in phase's row; false past the row (Go placedRow).</summary>
        public bool PlacedRow(int phase, int cell)
        {
            if (phase < 0) { if (Required.Length == 0) return false; phase = 0; }
            if (In.Length == 0) Prepare();
            int w = cell >> 6;
            return w < InW && (In[phase * InW + w] >> (cell & 63) & 1) != 0;
        }
    }

    PackingPlan? mPlan;
    int basinSink = -1;

    // ---- planner ---------------------------------------------------------------------------------------------------------------------
    struct PMove { public int From, To, Player; public bool Remove; public int Weight, Score; }
    sealed class PNode { public int[] Boxes = []; public int Player, Parent; public PMove Move; public int Weight; public PMove[] Moves = []; public int Next, Score; }
    struct PEntry { public int Node, Weight, Score; }

    sealed class PHeap
    {
        PEntry[] a = new PEntry[8]; int n; public int Count => n;
        static bool Less(in PEntry x, in PEntry y) => x.Weight != y.Weight ? x.Weight < y.Weight : x.Score != y.Score ? x.Score > y.Score : x.Node < y.Node;
        public void Push(PEntry e)
        {
            if (n == a.Length) Array.Resize(ref a, n * 2);
            int i = n++;
            while (i > 0) { int p = (i - 1) >> 1; if (!Less(e, a[p])) break; a[i] = a[p]; i = p; }
            a[i] = e;
        }
        public PEntry Pop()
        {
            var top = a[0]; var x = a[--n]; int i = 0;
            while (true)
            {
                int l = 2 * i + 1; if (l >= n) break;
                int m = l; if (l + 1 < n && Less(a[l + 1], a[l])) m = l + 1;
                if (!Less(a[m], x)) break;
                a[i] = a[m]; i = m;
            }
            if (n > 0) a[i] = x;
            return top;
        }
    }

    PackingPlan? MBuildPackingPlan(int maxNodes, long deadlineMs)
    {
        if (basinInside == null || basinSink < 0) return null;
        var sink = new bool[cells]; var start = new bool[cells]; var pushable = new bool[cells]; var distance = new int[cells];
        foreach (int b in lv.Boxes) start[b] = true;
        var reach = new bool[cells]; var fq = new List<int>();
        foreach (int b in lv.Boxes) { reach[b] = true; fq.Add(b); }
        for (int i = 0; i < fq.Count; i++)
        {
            int c = fq[i];
            foreach (int d in dirs)
            {
                int to = c + d;
                if (!wall[c - d] && !wall[to] && !reach[to] && !goal[to]) { reach[to] = true; fq.Add(to); }
            }
        }
        var q = new List<int>();
        foreach (int c in rmRooms[basinSink])
        {
            if (!reach[c]) continue; // RealSink
            sink[c] = true; pushable[c] = true; q.Add(c);
        }
        for (int i = 0; i < q.Count; i++)
            foreach (int d in dirs)
            {
                int to = q[i] + d, support = q[i] - d;
                if (!wall[to] && !wall[support] && !pushable[to]) { pushable[to] = true; q.Add(to); }
            }
        Array.Fill(distance, -1); q.Clear();
        foreach (int g in goals) { distance[g] = 0; q.Add(g); }
        for (int i = 0; i < q.Count; i++)
            foreach (int d in dirs)
            {
                int x = q[i] + d;
                if (!wall[x] && distance[x] < 0) { distance[x] = distance[q[i]] + 1; q.Add(x); }
            }
        return new Planner(this, sink, start, pushable, distance, maxNodes, deadlineMs).Run();
    }

    sealed class Planner
    {
        readonly FullSolver s; readonly bool[] sink, start, pushable; readonly int[] distance;
        readonly bool[] occ; readonly uint[] mark; uint stamp; readonly List<int> queue = new();
        readonly List<PNode> nodes = new(); readonly Dictionary<string, int> seen = new();
        readonly Dictionary<(int, int), int> cellIds = new(); readonly List<PHeap> queues = new(); readonly List<int> nodeCell = new();
        int next, best, expanded, moveCount; readonly int maxNodes; readonly long deadlineMs;
        readonly bool[] covered; readonly int[] floor;
        // pull-move scratch: player's parent region, per-box seen pull states, distinct (to, np) ends
        readonly uint[] pgen; uint pg; readonly uint[] pseen; uint pgenBox;

        public Planner(FullSolver s, bool[] sink, bool[] start, bool[] pushable, int[] distance, int maxNodes, long deadlineMs)
        {
            this.s = s; this.sink = sink; this.start = start; this.pushable = pushable; this.distance = distance; this.maxNodes = maxNodes; this.deadlineMs = deadlineMs;
            occ = new bool[s.cells]; mark = new uint[s.cells]; covered = new bool[s.cells]; pgen = new uint[s.cells]; pseen = new uint[4 * s.cells];
            floor = s.FloorCells();
        }

        bool Stopped() => s.opt.Stop || (s.mNodeLimit == 0 && s.clock.ElapsedMilliseconds > deadlineMs) || nodes.Count >= maxNodes || moveCount >= 200000;

        (int norm, uint st) Flood(int player)
        {
            uint st = ++stamp; queue.Clear(); queue.Add(player); mark[player] = st; int norm = player;
            for (int i = 0; i < queue.Count; i++)
            {
                norm = Math.Min(norm, queue[i]);
                foreach (int d in s.dirs)
                {
                    int x = queue[i] + d;
                    if (!s.wall[x] && !occ[x] && mark[x] != st) { mark[x] = st; queue.Add(x); }
                }
            }
            return (norm, st);
        }

        int Regions()
        {
            uint st = ++stamp; int n = 0;
            int[] q = regQ ??= new int[s.cells];
            foreach (int c in floor)
            {
                if (occ[c] || mark[c] == st) continue;
                n++; mark[c] = st;
                int tail = 0; q[tail++] = c;
                for (int i = 0; i < tail; i++)
                    foreach (int d in s.dirs) { int y = q[i] + d; if (mark[y] != st && !s.wall[y] && !occ[y]) { mark[y] = st; q[tail++] = y; } }
            }
            return n;
        }
        int[]? regQ;

        // cut scan over G0 (every box placed): queries of "player region with box b lifted and a box on `to`" without a flood per pull state
        CutScan? cut; byte[]? cblk;
        readonly List<PMove> pmMoves = new(), pmRemovals = new(); readonly HashSet<(int, int)> pmEnds = new(); readonly List<int> pmQ = new(), pmRb = new();
        void SetBlk(int c, bool v) { occ[c] = v; cblk![c] = (byte)(v || s.wall[c] ? 1 : 0); }

        static void StableSort(List<PMove> l, Comparison<PMove> cmp)
        {
            for (int i = 1; i < l.Count; i++)
            {
                var x = l[i]; int j = i - 1;
                while (j >= 0 && cmp(l[j], x) > 0) { l[j + 1] = l[j]; j--; }
                l[j + 1] = x;
            }
        }

        PMove[] PullMoves(int[] boxes, int player)
        {
            var dirs = s.dirs;
            if (cut == null)
            {
                cblk = new byte[s.cells]; cut = new CutScan(s.cells, dirs, cblk);
            }
            for (int c = 0; c < cblk!.Length; c++) cblk[c] = (byte)(s.wall[c] ? 1 : 0);
            foreach (int bx in boxes) cblk[bx] = 1;
            var (_, parentStamp) = Flood(player);
            pg++;
            foreach (int c in queue) if (mark[c] == parentStamp) pgen[c] = pg;
            var moves = pmMoves; moves.Clear(); var removals = pmRemovals; removals.Clear();
            var ends = pmEnds; var q = pmQ; var rbOf = pmRb; rbOf.Clear();
            int baseR = Regions();
            cut.CutNewNode();
            foreach (int b in boxes)
            {
                if (Stopped()) break;
                if (sink[b])
                {
                    SetBlk(b, false); var (np0, _) = Flood(player); SetBlk(b, true);
                    removals.Add(new PMove { From = b, To = b, Player = np0, Remove = true });
                    continue;
                }
                ends.Clear(); pgenBox++;
                uint g = pgenBox;
                q.Clear();
                for (int di = 0; di < 4; di++)
                    if (pgen[b + dirs[di]] == pg) { int k = b * 4 + di; q.Add(k); pseen[k] = g; }
                cut.CutBegin(b);
                SetBlk(b, false);
                int rb = -1;
                for (int qi = 0; qi < q.Count; qi++)
                {
                    if ((qi & 31) == 0 && Stopped()) break;
                    int k = q[qi];
                    int c = k / 4, d = dirs[k % 4];
                    int to = c + d, pl = c + 2 * d;
                    if (s.wall[to] || occ[to] || s.wall[pl] || occ[pl]) continue;
                    // player's region with the box on `to`: flood from pl with `to` blocked (cut scan answer)
                    var (np, sides) = cut.CutQuery(to, pl);
                    for (int di = 0; di < 4; di++)
                    {
                        int k2 = to * 4 + di;
                        if ((sides & (1 << di)) != 0 && pseen[k2] != g) { pseen[k2] = g; q.Add(k2); }
                    }
                    bool remove = sink[to];
                    if (remove) np = cut.CutCompMin(to);
                    if (!ends.Add((to, np))) continue;
                    var m = new PMove { From = b, To = to, Player = np, Remove = remove, Weight = 1, Score = distance[to] - distance[b] };
                    if (remove) { m.Weight = 0; m.Score = -distance[b]; removals.Add(m); }
                    else
                    {
                        moves.Add(m);
                        if (rb < 0) rb = baseR + 1 - cut.LiftNeighbourComps;
                        rbOf.Add(rb);
                    }
                }
                SetBlk(b, true);
            }
            if (removals.Count > 0)
            {
                StableSort(removals, (a, b2) => b2.Score - a.Score);
                return removals.ToArray();
            }
            int parentRegions = baseR;
            int advisor = -1; int bestScore = int.MinValue;
            for (int i = 0; i < moves.Count; i++)
            {
                var m = moves[i];
                if (!s.goal[m.From] || !(pushable[m.To] || start[m.To])) continue;
                cut.CutBegin(m.From);
                SetBlk(m.From, false);
                int regions = rbOf[i] - 1 + cut.CutPieces(m.To);
                if (FullSolver.CheckCut) { SetBlk(m.To, true); int f = Regions(); SetBlk(m.To, false); if (f != regions) throw new InvalidOperationException($"v1 planner regions: scan {regions} != flood {f}"); }
                SetBlk(m.From, true);
                if (regions <= parentRegions && m.Score > bestScore) { advisor = i; bestScore = m.Score; }
            }
            if (advisor >= 0) { var m = moves[advisor]; m.Weight = 0; moves[advisor] = m; }
            StableSort(moves, (a, b2) => a.Weight != b2.Weight ? a.Weight - b2.Weight : b2.Score - a.Score);
            return moves.ToArray();
        }

        static string Key(int[] boxes, int player)
        {
            var c = new char[boxes.Length + 1]; c[0] = (char)player;
            for (int i = 0; i < boxes.Length; i++) c[i + 1] = (char)boxes[i];
            return new string(c);
        }

        int Add(int[] boxes, int player, int parent, PMove move, int weight)
        {
            string key = Key(boxes, player);
            if (seen.ContainsKey(key)) return -1;
            int id = nodes.Count;
            seen[key] = id;
            int score = 0, goalsN = 0;
            foreach (int b in boxes) { score += distance[b]; if (s.goal[b]) goalsN++; }
            nodes.Add(new PNode { Boxes = (int[])boxes.Clone(), Player = player, Parent = parent, Move = move, Weight = weight, Score = score });
            var bn = nodes[best];
            if (boxes.Length < bn.Boxes.Length || boxes.Length == bn.Boxes.Length && score > bn.Score) best = id;
            var cell = (boxes.Length, goalsN);
            if (!cellIds.TryGetValue(cell, out int ci)) { ci = queues.Count; cellIds[cell] = ci; queues.Add(new PHeap()); }
            nodeCell.Add(ci);
            if (boxes.Length > 0 && !Stopped())
            {
                Array.Clear(occ);
                foreach (int b in boxes) occ[b] = true;
                var moves = PullMoves(boxes, player);
                nodes[id].Moves = moves;
                moveCount += moves.Length;
                Enqueue(id);
            }
            return id;
        }

        void Enqueue(int id)
        {
            var n = nodes[id];
            if (n.Next >= n.Moves.Length) return;
            var m = n.Moves[n.Next];
            queues[nodeCell[id]].Push(new PEntry { Node = id, Weight = n.Weight + m.Weight, Score = n.Score + m.Score });
        }

        public PackingPlan? Run()
        {
            Array.Clear(occ);
            foreach (int g in s.goals) occ[g] = true;
            var roots = new List<int>();
            foreach (int c in floor)
                if (!occ[c] && !covered[c])
                {
                    var (norm, st) = Flood(c);
                    roots.Add(norm);
                    foreach (int x in queue) if (mark[x] == st) covered[x] = true;
                }
            foreach (int r in roots)
            {
                if (Stopped()) break;
                Add(s.goals, r, -1, default, 0);
            }
            if (nodes.Count == 0) return null;
            while (!Stopped() && nodes[best].Boxes.Length > 0)
            {
                int found = -1;
                for (int k = 0, nq = queues.Count; k < nq; k++)
                {
                    int ci = next; next = (next + 1) % queues.Count;
                    if (queues[ci].Count > 0) { found = ci; break; }
                }
                if (found < 0) break;
                var e = queues[found].Pop();
                var n = nodes[e.Node];
                var m = n.Moves[n.Next];
                n.Next++;
                Enqueue(e.Node);
                var boxes = (int[])n.Boxes.Clone();
                int i = Array.BinarySearch(boxes, m.From);
                if (m.Remove) boxes = boxes.Where((_, j) => j != i).ToArray();
                else { boxes[i] = m.To; Array.Sort(boxes); }
                int weight = n.Weight + m.Weight;
                expanded++;
                Add(boxes, m.Player, e.Node, m, weight);
            }
            var bn = nodes[best];
            var res = new PackingPlan { Complete = bn.Boxes.Length == 0, Expanded = expanded, Remaining = bn.Boxes.Length };
            var req = new List<int[]>(); var plr = new List<int>(); var mvs = new List<PMove>();
            for (int id = best; id >= 0; id = nodes[id].Parent)
            {
                var n = nodes[id];
                req.Add((int[])n.Boxes.Clone()); plr.Add(n.Player); mvs.Add(n.Move);
                if (n.Parent >= 0 && !n.Move.Remove) res.Parkings++;
            }
            res.Required = req.ToArray(); res.Players = plr.ToArray(); res.Moves = mvs.ToArray();
            return res;
        }
    }
}
