using Soksolv.Search.Meet;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    /// <summary>Pull-lane owner of the Back mailboxes (Go PullLane = 3; owner 0 is the reverse lane).</summary>
    public const int PullOwner = 3;

    static MPush[] ToMPush(List<Push> l) { var a = new MPush[l.Count]; for (int i = 0; i < a.Length; i++) a[i] = new MPush(l[i].From, l[i].Dir); return a; }

    /// <summary>A new pull-tree node: record it in the backward perimeter; if the forward perimeter holds it, queue a meeting.</summary>
    void PullMeetAdd(int id, ReadOnlySpan<int> boxes, int player)
    {
        var net = opt.Net!;
        ulong k = net.Back.Key(boxes, player);
        net.Back.Add(k);
        if (meetPosted < MeetNet.MaxMeetPosts && net.Fwd.Has(k)) meetPending.Add(id);
    }

    /// <summary>Posts pending pull-tree hits with their tails to the forward searches. occ must be clear.</summary>
    void PullMeetPost(Span<int> cur)
    {
        var net = opt.Net!;
        if (cur.Length != n) { meetBuf ??= new int[n]; cur = meetBuf; }
        foreach (int id in meetPending)
        {
            if (meetPosted >= MeetNet.MaxMeetPosts) break;
            meetPosted++;
            nodes.Unpack(id, cur);
            net.Fwd.Post(new Meeting { Boxes = cur.ToArray(), Player = nodes.Player(id), Tail = ToMPush(PullTail(id)), From = "pull" });
        }
        meetPending.Clear();
    }

    /// <summary>Completes forward searchers' meetings: their prefix reaches a state this pull search stored; its tail finishes the level. occ must be clear.</summary>
    string? PullMeetCheckMail()
    {
        var net = opt.Net!;
        while (net.Back.Receive(opt.BackOwner, out var m))
        {
            if (m.Boxes.Length != n) continue;
            bool ok = true;
            foreach (int b in m.Boxes) if (nodes.Fidx[b] < 0) { ok = false; break; }
            if (!ok) continue;
            for (int w = 0; w < nodes.Nw; w++) ckey[w] = 0;
            foreach (int b in m.Boxes) Flip(b);
            int id = nodes.Find(ckey, m.Player, nodes.HashOf(m.Boxes, m.Player), out _, out _);
            if (id < 0) continue;
            var tail = ToMPush(PullTail(id));
            net.Back.Confirm();
            string t = MeetNet.Complete(wall, W, m.Boxes, m.At, tail);
            if (t != "" || tail.Length == 0) return m.Prefix + t;
        }
        return null;
    }
}
