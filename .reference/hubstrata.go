package main

// exp-686: hub strata. A pull tree's best state (frontier.best) sits on one
// unpacking branch (exp-683: our hubs share 81-100% of their box squares
// across styles, Festival's branches 45-71%). A stratum is a coarse
// signature of a pull node; the table keeps the best node (pull lane order,
// pfBetter) per signature, at most strataMax, so the hub lanes can try many
// distinct branches instead of one.
//
//	v0: hash(boxes on real goals, boxes per room of rooms.go, boxes in corridors)
//	v1: id of the node's ancestor at half its depth (its branch)
//
// -hub-strata-dump FILE (diagnostic, -strategy hub-strata): the hub-pull pull
// solver to -nodes N, then every stratum's best node of v0 and v1 (and the
// frontier.best baseline) as HUB blocks that -strategy tfwd reads.

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

var HubStrataDump string

// HubStrataPlans (exp-732, diagnostic): with -hub-strata-dump, also write
// each dumped tip as a soft plan PREFIX.N.json (N = block index in the dump,
// the tfwd -target-index): tip boxes on start squares as INIT, the others as
// arrivals, then the tip's tail macros (pullTail grouped per box).
var HubStrataPlans string

const strataMax = 64

// strata is a flat table: entry i is signature sig[i] with best node best[i].
type strata struct {
	ver    int
	sig    []uint64
	best   []int32
	failed []bool  // exp-686 race: the stratum's tip failed a trial (replaced first when full)
	goal   []bool  // real goals (the pull solver's root boxes)
	buf    []int32 // unpack scratch
	cnt    []int32 // per-room scratch (v0)
	added  int     // stratum creations (including replacements)
}

func newStrata(s *solver, ver int) *strata {
	t := &strata{ver: ver, goal: make([]bool, len(s.lv.Wall)), buf: make([]int32, s.n)}
	for _, g := range s.lv.Boxes {
		t.goal[g] = true
	}
	if s.rooms != nil {
		t.cnt = make([]int32, len(s.rooms.rooms)+1)
	} else {
		t.cnt = make([]int32, 1)
	}
	return t
}

// depthOf is the number of pull moves from the root to id.
func depthOf(s *solver, id int32) int {
	d := 0
	for p := s.parent[id]; p >= 0; p = s.parent[p] {
		d++
	}
	return d
}

func (t *strata) signature(s *solver, id int32) uint64 {
	if t.ver == 1 {
		d := depthOf(s, id)
		a := id
		for k := d - d/2; k > 0; k-- {
			a = s.parent[a]
		}
		return uint64(uint32(a))
	}
	s.unpack(id, t.buf)
	clear(t.cnt)
	on := 0
	for _, b := range t.buf {
		if t.goal[b] {
			on++
		}
		r := -1
		if s.rooms != nil {
			r = s.rooms.cell[b]
		}
		if r >= 0 {
			t.cnt[r+1]++
		} else {
			t.cnt[0]++ // corridor or room boundary
		}
	}
	h := uint64(14695981039346656037) ^ uint64(on)
	h *= 1099511628211
	for _, c := range t.cnt {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

// offer considers a new pull node for its stratum.
func (t *strata) offer(s *solver, id int32) {
	if id < 0 || s.dead[id] {
		return
	}
	sg := t.signature(s, id)
	at := -1
	for i, x := range t.sig {
		if x == sg {
			at = i
			break
		}
	}
	if at >= 0 {
		b := t.best[at]
		if !s.dead[b] && !pfBetter(s.pfBest[id], s.pfBest[b]) {
			return
		}
	} else if len(t.sig) >= strataMax {
		// Full: replace the worst stratum if the newcomer beats its best;
		// a failed stratum (race feedback) gives way to any new signature.
		w, wf := 0, -1
		for i := 1; i < len(t.best); i++ {
			if s.dead[t.best[i]] || !s.dead[t.best[w]] && pfBetter(s.pfBest[t.best[w]], s.pfBest[t.best[i]]) {
				w = i
			}
		}
		for i := range t.failed {
			if t.failed[i] && (wf < 0 || pfBetter(s.pfBest[t.best[wf]], s.pfBest[t.best[i]])) {
				wf = i
			}
		}
		if wf >= 0 {
			w = wf
		} else if !s.dead[t.best[w]] && !pfBetter(s.pfBest[id], s.pfBest[t.best[w]]) {
			return
		}
		at = w
	}
	if PullBestFeasible && !s.feasibleNode(id) {
		return
	}
	if at < 0 {
		t.sig = append(t.sig, sg)
		t.best = append(t.best, id)
		t.failed = append(t.failed, false)
		t.added++
		return
	}
	if t.sig[at] != sg {
		t.added++
	}
	t.sig[at], t.best[at], t.failed[at] = sg, id, false
}

// strataDumpRun is -strategy hub-strata: hub-pull's pull solver to -nodes, then the dump.
func strataDumpRun(rows []string, lv *Level, deadline time.Time, memLimit uint64) Result {
	if HubStrataDump == "" {
		return Result{Reason: "hub-strata needs -hub-strata-dump FILE"}
	}
	ps := newPullSolver(lv, deadline)
	ps.laneID = 9
	ps.pfOn, ps.owner, ps.bestSlot = true, FestXLane, FestXLane
	ps.pfSty = festXStyle()
	ps.rng = reviveSeed(0)
	ps.realRows = rows
	ps.budgetP, ps.trackedP = &Budget9, &Tracked9
	ps.nodeLimit = NodeBudget
	ps.strataT = []*strata{newStrata(ps, 0), newStrata(ps, 1)}
	t0 := time.Now()
	r := ps.solve(deadline, memLimit)
	if r.Solved {
		return r
	}
	f, err := os.Create(HubStrataDump)
	if err != nil {
		return Result{Reason: "hub-strata: " + err.Error()}
	}
	defer f.Close()
	goal := ps.strataT[0].goal
	nBlock := 0
	block := func(tag string, k int, sg uint64, id int32) {
		if HubStrataPlans != "" {
			tipPlanWrite(ps, lv, id, fmt.Sprintf("%s.%d.json", HubStrataPlans, nBlock))
		}
		nBlock++
		boxes := make([]int32, ps.n)
		ps.unpack(id, boxes)
		on := 0
		for _, b := range boxes {
			if goal[b] {
				on++
			}
		}
		p := ps.pfBest[id]
		fmt.Fprintf(f, "HUB strata=%s k=%d sig=%x id=%d depth=%d on_goal=%d reg=%d rooms=%d hot=%d dist=%d\n%s", tag, k, sg, id, depthOf(ps, id), on, p.reg, p.rooms, p.hot, p.dist, hubBoard(lv, boxes, ps.player[id]))
	}
	if b := ps.frontier.best; b >= 0 {
		block("base", 0, 0, b)
	}
	for _, t := range ps.strataT {
		for k := range t.sig {
			block(fmt.Sprintf("v%d", t.ver), k, t.sig[k], t.best[k])
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "STRATA nodes=%d stored=%d reason=%s secs=%.1f", ps.expandedN, len(ps.player), r.Reason, time.Since(t0).Seconds())
	for _, t := range ps.strataT {
		fmt.Fprintf(&sb, " v%d=%d/%d", t.ver, len(t.sig), t.added)
	}
	fmt.Fprintln(os.Stderr, sb.String())
	return Result{Reason: "hub-strata done", Nodes: int(ps.expandedN)}
}

// tipPlanWrite writes pull node id as a soft forward plan (exp-732 Gate 0):
// boxes on start squares as INIT, other boxes as arrivals (cell order), then
// the tail's pushes grouped into per-box macros.
func tipPlanWrite(ps *solver, lv *Level, id int32, path string) {
	boxes := make([]int32, ps.n)
	ps.unpack(id, boxes)
	clear(ps.occ)
	tail := ps.pullTail(id)
	start := make([]bool, len(lv.Wall))
	for _, b := range lv.Boxes {
		start[b] = true
	}
	fp := &festPlan{mode: 1}
	sorted := slices.Clone(boxes)
	slices.Sort(sorted)
	for _, b := range sorted {
		if start[b] {
			fp.e = append(fp.e, festEntry{from: -2, to: b})
			fp.initNum++
		}
	}
	for _, b := range sorted {
		if !start[b] {
			fp.e = append(fp.e, festEntry{from: -1, to: b})
		}
	}
	last := int32(-1)
	for _, pu := range tail {
		to := pu.From + ps.dirs[pu.Dir]
		if n := len(fp.e); last >= 0 && pu.From == last && fp.e[n-1].from >= 0 {
			fp.e[n-1].to = to
		} else {
			fp.e = append(fp.e, festEntry{from: pu.From, to: to})
		}
		last = to
	}
	writePlanFile(path, fp, lv)
}
