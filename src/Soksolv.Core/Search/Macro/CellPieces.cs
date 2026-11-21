using System.Runtime.CompilerServices;

namespace Soksolv.Search;

public sealed partial class FullSolver
{
    /// <summary>Block-cut view of a free-cell graph (4-neighbour, <see cref="Blk"/> != 0 = blocked): one low-link DFS answers, for every
    /// cell c, which of c's neighbours stay connected to each other when c is removed (<see cref="Labels"/>) in O(1) per cell,
    /// instead of one flood per (cell, neighbour). Used by the fa2 hot-side relation and the out-of-plan zones.</summary>
    internal sealed unsafe class CellPieces
    {
        readonly int cells, d0, d1, d2, d3;
        public readonly byte[] Blk; readonly byte* blk;
        readonly int[] tinA, toutA, lowA, parA, stackA; readonly byte[] sdirA;
        readonly int* tin, tout, low, par, stack; readonly byte* sdir;
        public CellPieces(int cells, int[] dirs)
        {
            this.cells = cells; d0 = dirs[0]; d1 = dirs[1]; d2 = dirs[2]; d3 = dirs[3];
            Blk = GC.AllocateArray<byte>(cells, pinned: true); blk = (byte*)Unsafe.AsPointer(ref Blk[0]);
            tinA = GC.AllocateArray<int>(cells, pinned: true); toutA = GC.AllocateArray<int>(cells, pinned: true); lowA = GC.AllocateArray<int>(cells, pinned: true);
            parA = GC.AllocateArray<int>(cells, pinned: true); stackA = GC.AllocateArray<int>(cells + 1, pinned: true); sdirA = GC.AllocateArray<byte>(cells + 1, pinned: true);
            tin = (int*)Unsafe.AsPointer(ref tinA[0]); tout = (int*)Unsafe.AsPointer(ref toutA[0]); low = (int*)Unsafe.AsPointer(ref lowA[0]);
            par = (int*)Unsafe.AsPointer(ref parA[0]); stack = (int*)Unsafe.AsPointer(ref stackA[0]); sdir = (byte*)Unsafe.AsPointer(ref sdirA[0]);
        }

        /// <summary>DFS forest over every unblocked cell.</summary>
        public void Build()
        {
            int n = cells, time = 0;
            for (int i = 0; i < n; i++) tin[i] = -1;
            byte* bk = blk;
            for (int root = 0; root < n; root++)
            {
                if (bk[root] != 0 || tin[root] >= 0) continue;
                tin[root] = time; low[root] = time; par[root] = -1; time++;
                stack[0] = root; sdir[0] = 0; int sp = 1;
                while (sp > 0)
                {
                    int top = sp - 1, v = stack[top];
                    bool pushed = false;
                    for (int di = sdir[top]; di < 4; di++)
                    {
                        int w = v + (di == 0 ? d0 : di == 1 ? d1 : di == 2 ? d2 : d3);
                        if (bk[w] != 0) continue;
                        if (tin[w] < 0)
                        {
                            tin[w] = time; low[w] = time; par[w] = v; time++;
                            sdir[top] = (byte)(di + 1);
                            stack[sp] = w; sdir[sp] = 0; sp++;
                            pushed = true;
                            break;
                        }
                        if (w != par[v] && tin[w] < low[v]) low[v] = tin[w];
                    }
                    if (pushed) continue;
                    tout[v] = time;
                    sp = top;
                    int p = par[v];
                    if (p >= 0 && low[v] < low[p]) low[p] = low[v];
                }
            }
        }

        /// <summary>lab[di] for the four neighbours of c (c itself unblocked): -1 = blocked, otherwise the piece number (0, 1, ... by first
        /// occurrence in direction order) of the component of the neighbour in the graph without c.</summary>
        public void Labels(int c, int* lab)
        {
            byte* bk = blk;
            int tc = tin[c], oc = tout[c]; bool isRoot = par[c] < 0;
            int* lo = stackalloc int[4]; int* hi = stackalloc int[4]; int ns = 0;
            for (int di = 0; di < 4; di++)
            {
                int u = c + (di == 0 ? d0 : di == 1 ? d1 : di == 2 ? d2 : d3);
                if (bk[u] != 0 || par[u] != c || tin[u] < 0) continue;
                if (isRoot || low[u] >= tc) { lo[ns] = tin[u]; hi[ns] = tout[u]; ns++; }
            }
            int* pc = stackalloc int[5]; int np = 0;   // distinct piece ids seen so far (child index, or 7 = the rest)
            for (int di = 0; di < 4; di++)
            {
                int w = c + (di == 0 ? d0 : di == 1 ? d1 : di == 2 ? d2 : d3);
                if (bk[w] != 0) { lab[di] = -1; continue; }
                int tw = tin[w], id = 7;
                if (tw > tc && tw < oc)
                    for (int k = 0; k < ns; k++)
                        if (lo[k] <= tw && tw < hi[k]) { id = k; break; }
                int l = 0;
                while (l < np && pc[l] != id) l++;
                if (l == np) pc[np++] = id;
                lab[di] = l;
            }
        }
    }
}
