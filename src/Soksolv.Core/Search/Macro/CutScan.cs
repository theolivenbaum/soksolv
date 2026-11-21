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
        struct CutNode { public uint Vgen; public int Tin, Tout, Low, Par, Sub, Cid, Pad; }
        internal struct Q { public uint Gen; public fixed int Np[4]; public uint Sd; public int Pc; public uint Emit; }
        CutNode[]? cndA; Q[]? qrA; int[]? cpreA, csufA, corderA, ccsA, cceA, cstackA; byte[]? csdirA;
        CutNode* nd; Q* qr; int* cpre, csuf, corder, ccs, cce, cstack; byte* csdir;
        uint cgen, cdgen; int clift = -1; uint cbgen; int cnbn, cnbc, cball;
        readonly int[] cbn = new int[4], cbnc = new int[4], cbcm = new int[4];
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
            nd = Pin<CutNode>(cells, out cndA); qr = Pin<Q>(cells, out qrA);
            cpre = Pin<int>(cells, out cpreA); csuf = Pin<int>(cells, out csufA); corder = Pin<int>(cells, out corderA);
            ccs = Pin<int>(cells + 1, out ccsA); cce = Pin<int>(cells + 1, out cceA); cstack = Pin<int>(cells + 1, out cstackA); csdir = Pin<byte>(cells + 1, out csdirA);
        }
        /// <summary>Distinct free components next to the lifted box (valid after the first query of a lift).</summary>
        public int LiftNeighbourComps { get { if (cbgen != cgen) CutBoxInfo(); return cnbc; } }
        public void CutNewNode() { CutInit(); cgen++; cdgen = cgen; clift = -1; corderN = 0; ccsN = 0; }
        public void CutBegin(int b) { cgen++; clift = b; }

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
                cbn[cnbn] = y; cbnc[cnbn] = cid; cnbn++;
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

        [MethodImpl(MethodImplOptions.AggressiveInlining)]
        static int Piece(int tw, int tinTo, int toutTo, int nc, int* clo, int* chi, int* csep)
        {
            if (tw > tinTo && tw < toutTo)
                for (int i = 0; i < nc; i++)
                    if (clo[i] <= tw && tw < chi[i]) return csep[i] != 0 ? i : -1;
            return -1;
        }

        [SkipLocalsInit]
        void CutQueryFill(int to)
        {
            Q* q = qr + to;
            q->Gen = cgen; q->Emit = 0;
            if (cbgen != cgen) CutBoxInfo();
            int lift = clift;
            int* lab = stackalloc int[4]; int* cu = stackalloc int[4]; int* clo = stackalloc int[4]; int* chi = stackalloc int[4];
            int* csep = stackalloc int[4]; int* clow = stackalloc int[4]; int* csub = stackalloc int[4];
            int fr = 0;
            byte* bk = blk; CutNode* nodes = nd;
            uint sdAll = 0;
            if (to == lift)
            {
                for (int di = 0; di < 4; di++)
                {
                    int w = to + Dir(di);
                    if (bk[w] != 0) continue;
                    fr |= 1 << di; lab[di] = nodes[w].Cid; // ensured by boxInfo
                }
                int npc = 0;
                for (int di = 0; di < 4; di++)
                {
                    if ((fr >> di & 1) == 0) continue;
                    int sides0 = 0; bool first0 = true;
                    for (int dj = 0; dj < 4; dj++)
                        if ((fr >> dj & 1) != 0 && lab[dj] == lab[di]) { sides0 |= 1 << dj; first0 = first0 && dj >= di; }
                    if (first0) npc++;
                    sdAll |= (uint)sides0 << (di << 3);
                    q->Np[di] = csuf[ccs[lab[di]]];
                }
                q->Sd = sdAll; q->Pc = npc;
                return;
            }
            CutNode* nt = nodes + to;
            if (nt->Vgen != cdgen) CutDfs(to);
            uint gen = cdgen;
            int tinTo = nt->Tin, toutTo = nt->Tout, cx = nt->Cid;
            bool isRoot = nt->Par < 0;
            int nc = 0;
            int* wc = stackalloc int[4];
            for (int di = 0; di < 4; di++)
            {
                int u = to + Dir(di); wc[di] = u;
                if (bk[u] != 0 || u == lift) continue;
                CutNode* nu = nodes + u;
                if (nu->Par == to && nu->Vgen == gen)
                {
                    cu[nc] = u; clo[nc] = nu->Tin; chi[nc] = nu->Tout; clow[nc] = nu->Low; csub[nc] = nu->Sub;
                    csep[nc] = isRoot || nu->Low >= tinTo ? 1 : 0;
                    nc++;
                }
            }
            int inM = 0; bool parentInM = false;
            int mm = lift;
            for (int i = 0; i < cnbn; i++)
            {
                int y = cbn[i];
                if (y == to) continue;
                if (cbnc[i] != cx) { int cm = csuf[ccs[cbnc[i]]]; if (cm < mm) mm = cm; continue; }
                int j = Piece(nodes[y].Tin, tinTo, toutTo, nc, clo, chi, csep);
                if (j >= 0) inM |= 1 << j; else parentInM = true;
            }
            const int merged = -2;
            bool needParent = parentInM;
            for (int di = 0; di < 4; di++)
            {
                int w = wc[di];
                if (w == lift) { fr |= 1 << di; lab[di] = merged; continue; }
                if (bk[w] != 0) continue;
                fr |= 1 << di;
                int j = Piece(nodes[w].Tin, tinTo, toutTo, nc, clo, chi, csep);
                if (j >= 0 ? (inM >> j & 1) != 0 : parentInM) lab[di] = merged;
                else { lab[di] = j; needParent |= j < 0; }
            }
            int parentMin = 0;
            if (needParent)
            {
                int m = 1 << 30;
                if (tinTo > ccs[cx]) m = cpre[tinTo - 1];
                if (toutTo < cce[cx]) { int sv = csuf[toutTo]; if (sv < m) m = sv; }
                for (int i = 0; i < nc; i++) if (clow[i] < tinTo && csub[i] < m) m = csub[i];
                parentMin = m;
            }
            if (parentInM && parentMin < mm) mm = parentMin;
            for (int i = 0; i < nc; i++) if ((inM >> i & 1) != 0 && csub[i] < mm) mm = csub[i];
            int np2 = 0;
            for (int di = 0; di < 4; di++)
            {
                if ((fr >> di & 1) == 0) continue;
                int sides = 0; bool first = true; int li = lab[di];
                for (int dj = 0; dj < 4; dj++)
                    if ((fr >> dj & 1) != 0 && lab[dj] == li) { sides |= 1 << dj; first = first && dj >= di; }
                if (first) np2++;
                sdAll |= (uint)sides << (di << 3);
                q->Np[di] = li == merged ? mm : li >= 0 ? csub[li] : parentMin;
            }
            q->Sd = sdAll; q->Pc = np2;
        }
    }
}
