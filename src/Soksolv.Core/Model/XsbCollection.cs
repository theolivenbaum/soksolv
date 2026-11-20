namespace Soksolv.Model;

/// <summary>Splits an .xsb collection into levels (board rows). Port of Go verify.SplitLevels / main.splitCollection.</summary>
public static class XsbCollection
{
    public static List<string[]> Split(ReadOnlySpan<char> data)
    {
        var levels = new List<string[]>();
        List<string>? cur = null;
        foreach (var lineRange in data.Split('\n'))
        {
            var line = data[lineRange].TrimEnd('\r');
            if (IsBoardLine(line))
                (cur ??= new()).Add(line.ToString());
            else if (cur is not null && IsFloorRow(line))
                cur.Add(line.ToString()); // spacer row inside a level (OTS "Master Head 29")
            else if (cur is not null)
            {
                levels.Add(TrimFloorRows(cur));
                cur = null;
            }
        }
        if (cur is not null) levels.Add(TrimFloorRows(cur));
        return levels;
    }

    public static List<string[]> ReadFile(string path) => Split(File.ReadAllText(path));

    static bool IsBoardLine(ReadOnlySpan<char> s)
    {
        bool wall = false;
        foreach (char c in s)
        {
            switch (c)
            {
                case '#': wall = true; break;
                case ' ' or '-' or '_' or '@' or '+' or '$' or '*' or '.': break;
                default: return false;
            }
        }
        return wall;
    }

    static bool IsFloorRow(ReadOnlySpan<char> s)
    {
        bool floor = false;
        foreach (char c in s)
        {
            if (c is '-' or '_') floor = true;
            else if (c != ' ') return false;
        }
        return floor;
    }

    static string[] TrimFloorRows(List<string> rows)
    {
        int n = rows.Count;
        while (n > 0 && IsFloorRow(rows[n - 1])) n--;
        return rows.GetRange(0, n).ToArray();
    }
}
