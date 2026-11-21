namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    DiagDeadChecker? dd;
    bool[]? festTun; int[]? fiC;

    /// <summary>Go festTunnels: Festival's forbidden push-tunnel squares.</summary>
    bool[] FestTunnels()
    {
        if (festTun != null) return festTun;
        int w = W;
        var tun = new bool[cells];
        bool WallAt(int c) => c < 0 || c >= cells || wall[c];
        (int along, int across)[] axes = [(1, w), (w, 1)];
        foreach (var (along, across) in axes)
            for (int c = 0; c < cells; c++)
            {
                if (WallAt(c)) continue;
                int p = c - along;
                if (WallAt(p - across) && !WallAt(p) && WallAt(p + across)) continue; // middle of a tunnel
                int cnt = 0;
                for (int x = c; WallAt(x - across) && !WallAt(x) && WallAt(x + across); x += along) cnt++;
                if (cnt < 2) continue;
                bool g = false;
                for (int k = 0; k < cnt; k++) { g = g || goal[c + k * along]; tun[c + k * along] = true; }
                if (!g) tun[c] = false;
            }
        return festTun = tun;
    }

    /// <summary>Go festInefficient: a box just pushed to t (occ holds the child, the player's region is m3 == mpstamp).</summary>
    bool FestInefficient(int t)
    {
        if (goal[t] || tgt != null && tgt[t]) return false;
        if (FestTunnels()[t]) return true;
        int w = W;
        Span<int> d8 = [-w, -w + 1, 1, w + 1, w, w - 1, -1, -w - 1];
        var c = fiC ??= new int[8];
        for (int i = 0; i < 8; i++) c[i] = t + d8[i];
        for (int j = 0; j < 8; j += 2)
        {
            bool Wl(int i) => wall[c[i & 7]];
            bool Rc(int i) => InPR(c[i & 7]);
            bool Bx(int i) => occ[c[i & 7]];
            if (Wl(j + 2) && Wl(j + 3) && Wl(j + 5) && !Wl(j + 6) && (!Rc(j + 6) || Rc(j))) return true; // types 1 and 3
            if (Wl(j + 3) && Wl(j + 5) && Wl(j + 6) && !Wl(j + 2) && (!Rc(j + 2) || Rc(j))) return true; // types 2 and 4
            if (Wl(j + 3) && Wl(j + 5) && Bx(j + 2) && !Rc(j + 6)) return true; // type 5
            if (Wl(j + 3) && Wl(j + 5) && Bx(j + 6) && !Rc(j + 2)) return true; // type 6
        }
        return false;
    }
}
