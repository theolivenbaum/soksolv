namespace Soksolv.Search;

public sealed partial class FullSolver
{
    /// <summary>Go cutscan.go: one low-link DFS per node over G0 (every box placed; blk != 0 = wall or box) answers "player region with box <c>lift</c> removed and a box on <c>to</c>" queries of every lifted box.</summary>
    internal sealed class CutScan
    {
        readonly int cells; readonly int[] dirs; readonly byte[] blk;
        public CutScan(int cells, int[] dirs, byte[] blk) { this.cells = cells; this.dirs = dirs; this.blk = blk; }
        // ---- cutscan.go port: one low-link DFS per planner node over G0 (every box placed) answers the queries of every lifted box -------------
        struct CutNode { public uint Vgen; public int Tin, Tout, Low, Par, Sub, Cid; }
        CutNode[] cnd = []; uint cgen, cdgen; int clift = -1; uint cbgen; int cnbn, cnbc, cball;
        readonly int[] cbn = new int[4], cbnc = new int[4], cbcm = new int[4], cuF = new int[4], cloF = new int[4], chiF = new int[4]; readonly bool[] csepF = new bool[4];
        byte[] cqpc = []; uint[] cqgen = []; int[] cqnp = []; byte[] cqsd = []; int[] cpre = [], csuf = [], corder = [], ccs = [], cce = [], cstack = []; byte[] csdir = [];
        int corderN, ccsN;

        void CutInit()
        {
            if (cnd.Length != 0) return;
            cnd = new CutNode[cells]; cqpc = new byte[cells]; cqgen = new uint[cells]; cqnp = new int[4 * cells]; cqsd = new byte[4 * cells];
            cpre = new int[cells]; csuf = new int[cells]; corder = new int[cells]; ccs = new int[cells + 1]; cce = new int[cells + 1]; cstack = new int[cells + 1]; csdir = new byte[cells + 1];
        }
        /// <summary>Distinct free components next to the lifted box (valid after the first query of a lift).</summary>
        public int LiftNeighbourComps { get { if (cbgen != cgen) CutBoxInfo(); return cnbc; } }
        public void CutNewNode() { CutInit(); cgen++; cdgen = cgen; clift = -1; corderN = 0; ccsN = 0; }
        public void CutBegin(int b) { cgen++; clift = b; }

        void CutDfs(int root)
        {
            uint gen = cdgen; int lift = clift;
            int comp = ccsN; int start = corderN;
            ccs[ccsN] = start; // component start (ce filled at the end)
            ccsN++;
            cnd[root] = new CutNode { Vgen = gen, Tin = corderN, Low = corderN, Par = -1, Sub = root, Cid = comp };
            corder[corderN++] = root;
            cstack[0] = root; csdir[0] = 0; int sp = 1;
            while (sp > 0)
            {
                int top = sp - 1, v = cstack[top];
                ref var nv = ref cnd[v];
                bool pushed = false;
                for (int di = csdir[top]; di < 4; di++)
                {
                    int w = v + dirs[di];
                    if (blk[w] != 0 || w == lift) continue;
                    ref var nw = ref cnd[w];
                    if (nw.Vgen != gen)
                    {
                        int t = corderN;
                        nw = new CutNode { Vgen = gen, Tin = t, Low = t, Par = v, Sub = w, Cid = comp };
                        corder[corderN++] = w;
                        csdir[top] = (byte)(di + 1);
                        cstack[sp] = w; csdir[sp] = 0; sp++;
                        pushed = true;
                        break;
                    }
                    else if (w != nv.Par && nw.Tin < nv.Low) nv.Low = nw.Tin;
                }
                if (pushed) continue;
                nv.Tout = corderN;
                sp = top;
                int p = nv.Par;
                if (p >= 0)
                {
                    ref var np = ref cnd[p];
                    np.Low = Math.Min(np.Low, nv.Low);
                    np.Sub = Math.Min(np.Sub, nv.Sub);
                }
            }
            int end = corderN;
            cce[comp] = end;
            int m = 1 << 30;
            for (int i = start; i < end; i++) { m = Math.Min(m, corder[i]); cpre[i] = m; }
            m = 1 << 30;
            for (int i = end - 1; i >= start; i--) { m = Math.Min(m, corder[i]); csuf[i] = m; }
        }

        void CutEnsure(int v) { if (cnd[v].Vgen != cdgen) CutDfs(v); }

        void CutBoxInfo()
        {
            cbgen = cgen; cnbn = 0; cnbc = 0;
            int b = clift, m = b;
            foreach (int d in dirs)
            {
                int y = b + d;
                if (blk[y] != 0) continue;
                CutEnsure(y);
                int cid = cnd[y].Cid;
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
            CutEnsure(v);
            int cid = cnd[v].Cid;
            for (int i = 0; i < cnbc; i++) if (cbcm[i] == cid) return cball;
            return csuf[ccs[cid]];
        }

        public (int np, int sides) CutQuery(int to, int player)
        {
            if (cqgen[to] != cgen) CutQueryFill(to);
            for (int di = 0; di < 4; di++) if (to + dirs[di] == player) return (cqnp[to * 4 + di], cqsd[to * 4 + di]);
            throw new InvalidOperationException("cutScan.query: player is not adjacent to to");
        }
        public int CutPieces(int v) { if (cqgen[v] != cgen) CutQueryFill(v); return cqpc[v]; }

        void CutQueryFill(int to)
        {
            cqgen[to] = cgen;
            if (cbgen != cgen) CutBoxInfo();
            int lift = clift, baseI = to * 4;
            Span<int> lab = stackalloc int[4]; Span<bool> fr = stackalloc bool[4];
            lab.Clear(); fr.Clear();
            if (to == lift)
            {
                for (int di = 0; di < 4; di++)
                {
                    int w = to + dirs[di];
                    if (blk[w] != 0) continue;
                    fr[di] = true; lab[di] = cnd[w].Cid; // ensured by boxInfo
                }
                int npc = 0;
                for (int di = 0; di < 4; di++)
                {
                    if (!fr[di]) continue;
                    int sides0 = 0; bool first0 = true;
                    for (int dj = 0; dj < 4; dj++)
                        if (fr[dj] && lab[dj] == lab[di]) { sides0 |= 1 << dj; first0 = first0 && dj >= di; }
                    if (first0) npc++;
                    cqsd[baseI + di] = (byte)sides0;
                    cqnp[baseI + di] = csuf[ccs[lab[di]]];
                }
                cqpc[to] = (byte)npc;
                return;
            }
            CutEnsure(to);
            uint gen = cdgen;
            ref var nt = ref cnd[to];
            int tinTo = nt.Tin, toutTo = nt.Tout, cx = nt.Cid;
            bool isRoot = nt.Par < 0;
            int[] cu = cuF, clo = cloF, chi = chiF; bool[] csep = csepF;
            int nc = 0;
            foreach (int d in dirs)
            {
                int u = to + d;
                if (blk[u] != 0 || u == lift) continue;
                ref var nu = ref cnd[u];
                if (nu.Vgen == gen && nu.Par == to)
                {
                    cu[nc] = u; clo[nc] = nu.Tin; chi[nc] = nu.Tout;
                    csep[nc] = isRoot || nu.Low >= tinTo;
                    nc++;
                }
            }
            // pieceOf: index of the separated child subtree holding DFS time tw, else -1 (the parent side)
            int PieceOf(int tw)
            {
                if (tw > tinTo && tw < toutTo)
                    for (int i = 0; i < nc; i++)
                        if (clo[i] <= tw && tw < chi[i]) { if (csep[i]) return i; break; }
                return -1;
            }
            Span<bool> inM = stackalloc bool[4]; inM.Clear();
            bool parentInM = false;
            int mm = lift;
            for (int i = 0; i < cnbn; i++)
            {
                int y = cbn[i];
                if (y == to) continue;
                if (cbnc[i] != cx) { mm = Math.Min(mm, csuf[ccs[cbnc[i]]]); continue; }
                int j = PieceOf(cnd[y].Tin);
                if (j >= 0) inM[j] = true; else parentInM = true;
            }
            const int merged = -2;
            bool needParent = parentInM;
            for (int di = 0; di < 4; di++)
            {
                int w = to + dirs[di];
                if (w == lift) { fr[di] = true; lab[di] = merged; continue; }
                if (blk[w] != 0) continue;
                fr[di] = true;
                int j = PieceOf(cnd[w].Tin);
                if ((j >= 0 && inM[j]) || (j < 0 && parentInM)) lab[di] = merged;
                else { lab[di] = j; needParent = needParent || j < 0; }
            }
            int parentMin = 0;
            if (needParent)
            {
                int m = 1 << 30;
                if (tinTo > ccs[cx]) m = cpre[tinTo - 1];
                if (toutTo < cce[cx]) m = Math.Min(m, csuf[toutTo]);
                for (int i = 0; i < nc; i++) if (cnd[cu[i]].Low < tinTo) m = Math.Min(m, cnd[cu[i]].Sub);
                parentMin = m;
            }
            if (parentInM) mm = Math.Min(mm, parentMin);
            for (int i = 0; i < nc; i++) if (inM[i]) mm = Math.Min(mm, cnd[cu[i]].Sub);
            int np2 = 0;
            for (int di = 0; di < 4; di++)
            {
                if (!fr[di]) continue;
                int sides = 0; bool first = true;
                for (int dj = 0; dj < 4; dj++)
                    if (fr[dj] && lab[dj] == lab[di]) { sides |= 1 << dj; first = first && dj >= di; }
                if (first) np2++;
                cqsd[baseI + di] = (byte)sides;
                int l = lab[di];
                cqnp[baseI + di] = l == merged ? mm : l >= 0 ? cnd[cu[l]].Sub : parentMin;
            }
            cqpc[to] = (byte)np2;
        }

    }
}
