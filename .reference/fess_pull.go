package main

import (
	"slices"
	"soksolv/deadzone"
	"time"

	"soksolv/meet"
)

// Pull-mode FESS (exp-179): the FESS engine run backward, from the solved
// position toward the start, with pull macros (see front, playerAfter,
// supportCell). It is a backward lane: it fills the backward perimeter,
// completes meetings with forward searchers' prefixes, and publishes its best
// state (by FESS features, so a well-connected one) as a forward target.

// PullLane is the backward mailbox and Best slot of the pull-FESS lane.
const PullLane = 3

// FestBLane is the backward mailbox and Best slot of the festb lane (exp-276).
const FestBLane = 4

// newPullSolver builds the pull view of lv: boxes on the goals, the start
// squares as goals, one root per player zone of the solved position.
func newPullSolver(lv *Level, deadline time.Time) *solver {
	start := make([]bool, len(lv.Wall))
	for _, b := range lv.Boxes {
		start[b] = true
	}
	var goals []int32
	occ := make([]bool, len(lv.Wall))
	for c, g := range lv.Goal {
		if g {
			goals = append(goals, int32(c))
			occ[c] = true
		}
	}
	// Zones of the solved position (free floor components).
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	seen := make([]bool, len(lv.Wall))
	var roots []int32
	endRoot := int32(-1)
	for c := range int32(len(lv.Wall)) {
		if lv.Wall[c] || occ[c] || seen[c] {
			continue
		}
		roots = append(roots, c)
		q := []int32{c}
		seen[c] = true
		for i := 0; i < len(q); i++ {
			for _, d := range dirs {
				if y := q[i] + d; !lv.Wall[y] && !occ[y] && !seen[y] {
					seen[y] = true
					q = append(q, y)
				}
			}
		}
		if relayEndLv == lv && relayEnd >= 0 && slices.Contains(q, int32(relayEnd)) {
			endRoot = c // exp-761 back-trial child: the suffix starts in this region
		}
	}
	if endRoot >= 0 {
		roots = []int32{endRoot}
	}
	pv := &Level{W: lv.W, H: lv.H, Wall: lv.Wall, Goal: start, Boxes: goals, Player: lv.Player}
	if len(roots) > 0 {
		pv.Player = roots[0]
	}
	s := newSolverDir(pv, deadline, true)
	s.realPlayer = lv.Player
	// Push distance from each cell to the real goals (0 on a goal, a large
	// value where no goal is reachable): the spread feature.
	s.spreadDist = make([]int32, len(lv.Wall))
	for i := range s.spreadDist {
		s.spreadDist[i] = -1
	}
	q := slices.Clone(goals)
	for _, g := range goals {
		s.spreadDist[g] = 0
	}
	for i := 0; i < len(q); i++ {
		c := q[i]
		for _, d := range dirs {
			if b := c - d; !lv.Wall[b] && !lv.Wall[b-d] && s.spreadDist[b] < 0 {
				s.spreadDist[b] = s.spreadDist[c] + 1
				q = append(q, b)
			}
		}
	}
	for i, d := range s.spreadDist {
		if d < 0 {
			s.spreadDist[i] = 0
		}
	}
	s.pullRoots = roots
	s.owner = PullLane
	s.bestSlot = PullLane
	s.pfSty = pfStyle{types: PFTypes} // hub-pull sets spread (festXStyle); exp-640 removed -pf-spread
	return s
}

// pullTail returns the forward pushes taking node id's position to the
// solved position: its pull path undone in reverse (s.occ must be clear).
func (s *solver) pullTail(id int32) []meet.Push {
	opposite := [4]int8{1, 0, 3, 2}
	var out []meet.Push
	pc, cc := make([]int32, s.n), make([]int32, s.n)
	for ; s.parent[id] >= 0; id = s.parent[id] {
		p := s.parent[id]
		s.unpack(p, pc)
		s.unpack(id, cc)
		t := s.pushFrom[id]
		for _, x := range cc {
			if !slices.Contains(pc, x) {
				t = x
			}
		}
		for _, x := range pc {
			s.occ[x] = true
		}
		s.reach(s.player[p], s.mark)
		seq := s.boxPathTo(s.pushFrom[id], t, s.stamp, int32(s.pushDir[id]))
		for _, x := range pc {
			s.occ[x] = false
		}
		for j := len(seq) - 1; j >= 0; j-- {
			pu := seq[j] // box pulled from pu.from to pu.from+d: forward, pushed back
			out = append(out, meet.Push{From: pu.from + s.dirs[pu.dir], Dir: opposite[pu.dir]})
		}
	}
	return out
}

// pullSolution turns a node at the start position into a forward solution.
func (s *solver) pullSolution(id int32) string {
	boxes := make([]int32, s.n)
	s.unpack(id, boxes)
	tail := s.pullTail(id)
	return meet.Complete(s.lv.Wall, s.lv.W, boxes, s.realPlayer, tail)
}

func (s *solver) pullPostMeetings(cur []int32) {
	for _, pc := range s.pendCands {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		if ref, ok := s.refGet(pc.k); ok {
			if boxes, tail := s.candTail(ref); boxes != nil {
				s.meetPosted++
				s.back.Post(meet.Meeting{Boxes: boxes, Player: pc.np, Tail: tail, From: "pull"})
			}
		}
	}
	s.pendCands = s.pendCands[:0]
	for _, id := range s.meetPending {
		if s.meetPosted >= maxMeetPosts {
			break
		}
		s.meetPosted++
		s.unpack(id, cur)
		s.back.Post(meet.Meeting{Boxes: slices.Clone(cur), Player: s.player[id], Tail: s.pullTail(id), From: "pull"})
	}
	s.meetPending = s.meetPending[:0]
}

// pullResolve finds a received meeting's state in this pull tree and returns
// the tail from it to the goals.
func (s *solver) pullResolve(m meet.Meeting, tab *meet.Table) (tail []meet.Push, found bool) {
	if len(m.Boxes) != s.n || slices.ContainsFunc(m.Boxes, func(b int32) bool { return s.fidx[b] < 0 }) {
		return nil, false
	}
	if _, id, _ := s.find(s.pack(m.Boxes), m.Player); id >= 0 {
		return s.pullTail(id), true
	}
	if ref, ok := s.refGet(tab.Key(m.Boxes, m.Player)); ok {
		// A candidate child never stored: resolve it through its parent.
		if boxes, tl := s.candTail(ref); boxes != nil {
			s.tr.SetExtra("met_via_ref", 1) // exp-659
			return tl, true
		}
	}
	return nil, false
}

// pullCheckMail completes forward searchers' meetings: their prefix reaches a
// state this search stored; its tail finishes the level.
func (s *solver) pullCheckMail() (string, bool) {
	if s.fwd == nil {
		return "", false // the search never started (or has no perimeter)
	}
	for {
		m, ok := s.fwd.Receive(s.owner)
		if !ok {
			return "", false
		}
		if len(m.Boxes) != s.n || slices.ContainsFunc(m.Boxes, func(b int32) bool { return s.fidx[b] < 0 }) {
			continue
		}
		tail, found := s.pullResolve(m, s.fwd)
		if found {
			s.fwd.Confirmed.Add(1)
			if t := meet.Complete(s.lv.Wall, s.lv.W, m.Boxes, m.At, tail); t != "" || len(tail) == 0 {
				// exp-545 diagnostic: which forward searcher supplied the prefix.
				s.tr.SetExtra("met_lane", m.Lane)
				s.tr.SetExtra("met_owner", m.Owner)
				s.tr.SetExtra("met_prefix_pushes", countPushes(m.Prefix))
				s.tr.SetExtra("met_tail_pushes", len(tail))
				return m.Prefix + t, true
			}
		}
	}
}

// bestTarget is the best node by FESS features as a forward target.
func (s *solver) bestTarget() *meet.Target {
	id := s.frontier.best
	if id < 0 {
		return nil
	}
	boxes := make([]int32, s.n)
	s.unpack(id, boxes)
	return &meet.Target{Boxes: boxes, Player: s.player[id], Tail: s.pullTail(id)}
}

// publishPullBest offers the best node by FESS features as a forward target.
func (s *solver) publishPullBest() {
	id := s.frontier.best
	if id < 0 || id == s.lastPub || time.Since(s.lastPubAt) < 100*time.Millisecond {
		return
	}
	s.lastPub, s.lastPubAt = id, time.Now()
	boxes := make([]int32, s.n)
	s.unpack(id, boxes)
	h := int32(0)
	for _, b := range boxes {
		h += s.dist[b]
	}
	meet.StoreBest(s.bestSlot, &meet.Target{Boxes: boxes, Player: s.player[id], Tail: s.pullTail(id), H: h})
}

// Candidate children in the backward perimeter (pull lane). Each published
// key maps to a reference (parent node, box index, target cell, final side)
// in a flat open-addressing table, so a meeting on a child that was never
// stored still resolves to a tail: that macro reversed, then the parent's.

const refEmpty = 0

func packRef(parent int32, i int, t int32, side int32) uint64 {
	return uint64(uint32(parent)) | uint64(i)<<32 | uint64(t)<<44 | uint64(side)<<60
}

func unpackRef(r uint64) (parent int32, i int, t int32, side int32) {
	return int32(uint32(r)), int(r >> 32 & 0xFFF), int32(r >> 44 & 0xFFFF), int32(r >> 60 & 3)
}

func (s *solver) refPut(k, ref uint64) {
	if s.refs == nil {
		s.refs = make([]uint64, 2<<16)
	}
	if s.refN*4 >= len(s.refs)/2*3 { // 3/4 load: double
		old := s.refs
		s.refs = make([]uint64, 2*len(old))
		s.refN = 0
		for j := 0; j < len(old); j += 2 {
			if old[j] != refEmpty {
				s.refInsert(old[j], old[j+1])
			}
		}
	}
	s.refInsert(k, ref)
}

func (s *solver) refInsert(k, ref uint64) {
	mask := uint64(len(s.refs)/2 - 1)
	for i := k & mask; ; i = (i + 1) & mask {
		if s.refs[2*i] == refEmpty {
			s.refs[2*i], s.refs[2*i+1] = k, ref
			s.refN++
			return
		}
		if s.refs[2*i] == k {
			return // first reference wins
		}
	}
}

func (s *solver) refGet(k uint64) (uint64, bool) {
	if s.refs == nil {
		return 0, false
	}
	mask := uint64(len(s.refs)/2 - 1)
	for i := k & mask; ; i = (i + 1) & mask {
		switch s.refs[2*i] {
		case refEmpty:
			return 0, false
		case k:
			return s.refs[2*i+1], true
		}
	}
}

// publishCandidate adds the child (box i of cur moved to t, final side) of
// node id to the backward perimeter, with a reference to resolve it.
func (s *solver) publishCandidate(id int32, cur []int32, i int, t, side int32) {
	np := s.candNP
	if np < 0 || !CleanPerimeter {
		np = s.queue[0]
		for _, c := range s.queue[1:] {
			np = min(np, c)
		}
	}
	child := s.candKey[:0]
	child = append(child, cur...)
	child[i] = t
	s.candKey = child
	k := s.fwd.Key(child, np)
	s.fwd.Add(k)
	s.refPut(k, packRef(id, i, t, side))
	if s.meetAudit != nil {
		if s.pull {
			s.meetAudit.probe(s, k, child, np, packRef(id, i, t, side), -1)
		} else {
			s.meetAudit.published(k, s.expandedN)
		}
	}
	if s.back != nil && s.meetPosted < maxMeetPosts && s.back.Has(k) {
		s.pendCands = append(s.pendCands, pendingCand{k: k, np: np})
		s.noteMeet()
	}
}

// noteMeet records when the first perimeter hit happened.
func (s *solver) noteMeet() {
	if s.firstMeet == 0 {
		s.firstMeet = s.expandedN + 1
	}
}

type pendingCand struct {
	k  uint64
	np int32
}

// candTail resolves a referenced child's tail: its macro reversed, then the
// parent's tail (s.occ must be clear).
func (s *solver) candTail(ref uint64) (boxes []int32, tail []meet.Push) {
	opposite := [4]int8{1, 0, 3, 2}
	p, i, t, side := unpackRef(ref)
	pc := make([]int32, s.n)
	s.unpack(p, pc)
	b := pc[i]
	for _, x := range pc {
		s.occ[x] = true
	}
	s.reach(s.player[p], s.mark)
	seq := s.boxPathTo(b, t, s.stamp, side)
	for _, x := range pc {
		s.occ[x] = false
	}
	if len(seq) == 0 {
		return nil, nil
	}
	for j := len(seq) - 1; j >= 0; j-- {
		pu := seq[j]
		tail = append(tail, meet.Push{From: pu.from + s.dirs[pu.dir], Dir: opposite[pu.dir]})
	}
	tail = append(tail, s.pullTail(p)...)
	boxes = slices.Clone(pc)
	boxes[i] = t
	return boxes, tail
}

// DensePerimeter publishes FESS's candidate children to the perimeters
// (exp-179); otherwise only applied moves are published.
var DensePerimeter = true

// Pull-side freeze deadlock (exp-183): the pull analogue of freeze. Pulling
// the box on c toward +d needs the player on c+d and room to step back to
// c+2d; the box lands on c+d. That direction is blocked by walls, by boxes
// frozen themselves (cells under examination count as walls: mutually
// blocking boxes can't move first), or when c+d is a pull-dead square. A
// box blocked in both directions of both axes never moves again; frozen off
// a start square (the pull view's goal), the position can't reach the start.
func (s *solver) pullFreezeDead(t int32) bool {
	// One query: boxes proven pullable stay proven (sound: in-progress boxes
	// count as blocked, which only makes boxes look more frozen), and the
	// query gives up after pullFreezeVisits boxes (answering "not dead" is
	// always sound). Without both, dense box fields made it exponential.
	s.fzEpoch++
	s.fzLeft = pullFreezeVisits
	frozen, off := s.pullFrozen(t)
	return frozen && off
}

const pullFreezeVisits = 64

// pullFrozen reports whether the box on c can never be pulled again, and
// whether the frozen group that proves it has a box off a start square (in
// the pull view, start squares are goals). Only subproofs the result relies
// on contribute to off.
func (s *solver) pullFrozen(c int32) (frozen, off bool) {
	if s.fzNo[c] == s.fzEpoch || s.fzLeft <= 0 {
		return false, false
	}
	s.fzLeft--
	s.fz[c] = true
	off = !s.lv.Goal[c]
	w := int32(s.lv.W)
	frozen = true
	for _, d := range [4]int32{1, -1, w, -w} {
		b, o := s.pullBlockedDir(c, d)
		if !b {
			frozen = false
			break
		}
		off = off || o
	}
	s.fz[c] = false
	if !frozen {
		s.fzNo[c] = s.fzEpoch
		return false, false
	}
	return true, off
}

// pullBlockedDir: the box on c can't be pulled toward d (the player would
// stand on c+d and then step to c+2d).
func (s *solver) pullBlockedDir(c, d int32) (blocked, off bool) {
	x1, x2 := c+d, c+2*d
	wall := s.lv.Wall
	if wall[x1] || wall[x2] || s.fz[x1] || s.fz[x2] || s.dist[x1] < 0 {
		return true, false
	}
	if s.occ[x1] {
		if f, o := s.pullFrozen(x1); f {
			return true, o
		}
	}
	if s.occ[x2] {
		if f, o := s.pullFrozen(x2); f {
			return true, o
		}
	}
	return false, false
}

// newLevelPullChecker builds a pull-mode corral prover for lv (start squares
// = the level's box squares; pull-dead = cells no box from a start square
// can be pushed to). exp-241.
func newLevelPullChecker(lv *Level, capStates int) *deadzone.PullChecker {
	n := len(lv.Wall)
	start := make([]bool, n)
	dist := make([]int32, n)
	for i := range dist {
		dist[i] = -1
	}
	var q []int32
	for _, b := range lv.Boxes {
		start[b] = true
		dist[b] = 0
		q = append(q, b)
	}
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	for i := 0; i < len(q); i++ {
		for _, d := range dirs {
			c := q[i]
			if !lv.Wall[c-d] && !lv.Wall[c+d] && dist[c+d] < 0 {
				dist[c+d] = dist[c] + 1
				q = append(q, c+d)
			}
		}
	}
	pd := make([]bool, n)
	for c := range pd {
		pd[c] = !lv.Wall[c] && dist[c] < 0
	}
	return deadzone.NewPullChecker(&deadzone.PullBoard{Board: deadzone.NewBoard(lv.Wall, make([]bool, n), pd, lv.W), Start: start, PullDead: pd}, capStates)
}

// pullDeadTarget reports whether a backward state can't reach the start.
func pullDeadTarget(pc *deadzone.PullChecker, lv *Level, boxes []int32, player int32) bool {
	occ := make([]bool, len(lv.Wall))
	for _, b := range boxes {
		occ[b] = true
	}
	return pc.Dead(occ, boxes, player)
}

// countPushes counts the pushes (upper-case moves) in a LURD string.
func countPushes(lurd string) int {
	n := 0
	for _, c := range lurd {
		if c >= 'A' && c <= 'Z' {
			n++
		}
	}
	return n
}
