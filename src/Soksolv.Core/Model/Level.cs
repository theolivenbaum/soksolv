namespace Soksolv.Model;

/// <summary>
/// Padded grid: the outer ring is always wall, so neighbour lookups never go out of bounds.
/// Cells unreachable by the player are turned into walls. Cell id = y*W + x (same as Go).
/// </summary>
public sealed class Level
{
    public int W, H;
    public bool[] Wall = [];
    public bool[] Goal = [];
    public int[] Boxes = [];
    public int Player;
    public int Cells => W * H;

    public static Level Parse(string[] rows, bool closeHoles = true)
    {
        var lv = ParseRaw(rows);
        if (closeHoles) CloseHoles(lv.Wall, lv.Goal, lv.W, lv.Boxes, lv.Player);
        return lv;
    }

    /// <summary>Port of Go parseLevelRaw.</summary>
    public static Level ParseRaw(string[] rows)
    {
        int w = 0;
        foreach (var r in rows) w = Math.Max(w, r.Length);
        var lv = new Level { W = w + 2, H = rows.Length + 2, Player = -1 };
        int n = lv.W * lv.H;
        if (n > 32767) throw new InvalidDataException($"board too large ({n} cells)");
        lv.Wall = new bool[n];
        lv.Goal = new bool[n];
        var isBox = new bool[n];
        Array.Fill(lv.Wall, true);
        for (int y = 0; y < rows.Length; y++)
            for (int x = 0; x < rows[y].Length; x++)
            {
                int c = (y + 1) * lv.W + x + 1;
                char ch = rows[y][x];
                lv.Wall[c] = ch == '#';
                switch (ch)
                {
                    case '@': lv.Player = c; break;
                    case '+': lv.Player = c; lv.Goal[c] = true; break;
                    case '$': isBox[c] = true; break;
                    case '*': isBox[c] = true; lv.Goal[c] = true; break;
                    case '.': lv.Goal[c] = true; break;
                }
            }
        if (lv.Player < 0) throw new InvalidDataException("level has no player");

        var inside = new bool[n];
        var stack = new Stack<int>();
        stack.Push(lv.Player);
        inside[lv.Player] = true;
        Span<int> dirs = [-lv.W, lv.W, -1, 1];
        while (stack.TryPop(out int c))
            foreach (int d in dirs)
            {
                int nb = c + d;
                if (!lv.Wall[nb] && !inside[nb]) { inside[nb] = true; stack.Push(nb); }
            }
        var boxes = new List<int>();
        int goals = 0;
        for (int c = 0; c < n; c++)
        {
            if (!inside[c])
            {
                if (isBox[c] != lv.Goal[c]) throw new InvalidDataException("unsolvable: box or goal outside the player's region");
                lv.Wall[c] = true; lv.Goal[c] = false;
                continue;
            }
            if (isBox[c]) boxes.Add(c);
            if (lv.Goal[c]) goals++;
        }
        if (boxes.Count != goals) throw new InvalidDataException($"{boxes.Count} boxes but {goals} goals");
        lv.Boxes = [.. boxes];
        return lv;
    }

    /// <summary>Port of Go holes.Close: walls off dead-end floor cells (goal/box/player-free, >=3 wall neighbours), cascading.</summary>
    public static int CloseHoles(bool[] wall, bool[] goal, int w, int[] boxes, int player)
    {
        var keep = new bool[wall.Length];
        foreach (int b in boxes) keep[b] = true;
        if (player >= 0) keep[player] = true;
        int[] dirs = [-w, w, -1, 1];
        int Walls(int c) { int n = 0; foreach (int d in dirs) if (wall[c + d]) n++; return n; }
        var stack = new Stack<int>();
        for (int c = w; c < wall.Length - w; c++)
            if (!wall[c] && !goal[c] && !keep[c] && Walls(c) >= 3) stack.Push(c);
        int closed = 0;
        while (stack.TryPop(out int c))
        {
            if (wall[c] || goal[c] || keep[c] || Walls(c) < 3) continue;
            wall[c] = true; closed++;
            foreach (int d in dirs)
            {
                int n = c + d;
                if (!wall[n] && !goal[n] && !keep[n] && Walls(n) >= 3) stack.Push(n);
            }
        }
        return closed;
    }
}
