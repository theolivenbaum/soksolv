package main

import (
	"container/heap"
	"encoding/binary"
	"math/bits"
	"slices"
	"soksolv/meet"
	"time"
)

// A phase is a configuration of already supplied boxes. Other boxes may be
// anywhere. Reversing a pull-to-sink witness produces these packing/parking
// checkpoints; they are guidance and never constrain legal forward moves.
type packingPhase struct {
	required    []int32
	player      int32
	reverseMove packingMove // witnessed transition from the next phase to this one
}
type packingPlan struct {
	phases             []packingPhase
	check              [][]int32 // per phase: required cells, those new in it first (fail fast)
	in                 []uint64  // per phase: bitset of required cells (stride inW words), for placed
	inW                int
	complete           bool
	expanded, parkings int
	remaining          int
	fest               *festPlan // PackedFest: lazily built
	festPacked         bool      // exp-527: packed = fest prefix for this plan (plan4 / its v2 fallback)
	dag                *packingDAG
	causalVisits       int
	occBits            []uint64 // nodeSetup scratch: the node's boxes as a cell bitset
	miss               []int32  // nodeSetup: per phase, required cells still empty
	near               []int32  // nodeSetup (exp-659 -lazy-child): phases with miss <= 1, last first
}

func (p *packingPlan) status(occ []bool) (phase, packed int) {
	if p.check == nil {
		p.prepareChecks()
	}
	for i := len(p.phases) - 1; i >= 0; i-- {
		ok := true
		for _, c := range p.check[i] {
			if !occ[c] {
				ok = false
				break
			}
		}
		if ok {
			return i, len(p.phases[i].required)
		}
	}
	// A bounded presearch can leave several boxes in its first checkpoint.
	// Credit each supplied box there; waiting for the whole configuration
	// would erase all packing guidance until every remaining box arrived.
	n := 0
	if len(p.phases) > 0 {
		for _, c := range p.phases[0].required {
			if occ[c] {
				n++
			}
		}
	}
	return -1, n
}

// nodeSetup (exp-419) records, for the node whose boxes are boxes, how many
// required cells of each phase are still empty, so childStatus can score a
// child (one box moved) in O(phases) instead of scanning the phases' cells.
func (p *packingPlan) nodeSetup(boxes []int32) {
	if p.in == nil {
		p.prepareChecks()
	}
	if len(p.occBits) != p.inW {
		p.occBits = make([]uint64, p.inW)
	}
	clear(p.occBits)
	for _, b := range boxes {
		if w := int(b >> 6); w < p.inW {
			p.occBits[w] |= 1 << (uint(b) & 63)
		}
	}
	if len(p.miss) != len(p.phases) {
		p.miss = make([]int32, len(p.phases))
	}
	for i := range p.phases {
		m := 0
		for w, v := range p.in[i*p.inW : (i+1)*p.inW] {
			m += bits.OnesCount64(v &^ p.occBits[w])
		}
		p.miss[i] = int32(m)
	}
	if LazyChild {
		p.near = p.near[:0]
		for i := len(p.phases) - 1; i >= 0; i-- {
			if p.miss[i] <= 1 {
				p.near = append(p.near, int32(i))
			}
		}
	}
}

// childStatus is status for the node of the last nodeSetup after the box on
// b moved to the empty cell t.
func (p *packingPlan) childStatus(b, t int32) (phase, packed int) {
	in := func(i int, c int32) int32 {
		if w := int(c >> 6); w < p.inW {
			return int32(p.in[i*p.inW+w] >> (uint(c) & 63) & 1)
		}
		return 0
	}
	if LazyChild {
		// One box moved changes a phase's miss by at most one: only the
		// phases missing at most one cell can be complete in the child.
		for _, i32 := range p.near {
			if i := int(i32); p.miss[i]+in(i, b)-in(i, t) == 0 {
				return i, len(p.phases[i].required)
			}
		}
	} else {
		for i := len(p.phases) - 1; i >= 0; i-- {
			if p.miss[i]+in(i, b)-in(i, t) == 0 {
				return i, len(p.phases[i].required)
			}
		}
	}
	if len(p.phases) > 0 {
		return -1, len(p.phases[0].required) - int(p.miss[0]+in(0, b)-in(0, t))
	}
	return -1, 0
}

func (p *packingPlan) placed(cell int32, phase int) bool {
	if phase < 0 {
		if len(p.phases) == 0 {
			return false
		}
		phase = 0
	}
	if p.in == nil {
		p.prepareChecks()
	}
	w := int(cell >> 6)
	return w < p.inW && p.in[phase*p.inW+w]>>(uint(cell)&63)&1 != 0
}

// placedRow is the placed bitset row of phase (placed(c, phase) is bit c of
// it; cells past its end are not placed). A non-nil empty row stands for
// "nothing placed".
func (p *packingPlan) placedRow(phase int) []uint64 {
	if phase < 0 {
		if len(p.phases) == 0 {
			return []uint64{}
		}
		phase = 0
	}
	if p.in == nil {
		p.prepareChecks()
	}
	return p.in[phase*p.inW : (phase+1)*p.inW : (phase+1)*p.inW]
}

type packingMove struct {
	from, to, player int32
	remove           bool
	weight           int32
	score            int
}
type packingNode struct {
	boxes  []int32
	player int32
	parent int
	move   packingMove
	weight int32
	moves  []packingMove
	next   int
	score  int
}
type packingCell struct{ boxes, goals int }
type packingEntry struct {
	node   int
	weight int32
	score  int
}
type packingHeap []packingEntry

func (h packingHeap) Len() int { return len(h) }
func (h packingHeap) Less(i, j int) bool {
	if h[i].weight != h[j].weight {
		return h[i].weight < h[j].weight
	}
	if h[i].score != h[j].score {
		return h[i].score > h[j].score
	}
	return h[i].node < h[j].node
}
func (h packingHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *packingHeap) Push(x any)   { *h = append(*h, x.(packingEntry)) }
func (h *packingHeap) Pop() any     { a := *h; x := a[len(a)-1]; *h = a[:len(a)-1]; return x }

type packingPlanner struct {
	s                     *solver
	sink, start, pushable []bool
	distance              []int
	nodes                 []packingNode
	seen                  map[string]int
	cells                 map[packingCell]int
	queues                []packingHeap
	nodeCell              []int
	next, best, expanded  int
	mark                  []uint32
	stamp                 uint32
	queue                 []int32
	deadline              time.Time
	maxNodes              int
	// Keep a small retained arena: planning is a presearch, not a second
	// unbounded search competing with the forward portfolio's memory.
	moveCount int
	cut       cutScan
}

func (p *packingPlanner) stopped() bool {
	return Stop.Load() || NodeBudget == 0 && time.Now().After(p.deadline) || len(p.nodes) >= p.maxNodes || p.moveCount >= 200000
}

func (p *packingPlanner) flood(player int32) (int32, uint32) {
	p.stamp++
	st := p.stamp
	q := append(p.queue[:0], player)
	p.mark[player] = st
	norm := player
	for i := 0; i < len(q); i++ {
		norm = min(norm, q[i])
		for _, d := range p.s.dirs {
			x := q[i] + d
			if !p.s.lv.Wall[x] && !p.s.occ[x] && p.mark[x] != st {
				p.mark[x] = st
				q = append(q, x)
			}
		}
	}
	p.queue = q
	return norm, st
}

// pullMoves retains distinct final player regions and checks player routing
// after every pull. Other boxes stay fixed throughout each macro.
//
// For one pulled box every other box is fixed, so the free graph G0 (the box
// removed) is the same for all its pulls. A pull puts the box on `to` and the
// player on a neighbour of `to`; the player's region is then the piece of G0
// minus `to` that contains the player. cutScan answers that from one DFS
// (low-links) per box instead of one flood per pull; see cutscan.go.
func (p *packingPlanner) pullMoves(boxes []int32, player int32) []packingMove {
	s := p.s
	cs := &p.cut
	cs.init(s)
	_, parentStamp := p.flood(player)
	pg := cs.newNode()
	for _, c := range p.queue {
		if p.mark[c] == parentStamp {
			cs.pgen[c] = pg
		}
	}
	var moves, removals []packingMove
	for _, b := range boxes {
		if p.stopped() {
			break
		}
		if p.sink[b] {
			s.occ[b] = false
			np, _ := p.flood(player)
			s.occ[b] = true
			removals = append(removals, packingMove{from: b, to: b, player: np, remove: true})
			continue
		}
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
		for qi := 0; qi < len(q); qi++ {
			if qi&31 == 0 && p.stopped() {
				break
			}
			k := q[qi]
			c, d := k/4, s.dirs[k%4]
			to, player := c+d, c+2*d
			if s.lv.Wall[to] || s.occ[to] || s.lv.Wall[player] || s.occ[player] {
				continue
			}
			np, sides := cs.query(to, player)
			// Save next pulling sides before removal changes the player region.
			for di := range s.dirs {
				k2 := to*4 + int32(di)
				if sides&(1<<uint(di)) != 0 && cs.seen[k2] != g {
					cs.seen[k2] = g
					q = append(q, k2)
				}
			}
			remove := p.sink[to]
			if remove {
				np = cs.compMin(to)
			}
			if !cs.newEnd(to, np) {
				continue
			}
			m := packingMove{from: b, to: to, player: np, remove: remove, weight: 1, score: p.distance[to] - p.distance[b]}
			if remove {
				m.weight = 0
				m.score = -p.distance[b]
				removals = append(removals, m)
			} else {
				moves = append(moves, m)
			}
		}
		cs.q = q
		s.occ[b] = true
	}
	// Infinite-capacity removal cannot crowd any other box, so it has
	// precedence over parking. It is not forward-search pruning.
	if len(removals) > 0 {
		slices.SortStableFunc(removals, func(a, b packingMove) int { return b.score - a.score })
		return removals
	}
	parentRegions := s.regions()
	advisor := -1
	bestScore := -int(^uint(0) >> 1)
	for i, m := range moves {
		if !s.lv.Goal[m.from] || !(p.pushable[m.to] || p.start[m.to]) {
			continue
		}
		s.occ[m.from] = false
		s.occ[m.to] = true
		regions := s.regions()
		s.occ[m.to] = false
		s.occ[m.from] = true
		if regions <= parentRegions && m.score > bestScore {
			advisor, bestScore = i, m.score
		}
	}
	if advisor >= 0 {
		moves[advisor].weight = 0
	}
	slices.SortStableFunc(moves, func(a, b packingMove) int {
		if a.weight != b.weight {
			return int(a.weight - b.weight)
		}
		return b.score - a.score
	})
	return moves
}

func packingKey(boxes []int32, player int32) string {
	b := make([]byte, 0, 2*(len(boxes)+1))
	b = binary.LittleEndian.AppendUint16(b, uint16(player))
	for _, c := range boxes {
		b = binary.LittleEndian.AppendUint16(b, uint16(c))
	}
	return string(b)
}

func (p *packingPlanner) add(boxes []int32, player int32, parent int, move packingMove, weight int32) int {
	key := packingKey(boxes, player)
	if _, ok := p.seen[key]; ok {
		return -1
	}
	id := len(p.nodes)
	p.seen[key] = id
	score, goals := 0, 0
	for _, b := range boxes {
		score += p.distance[b]
		if p.s.lv.Goal[b] {
			goals++
		}
	}
	p.nodes = append(p.nodes, packingNode{boxes: slices.Clone(boxes), player: player, parent: parent, move: move, weight: weight, score: score})
	if len(boxes) < len(p.nodes[p.best].boxes) || len(boxes) == len(p.nodes[p.best].boxes) && score > p.nodes[p.best].score {
		p.best = id
	}
	cell := packingCell{len(boxes), goals}
	ci, ok := p.cells[cell]
	if !ok {
		ci = len(p.queues)
		p.cells[cell] = ci
		p.queues = append(p.queues, nil)
	}
	p.nodeCell = append(p.nodeCell, ci)
	if len(boxes) > 0 && !p.stopped() {
		clear(p.s.occ)
		for _, b := range boxes {
			p.s.occ[b] = true
		}
		moves := p.pullMoves(boxes, player)
		p.nodes[id].moves = moves
		p.moveCount += len(moves)
		p.enqueue(id)
	}
	return id
}

func (p *packingPlanner) enqueue(id int) {
	n := &p.nodes[id]
	if n.next >= len(n.moves) {
		return
	}
	m := n.moves[n.next]
	heap.Push(&p.queues[p.nodeCell[id]], packingEntry{id, n.weight + m.weight, n.score + m.score})
}

func (p *packingPlanner) run() *packingPlan {
	// Every possible final player component is a root. A guessed final
	// component must not exclude the useful relaxed packing witness.
	clear(p.s.occ)
	for _, g := range p.s.goals {
		p.s.occ[g] = true
	}
	var roots []int32
	covered := make([]bool, len(p.s.lv.Wall))
	for _, c := range p.s.fcell {
		if !p.s.occ[c] && !covered[c] {
			norm, st := p.flood(c)
			roots = append(roots, norm)
			for _, x := range p.queue {
				if p.mark[x] == st {
					covered[x] = true
				}
			}
		}
	}
	for _, r := range roots {
		if p.stopped() {
			break
		}
		p.add(p.s.goals, r, -1, packingMove{}, 0)
	}
	if len(p.nodes) == 0 {
		return nil
	}
	for !p.stopped() && len(p.nodes[p.best].boxes) > 0 {
		found := -1
		for range len(p.queues) {
			ci := p.next
			p.next = (p.next + 1) % len(p.queues)
			if len(p.queues[ci]) > 0 {
				found = ci
				break
			}
		}
		if found < 0 {
			break
		}
		e := heap.Pop(&p.queues[found]).(packingEntry)
		n := &p.nodes[e.node]
		m := n.moves[n.next]
		n.next++
		p.enqueue(e.node)
		boxes := slices.Clone(n.boxes)
		i, _ := slices.BinarySearch(boxes, m.from)
		if m.remove {
			boxes = append(boxes[:i], boxes[i+1:]...)
		} else {
			boxes[i] = m.to
			slices.Sort(boxes)
		}
		weight := n.weight + m.weight
		p.expanded++
		p.add(boxes, m.player, e.node, m, weight)
	}
	best := p.best
	result := &packingPlan{complete: len(p.nodes[best].boxes) == 0, expanded: p.expanded, remaining: len(p.nodes[best].boxes)}
	for id := best; id >= 0; id = p.nodes[id].parent {
		n := &p.nodes[id]
		result.phases = append(result.phases, packingPhase{required: slices.Clone(n.boxes), player: n.player, reverseMove: n.move})
		if n.parent >= 0 && !n.move.remove {
			result.parkings++
		}
	}
	return result
}

func (s *solver) buildPackingPlan(deadline time.Time, maxNodes int) *packingPlan {
	if PlanV2 {
		return s.buildPlanV2(deadline, maxNodes)
	}
	if s.basin == nil || s.basin.sink < 0 {
		return nil
	}
	work := *s
	work.occ = make([]bool, len(s.lv.Wall))
	work.rmark = nil
	work.rqueue = nil
	work.rstamp = 0
	p := &packingPlanner{s: &work, sink: make([]bool, len(s.lv.Wall)), start: make([]bool, len(s.lv.Wall)), pushable: make([]bool, len(s.lv.Wall)), distance: make([]int, len(s.lv.Wall)), seen: map[string]int{}, cells: map[packingCell]int{}, mark: make([]uint32, len(s.lv.Wall)), deadline: deadline, maxNodes: maxNodes}
	for _, b := range s.lv.Boxes {
		p.start[b] = true
	}
	// A sink square only counts if a box can really be pushed onto it from a
	// start square without crossing a goal square (one box, others ignored):
	// otherwise "removing" a box
	// there imagines an arrival the forward search can never make (XSokoban
	// #39's first plan steps were such squares, beside the goal room).
	reach := make([]bool, len(s.lv.Wall))
	var fq []int32
	for _, b := range s.lv.Boxes {
		reach[b] = true
		fq = append(fq, b)
	}
	for i := 0; i < len(fq); i++ {
		c := fq[i]
		for _, d := range s.dirs {
			if to := c + d; !s.lv.Wall[c-d] && !s.lv.Wall[to] && !reach[to] && !s.lv.Goal[to] {
				reach[to] = true
				fq = append(fq, to)
			}
		}
	}
	var q []int32
	for _, c := range s.rooms.rooms[s.basin.sink] {
		if RealSink && !reach[c] {
			continue
		}
		p.sink[c] = true
		p.pushable[c] = true
		q = append(q, c)
	}
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			to, support := q[i]+d, q[i]-d
			if !s.lv.Wall[to] && !s.lv.Wall[support] && !p.pushable[to] {
				p.pushable[to] = true
				q = append(q, to)
			}
		}
	}
	for i := range p.distance {
		p.distance[i] = -1
	}
	q = q[:0]
	for _, g := range s.goals {
		p.distance[g] = 0
		q = append(q, g)
	}
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			x := q[i] + d
			if !s.lv.Wall[x] && p.distance[x] < 0 {
				p.distance[x] = p.distance[q[i]] + 1
				q = append(q, x)
			}
		}
	}
	return p.run()
}

// planFromTail builds a packing plan from a backward search's best state:
// replaying its tail (pushes from that state to the solved position), each
// goal is ordered by when its final box arrives. The backward search proves
// this order feasible from that state, unlike the relaxed planner's guess.
func planFromTail(lv *Level, dirs [4]int32, t *meet.Target) *packingPlan {
	occ := map[int32]bool{}
	for _, b := range t.Boxes {
		occ[b] = true
	}
	arrive := map[int32]int{}
	for _, b := range t.Boxes {
		if lv.Goal[b] {
			arrive[b] = -1 // on its goal from the start of the tail
		}
	}
	for k, pu := range t.Tail {
		to := pu.From + dirs[pu.Dir]
		delete(arrive, pu.From)
		occ[pu.From], occ[to] = false, true
		if lv.Goal[to] {
			arrive[to] = k
		}
	}
	var goals []int32
	for c, g := range lv.Goal {
		if g {
			goals = append(goals, int32(c))
		}
	}
	slices.SortStableFunc(goals, func(a, b int32) int {
		ka, oka := arrive[a]
		kb, okb := arrive[b]
		if !oka {
			ka = 1 << 30
		}
		if !okb {
			kb = 1 << 30
		}
		return ka - kb
	})
	plan := &packingPlan{complete: true}
	for k := range len(goals) + 1 {
		plan.phases = append(plan.phases, packingPhase{required: slices.Clone(goals[:k])})
	}
	return plan
}

// prepareChecks orders each phase's required cells for status: cells the
// previous phase did not require come first, since they are the least
// likely to be occupied, so a phase that isn't reached fails after a check
// or two instead of a scan. The set (and so the result) is unchanged.
func (p *packingPlan) prepareChecks() {
	maxc := int32(0)
	for _, ph := range p.phases {
		for _, c := range ph.required {
			maxc = max(maxc, c)
		}
	}
	inW := int(maxc>>6) + 1
	in := make([]uint64, len(p.phases)*inW)
	for i, ph := range p.phases {
		for _, c := range ph.required {
			in[i*inW+int(c>>6)] |= 1 << (uint(c) & 63)
		}
	}
	p.inW, p.in = inW, in
	p.check = make([][]int32, len(p.phases))
	for i, ph := range p.phases {
		var prev []int32
		if i > 0 {
			prev = p.phases[i-1].required
		}
		var fresh, old []int32
		for _, c := range ph.required {
			if slices.Contains(prev, c) {
				old = append(old, c)
			} else {
				fresh = append(fresh, c)
			}
		}
		p.check[i] = append(fresh, old...)
	}
}

// RealSink restricts the planner's sink to squares a box can be pushed onto
// from a start square.
var RealSink = true

var DebugPlan = false
