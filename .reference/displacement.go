package main

import (
	"math/bits"
)

// DisplacementLandmarks precomputes single-pin reachability and evaluates
// Certified Mandatory Displacement (CMD, Theorem 1) and Collective Goal-Wall
// Unpacking (Theorem 2) certificates.
//
// 100% deductive certificates with ZERO offline training or heuristics:
// - Theorem 1: If box on goal p never moves, every push of any other box b' in a
//   valid solution is a valid 1-box push on Board \ (Walls U {p}). If the other
//   boxes cannot be injectively matched to Goals \ {p}, p MUST move.
// - Theorem 2: If NO box in B \cap Goals ever moves, they all act as permanent walls.
//   If unpacked boxes B \ Goals cannot be injectively matched to Goals \ B on
//   Board \ (Walls U (B \cap Goals)), at least one occupied goal MUST be unpacked.

var DisplacementLandmarksOn = true

type DisplacementLandmarks struct {
	lv       *Level
	dirs     [4]int32
	cells    int
	numGoals int
	goals    []int32
	goalIdx  []int32 // cell -> goal index [0, numGoals), -1 if not goal

	// pinnedGoalMask[pIdx][sq*4 + d]: bitmask of reachable goals in Goals \ {p}
	// by 1-box pushes from (sq, d) when goal pIdx is a permanent wall.
	pinnedGoalMask [][]uint64

	// pinComp[pIdx][cell]: static connected component of Board \ (Walls U {p})
	// allows O(1) player reachability queries without any BFS during search.
	pinComp [][]uint16

	// Scratch buffers for zero heap allocation during per-state queries
	stamp          uint32
	visited        []uint32
	q              []int32
	matchOf        []int32
	gseen          []uint32
	gstamp         uint32
	reachMask      []uint64 // per box
	uBoxes         []int32
	eGoals         []int32
	isGoalBox      []bool // cell -> is currently occupied goal
	unpackedOthers []int
}

// NewDisplacementLandmarks precomputes 1-box push reachability and static
// components with single-pin walls for each goal in lv.
func NewDisplacementLandmarks(lv *Level, dirs [4]int32, goals []int32) *DisplacementLandmarks {
	numGoals := len(goals)
	if numGoals == 0 || numGoals > 64 {
		return nil
	}
	cells := len(lv.Wall)
	dl := &DisplacementLandmarks{
		lv:             lv,
		dirs:           dirs,
		cells:          cells,
		numGoals:       numGoals,
		goals:          goals,
		goalIdx:        make([]int32, cells),
		pinnedGoalMask: make([][]uint64, numGoals),
		pinComp:        make([][]uint16, numGoals),
		visited:        make([]uint32, cells),
		q:              make([]int32, cells),
		matchOf:        make([]int32, numGoals),
		gseen:          make([]uint32, numGoals),
		reachMask:      make([]uint64, len(lv.Boxes)),
		uBoxes:         make([]int32, 0, len(lv.Boxes)),
		eGoals:         make([]int32, 0, numGoals),
		isGoalBox:      make([]bool, cells),
		unpackedOthers: make([]int, 0, len(lv.Boxes)),
	}
	for i := range dl.goalIdx {
		dl.goalIdx[i] = -1
	}
	for gIdx, g := range goals {
		dl.goalIdx[g] = int32(gIdx)
	}

	// Precompute static player connected components for each pinned goal p
	for pIdx, p := range goals {
		dl.pinComp[pIdx] = make([]uint16, cells)
		cid := uint16(0)
		for c := int32(0); c < int32(cells); c++ {
			if lv.Wall[c] || c == p || dl.pinComp[pIdx][c] > 0 {
				continue
			}
			cid++
			dl.pinComp[pIdx][c] = cid
			q := dl.q[:0]
			q = append(q, c)
			for len(q) > 0 {
				curr := q[0]
				q = q[1:]
				for _, dir := range dirs {
					nxt := curr + dir
					if nxt < 0 || nxt >= int32(cells) || lv.Wall[nxt] || nxt == p || dl.pinComp[pIdx][nxt] > 0 {
						continue
					}
					dl.pinComp[pIdx][nxt] = cid
					q = append(q, nxt)
				}
			}
		}
	}

	// Precomputation scratch buffers
	canWalk := make([][4][4]bool, cells)
	walkComputed := make([]uint32, cells)
	walkVisited := make([]uint32, cells)
	walkQ := make([]int32, cells)
	var walkStamp uint32
	var computeStamp uint32

	bfsVisited := make([]uint32, 4*cells)
	bfsQ := make([]int32, 4*cells)
	var bfsStamp uint32

	for pIdx, p := range goals {
		dl.pinnedGoalMask[pIdx] = make([]uint64, 4*cells)
		computeStamp++
		dl.precomputeGoalPin(pIdx, p, canWalk, walkComputed, walkVisited, walkQ, computeStamp, &walkStamp, bfsVisited, bfsQ, &bfsStamp)
	}
	return dl
}

func (dl *DisplacementLandmarks) precomputeGoalPin(
	pIdx int, p int32,
	canWalk [][4][4]bool,
	walkComputed, walkVisited []uint32,
	walkQ []int32,
	computeStamp uint32,
	walkStamp *uint32,
	bfsVisited []uint32,
	bfsQ []int32,
	bfsStamp *uint32,
) {
	lv := dl.lv
	cells := dl.cells
	dirs := dl.dirs
	mask := dl.pinnedGoalMask[pIdx]

	isBlocked := func(c int32) bool {
		return c < 0 || int(c) >= cells || lv.Wall[c] || c == p
	}

	computeCanWalk := func(to int32) {
		if walkComputed[to] == computeStamp {
			return
		}
		walkComputed[to] = computeStamp
		for d1 := 0; d1 < 4; d1++ {
			for d2 := 0; d2 < 4; d2++ {
				canWalk[to][d1][d2] = false
			}
		}

		comp := [4]int{-1, -1, -1, -1}
		cid := 0
		for d := 0; d < 4; d++ {
			n := to - dirs[d]
			if isBlocked(n) || comp[d] >= 0 {
				continue
			}
			cid++
			comp[d] = cid
			*walkStamp++
			walkVisited[to] = *walkStamp
			walkVisited[n] = *walkStamp
			q := walkQ[:0]
			q = append(q, n)
			for len(q) > 0 {
				curr := q[0]
				q = q[1:]
				for _, dir := range dirs {
					nxt := curr + dir
					if isBlocked(nxt) || walkVisited[nxt] == *walkStamp {
						continue
					}
					walkVisited[nxt] = *walkStamp
					q = append(q, nxt)
				}
			}
			for d2 := d + 1; d2 < 4; d2++ {
				n2 := to - dirs[d2]
				if !isBlocked(n2) && walkVisited[n2] == *walkStamp {
					comp[d2] = cid
				}
			}
		}
		for d1 := 0; d1 < 4; d1++ {
			for d2 := 0; d2 < 4; d2++ {
				if comp[d1] > 0 && comp[d1] == comp[d2] {
					canWalk[to][d1][d2] = true
				}
			}
		}
	}

	// Multi-source backward propagation across all target goals in Goals \ {p}
	q := bfsQ[:0]
	for gIdx, g := range dl.goals {
		if gIdx == pIdx {
			continue
		}
		targetBit := uint64(1) << gIdx

		// Base states: any push (sq, d) that pushes the box directly onto goal g
		for d := 0; d < 4; d++ {
			sq := g - dirs[d]
			supp := sq - dirs[d]
			if isBlocked(sq) || isBlocked(supp) {
				continue
			}
			sid := sq*4 + int32(d)
			if (mask[sid] & targetBit) == 0 {
				mask[sid] |= targetBit
				q = append(q, sid)
			}
		}
	}

	// Propagate backward along valid transitions (sq, d) -> (to, d_next)
	for len(q) > 0 {
		curr := q[0]
		q = q[1:]
		to := curr / 4
		dNext := curr % 4
		toMask := mask[curr]

		computeCanWalk(to)

		for d := 0; d < 4; d++ {
			sq := to - dirs[d]
			supp := sq - dirs[d]
			if isBlocked(sq) || isBlocked(supp) {
				continue
			}
			if !canWalk[to][d][dNext] {
				continue
			}
			prevSid := sq*4 + int32(d)
			oldMask := mask[prevSid]
			newMask := oldMask | toMask
			if newMask != oldMask {
				mask[prevSid] = newMask
				q = append(q, prevSid)
			}
		}
	}
}

// ComputeSinglePinCMD evaluates Theorem 1 on state (boxes, player):
// For each box currently sitting on a goal p, verifies whether the other boxes
// can be injectively matched to the other goals when p is pinned forever.
// Returns a bitmask of goal indices that are Certified Mandatory Displacements (CMD).
func (dl *DisplacementLandmarks) ComputeSinglePinCMD(boxes []int32, player int32) uint64 {
	if dl == nil {
		return 0
	}
	numBoxes := len(boxes)
	if numBoxes <= 1 || dl.numGoals != numBoxes {
		return 0
	}

	var cmdMask uint64
	allGoals := (uint64(1) << dl.numGoals) - 1
	dirs := dl.dirs
	cells := int32(dl.cells)

	// Identify occupied goals and empty goals
	clear(dl.isGoalBox)
	dl.uBoxes = dl.uBoxes[:0]
	emptyGoalsMask := allGoals

	for _, b := range boxes {
		gIdx := dl.goalIdx[b]
		if gIdx >= 0 {
			dl.isGoalBox[b] = true
			emptyGoalsMask &= ^(uint64(1) << gIdx)
		} else {
			dl.uBoxes = append(dl.uBoxes, b)
		}
	}

	for _, p := range boxes {
		pIdx := dl.goalIdx[p]
		if pIdx < 0 {
			continue // box is not on a goal
		}

		pComp := dl.pinComp[pIdx][player]
		if pComp == 0 {
			// Player cannot move on Board \ (Walls U {p})
			continue
		}

		targetGoalsMask := allGoals & ^(uint64(1) << pIdx)
		pinnedMask := dl.pinnedGoalMask[pIdx]

		// FAST FILTER:
		// First check only the unpacked boxes U against emptyGoalsMask.
		// If U can match directly to emptyGoalsMask, all other boxes b' sitting on goals
		// can trivially stay on their goals, so a full matching DEFINITIVELY exists!
		if len(dl.uBoxes) > 0 {
			uCanDirectMatch := true
			uIdx := 0
			for _, u := range dl.uBoxes {
				reach := uint64(0)
				for d := 0; d < 4; d++ {
					supp := u - dirs[d]
					if supp >= 0 && supp < cells && dl.pinComp[pIdx][supp] == pComp {
						sid := u*4 + int32(d)
						reach |= pinnedMask[sid]
					}
				}
				reach &= emptyGoalsMask
				dl.reachMask[uIdx] = reach
				if reach == 0 {
					uCanDirectMatch = false
					break
				}
				uIdx++
			}
			if uCanDirectMatch && dl.matchUnpackedDirect(len(dl.uBoxes), emptyGoalsMask) {
				continue
			}
		}

		// Full check across all other boxes:
		// Pre-assign other occupied goals to themselves, only run augmenting paths
		// for the unpacked other boxes!
		for j := range dl.matchOf {
			dl.matchOf[j] = -1
		}
		dl.unpackedOthers = dl.unpackedOthers[:0]

		otherIdx := 0
		unionMask := uint64(0)
		anyZero := false

		for _, bPrime := range boxes {
			if bPrime == p {
				continue
			}
			reach := uint64(0)
			gPrimeIdx := dl.goalIdx[bPrime]
			if gPrimeIdx >= 0 && gPrimeIdx != pIdx {
				reach |= uint64(1) << gPrimeIdx
				dl.matchOf[gPrimeIdx] = int32(otherIdx)
			} else {
				dl.unpackedOthers = append(dl.unpackedOthers, otherIdx)
			}

			for d := 0; d < 4; d++ {
				supp := bPrime - dirs[d]
				if supp >= 0 && supp < cells && dl.pinComp[pIdx][supp] == pComp {
					sid := bPrime*4 + int32(d)
					reach |= pinnedMask[sid]
				}
			}
			reach &= targetGoalsMask
			dl.reachMask[otherIdx] = reach
			unionMask |= reach
			if reach == 0 {
				anyZero = true
				break
			}
			otherIdx++
		}

		if anyZero || (unionMask&targetGoalsMask) != targetGoalsMask {
			cmdMask |= uint64(1) << pIdx
			continue
		}

		// Run augmenting paths only for unpacked others (starting with occupied pre-matched)
		if !dl.matchUnpackedAugment(targetGoalsMask) {
			cmdMask |= uint64(1) << pIdx
		}
	}
	return cmdMask
}

func (dl *DisplacementLandmarks) matchUnpackedDirect(nUnpacked int, emptyGoalsMask uint64) bool {
	for j := range dl.matchOf {
		dl.matchOf[j] = -1
	}

	var try func(i int) bool
	try = func(i int) bool {
		mask := dl.reachMask[i] & emptyGoalsMask
		for mask > 0 {
			j := bits.TrailingZeros64(mask)
			mask &= mask - 1
			if dl.gseen[j] == dl.gstamp {
				continue
			}
			dl.gseen[j] = dl.gstamp
			if dl.matchOf[j] < 0 || try(int(dl.matchOf[j])) {
				dl.matchOf[j] = int32(i)
				return true
			}
		}
		return false
	}

	for i := 0; i < nUnpacked; i++ {
		dl.gstamp++
		if dl.gstamp == 0 {
			clear(dl.gseen)
			dl.gstamp = 1
		}
		if !try(i) {
			return false
		}
	}
	return true
}

func (dl *DisplacementLandmarks) matchUnpackedAugment(targetGoalsMask uint64) bool {
	var try func(i int) bool
	try = func(i int) bool {
		mask := dl.reachMask[i] & targetGoalsMask
		for mask > 0 {
			j := bits.TrailingZeros64(mask)
			mask &= mask - 1
			if dl.gseen[j] == dl.gstamp {
				continue
			}
			dl.gseen[j] = dl.gstamp
			if dl.matchOf[j] < 0 || try(int(dl.matchOf[j])) {
				dl.matchOf[j] = int32(i)
				return true
			}
		}
		return false
	}

	for _, i := range dl.unpackedOthers {
		dl.gstamp++
		if dl.gstamp == 0 {
			clear(dl.gseen)
			dl.gstamp = 1
		}
		if !try(i) {
			return false
		}
	}
	return true
}

// ComputeCollectiveUnpacking evaluates Theorem 2:
// Checks if unpacked boxes U = B \ Goals cannot reach empty goals G_empty = Goals \ B
// when all currently occupied goals B_goal = B \cap Goals act as permanent walls.
// If true, at least one box on an occupied goal MUST be unpacked in every valid solution.
func (dl *DisplacementLandmarks) ComputeCollectiveUnpacking(boxes []int32, player int32) (needsUnpacking bool, candidateUnpins uint64) {
	if dl == nil {
		return false, 0
	}
	lv := dl.lv
	cells := int32(dl.cells)
	dirs := dl.dirs

	// Identify B_goal, U = B \ Goals, G_empty = Goals \ B
	dl.uBoxes = dl.uBoxes[:0]
	dl.eGoals = dl.eGoals[:0]
	clear(dl.isGoalBox)

	nGoalBoxes := 0
	for _, b := range boxes {
		gIdx := dl.goalIdx[b]
		if gIdx >= 0 {
			dl.isGoalBox[b] = true
			nGoalBoxes++
		} else {
			dl.uBoxes = append(dl.uBoxes, b)
		}
	}

	// If no boxes are on goals, or all boxes are already on goals (solved), no collective unpacking
	if nGoalBoxes == 0 || len(dl.uBoxes) == 0 {
		return false, 0
	}

	for gIdx, g := range dl.goals {
		if !dl.isGoalBox[g] {
			dl.eGoals = append(dl.eGoals, int32(gIdx))
		}
	}
	k := len(dl.uBoxes)
	if len(dl.eGoals) != k {
		return false, 0
	}

	// Wall set: Walls U B_goal
	isWallAll := func(c int32) bool {
		return c < 0 || c >= cells || lv.Wall[c] || dl.isGoalBox[c]
	}

	// Fast pre-check: multi-source pull BFS from ALL empty goals simultaneously.
	// If any unpacked box cannot reach ANY empty goal, needsUnpacking is true immediately!
	dl.stamp++
	multiStamp := dl.stamp
	q := dl.q[:0]
	for _, gIdx := range dl.eGoals {
		g := dl.goals[gIdx]
		dl.visited[g] = multiStamp
		q = append(q, g)
	}

	for len(q) > 0 {
		c := q[0]
		q = q[1:]
		for _, d := range dirs {
			b := c - d
			supp := b - d
			if !isWallAll(b) && !isWallAll(supp) {
				if dl.visited[b] != multiStamp {
					dl.visited[b] = multiStamp
					q = append(q, b)
				}
			}
		}
	}

	for _, u := range dl.uBoxes {
		if dl.visited[u] != multiStamp {
			needsUnpacking = true
			break
		}
	}

	// If fast pre-check passed, run per-goal pull BFS for matching
	if !needsUnpacking {
		var canReach [64]uint64
		for eIdx, gIdx := range dl.eGoals {
			g := dl.goals[gIdx]
			dl.stamp++
			dl.visited[g] = dl.stamp
			q = dl.q[:0]
			q = append(q, g)

			for len(q) > 0 {
				c := q[0]
				q = q[1:]
				for _, d := range dirs {
					b := c - d
					supp := b - d
					if !isWallAll(b) && !isWallAll(supp) {
						if dl.visited[b] != dl.stamp {
							dl.visited[b] = dl.stamp
							q = append(q, b)
						}
					}
				}
			}

			for uIdx, u := range dl.uBoxes {
				if dl.visited[u] == dl.stamp {
					canReach[uIdx] |= uint64(1) << eIdx
				}
			}
		}

		unionMask := uint64(0)
		allEmptyMask := (uint64(1) << k) - 1
		for uIdx := 0; uIdx < k; uIdx++ {
			if canReach[uIdx] == 0 {
				needsUnpacking = true
				break
			}
			unionMask |= canReach[uIdx]
		}

		if !needsUnpacking && (unionMask&allEmptyMask) != allEmptyMask {
			needsUnpacking = true
		}

		if !needsUnpacking {
			for j := range dl.matchOf {
				dl.matchOf[j] = -1
			}
			var try func(i int) bool
			try = func(i int) bool {
				mask := canReach[i] & allEmptyMask
				for mask > 0 {
					j := bits.TrailingZeros64(mask)
					mask &= mask - 1
					if dl.gseen[j] == dl.gstamp {
						continue
					}
					dl.gseen[j] = dl.gstamp
					if dl.matchOf[j] < 0 || try(int(dl.matchOf[j])) {
						dl.matchOf[j] = int32(i)
						return true
					}
				}
				return false
			}

			matched := true
			for i := 0; i < k; i++ {
				dl.gstamp++
				if dl.gstamp == 0 {
					clear(dl.gseen)
					dl.gstamp = 1
				}
				if !try(i) {
					matched = false
					break
				}
			}
			if !matched {
				needsUnpacking = true
			}
		}
	}

	if needsUnpacking {
		// Identify pushable candidate boxes in B_goal
		for _, b := range boxes {
			if !dl.isGoalBox[b] {
				continue
			}
			gIdx := dl.goalIdx[b]
			// Check if player can push b: player at b - d, b + d is free
			for _, d := range dirs {
				supp := b - d
				to := b + d
				if !isWallAll(supp) && !isWallAll(to) {
					candidateUnpins |= uint64(1) << gIdx
					break
				}
			}
		}
	}

	return needsUnpacking, candidateUnpins
}
