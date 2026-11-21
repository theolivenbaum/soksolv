using Soksolv.Search.Meet;

namespace Soksolv.Search.Reverse;

public sealed unsafe partial class ReverseSolver
{
    readonly List<int> meetPending = new();
    int meetPosted, meetAddPending;

    /// <summary>A new node: record it in the backward perimeter; if the forward perimeter holds it, queue a meeting.</summary>
    void MeetAdd(int id, ReadOnlySpan<int> boxes, int player)
    {
        var net = opt.Net!;
        ulong k = net.Back.Key(boxes, player);
        net.Back.AddBatched(ref meetAddPending, k);
        if (meetPosted < MeetNet.MaxMeetPosts && net.Fwd.Has(k)) meetPending.Add(id);
    }

    /// <summary>The pushes from node id's state to the solved position: the pulls on its chain to the root, reversed.</summary>
    MPush[] MeetTail(int id)
    {
        var res = new List<MPush>();
        for (; store.ParentOf(id) >= 0; id = store.ParentOf(id))
        {
            Pulled(id, out int b, out int t);
            for (int k = 0; k < 4; k++) if (b + dirs[k] == t) res.Add(new MPush(t, (sbyte)(k ^ 1)));
        }
        return res.ToArray();
    }

    void MeetPost(Span<int> cur)
    {
        var net = opt.Net!;
        foreach (int id in meetPending)
        {
            if (meetPosted >= MeetNet.MaxMeetPosts) break;
            meetPosted++;
            store.Unpack(id, cur);
            net.Fwd.Post(new Meeting { Boxes = cur.ToArray(), Player = store.PlayerOf(id), Tail = MeetTail(id), From = "reverse" });
        }
        meetPending.Clear();
    }

    /// <summary>Resolves meetings posted by forward searches: a posted state in this table joins their prefix with our pushes from it.</summary>
    string? MeetCheckMail()
    {
        var net = opt.Net!;
        while (net.Back.Receive(opt.BackOwner, out var m))
        {
            if (m.Boxes.Length != n) continue;
            bool ok = true;
            foreach (int b in m.Boxes) if (fidx[b] < 0) { ok = false; break; }
            if (!ok) continue;
            BuildKey(m.Boxes, key);
            int id = store.Find(key, m.Player, store.HashOfBoxes(m.Boxes, m.Player), out _, out _);
            if (id < 0) continue;
            net.Back.Confirm();
            var tl = MeetTail(id);
            string tail = MeetNet.Complete(wall, lv.W, m.Boxes, m.At, tl);
            if (tail != "" || tl.Length == 0) return m.Prefix + tail;
        }
        return null;
    }
}
