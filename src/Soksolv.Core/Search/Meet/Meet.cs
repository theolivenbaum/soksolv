using System.Collections.Concurrent;

namespace Soksolv.Search.Meet;

/// <summary>Moves the box on From one cell in direction Dir (0..3 = u d l r). Port of meet.Push.</summary>
public struct MPush { public int From; public sbyte Dir; public MPush(int f, sbyte d) { From = f; Dir = d; } }

/// <summary>A state that hit the other side's perimeter (port of meet.Meeting).</summary>
public sealed class Meeting
{
    public int[] Boxes = [];       // sorted box cells
    public int Player;             // normalized player
    public string Prefix = "";     // posted by a forward search: LURD moves from the start to this state
    public int At;                 // with Prefix: the player's actual cell after it
    public MPush[] Tail = [];      // posted by the backward search: pushes from this state to the solved position
    public string From = "";
    public int Lane, Owner;
}

/// <summary>A backward-search state offered to forward "bridge" searches as an intermediate goal.</summary>
public sealed class Target
{
    public int[] Boxes = [];
    public int Player;             // normalized
    public MPush[] Tail = [];
    public int H;
}

/// <summary>
/// Perimeter: a direct-mapped lossy set of 64-bit state keys (4-way buckets), shared lock-free between searches
/// (port of meet.Table). A lost entry only costs a missed meeting: hits are confirmed exactly by the owner.
/// </summary>
public sealed class Table
{
    ulong[] cur;
    long adds;
    readonly int max;
    readonly ulong[] zbox, zpl;
    readonly ConcurrentQueue<Meeting>[] mail;
    readonly int[] mailCount;
    public long Posted, Confirmed, Dropped;
    const int MailboxCap = 64;

    public Table(int cells, int logSize, int owners)
    {
        max = 1 << logSize;
        zbox = new ulong[cells]; zpl = new ulong[cells];
        cur = new ulong[Math.Min(1 << 16, max)];
        mail = new ConcurrentQueue<Meeting>[owners]; mailCount = new int[owners];
        for (int i = 0; i < owners; i++) mail[i] = new ConcurrentQueue<Meeting>();
        ulong x = 0x9E3779B97F4A7C15UL;
        ulong Next() { x += 0x9E3779B97F4A7C15UL; ulong z = x; z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9UL; z = (z ^ (z >> 27)) * 0x94D049BB133111EBUL; return z ^ (z >> 31); }
        for (int c = 0; c < cells; c++) { zbox[c] = Next(); zpl[c] = Next(); }
    }

    public long Bytes => (long)Volatile.Read(ref cur).Length * 8;
    public long Adds => Interlocked.Read(ref adds);

    /// <summary>The state's 64-bit key (never 0). boxes: cells in any order.</summary>
    public ulong Key(ReadOnlySpan<int> boxes, int player)
    {
        ulong k = zpl[player];
        foreach (int b in boxes) k ^= zbox[b];
        return k | 1;
    }
    /// <summary>Incremental form: key of a state given the xor of box keys.</summary>
    public ulong BoxKey(int cell) => zbox[cell];
    public ulong PlayerKey(int cell) => zpl[cell];

    public void Add(ulong k)
    {
        var sl = Volatile.Read(ref cur);
        Insert(sl, k);
        long n = Interlocked.Increment(ref adds);
        if (n > sl.Length / 2 && sl.Length < max)
        {
            var next = new ulong[Math.Min(8 * sl.Length, max)];
            for (int i = 0; i < sl.Length; i++) { ulong x = Volatile.Read(ref sl[i]); if (x != 0) Insert(next, x); }
            Interlocked.CompareExchange(ref cur, next, sl);
        }
    }

    static void Insert(ulong[] sl, ulong k)
    {
        int bas = (int)(k & (ulong)(sl.Length - 1) & ~3UL);
        for (int w = 0; w < 4; w++)
        {
            ulong x = Volatile.Read(ref sl[bas + w]);
            if (x == k) return;
            if (x == 0 && Interlocked.CompareExchange(ref sl[bas + w], k, 0) == 0) return;
        }
        Volatile.Write(ref sl[bas + (int)(k >> 62)], k);
    }

    public bool Has(ulong k)
    {
        var sl = Volatile.Read(ref cur);
        int bas = (int)(k & (ulong)(sl.Length - 1) & ~3UL);
        for (int w = 0; w < 4; w++) if (Volatile.Read(ref sl[bas + w]) == k) return true;
        return false;
    }

    /// <summary>Offers a meeting to every owner without blocking (a full mailbox drops it).</summary>
    public void Post(Meeting m)
    {
        Interlocked.Increment(ref Posted);
        for (int i = 0; i < mail.Length; i++)
        {
            if (Volatile.Read(ref mailCount[i]) >= MailboxCap) { Interlocked.Increment(ref Dropped); continue; }
            Interlocked.Increment(ref mailCount[i]);
            mail[i].Enqueue(m);
        }
    }

    public bool Receive(int owner, out Meeting m)
    {
        if (mail[owner].TryDequeue(out m!)) { Interlocked.Decrement(ref mailCount[owner]); return true; }
        m = null!; return false;
    }
    public void Confirm() => Interlocked.Increment(ref Confirmed);
}

/// <summary>The perimeter network of one portfolio run: Back = backward searches' states, Fwd = forward searches' states.</summary>
public sealed class MeetNet
{
    public readonly Table Back, Fwd;
    public readonly Target?[] Best = new Target?[7];   // reverse lanes 0-2, pull-FESS lane 3, festb lanes 4, 5, dive 6
    public MeetNet(int cells, int backOwners = 4, int fwdOwners = 4, int logSize = 24)
    {
        Back = new Table(cells, logSize, backOwners);
        Fwd = new Table(cells, logSize, fwdOwners);
    }
    public void StoreBest(int i, Target t) => Volatile.Write(ref Best[i], t);
    public Target? GetBest(int i) => Volatile.Read(ref Best[i]);

    public const int MaxMeetPosts = 2000;

    /// <summary>
    /// LURD moves that make the given pushes, starting with the boxes on boxes and the player on player, walking between
    /// pushes by BFS ("" if some push can't be reached; callers verify the joined solution anyway). Port of meet.Complete.
    /// </summary>
    public static string Complete(bool[] wall, int w, ReadOnlySpan<int> boxes, int player, ReadOnlySpan<MPush> pushes)
    {
        int[] dirs = [-w, w, -1, 1];
        var occ = new bool[wall.Length];
        foreach (int b in boxes) occ[b] = true;
        var prev = new sbyte[wall.Length]; var seen = new uint[wall.Length]; uint stamp = 0;
        var sb = new System.Text.StringBuilder();
        int p = player;
        var q = new List<int>(); var walk = new List<char>();
        foreach (var pu in pushes)
        {
            int target = pu.From - dirs[pu.Dir];
            stamp++;
            q.Clear(); q.Add(p); seen[p] = stamp;
            for (int i = 0; i < q.Count && seen[target] != stamp; i++)
            {
                int c = q[i];
                for (int k = 0; k < 4; k++)
                {
                    int nb = c + dirs[k];
                    if (seen[nb] != stamp && !wall[nb] && !occ[nb]) { seen[nb] = stamp; prev[nb] = (sbyte)k; q.Add(nb); }
                }
            }
            int to = pu.From + dirs[pu.Dir];
            if (seen[target] != stamp || wall[to] || occ[to] || !occ[pu.From]) return "";
            walk.Clear();
            for (int c = target; c != p; c -= dirs[prev[c]]) walk.Add("udlr"[prev[c]]);
            for (int i = walk.Count - 1; i >= 0; i--) sb.Append(walk[i]);
            sb.Append("UDLR"[pu.Dir]);
            occ[pu.From] = false; occ[to] = true;
            p = pu.From;
        }
        return sb.ToString();
    }

    /// <summary>The player's cell after walking moves from start on a grid w cells wide.</summary>
    public static int EndCell(string moves, int start, int w)
    {
        int p = start;
        foreach (char ch in moves)
            switch (char.ToLowerInvariant(ch)) { case 'u': p -= w; break; case 'd': p += w; break; case 'l': p--; break; case 'r': p++; break; }
        return p;
    }
}
