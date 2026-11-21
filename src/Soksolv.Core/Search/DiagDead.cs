namespace Soksolv.Search;

/// <summary>Diagonal-chain deadlocks (port of Go package diagdead, exp-459). Dead() allocates nothing.</summary>
public sealed class DiagDeadChecker
{
    public const int MaxChain = 24;
    readonly bool[] wall, goal;
    readonly int w;

    public DiagDeadChecker(bool[] wall, bool[] goal, int w) { this.wall = wall; this.goal = goal; this.w = w; }

    bool Blocked(bool[] occ, int x) => wall[x] || occ[x];

    bool Inner(bool[] occ, int x, int player)
    {
        if (x == player || wall[x] || occ[x]) return false;
        return Blocked(occ, x - w) && Blocked(occ, x + w) && Blocked(occ, x - 1) && Blocked(occ, x + 1);
    }

    /// <summary>Whether occ holds a diagonal-chain deadlock through the 8-neighbourhood of the box just pushed to t, with the player at player.</summary>
    public bool Dead(bool[] occ, int t, int player)
    {
        Span<int> seen = stackalloc int[16];
        int ns = 0;
        Span<int> offs = [-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1];
        foreach (int off in offs)
        {
            int n = t + off;
            if (!Inner(occ, n, player)) continue;
            for (int di = 0; di < 2; di++)
            {
                int delta = di == 0 ? 1 : -1;
                int dd = w + delta;
                int c0 = n, steps = 0;
                while (Inner(occ, c0 - dd, player))
                {
                    c0 -= dd;
                    if (++steps > MaxChain) break;
                }
                if (steps > MaxChain) continue;
                int key = c0 * 2 + (delta + 1) / 2;
                bool dup = false;
                for (int i = 0; i < ns; i++) if (seen[i] == key) { dup = true; break; }
                if (dup) continue;
                if (ns < seen.Length) seen[ns++] = key;
                if (ChainDead(occ, c0, delta, player)) return true;
            }
        }
        return false;
    }

    unsafe struct Chain
    {
        public int K;
        public bool CA, CB;
        public fixed bool Has[MaxChain + 1];
        public fixed bool Bad[MaxChain + 1];
        public fixed bool Ng[MaxChain];
        public fixed byte Memo[MaxChain * MaxChain];
    }

    unsafe bool ChainDead(bool[] occ, int c0, int delta, int player)
    {
        int dd = w + delta;
        int a = -w, b = -delta;
        Chain q = default;
        while (q.K < MaxChain && Inner(occ, c0 + q.K * dd, player)) q.K++;
        if (q.K == 0 || q.K >= MaxChain && Inner(occ, c0 + q.K * dd, player)) return false;
        int k = q.K;
        int last = c0 + (k - 1) * dd;
        bool wallStart = wall[c0 + a] && wall[c0 + b];
        int capA = c0 - dd;
        if (!wallStart && !Blocked(occ, capA)) return false;
        bool wallEnd = wall[last - a] && wall[last - b];
        int capB = last + dd;
        if (!wallEnd && !Blocked(occ, capB)) return false;
        q.CA = !wallStart && occ[capA] && !goal[capA];
        q.CB = !wallEnd && occ[capB] && !goal[capB];
        for (int m = 0; m < k; m++)
        {
            int x = c0 + m * dd;
            q.Ng[m] = !goal[x];
            AddPair(occ, &q, m, x + a);
            AddPair(occ, &q, m, x + b);
        }
        AddPair(occ, &q, k, last - a);
        AddPair(occ, &q, k, last - b);
        return Dead(&q, 0, k - 1);
    }

    unsafe void AddPair(bool[] occ, Chain* q, int m, int x)
    {
        if (occ[x]) { q->Has[m] = true; if (!goal[x]) q->Bad[m] = true; }
    }

    static unsafe bool SA(Chain* q, int a) => a == 0 ? q->CA : q->Ng[a - 1];
    static unsafe bool SB(Chain* q, int b) => b == q->K - 1 ? q->CB : q->Ng[b + 1];

    static unsafe bool Dead(Chain* q, int a, int b)
    {
        byte v = q->Memo[a * MaxChain + b];
        if (v != 0) return v == 1;
        bool r = Compute(q, a, b);
        q->Memo[a * MaxChain + b] = (byte)(r ? 1 : 2);
        return r;
    }

    static unsafe bool Compute(Chain* q, int a, int b)
    {
        bool sa = SA(q, a), sb = SB(q, b);
        bool obl = sa || sb;
        for (int m = a; m <= b + 1 && !obl; m++) obl = q->Bad[m];
        if (!obl) return false;
        for (int m = a; m <= b; m++)
        {
            if (q->Has[m + 1])
            {
                bool d = m == a ? sa || q->Bad[a] || q->Ng[a] : Dead(q, a, m - 1);
                if (!d) return false;
            }
            if (q->Has[m])
            {
                bool d = m == b ? sb || q->Bad[b + 1] || q->Ng[b] : Dead(q, m + 1, b);
                if (!d) return false;
            }
        }
        return true;
    }
}
