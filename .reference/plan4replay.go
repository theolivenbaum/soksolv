package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Plan4Replay (exp-689 diagnostic, planner twin of -fa2-replay): walk
// Festival's backward BASE_SEARCH witness (research/exp-689/scripts/witness.py
// output, Festival cells) through plan4's expand4 and print, per step, whether
// the step is a generated child, its rank, or the gate that rejected it
// (veto, noelig, notlargest, onesink, sep, match, alive, sinkdrop, beam,
// unreach). The walk forces Festival's step when plan4 does not generate it.
var Plan4Replay string
var Plan4ReplaySearch int

// exp-731: -plan4-replay-sol builds the witness from a known forward solution
// instead (p4StepsFromSol); -plan4-replay-out appends one JSON record per run.
var Plan4ReplaySol, Plan4ReplayOut, Plan4ReplayName string
var Plan4ReplayCommute bool

func init() {
	flag.BoolVar(&Plan4ReplayCommute, "plan4-replay-commute", false, "exp-731 diagnostic: when plan4 drops the witness step for sink moves, take one of those sinks if the witness later deletes that box on that base without moving it before (a commuted, equally valid chain) and go on")
	flag.StringVar(&Plan4ReplaySol, "plan4-replay-sol", "", "exp-731 diagnostic: with -plan4-replay -, the witness is the reversed LURD solution in this file (each box's first forward macro becomes its deletion)")
	flag.StringVar(&Plan4ReplayOut, "plan4-replay-out", "", "exp-731 diagnostic: append the replay's per-step JSON record to this file")
	flag.StringVar(&Plan4ReplayName, "plan4-replay-name", "", "exp-731 diagnostic: level name for the JSON record")
	flag.IntVar(&Plan4ReplaySearch, "plan4-replay-search", 0, "exp-689 diagnostic: with -plan4-replay, first run plan4 for N expansions and report which witness states its tree reached (and at which expansion)")
	flag.StringVar(&Plan4Replay, "plan4-replay", "", "exp-689 diagnostic: replay Festival's backward witness (witness.py file) through plan4's child generation")
}

// p4Want is the watched move of one replay step (planner4.rw).
type p4Want struct {
	from, to  int32
	del       bool
	reached   bool   // the pull BFS of box `from` reached `to`
	why       string // rejecting gate seen inside expand4
	sinkState string // node-level sink gate: ok | veto | noelig ("" = no free base)
}

func (w *p4Want) watchPull(p *planner4, to int32, used []uint64, sinks, sunk bool) {
	w.reached = true
	if !w.del {
		return
	}
	t := p.baseAt[to]
	switch {
	case t < 0:
		w.why = "notbase"
	case used[t>>6]>>(uint(t)&63)&1 != 0:
		w.why = "used"
	case !sinks:
		w.why = w.sinkState
		if w.why == "ok" || w.why == "" {
			w.why = "nosink"
		}
	case sunk && !Plan4MultiSink:
		w.why = "onesink"
	case !p.elimOK[t]:
		w.why = "notlargest"
	}
}

type p4ReplayStep struct {
	fexp     int
	from, to int32
	del      bool
	boxes    []int32
	player   int32
}

func readP4Witness(path string, lv *Level) ([]p4ReplayStep, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var fw int32
	tf := func(c int32) int32 { return (c/fw+1)*(fw+2) + c%fw + 1 }
	var out []p4ReplayStep
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "W ") {
			v, _ := strconv.Atoi(strings.TrimSpace(line[2:]))
			fw = int32(v)
			if int(fw)+2 != lv.W {
				return nil, fmt.Errorf("witness width %d, level width %d", fw, lv.W)
			}
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		h := strings.Fields(parts[0])
		var st p4ReplayStep
		st.fexp, _ = strconv.Atoi(h[1])
		st.from, st.to = -1, -1
		if h[0] == "S" {
			a, _ := strconv.Atoi(h[2])
			b, _ := strconv.Atoi(h[3])
			st.from, st.to, st.del = tf(int32(a)), tf(int32(b)), h[4] == "D"
		}
		for _, x := range strings.Fields(parts[1]) {
			v, _ := strconv.Atoi(x)
			st.boxes = append(st.boxes, tf(int32(v)))
		}
		slices.Sort(st.boxes)
		v, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		st.player = tf(int32(v))
		out = append(out, st)
	}
	return out, sc.Err()
}

// p4StepsFromSol (exp-731) reverses a forward LURD solution into plan4's
// witness: the root is the goal board with the final player, each forward
// macro (pdParse) is undone as one pull, and a box's first forward macro
// (its last pull) is a deletion on its start square, which removes it from
// later states. Boxes the solution never moves stay to the end.
func p4StepsFromSol(lv *Level, lurd string) []p4ReplayStep {
	path := pdParse(lv, lurd)
	n := len(path.moves)
	// box identity per forward macro: who moves, and is it its first move
	ids := map[int32]int{}
	for i, b := range lv.Boxes {
		ids[b] = i
	}
	who := make([]int, n)
	first := make([]bool, n)
	moved := make([]bool, len(lv.Boxes))
	for m, mv := range path.moves {
		id := ids[mv[0]]
		delete(ids, mv[0])
		ids[mv[1]] = id
		who[m], first[m] = id, !moved[id]
		moved[id] = true
	}
	// cells of every box in forward state k
	pos := make([][]int32, n+1)
	cur := slices.Clone(lv.Boxes)
	pos[0] = slices.Clone(cur)
	for m, mv := range path.moves {
		cur[who[m]] = mv[1]
		pos[m+1] = slices.Clone(cur)
	}
	deleted := make([]bool, len(lv.Boxes))
	state := func(k int) []int32 {
		var bs []int32
		for i, c := range pos[k] {
			if !deleted[i] {
				bs = append(bs, c)
			}
		}
		slices.Sort(bs)
		return bs
	}
	out := []p4ReplayStep{{fexp: n, from: -1, to: -1, boxes: state(n), player: path.player[n]}}
	for m := n - 1; m >= 0; m-- {
		st := p4ReplayStep{fexp: m, from: path.moves[m][1], to: path.moves[m][0], del: first[m]}
		if first[m] {
			deleted[who[m]] = true
		}
		st.boxes, st.player = state(m), path.player[m]
		out = append(out, st)
	}
	return out
}

// p4Commute (exp-731): node cur is a sink node and witness step k is not among
// its children. A deletion can move earlier in a backward chain (removing a
// box only frees cells), so if one of cur's sink children deletes box b on base
// t and the witness's next move of b is exactly that deletion, splice it in at
// k (b leaves the states in between) and report the new path.
func (p *planner4) p4Commute(cur int32, k int, path []p4ReplayStep) ([]p4ReplayStep, bool) {
	nd := &p.nodes[cur]
	for i := int32(0); i < nd.cLen; i++ {
		c := p.kids.at(nd.cOff, i)
		if c.base < 0 {
			continue
		}
		b, t := int32(c.from), int32(c.to)
		x := -1
		for y := k; y < len(path); y++ {
			if path[y].from == b {
				x = y
				break
			}
		}
		if x < 0 || !path[x].del || path[x].to != t {
			continue
		}
		out := slices.Clone(path[:k])
		cs := p4ReplayStep{fexp: path[x].fexp, from: b, to: t, del: true, player: int32(c.player)}
		for _, bb := range path[k-1].boxes {
			if bb != b {
				cs.boxes = append(cs.boxes, bb)
			}
		}
		out = append(out, cs)
		for y := k; y < len(path); y++ {
			if y == x {
				continue
			}
			st := path[y]
			if y < x {
				st.boxes = slices.DeleteFunc(slices.Clone(st.boxes), func(v int32) bool { return v == b })
			}
			out = append(out, st)
		}
		return out, true
	}
	return nil, false
}

// p4rStep is one replay step of the JSON record (exp-731).
type p4rStep struct {
	K      int    `json:"k"`
	Del    bool   `json:"del"`
	Gen    bool   `json:"gen"`
	Why    string `json:"why,omitempty"`
	Rank   int    `json:"rank"`
	N      int    `json:"n"`
	W      int    `json:"w"` // 0: a weight-0 advisor child (Festival-style plan), else 1
	CumW   int    `json:"cumw"`
	Sep    string `json:"sep,omitempty"` // first key field separating it from the rank-0 sibling
	InTree bool   `json:"in_tree"`       // the bounded plan4 search stored this state
	// Commuted: with -plan4-replay-commute, the step is a later deletion of the
	// solution moved forward to here (plan4 offered it while dropping the true park)
	Commuted bool `json:"commuted,omitempty"`
}

type p4rRec struct {
	Level     string    `json:"level"`
	Fest      bool      `json:"fest"`
	Steps     int       `json:"steps"`
	Search    int       `json:"search"`
	SearchExp int       `json:"search_expanded"`
	Done      int       `json:"search_done_at"`
	FirstMiss int       `json:"first_miss"` // first step not generated (-1: none)
	FirstOut  int       `json:"first_out"`  // first step the bounded search never stored (-1: none / no search)
	Stop      string    `json:"stop,omitempty"`
	Path      []p4rStep `json:"path"`
}

// p4KeyField names the first plan4 key field where a and b differ.
func p4KeyField(a, b uint64) string {
	switch {
	case a>>52 != b>>52:
		return "rem"
	case a>>42&1023 != b>>42&1023:
		return "regions"
	case a>>34&255 != b>>34&255:
		return "goals"
	case a>>26&255 != b>>26&255:
		return "rooms"
	case a != b:
		return "dist"
	}
	return "tie"
}

// plan4ReplayRun prints the replay and a summary line ("p4replay ...").
func plan4ReplayRun(s *solver) Result {
	var path []p4ReplayStep
	var err error
	if Plan4ReplaySol != "" {
		raw, e := os.ReadFile(Plan4ReplaySol)
		if e != nil {
			return Result{Reason: "plan4-replay: " + e.Error()}
		}
		path = p4StepsFromSol(s.lv, strings.TrimSpace(string(raw)))
	} else {
		path, err = readP4Witness(Plan4Replay, s.lv)
	}
	rec := &p4rRec{Level: Plan4ReplayName, Steps: len(path) - 1, Search: Plan4ReplaySearch, FirstMiss: -1, FirstOut: -1}
	if err != nil || len(path) < 2 {
		return Result{Reason: fmt.Sprintf("plan4-replay: %v (%d states)", err, len(path))}
	}
	var srch map[uint64]int // state hash -> expansion index in the plain plan4 search
	srchDone := -1
	if Plan4ReplaySearch > 0 {
		q := s.newPlanner4(time.Now().Add(time.Hour), Plan4ReplaySearch, false)
		q.maxExp = Plan4ReplaySearch
		if q.start4() {
			q.step4(0)
		}
		srch = make(map[uint64]int, len(q.nodes))
		for id := range q.nodes {
			srch[q.nodes[id].hash] = id
		}
		if q.done {
			srchDone = q.expanded
		}
		rec.SearchExp = q.expanded
		fmt.Printf("search: expanded=%d nodes=%d done=%v\n", q.expanded, len(q.nodes), q.done)
	}
	inTree, deepest := 0, 0
	p := s.newPlanner4(time.Now().Add(time.Hour), 1<<30, false)
	if !p.start4() {
		return Result{Reason: "plan4-replay: no root"}
	}
	w := int32(s.lv.W)
	yx := func(c int32) string { return fmt.Sprintf("%d,%d", c/w-1, c%w-1) }
	norm := func(boxes []int32, player int32) int32 {
		clear(p.s.occ)
		for _, b := range boxes {
			p.s.occ[b] = true
		}
		n, _ := p.flood(player)
		return n
	}
	cur := int32(-1)
	r0 := norm(path[0].boxes, path[0].player)
	for id := range p.nodes {
		if p.nodes[id].parent < 0 && p.nodes[id].player == r0 {
			cur = int32(id)
		}
	}
	rec.Fest = p.weighted()
	if cur < 0 {
		rec.Stop = "root-missing"
		p4rWrite(rec)
		for id := range p.nodes {
			fmt.Printf("root %d player %s\n", id, yx(p.nodes[id].player))
		}
		fmt.Printf("festival root player %s norm %s boxes=%d goals=%d\n", yx(path[0].player), yx(r0), len(path[0].boxes), len(s.goals))
		return Result{Reason: "plan4-replay: root not found"}
	}
	expanded := map[int32]bool{}
	for id := range p.nodes {
		expanded[int32(id)] = true // start4 expanded every root
	}
	why := map[string]int{}
	var nPull, nDel, genPull, genDel, premature, missedSink, firstMiss int
	firstMiss = -1
	cumW, filt := 0, 0
	firstDelF, firstDelOurs := -1, -1
	for k := 1; k < len(path); k++ {
		st := path[k]
		j, reason, sinkNode, want := p.p4Try(cur, st, expanded)
		commuted := false
		if j < 0 && sinkNode && Plan4ReplayCommute {
			if np2, ok := p.p4Commute(cur, k, path); ok {
				path = np2
				st = path[k]
				j, reason, sinkNode, want = p.p4Try(cur, st, expanded)
				commuted = true
			}
		}
		nd := &p.nodes[cur]
		np := norm(st.boxes, st.player)
		tag := "P"
		if st.del {
			tag = "D"
			nDel++
			if firstDelF < 0 {
				firstDelF = k
			}
		} else {
			nPull++
		}
		if sinkNode && firstDelOurs < 0 {
			firstDelOurs = k
		}
		if j >= 0 {
			if st.del {
				genDel++
			} else {
				genPull++
			}
		} else {
			why[tag+":"+reason]++
			if firstMiss < 0 {
				firstMiss = k
			}
		}
		if !st.del && sinkNode {
			premature++
		}
		if st.del && !sinkNode {
			missedSink++
		}
		adv := j >= 0 && int(cur) < len(p.nodeAdv) && j < int(p.nodeAdv[cur])
		if j < 0 || !adv {
			cumW++
		}
		rs := p4rStep{K: k, Del: st.del, Gen: j >= 0, Why: reason, Rank: j, N: int(nd.cLen), W: 1, CumW: cumW, Commuted: commuted}
		if adv {
			rs.W = 0
		}
		if j > 0 {
			rs.Sep = p4KeyField(p.kidKey(cur, int32(j)), p.kidKey(cur, 0))
			if p.weighted() && !adv && int(cur) < len(p.nodeAdv) && p.nodeAdv[cur] > 0 {
				rs.Sep = "adv:" + rs.Sep // a weight-0 advisor sibling comes first
			}
		}
		rec.Path = append(rec.Path, rs)
		res := fmt.Sprintf("rank %d/%d adv=%v cumw=%d", j, nd.cLen, adv, cumW)
		if j < 0 {
			res = fmt.Sprintf("NOT GENERATED (%s) n=%d", reason, nd.cLen)
			if sinkNode {
				res += " sinks:"
				for i := int32(0); i < nd.cLen; i++ {
					if c := p.kids.at(nd.cOff, i); c.base >= 0 {
						res += " " + yx(int32(c.from)) + ">" + yx(int32(c.to))
					}
				}
			}
		}
		fmt.Printf("k%-3d fexp %-6d %s %s -> %s nb=%d sinknode=%v sinkgate=%s %s\n", k, st.fexp, tag, yx(st.from), yx(st.to), len(st.boxes), sinkNode, want.sinkState, res)
		// take Festival's step (forced when not generated)
		if Plan4RevPlay || Plan4PullDead {
			var ch p2Child
			if j >= 0 {
				ch = p.kid(cur, int32(j))
			} else {
				ch = p2Child{from: st.from, to: st.to, player: np, base: -1}
				if st.del {
					ch.base = p.baseAt[st.to]
				}
			}
			r0, d0 := p.st4.revRej, p.st4.deadRej
			if p.popReject(cur, &ch) {
				filt++
				fmt.Printf("     FILTERED (rev %d dead %d)\n", p.st4.revRej-r0, p.st4.deadRej-d0)
			}
		}
		id, msg := p.p4Force(cur, st, j, np)
		if id < 0 {
			fmt.Printf("k%d: %s; stop\n", k, msg)
			rec.Stop = fmt.Sprintf("k%d %s", k, msg)
			break
		}
		if got := slices.Sorted(slices.Values(p.boxes(id))); !slices.Equal(got, st.boxes) {
			fmt.Printf("k%d: box set mismatch; stop\n", k)
			rec.Stop = fmt.Sprintf("k%d box set mismatch", k)
			break
		}
		cur = id
		if srch != nil {
			if at, ok := srch[p.nodes[id].hash]; ok {
				inTree++
				deepest = k
				rec.Path[len(rec.Path)-1].InTree = true
				fmt.Printf("     in search tree (node %d)\n", at)
			} else if rec.FirstOut < 0 {
				rec.FirstOut = k
			}
		}
	}
	if srch != nil {
		fmt.Printf("p4search witness_in_tree=%d deepest=%d of %d search_done_at=%d\n", inTree, deepest, len(path)-1, srchDone)
	}
	{
		used := p.usedBits(cur)
		off := 0
		for _, b := range p.boxes(cur) {
			if t := p.baseAt[b]; t < 0 || used[t>>6]>>(uint(t)&63)&1 != 0 {
				off++
				fmt.Printf("end box %s base=%d goal=%v\n", yx(b), t, s.lv.Goal[b])
			}
		}
		p.setOcc(cur)
		_, stp := p.flood(p.nodes[cur].player)
		fmt.Printf("end: boxes=%d off_base=%d player_start_reached=%v\n", p.nodes[cur].nb, off, p.mark[s.lv.Player] == stp)
	}
	rec.FirstMiss, rec.Done = firstMiss, srchDone
	p4rWrite(rec)
	ws := []string{}
	for k, v := range why {
		ws = append(ws, fmt.Sprintf("%s=%d", k, v))
	}
	slices.Sort(ws)
	fmt.Printf("p4replay filtered=%d cumw=%d steps=%d pulls=%d dels=%d gen_pulls=%d gen_dels=%d premature_sink=%d missed_sink=%d first_miss=%d first_fest_del=%d first_p4_sinknode=%d atstart=%v why=%s\n",
		filt, cumW, len(path)-1, nPull, nDel, genPull, genDel, premature, missedSink, firstMiss, firstDelF, firstDelOurs, p.atStart(cur), strings.Join(ws, ","))
	return Result{Reason: "replay"}
}

func p4rWrite(rec *p4rRec) {
	if Plan4ReplayOut == "" {
		return
	}
	if f, err := os.OpenFile(Plan4ReplayOut, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		b, _ := json.Marshal(rec)
		f.Write(append(b, '\n'))
		f.Close()
	}
}

// Plan4Cells (exp-689 diagnostic): histogram of plan4 feature cells
// (boxes left, regions, boxes on goals) over Festival's expanded backward
// nodes (bwd.txt E lines) and over plan4's first equally many expansions.
var Plan4Cells string

func init() {
	flag.StringVar(&Plan4Cells, "plan4-cells", "", "exp-689 diagnostic: compare feature-cell histograms of Festival's backward expansions (bwd.txt) and plan4's")
}

func plan4CellsRun(s *solver) Result {
	fh, err := os.Open(Plan4Cells)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	defer fh.Close()
	var fw int32
	tf := func(c int32) int32 { return (c/fw+1)*(fw+2) + c%fw + 1 }
	type cell struct{ nb, rg, gl int }
	fest := map[cell]int{}
	fset := map[string]int{} // Festival state -> expansion index
	order := []cell{}
	p := s.newPlanner4(time.Now().Add(time.Hour), 1<<30, false)
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	nf := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "W ") {
			v, _ := strconv.Atoi(strings.TrimSpace(line[2:]))
			fw = int32(v)
			continue
		}
		if !strings.HasPrefix(line, "I ") {
			continue
		}
		parts := strings.Split(line, "|")
		if h := strings.Fields(parts[0]); len(h) < 7 || h[6] != "E" {
			continue
		}
		clear(p.s.occ)
		c := cell{}
		for _, x := range strings.Fields(parts[1]) {
			v, _ := strconv.Atoi(x)
			b := tf(int32(v))
			p.s.occ[b] = true
			c.nb++
			if s.lv.Goal[b] {
				c.gl++
			}
		}
		c.rg = p.s.regions()
		{
			var bs []int32
			for b := range p.s.occ {
				if p.s.occ[b] {
					bs = append(bs, int32(b))
				}
			}
			v, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
			n, _ := p.flood(tf(int32(v)))
			fset[fmt.Sprint(bs, n)] = nf
		}
		if fest[c] == 0 {
			order = append(order, c)
		}
		fest[c]++
		nf++
	}
	q := s.newPlanner4(time.Now().Add(time.Hour), nf, false)
	q.maxExp = nf
	ovl := func() int {
		n := 0
		for id := range q.nodes {
			bs := slices.Sorted(slices.Values(q.boxes(int32(id))))
			if _, ok := fset[fmt.Sprint(bs, q.nodes[id].player)]; ok {
				n++
			}
		}
		return n
	}
	if q.start4() {
		for _, m := range []int{nf / 10, nf / 4, nf / 2, nf} {
			q.maxExp = m
			q.step4(0)
			fmt.Printf("overlap at %d: %d festival states among our nodes\n", m, ovl())
		}
		q.maxExp = nf
	}
	ours := map[cell]int{}
	for id := range q.nodes {
		n := &q.nodes[id]
		k := n.key
		c := cell{int(k >> 52), int(k >> 42 & 1023), int(k >> 34 & 255)}
		if ours[c] == 0 && fest[c] == 0 {
			order = append(order, c)
		}
		ours[c]++
	}
	fmt.Printf("cells festival_exp=%d ours_nodes=%d festival_cells=%d ours_cells=%d\n", nf, len(q.nodes), len(fest), len(ours))
	for _, c := range order {
		fmt.Printf("cell nb=%d rg=%d gl=%d fest=%d ours=%d\n", c.nb, c.rg, c.gl, fest[c], ours[c])
	}
	return Result{Reason: "cells"}
}

// p4Try expands node cur (re-expanding it to observe the gates) and finds
// Festival's step st among its children: rank j (-1 when not generated) and
// the rejecting gate.
func (p *planner4) p4Try(cur int32, st p4ReplayStep, expanded map[int32]bool) (int, string, bool, *p4Want) {
	want := &p4Want{from: st.from, to: st.to, del: st.del}
	p.rw = want
	p.expand4(cur)
	if !expanded[cur] {
		p.expanded++
		expanded[cur] = true
	}
	p.rw = nil
	nd := &p.nodes[cur]
	sinkNode := false
	for i := int32(0); i < nd.cLen; i++ {
		if p.kids.at(nd.cOff, i).base >= 0 {
			sinkNode = true
			break
		}
	}
	clear(p.s.occ)
	for _, b := range st.boxes {
		p.s.occ[b] = true
	}
	np, _ := p.flood(st.player)
	j := -1
	for i := int32(0); i < nd.cLen; i++ {
		c := p.kids.at(nd.cOff, i)
		if int32(c.from) == st.from && int32(c.to) == st.to && (c.base >= 0) == st.del && int32(c.player) == np {
			j = int(i)
			break
		}
	}
	reason := ""
	if j < 0 {
		switch {
		case !st.del && sinkNode && p.keepParks == 0:
			reason = "sinkdrop"
		case want.why != "":
			reason = want.why
		case !want.reached:
			reason = "unreach"
		case !st.del:
			reason = "beam"
		default:
			reason = "other"
		}
	}
	return j, reason, sinkNode, want
}

// p4Force adds Festival's step st below cur (child j, or a built child when
// not generated); -1 and a message when it cannot.
func (p *planner4) p4Force(cur int32, st p4ReplayStep, j int, np int32) (int32, string) {
	s := p.s
	var ch p2Child
	if j >= 0 {
		ch = p.kid(cur, int32(j))
	} else {
		ch = p2Child{from: st.from, to: st.to, player: np, base: -1}
		if st.del {
			ch.base = p.baseAt[st.to]
			if ch.base < 0 {
				return -1, "deletion on a non-base cell"
			}
		}
		nd := &p.nodes[cur]
		ch.hash = nd.hash ^ p.zb[ch.from] ^ p.zp[nd.player] ^ p.zp[ch.player]
		if ch.base >= 0 {
			ch.hash ^= p.zu[ch.base]
		} else {
			ch.hash ^= p.zb[ch.to]
		}
		clear(s.occ)
		for _, b := range st.boxes {
			s.occ[b] = true
		}
		goals, dist := 0, int32(0)
		for _, b := range st.boxes {
			if s.lv.Goal[b] {
				goals++
			}
			dist += p.distance[b]
		}
		ch.key = p.key(len(st.boxes), s.regions(), goals, dist, 0)
	}
	id := p.addNode(cur, &ch)
	if id < 0 {
		return -1, "duplicate state"
	}
	if p.weighted() {
		p.nodeW = append(p.nodeW, 0)
	}
	return id, ""
}

// Plan4FestAll (exp-689 diagnostic): every node Festival expanded in its
// backward search (bwd.txt E lines), checked against plan4's expand4 of its
// Festival parent: generated or the rejecting gate, by kind.
var Plan4FestAll string

func init() {
	flag.StringVar(&Plan4FestAll, "plan4-fest-all", "", "exp-689 diagnostic: check every Festival backward expansion (bwd.txt) against plan4's child generation")
}

func plan4FestAllRun(s *solver) Result {
	fh, err := os.Open(Plan4FestAll)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	defer fh.Close()
	var fw int32
	tf := func(c int32) int32 { return (c/fw+1)*(fw+2) + c%fw + 1 }
	p := s.newPlanner4(time.Now().Add(time.Hour), 1<<30, false)
	if !p.start4() {
		return Result{Reason: "no root"}
	}
	expanded := map[int32]bool{}
	for id := range p.nodes {
		expanded[int32(id)] = true
	}
	var ours []int32 // Festival node index -> our node (-1 unknown)
	why := map[string]int{}
	gen := map[string]int{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "W ") {
			v, _ := strconv.Atoi(strings.TrimSpace(line[2:]))
			fw = int32(v)
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		h := strings.Fields(parts[0])
		var st p4ReplayStep
		for _, x := range strings.Fields(parts[1]) {
			v, _ := strconv.Atoi(x)
			st.boxes = append(st.boxes, tf(int32(v)))
		}
		slices.Sort(st.boxes)
		v, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		st.player = tf(int32(v))
		if h[0] == "R" {
			clear(p.s.occ)
			for _, b := range st.boxes {
				p.s.occ[b] = true
			}
			r0, _ := p.flood(st.player)
			id := int32(-1)
			for x := range p.nodes {
				if p.nodes[x].parent < 0 && p.nodes[x].player == r0 {
					id = int32(x)
				}
			}
			ours = append(ours, id)
			continue
		}
		if h[0] != "I" || len(h) < 7 || h[6] != "E" {
			continue
		}
		par, _ := strconv.Atoi(h[3])
		a, _ := strconv.Atoi(h[4])
		b, _ := strconv.Atoi(h[5])
		st.from, st.to = tf(int32(a)), tf(int32(b))
		st.del = !slices.Contains(st.boxes, st.to)
		cur := int32(-1)
		if par < len(ours) {
			cur = ours[par]
		}
		if cur < 0 {
			ours = append(ours, -1)
			why["orphan"]++
			continue
		}
		j, reason, _, _ := p.p4Try(cur, st, expanded)
		tag := "P"
		if st.del {
			tag = "D"
		}
		gen[tag]++
		if j < 0 {
			why[tag+":"+reason]++
		}
		clear(p.s.occ)
		for _, x := range st.boxes {
			p.s.occ[x] = true
		}
		np, _ := p.flood(st.player)
		id, _ := p.p4Force(cur, st, j, np)
		if id < 0 {
			why["dup"]++
		}
		ours = append(ours, id)
	}
	ws := []string{}
	for k, v := range why {
		ws = append(ws, fmt.Sprintf("%s=%d", k, v))
	}
	slices.Sort(ws)
	fmt.Printf("p4festall pulls=%d dels=%d missing: %s\n", gen["P"], gen["D"], strings.Join(ws, " "))
	return Result{Reason: "festall"}
}
