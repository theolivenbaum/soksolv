package main

import (
	"container/heap"
	"slices"
	"soksolv/holes"
	"soksolv/trace"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/verify"
)

// RetargetReuse (exp-299): active graph reuse on retarget without restarting from root.
var RetargetReuse = true

// Target-directed FESS (exp-177, after Festival's HF strategy): FESS on the
// level with its goals moved to a backward-search state's box squares.
// Reaching that board (with the player in the target's region) and then
// replaying the state's backward tail solves the real level; meeting the
// backward perimeter on the way does too.

var TargetFloorFix = true
var TargetProofPrivate = true

// rowsWithGoals rewrites rows so the goals are exactly the given cells.
func rowsWithGoals(rows []string, w int, goals []int32) []string {
	out := make([][]byte, len(rows))
	for y, r := range rows {
		b := []byte(r)
		for x, ch := range b {
			switch ch {
			case '.':
				b[x] = ' '
			case '*':
				b[x] = '$'
			case '+':
				b[x] = '@'
			}
		}
		out[y] = b
	}
	for _, g := range goals {
		y, x := int(g)/w-1, int(g)%w-1
		for len(out[y]) <= x {
			out[y] = append(out[y], ' ')
		}
		switch out[y][x] {
		case ' ', '-', '_':
			if TargetFloorFix || out[y][x] == ' ' {
				out[y][x] = '.'
			}
		case '$':
			out[y][x] = '*'
		case '@':
			out[y][x] = '+'
		}
	}
	res := make([]string, len(out))
	for i, b := range out {
		res[i] = string(b) // no trimming: the width (and so every cell index) must not change
	}
	return res
}

// fessToward runs FESS toward tgt and returns a verified solution of the real
// level (rows) or "".
func fessToward(rows []string, lv *Level, tgt *meet.Target, deadline time.Time, memLimit uint64, tr *trace.Recorder) (string, Result) {
	if TowardHF {
		return fessTowardReal(rows, lv, tgt, deadline, memLimit, tr, &Budget3, &Tracked3, 2, 0)
	}
	rows2 := rowsWithGoals(rows, lv.W, tgt.Boxes)
	lv2, err := parseLevelRaw(rows2)
	if err != nil {
		return "", Result{Reason: err.Error()}
	}
	if lv2.W != lv.W || lv2.H != lv.H {
		return "", Result{Reason: "target board geometry differs"}
	}
	if holes.Enabled { // exp-464: this board closes the real level's holes, not its own
		for c, w := range lv.Wall {
			if w && !lv2.Wall[c] {
				if lv2.Goal[c] {
					return "", Result{Reason: "target in a closed hole"}
				}
				lv2.Wall[c] = true
			}
		}
	}
	s := newSolverUntil(lv2, deadline)
	if s.cl != nil {
		s.cl.Private = TargetProofPrivate
	}
	if s.dz != nil {
		s.dz.Private = TargetProofPrivate
	}
	s.rows = rows2
	s.tgtFull, s.targetRows = tgt, rows
	// Its own memory slot and forward mailbox (2: unused outside wide mode).
	s.budgetP, s.trackedP, s.owner = &Budget3, &Tracked3, 2
	s.connFirst = TowardConnFirst
	s.noPlan = TowardNoPlan
	if tr != nil {
		s.tr = tr
	}
	res := s.solve(deadline, memLimit)
	if !res.Solved {
		return "", res
	}
	if verify.Check(rows, res.Solution) == nil {
		return res.Solution, res // met the backward perimeter: already complete
	}
	at := meet.EndCell(res.Solution, lv.Player, lv.W)
	tail := meet.Complete(lv.Wall, lv.W, tgt.Boxes, at, tgt.Tail)
	sol := res.Solution + tail
	if (tail != "" || len(tgt.Tail) == 0) && verify.Check(rows, sol) == nil {
		return sol, res
	}
	return "", Result{Reason: "target region unreachable"}
}

// TowardConnFirst ranks the toward search's moves by connectivity before
// boxes on the target board, as Festival's forward HF search does, so both
// directions concentrate on well-connected positions and meet there.
var TowardConnFirst = true

// TowardNoPlan: the toward search uses no packing plan (Festival's forward HF
// search has none): its packed feature is simply the boxes on the target
// board's squares (exp-188).
var TowardNoPlan = false

// TowardHF: the toward search runs on the real level (real goals, deadlocks
// and meetings) and ranks like Festival's forward HF search against the
// target board: connectivity, boxes on the target's squares, hotspots
// against the target, min-cost matching distance (exp-189).
var TowardHF = true

// HFPullNodes fixes standalone HF's pull work independently of the forward
// budget. With -nodes 0, the forward phase uses the remaining wall time.
var HFPullNodes int64

func fessTowardRealWithPrev(rows []string, lv *Level, tgt *meet.Target, deadline time.Time, memLimit uint64, tr *trace.Recorder, budget, tracked *atomic.Uint64, owner int, nodeLimit int64, prevTrees []*solver) (string, Result, *solver) {
	s := newTowardReal(rows, lv, tgt, deadline, tr, budget, tracked, owner, nodeLimit)
	if RetainFwdMail && len(prevTrees) > 0 {
		s.prevTrees = slices.Clone(prevTrees)
	}
	sol, res := s.towardRun(deadline, memLimit)
	return sol, res, s
}

func fessTowardReal(rows []string, lv *Level, tgt *meet.Target, deadline time.Time, memLimit uint64, tr *trace.Recorder, budget, tracked *atomic.Uint64, owner int, nodeLimit int64) (string, Result) {
	sol, res, _ := fessTowardRealWithPrev(rows, lv, tgt, deadline, memLimit, tr, budget, tracked, owner, nodeLimit, nil)
	return sol, res
}

// newTowardReal builds the real-level toward search aimed at tgt; towardRun
// runs it (again, in rounds: exp-278) and completes a solution.
func newTowardReal(rows []string, lv *Level, tgt *meet.Target, deadline time.Time, tr *trace.Recorder, budget, tracked *atomic.Uint64, owner int, nodeLimit int64) *solver {
	t0 := time.Now()
	s := newSolverUntil(lv, deadline)
	s.rows = rows
	s.budgetP, s.trackedP, s.owner = budget, tracked, owner
	if owner == 2 {
		s.stopCheck = towardStop
	}
	s.nodeLimit = nodeLimit
	s.setTarget(tgt.Boxes)
	s.tgtFull = tgt
	s.setTargetPockets(tgt)
	s.noPlan, s.hfRank = true, true
	if tr != nil {
		s.tr = tr
	}
	s.setupDuration = time.Since(t0)
	s.stats.RetargetSetupMs = float64(s.setupDuration.Microseconds()) / 1000.0
	return s
}

func (s *solver) towardRun(deadline time.Time, memLimit uint64) (string, Result) {
	rows, lv, tgt := s.rows, s.lv, s.tgtFull
	if s.resume == nil && s.onTarget(lv.Boxes) {
		tail := meet.Complete(lv.Wall, lv.W, tgt.Boxes, lv.Player, tgt.Tail)
		if (tail != "" || len(tgt.Tail) == 0) && verify.Check(rows, tail) == nil {
			return tail, Result{Solved: true, Solution: tail}
		}
	}
	res := s.solve(deadline, memLimit)
	if !res.Solved {
		return "", res
	}
	if verify.Check(rows, res.Solution) == nil {
		return res.Solution, res // solved outright or met the backward perimeter
	}
	at := meet.EndCell(res.Solution, lv.Player, lv.W)
	tail := meet.Complete(lv.Wall, lv.W, tgt.Boxes, at, tgt.Tail)
	sol := res.Solution + tail
	if (tail != "" || len(tgt.Tail) == 0) && verify.Check(rows, sol) == nil {
		return sol, res
	}
	return "", Result{Reason: "target region unreachable"}
}

func (s *solver) completeRootTarget() bool {
	if verify.Check(s.targetRows, "") == nil {
		return true
	}
	tail := meet.Complete(s.lv.Wall, s.lv.W, s.tgtFull.Boxes, s.lv.Player, s.tgtFull.Tail)
	return (tail != "" || len(s.tgtFull.Tail) == 0) && verify.Check(s.targetRows, tail) == nil
}

// completeTarget verifies the concrete suffix before declaring a target hit.
// Equal box occupancy alone does not imply that its first push is accessible.
func (s *solver) completeTarget(id int32) (string, bool) {
	prefix := s.reconstruct(id)
	rows := s.rows
	if s.targetRows != nil {
		rows = s.targetRows
	}
	if verify.Check(rows, prefix) == nil {
		return prefix, true
	}
	at := meet.EndCell(prefix, s.lv.Player, s.lv.W)
	tail := meet.Complete(s.lv.Wall, s.lv.W, s.tgtFull.Boxes, at, s.tgtFull.Tail)
	sol := prefix + tail
	return sol, (tail != "" || len(s.tgtFull.Tail) == 0) && verify.Check(rows, sol) == nil
}

func (s *solver) lazyScore(id int32) moveScore {
	if s.hfRank && HFFestSched && int(id) < len(s.hfOwn) {
		own := s.hfOwn[id]
		oop := int(own.reg)
		if HFMatchFirst && own.inf {
			oop += hfNoMatch
		}
		return fessScore{oop: oop, packed: int(own.on), conn: int(own.reg), room: int(own.hot), hot: int(own.dist)}.compact()
	}
	cell := s.actual[id]
	return fessScore{oop: cell.oop, packed: cell.packed, conn: int(s.reg[id]), room: cell.connectivity, hot: 0}.compact()
}

// retargetTo re-keys the existing live search graph toward a new target board
// without restarting from the root.
func (s *solver) retargetTo(tgt *meet.Target) (string, bool) {
	if s.memFull || len(s.player) == 0 || len(s.player) > 16384 {
		return "", false
	}

	s.setTarget(tgt.Boxes)
	s.tgtFull = tgt
	s.setTargetPockets(tgt)
	s.cancelled = false
	s.reopen = -1
	s.lastImprove = time.Now()
	s.stallTries = 0
	s.meetPending = s.meetPending[:0]
	s.pendCands = s.pendCands[:0]

	s.frontier = fessFrontier{best: -1}
	if s.cellIDs == nil {
		s.cellIDs = map[featureCell]int{}
	} else {
		clear(s.cellIDs)
	}
	s.mv = s.mv[:0]
	s.mvScore = s.mvScore[:0]
	s.edges = s.edges[:0]
	s.novWords = s.novWords[:0]
	s.revWords = s.revWords[:0]
	s.hfOwn = s.hfOwn[:0]
	s.hfBest = s.hfBest[:0]

	boxes := make([]int32, s.n)
	nNodes := int32(len(s.player))
	for id := int32(0); id < nNodes; id++ {
		s.unpack(id, boxes)
		if s.onTarget(boxes) {
			if sol, ok := s.completeTarget(id); ok {
				return sol, true
			}
		}

		s.dead[id] = false

		s.version[id]++
		s.w[id] = 0
		s.edgeHead[id] = -1
		s.liveChildren[id] = 0
		s.mvStart[id] = -1
		s.mvLen[id] = 0
		s.mvNext[id] = 0
		s.mvLeft[id] = 0
		s.mvOff[id] = 0
		s.mvTotal[id] = 0
		s.novOffset[id] = 0
		s.revOffset[id] = 0
		s.revBestExit[id] = -1

		for _, b := range boxes {
			s.occ[b] = true
		}
		reg := int(s.reg[id])
		onGoal := s.packed()
		hot := s.hotRel(boxes)
		dist := s.matchDist(boxes)
		for _, b := range boxes {
			s.occ[b] = false
		}

		cell := featureCell{packed: onGoal, connectivity: hot, oop: reg}
		if HFMatchFirst && (dist >= 1<<20 || HFFrozenMatch && s.hfFrozenOff(boxes)) {
			cell.oop += hfNoMatch
		}
		s.actual[id] = cell
		s.addHF = int32(min(dist, 65535))

		parent := s.parent[id]
		bucket := 0
		if s.pfOn {
			bucket = s.pfAdd(boxes, parent, reg)
		} else if s.hfRank && HFFestSched {
			p := parent
			if p >= id || p >= int32(len(s.hfBest)) {
				p = -1
			}
			bucket = s.hfSchedAdd(boxes, cell, p, reg)
		} else {
			p := parent
			if p >= id {
				p = -1
			}
			bucket = s.project(cell, p)
		}
		s.addHF = -1
		s.bucket[id] = uint32(bucket)

		if !s.dead[id] {
			s.noteBest(id)
			ms := s.lazyScore(id)
			heap.Push(&s.frontier.queues[s.bucket[id]], fessEntry{
				node:    id,
				move:    -1,
				weight:  0,
				version: s.version[id],
				score:   ms,
			})
		}
	}
	for id := int32(1); id < nNodes; id++ {
		if p := s.parent[id]; p >= 0 && p < nNodes && !s.dead[id] {
			s.recordEdge(p, id)
		}
	}
	s.retargetNodes = nNodes
	return "", false
}

// setTargetPockets (exp-460, -hf-reg-rel) marks the floor cells of the target
// board that its player cannot reach and that hold no target box: pockets the
// target itself seals. regions() then ignores components made only of them.
func (s *solver) setTargetPockets(tgt *meet.Target) {
	s.tgtPocket = nil
	if !HFRegRel && !s.regRel {
		return
	}
	lv := s.lv
	occ := make([]bool, len(lv.Wall))
	for _, b := range tgt.Boxes {
		occ[b] = true
	}
	seen := make([]bool, len(lv.Wall))
	q := []int32{tgt.Player}
	seen[tgt.Player] = true
	for i := 0; i < len(q); i++ {
		for _, d := range s.dirs {
			if y := q[i] + d; !seen[y] && !lv.Wall[y] && !occ[y] {
				seen[y] = true
				q = append(q, y)
			}
		}
	}
	pk := make([]bool, len(lv.Wall))
	for c := range pk {
		pk[c] = !lv.Wall[c] && !occ[c] && !seen[c]
	}
	s.tgtPocket = pk
	s.stats.RegRelTargets++
}
