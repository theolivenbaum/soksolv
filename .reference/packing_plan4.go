package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"
)

// Plan4 (exp-527): a BASE_SEARCH-style packing planner. It is a backward pull
// search from the solved board in which every original box square is a
// single-use base. A pull that ends on a base may delete the box (a "sink
// move") only when Festival's sink gating holds (research/exp-517/REVIEW.md
// section 2):
//   - the base is in a largest tied group of mutually push-reachable unused,
//     unoccupied bases (current boxes as walls);
//   - every such eligible base is inside the player's region (else no sink at
//     all in this state);
//   - the box's source and the base are well separated (static walk distance
//     >= Plan4Sep, or different rooms);
//   - the remaining boxes still match the remaining bases.
//
// If any sink qualifies, ordinary parks are dropped and each box keeps at most
// one sink (the first in enumeration order); otherwise every pull, including
// one that parks on a base, is a child. The search is FESS-like (feature cells
// visited round-robin, alternating with the best node's cell). The plan is the
// reversed witness path of the best node (Festival get_parking_plan): a node
// with no box left, or one whose remaining boxes all stand on unused bases
// with the original player in reach, ends the search.
var Plan4 = false
var Plan4Exp = 200000
var Plan4Out = ""
var Plan4Sep = 6
var Plan4BF = false
var Plan4Beam = 512
var Plan4Frac = 0.2
var Plan4Fallback = true
var Plan4Macro = false
var Plan4MacroOwn = true
var Plan4NoGate = false
var Plan4Anytime = false
var Plan4AnytimeRatio = 4
var Plan4InitExp = 0
var Plan4AnytimeRestart = false
var Plan4FestSink = 0
var Plan4RoomSep = false
var Plan4BestPast = false
var Plan4NewCell = false
var Plan4ConnAdv = false
var Plan4NoNull = false
var Plan4RevPlay = false
var Plan4BFRetry = false
var Plan4PullDead = false
var Plan4Explorer = false
var Plan4KeepParks = 0
var Plan4MultiSink = false
var Plan4Scope = ""
var p4onBase, p4onElim int
var p4dbg = os.Getenv("P4DBG") != ""
var p4trace = os.Getenv("P4TRACE") != ""

func init() {
	flag.BoolVar(&Plan4, "plan4", false, "exp-527: BASE_SEARCH-style planner (gated sink moves, FESS cells, reversed witness plan) replaces the v2 planner")
	flag.IntVar(&Plan4Exp, "plan4-exp", 200000, "exp-527: plan4 expansion cap")
	flag.StringVar(&Plan4Out, "plan4-out", "", "exp-527 diagnostic: write the plan4 event list as a plan-file JSON")
	flag.IntVar(&Plan4Sep, "plan4-sep", 6, "exp-527: sink source/base walk distance that counts as well separated")
	flag.BoolVar(&Plan4BF, "plan4-bf", false, "exp-527 ablation: plain best-first instead of FESS cells")
	flag.IntVar(&Plan4Beam, "plan4-beam", 512, "exp-527: plain pulls kept per expanded node")
	flag.Float64Var(&Plan4Frac, "plan4-frac", 0.2, "exp-527: share of the lane's remaining time the plan4 planner may use (timed runs)")
	flag.BoolVar(&Plan4Fallback, "plan4-fallback", true, "exp-527: when plan4 ends without a complete witness, use the v2 plan instead")
	flag.BoolVar(&Plan4Macro, "plan4-macro", true, "exp-527 (promoted 2026-11-09, default on): the macro lane (laneID 1) plans with plan4 and counts packed as its Festival-style prefix (other lanes unchanged)")
	flag.BoolVar(&Plan4MacroOwn, "plan4-macro-own", true, "exp-527: with -plan4-macro, an incomplete plan4 witness falls back to macro's own sink planner (default packed), not v2")
	flag.BoolVar(&Plan4Anytime, "plan4-anytime", true, "exp-563 (promoted 2026-11-10, default on): with -plan4-macro, an incomplete plan4 keeps planning inside the macro loop (-plan4-anytime-ratio planner node expansions per macro expansion) and replaces the fallback plan when it completes")
	flag.IntVar(&Plan4AnytimeRatio, "plan4-anytime-ratio", 8, "exp-563: planner node expansions per macro expansion while plan4 is pending")
	flag.BoolVar(&Plan4AnytimeRestart, "plan4-anytime-restart", true, "exp-563 (promoted, default on): when the anytime plan4 completes, restart the macro search with it instead of swapping it into the running tree")
	flag.IntVar(&Plan4InitExp, "plan4-init-exp", 0, "exp-563 diagnostic: cap the up-front plan4 phase at N expansions (deterministic anytime tests)")
	flag.IntVar(&Plan4FestSink, "plan4-festsink", 0, "exp-612: Festival's BASE_SEARCH advisor at move weight 0 (every other child weight 1; feature cells pop the lowest path weight first): in a sink node find_best_base_pull (the first floater sink, a box off every goal; else the sink leaving the fewest player regions with the box on the base, then the longest source-base walk), and in park nodes pick_pull_advisor_normal (pull a box off a goal, no more regions, farther from the goals, fewest regions then longest walk). Any value > 0 is on (mode 1, the sink advisor alone, was removed in exp-614: strictly worse); 0 = off")
	flag.BoolVar(&Plan4RoomSep, "plan4-roomsep", false, "exp-612: sink separation is Festival's boxes_in_different_rooms exactly: walk >= 6, or different rooms, else a room/corridor pair is close when the corridor cell touches the room, otherwise needs both row and column to differ (shared room cells count as corridor)")
	flag.BoolVar(&Plan4BestPast, "plan4-bestpast", false, "exp-689: a node waits in the feature cell of the best key on its path (Festival best_past labels), not its own")
	flag.BoolVar(&Plan4NewCell, "plan4-newcell", false, "exp-689: a newly created feature cell is the next round-robin cell (Festival add_pack_request moves the request pointer to it)")
	flag.BoolVar(&Plan4ConnAdv, "plan4-conn-adv", false, "exp-689: with the Festival-style plan, Festival's connectivity advisor is a second weight-0 child (fewer player regions)")
	flag.BoolVar(&Plan4Explorer, "plan4-explorer", false, "exp-689: with the Festival-style plan, Festival's explorer advisor is a further weight-0 child (opens a new pull)")
	flag.BoolVar(&Plan4NoNull, "plan4-nonull", false, "exp-689: drop the null pull (box back on its own cell, player in the same region), a duplicate of the node")
	flag.BoolVar(&Plan4RevPlay, "plan4-revplay", false, "exp-689: reverse-playability filter at pop time (Festival can_played_reversed): with more than one player region and a forward PI-corral (festCorral), reject a deletion, or a pull whose box is not a corral box")
	flag.BoolVar(&Plan4PullDead, "plan4-pulldead", false, "exp-689: pull-side freeze deadlock at pop time: reject a pull leaving the moved box or a neighbour frozen (never pullable again) off an unused base")
	flag.BoolVar(&Plan4BFRetry, "plan4-bf-retry", false, "exp-689: when plan4 ends incomplete, run a second plain best-first plan4 (default sink rules, -plan4-bf) with the same cap before the v2 fallback")
	flag.IntVar(&Plan4KeepParks, "plan4-keep-parks", 0, "exp-733: a sink node keeps its ordinary parks (weight 1) after its sinks instead of dropping them (exp-731 sinkdrop). 1 = every sink is a weight-0 advisor (Festival-style plan); 2 = only the find_best_base_pull sink is (as today); 3 = as 2, and parks interleave with the other sinks by key without the remaining-box count; 0 = off (sinks only)")
	flag.StringVar(&Plan4Scope, "plan4-scope", "", "exp-742: which plan4 planners -plan4-festsink, -plan4-roomsep and -plan4-keep-parks reach: empty = every planner; macro = only the macro lane's own plan4 (laneID 1; the fa2 lane then plans its default plan4 itself instead of taking the macro lane's list, and its festplan engine and streams keep their defaults); fa2 = only the fa2 lane's planners (the macro lane keeps the default plan4)")
	flag.BoolVar(&Plan4MultiSink, "plan4-multisink", false, "exp-733: every qualifying sink of a box is a child, not only the first (exp-731 onesink)")
	flag.BoolVar(&Plan4NoGate, "plan4-nogate", false, "exp-527 ablation: no sink gating (any reachable unused base is a sink)")
}

type p4Stats struct {
	veto, sepFail, matchFail, sinkNodes, parkNodes, cells, solvedStop int
	revRej, deadRej                                                   int // exp-689 pop-time filters
}

type planner4 struct {
	*planner2
	cellIdx                                         map[uint64]int32
	cellHeap                                        [][]p2Entry
	rr                                              int
	bfHeap                                          []p2Entry
	sepD                                            []int32
	lab                                             []int32
	labGen                                          []uint32
	labStamp                                        uint32
	labQ                                            []int32
	sepQ                                            []int32
	occBase                                         []bool
	elimOK                                          []bool
	sccIdx, sccLow, sccCmp, sccCnt, sccStk, sccCall []int32 // computeElim4 Tarjan scratch (exp-644)
	sccDir                                          []uint8
	sortTmp                                         []p2Child // sortKids scratch
	lastFail                                        string    // exp-689: trySink's failing gate (replay diagnostics)
	fz                                              []bool    // exp-689 pull freeze: cells under examination
	fzNo                                            []uint32  // exp-689 pull freeze: proven pullable in epoch
	fzEpoch                                         uint32
	fzLeft                                          int
	fest                                            bool // exp-615: Festival-style planning for this planner (-plan4-festsink or the fa2 lane's -fa2-festplan)
	roomSep                                         bool
	keepParks                                       int      // exp-742: -plan4-keep-parks for this planner (0 outside -plan4-scope)
	nodeW                                           []int32  // exp-610/612: path weight per node (Festival-style plan)
	sinkTo                                          []int32  // exp-612: a box's candidate sink cells (-plan4-festsink)
	nodeAdv                                         []uint8  // exp-612: the node's first nodeAdv[id] children are weight-0 advisors (-plan4-festsink; exp-689 adds more than one)
	nodeBK                                          []uint64 // exp-689: best key on the node's path (-plan4-bestpast)
	st4                                             p4Stats
	// exp-563: resumable main loop state
	best      int32
	done      bool
	fails     int
	iter      int
	started   bool
	exhausted bool     // tree exhausted / fails cap: no further progress possible
	rw        *p4Want  // exp-689 -plan4-replay: watched move (nil in normal runs)
	lv        p4Levers // exp-690: per-planner copy of the exp-689 levers
	bf        bool     // plain best-first (-plan4-bf; exp-689: the -plan4-bf-retry planner)
}

func (p *planner4) cellOf(key uint64) int32 {
	k := key >> 26
	if p.bf {
		k = 0
	}
	if i, ok := p.cellIdx[k]; ok {
		return i
	}
	i := int32(len(p.cellHeap))
	p.cellIdx[k] = i
	p.cellHeap = append(p.cellHeap, nil)
	p.st4.cells++
	if p.lv.NewCell {
		p.rr = int(i)
	}
	return i
}

func heapPush(h []p2Entry, e p2Entry) []p2Entry {
	h = append(h, e)
	i := len(h) - 1
	for i > 0 {
		par := (i - 1) / 2
		if !p2Less(h[i], h[par]) {
			break
		}
		h[i], h[par] = h[par], h[i]
		i = par
	}
	return h
}

func heapPop(h []p2Entry) (p2Entry, []p2Entry) {
	top := h[0]
	last := len(h) - 1
	h[0] = h[last]
	h = h[:last]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < len(h) && p2Less(h[l], h[m]) {
			m = l
		}
		if r < len(h) && p2Less(h[r], h[m]) {
			m = r
		}
		if m == i {
			break
		}
		h[i], h[m] = h[m], h[i]
		i = m
	}
	return top, h
}

// enqueue puts node id (with pending children) into its own feature cell,
// keyed by its next child's key.
func (p *planner4) enqueue(id int32) {
	nd := &p.nodes[id]
	if nd.cNext >= nd.cLen {
		return
	}
	p.seq++
	ck := nd.key
	if p.lv.BestPast {
		for len(p.nodeBK) < len(p.nodes) {
			i := len(p.nodeBK)
			bk := p.nodes[i].key
			if par := p.nodes[i].parent; par >= 0 {
				bk = min(bk, p.nodeBK[par])
			}
			p.nodeBK = append(p.nodeBK, bk)
		}
		ck = p.nodeBK[id]
	}
	c := p.cellOf(ck)
	var w int32
	if p.weighted() {
		w = p.nodeW[id]
		if int(id) >= len(p.nodeAdv) || nd.cNext >= int32(p.nodeAdv[id]) {
			w++
		}
	}
	p.cellHeap[c] = heapPush(p.cellHeap[c], p2Entry{p.kidKey(id, nd.cNext), p.seq, id, w})
}

// weighted: plan4 children carry move weights and cells pop the lowest
// path weight first (exp-612; Festival-style plan only).
func (p *planner4) weighted() bool { return p.fest }

// setAdv (exp-612) moves child i of the scratch list ch to the front and
// marks node id's first child as its weight-0 advisor.
func (p *planner4) setAdv(id int32, ch []p2Child, i int) {
	for len(p.nodeAdv) <= int(id) {
		p.nodeAdv = append(p.nodeAdv, 0)
	}
	if i < 0 {
		return
	}
	k := int(p.nodeAdv[id]) // advisors already at the front
	if i < k {
		return
	}
	x := ch[i]
	copy(ch[k+1:i+1], ch[k:i])
	ch[k] = x
	p.nodeAdv[id]++
}

// connAdv (exp-689, -plan4-conn-adv): Festival's connectivity advisor in
// pull mode (choose_advisor OPTIMIZE_CONNECTIVITY): the best-keyed child,
// not yet an advisor, with fewer player regions than the node. ch is key-sorted
// behind the advisors.
func (p *planner4) connAdv(id int32, ch []p2Child) int {
	pr := p.nodes[id].key >> 42 & 1023
	k := 0
	if int(id) < len(p.nodeAdv) {
		k = int(p.nodeAdv[id])
	}
	for i := k; i < len(ch); i++ {
		if ch[i].key>>42&1023 < pr {
			return i
		}
	}
	return -1
}

// explorerAdv (exp-689, -plan4-explorer): Festival's explorer advisor (opener.cpp
// explorer/are_new_moves): the best-keyed child, not yet an advisor, after
// which the player newly reaches a cell next to an unmoved box from which
// that box can be pulled. inParent marks the node's player region.
func (p *planner4) explorerAdv(id int32, ch []p2Child, inParent func(int32) bool) int {
	s := p.s
	k := 0
	if int(id) < len(p.nodeAdv) {
		k = int(p.nodeAdv[id])
	}
	boxes := p.boxes(id)
	for i := k; i < len(ch); i++ {
		c := &ch[i]
		s.occ[c.from] = false
		s.occ[c.to] = true
		_, st := p.flood(c.player)
		found := false
		for _, b := range boxes {
			if b == c.from {
				continue
			}
			for _, d := range s.dirs {
				n := b + d
				if s.occ[n] || s.lv.Wall[n] || inParent(n) || p.mark[n] != st {
					continue
				}
				if m := n + d; !s.lv.Wall[m] && !s.occ[m] {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		s.occ[c.to] = false
		s.occ[c.from] = true
		if found {
			return i
		}
	}
	return -1
}

// festSinkAdv (exp-612): Festival's find_best_base_pull over the sink
// children in generation order: the first floater (a box on no goal; bases
// are not marked on Festival's board), else the fewest player regions with
// the box standing on its base (connectivity_after_move), then the longest
// static walk from base to source. Festival's target-hole term is not
// modelled. All boxes are on s.occ.
func (p *planner4) festSinkAdv(ch []p2Child) int {
	s := p.s
	for i := range ch {
		if !s.lv.Goal[ch[i].from] {
			return i
		}
	}
	best, bR, bD := -1, 0, int32(0)
	for i := range ch {
		s.occ[ch[i].from] = false
		s.occ[ch[i].to] = true
		r := s.regions()
		s.occ[ch[i].to] = false
		s.occ[ch[i].from] = true
		d := s.walk(ch[i].to, ch[i].from)
		if best < 0 || r < bR || (r == bR && d > bD) {
			best, bR, bD = i, r, d
		}
	}
	return best
}

// festPackAdv (exp-612, -plan4-festsink 2): Festival's
// pick_pull_advisor_normal over key-sorted park children: a box pulled off a
// goal, no more regions than the node, farther from the goals; fewest
// regions, then the longest static walk. (Festival's kill zone is never
// filled in get_kill_zone; its target-hole and rooms terms are not modelled.)
func (p *planner4) festPackAdv(id int32, ch []p2Child) int {
	s := p.s
	nd := &p.nodes[id]
	pr := nd.key >> 42 & 1023
	best, bR, bL := -1, uint64(0), int32(0)
	for i := range ch {
		r := ch[i].key >> 42 & 1023
		if r > pr || !s.lv.Goal[ch[i].from] {
			continue
		}
		if p.distance[ch[i].to] <= p.distance[ch[i].from] {
			continue
		}
		l := s.walk(ch[i].to, ch[i].from)
		if best < 0 || r < bR || (r == bR && l > bL) {
			best, bR, bL = i, r, l
		}
	}
	return best
}

// separated is Festival's boxes_in_different_rooms, approximated with our
// static room map: far apart by walking (empty board), or in different rooms;
// a room/corridor pair also needs both row and column to differ.
func (p *planner4) separated(a, b int32) bool {
	s := p.s
	if Plan4Sep > 0 {
		// walking distance >= Manhattan distance: skip the flood when b is out of range
		w := int32(s.lv.W)
		dx, dy := a%w-b%w, a/w-b/w
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx+dy >= int32(Plan4Sep) && a != b {
			return true
		}
		for _, c := range p.sepQ {
			p.sepD[c] = -1
		}
		q := append(p.sepQ[:0], a)
		p.sepD[a] = 0
		near := false
		for i := 0; i < len(q) && !near; i++ {
			x := q[i]
			if p.sepD[x] >= int32(Plan4Sep)-1 {
				continue
			}
			for _, d := range s.dirs {
				y := x + d
				if s.lv.Wall[y] || p.sepD[y] >= 0 {
					continue
				}
				p.sepD[y] = p.sepD[x] + 1
				q = append(q, y)
				if y == b {
					near = true
					break
				}
			}
		}
		p.sepQ = q
		if !near && a != b {
			return true
		}
	}
	if s.rooms == nil {
		return false
	}
	if p.roomSep {
		return p.festRoomSep(a, b)
	}
	ra, rb := s.rooms.cell[a], s.rooms.cell[b]
	if ra >= 0 && rb >= 0 {
		return ra != rb
	}
	if ra != rb {
		w := int32(s.lv.W)
		return a/w != b/w && a%w != b%w
	}
	return false
}

// festRoomSep (exp-612, -plan4-roomsep): the room half of Festival's
// boxes_in_different_rooms. Two rooms: different rooms. A room and a
// corridor cell (or two different non-room cells): not separated when the
// other cell lies in the room with its corridor fringe (room_with_corridors:
// the room's cells and their floor neighbours), else separated when both row
// and column differ.
func (p *planner4) festRoomSep(a, b int32) bool {
	s := p.s
	ra, rb := s.rooms.cell[a], s.rooms.cell[b]
	if ra < 0 {
		ra = -1
	}
	if rb < 0 {
		rb = -1
	}
	if ra >= 0 && rb >= 0 {
		return ra != rb
	}
	if ra == rb {
		return false
	}
	near := func(r, c int32) bool { // c in room r with corridors
		if s.rooms.cell[c] == int(r) {
			return true
		}
		for _, d := range s.dirs {
			if s.rooms.cell[c+d] == int(r) {
				return true
			}
		}
		return false
	}
	if ra >= 0 && near(int32(ra), b) || rb >= 0 && near(int32(rb), a) {
		return false
	}
	w := int32(s.lv.W)
	return a/w != b/w && a%w != b%w
}

// computeElim4 marks the unused, unoccupied bases that lie in a largest tied
// group of mutually push-reachable bases (boxes as walls).
func (p *planner4) computeElim4() {
	s := p.s
	nb := len(p.baseCell)
	if len(p.elimOK) < nb {
		p.elimOK = make([]bool, nb)
	}
	clear(p.elimOK[:nb])
	fr := p.elimFr[:0]
	for _, t := range p.free {
		if !s.occ[p.baseCell[t]] {
			fr = append(fr, t)
		}
	}
	p.elimFr = fr
	m := len(fr)
	if m == 0 {
		return
	}
	// blk folds wall and occupancy into one array (exp-638): the flood below
	// tests four cells per step.
	if len(p.elimBlk) < len(s.lv.Wall) {
		p.elimBlk = make([]bool, len(s.lv.Wall))
	}
	blk := p.elimBlk[:len(s.lv.Wall)]
	for c, w := range s.lv.Wall {
		blk[c] = w || s.occ[c]
	}
	// Two bases are mutually push-reachable iff they lie in one strongly
	// connected component of the cell graph c -> c+d (c+d and c-d free). One
	// iterative Tarjan pass over the cells reachable from the bases replaces
	// the former m floods and the m*m reach matrix (exp-644).
	n := len(s.lv.Wall)
	if len(p.bseen) < n {
		p.bseen = make([]uint32, n)
	}
	if len(p.sccIdx) < n {
		p.sccIdx, p.sccLow, p.sccCmp = make([]int32, n), make([]int32, n), make([]int32, n)
		p.sccCnt = make([]int32, n+1)
		p.sccStk, p.sccCall, p.sccDir = make([]int32, 0, n), make([]int32, n), make([]uint8, n)
	}
	p.bstamp++
	st := p.bstamp
	seen, idx, low, cmp := p.bseen, p.sccIdx, p.sccLow, p.sccCmp
	stk := p.sccStk[:0]
	call, cdir := p.sccCall, p.sccDir
	dirs := s.dirs
	next, ncomp := int32(0), int32(0)
	for _, r := range fr {
		root := p.baseCell[r]
		if seen[root] == st {
			continue
		}
		seen[root], idx[root], low[root], cmp[root] = st, next, next, -1
		next++
		stk = append(stk, root)
		call[0], cdir[0] = root, 0
		sp := 1
		for sp > 0 {
			top := sp - 1
			v := call[top]
			if di := cdir[top]; di < 4 {
				cdir[top] = di + 1
				to, back := v+dirs[di], v-dirs[di]
				if blk[to] || blk[back] {
					continue
				}
				if seen[to] != st {
					seen[to], idx[to], low[to], cmp[to] = st, next, next, -1
					next++
					stk = append(stk, to)
					call[sp], cdir[sp] = to, 0
					sp++
				} else if cmp[to] < 0 && idx[to] < low[v] {
					low[v] = idx[to]
				}
				continue
			}
			sp = top
			if low[v] == idx[v] {
				for {
					w := stk[len(stk)-1]
					stk = stk[:len(stk)-1]
					cmp[w] = ncomp
					if w == v {
						break
					}
				}
				ncomp++
			}
			if sp > 0 {
				if u := call[sp-1]; low[v] < low[u] {
					low[u] = low[v]
				}
			}
		}
	}
	p.sccStk = stk
	cnt := p.sccCnt[:ncomp]
	clear(cnt)
	best := int32(0)
	for _, ti := range fr {
		c := cmp[p.baseCell[ti]]
		cnt[c]++
		best = max(best, cnt[c])
	}
	for _, ti := range fr {
		if cnt[cmp[p.baseCell[ti]]] == best {
			p.elimOK[ti] = true
		}
	}
}

// expand4 generates node id's children (see Plan4).
func (p *planner4) expand4(id int32) {
	p.expGen++
	s := p.s
	nd := &p.nodes[id]
	boxes := p.boxes(id)
	p.setOcc(id)
	used := p.usedBits(id)
	cs := &p.cut
	cs.init(s)
	_, parentStamp := p.flood(nd.player)
	pg := cs.newNode()
	for _, c := range p.queue {
		if p.mark[c] == parentStamp {
			cs.pgen[c] = pg
		}
	}
	p.free = p.free[:0]
	for t := range p.baseCell {
		if used[t>>6]>>(uint(t)&63)&1 == 0 {
			p.free = append(p.free, int32(t))
		}
	}
	sinks := len(p.free) > 0
	if sinks && !Plan4NoGate {
		p.computeElim4()
		any := false
		for _, t := range p.free {
			if p.elimOK[t] {
				any = true
				if cs.pgen[p.baseCell[t]] != pg {
					sinks = false // an eligible base outside the player's reach
				}
			}
		}
		if !any {
			sinks = false
		}
		if !sinks && any {
			p.st4.veto++
		}
		if p.rw != nil {
			p.rw.sinkState = "ok"
			if !any {
				p.rw.sinkState = "noelig"
			} else if !sinks {
				p.rw.sinkState = "veto"
			}
		}
		if p4dbg && p.expanded < 40 {
			ne, nu := 0, 0
			for _, t := range p.free {
				if p.elimOK[t] {
					ne++
					if cs.pgen[p.baseCell[t]] != pg {
						nu++
						print(" U", p.baseCell[t])
					}
				}
			}
			println(" dbg onbase", p4onBase, "onelim", p4onElim, "exp", p.expanded, "nb", nd.nb, "free", len(p.free), "elim", ne, "unreach", nu, "sinks", sinks)
		}
	} else if sinks {
		if len(p.elimOK) < len(p.baseCell) {
			p.elimOK = make([]bool, len(p.baseCell))
		}
		for _, t := range p.free {
			p.elimOK[t] = true
		}
	}
	if len(p.alive) < len(p.reach[0]) {
		p.alive = make([]uint64, len(p.reach[0]))
	}
	clear(p.alive)
	for _, t := range p.free {
		for w, v := range p.reach[t] {
			p.alive[w] |= v
		}
	}
	p.cands = p.cands[:0]
	p.rem = p.rem[:0]
	p.region0 = p.region0[:0]
	base := int32(-1)
	if s.tgtPocket == nil {
		base = p.labelRegions()
	}
	trySink := func(b int32, bi int, to, t, rb int32) bool {
		ok := Plan4NoGate || p.separated(b, to)
		if !ok {
			p.st4.sepFail++
			p.lastFail = "sep"
		} else if p.matchOn && !p.matchSkip(boxes, bi, t) {
			ok = false
			p.st4.matchFail++
			p.lastFail = "match"
		}
		if p4dbg && p.expanded < 3 {
			println("  cand box", b, "to", to, "sep", Plan4NoGate || p.separated(b, to), "ok", ok)
		}
		if ok {
			npl := cs.compMin(nd.player) // same graph as the box's DFS (box b lifted): no second flood
			nd = &p.nodes[id]
			goals := nd.goals
			if s.lv.Goal[b] {
				goals--
			}
			dist := nd.dist - p.distance[b]
			// no hash: the stored child recomputes it (planner2.kid)
			ch := p2Child{from: b, to: to, player: npl, base: t}
			ch.key = p.key(int(nd.nb)-1, int(rb), int(goals), dist, 0)
			p.rem = append(p.rem, ch)
		}
		return ok
	}
	for bi, b := range boxes {
		rb := int32(-1) // regions with box b lifted
		if base >= 0 {
			rb = base + 1
			var seen [4]int32
			k := 0
			for _, d := range s.dirs {
				if l := p.lab[b+d]; l >= 0 && p.labGen[b+d] == p.labStamp {
					dup := false
					for j := 0; j < k; j++ {
						dup = dup || seen[j] == l
					}
					if !dup {
						seen[k] = l
						k++
					}
				}
			}
			if k > 0 {
				rb = base - int32(k) + 1
			}
		}
		if bi&7 == 7 && p.stopped() {
			break
		}
		watch := p.rw != nil && b == p.rw.from
		cs.begin(b)
		g := cs.gen
		q := cs.q[:0]
		for di, d := range s.dirs {
			if cs.pgen[b+d] == pg {
				k := b*4 + int32(di)
				q = append(q, k)
				cs.seen[k] = g
			}
		}
		s.occ[b] = false
		if rb < 0 {
			rb = int32(s.regions())
		}
		sunk := false
		for qi := 0; qi < len(q); qi++ {
			k := q[qi]
			c, d := k/4, s.dirs[k%4]
			to, player := c+d, c+2*d
			if s.lv.Wall[to] || s.occ[to] || s.lv.Wall[player] || s.occ[player] {
				continue
			}
			np, sides := cs.query(to, player)
			for di := range s.dirs {
				k2 := to*4 + int32(di)
				if sides&(1<<uint(di)) != 0 && cs.seen[k2] != g {
					cs.seen[k2] = g
					q = append(q, k2)
				}
			}
			if t := p.baseAt[to]; p4dbg && t >= 0 && used[t>>6]>>(uint(t)&63)&1 == 0 {
				p4onBase++
				if p.elimOK[t] {
					p4onElim++
				}
			}
			if watch && to == p.rw.to {
				p.rw.watchPull(p, to, used, sinks, sunk)
			}
			if t := p.baseAt[to]; sinks && (!sunk || Plan4MultiSink) && t >= 0 && used[t>>6]>>(uint(t)&63)&1 == 0 && p.elimOK[t] {
				if p.fest {
					p.sinkTo = append(p.sinkTo, to) // tested in cell order below
				} else {
					sunk = trySink(b, bi, to, t, rb) || sunk
					if watch && to == p.rw.to && p.rw.del && !sunk {
						p.rw.why = p.lastFail
					}
				}
			}
			if p.alive[to>>6]>>(uint(to)&63)&1 == 0 {
				if watch && to == p.rw.to && !p.rw.del {
					p.rw.why = "alive"
				}
				continue
			}
			if !cs.newEnd(to, np) {
				continue
			}
			if p.lv.NoNull && to == b && np == nd.player {
				continue // exp-689: the box back on its cell, player in the same region: the node itself
			}
			p.cands = append(p.cands, p2Cand{box: int32(bi), to: to, player: np, pieces: cs.pieces(to)})
		}
		if len(p.sinkTo) > 0 {
			// exp-612: Festival lists a box's pulls by destination cell
			// (get_box_moves_from_graph), so its one sink per box is the
			// lowest-cell qualifying base, not the first one the pull BFS meets.
			slices.Sort(p.sinkTo)
			for _, to := range p.sinkTo {
				if trySink(b, bi, to, p.baseAt[to], rb) {
					if Plan4MultiSink {
						continue
					}
					if watch && p.rw.del && to != p.rw.to && p.rw.why == "" {
						p.rw.why = "onesink"
					}
					break
				}
				if watch && to == p.rw.to && p.rw.del {
					p.rw.why = p.lastFail
				}
			}
			p.sinkTo = p.sinkTo[:0]
		}
		cs.q = q
		p.region0 = append(p.region0, rb)
		s.occ[b] = true
	}
	nd = &p.nodes[id]
	if len(p.rem) > 0 && p.keepParks > 0 {
		p.keepParks4(id, boxes)
		return
	}
	if len(p.rem) > 0 {
		p.st4.sinkNodes++
		adv := -1
		var advCh p2Child
		if p.fest {
			if adv = p.festSinkAdv(p.rem); adv >= 0 {
				advCh = p.rem[adv]
			}
		}
		sortKids(p.rem, &p.sortTmp)
		if adv >= 0 {
			for i := range p.rem {
				if p.rem[i].from == advCh.from && p.rem[i].to == advCh.to {
					p.setAdv(id, p.rem, i)
					break
				}
			}
		}
		nd.cOff = p.kids.put(p.rem, nil)
		nd.cLen = int32(len(p.rem))
		p.stats.generated += len(p.rem)
		return
	}
	p.st4.parkNodes++
	p.parkKids4(id, boxes)
	if p.fest {
		p.setAdv(id, p.beam, p.festPackAdv(id, p.beam))
		if p.lv.ConnAdv {
			p.setAdv(id, p.beam, p.connAdv(id, p.beam))
		}
		if p.lv.Explorer {
			p.setAdv(id, p.beam, p.explorerAdv(id, p.beam, func(c int32) bool { return cs.pgen[c] == pg }))
		}
	}
	if Plan4Beam > 0 && len(p.beam) > Plan4Beam {
		p.beam = p.beam[:Plan4Beam]
	}
	nd.cOff = p.kids.put(p.beam, nil)
	nd.cLen = int32(len(p.beam))
	p.stats.generated += len(p.beam)
}

// keepParks4 (exp-733, -plan4-keep-parks): a sink node's children are its
// sinks (key-sorted, nb-1 boxes, so ahead of every park) and then its
// ordinary parks (weight 1). With the Festival-style plan, mode 1 makes every
// sink a weight-0 advisor (find_best_base_pull's first), mode 2 only that one;
// mode 3 as 2 with parks and the other sinks ordered by key without rem.
func (p *planner4) keepParks4(id int32, boxes []int32) {
	p.st4.sinkNodes++
	adv := -1
	var advCh p2Child
	if p.fest {
		if adv = p.festSinkAdv(p.rem); adv >= 0 {
			advCh = p.rem[adv]
		}
	}
	sortKids(p.rem, &p.sortTmp)
	if adv >= 0 {
		for i := range p.rem {
			if p.rem[i].from == advCh.from && p.rem[i].to == advCh.to {
				p.setAdv(id, p.rem, i)
				break
			}
		}
	}
	if p.fest && p.keepParks == 1 {
		for len(p.nodeAdv) <= int(id) {
			p.nodeAdv = append(p.nodeAdv, 0)
		}
		p.nodeAdv[id] = uint8(min(len(p.rem), 255))
	}
	p.parkKids4(id, boxes)
	if Plan4Beam > 0 && len(p.beam) > Plan4Beam {
		p.beam = p.beam[:Plan4Beam]
	}
	p.rem = append(p.rem, p.beam...)
	if p.keepParks == 3 {
		// parks and the non-advisor sinks interleave by key without the
		// remaining-box count (a sink no longer outranks every park)
		k := 0
		if int(id) < len(p.nodeAdv) {
			k = int(p.nodeAdv[id])
		}
		const low = uint64(1)<<52 - 1
		slices.SortStableFunc(p.rem[k:], func(x, y p2Child) int { return cmp.Compare(x.key&low, y.key&low) })
	}
	nd := &p.nodes[id]
	nd.cOff = p.kids.put(p.rem, nil)
	nd.cLen = int32(len(p.rem))
	p.stats.generated += len(p.rem)
}

// parkKids4 fills p.beam with the node's ordinary pulls (p.cands), key-sorted.
func (p *planner4) parkKids4(id int32, boxes []int32) {
	s := p.s
	nd := &p.nodes[id]
	p.beam = p.beam[:0]
	for _, c := range p.cands {
		b := boxes[c.box]
		regions := int(p.region0[c.box]) - 1 + int(c.pieces)
		goals := nd.goals
		if s.lv.Goal[b] {
			goals--
		}
		if s.lv.Goal[c.to] {
			goals++
		}
		dist := nd.dist - p.distance[b] + p.distance[c.to]
		ch := p2Child{key: p.key(int(nd.nb), regions, int(goals), dist, 0), from: b, to: c.to, player: c.player, base: -1}
		p.beam = append(p.beam, ch)
	}
	sortKids(p.beam, &p.sortTmp)
}

// sortKids sorts scratch children by key, stably: the result of
// slices.SortStableFunc by key (exp-583: that merge sort of 32-byte children
// was 8-20% of plan4 on large levels). LSD radix sort on the key bytes,
// skipping bytes every child shares; insertion sort for short lists.
func sortKids(a []p2Child, tmp *[]p2Child) {
	n := len(a)
	if n < 48 {
		for i := 1; i < n; i++ {
			x := a[i]
			j := i
			for j > 0 && a[j-1].key > x.key {
				a[j] = a[j-1]
				j--
			}
			a[j] = x
		}
		return
	}
	var cnt [8][256]int32
	for i := range a {
		k := a[i].key
		for b := 0; b < 8; b++ {
			cnt[b][k>>(8*uint(b))&255]++
		}
	}
	if cap(*tmp) < n {
		*tmp = make([]p2Child, n)
	}
	src, dst := a, (*tmp)[:n]
	for b := 0; b < 8; b++ {
		c := &cnt[b]
		if c[a[0].key>>(8*uint(b))&255] == int32(n) {
			continue // every key has this byte
		}
		sum := int32(0)
		for v := range c {
			c[v], sum = sum, sum+c[v]
		}
		for i := range src {
			v := src[i].key >> (8 * uint(b)) & 255
			dst[c[v]] = src[i]
			c[v]++
		}
		src, dst = dst, src
	}
	if &src[0] != &a[0] {
		copy(a, src)
	}
}

// atStart: every remaining box stands on an unused base and the original
// player square is in the player's region (Festival node_is_solved).
func (p *planner4) atStart(id int32) bool {
	s := p.s
	used := p.usedBits(id)
	for _, b := range p.boxes(id) {
		t := p.baseAt[b]
		if t < 0 || used[t>>6]>>(uint(t)&63)&1 != 0 {
			return false
		}
	}
	p.setOcc(id)
	_, st := p.flood(p.nodes[id].player)
	return p.mark[s.lv.Player] == st
}

// start4 builds the roots and expands them; false when there is no root.
func (p *planner4) start4() bool {
	s := p.s
	clear(s.occ)
	for _, g := range s.goals {
		s.occ[g] = true
	}
	var roots []int32
	covered := make([]bool, len(s.lv.Wall))
	for _, c := range s.fcell {
		if !s.occ[c] && !covered[c] {
			norm, st := p.flood(c)
			roots = append(roots, norm)
			for _, x := range p.queue {
				if p.mark[x] == st {
					covered[x] = true
				}
			}
		}
	}
	if len(roots) == 0 {
		return false
	}
	var gsum int32
	for _, g := range s.goals {
		gsum += p.distance[g]
	}
	for _, r := range roots {
		h := p.zp[r]
		for _, g := range s.goals {
			h ^= p.zb[g]
		}
		p.boxArena = append(p.boxArena, s.goals...)
		p.nodes = append(p.nodes, p2Node{parent: -1, bOff: int32(len(p.boxArena) - len(s.goals)), nb: int32(len(s.goals)), player: r, hash: h,
			goals: int32(len(s.goals)), dist: gsum, mbase: -1, key: p.key(len(s.goals), 1, len(s.goals), gsum, 0)})
		p.usedArena = append(p.usedArena, make([]uint64, p.nw)...)
		p.seenInsert(h)
		if p.weighted() {
			p.nodeW = append(p.nodeW, 0)
		}
	}
	if p.matchOn {
		p.setOcc(0)
		p.free = p.free[:0]
		for t := range p.baseCell {
			p.free = append(p.free, int32(t))
		}
		if !p.matchOK(p.boxes(0), -1, -1) {
			p.matchOn = false
		}
	}
	best, done := int32(0), false
	for id := range p.nodes {
		if p.nodes[id].key < p.nodes[best].key {
			best = int32(id)
		}
		if p.atStart(int32(id)) {
			best, done = int32(id), true
			break
		}
		p.expand4(int32(id))
		p.expanded++
		p.enqueue(int32(id))
	}
	p.best, p.done, p.started = best, done, true
	return true
}

// step4 runs the main loop until maxIter more nodes were expanded (<=0: until
// the deadline, cap or completion); the loop state lives in the planner so a later
// call resumes it (exp-563).
func (p *planner4) step4(maxIter int) {
	best, done, fails := p.best, p.done, p.fails
	tgt := p.expanded + maxIter
	for !done && !p.stopped() && p.expanded < p.maxExp && fails < 10000 && (maxIter <= 0 || p.expanded < tgt) {
		iter := p.iter
		p.iter++
		// pick a cell: odd iterations the best node's cell, else round-robin
		c := int32(-1)
		if iter&1 == 1 {
			if bc, ok := p.cellIdx[p.nodes[best].key>>26]; ok && len(p.cellHeap[bc]) > 0 && !p.bf {
				c = bc
			}
		}
		if c < 0 {
			n := len(p.cellHeap)
			for k := 0; k < n; k++ {
				i := (p.rr + k) % n
				if len(p.cellHeap[i]) > 0 {
					c = int32(i)
					p.rr = i + 1
					break
				}
			}
		}
		if c < 0 {
			p.exhausted = true
			break // tree exhausted
		}
		var e p2Entry
		e, p.cellHeap[c] = heapPop(p.cellHeap[c])
		pn := &p.nodes[e.node]
		ch := p.kid(e.node, pn.cNext)
		pn.cNext++
		p.enqueue(e.node)
		if (p.lv.RevPlay || p.lv.PullDead) && p.popReject(e.node, &ch) {
			continue
		}
		id := p.addNode(e.node, &ch)
		if p4trace && p.expanded < 400 {
			w := int32(p.s.lv.W)
			tag, pick := "E", "R"
			if id < 0 {
				tag = "D"
			}
			if iter&1 == 1 && c >= 0 && len(p.cellHeap) > 0 {
				if bc, ok := p.cellIdx[p.nodes[best].key>>26]; ok && bc == c {
					pick = "B"
				}
			}
			fmt.Fprintf(os.Stderr, "T %d %s par %d %d,%d->%d,%d %s w=%d cell=%d\n", id, pick, e.node, ch.from/w-1, ch.from%w-1, ch.to/w-1, ch.to%w-1, tag, e.w, c)
		}
		if id < 0 {
			fails++
			continue
		}
		if p.weighted() {
			p.nodeW = append(p.nodeW, e.w)
		}
		if p.nodes[id].key < p.nodes[best].key {
			best = id
		}
		if p.nodes[id].nb == 0 || p.atStart(id) {
			best, done = id, true
			p.st4.solvedStop++
			break
		}
		fails = 0
		p.expand4(id)
		p.expanded++
		p.enqueue(id)
	}
	p.best, p.done, p.fails = best, done, fails
	if fails >= 10000 {
		p.exhausted = true
	}
}

func (p *planner4) run4() *packingPlan {
	if !p.start4() {
		return nil
	}
	p.step4(0)
	return p.finish4()
}

// finish4 turns the current best node into the plan.
func (p *planner4) finish4() *packingPlan {
	res := p.result(p.best)
	if res != nil && p.atStart(p.best) {
		res.complete = true
	}
	return res
}

// partitionInits moves permanent initial entries ahead of temporary ones
// (Festival stable partition); later indices are unchanged.
func (f *festPlan) partitionInits() {
	in := f.e[:f.initNum]
	f.recomputeUntil()
	// Festival scans the endpoint in descending cell order
	slices.SortStableFunc(in, func(a, b festEntry) int { return int(b.to - a.to) })
	slices.SortStableFunc(in, func(a, b festEntry) int {
		ap, bp := a.until == -1, b.until == -1
		if ap == bp {
			return 0
		}
		if ap {
			return -1
		}
		return 1
	})
	f.recomputeUntil()
}

// writePlanFile dumps the plan in plan-file JSON (Festival cell numbering).
func writePlanFile(path string, fp *festPlan, lv *Level) {
	w := int32(lv.W)
	tf := func(c int32) int32 {
		if c < 0 {
			return c
		}
		return (c/w-1)*(w-2) + c%w - 1
	}
	j := planFileJSON{W: lv.W - 2, Init: fp.initNum}
	for _, e := range fp.e {
		j.E = append(j.E, []int32{tf(e.from), tf(e.to)})
	}
	b, _ := json.Marshal(j)
	_ = os.WriteFile(path, b, 0o644)
}

// buildPlan4 runs the plan4 planner (shares planner2's arenas and helpers).
func (s *solver) buildPlan4(deadline time.Time, maxNodes int) *packingPlan {
	return s.buildPlan4x(deadline, maxNodes, Plan4Fallback)
}

// buildPlan4x: with fallback, an incomplete witness is replaced by the v2
// plan; without, the caller handles it (res.complete false).
func (s *solver) buildPlan4x(deadline time.Time, maxNodes int, fallback bool) *packingPlan {
	p := s.newPlanner4(deadline, maxNodes, fallback)
	res := p.run4()
	if Plan4BFRetry && (res == nil || !res.complete) && !Stop.Load() {
		q := s.newPlanner4(deadline, maxNodes, fallback)
		q.bf, q.fest, q.roomSep = true, false, false
		if r2 := q.run4(); r2 != nil && r2.complete {
			s.tr.SetExtra("plan4_bf_retry", q.expanded)
			p, res = q, r2
		}
	}
	return s.endPlan4(p, res, fallback, deadline)
}

// newPlanner4 builds the planner (allocations, base reach, goal distances)
// without running it; exp-563 steps it from the macro loop.
func (s *solver) newPlanner4(deadline time.Time, maxNodes int, fallback bool) *planner4 {
	lv := s.p4lv.or(p4GlobalLevers())
	size := len(s.lv.Wall)
	work := *s
	work.occ = make([]bool, size)
	work.rmark = nil
	work.rqueue = nil
	work.rstamp = 0
	if lv.RevPlay {
		// exp-689: festCorral and reach use these; the anytime planner runs
		// inside the forward search, which owns the originals
		work.mark = make([]uint32, size)
		work.cmark = make([]uint32, size)
		work.cstamp = 0
		work.queue = nil
		work.fc = festCorralScratch{}
	}
	n := len(s.lv.Boxes)
	if Plan4Exp > 0 {
		maxNodes = Plan4Exp
	}
	p4dl := deadline
	if fallback {
		p4dl = time.Now().Add(time.Until(deadline) * 4 / 5) // leave time for the v2 fallback
	}
	p2 := &planner2{s: &work, n: n, nw: (n + 63) / 64, deadline: p4dl, maxExp: maxNodes, matchOn: PlanV2Match,
		baseAt: make([]int32, size), mark: make([]uint32, size), distance: make([]int32, size),
		zb: make([]uint64, size), zp: make([]uint64, size), zu: make([]uint64, n), demand: make([]int32, n),
		mBox: make([]int32, n), mSeen: make([]uint32, n), fmBy: make([]int32, n)}
	seed := uint64(0x5eed)
	for i := range p2.zb {
		p2.zb[i], p2.zp[i] = splitmix(&seed), splitmix(&seed)
	}
	for i := range p2.zu {
		p2.zu[i] = splitmix(&seed)
	}
	for i := range p2.baseAt {
		p2.baseAt[i] = -1
	}
	p2.baseCell = slices.Clone(s.lv.Boxes)
	slices.Sort(p2.baseCell)
	for i, c := range p2.baseCell {
		p2.baseAt[c] = int32(i)
	}
	words := (size + 63) / 64
	p2.reach = make([][]uint64, n)
	var q []int32
	for t, bc := range p2.baseCell {
		r := make([]uint64, words)
		r[bc>>6] |= 1 << (uint(bc) & 63)
		q = append(q[:0], bc)
		for i := 0; i < len(q); i++ {
			x := q[i]
			for _, d := range s.dirs {
				c := x - d
				if s.lv.Wall[c] || s.lv.Wall[x] || s.lv.Wall[x+d] || r[c>>6]>>(uint(c)&63)&1 != 0 {
					continue
				}
				r[c>>6] |= 1 << (uint(c) & 63)
				q = append(q, c)
			}
		}
		p2.reach[t] = r
	}
	// distance score: sum of finite walking distances to all goals (Festival
	// score.cpp; larger is better in the backward search)
	dg := make([]int32, size)
	for _, g := range s.goals {
		for i := range dg {
			dg[i] = -1
		}
		dg[g] = 0
		q = append(q[:0], g)
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				x := q[i] + d
				if !s.lv.Wall[x] && dg[x] < 0 {
					dg[x] = dg[q[i]] + 1
					q = append(q, x)
				}
			}
		}
		for i, v := range dg {
			if v > 0 {
				p2.distance[i] += v
			}
		}
	}
	cap2 := 1 << 16
	p2.seen = make([]uint64, cap2)
	p2.mask = uint64(cap2 - 1)
	g := s.plan4InScope()
	p := &planner4{planner2: p2, cellIdx: map[uint64]int32{}, sepD: make([]int32, size),
		fest: g && Plan4FestSink > 0 || s.festPlan, roomSep: g && Plan4RoomSep || s.festPlan, bf: Plan4BF || s.planBF, lv: lv}
	if g {
		p.keepParks = Plan4KeepParks
	}
	if s.planAlt {
		p.fest, p.roomSep, p.keepParks = true, true, 1 // exp-769: the macro lane's alt engine (exp-742's plan config)
	}
	for i := range p.sepD {
		p.sepD[i] = -1
	}
	return p
}

// plan4InScope (exp-742, -plan4-scope): the global plan4 settings reach this
// solver's planner.
func (s *solver) plan4InScope() bool {
	switch Plan4Scope {
	case "macro":
		return s.laneID == 1 && !s.fa2Lane
	case "fa2":
		return s.fa2Lane
	}
	return true
}

// endPlan4 finishes a plan4 attempt: fest partition, diagnostics, optional v2 fallback.
func (s *solver) endPlan4(p *planner4, res *packingPlan, fallback bool, deadline time.Time) *packingPlan {
	if res != nil && res.fest != nil {
		if res.fest.mode == 1 {
			res.fest.partitionInits()
		}
		if Plan4Out != "" {
			writePlanFile(Plan4Out, res.fest, s.lv)
		}
	}
	if res != nil && os.Getenv("P4WIT") != "" {
		p.dumpWitness()
	}
	if res != nil && Plan4Out != "" {
		fmt.Fprintf(os.Stderr, "plan4 exp=%d nodes=%d remaining=%d complete=%v events=%d init=%d veto=%d sepfail=%d matchfail=%d sinknodes=%d parknodes=%d cells=%d stop=%d heapleft=%d dupes=%d revrej=%d deadrej=%d\n",
			p.expanded, len(p.nodes), res.remaining, res.complete, len(res.fest.e), res.fest.initNum, p.st4.veto, p.st4.sepFail, p.st4.matchFail, p.st4.sinkNodes, p.st4.parkNodes, p.st4.cells, p.st4.solvedStop, p.pending(), p.stats.dupes, p.st4.revRej, p.st4.deadRej)
	}
	if (res == nil || !res.complete) && fallback && time.Now().Before(deadline) {
		v2 := s.buildPlanV2Core(deadline, 4096)
		if v2 != nil {
			s.tr.SetExtra("plan4_fallback", true)
			return v2
		}
	}
	if res != nil {
		s.tr.SetExtra("plan4", map[string]int{"expanded": p.expanded, "nodes": len(p.nodes), "remaining": res.remaining,
			"events": len(res.fest.e), "init": res.fest.initNum, "veto": p.st4.veto, "sep_fail": p.st4.sepFail,
			"match_fail": p.st4.matchFail, "sink_nodes": p.st4.sinkNodes, "park_nodes": p.st4.parkNodes, "cells": p.st4.cells})
	}
	return res
}

func (p *planner4) pending() int {
	n := 0
	for _, h := range p.cellHeap {
		n += len(h)
	}
	return n
}

// labelRegions labels the free components of the current occupancy (p.lab,
// valid where labGen == labStamp) and returns their number (s.regions()
// without target pockets).
func (p *planner4) labelRegions() int32 {
	s := p.s
	if p.lab == nil {
		p.lab = make([]int32, len(s.lv.Wall))
		p.labGen = make([]uint32, len(s.lv.Wall))
	}
	p.labStamp++
	st := p.labStamp
	n := int32(0)
	q := p.labQ[:0]
	for _, c := range s.floorCells() {
		if s.occ[c] || p.labGen[c] == st {
			continue
		}
		p.labGen[c], p.lab[c] = st, n
		q = append(q[:0], c)
		for i := 0; i < len(q); i++ {
			for _, d := range s.dirs {
				if y := q[i] + d; p.labGen[y] != st && !s.lv.Wall[y] && !s.occ[y] {
					p.labGen[y], p.lab[y] = st, n
					q = append(q, y)
				}
			}
		}
		n++
	}
	p.labQ = q[:0]
	return n
}

// planAnytime (exp-563) runs plan4 up to the deadline (or -plan4-init-exp
// expansions). A complete witness is returned as buildPlan4x would; otherwise
// the planner stays alive in s.p4pend, resumable, and nil is returned so the
// caller installs its fallback plan. The macro loop then steps it
// (stepPlan4) a few iterations per expansion and swaps the plan in on
// completion, so planning shares the lane's core by work, not by wall clock.
func (s *solver) planAnytime(deadline time.Time, maxNodes int) *packingPlan {
	p := s.newPlanner4(deadline, maxNodes, false)
	if !p.start4() {
		return s.endPlan4(p, nil, false, deadline)
	}
	full := p.maxExp
	if Plan4InitExp > 0 {
		p.maxExp = min(full, Plan4InitExp)
	}
	p.step4(0)
	p.maxExp = full
	res := p.finish4()
	if res != nil && res.complete {
		return s.endPlan4(p, res, false, deadline)
	}
	if !p.exhausted && p.expanded < p.maxExp && !Stop.Load() {
		s.p4pend = p
		s.stats.Plan4Pending = true
	}
	s.tr.SetExtra("plan4_anytime_start", p.expanded)
	return nil
}

// stepPlan4 advances the pending planner by Plan4AnytimeRatio iterations and
// installs its plan if it completes (or retires it when it cannot).
func (s *solver) stepPlan4() (restart bool) {
	p := s.p4pend
	p.deadline = time.Now().Add(time.Hour)
	p.step4(Plan4AnytimeRatio)
	if p4dbg && s.expandedN%5000 == 0 {
		fmt.Fprintf(os.Stderr, "p4 step N=%d exp=%d\n", s.expandedN, p.expanded)
	}
	if !p.done && !p.exhausted && p.expanded < p.maxExp {
		return false
	}
	s.p4pend = nil
	if p4dbg {
		fmt.Fprintf(os.Stderr, "p4 retire exp=%d done=%v exh=%v max=%d fails=%d\n", p.expanded, p.done, p.exhausted, p.maxExp, p.fails)
	}
	res := p.finish4()
	s.stats.Plan4Exp = p.expanded
	if res == nil || !res.complete {
		return false
	}
	res = s.endPlan4(p, res, false, p.deadline)
	res.festPacked = true
	if Plan4AnytimeRestart {
		s.prePlan, s.prePlanAt = res, int(s.expandedN)
		return true
	}
	s.plan = res
	s.stats.Plan4Complete, s.stats.Plan4Swap = true, int(s.expandedN)
	s.tr.SetExtra("plan4_anytime_swap", int(s.expandedN))
	s.installPlanStats()
	return false
}

// dumpWitness (exp-610 diagnostic, env P4WIT): the best node's witness pulls
// root first, in Festival's trace notation (row col of the board without our
// padding; "base" marks a sink), with the expansion order and path weight.
func (p *planner4) dumpWitness() {
	w := int32(p.s.lv.W)
	yx := func(c int32) string { return fmt.Sprintf("%2d %2d", c/w-1, c%w-1) }
	var path []int32
	for id := p.best; id >= 0; id = p.nodes[id].parent {
		path = append(path, id)
	}
	slices.Reverse(path)
	fmt.Fprintf(os.Stderr, "witness root=%d player=%s depth=%d\n", path[0], yx(p.nodes[path[0]].player), len(path)-1)
	for d, id := range path[1:] {
		n := &p.nodes[id]
		tag := ""
		if n.mbase >= 0 {
			tag = "base "
		}
		wt := int32(-1)
		if int(id) < len(p.nodeW) {
			wt = p.nodeW[id]
		}
		fmt.Fprintf(os.Stderr, "wit d%-3d id%-6d w%-3d %s%s -> %s nb=%d\n", d+1, id, wt, tag, yx(n.mfrom), yx(n.mto), n.nb)
	}
}

// popReject (exp-689) applies the pop-time child filters (Festival checks
// move_is_deadlocked and can_played_reversed when a child is popped, not
// when it is generated): -plan4-pulldead and -plan4-revplay.
func (p *planner4) popReject(parent int32, ch *p2Child) bool {
	s := p.s
	p.setOcc(parent)
	s.occ[ch.from] = false
	if ch.base < 0 {
		s.occ[ch.to] = true
	}
	if p.lv.PullDead && ch.base < 0 {
		used := p.usedBits(parent)
		// p.alive for the parent's free bases (expand4 left the last expanded node's)
		clear(p.alive)
		for t := range p.baseCell {
			if used[t>>6]>>(uint(t)&63)&1 == 0 {
				for w, v := range p.reach[t] {
					p.alive[w] |= v
				}
			}
		}
		if p.pullDeadAt(ch.to, used) {
			p.st4.deadRej++
			return true
		}
		for _, d := range s.dirs {
			if s.occ[ch.to+d] && p.pullDeadAt(ch.to+d, used) {
				p.st4.deadRej++
				return true
			}
		}
	}
	if p.lv.RevPlay {
		nb := int(p.nodes[parent].nb)
		if ch.base >= 0 {
			nb--
		}
		if nb > 0 && s.regions() > 1 {
			s.reach(ch.player, s.mark)
			if cm := s.festCorral(ch.player, s.stamp); cm != 0 && (ch.base >= 0 || s.cmark[ch.to] != cm) {
				p.st4.revRej++
				return true
			}
		}
	}
	return false
}

// pullDeadAt (exp-689): the box on c can never be pulled again (each
// direction blocked by walls, frozen boxes or a cell outside every free
// base's pull reach) and the frozen group holds a box off an unused base.
// s.occ holds the position. Bounded like pullFreezeDead ("not dead" when
// the budget runs out).
func (p *planner4) pullDeadAt(c int32, used []uint64) bool {
	if len(p.fz) < len(p.s.lv.Wall) {
		p.fz = make([]bool, len(p.s.lv.Wall))
		p.fzNo = make([]uint32, len(p.s.lv.Wall))
	}
	p.fzEpoch++
	p.fzLeft = pullFreezeVisits
	fr, off := p.p4Frozen(c, used)
	return fr && off
}

func (p *planner4) p4Frozen(c int32, used []uint64) (frozen, off bool) {
	if p.fzNo[c] == p.fzEpoch || p.fzLeft <= 0 {
		return false, false
	}
	p.fzLeft--
	s := p.s
	p.fz[c] = true
	t := p.baseAt[c]
	off = t < 0 || used[t>>6]>>(uint(t)&63)&1 != 0
	frozen = true
	for _, d := range s.dirs {
		x1, x2 := c+d, c+2*d
		if s.lv.Wall[x1] || s.lv.Wall[x2] || p.fz[x1] || p.fz[x2] || p.alive[x1>>6]>>(uint(x1)&63)&1 == 0 {
			continue
		}
		if s.occ[x1] {
			if f, o := p.p4Frozen(x1, used); f {
				off = off || o
				continue
			}
		}
		if s.occ[x2] {
			if f, o := p.p4Frozen(x2, used); f {
				off = off || o
				continue
			}
		}
		frozen = false
		break
	}
	p.fz[c] = false
	if !frozen {
		p.fzNo[c] = p.fzEpoch
		return false, false
	}
	return true, off
}

// p4Levers (exp-690) are the exp-689 plan4 levers as per-planner values, so a
// plan-stream source can run them without the process-wide flags.
type p4Levers struct{ BestPast, NewCell, ConnAdv, NoNull, RevPlay, PullDead, Explorer bool }

func p4GlobalLevers() p4Levers {
	return p4Levers{Plan4BestPast, Plan4NewCell, Plan4ConnAdv, Plan4NoNull, Plan4RevPlay, Plan4PullDead, Plan4Explorer}
}

func (a p4Levers) or(b p4Levers) p4Levers {
	return p4Levers{a.BestPast || b.BestPast, a.NewCell || b.NewCell, a.ConnAdv || b.ConnAdv, a.NoNull || b.NoNull, a.RevPlay || b.RevPlay, a.PullDead || b.PullDead, a.Explorer || b.Explorer}
}
