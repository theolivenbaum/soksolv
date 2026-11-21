using System.Numerics;
using System.Runtime.CompilerServices;

namespace Soksolv.Search;

public sealed partial class FullSolver
{
    /// <summary>Go cutscan.go: one low-link DFS per node over G0 (every box placed; blk != 0 = wall or box) answers "player region with box <c>lift</c> removed and a box on <c>to</c>" queries of every lifted box.
    /// Pointer kernel: all tables are pinned (POH) arrays whose references are kept in fields; <see cref="Blk"/> is owned here so callers share the pinned map.</summary>
    internal sealed unsafe class CutScan
    {
        readonly int cells; readonly int d0, d1, d2, d3;
        /// <summary>Blocked map (wall or box): written by the caller, read by the scan.</summary>
        public readonly byte[] Blk; readonly byte* blk;
        internal struct CutNode { public uint Vgen; public int Tin, Tout, Low, Par, Sub, Cid, Pad; }
        internal struct Q { public uint Gen; public fixed int Np[4]; public uint Sd; public int Pc; public uint Emit; }
        CutNode[]? cndA; Q[]? qrA; int[]? cpreA, csufA, corderA, ccsA, cceA, cstackA; byte[]? csdirA;
        CutNode* nd; Q* qr; int* cpre, csuf, corder, ccs, cce, cstack; byte* csdir;
        uint cgen, cdgen; int clift = -1; uint cbgen; int cnbn, cnbc, cball;
        readonly int[] cbn = new int[4], cbnc = new int[4], cbcm = new int[4], cbnTin = new int[4];
        int corderN, ccsN;

        public CutScan(int cells, int[] dirs)
        {
            this.cells = cells; d0 = dirs[0]; d1 = dirs[1]; d2 = dirs[2]; d3 = dirs[3];
            Blk = GC.AllocateArray<byte>(cells, pinned: true);
            blk = (byte*)Unsafe.AsPointer(ref Blk[0]);
        }
        static T* Pin<T>(int n, out T[] arr) where T : unmanaged { arr = GC.AllocateArray<T>(n, pinned: true); return (T*)Unsafe.AsPointer(ref arr[0]); }
        void CutInit()
        {
            if (nd != null) return;
            gp = Pin<G>(cells, out gA); dp = Pin<int>(4, out dpA); dp[0] = d0; dp[1] = d1; dp[2] = d2; dp[3] = d3;
            nd = Pin<CutNode>(cells, out cndA); qr = Pin<Q>(cells, out qrA);
            cpre = Pin<int>(cells, out cpreA); csuf = Pin<int>(cells, out csufA); corder = Pin<int>(cells, out corderA);
            ccs = Pin<int>(cells + 1, out ccsA); cce = Pin<int>(cells + 1, out cceA); cstack = Pin<int>(cells + 1, out cstackA); csdir = Pin<byte>(cells + 1, out csdirA);
        }
        /// <summary>Distinct free components next to the lifted box (valid after the first query of a lift).</summary>
        public int LiftNeighbourComps { get { if (cbgen != cgen) CutBoxInfo(); return cnbc; } }
        public void CutNewNode() { CutInit(); cgen++; cdgen = cgen; clift = -1; corderN = 0; ccsN = 0; }
        public void CutBegin(int b) { cgen++; clift = b; }
        /// <summary>Runs the DFS over every free cell of G0 (so every component has an id: <see cref="CompId"/>) and returns the number of components.</summary>
        public int CutAll(int[] floorCells)
        {
            byte* bk = blk; CutNode* nodes = nd; uint gen = cdgen;
            for (int i = 0; i < floorCells.Length; i++) { int c = floorCells[i]; if (bk[c] == 0 && nodes[c].Vgen != gen) CutDfs(c); }
            return ccsN;
        }
        public int CompId(int cell) => nd[cell].Cid;
        internal CutNode* Nodes => nd;
        public byte* BlkP => blk;

        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        int Dir(int di) => di == 0 ? d0 : di == 1 ? d1 : di == 2 ? d2 : d3;

        void CutDfs(int root)
        {
            uint gen = cdgen; int lift = clift;
            int comp = ccsN; int start = corderN;
            ccs[ccsN] = start; // component start (ce filled at the end)
            ccsN++;
            CutNode* nodes = nd; byte* bk = blk; int* order = corder; int* stack = cstack; byte* sdir = csdir;
            int on = corderN;
            CutNode* nr = nodes + root;
            nr->Vgen = gen; nr->Tin = on; nr->Low = on; nr->Par = -1; nr->Sub = root; nr->Cid = comp;
            order[on++] = root;
            stack[0] = root; sdir[0] = 0; int sp = 1;
            int e0 = d0, e1 = d1, e2 = d2, e3 = d3;
            while (sp > 0)
            {
                int top = sp - 1, v = stack[top];
                CutNode* nv = nodes + v;
                bool pushed = false;
                for (int di = sdir[top]; di < 4; di++)
                {
                    int w = v + (di == 0 ? e0 : di == 1 ? e1 : di == 2 ? e2 : e3);
                    if (bk[w] != 0 || w == lift) continue;
                    CutNode* nw = nodes + w;
                    if (nw->Vgen != gen)
                    {
                        int t = on;
                        nw->Vgen = gen; nw->Tin = t; nw->Low = t; nw->Par = v; nw->Sub = w; nw->Cid = comp;
                        order[on++] = w;
                        sdir[top] = (byte)(di + 1);
                        stack[sp] = w; sdir[sp] = 0; sp++;
                        pushed = true;
                        break;
                    }
                    else if (w != nv->Par && nw->Tin < nv->Low) nv->Low = nw->Tin;
                }
                if (pushed) continue;
                nv->Tout = on;
                sp = top;
                int p = nv->Par;
                if (p >= 0)
                {
                    CutNode* np = nodes + p;
                    if (nv->Low < np->Low) np->Low = nv->Low;
                    if (nv->Sub < np->Sub) np->Sub = nv->Sub;
                }
            }
            corderN = on;
            int end = on;
            cce[comp] = end;
            int m = 1 << 30;
            int* pre = cpre; int* suf = csuf;
            for (int i = start; i < end; i++) { int c = order[i]; if (c < m) m = c; pre[i] = m; }
            m = 1 << 30;
            for (int i = end - 1; i >= start; i--) { int c = order[i]; if (c < m) m = c; suf[i] = m; }
        }

        void CutBoxInfo()
        {
            cbgen = cgen; cnbn = 0; cnbc = 0;
            int b = clift, m = b;
            for (int di = 0; di < 4; di++)
            {
                int y = b + Dir(di);
                if (blk[y] != 0) continue;
                if (nd[y].Vgen != cdgen) CutDfs(y);
                int cid = nd[y].Cid;
                cbn[cnbn] = y; cbnc[cnbn] = cid; cbnTin[cnbn] = nd[y].Tin; cnbn++;
                bool dup = false;
                for (int i = 0; i < cnbc; i++) dup |= cbcm[i] == cid;
                if (!dup) { cbcm[cnbc++] = cid; m = Math.Min(m, csuf[ccs[cid]]); }
            }
            cball = m;
        }

        public int CutCompMin(int v)
        {
            if (cbgen != cgen) CutBoxInfo();
            if (v == clift) return cball;
            if (nd[v].Vgen != cdgen) CutDfs(v);
            int cid = nd[v].Cid;
            for (int i = 0; i < cnbc; i++) if (cbcm[i] == cid) return cball;
            return csuf[ccs[cid]];
        }

        /// <summary>np / sides of the player region after a box lands on <c>to</c> with the player on <c>to + dirs[di]</c>.</summary>
        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        public int CutQueryDir(int to, int di, out int sides)
        {
            Q* q = qr + to;
            if (q->Gen != cgen) CutQueryFill(to);
            sides = (int)(q->Sd >> (di << 3) & 15);
            return q->Np[di];
        }
        public (int np, int sides) CutQuery(int to, int player)
        {
            int di = player == to + d0 ? 0 : player == to + d1 ? 1 : player == to + d2 ? 2 : player == to + d3 ? 3 : throw new InvalidOperationException("cutScan.query: player is not adjacent to to");
            int np = CutQueryDir(to, di, out int sides);
            return (np, sides);
        }
        /// <summary>The filled query record of <c>to</c> for the current lift (Np per direction, Sd = 4 side bits per direction, Pc pieces, Emit = caller scratch mask reset per fill).</summary>
        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        public Q* CutQ(int to) { Q* q = qr + to; if (q->Gen != cgen) CutQueryFill(to); return q; }
        public int CutPieces(int v) { Q* q = qr + v; if (q->Gen != cgen) CutQueryFill(v); return q->Pc; }

        /// <summary>Lift-independent part of a query on <c>to</c> (valid for the whole node): the separated child subtrees of <c>to</c> in the DFS tree
        /// of G0, the piece of every neighbour, and the minimum cell of the parent side. Only the cells adjacent to the lifted box (opened by the
        /// lift) differ between lifts, so one record serves every box of the node.</summary>
        internal struct G { public uint Gen; public int Tin, Tout, Cx, Ns, ParentMin; public uint Pj; public fixed int Lo[4]; public fixed int Hi[4]; public fixed int Sub[4]; }
        G[]? gA; G* gp; int* dp; int[]? dpA;
        const int Blocked = 0xFE;   // Pj byte: neighbour blocked in G0 (wall, box); 0xFF = parent side; 0..3 = separated child

        G* FillG(int to)
        {
            G* g = gp + to;
            byte* bk = blk; CutNode* nodes = nd; int lift = clift;
            CutNode* nt = nodes + to;
            if (nt->Vgen != cdgen) CutDfs(to);
            g->Gen = cdgen;
            int tinTo = nt->Tin, toutTo = nt->Tout, cx = nt->Cid;
            bool isRoot = nt->Par < 0;
            g->Tin = tinTo; g->Tout = toutTo; g->Cx = cx;
            int ns = 0, pm = 1 << 30;
            for (int di = 0; di < 4; di++)
            {
                int u = to + dp[di];
                if (bk[u] != 0 || u == lift) continue;
                CutNode* nu = nodes + u;
                if (nu->Par == to && nu->Vgen == cdgen)
                {
                    if (nu->Low < tinTo && nu->Sub < pm) pm = nu->Sub;
                    if (isRoot || nu->Low >= tinTo) { g->Lo[ns] = nu->Tin; g->Hi[ns] = nu->Tout; g->Sub[ns] = nu->Sub; ns++; }
                }
            }
            g->Ns = ns;
            if (tinTo > ccs[cx]) { int v = cpre[tinTo - 1]; if (v < pm) pm = v; }
            if (toutTo < cce[cx]) { int v = csuf[toutTo]; if (v < pm) pm = v; }
            g->ParentMin = pm;
            uint pj = 0;
            for (int di = 0; di < 4; di++)
            {
                int w = to + dp[di]; uint v;
                if (bk[w] != 0 || w == lift) v = (uint)Blocked;
                else
                {
                    int tw = nodes[w].Tin; v = 0xFF;
                    if (tw > tinTo && tw < toutTo)
                        for (int i = 0; i < ns; i++)
                            if (g->Lo[i] <= tw && tw < g->Hi[i]) { v = (uint)i; break; }
                }
                pj |= v << (di << 3);
            }
            g->Pj = pj;
            return g;
        }

        [SkipLocalsInit]
        void CutQueryFill(int to)
        {
            Q* q = qr + to;
            q->Gen = cgen; q->Emit = 0;
            if (cbgen != cgen) CutBoxInfo();
            int lift = clift;
            int* lab = stackalloc int[4];
            int fr = 0;
            byte* bk = blk; CutNode* nodes = nd; int* dr = dp;
            uint sdAll = 0;
            if (to == lift)
            {
                for (int di = 0; di < 4; di++)
                {
                    int w = to + dr[di];
                    if (bk[w] != 0) continue;
                    fr |= 1 << di; lab[di] = nodes[w].Cid; // ensured by boxInfo
                }
                int npc = 0, asg = 0;
                for (int di = 0; di < 4; di++)
                {
                    if (((fr & ~asg) >> di & 1) == 0) continue;
                    int li = lab[di], grp = 1 << di;
                    for (int dj = di + 1; dj < 4; dj++) if ((fr >> dj & 1) != 0 && lab[dj] == li) grp |= 1 << dj;
                    asg |= grp; npc++;
                    int npv = csuf[ccs[li]];
                    for (int m = grp; m != 0; m &= m - 1)
                    {
                        int dj = BitOperations.TrailingZeroCount(m);
                        sdAll |= (uint)grp << (dj << 3);
                        q->Np[dj] = npv;
                    }
                }
                q->Sd = sdAll; q->Pc = npc;
                return;
            }
            G* g = gp + to;
            if (g->Gen != cdgen) g = FillG(to);
            int cx = g->Cx, tinTo = g->Tin, toutTo = g->Tout, ns = g->Ns;
            int inM = 0; bool parentInM = false;
            int mm = lift;
            for (int i = 0; i < cnbn; i++)
            {
                int y = cbn[i];
                if (y == to) continue;
                if (cbnc[i] != cx) { int cm = csuf[ccs[cbnc[i]]]; if (cm < mm) mm = cm; continue; }
                int tw = cbnTin[i], j = -1;
                if (tw > tinTo && tw < toutTo)
                    for (int k = 0; k < ns; k++)
                        if (g->Lo[k] <= tw && tw < g->Hi[k]) { j = k; break; }
                if (j >= 0) inM |= 1 << j; else parentInM = true;
            }
            uint pj = g->Pj;
            int l0 = Label(to + d0, lift, (int)(pj & 0xFF), inM, parentInM, -100);
            int l1 = Label(to + d1, lift, (int)(pj >> 8 & 0xFF), inM, parentInM, -101);
            int l2 = Label(to + d2, lift, (int)(pj >> 16 & 0xFF), inM, parentInM, -102);
            int l3 = Label(to + d3, lift, (int)(pj >> 24), inM, parentInM, -103);
            if (parentInM && g->ParentMin < mm) mm = g->ParentMin;
            for (int i = 0; i < ns; i++) if ((inM >> i & 1) != 0 && g->Sub[i] < mm) mm = g->Sub[i];
            int parentMin = g->ParentMin;
            // labels of non-free directions are distinct negative sentinels below -2, so equal labels = same piece
            uint m0 = 1u | (l1 == l0 ? 2u : 0) | (l2 == l0 ? 4u : 0) | (l3 == l0 ? 8u : 0);
            uint m1 = 2u | (l0 == l1 ? 1u : 0) | (l2 == l1 ? 4u : 0) | (l3 == l1 ? 8u : 0);
            uint m2 = 4u | (l0 == l2 ? 1u : 0) | (l1 == l2 ? 2u : 0) | (l3 == l2 ? 8u : 0);
            uint m3 = 8u | (l0 == l3 ? 1u : 0) | (l1 == l3 ? 2u : 0) | (l2 == l3 ? 4u : 0);
            q->Sd = m0 | m1 << 8 | m2 << 16 | m3 << 24;
            q->Np[0] = NpOf(l0, mm, g, parentMin); q->Np[1] = NpOf(l1, mm, g, parentMin); q->Np[2] = NpOf(l2, mm, g, parentMin); q->Np[3] = NpOf(l3, mm, g, parentMin);
            // distinct free pieces = free directions that are the first of their piece
            int pc = (l0 >= -2 ? 1 : 0) + (l1 >= -2 && l1 != l0 ? 1 : 0) + (l2 >= -2 && l2 != l0 && l2 != l1 ? 1 : 0) + (l3 >= -2 && l3 != l0 && l3 != l1 && l3 != l2 ? 1 : 0);
            q->Pc = pc;
        }

        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        static int Label(int w, int lift, int pv, int inM, bool parentInM, int sentinel)
        {
            if (w == lift) return -2;                         // the lifted cell is free: merges the pieces it touches
            if (pv == Blocked) return sentinel;
            if (pv == 0xFF) return parentInM ? -2 : -1;
            return (inM >> pv & 1) != 0 ? -2 : pv;
        }
        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        static int NpOf(int l, int mm, G* g, int parentMin) => l == -2 ? mm : l >= 0 ? g->Sub[l] : parentMin;
    }
}
