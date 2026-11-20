namespace Soksolv.Search;

/// <summary>Stage 4 of the macro lane: hotspots (Go hotspots.go) — advisor information only, never a deadlock proof.</summary>
public sealed unsafe partial class FullSolver
{
    // row(y): floor-index bitset of box squares whose reachable-goal set shrinks if y becomes a permanent wall
    // (union over goals g of T_g \ T'_g(y) \ {y}, T_g = push-reachability tree of g, T' with y walled as cell and as support).
    readonly Dictionary<int, ulong[]> hotRows = new();
    uint[]? hotSeen; uint hotStamp; int[]? hotQ;

    ulong[] HotRow(int blocker)
    {
        if (hotRows.TryGetValue(blocker, out var cached)) return cached;
        var row = new ulong[Math.Max(1, nodes.Nw)];
        hotSeen ??= new uint[cells]; hotQ ??= new int[cells];
        for (int g = 0; g < goals.Length; g++)
        {
            int goalCell = goals[g];
            int gbase = g * cells;
            if (goalCell == blocker)
            {
                for (int c = 0; c < cells; c++)
                    if (c != goalCell && goalDist[gbase + c] >= 0) { int f = nodes.Fidx[c]; row[f >> 6] |= 1UL << (f & 63); }
                continue;
            }
            uint st = ++hotStamp; int tail = 0;
            hotQ[tail++] = goalCell; hotSeen[goalCell] = st;
            for (int i = 0; i < tail; i++)
            {
                int q = hotQ[i];
                foreach (int d in dirs)
                {
                    int b = q - d, sup = b - d;
                    if (wall[b] || hotSeen[b] == st || wall[sup] || b == blocker || sup == blocker) continue;
                    hotSeen[b] = st; hotQ[tail++] = b;
                }
            }
            for (int c = 0; c < cells; c++)
                if (c != blocker && goalDist[gbase + c] >= 0 && hotSeen[c] != st) { int f = nodes.Fidx[c]; row[f >> 6] |= 1UL << (f & 63); }
        }
        hotRows[blocker] = row;
        return row;
    }

    static bool RowHits(ulong[] row, int fidx) => (row[fidx >> 6] >> (fidx & 63) & 1) != 0;

    /// <summary>Per box: number of other boxes its blocking would hurt.</summary>
    int[] HotCounts(ReadOnlySpan<int> boxes)
    {
        var counts = new int[boxes.Length];
        for (int i = 0; i < boxes.Length; i++)
        {
            var row = HotRow(boxes[i]);
            for (int j = 0; j < boxes.Length; j++)
                if (i != j && RowHits(row, nodes.Fidx[boxes[j]])) counts[i]++;
        }
        return counts;
    }

    static int HotspotCount(int[] counts) { int n = 0; foreach (int c in counts) if (c > 0) n++; return n; }

    /// <summary>Hotspot boxes after replacing boxes[moved] with target.</summary>
    int HotAfter(ReadOnlySpan<int> boxes, int[] counts, int moved, int target)
    {
        int old = nodes.Fidx[boxes[moved]], now = nodes.Fidx[target];
        int n = 0;
        for (int i = 0; i < boxes.Length; i++)
        {
            if (i == moved) continue;
            var row = HotRow(boxes[i]);
            int c = counts[i] - (RowHits(row, old) ? 1 : 0) + (RowHits(row, now) ? 1 : 0);
            if (c > 0) n++;
        }
        var tr = HotRow(target);
        for (int j = 0; j < boxes.Length; j++)
            if (j != moved && RowHits(tr, nodes.Fidx[boxes[j]])) { n++; break; }
        return n;
    }

    /// <summary>
    /// Move access (Go moveAccessScan with access novelty off), for the child just flooded by the box search (m3 stamp mpstamp,
    /// occ = child): mobility = (box, direction) pairs whose support cell is in the child's player region; explorer = an unchanged
    /// box gains a legal push from support outside the parent's region (mark stamped mOpenSt).
    /// </summary>
    (int mobility, bool explorer) MMoveAccess(ReadOnlySpan<int> cur, int moved, int target)
    {
        uint cs = mpstamp;
        int mobility = 0; bool explorer = false;
        for (int i = 0; i < cur.Length; i++)
        {
            int b = i == moved ? target : cur[i];
            for (int di = 0; di < 4; di++)
            {
                int d = dirs[di];
                int support = b - d, to = b + d;
                if (m3[support] != cs) continue;
                mobility++;
                bool fresh = i != moved && mark[support] != mOpenSt;
                if (!fresh || explorer) continue;
                if (!wall[to] && !occ[to] && dist[b + d] >= 0) explorer = true;
            }
        }
        return (mobility, explorer);
    }
}
