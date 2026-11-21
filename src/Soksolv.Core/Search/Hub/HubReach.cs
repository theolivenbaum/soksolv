using System.Numerics;

namespace Soksolv.Search.Hub;

/// <summary>
/// Start-feasibility of generator hubs by EXACT single-box reach (port of Go hubreach.go, exp-463).
/// For every start box b, R_b = the cells the box can be pushed to with only walls on the board (all other boxes
/// removed) with the player's access checked: a state is (box cell, the player's component in the board minus the
/// box), and a push needs the cell behind the box in that component. R_b is a superset of what the real game
/// allows, so "hub square not in R_b" and "no perfect matching boxes -> hub squares over R_b" are NECESSARY
/// conditions for forward reachability. Boxes with equal R_b form classes; a class set S (other than the union)
/// holding k boxes' reach needs >= k hub boxes inside S (at most 64 classes, smallest first).
/// </summary>
public sealed class HubReach
{
    public readonly ulong[] CellMask;   // per cell: bit j set if the cell is in class j's set
    public readonly int[] Need;         // per class: boxes of the start that must end inside it
    public readonly bool[] InU;         // per cell: some start box can reach it
    public readonly int NCls;
    public readonly ulong[][] Rb;       // per start box: reach bitset over cells

    // matching scratch
    readonly int[] matchOf; readonly uint[] seen; uint stamp; readonly int[][] adj; readonly int[] adjLen;

    HubReach(ulong[] cellMask, int[] need, bool[] inU, int nCls, ulong[][] rb)
    {
        CellMask = cellMask; Need = need; InU = inU; NCls = nCls; Rb = rb;
        int n = rb.Length;
        matchOf = new int[n]; seen = new uint[n]; adj = new int[n][]; adjLen = new int[n];
        for (int i = 0; i < n; i++) adj[i] = new int[n];
    }

    /// <summary>
    /// startBoxes: the start squares (cells, ascending) of the pull view; realPlayer: the real start player cell.
    /// Returns null when there are no start boxes.
    /// </summary>
    public static HubReach? Create(bool[] wall, int w, ReadOnlySpan<int> startBoxes, int realPlayer)
    {
        int cells = wall.Length;
        int n = startBoxes.Length;
        if (n == 0) return null;
        int[] dirs = [-w, w, -1, 1];
        ReadOnlySpan<int> opp = [1, 0, 3, 2];
        // comp[x*4+k]: canonical (smallest) side index of the component, in the board minus x, holding the cell beside x on side k; -1 if wall
        var comp = new sbyte[cells * 4];
        var lab = new uint[cells];
        uint stamp = 0;
        var q = new int[cells];
        void Flood(int from, int block)
        {
            stamp++;
            int tail = 0; q[tail++] = from; lab[from] = stamp;
            for (int i = 0; i < tail; i++)
            {
                int c = q[i];
                foreach (int d in dirs)
                {
                    int y = c + d;
                    if (y != block && !wall[y] && lab[y] != stamp) { lab[y] = stamp; q[tail++] = y; }
                }
            }
        }
        for (int x = 0; x < cells; x++)
        {
            for (int k = 0; k < 4; k++) comp[x * 4 + k] = -1;
            if (wall[x]) continue;
            for (int k = 0; k < 4; k++)
            {
                int nb = x + dirs[k];
                if (wall[nb]) continue;
                if (k > 0 && comp[x * 4 + k] >= 0) continue;
                Flood(nb, x);
                for (int j = k; j < 4; j++)
                {
                    int y = x + dirs[j];
                    if (!wall[y] && lab[y] == stamp) comp[x * 4 + j] = (sbyte)k;
                }
            }
        }
        int words = (cells + 63) / 64;
        var rb = new ulong[n][];
        var vis = new ulong[(cells * 4 + 63) / 64];
        var bfs = new int[cells * 4];
        for (int bi = 0; bi < n; bi++)
        {
            int b = startBoxes[bi];
            int pk = -1;
            Flood(realPlayer, b);
            for (int k = 0; k < 4; k++)
            {
                int nb = b + dirs[k];
                if (!wall[nb] && lab[nb] == stamp) { pk = comp[b * 4 + k]; break; }
            }
            var set = new ulong[words];
            set[b >> 6] |= 1UL << (b & 63);
            rb[bi] = set;
            if (pk < 0) continue;
            Array.Clear(vis);
            int tail = 0;
            bfs[tail++] = b * 4 + pk;
            vis[(b * 4 + pk) >> 6] |= 1UL << ((b * 4 + pk) & 63);
            for (int i = 0; i < tail; i++)
            {
                int x = bfs[i] >> 2; int k = bfs[i] & 3;
                for (int di = 0; di < 4; di++)
                {
                    int y = x + dirs[di];
                    if (wall[y] || comp[x * 4 + opp[di]] != k) continue;
                    int ny = comp[y * 4 + opp[di]];
                    if (ny < 0) continue;
                    int s = y * 4 + ny;
                    if ((vis[s >> 6] & (1UL << (s & 63))) != 0) continue;
                    vis[s >> 6] |= 1UL << (s & 63);
                    set[y >> 6] |= 1UL << (y & 63);
                    bfs[tail++] = s;
                }
            }
        }
        // union and classes
        var union = new ulong[words];
        foreach (var s in rb) for (int i = 0; i < words; i++) union[i] |= s[i];
        var inU = new bool[cells];
        for (int c = 0; c < cells; c++) inU[c] = (union[c >> 6] & (1UL << (c & 63))) != 0;
        var sets = new List<ulong[]>();
        foreach (var s in rb)
            if (!s.AsSpan().SequenceEqual(union) && !sets.Exists(t => t.AsSpan().SequenceEqual(s))) sets.Add(s);
        int Card(ulong[] s) { int c = 0; foreach (var x in s) c += BitOperations.PopCount(x); return c; }
        // stable sort by cardinality (List.Sort is unstable)
        var keyed = sets.Select((s, i) => (s, i, c: Card(s))).OrderBy(t => t.c).ThenBy(t => t.i).Select(t => t.s).ToList();
        if (keyed.Count > 64) keyed = keyed.GetRange(0, 64);
        int nCls = keyed.Count;
        var cellMask = new ulong[cells];
        var need = new int[nCls];
        for (int j = 0; j < nCls; j++)
        {
            var s = keyed[j];
            for (int c = 0; c < cells; c++) if ((s[c >> 6] & (1UL << (c & 63))) != 0) cellMask[c] |= 1UL << j;
            foreach (var t in rb)
            {
                bool sub = true;
                for (int i = 0; i < words; i++) if ((t[i] & ~s[i]) != 0) { sub = false; break; }
                if (sub) need[j]++;
            }
        }
        return new HubReach(cellMask, need, inU, nCls, rb);
    }

    /// <summary>Fills cnt[j] with the number of boxes of bs inside class j's set.</summary>
    public void Counts(ReadOnlySpan<ushort> bs, Span<int> cnt)
    {
        cnt[..64].Clear();
        foreach (ushort b in bs)
            for (ulong m = CellMask[b]; m != 0; m &= m - 1) cnt[BitOperations.TrailingZeroCount(m)]++;
    }

    /// <summary>Whether pulling a box from c to x keeps every class's requirement, given the node's counts.</summary>
    public bool Ok(int c, int x, ReadOnlySpan<int> cnt)
    {
        for (ulong m = CellMask[c] & ~CellMask[x]; m != 0; m &= m - 1)
        {
            int j = BitOperations.TrailingZeroCount(m);
            if (cnt[j] - 1 < Need[j]) return false;
        }
        return true;
    }

    /// <summary>Exact perfect-matching test: can the start boxes be matched to distinct squares of the hub, each within its reach set?</summary>
    public bool Feasible(ReadOnlySpan<int> hub)
    {
        int n = Rb.Length;
        if (hub.Length != n) return true;
        for (int i = 0; i < n; i++)
        {
            int len = 0;
            var r = Rb[i];
            for (int j = 0; j < hub.Length; j++)
            {
                int h = hub[j];
                if ((r[h >> 6] & (1UL << (h & 63))) != 0) adj[i][len++] = j;
            }
            if (len == 0) return false;
            adjLen[i] = len;
        }
        Array.Fill(matchOf, -1);
        for (int i = 0; i < n; i++)
        {
            stamp++;
            if (!Try(i)) return false;
        }
        return true;
    }

    bool Try(int i)
    {
        var a = adj[i]; int len = adjLen[i];
        for (int k = 0; k < len; k++)
        {
            int j = a[k];
            if (seen[j] == stamp) continue;
            seen[j] = stamp;
            if (matchOf[j] < 0 || Try(matchOf[j])) { matchOf[j] = i; return true; }
        }
        return false;
    }
}
