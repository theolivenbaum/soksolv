namespace Soksolv.Search;

/// <summary>
/// Festival's PI-corral detection (Go corral_fest.go, exp-269): a corral grows from one floor component behind a box that touches the
/// player's component (or two touching components), adding a component only when a corral box can be pushed into it or aside into it;
/// it is a PI-corral when no corral box can be pushed outside and every push into it starts from a cell the player reaches now.
/// Same results as Go (whose per-box grow cache is only an optimisation).
/// </summary>
public sealed unsafe partial class FullSolver
{
    int[]? fcComp; uint[]? fcGen; uint fcStamp; int fcN;
    bool[] fcUsed = [], fcCand = [], fcGoal = [];
    uint[]? fcCb; uint fcInStamp;
    int[]? fcBoxesArr; int fcNBoxes;
    readonly List<int> fcCl = new(), fcCs = new(), fcCbl = new();
    readonly List<int>[] fcBlStore = new List<int>[0];
    List<int>?[] fcBl = [];
    bool[] fcBng = [];
    uint[] fcBdone = [];
    uint[]? fcBseen; uint fcBctr;
    int fcPc;

    int FcComp(int c) => fcGen![c] != fcStamp ? -1 : fcComp![c];
    bool FcIn(int c) => fcGen![c] == fcStamp && fcUsed[fcComp![c]];

    uint FestCorral(int player, uint st)
    {
        if (fcComp == null)
        {
            fcComp = new int[cells]; fcGen = new uint[cells]; fcBseen = new uint[cells]; fcCb = new uint[cells]; fcBoxesArr = new int[cells];
        }
        if (++fcStamp == 0) { Array.Clear(fcGen!); fcStamp = 1; }
        fcN = 0; fcNBoxes = 0;
        fcCl.Clear(); fcCs.Clear(); fcCs.Add(0);
        var goalList = new List<bool>();
        var q = cqueue;
        for (int c = 0; c < cells; c++)
        {
            if (occ[c]) fcBoxesArr![fcNBoxes++] = c;
            if (wall[c] || occ[c] || fcGen![c] == fcStamp) continue;
            int tail = 0; q[tail++] = c;
            fcGen[c] = fcStamp; fcComp![c] = fcN;
            bool hasGoal = goal[c];
            for (int i = 0; i < tail; i++)
                foreach (int d in dirs)
                {
                    int y = q[i] + d;
                    if (!wall[y] && !occ[y] && fcGen[y] != fcStamp) { fcGen[y] = fcStamp; fcComp[y] = fcN; hasGoal = hasGoal || goal[y]; q[tail++] = y; }
                }
            goalList.Add(hasGoal);
            for (int i = 0; i < tail; i++) fcCl.Add(q[i]);
            fcCs.Add(fcCl.Count);
            fcN++;
        }
        if (fcN > 250) return 0;
        if (fcGoal.Length < fcN) { fcGoal = new bool[fcN * 2]; fcUsed = new bool[fcN * 2]; fcCand = new bool[fcN * 2]; fcBng = new bool[fcN * 2]; fcBdone = new uint[fcN * 2]; Array.Resize(ref fcBl, fcN * 2); }
        for (int i = 0; i < fcN; i++) fcGoal[i] = goalList[i];
        int pc = fcPc = FcComp(player);
        Array.Clear(fcCand, 0, fcN);
        for (int bi = 0; bi < fcNBoxes; bi++)
        {
            int b = fcBoxesArr![bi];
            foreach (int d in dirs)
                if (FcComp(b + d) == pc)
                {
                    int o = FcComp(b - d);
                    if (o >= 0 && o != pc) fcCand[o] = true;
                }
        }
        for (int i = 0; i < fcN; i++)
            if (fcCand[i]) { uint r = FcTry(i, i, st); if (r != 0) return r; }
        // two-room corrals: components touching the same box, tried in (i, j) order
        var pairs = new List<ulong>();
        Span<int> nb = stackalloc int[4];
        for (int bi = 0; bi < fcNBoxes; bi++)
        {
            int b = fcBoxesArr![bi], k = 0;
            foreach (int d in dirs)
            {
                int c = FcComp(b + d);
                if (c < 0 || c == pc) continue;
                bool dup = false;
                for (int x = 0; x < k; x++) if (nb[x] == c) { dup = true; break; }
                if (dup) continue;
                nb[k++] = c;
            }
            for (int x = 0; x < k; x++)
                for (int y = x + 1; y < k; y++)
                {
                    int i = nb[x], j = nb[y];
                    if (i > j) (i, j) = (j, i);
                    if (fcCand[i] || fcCand[j]) pairs.Add((ulong)i << 32 | (uint)j);
                }
        }
        pairs.Sort();
        for (int k = 0; k < pairs.Count; k++)
        {
            if (k > 0 && pairs[k] == pairs[k - 1]) continue;
            uint r = FcTry((int)(pairs[k] >> 32), (int)(pairs[k] & 0xffffffff), st);
            if (r != 0) return r;
        }
        return 0;
    }

    uint FcTry(int c1, int c2, uint st)
    {
        Array.Clear(fcUsed, 0, fcN);
        fcUsed[c1] = true; fcUsed[c2] = true;
        if (!FcActive()) return 0;
        return FcExpand(st);
    }

    List<int> FcBoxes(int i)
    {
        if (fcBdone[i] == fcStamp) return fcBl[i]!;
        fcBdone[i] = fcStamp;
        if (++fcBctr == 0) { Array.Clear(fcBseen!); fcBctr = 1; }
        int w = W;
        Span<int> n8 = [1, -1, w, -w, w + 1, w - 1, -w + 1, -w - 1];
        var list = fcBl[i] ??= new List<int>();
        list.Clear();
        bool ng = false;
        for (int x = fcCs[i]; x < fcCs[i + 1]; x++)
        {
            int c = fcCl[x];
            foreach (int d in n8)
            {
                int y = c + d;
                if (occ[y] && fcBseen![y] != fcBctr) { fcBseen[y] = fcBctr; list.Add(y); ng = ng || !goal[y]; }
            }
        }
        fcBng[i] = ng;
        return list;
    }

    void FcMarkComp(int i)
    {
        foreach (int b in FcBoxes(i))
            if (fcCb![b] != fcInStamp) { fcCb[b] = fcInStamp; fcCbl.Add(b); }
    }

    void FcMark()
    {
        if (++fcInStamp == 0) { Array.Clear(fcCb!); fcInStamp = 1; }
        fcCbl.Clear();
        for (int i = 0; i < fcN; i++) if (fcUsed[i]) FcMarkComp(i);
    }

    bool FcActive()
    {
        FcMark();
        for (int i = 0; i < fcN; i++) if (fcUsed[i] && fcGoal[i]) return true;
        for (int i = 0; i < fcN; i++) if (fcUsed[i] && fcBng[i]) return true;
        return false;
    }

    bool FcOcc(int c) => wall[c] || fcCb![c] == fcInStamp; // relaxed board

    int FcGrow(int b)
    {
        bool touchIn = false, touchOut = false;
        foreach (int d in dirs)
        {
            if (FcIn(b + d)) touchIn = true;
            if (!FcOcc(b + d) && !FcIn(b + d)) touchOut = true;
        }
        if (!touchIn || !touchOut) return -1;
        foreach (int d in dirs)
        {
            int pu = b + d, de = b - d; // pusher, destination
            if (wall[pu] || FcOcc(de) || dist[de] < 0) continue;
            bool inside = !FcOcc(pu) && !FcIn(pu) && FcIn(de);
            bool aside = !FcOcc(pu) && !FcIn(pu) && !FcIn(de);
            if (inside) { int c = FcComp(pu); if (c >= 0 && c != fcPc && !fcUsed[c]) return c; }
            if (aside) { int c = FcComp(de); if (c >= 0 && c != fcPc && !fcUsed[c]) return c; }
        }
        return -1;
    }

    uint FcExpand(uint st)
    {
        while (true)
        {
            int add = -1, first = -1;
            foreach (int b in fcCbl)
            {
                if (first >= 0 && b > first) continue;
                int g = FcGrow(b);
                if (g >= 0) { add = g; first = b; }
            }
            if (add < 0) break;
            fcUsed[add] = true;
            FcMarkComp(add);
        }
        // no corral box can be pushed outside the corral (relaxed board)
        foreach (int b in fcCbl)
            foreach (int d in dirs)
            {
                int pu = b + d, de = b - d;
                if (!FcOcc(pu) && !FcOcc(de) && !FcIn(pu) && !FcIn(de) && dist[de] >= 0) return 0;
            }
        // every push into the corral starts where the player is now
        uint opt2 = ++cstamp;
        bool anyOpt = false;
        foreach (int b in fcCbl)
            foreach (int d in dirs)
            {
                int pu = b + d, de = b - d;
                if (wall[pu] || FcOcc(de) || FcIn(pu) || !FcIn(de) || dist[de] < 0 || FcOcc(pu)) continue;
                if (mark[pu] != st) return 0;
                cmark[b] = opt2;
                anyOpt = true;
            }
        return anyOpt ? opt2 : 0;
    }
}
