using System.Runtime.Intrinsics;

namespace Soksolv.Search;

/// <summary>
/// Minimum-cost box-to-goal assignment (Hungarian algorithm, port of Go solver.go assignTo / augmentAssignment / assignmentCost):
/// dist is goal-major (n goals x cells), edges with dist &lt; 0 cost AssignmentInf. The duals and assignment of the last run stay in
/// dhU/dhV/dhP so a one-row change can be repaired in O(n^2) (dive.go matchChild).
/// </summary>
public sealed unsafe partial class FullSolver
{
    int[] dhU = [], dhV = [], dhP = [], dhWay = [], dhMinv = [];
    int[] dhUsed = [];
    int[] dhUl = [], dhUd = []; int dhN;
    // box-major transposes of the two distance tables a dive alternates (Go distT with dtSrc/dtSrc2)
    int[]? dtSrc, dtT, dtSrc2, dtT2;
    bool diveOn;

    void AssignInit()
    {
        int m = n + 1;
        if (dhU.Length == m) return;
        dhU = new int[m]; dhV = new int[m]; dhP = new int[m]; dhWay = new int[m]; dhMinv = new int[m]; dhUsed = new int[m];
        dhUl = new int[m + 1]; dhUd = new int[m + 1];
    }

    int AssignEdge(int[] dist, int box, int goal1)
    {
        int d = dist[(goal1 - 1) * cells + box];
        return d >= 0 ? d : AssignmentInf;
    }

    int[] DistT(int[] dist)
    {
        if (dtSrc != null && ReferenceEquals(dtSrc, dist)) return dtT!;
        if (diveOn && dtSrc2 != null && ReferenceEquals(dtSrc2, dist))
        {
            (dtSrc, dtSrc2) = (dtSrc2, dtSrc);
            (dtT, dtT2) = (dtT2, dtT);
            return dtT!;
        }
        if (diveOn) { dtSrc2 = dtSrc; dtT2 = dtT; dtT = null; }
        int g = dist.Length / cells;
        if (dtT == null || dtT.Length < dist.Length) dtT = new int[dist.Length];
        for (int gi = 0; gi < g; gi++)
            for (int c = 0; c < cells; c++) dtT[c * g + gi] = dist[gi * cells + c];
        dtSrc = dist;
        return dtT;
    }

    /// <summary>Minimum total distance over assignments of boxes to distinct goals, or -1 if none is feasible.</summary>
    public int AssignTo(ReadOnlySpan<int> boxes, int[] dist)
    {
        AssignInit();
        Array.Clear(dhU); Array.Clear(dhV); Array.Clear(dhP);
        for (int row = 1; row <= n; row++) AugmentAssignment(boxes, dist, row);
        long total = AssignmentCost(boxes, dist);
        return total < AssignmentInf ? (int)total : -1;
    }

    /// <summary>
    /// AssignTo warm-started from the column potentials of the previous prime on the same distance table: each row's potential is set to
    /// its cheapest reduced edge (a feasible dual), so the row insertions find short augmenting paths. The minimum cost is unique, so the
    /// result equals the cold run; only which optimal assignment/dual is left in dhU/dhV/dhP may differ.
    /// </summary>
    int AssignWarm(ReadOnlySpan<int> boxes, int[] dist, int[] prev)
    {
        AssignInit();
        int m = n + 1;
        Array.Clear(dhU); Array.Clear(dhP);
        Array.Copy(prev, m, dhV, 0, m); dhV[0] = 0;
        var v = dhV; var u = dhU;
        for (int row = 1; row <= n; row++)
        {
            int box = boxes[row - 1], best = int.MaxValue;
            for (int j = 1; j <= n; j++) { int r = AssignEdge(dist, box, j) - v[j]; if (r < best) best = r; }
            u[row] = best;
        }
        for (int row = 1; row <= n; row++) AugmentAssignment(boxes, dist, row);
        long total = AssignmentCost(boxes, dist);
        return total < AssignmentInf ? (int)total : -1;
    }

    long AssignmentCost(ReadOnlySpan<int> boxes, int[] dist)
    {
        long cost = 0;
        if (dist.Length == n * cells)
        {
            var dt = DistT(dist);
            for (int g = 0; g < n; g++)
            {
                int row = dhP[g + 1];
                int d = dt[boxes[row - 1] * n + g];
                if (d < 0) d = AssignmentInf;
                cost += d;
            }
            return cost;
        }
        for (int g = 0; g < n; g++) cost += AssignEdge(dist, boxes[dhP[g + 1] - 1], g + 1);
        return cost;
    }

    /// <summary>Inserts an unmatched box row into a feasible dual and a tight partial matching (also repairs one replaced row of a complete matching).</summary>
    void AugmentAssignment(ReadOnlySpan<int> boxes, int[] dist, int row)
    {
        var u = dhU; var v = dhV; var p = dhP; var way = dhWay; var minv = dhMinv; var used = dhUsed;
        int[]? dt = dist.Length == n * cells ? DistT(dist) : null;
        p[0] = row;
        int j0 = 0;
        for (int j = 0; j < minv.Length; j++) { minv[j] = int.MaxValue; used[j] = 0; }
        // Lazy duals: minv holds each unused column's true value plus D, the sum of the deltas so far
        int D = 0;
        dhN = 0;
        while (true)
        {
            used[j0] = 1;
            dhUl[dhN] = j0; dhUd[dhN] = D; dhN++;
            int i0 = p[j0], ms = int.MaxValue, j1 = 0;
            int ui0 = u[i0] - D;
            if (dt != null)
            {
                int eo = boxes[i0 - 1] * n;
                int j = 1;
                if (Vector128.IsHardwareAccelerated && n >= 8)
                {
                    // 4 columns per step: minv/way update and the running minimum over unused columns
                    var vui0 = Vector128.Create(ui0); var vInf = Vector128.Create(AssignmentInf); var vMax = Vector128.Create(int.MaxValue);
                    var vj0 = Vector128.Create(j0); var zero = Vector128<int>.Zero; var vms = vMax;
                    ref int rUsed = ref used[0]; ref int rDt = ref dt[0]; ref int rV = ref v[0]; ref int rMinv = ref minv[0]; ref int rWay = ref way[0];
                    for (; j + 3 <= n; j += 4)
                    {
                        var nu = Vector128.Equals(Vector128.LoadUnsafe(ref rUsed, (nuint)j), zero);
                        var e = Vector128.LoadUnsafe(ref rDt, (nuint)(eo + j - 1));
                        e = Vector128.ConditionalSelect(Vector128.LessThan(e, zero), vInf, e);
                        var cur = e - vui0 - Vector128.LoadUnsafe(ref rV, (nuint)j);
                        var mv = Vector128.LoadUnsafe(ref rMinv, (nuint)j);
                        var lt = Vector128.LessThan(cur, mv) & nu;
                        mv = Vector128.ConditionalSelect(lt, cur, mv);
                        mv.StoreUnsafe(ref rMinv, (nuint)j);
                        Vector128.ConditionalSelect(lt, vj0, Vector128.LoadUnsafe(ref rWay, (nuint)j)).StoreUnsafe(ref rWay, (nuint)j);
                        vms = Vector128.Min(vms, Vector128.ConditionalSelect(nu, mv, vMax));
                    }
                    ms = Math.Min(Math.Min(vms[0], vms[1]), Math.Min(vms[2], vms[3]));
                }
                for (; j <= n; j++)
                {
                    if (used[j] != 0) continue;
                    int e = dt[eo + j - 1];
                    if (e < 0) e = AssignmentInf;
                    int cur = e - ui0 - v[j];
                    if (cur < minv[j]) { minv[j] = cur; way[j] = j0; }
                    if (minv[j] < ms) ms = minv[j];
                }
                // tie-break as the scalar scan: the last free column at the minimum, else the first column at the minimum
                int first = 0, last0 = 0;
                for (j = 1; j <= n; j++)
                    if (used[j] == 0 && minv[j] == ms) { if (first == 0) first = j; if (p[j] == 0) last0 = j; }
                j1 = last0 != 0 ? last0 : first;
            }
            else
            {
                for (int j = 1; j <= n; j++)
                {
                    if (used[j] != 0) continue;
                    int cur = AssignEdge(dist, boxes[i0 - 1], j) - ui0 - v[j];
                    if (cur < minv[j]) { minv[j] = cur; way[j] = j0; }
                    int mj = minv[j];
                    if (mj < ms || (mj == ms && p[j] == 0)) { ms = mj; j1 = j; }
                }
            }
            D = ms;
            j0 = j1;
            if (p[j0] == 0) break;
        }
        for (int k = 0; k < dhN; k++)
        {
            int j = dhUl[k];
            int d = D - dhUd[k];
            u[p[j]] += d;
            v[j] -= d;
        }
        while (j0 != 0) { int j1 = way[j0]; p[j0] = p[j1]; j0 = j1; }
    }
}
