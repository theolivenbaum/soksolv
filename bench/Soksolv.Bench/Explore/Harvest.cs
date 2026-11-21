using Soksolv.Model;

namespace Soksolv.Bench.Explore;

/// <summary>A harvested set of real push-search states: sorted box floor-indices + normalized player floor-index.</summary>
public sealed class Harvest
{
    public string Name = "";
    public Level Lv = null!;
    public int[] Fidx = [];      // cell -> floor idx or -1
    public int[] Fcell = [];     // floor idx -> cell
    public int NBoxes;
    public int Floors => Fcell.Length;
    public List<ushort[]> States = new(); // [boxes sorted (floor idx)..., player]
    public bool[] Dead = [];

    public static Harvest Run(string path, int maxStates)
    {
        var rows = XsbCollection.ReadFile(path)[0];
        var lv = Level.Parse(rows);
        var h = new Harvest { Name = Path.GetFileNameWithoutExtension(path), Lv = lv };
        int n = lv.Cells;
        h.Fidx = new int[n];
        var fc = new List<int>();
        for (int c = 0; c < n; c++) { h.Fidx[c] = -1; if (!lv.Wall[c]) { h.Fidx[c] = fc.Count; fc.Add(c); } }
        h.Fcell = [.. fc];
        h.NBoxes = lv.Boxes.Length;
        // dead squares by reverse BFS from goals
        int[] dirs = [-lv.W, lv.W, -1, 1];
        var dist = new int[n]; Array.Fill(dist, -1);
        var q = new Queue<int>();
        for (int c = 0; c < n; c++) if (lv.Goal[c]) { dist[c] = 0; q.Enqueue(c); }
        while (q.Count > 0) { int c = q.Dequeue(); foreach (int d in dirs) { int b = c - d; if (!lv.Wall[b] && !lv.Wall[b - d] && dist[b] < 0) { dist[b] = dist[c] + 1; q.Enqueue(b); } } }
        h.Dead = new bool[n]; for (int c = 0; c < n; c++) h.Dead[c] = dist[c] < 0;

        var seen = new HashSet<string>();
        var queue = new Queue<(int[] boxes, int player)>();
        var occ = new bool[n];
        var mark = new int[n]; var mark2 = new int[n]; int stamp = 0;
        var qq = new int[n];
        int Reach(int p, int[] mark) { stamp++; int head = 0, tail = 0; qq[tail++] = p; mark[p] = stamp; int m = p; while (head < tail) { int c = qq[head++]; foreach (int d in dirs) { int y = c + d; if (mark[y] != stamp && !lv.Wall[y] && !occ[y]) { mark[y] = stamp; qq[tail++] = y; if (y < m) m = y; } } } return m; }
        foreach (var b in lv.Boxes) occ[b] = true;
        int p0 = Reach(lv.Player, mark);
        var start = ((int[])lv.Boxes.Clone(), p0); Array.Sort(start.Item1);
        string Key(int[] b, int p) => string.Join(',', b) + "|" + p;
        seen.Add(Key(start.Item1, p0)); queue.Enqueue(start);
        foreach (var b in lv.Boxes) occ[b] = false;
        while (queue.Count > 0 && h.States.Count < maxStates)
        {
            var (boxes, player) = queue.Dequeue();
            var st = new ushort[boxes.Length + 1];
            for (int i = 0; i < boxes.Length; i++) st[i] = (ushort)h.Fidx[boxes[i]];
            st[^1] = (ushort)h.Fidx[player];
            h.States.Add(st);
            foreach (var b in boxes) occ[b] = true;
            Reach(player, mark); int rs = stamp;
            for (int i = 0; i < boxes.Length; i++)
                foreach (int d in dirs)
                {
                    int b = boxes[i], t = b + d;
                    if (mark[b - d] != rs || lv.Wall[t] || occ[t] || h.Dead[t]) continue;
                    occ[b] = false; occ[t] = true;
                    int np = Reach(b, mark2);
                    var nb = (int[])boxes.Clone(); nb[i] = t; Array.Sort(nb);
                    occ[t] = false; occ[b] = true;
                    var k = Key(nb, np);
                    if (seen.Add(k)) queue.Enqueue((nb, np));
                }
            foreach (var b in boxes) occ[b] = false;
        }
        return h;
    }
}
