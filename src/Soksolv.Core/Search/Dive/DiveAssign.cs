namespace Soksolv.Search;

/// <summary>
/// Minimum-cost box-to-goal assignment (Hungarian algorithm, port of Go solver.go assignTo / augmentAssignment / assignmentCost):
/// dist is goal-major (n goals x cells), edges with dist &lt; 0 cost AssignmentInf. The duals and assignment of the last run stay in
/// dhU/dhV/dhP so a one-row change can be repaired in O(n^2) (dive.go matchChild).
/// </summary>
public sealed unsafe partial class FullSolver
{
    public const int AssignmentInf = 1 << 20;
    int[] dhU = [], dhV = [], dhP = [], dhWay = [], dhMinv = [];
    bool[] dhUsed = [];
    int[] dhUl = [], dhUd = []; int dhN;
    // box-major transposes of the two distance tables a dive alternates (Go distT with dtSrc/dtSrc2)
    int[]? dtSrc, dtT, dtSrc2, dtT2;
    bool diveOn;

    void AssignInit()
    {
        int m = n + 1;
        if (dhU.Length == m) return;
        dhU = new int[m]; dhV = new int[m]; dhP = new int[m]; dhWay = new int[m]; dhMinv = new int[m]; dhUsed = new bool[m];
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
        for (int j = 0; j < minv.Length; j++) { minv[j] = int.MaxValue; used[j] = false; }
        // Lazy duals: minv holds each unused column's true value plus D, the sum of the deltas so far
        int D = 0;
        dhN = 0;
        while (true)
        {
            used[j0] = true;
            dhUl[dhN] = j0; dhUd[dhN] = D; dhN++;
            int i0 = p[j0], ms = int.MaxValue, j1 = 0;
            int ui0 = u[i0] - D;
            if (dt != null)
            {
                int eo = boxes[i0 - 1] * n;
                for (int j = 1; j <= n; j++)
                {
                    if (used[j]) continue;
                    int e = dt[eo + j - 1];
                    if (e < 0) e = AssignmentInf;
                    int cur = e - ui0 - v[j];
                    if (cur < minv[j]) { minv[j] = cur; way[j] = j0; }
                    int mj = minv[j];
                    if (mj < ms || (mj == ms && p[j] == 0)) { ms = mj; j1 = j; }
                }
            }
            else
            {
                for (int j = 1; j <= n; j++)
                {
                    if (used[j]) continue;
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
