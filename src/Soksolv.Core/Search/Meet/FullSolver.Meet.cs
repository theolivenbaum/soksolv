using Soksolv.Search.Meet;

namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    readonly List<int> meetPending = new();
    int meetPosted;

    /// <summary>A new node: record it in the forward perimeter; if the backward perimeter holds it too, queue a meeting (port of the s.fwd block in Go add).</summary>
    void MeetAdd(int id, ReadOnlySpan<int> boxes, int player)
    {
        var net = opt.Net!;
        ulong k = net.Fwd.Key(boxes, player);
        net.Fwd.Add(k);
        if (meetPosted < MeetNet.MaxMeetPosts && net.Back.Has(k)) meetPending.Add(id);
    }

    /// <summary>Posts pending meetings (new nodes that hit the backward perimeter) with their forward paths. occ must be clear.</summary>
    void MeetPost(Span<int> cur)
    {
        var net = opt.Net!;
        foreach (int id in meetPending)
        {
            if (meetPosted >= MeetNet.MaxMeetPosts) break;
            meetPosted++;
            string prefix = Reconstruct(id);
            nodes.Unpack(id, cur);
            net.Back.Post(new Meeting { Boxes = cur.ToArray(), Player = nodes.Player(id), Prefix = prefix, At = MeetNet.EndCell(prefix, lv.Player, W), From = "full" });
        }
        meetPending.Clear();
    }

    /// <summary>Resolves meetings posted by the backward search: a posted state in this table joins our path to it with the backward pushes from it. occ must be clear.</summary>
    string? MeetCheckMail()
    {
        var net = opt.Net!;
        while (net.Fwd.Receive(opt.MeetOwner, out var m))
        {
            if (m.Boxes.Length != n) continue;
            bool ok = true;
            foreach (int b in m.Boxes) if (nodes.Fidx[b] < 0) { ok = false; break; }
            if (!ok) continue;
            for (int w = 0; w < nodes.Nw; w++) ckey[w] = 0;
            foreach (int b in m.Boxes) Flip(b);
            int id = nodes.Find(ckey, m.Player, nodes.HashOf(m.Boxes, m.Player), out _, out _);
            if (id < 0) continue;
            net.Fwd.Confirm();
            string prefix = Reconstruct(id);
            int at = MeetNet.EndCell(prefix, lv.Player, W);
            string tail = MeetNet.Complete(wall, W, m.Boxes, at, m.Tail);
            if (tail != "" || m.Tail.Length == 0) return prefix + tail;
        }
        return null;
    }
}
