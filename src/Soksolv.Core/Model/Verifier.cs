namespace Soksolv.Model;

/// <summary>
/// Independent LURD replay (port of Go `verify`): shares no code with the solver's Level model.
/// Lowercase = walk, uppercase = push.
/// </summary>
public static class Verifier
{
    /// <summary>Returns null if valid, otherwise the failure reason.</summary>
    public static string? CheckSolution(string[] rows, ReadOnlySpan<char> lurd)
    {
        if (lurd.IndexOfAny("LURD") < 0) return "solution has no push";
        return Check(rows, lurd);
    }

    public static string? Check(string[] rows, ReadOnlySpan<char> lurd)
    {
        int h = rows.Length, w = 0;
        foreach (var r in rows) w = Math.Max(w, r.Length);
        char At(int x, int y) => (y < 0 || y >= h || x < 0 || x >= rows[y].Length) ? '#' : rows[y][x];

        var box = new HashSet<(int, int)>();
        int goals = 0, px = 0, py = 0, players = 0;
        for (int y = 0; y < h; y++)
            for (int x = 0; x < w; x++)
                switch (At(x, y))
                {
                    case '@': (px, py) = (x, y); players++; break;
                    case '+': (px, py) = (x, y); players++; goals++; break;
                    case '$': box.Add((x, y)); break;
                    case '*': box.Add((x, y)); goals++; break;
                    case '.': goals++; break;
                }
        if (players != 1) return $"board has {players} players";
        if (box.Count == 0 || box.Count != goals) return $"board has {box.Count} boxes and {goals} goals";

        for (int i = 0; i < lurd.Length; i++)
        {
            char c = lurd[i];
            int dx = 0, dy = 0;
            switch (c)
            {
                case 'l' or 'L': dx = -1; break;
                case 'r' or 'R': dx = 1; break;
                case 'u' or 'U': dy = -1; break;
                case 'd' or 'D': dy = 1; break;
                default: return $"move {i}: invalid character '{c}'";
            }
            bool push = c is >= 'A' and <= 'Z';
            int nx = px + dx, ny = py + dy;
            if (At(nx, ny) == '#') return $"move {i}: walks into wall";
            if (box.Contains((nx, ny)) != push)
                return push ? $"move {i}: push with no box" : $"move {i}: walk into a box (should be a push)";
            if (push)
            {
                int bx = nx + dx, by = ny + dy;
                if (At(bx, by) == '#' || box.Contains((bx, by))) return $"move {i}: box blocked";
                box.Remove((nx, ny));
                box.Add((bx, by));
            }
            (px, py) = (nx, ny);
        }
        foreach (var (x, y) in box)
            if (At(x, y) is not ('.' or '*' or '+')) return $"box at ({x},{y}) not on a goal";
        return null;
    }
}
