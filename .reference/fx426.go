package main

// exp-426 failure forensics (data prep, off unless -fx-sol is set): after a single-strategy lane ends,
// compare its stored/expanded states with a known solution and write a plain-text report.

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"soksolv/reverse"
)

var FXSol, FXOut string

func init() {
	flag.StringVar(&FXSol, "fx-sol", "", "exp-426: file holding the verified LURD solution; enables the end-of-lane forensics report")
	flag.StringVar(&FXOut, "fx-out", "fx", "exp-426: output prefix for forensics reports")
}

type fxSolution struct {
	lv     *Level
	states [][]int32      // states[k]: sorted boxes after k macros (0 = start)
	keys   map[string]int // box-set key -> k
	macros [][3]int32     // per macro: box from, to, pushes
}

func fxKey(sorted []int32) string {
	b := make([]byte, 0, 2*len(sorted))
	for _, c := range sorted {
		b = binary.LittleEndian.AppendUint16(b, uint16(c))
	}
	return string(b)
}

func fxLoadSolution(lv *Level) *fxSolution {
	raw, err := os.ReadFile(FXSol)
	if err != nil {
		return nil
	}
	lurd := strings.TrimSpace(string(raw))
	fs := &fxSolution{lv: lv, keys: map[string]int{}}
	dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
	boxes := slices.Clone(lv.Boxes)
	snap := func() {
		c := slices.Clone(boxes)
		slices.Sort(c)
		fs.keys[fxKey(c)] = len(fs.states)
		fs.states = append(fs.states, c)
	}
	snap()
	p := lv.Player
	curFrom, curTo, curN := int32(-1), int32(-1), int32(0)
	flush := func() {
		if curN > 0 {
			fs.macros = append(fs.macros, [3]int32{curFrom, curTo, curN})
			snap()
		}
		curN = 0
	}
	for i := 0; i < len(lurd); i++ {
		c := lurd[i]
		d := dirs[c|0x20]
		if c >= 'A' && c <= 'Z' {
			from, to := p+d, p+2*d
			if curN > 0 && curTo == from {
				curTo = to
				curN++
			} else {
				flush()
				curFrom, curTo, curN = from, to, 1
			}
			j := slices.Index(boxes, from)
			boxes[j] = to
		}
		p += d
	}
	flush()
	return fs
}

// per-run expansion bookkeeping (single lane per process under -strategy)
var (
	fxExpanded []bool
	fxCellExp  = map[featureCell]int{}
	fxExpSeq   []int32
)

func fxInstall() {
	if FXSol == "" {
		return
	}
	DebugExpand = func(s *solver, e fessEntry) {
		for int(e.node) >= len(fxExpanded) {
			fxExpanded = append(fxExpanded, make([]bool, len(fxExpanded)+1024)...)
		}
		fxExpanded[e.node] = true
		fxCellExp[s.actual[e.node]]++
		fxExpSeq = append(fxExpSeq, e.node)
	}
}

func fxAscii(lv *Level, boxes []int32, player int32) string {
	isBox := map[int32]bool{}
	for _, b := range boxes {
		isBox[b] = true
	}
	var sb strings.Builder
	for y := 1; y < lv.H-1; y++ {
		row := make([]byte, 0, lv.W)
		for x := 1; x < lv.W-1; x++ {
			c := int32(y*lv.W + x)
			ch := byte(' ')
			switch {
			case lv.Wall[c]:
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
			row = append(row, ch)
		}
		sb.WriteString(strings.TrimRight(string(row), " "))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (fs *fxSolution) onGoals(b []int32) int {
	n := 0
	for _, c := range b {
		if fs.lv.Goal[c] {
			n++
		}
	}
	return n
}

func (fs *fxSolution) offStart(b []int32) int {
	st := map[int32]bool{}
	for _, c := range fs.states[0] {
		st[c] = true
	}
	n := 0
	for _, c := range b {
		if !st[c] {
			n++
		}
	}
	return n
}

func fxReport(lane string, s *solver, res Result) {
	fxTraceEnd(s)
	if lane == "macro" && !res.Solved {
		refuteBest(s)
	}
	if FXSol == "" || s == nil {
		return
	}
	real, err := parseLevel(s.realRowsOr())
	if err != nil {
		return
	}
	fs := fxLoadSolution(real)
	if fs == nil {
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "lane %s solved=%v reason=%s expanded=%d stored=%d macros_in_solution=%d\n", lane, res.Solved, res.Reason, s.expandedN, len(s.player), len(fs.macros))
	if s.tr != nil {
		sn := s.tr.Snapshot()
		fmt.Fprintf(&sb, "trace end=%s nodes=%d best_h=%d best_packed=%d h0=%d last_improve_ms=%d extra=%v\n", sn.End, sn.Nodes, sn.BestH, sn.BestPacked, sn.H0, sn.LastImprove, sn.Extra)
	}
	if s.plan != nil {
		fmt.Fprintf(&sb, "plan phases=%d complete=%v parkings=%d remaining=%d\n", len(s.plan.phases), s.plan.complete, s.plan.parkings, s.plan.remaining)
	}
	if s.basin != nil {
		fmt.Fprintf(&sb, "basin sink_room=%d goal=%d\n", s.basin.sink, s.basin.goal)
	}
	n := len(s.player)
	cur := make([]int32, s.n)
	type hit struct {
		id    int32
		count int
	}
	hits := map[int]*hit{}
	for id := int32(0); int(id) < n; id++ {
		if s.dead[id] {
			continue
		}
		s.unpack(id, cur)
		c := slices.Clone(cur)
		slices.Sort(c)
		if k, ok := fs.keys[fxKey(c)]; ok {
			h := hits[k]
			if h == nil {
				hits[k] = &hit{id, 1}
			} else {
				h.count++
				if (fxExpanded != nil && int(id) < len(fxExpanded) && fxExpanded[id] && !(int(h.id) < len(fxExpanded) && fxExpanded[h.id])) || s.w[id] < s.w[h.id] {
					h.id = id
				}
			}
		}
	}
	isExp := func(id int32) bool { return int(id) < len(fxExpanded) && fxExpanded[id] }
	ks := make([]int, 0, len(hits))
	for k := range hits {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	maxStored, maxExp, minStored := -1, -1, 1<<30
	firstUnexp := -1
	for _, k := range ks {
		maxStored = max(maxStored, k)
		minStored = min(minStored, k)
		if isExp(hits[k].id) {
			maxExp = max(maxExp, k)
		} else if firstUnexp < 0 {
			firstUnexp = k
		}
	}
	fmt.Fprintf(&sb, "solution states (box sets after k macros; 0=start, %d=end) stored: %d distinct; max stored k=%d, max expanded k=%d, min stored k=%d, first stored-but-unexpanded k=%d\n", len(fs.macros), len(hits), maxStored, maxExp, minStored, firstUnexp)
	// contiguous suffix from the end
	suf := 0
	for k := len(fs.macros); k >= 0 && hits[k] != nil; k-- {
		suf++
	}
	fmt.Fprintf(&sb, "contiguous solution suffix stored from the end: %d states\n", suf)
	if fs2 := firstUnexp; fs2 >= 0 {
		hh := hits[fs2]
		fmt.Fprintf(&sb, "first unexpanded solution state k=%d node=%d w=%d g=%d cell(packed,conn,roomconn,oop)=%v bucket_cell=%v best_cell=%v\n", fs2, hh.id, s.w[hh.id], s.g[hh.id], s.actual[hh.id], s.frontier.cells[s.bucket[hh.id]], s.actual[max(s.frontier.best, 0)])
	}
	// weights of expanded nodes
	var ws []int
	for _, id := range fxExpSeq {
		ws = append(ws, int(s.w[id]))
	}
	pct := func(a []int, p int) int {
		if len(a) == 0 {
			return 0
		}
		b := slices.Clone(a)
		slices.Sort(b)
		return b[(len(b)-1)*p/100]
	}
	last := ws
	if len(last) > 5000 {
		last = ws[len(ws)-5000:]
	}
	fmt.Fprintf(&sb, "weight of expanded nodes: p0=%d p50=%d p90=%d max=%d ; last 5000 expansions p50=%d p90=%d max=%d\n", pct(ws, 0), pct(ws, 50), pct(ws, 90), pct(ws, 100), pct(last, 50), pct(last, 90), pct(last, 100))
	var ws2 []int
	for id := 0; id < n; id++ {
		ws2 = append(ws2, int(s.w[id]))
	}
	fmt.Fprintf(&sb, "weight of all stored nodes: p50=%d p90=%d max=%d\n", pct(ws2, 50), pct(ws2, 90), pct(ws2, 100))
	// expansions per cell
	type ce struct {
		c featureCell
		n int
	}
	var cells []ce
	for c, k := range fxCellExp {
		cells = append(cells, ce{c, k})
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].n > cells[j].n })
	fmt.Fprintf(&sb, "expansions per feature cell (packed,conn,roomconn,oop): %d cells used; top:\n", len(cells))
	for i := 0; i < len(cells) && i < 12; i++ {
		fmt.Fprintf(&sb, "  %v x%d\n", cells[i].c, cells[i].n)
	}
	// per-state table
	sb.WriteString("k\tstored\tnodes\tnode\texpanded\tw\tg\tcell\tgoals\n")
	for _, k := range ks {
		h := hits[k]
		fmt.Fprintf(&sb, "%d\t1\t%d\t%d\t%v\t%d\t%d\t%v\t%d\n", k, h.count, h.id, isExp(h.id), s.w[h.id], s.g[h.id], s.actual[h.id], fs.onGoals(fs.states[k]))
	}
	// best state
	if b := s.frontier.best; b >= 0 && int(b) < n {
		s.unpack(b, cur)
		var h int32
		for _, c := range cur {
			h += s.dist[c]
		}
		fmt.Fprintf(&sb, "BEST node=%d cell=%v h=%d boxes_on_goals=%d/%d off_start=%d w=%d g=%d expanded=%v\n", b, s.actual[b], h, fs.onGoals(cur), s.n, fs.offStart(cur), s.w[b], s.g[b], isExp(b))
		sb.WriteString(fxAscii(s.lv, cur, s.player[b]))
	}
	if s.pull {
		// pull lane: closest-to-start stored state
		bestOff, bestID := 1<<30, int32(-1)
		for id := int32(0); int(id) < n; id++ {
			if s.dead[id] {
				continue
			}
			s.unpack(id, cur)
			if o := fs.offStart(cur); o < bestOff {
				bestOff, bestID = o, id
			}
		}
		if bestID >= 0 {
			s.unpack(bestID, cur)
			fmt.Fprintf(&sb, "PULL closest-to-start stored node=%d off_start=%d cell=%v\n", bestID, bestOff, s.actual[bestID])
			sb.WriteString(fxAscii(s.lv, cur, s.player[bestID]))
		}
	}
	// rooms
	if s.rooms != nil && lane == "macro" {
		sb.WriteString("ROOMS\n")
		for r, cells := range s.rooms.rooms {
			goals := 0
			for _, c := range cells {
				if s.lv.Goal[c] {
					goals++
				}
			}
			var nb []int
			for r2 := range s.rooms.rooms {
				if r2 != r && s.rooms.edges[r][r2] {
					nb = append(nb, r2)
				}
			}
			fmt.Fprintf(&sb, "room %d cells=%d goals=%d doors_to=%v\n", r, len(cells), goals, nb)
		}
		sb.WriteString("room map (digit/letter = room id, '+' corridor, '#' wall)\n")
		for y := 1; y < s.lv.H-1; y++ {
			row := make([]byte, 0, s.lv.W)
			for x := 1; x < s.lv.W-1; x++ {
				c := y*s.lv.W + x
				switch {
				case s.lv.Wall[c]:
					row = append(row, '#')
				case s.rooms.cell[c] >= 0:
					row = append(row, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"[s.rooms.cell[c]%62])
				default:
					row = append(row, '+')
				}
			}
			sb.WriteString(strings.TrimRight(string(row), " ") + "\n")
		}
	}
	os.WriteFile(FXOut+"-"+lane+".txt", []byte(sb.String()), 0o644)
}

func (s *solver) realRowsOr() []string {
	if s.realRows != nil {
		return s.realRows
	}
	return s.rows
}

// reverse lane: called when the search ends
func fxReverseHook() {
	if FXSol == "" {
		return
	}
	reverse.FXEnd = func(lane int, lv *reverse.Level, nstored int, unpack func(int32, []int32), nbox int, bestID, bestH int32, expanded int64) {
		real := &Level{W: lv.W, H: lv.H, Wall: lv.Wall, Goal: lv.Goal, Boxes: lv.Boxes, Player: lv.Player}
		fs := fxLoadSolution(real)
		if fs == nil {
			return
		}
		cur := make([]int32, nbox)
		hits := map[int]int{}
		for id := int32(0); int(id) < nstored; id++ {
			unpack(id, cur)
			c := slices.Clone(cur)
			slices.Sort(c)
			if k, ok := fs.keys[fxKey(c)]; ok {
				hits[k]++
			}
		}
		var ks []int
		for k := range hits {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		var sb strings.Builder
		fmt.Fprintf(&sb, "lane reverse(lane-index %d) expanded=%d stored=%d macros_in_solution=%d\n", lane, expanded, nstored, len(fs.macros))
		suf := 0
		for k := len(fs.macros); k >= 0 && hits[k] > 0; k-- {
			suf++
		}
		fmt.Fprintf(&sb, "solution states stored: %d distinct, k list=%v\ncontiguous solution suffix stored from the end (goal state backward): %d states; min k stored=%v\n", len(ks), ks, suf, func() int {
			if len(ks) == 0 {
				return -1
			}
			return ks[0]
		}())
		if bestID >= 0 {
			unpack(bestID, cur)
			fmt.Fprintf(&sb, "BEST node=%d h(assignment to start squares)=%d boxes_off_start=%d/%d boxes_on_goals=%d\n", bestID, bestH, fs.offStart(cur), nbox, fs.onGoals(cur))
			sb.WriteString(fxAscii(real, cur, -1))
		}
		os.WriteFile(fmt.Sprintf("%s-reverse%d.txt", FXOut, lane), []byte(sb.String()), 0o644)
	}
}
