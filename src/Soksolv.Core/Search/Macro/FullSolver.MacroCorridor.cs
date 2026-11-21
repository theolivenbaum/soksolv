namespace Soksolv.Search;

public sealed unsafe partial class FullSolver
{
    // ---- corridor flow (Go corridor_flow.go, CorridorFlow / exp-330) ----------------------------------------------------------------
    sealed class FlowCorridor { public int[] Cells = []; public int Goals; public int EndA, EndB; public bool IsCut; }
    List<FlowCorridor>? flowCorridors;
    List<int>?[]? corrsForCell;

    void InitCorridorFlow()
    {
        if (flowCorridors != null) return;
        flowCorridors = new();
        corrsForCell = new List<int>?[cells];
        var floor = FloorCells();
        var isFloor = new bool[cells];
        foreach (int c in floor) isFloor[c] = true;
        var visited = new bool[cells];
        var isCorridor = new bool[cells];
        var axisDir = new int[cells];
        int w = W;
        foreach (int c in floor)
        {
            bool hWall = wall[c - 1] && wall[c + 1];
            bool vWall = wall[c - w] && wall[c + w];
            if (hWall && !vWall && isFloor[c - w] && isFloor[c + w]) { isCorridor[c] = true; axisDir[c] = w; }
            else if (vWall && !hWall && isFloor[c - 1] && isFloor[c + 1]) { isCorridor[c] = true; axisDir[c] = 1; }
        }
        var seenCorr = new bool[cells];
        var queueL = new Queue<int>();
        foreach (int c in floor)
        {
            if (!isCorridor[c] || seenCorr[c]) continue;
            int ad = axisDir[c];
            int start = c;
            while (isCorridor[start - ad] && axisDir[start - ad] == ad) start -= ad;
            var corrCells = new List<int>();
            int curr = start;
            while (isCorridor[curr] && axisDir[curr] == ad) { seenCorr[curr] = true; corrCells.Add(curr); curr += ad; }
            int endA = start - ad, endB = curr;
            if (!isFloor[endA] || !isFloor[endB]) continue;
            // does removing this corridor disconnect endA and endB?
            Array.Clear(visited);
            foreach (int x in corrCells) visited[x] = true;
            queueL.Clear(); queueL.Enqueue(endA);
            visited[endA] = true;
            bool isCut = true;
            while (queueL.Count > 0)
            {
                int u = queueL.Dequeue();
                if (u == endB) { isCut = false; break; }
                foreach (int d in dirs)
                {
                    int nxt = u + d;
                    if (isFloor[nxt] && !visited[nxt]) { visited[nxt] = true; queueL.Enqueue(nxt); }
                }
            }
            int goalsN = 0;
            foreach (int x in corrCells) if (goal[x]) goalsN++;
            int idx = flowCorridors.Count;
            flowCorridors.Add(new FlowCorridor { Cells = [.. corrCells], Goals = goalsN, EndA = endA, EndB = endB, IsCut = isCut });
            foreach (int x in corrCells) (corrsForCell[x] ??= new()).Add(idx);
        }
    }

    /// <summary>Transit-capacity deadlock of the candidate box move to t (occ holds the child; m3 == mpstamp is the player's region after the push).</summary>
    bool CorridorFlowDeadlock(int t)
    {
        InitCorridorFlow();
        if (flowCorridors!.Count == 0) return false;
        var lst = corrsForCell![t];
        if (lst == null) return false;
        foreach (int corrIdx in lst)
        {
            var corr = flowCorridors[corrIdx];
            if (!corr.IsCut) continue;
            int k = 0, firstBox = -1, lastBox = -1;
            for (int idx = 0; idx < corr.Cells.Length; idx++)
                if (occ[corr.Cells[idx]]) { k++; if (firstBox == -1) firstBox = idx; lastBox = idx; }
            if (k == 0) continue;
            // 1. several boxes in a 1-wide corridor exceeding its goals, with the player not between them
            if (k >= 2 && k > corr.Goals)
            {
                bool playerBetween = false;
                for (int j = firstBox + 1; j < lastBox; j++)
                    if (InPR(corr.Cells[j])) { playerBetween = true; break; }
                if (!playerBetween) return true;
            }
            // 2. exactly one box on a non-goal in a goal-less cut corridor
            if (k == 1 && corr.Goals == 0)
            {
                int bIdx = firstBox;
                int bCell = corr.Cells[bIdx];
                if (!goal[bCell])
                {
                    bool playerAtA = InPR(corr.EndA), playerAtB = InPR(corr.EndB);
                    if (playerAtA && !playerAtB)
                    {
                        int nxt = bIdx + 1 < corr.Cells.Length ? corr.Cells[bIdx + 1] : corr.EndB;
                        if (occ[nxt] || wall[nxt]) return true;
                    }
                    else if (playerAtB && !playerAtA)
                    {
                        int nxt = bIdx - 1 >= 0 ? corr.Cells[bIdx - 1] : corr.EndA;
                        if (occ[nxt] || wall[nxt]) return true;
                    }
                }
            }
        }
        return false;
    }
}
