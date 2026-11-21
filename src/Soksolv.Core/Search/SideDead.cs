using Soksolv.Model;

namespace Soksolv.Search;

/// <summary>
/// Player-side dead squares (port of Go package sidedead). For every floor cell c and each player
/// component k around c (other boxes removed): can a box on c still reach a goal?
/// </summary>
public sealed class SideDead
{
    readonly int[] fi;       // cell -> floor index, -1 for walls
    readonly sbyte[]?[] label; // per floor index: component of every floor cell with the box there (null: one component)
    readonly bool[] live;    // floor index * 4 + component

    public SideDead(bool[] wall, bool[] goal, int w)
    {
        int[] dirs = [-w, w, -1, 1];
        int n = wall.Length;
        fi = new int[n];
        var floor = new List<int>();
        for (int c = 0; c < n; c++)
        {
            fi[c] = -1;
            if (!wall[c]) { fi[c] = floor.Count; floor.Add(c); }
        }
        int nf = floor.Count;
        label = new sbyte[nf][];
        live = new bool[nf * 4];
        var comp = new sbyte[nf];
        var queue = new List<int>(nf);
        for (int i = 0; i < nf; i++)
        {
            int c = floor[i];
            Array.Fill(comp, (sbyte)-1);
            sbyte k = 0;
            foreach (int d in dirs)
            {
                int x = c + d;
                if (wall[x] || comp[fi[x]] >= 0) continue;
                comp[fi[x]] = k;
                queue.Clear(); queue.Add(x);
                for (int q = 0; q < queue.Count; q++)
                    foreach (int d2 in dirs)
                    {
                        int y = queue[q] + d2;
                        if (y != c && !wall[y] && comp[fi[y]] < 0) { comp[fi[y]] = k; queue.Add(y); }
                    }
                k++;
            }
            if (k > 1) label[i] = (sbyte[])comp.Clone();
        }
        sbyte Side(int c, int player) => label[fi[c]] is { } l ? l[fi[player]] : (sbyte)0;
        var bq = new Queue<int>();
        for (int i = 0; i < nf; i++)
            if (goal[floor[i]])
                for (int k = 0; k < 4; k++) { live[i * 4 + k] = true; bq.Enqueue(i * 4 + k); }
        while (bq.Count > 0)
        {
            int v = bq.Dequeue();
            int y = floor[v / 4];
            sbyte ky = (sbyte)(v % 4);
            foreach (int d in dirs)
            {
                int x = y - d, p = y - 2 * d;
                if (wall[x] || wall[p] || Side(y, x) != ky) continue;
                int u = fi[x] * 4 + Side(x, p);
                if (!live[u]) { live[u] = true; bq.Enqueue(u); }
            }
        }
    }

    /// <summary>The cell separates the floor into several player components when a box sits on it (Dead then depends on the player's side).</summary>
    public bool IsSplitter(int c) => label[fi[c]] != null;
    /// <summary>For a non-splitter cell: a box there is dead whatever the player's side.</summary>
    public bool StaticDead(int c) => !live[fi[c] * 4];

    /// <summary>Whether a box on c, with the player on cell player, can no longer reach any goal.</summary>
    public bool Dead(int c, int player)
    {
        int i = fi[c];
        int k = 0;
        if (label[i] is { } l) k = l[fi[player]];
        return k < 0 || !live[i * 4 + k];
    }
}
