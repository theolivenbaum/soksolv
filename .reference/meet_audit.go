package main

// exp-545: a deliberately sequential geometry census, not a portfolio or a
// benchmark. Instrumentation never publishes extra states or changes pruning.
import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"soksolv/cluster"
	"soksolv/deadzone"
	"soksolv/meet"
	"soksolv/trace"
	"soksolv/verify"
)

var meetAuditOn bool
var meetAuditTargets string
var meetAuditPullNodes int64
var meetAuditPath string

func init() {
	flag.Int64Var(&meetAuditPullNodes, "meet-audit-pull-nodes", 0, "exp-545: optional separate pull expansion cap (0 uses -nodes)")
	flag.BoolVar(&meetAuditOn, "meet-audit", false, "exp-545: sequential macro/pull geometry census; requires -nodes, emits audit JSON")
	flag.StringVar(&meetAuditPath, "meet-audit-path", "", "exp-545: LURD file whose push states are looked up in the frozen forward perimeter, macro visit shadow and pull perimeter")
	flag.StringVar(&meetAuditTargets, "meet-audit-targets", "", "exp-545: prior audit JSON whose meeting witnesses are checked within two pushes")
}

const meetAuditCap = 12_000_000

type auditRef struct {
	Ref         uint64
	Node        int64
	PublishedAt int64
}
type auditHit struct {
	Key         uint64  `json:"key"`
	Kind        string  `json:"kind"`
	ForwardNode int64   `json:"forward_node"`
	PullNode    int64   `json:"pull_node"`
	Boxes       []int32 `json:"boxes"`
	Player      int32   `json:"player"`
	Verified    bool    `json:"verified"`
	Solution    string  `json:"solution,omitempty"`
	ForwardRef  uint64  `json:"forward_ref"`
	PullRef     uint64  `json:"pull_ref"`
	PullID      int32   `json:"pull_id"`
	Origin      string  `json:"origin,omitempty"`
}
type meetAuditState struct {
	BalancedHits       []auditHit `json:"balanced_hits"`
	ForwardFingerprint uint64     `json:"forward_fingerprint"`
	PullFingerprint    uint64     `json:"pull_fingerprint"`
	MultiPushVisits    int64      `json:"multi_push_visits"`
	table              *meet.Table
	shadow             map[uint64]auditRef
	forward            *solver
	scratch            []int32
	RawVisits          int64      `json:"raw_forward_visits"`
	ShadowKeys         int        `json:"shadow_keys"`
	Saturated          bool       `json:"saturated"`
	Probes             int64      `json:"backward_probes"`
	RealHits           int64      `json:"real_hits"`
	RealNoShadow       int64      `json:"real_not_macro_visited"` // exp-569: real hits on keys macro never visited (published by the embedded fa2)
	ShadowOnly         int64      `json:"shadow_only_hits"`
	NeverPublished     int64      `json:"never_published_hits"`
	Evicted            int64      `json:"evicted_hits"`
	Hits               []auditHit `json:"hits"`
	hitKeys            map[uint64]bool
}

func (a *meetAuditState) record(s *solver, id int32, cur []int32, i int, to, side int32) {
	a.RawVisits++
	if s.bprev[(to-s.dirs[side])*4+side] >= 0 {
		a.MultiPushVisits++
	}
	a.scratch = append(a.scratch[:0], cur...)
	a.scratch[i] = to
	k := a.table.Key(a.scratch, s.candNP)
	a.ForwardFingerprint = a.ForwardFingerprint*0x9e3779b97f4a7c15 ^ k
	if _, ok := a.shadow[k]; ok {
		return
	}
	if len(a.shadow) >= meetAuditCap {
		a.Saturated = true
		return
	}
	a.shadow[k] = auditRef{Ref: packRef(id, i, to, side), Node: s.expandedN, PublishedAt: -1}
}
func (a *meetAuditState) probe(s *solver, k uint64, boxes []int32, player int32, ref uint64, id int32) {
	a.Probes++
	a.PullFingerprint = a.PullFingerprint*0x9e3779b97f4a7c15 ^ k
	f, shadow := a.shadow[k]
	real := a.table.Has(k)
	if !shadow && !real {
		return
	}
	kind := "real"
	if real {
		a.RealHits++
		if !shadow {
			a.RealNoShadow++
		}
	} else {
		a.ShadowOnly++
		if _, ok := a.forward.refGet(k); ok {
			a.Evicted++
			kind = "evicted"
		} else {
			a.NeverPublished++
			kind = "never-published"
		}
	}
	if !shadow {
		f.Node = -1
	} else if real && f.PublishedAt >= 0 {
		f.Node = f.PublishedAt
	}
	h := auditHit{Key: k, Kind: kind, ForwardNode: f.Node, PullNode: s.expandedN, Boxes: slices.Clone(boxes), Player: player, ForwardRef: f.Ref, PullRef: ref, PullID: id}
	duplicate := false
	for _, x := range a.BalancedHits {
		if x.Key == k {
			duplicate = true
			break
		}
	}
	if !duplicate && (len(a.BalancedHits) < 16 || max(h.ForwardNode, h.PullNode) < max(a.BalancedHits[15].ForwardNode, a.BalancedHits[15].PullNode)) {
		a.BalancedHits = append(a.BalancedHits, h)
		slices.SortStableFunc(a.BalancedHits, func(x, y auditHit) int {
			a, b := max(x.ForwardNode, x.PullNode), max(y.ForwardNode, y.PullNode)
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
			return 0
		})
		if len(a.BalancedHits) > 16 {
			a.BalancedHits = a.BalancedHits[:16]
		}
	}
	if a.hitKeys[k] {
		return
	}
	// Keep first 16 witnesses of each kind, including rare shadow-only hits.
	count := 0
	for _, h := range a.Hits {
		if h.Kind == kind {
			count++
		}
	}
	if count >= 16 {
		return
	}
	a.hitKeys[k] = true
	a.Hits = append(a.Hits, auditHit{Key: k, Kind: kind, ForwardNode: f.Node, PullNode: s.expandedN, Boxes: slices.Clone(boxes), Player: player, ForwardRef: f.Ref, PullRef: ref, PullID: id})
}

type auditGeometry struct {
	Key           uint64  `json:"target_key"`
	Distance      int     `json:"distance"` // -1: no perimeter state within two legal pushes in either direction
	Direction     string  `json:"direction,omitempty"`
	MatchedBoxes  []int32 `json:"matched_boxes,omitempty"`
	MatchedPlayer int32   `json:"matched_player,omitempty"`
}

// Probe the true push graph in both directions, without deadlock pruning.
// Reverse edges are legal pulls, with the normalized player after the pull.
// A hash hit is also checked against an exact forward node/reference board.
func auditGeometryOf(lv *Level, fw *solver, table *meet.Table, h auditHit) auditGeometry {
	out := auditGeometry{Key: h.Key, Distance: -1}
	type state struct {
		b     []int32
		p     int32
		depth int
	}
	exact := func(b []int32, p int32) bool {
		k := table.Key(b, p)
		if !table.Has(k) {
			return false
		}
		if _, id, _ := fw.find(fw.pack(b), p); id >= 0 {
			return true
		}
		ref, ok := fw.refGet(k)
		if !ok {
			return false
		}
		par, i, to, _ := unpackRef(ref)
		bb := make([]int32, fw.n)
		fw.unpack(par, bb)
		bb[i] = to
		slices.Sort(bb)
		cp := slices.Clone(b)
		slices.Sort(cp)
		return slices.Equal(bb, cp)
	}
	for _, backwards := range []bool{false, true} {
		queue := []state{{slices.Clone(h.Boxes), h.Player, 0}}
		seen := map[uint64]bool{}
		for qi := 0; qi < len(queue); qi++ {
			st := queue[qi]
			k := table.Key(st.b, st.p)
			if seen[k] {
				continue
			}
			seen[k] = true
			if exact(st.b, st.p) {
				if out.Distance < 0 || st.depth < out.Distance {
					out.Distance = st.depth
					out.Direction = "push-from-witness"
					if backwards {
						out.Direction = "pull-from-witness"
					}
					out.MatchedBoxes = st.b
					out.MatchedPlayer = st.p
				}
				break
			}
			if st.depth == 2 {
				continue
			}
			occ := make([]bool, len(lv.Wall))
			for _, b := range st.b {
				occ[b] = true
			}
			reachable := auditReach(lv, occ, st.p)
			for i, b := range st.b {
				for _, d := range fw.dirs {
					to, p, support := b+d, b, b-d
					if backwards {
						p = b + 2*d
						support = b + d
					}
					if lv.Wall[to] || occ[to] || !reachable[support] || lv.Wall[p] || (p != b && occ[p]) {
						continue
					}
					bb := slices.Clone(st.b)
					bb[i] = to
					occ[b] = false
					occ[to] = true
					rr := auditReach(lv, occ, p)
					np := p
					for c, v := range rr {
						if v && int32(c) < np {
							np = int32(c)
						}
					}
					occ[to] = false
					occ[b] = true
					queue = append(queue, state{bb, np, st.depth + 1})
				}
			}
		}
	}
	return out
}
func auditReach(lv *Level, occ []bool, p int32) []bool {
	seen := make([]bool, len(lv.Wall))
	seen[p] = true
	q := []int32{p}
	for i := 0; i < len(q); i++ {
		for _, d := range []int32{-int32(lv.W), int32(lv.W), -1, 1} {
			c := q[i] + d
			if !lv.Wall[c] && !occ[c] && !seen[c] {
				seen[c] = true
				q = append(q, c)
			}
		}
	}
	return seen
}

func runMeetAudit(lv *Level, rows []string, mem uint64) error {
	if NodeBudget <= 0 {
		return fmt.Errorf("-meet-audit requires positive -nodes")
	}
	// Sharing learned deadlocks between the two sequential lanes would make the
	// second lane depend on which macro plan ran first. Keep lane-local learning.
	cluster.Shared.Store(nil)
	cluster.SharedPen.Store(nil)
	deadzone.Shared.Store(nil)
	deadline := time.Now().Add(24 * time.Hour)
	logSize := 10
	for uint64(8)<<(logSize+1) <= mem/32 {
		logSize++
	}
	fwTable := meet.New(len(lv.Wall), logSize, 6)
	a := &meetAuditState{table: fwTable, shadow: map[uint64]auditRef{}, hitKeys: map[uint64]bool{}}
	meet.Fwd.Store(fwTable)
	meet.Back.Store(nil)
	defer func() { meet.Fwd.Store(nil); meet.Back.Store(nil) }()
	fw := newSolverUntil(lv, deadline)
	fw.laneID = 1
	fw.rows = rows
	fw.tr = trace.New()
	fw.meetAudit = a
	a.forward = fw
	fr := fw.solve(deadline, mem)
	if fr.Solved {
		if err := verify.Check(rows, fr.Solution); err != nil {
			return fmt.Errorf("macro: %w", err)
		}
	}
	// Freeze Fwd but do not deliver meetings: census the same pull tree to its
	// node cap under either plan. All joins are reconstructed below.
	meet.Fwd.Store(nil)
	bkTable := meet.New(len(lv.Wall), logSize, 6)
	meet.Back.Store(bkTable)
	ps := newPullSolver(lv, deadline)
	ps.laneID = 6
	ps.budgetP = &Budget4
	ps.trackedP = &Tracked4
	ps.tr = trace.New()
	ps.realRows = rows
	ps.meetAudit = a
	if meetAuditPullNodes > 0 {
		ps.nodeLimit = meetAuditPullNodes
	}
	pr := ps.solve(deadline, mem)
	if pr.Solved {
		if err := verify.Check(rows, pr.Solution); err != nil {
			return fmt.Errorf("pull: %w", err)
		}
	}
	allHits := append(a.Hits, a.BalancedHits...)
	for i := range allHits {
		h := &allHits[i]
		var tail []meet.Push
		if h.PullID >= 0 {
			tail = ps.pullTail(h.PullID)
		} else {
			_, tail = ps.candTail(h.PullRef)
		}
		prefix := fw.reconstructRef(h.ForwardRef)
		if ref, ok := fw.refGet(h.Key); ok {
			prefix = fw.reconstructRef(ref)
		}
		if _, id, _ := fw.find(fw.pack(h.Boxes), h.Player); id >= 0 {
			prefix = fw.reconstruct(id)
		}
		h.Origin = "macro"
		sol := prefix + meet.Complete(lv.Wall, lv.W, h.Boxes, meet.EndCell(prefix, lv.Player, lv.W), tail)
		h.Verified = verify.Check(rows, sol) == nil
		if h.Verified {
			h.Solution = sol
		}
	}
	a.Hits, a.BalancedHits = allHits[:len(a.Hits)], allHits[len(a.Hits):]
	var geometry []auditGeometry
	if meetAuditTargets != "" {
		data, err := os.ReadFile(meetAuditTargets)
		if err != nil {
			return err
		}
		var prev struct {
			Audit meetAuditState `json:"audit"`
		}
		if err = json.Unmarshal(data, &prev); err != nil {
			return err
		}
		for _, h := range prev.Audit.BalancedHits {
			geometry = append(geometry, auditGeometryOf(lv, fw, fwTable, h))
		}
	}
	a.ShadowKeys = len(a.shadow)
	// Report endpoint coverage separately from eviction of previously published keys.
	never, evicted := 0, 0
	for k := range a.shadow {
		if !fwTable.Has(k) {
			if _, ok := fw.refGet(k); ok {
				evicted++
			} else {
				never++
			}
		}
	}
	samples := []map[string]any{}
	for _, id := range []int32{0, ps.frontier.best, int32(len(ps.player) / 2), int32(len(ps.player) - 1)} {
		if id >= 0 && int(id) < len(ps.player) {
			bb := make([]int32, ps.n)
			ps.unpack(id, bb)
			samples = append(samples, map[string]any{"id": id, "boxes": bb, "player": ps.player[id]})
		}
	}
	var pathRep map[string]any
	if meetAuditPath != "" {
		data, err := os.ReadFile(meetAuditPath)
		if err != nil {
			return err
		}
		pathRep = auditPath(lv, fwTable, bkTable, a.shadow, fw, ps, strings.TrimSpace(string(data)))
	}
	macroSol := ""
	if fr.Solved {
		macroSol = fr.Solution
	}
	out := map[string]any{"path": pathRep, "macro_solution": macroSol, "pull_samples": samples, "plan4": Plan4Macro, "node_limit": NodeBudget, "macro_nodes": fw.expandedN, "pull_nodes": ps.expandedN, "macro_solved": fr.Solved, "pull_solved": pr.Solved, "macro_reason": fr.Reason, "pull_reason": pr.Reason, "macro_plan_complete": fr.Stats.PlanComplete, "macro_plan_expanded": fr.Stats.PlanExpanded, "forward_refs": fw.refN, "shadow_never_published": never, "shadow_evicted": evicted, "audit": a, "geometry": geometry, "rows": rows, "width": lv.W, "height": lv.H}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func (a *meetAuditState) published(k uint64, node int64) {
	if f, ok := a.shadow[k]; ok && f.PublishedAt < 0 {
		f.PublishedAt = node
		a.shadow[k] = f
	}
}

// auditPath replays a LURD solution and, after every push, looks the state
// (boxes + normalized player) up in the frozen forward perimeter (F), the
// macro visit shadow (S: every boxSearch visit, published or not), the
// macro's stored nodes (N), the pull perimeter (B) and pull's stored nodes (P).
func auditPath(lv *Level, fwT, bkT *meet.Table, shadow map[uint64]auditRef, fw, ps *solver, lurd string) map[string]any {
	boxes := slices.Clone(lv.Boxes)
	occ := make([]bool, len(lv.Wall))
	for _, b := range boxes {
		occ[b] = true
	}
	p := lv.Player
	w := int32(lv.W)
	var flags []string
	lastF, lastS, firstB, firstP, lastN := -1, -1, -1, -1, -1
	near := map[int]int{}
	push := 0
	state := func() {
		rr := auditReach(lv, occ, p)
		np := p
		for c, v := range rr {
			if v && int32(c) < np {
				np = int32(c)
			}
		}
		k := fwT.Key(boxes, np)
		f := ""
		if fwT.Has(k) {
			f += "F"
			lastF = push
		}
		if r, ok := shadow[k]; ok {
			f += "S"
			lastS = push
			_ = r
		}
		if _, id, _ := fw.find(fw.pack(boxes), np); id >= 0 {
			f += "N"
			lastN = push
		}
		if bkT.Has(k) {
			f += "B"
			if firstB < 0 {
				firstB = push
			}
		}
		if _, id, _ := ps.find(ps.pack(boxes), np); id >= 0 {
			f += "P"
			if firstP < 0 {
				firstP = push
			}
		}
		if !fwT.Has(k) {
			// Near miss: a forward perimeter state within two legal pushes/pulls.
			if g := auditGeometryOf(lv, fw, fwT, auditHit{Boxes: slices.Clone(boxes), Player: np}); g.Distance > 0 {
				f += fmt.Sprint(g.Distance)
				near[g.Distance]++
			}
		}
		if f == "" {
			f = "."
		}
		flags = append(flags, f)
	}
	state()
	for _, c := range lurd {
		var d int32
		switch c {
		case 'u', 'U':
			d = -w
		case 'd', 'D':
			d = w
		case 'l', 'L':
			d = -1
		case 'r', 'R':
			d = 1
		default:
			continue
		}
		p += d
		if c >= 'A' && c <= 'Z' {
			for i, b := range boxes {
				if b == p {
					boxes[i] = p + d
				}
			}
			occ[p] = false
			occ[p+d] = true
			push++
			state()
		}
	}
	return map[string]any{"pushes": push, "flags": strings.Join(flags, " "), "last_forward": lastF, "last_shadow": lastS, "last_macro_node": lastN, "first_pull_perim": firstB, "first_pull_node": firstP, "near_forward": near}
}
