package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"
)

// exp-681 diagnostic (-refute-best N, macro lane only): at the end of the
// run, take the N stored nodes with the highest packed count and try to
// refute each one:
//   - frozen: a frozen box off goal (the solver's freeze test);
//   - hall: frozen-on-goal boxes as walls, the side-aware lone-box relation
//     (-fa2-hall-side semantics) leaves a loose box unmatched;
//   - exh: a node-bounded macro search of the sub-level (frozen-on-goal boxes
//     as walls) exhausts;
//   - parse: that sub-level is sealed or unbalanced.
// Results go to the macro trace extra "refute".

var RefuteBest int
var RefuteCap int64 = 20000
var RefuteRoot bool

func init() {
	flag.IntVar(&RefuteBest, "refute-best", 0, "exp-681 diag: at the end of a -strategy macro run, try to refute the N most-packed stored nodes (trace extra \"refute\")")
	flag.BoolVar(&RefuteRoot, "refute-root", false, "exp-681 diag: refute the given board (frozen, hall, exh) and print the verdict as JSON")
	flag.Int64Var(&RefuteCap, "refute-cap", 20000, "exp-681 diag: node cap of -refute-best's exhaustive sub-search")
}

type refuteRow struct {
	Packed  int    `json:"packed"`
	Verdict string `json:"verdict"`
	Frozen  int    `json:"frozen"`
	Def     int32  `json:"def"`
	Nodes   int64  `json:"nodes"`
	Reason  string `json:"reason"`
	Board   string `json:"board"`
	// Starved: a goal set G' whose suppliers number |G'|-1 (cells), when hall
	Starved []int32 `json:"starved,omitempty"`
}

// hallStarved returns the goal cells reachable by alternating paths from the
// open goals the maximum matching m leaves free: every box that can serve one
// is matched inside the set, so the set has fewer suppliers than goals.
func hallStarved(h *fa2Hall, bx []int32, m *hallM, player int32) []int32 {
	l := h.level(0)
	in := make([]bool, h.ng)
	var q []int32
	for g := 0; g < h.ng; g++ {
		if m.mg[g] < 0 && l.open[g>>6]>>(uint(g)&63)&1 != 0 {
			in[g] = true
			q = append(q, int32(g))
		}
	}
	free := len(q)
	for i := 0; i < len(q); i++ {
		g := q[i]
		for b, c := range bx {
			if h.walled(l, c) || h.row(l, c, player)[g>>6]>>(uint(g)&63)&1 == 0 {
				continue
			}
			if g2 := m.mb[b]; g2 >= 0 && !in[g2] {
				in[g2] = true
				q = append(q, g2)
			}
		}
	}
	if len(q) > 64 {
		q = q[:64]
	}
	out := []int32{int32(free)}
	for _, g := range q {
		out = append(out, h.goalAt[g])
	}
	return out
}

func refuteBest(s *solver) {
	if RefuteBest <= 0 || s == nil || len(s.actual) == 0 {
		return
	}
	n := len(s.player)
	ids := make([]int32, 0, n)
	for id := 0; id < n && id < len(s.actual); id++ {
		if !s.dead[id] {
			ids = append(ids, int32(id))
		}
	}
	slices.SortStableFunc(ids, func(a, b int32) int { return s.actual[b].packed - s.actual[a].packed })
	maxP := -1
	if len(ids) > 0 {
		maxP = s.actual[ids[0]].packed
	}
	var rows []refuteRow
	atMax, refMax := 0, 0
	cur := make([]int32, s.n)
	for _, id := range ids[:min(RefuteBest, len(ids))] {
		s.unpack(id, cur)
		r := refuteOne(s, cur, s.player[id], true)
		r.Packed = s.actual[id].packed
		if r.Packed == maxP {
			atMax++
			if r.Verdict != "" {
				refMax++
			}
		}
		rows = append(rows, r)
	}
	s.tr.SetExtra("refute", map[string]any{"max_packed": maxP, "at_max": atMax, "refuted_at_max": refMax, "rows": rows})
}

func refuteRootMain(lv *Level) {
	s := newSolverUntil(lv, time.Now().Add(time.Minute))
	r := refuteOne(s, slices.Clone(lv.Boxes), lv.Player, true)
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func refuteOne(s *solver, cur []int32, player int32, exh bool) refuteRow {
	var r refuteRow
	clear(s.occ)
	for _, c := range cur {
		s.occ[c] = true
	}
	var fz []int32
	off := false
	for _, c := range cur {
		o := false
		if s.frozen(c, &o) {
			if !s.lv.Goal[c] || o {
				off = true
			}
			fz = append(fz, c)
		}
	}
	clear(s.occ)
	r.Frozen = len(fz)
	r.Board = xsbBoard(s.lv, cur, player)
	if off {
		r.Verdict = "frozen"
		return r
	}
	isF := make(map[int32]bool, len(fz))
	for _, c := range fz {
		isF[c] = true
	}
	// sub-level rows: frozen boxes become walls
	var sb strings.Builder
	lv := s.lv
	isBox := map[int32]bool{}
	for _, c := range cur {
		isBox[c] = true
	}
	var lines []string
	for y := 1; y < lv.H-1; y++ {
		sb.Reset()
		for x := 1; x < lv.W-1; x++ {
			c := int32(y*lv.W + x)
			ch := byte(' ')
			switch {
			case lv.Wall[c] || isF[c]:
				ch = '#'
			case isBox[c] && lv.Goal[c]:
				ch = '*'
			case isBox[c]:
				ch = '$'
			case c == player && lv.Goal[c]:
				ch = '+'
			case c == player:
				ch = '@'
			case lv.Goal[c]:
				ch = '.'
			}
			sb.WriteByte(ch)
		}
		lines = append(lines, strings.TrimRight(sb.String(), " "))
	}
	sub, err := parseLevelRaw(lines)
	if err != nil {
		r.Verdict = "parse"
		return r
	}
	deadline := time.Now().Add(60 * time.Second)
	ss := newSolverUntil(sub, deadline)
	ss.laneID, ss.rows, ss.isolated = 1, lines, true
	// side-aware Hall at level 0 (no plan walls)
	{
		h := newFA2HallOpt(ss, &festPlan{}, true, false)
		if !h.off {
			bx := slices.Clone(sub.Boxes)
			m := hallM{mb: make([]int32, len(bx)), mg: make([]int32, h.ng)}
			m.reset()
			r.Def = h.solve(0, bx, &m, sub.Player)
			if r.Def > 0 {
				r.Starved = hallStarved(h, bx, &m, sub.Player)
			}
		}
		if r.Def > 0 {
			r.Verdict = "hall"
			return r
		}
	}
	if !exh {
		return r
	}
	ss.nodeLimit = RefuteCap
	old := NodeBudget
	NodeBudget = RefuteCap
	res := ss.solve(deadline, 4<<30)
	NodeBudget = old
	r.Nodes, r.Reason = ss.expandedN, res.Reason
	if !res.Solved && res.Reason == "exhausted" {
		r.Verdict = "exh"
	}
	return r
}
