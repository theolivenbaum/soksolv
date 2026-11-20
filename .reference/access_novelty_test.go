package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type diagLevelItem struct {
	file   string
	index  int
	label  string
	isDiff bool
}

var diagnosticLevels = []diagLevelItem{
	// 12 difficult levels
	{"levels/Large Test Suite/Grigr2001_100.xsb", 84, "Grigr2001_100 #84", true},
	{"levels/Large Test Suite/Holland_81.xsb", 76, "Holland_81 #76", true},
	{"levels/Large Test Suite/Sasquatch_III_50.xsb", 36, "Sasquatch_III_50 #36", true},
	{"levels/Large Test Suite/Sasquatch_IV_50.xsb", 28, "Sasquatch_IV_50 #28", true},
	{"levels/Large Test Suite/Sasquatch_V_50.xsb", 28, "Sasquatch_V_50 #28", true},
	{"levels/Large Test Suite/SokHard_163.xsb", 84, "SokHard_163 #84", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 156, "Sven_1623 #156", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 172, "Sven_1623 #172", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 196, "Sven_1623 #196", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 228, "Sven_1623 #228", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 292, "Sven_1623 #292", true},
	{"levels/Large Test Suite/Sven_1623.xsb", 828, "Sven_1623 #828", true},

	// 6 easy/solvable control levels
	{"levels/Large Test Suite/Microban_155.xsb", 12, "Microban_155 #12", false},
	{"levels/Large Test Suite/Microban II_135.xsb", 124, "Microban II_135 #124", false},
	{"levels/Large Test Suite/Grigr2001_100.xsb", 20, "Grigr2001_100 #20", false},
	{"levels/Large Test Suite/Holland_81.xsb", 4, "Holland_81 #4", false},
	{"levels/Large Test Suite/XSokoban_90.xsb", 8, "XSokoban_90 #8", false},
	{"levels/Large Test Suite/Sven_1623.xsb", 12, "Sven_1623 #12", false},
}

type expBucket struct {
	packed           int
	totalMoves       int
	occMoves         int
	occW1Moves       int
	occW2Moves       int
	accessMoves      int
	exclAccessMoves  int
	newPushSideMoves int
	factAMoves       int
	factBMoves       int
	factCMoves       int
}

type factAPairKey struct{ psi, extra uint32 }

type noveltyTracker struct {
	maxPacked      int
	lastPackedStep int64
	currentExp     int64
	currentPacked  int

	currentOpenId    int32
	nodeOccNovel     bool
	nodeAccessNovel  bool
	nodeExclAccess   bool
	nodesWithOcc     int64
	nodesWithAccess  int64
	nodesWithExcl    int64
	totalNodesOpened int64

	totalMoves int64
	expBuckets []expBucket

	seenOccW1          [][]uint64
	seenOccW2          map[uint64]struct{}
	seenFactA          [][]uint64
	seenFactAPair      map[uint64]struct{}
	seenFactBRoomPairs map[uint64]struct{}
	seenFactBRoomBox   map[uint64]struct{}
	seenFactBPassage   map[uint64]struct{}
	seenFactC          map[uint64]struct{}

	reachRoomsBuf     []int
	reachNbBuf        []int32
	newFactABuf       []factAPairKey
	newFactASingleBuf []uint32
	newRoomPairsBuf   []uint64
	newRoomBoxBuf     []uint64
	newPassageBuf     []uint64
	newFactCBuf       []uint64

	diagDurationNs  int64
	totalDurationNs int64
}

func newNoveltyTracker() *noveltyTracker {
	return &noveltyTracker{
		currentOpenId:      -1,
		seenOccW2:          make(map[uint64]struct{}),
		seenFactAPair:      make(map[uint64]struct{}),
		seenFactBRoomPairs: make(map[uint64]struct{}),
		seenFactBRoomBox:   make(map[uint64]struct{}),
		seenFactBPassage:   make(map[uint64]struct{}),
		seenFactC:          make(map[uint64]struct{}),
		expBuckets:         make([]expBucket, 0, 5100),
	}
}

func (tr *noveltyTracker) finishLastNode() {
	if tr.currentOpenId >= 0 {
		if tr.nodeOccNovel {
			tr.nodesWithOcc++
		}
		if tr.nodeAccessNovel {
			tr.nodesWithAccess++
		}
		if tr.nodeExclAccess {
			tr.nodesWithExcl++
		}
		tr.currentOpenId = -1
	}
}

func (tr *noveltyTracker) handleExpand(s *solver, e fessEntry) {
	exp := s.expandedN
	p := s.actual[e.node].packed
	if p > tr.maxPacked {
		tr.maxPacked = p
		tr.lastPackedStep = exp
	}
	tr.currentExp = exp
	tr.currentPacked = p
}

func (tr *noveltyTracker) handleCandidate(s *solver, id int32, cur []int32, i int, b, t int32, st uint32, cmdMask, unpinMask uint64) {
	t0 := time.Now()
	defer func() {
		tr.diagDurationNs += time.Since(t0).Nanoseconds()
	}()

	bk := int(s.bucket[id])
	exp := tr.currentExp

	for int64(len(tr.expBuckets)) <= exp {
		tr.expBuckets = append(tr.expBuckets, expBucket{})
	}
	bkt := &tr.expBuckets[exp]
	bkt.packed = tr.currentPacked
	bkt.totalMoves++
	tr.totalMoves++

	if id != tr.currentOpenId {
		if tr.currentOpenId >= 0 {
			if tr.nodeOccNovel {
				tr.nodesWithOcc++
			}
			if tr.nodeAccessNovel {
				tr.nodesWithAccess++
			}
			if tr.nodeExclAccess {
				tr.nodesWithExcl++
			}
		}
		tr.currentOpenId = id
		tr.totalNodesOpened++
		tr.nodeOccNovel = false
		tr.nodeAccessNovel = false
		tr.nodeExclAccess = false
	}

	// 1. Occupancy Novelty
	for len(tr.seenOccW1) <= bk {
		tr.seenOccW1 = append(tr.seenOccW1, nil)
	}
	if tr.seenOccW1[bk] == nil {
		tr.seenOccW1[bk] = make([]uint64, (len(s.lv.Wall)+63)/64)
	}
	w, bit := t/64, uint64(1)<<(t%64)
	w1Novel := (tr.seenOccW1[bk][w] & bit) == 0
	w2Novel := false
	for j, bpos := range cur {
		if j == i {
			continue
		}
		c1, c2 := t, bpos
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		k := (uint64(bk) << 40) | (uint64(c1) << 20) | uint64(c2)
		if _, seen := tr.seenOccW2[k]; !seen {
			w2Novel = true
		}
	}
	isOccNovel := w1Novel || w2Novel
	if w1Novel {
		bkt.occW1Moves++
	}
	if w2Novel {
		bkt.occW2Moves++
	}
	if isOccNovel {
		bkt.occMoves++
		tr.nodeOccNovel = true
	}

	// 2. Fact A: Push-Side Capability
	for len(tr.seenFactA) <= bk {
		tr.seenFactA = append(tr.seenFactA, nil)
	}
	if tr.seenFactA[bk] == nil {
		tr.seenFactA[bk] = make([]uint64, (4*len(s.lv.Wall)+63)/64)
	}
	var factANovel bool
	var openedNewPushSide bool
	newFactAPairs := tr.newFactABuf[:0]
	newFactASingle := tr.newFactASingleBuf[:0]

	for j, bpos := range cur {
		c := bpos
		if j == i {
			c = t
		}
		for dirIdx, d := range s.dirs {
			support := s.front(c, d)
			if s.mark3[support] != s.pstamp {
				continue
			}
			to := c + d
			if s.lv.Wall[to] || s.occ[to] || s.dist[to] < 0 {
				continue
			}
			psi := uint32(c*4 + int32(dirIdx))
			pw, pbit := psi/64, uint64(1)<<(psi%64)
			w1FactANew := (tr.seenFactA[bk][pw] & pbit) == 0

			kBox := (uint64(bk) << 40) | (uint64(psi) << 20) | uint64(t)
			_, w2BoxSeen := tr.seenFactAPair[kBox]
			w2BoxNew := !w2BoxSeen

			w2GoalNew := false
			var kGoal uint64
			if s.lv.Goal[b] {
				kGoal = (uint64(bk) << 40) | (uint64(psi) << 20) | uint64(b)
				_, w2GoalSeen := tr.seenFactAPair[kGoal]
				w2GoalNew = !w2GoalSeen
			}

			if w1FactANew || w2BoxNew || w2GoalNew {
				factANovel = true
				if s.mark[support] != st {
					openedNewPushSide = true
				}
				if w1FactANew {
					newFactASingle = append(newFactASingle, psi)
				}
				if w2BoxNew {
					newFactAPairs = append(newFactAPairs, factAPairKey{psi, uint32(t)})
				}
				if w2GoalNew {
					newFactAPairs = append(newFactAPairs, factAPairKey{psi, uint32(b)})
				}
			}
		}
	}
	tr.newFactABuf = newFactAPairs
	tr.newFactASingleBuf = newFactASingle

	// Fact B: Room / Passage Crossing Capability
	var factBNovel bool
	newRoomPairs := tr.newRoomPairsBuf[:0]
	newRoomBox := tr.newRoomBoxBuf[:0]
	newPassage := tr.newPassageBuf[:0]

	if s.rooms != nil && len(s.rooms.rooms) >= 2 {
		reachRooms := tr.reachRoomsBuf[:0]
		for r, cells := range s.rooms.rooms {
			for _, x := range cells {
				if s.mark3[x] == s.pstamp {
					reachRooms = append(reachRooms, r)
					break
				}
			}
		}
		tr.reachRoomsBuf = reachRooms
		for idx1 := 0; idx1 < len(reachRooms); idx1++ {
			r1 := reachRooms[idx1]
			kBox := (uint64(bk) << 40) | (uint64(r1) << 20) | uint64(t)
			if _, seen := tr.seenFactBRoomBox[kBox]; !seen {
				factBNovel = true
				newRoomBox = append(newRoomBox, kBox)
			}
			for idx2 := idx1 + 1; idx2 < len(reachRooms); idx2++ {
				r2 := reachRooms[idx2]
				kPair := (uint64(bk) << 40) | (uint64(r1) << 20) | uint64(r2)
				if _, seen := tr.seenFactBRoomPairs[kPair]; !seen {
					factBNovel = true
					newRoomPairs = append(newRoomPairs, kPair)
				}
			}
		}
	}
	// Passage crossing
	if s.rooms == nil || s.rooms.cell[b] < 0 {
		reachNb := tr.reachNbBuf[:0]
		for _, d := range s.dirs {
			nb := b + d
			if !s.lv.Wall[nb] && !s.occ[nb] && s.mark3[nb] == s.pstamp {
				reachNb = append(reachNb, nb)
			}
		}
		tr.reachNbBuf = reachNb
		for idx1 := 0; idx1 < len(reachNb); idx1++ {
			n1 := reachNb[idx1]
			for idx2 := idx1 + 1; idx2 < len(reachNb); idx2++ {
				n2 := reachNb[idx2]
				if s.mark[n1] != st || s.mark[n2] != st {
					c1, c2 := n1, n2
					if c1 > c2 {
						c1, c2 = c2, c1
					}
					kPass := (uint64(bk) << 40) | (uint64(c1) << 20) | uint64(c2)
					if _, seen := tr.seenFactBPassage[kPass]; !seen {
						factBNovel = true
						newPassage = append(newPassage, kPass)
					}
				}
			}
		}
	}
	tr.newRoomPairsBuf = newRoomPairs
	tr.newRoomBoxBuf = newRoomBox
	tr.newPassageBuf = newPassage

	// Fact C: Displacement-Opened Access
	var factCNovel bool
	newFactCKeys := tr.newFactCBuf[:0]
	if (cmdMask|unpinMask) != 0 || (s.lv.Goal[b] && !s.lv.Goal[t]) {
		for j, bpos := range cur {
			c := bpos
			if j == i {
				c = t
			}
			for dirIdx, d := range s.dirs {
				support := s.front(c, d)
				if s.mark3[support] == s.pstamp && s.mark[support] != st {
					psi := uint32(c*4 + int32(dirIdx))
					kC := (uint64(bk) << 40) | (uint64(b) << 20) | uint64(psi)
					if _, seen := tr.seenFactC[kC]; !seen {
						factCNovel = true
						newFactCKeys = append(newFactCKeys, kC)
					}
				}
			}
		}
		if s.mark[s.candNP] != st {
			kC := (uint64(bk) << 40) | (uint64(b) << 20) | uint64(s.candNP)
			if _, seen := tr.seenFactC[kC]; !seen {
				factCNovel = true
				newFactCKeys = append(newFactCKeys, kC)
			}
		}
	}
	tr.newFactCBuf = newFactCKeys

	isAccessNovel := factANovel || factBNovel || factCNovel
	isExclAccessNovel := isAccessNovel && !isOccNovel

	if factANovel {
		bkt.factAMoves++
	}
	if factBNovel {
		bkt.factBMoves++
	}
	if factCNovel {
		bkt.factCMoves++
	}
	if isAccessNovel {
		bkt.accessMoves++
		tr.nodeAccessNovel = true
	}
	if isExclAccessNovel {
		bkt.exclAccessMoves++
		tr.nodeExclAccess = true
	}
	if openedNewPushSide {
		bkt.newPushSideMoves++
	}

	// Update seen sets
	if isOccNovel {
		tr.seenOccW1[bk][w] |= bit
		for j, bpos := range cur {
			if j == i {
				continue
			}
			c1, c2 := t, bpos
			if c1 > c2 {
				c1, c2 = c2, c1
			}
			tr.seenOccW2[(uint64(bk)<<40)|(uint64(c1)<<20)|uint64(c2)] = struct{}{}
		}
	}
	if factANovel {
		for _, psi := range newFactASingle {
			tr.seenFactA[bk][psi/64] |= 1 << (psi % 64)
		}
		for _, pk := range newFactAPairs {
			tr.seenFactAPair[(uint64(bk)<<40)|(uint64(pk.psi)<<20)|uint64(pk.extra)] = struct{}{}
		}
	}
	if factBNovel {
		for _, k := range newRoomPairs {
			tr.seenFactBRoomPairs[k] = struct{}{}
		}
		for _, k := range newRoomBox {
			tr.seenFactBRoomBox[k] = struct{}{}
		}
		for _, k := range newPassage {
			tr.seenFactBPassage[k] = struct{}{}
		}
	}
	if factCNovel {
		for _, k := range newFactCKeys {
			tr.seenFactC[k] = struct{}{}
		}
	}
}

type levelResult struct {
	item           diagLevelItem
	solved         bool
	reason         string
	solveMs        int64
	totalExp       int64
	maxPacked      int
	lastPackedStep int64
	totalMoves     int64

	totalOcc    int64
	totalOccW1  int64
	totalOccW2  int64
	totalAccess int64
	totalFactA  int64
	totalFactB  int64
	totalFactC  int64
	totalExcl   int64

	nodesOpened int64
	nodesOcc    int64
	nodesAccess int64
	nodesExcl   int64

	h2Moves  int64
	h2Occ    int64
	h2Access int64
	h2Excl   int64

	platMoves   int64
	platOcc     int64
	platAccess  int64
	platExcl    int64
	platNewPush int64

	baseWallMs  int64
	diagWallMs  int64
	checkNs     int64
	overheadPct float64
}

func TestAccessNoveltyDiagnostic(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: skipped in fast tier (go test -short); see research/exp-371")
	}
	defer func() {
		DebugCandidateMove = nil
		DebugExpand = nil
	}()

	results := make([]levelResult, 0, len(diagnosticLevels))

	for _, item := range diagnosticLevels {
		data, err := os.ReadFile(item.file)
		if err != nil {
			t.Fatalf("failed reading %s: %v", item.file, err)
		}
		levels := splitCollection(data)
		if item.index > len(levels) {
			t.Fatalf("index %d out of range (len %d) for %s", item.index, len(levels), item.file)
		}
		rows := levels[item.index-1]
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatalf("failed parsing %s #%d: %v", item.file, item.index, err)
		}

		// 1. Run baseline without diagnostic hook to measure true runtime
		DebugCandidateMove = nil
		DebugExpand = nil
		baseSolver := newSolverUntil(lv, time.Now().Add(1500*time.Millisecond))
		baseSolver.novelty = 100
		baseSolver.nodeLimit = 5000
		baseStart := time.Now()
		_ = baseSolver.solve(time.Now().Add(1500*time.Millisecond), hugeMem)
		baseElapsed := time.Since(baseStart)

		// 2. Run instrumented with novelty tracker
		tracker := newNoveltyTracker()
		s := newSolverUntil(lv, time.Now().Add(1500*time.Millisecond))
		s.novelty = 100
		s.nodeLimit = 5000

		DebugExpand = tracker.handleExpand
		DebugCandidateMove = tracker.handleCandidate

		diagStart := time.Now()
		res := s.solve(time.Now().Add(1500*time.Millisecond), hugeMem)
		diagElapsed := time.Since(diagStart)
		tracker.totalDurationNs = diagElapsed.Nanoseconds()
		tracker.finishLastNode()

		totalExp := s.expandedN
		lr := levelResult{
			item:           item,
			solved:         res.Solved,
			reason:         res.Reason,
			solveMs:        diagElapsed.Milliseconds(),
			totalExp:       totalExp,
			maxPacked:      tracker.maxPacked,
			lastPackedStep: tracker.lastPackedStep,
			totalMoves:     tracker.totalMoves,
			nodesOpened:    tracker.totalNodesOpened,
			nodesOcc:       tracker.nodesWithOcc,
			nodesAccess:    tracker.nodesWithAccess,
			nodesExcl:      tracker.nodesWithExcl,
			baseWallMs:     baseElapsed.Milliseconds(),
			diagWallMs:     diagElapsed.Milliseconds(),
			checkNs:        tracker.diagDurationNs,
		}

		if baseElapsed > 0 {
			lr.overheadPct = float64(diagElapsed-baseElapsed) / float64(baseElapsed) * 100.0
		} else if tracker.totalDurationNs > tracker.diagDurationNs {
			lr.overheadPct = float64(tracker.diagDurationNs) / float64(tracker.totalDurationNs-tracker.diagDurationNs) * 100.0
		}

		midExp := totalExp / 2
		for exp := int64(0); exp < int64(len(tracker.expBuckets)); exp++ {
			b := tracker.expBuckets[exp]
			lr.totalOcc += int64(b.occMoves)
			lr.totalOccW1 += int64(b.occW1Moves)
			lr.totalOccW2 += int64(b.occW2Moves)
			lr.totalAccess += int64(b.accessMoves)
			lr.totalFactA += int64(b.factAMoves)
			lr.totalFactB += int64(b.factBMoves)
			lr.totalFactC += int64(b.factCMoves)
			lr.totalExcl += int64(b.exclAccessMoves)

			if exp >= midExp {
				lr.h2Moves += int64(b.totalMoves)
				lr.h2Occ += int64(b.occMoves)
				lr.h2Access += int64(b.accessMoves)
				lr.h2Excl += int64(b.exclAccessMoves)
			}

			if exp > tracker.lastPackedStep {
				lr.platMoves += int64(b.totalMoves)
				lr.platOcc += int64(b.occMoves)
				lr.platAccess += int64(b.accessMoves)
				lr.platExcl += int64(b.exclAccessMoves)
				lr.platNewPush += int64(b.newPushSideMoves)
			}
		}

		results = append(results, lr)
	}

	// Print full tables and diagnostic reports
	fmt.Println()
	fmt.Println("=========================================================================================================================")
	fmt.Println("                                ACCESS-AWARE NOVELTY DIAGNOSTIC (exp-289)")
	fmt.Println("=========================================================================================================================")
	fmt.Printf("%-24s | %-6s | %5s | %5s | %4s | %6s | %5s | %6s | %5s | %5s | %5s | %5s | %5s\n",
		"Level", "Solved", "Nodes", "Plat@", "MaxP", "Moves", "Occ", "Access", "Excl", "FactA", "FactB", "FactC", "Overhd")
	fmt.Println(strings.Repeat("-", 121))

	var diffTot, ctrlTot levelResult
	var diffCount, ctrlCount int

	accum := func(tot *levelResult, r levelResult) {
		tot.totalExp += r.totalExp
		tot.totalMoves += r.totalMoves
		tot.totalOcc += r.totalOcc
		tot.totalOccW1 += r.totalOccW1
		tot.totalOccW2 += r.totalOccW2
		tot.totalAccess += r.totalAccess
		tot.totalFactA += r.totalFactA
		tot.totalFactB += r.totalFactB
		tot.totalFactC += r.totalFactC
		tot.totalExcl += r.totalExcl
		tot.nodesOpened += r.nodesOpened
		tot.nodesOcc += r.nodesOcc
		tot.nodesAccess += r.nodesAccess
		tot.nodesExcl += r.nodesExcl
		tot.h2Moves += r.h2Moves
		tot.h2Occ += r.h2Occ
		tot.h2Access += r.h2Access
		tot.h2Excl += r.h2Excl
		tot.platMoves += r.platMoves
		tot.platOcc += r.platOcc
		tot.platAccess += r.platAccess
		tot.platExcl += r.platExcl
		tot.platNewPush += r.platNewPush
		tot.baseWallMs += r.baseWallMs
		tot.diagWallMs += r.diagWallMs
		tot.checkNs += r.checkNs
		if r.solved {
			tot.solveMs++ // use solveMs to count solved
		}
	}

	for _, r := range results {
		status := "FAIL"
		if r.solved {
			status = fmt.Sprintf("%dms", r.solveMs)
		}
		fmt.Printf("%-24s | %-6s | %5d | %5d | %4d | %6d | %5d | %6d | %5d | %5d | %5d | %5d | %+5.1f%%\n",
			r.item.label, status, r.totalExp, r.lastPackedStep, r.maxPacked, r.totalMoves,
			r.totalOcc, r.totalAccess, r.totalExcl, r.totalFactA, r.totalFactB, r.totalFactC, r.overheadPct)

		if r.item.isDiff {
			accum(&diffTot, r)
			diffCount++
		} else {
			accum(&ctrlTot, r)
			ctrlCount++
		}
	}

	fmt.Println(strings.Repeat("-", 121))
	fmt.Printf("%-24s | %2d/%2d  | %5d | %5s | %4s | %6d | %5d | %6d | %5d | %5d | %5d | %5d | %+5.1f%%\n",
		"Diff Sum (12 levels)", int(diffTot.solveMs), diffCount, diffTot.totalExp, "-", "-", diffTot.totalMoves,
		diffTot.totalOcc, diffTot.totalAccess, diffTot.totalExcl, diffTot.totalFactA, diffTot.totalFactB, diffTot.totalFactC,
		float64(diffTot.diagWallMs-diffTot.baseWallMs)/float64(diffTot.baseWallMs)*100.0)
	fmt.Printf("%-24s | %2d/%2d  | %5d | %5s | %4s | %6d | %5d | %6d | %5d | %5d | %5d | %5d | %+5.1f%%\n",
		"Ctrl Sum (6 levels)", int(ctrlTot.solveMs), ctrlCount, ctrlTot.totalExp, "-", "-", ctrlTot.totalMoves,
		ctrlTot.totalOcc, ctrlTot.totalAccess, ctrlTot.totalExcl, ctrlTot.totalFactA, ctrlTot.totalFactB, ctrlTot.totalFactC,
		float64(ctrlTot.diagWallMs-ctrlTot.baseWallMs)/float64(ctrlTot.baseWallMs)*100.0)

	var allTot levelResult
	accum(&allTot, diffTot)
	accum(&allTot, ctrlTot)
	allTot.solveMs = diffTot.solveMs + ctrlTot.solveMs
	fmt.Printf("%-24s | %2d/%2d  | %5d | %5s | %4s | %6d | %5d | %6d | %5d | %5d | %5d | %5d | %+5.1f%%\n",
		"Total (18 levels)", int(allTot.solveMs), 18, allTot.totalExp, "-", "-", allTot.totalMoves,
		allTot.totalOcc, allTot.totalAccess, allTot.totalExcl, allTot.totalFactA, allTot.totalFactB, allTot.totalFactC,
		float64(allTot.diagWallMs-allTot.baseWallMs)/float64(allTot.baseWallMs)*100.0)
	fmt.Println()

	// Trajectory breakdown table: 2nd Half and Plateau Breakdown
	fmt.Println("=========================================================================================================================")
	fmt.Println("                         TRAJECTORY BREAKDOWN: SECOND HALF [N/2, N] AND PLATEAU (step > lastPacked)")
	fmt.Println("=========================================================================================================================")
	fmt.Printf("%-24s | %18s | %32s\n", "Level", "2nd Half Expansions", "Plateau Regime (expanded > lastPacked)")
	fmt.Printf("%-24s | %5s | %5s | %5s | %6s | %5s | %6s | %5s | %10s\n",
		"", "Moves", "Occ", "Excl", "Moves", "Occ", "Access", "Excl", "NewSup/Acc")
	fmt.Println(strings.Repeat("-", 100))

	for _, r := range results {
		newSupFrac := "N/A"
		if r.platAccess > 0 {
			newSupFrac = fmt.Sprintf("%.1f%%", float64(r.platNewPush)/float64(r.platAccess)*100.0)
		}
		fmt.Printf("%-24s | %5d | %5d | %5d | %6d | %5d | %6d | %5d | %10s\n",
			r.item.label, r.h2Moves, r.h2Occ, r.h2Excl, r.platMoves, r.platOcc, r.platAccess, r.platExcl, newSupFrac)
	}
	fmt.Println(strings.Repeat("-", 100))

	diffPlatFrac := "N/A"
	if diffTot.platAccess > 0 {
		diffPlatFrac = fmt.Sprintf("%.1f%%", float64(diffTot.platNewPush)/float64(diffTot.platAccess)*100.0)
	}
	fmt.Printf("%-24s | %5d | %5d | %5d | %6d | %5d | %6d | %5d | %10s\n",
		"Diff Sum (12 levels)", diffTot.h2Moves, diffTot.h2Occ, diffTot.h2Excl,
		diffTot.platMoves, diffTot.platOcc, diffTot.platAccess, diffTot.platExcl, diffPlatFrac)

	ctrlPlatFrac := "N/A"
	if ctrlTot.platAccess > 0 {
		ctrlPlatFrac = fmt.Sprintf("%.1f%%", float64(ctrlTot.platNewPush)/float64(ctrlTot.platAccess)*100.0)
	}
	fmt.Printf("%-24s | %5d | %5d | %5d | %6d | %5d | %6d | %5d | %10s\n",
		"Ctrl Sum (6 levels)", ctrlTot.h2Moves, ctrlTot.h2Occ, ctrlTot.h2Excl,
		ctrlTot.platMoves, ctrlTot.platOcc, ctrlTot.platAccess, ctrlTot.platExcl, ctrlPlatFrac)

	allPlatFrac := "N/A"
	if allTot.platAccess > 0 {
		allPlatFrac = fmt.Sprintf("%.1f%%", float64(allTot.platNewPush)/float64(allTot.platAccess)*100.0)
	}
	fmt.Printf("%-24s | %5d | %5d | %5d | %6d | %5d | %6d | %5d | %10s\n",
		"Total (18 levels)", allTot.h2Moves, allTot.h2Occ, allTot.h2Excl,
		allTot.platMoves, allTot.platOcc, allTot.platAccess, allTot.platExcl, allPlatFrac)
	fmt.Println()

	// High-level answers to Task 2 diagnostic questions
	fmt.Println("=========================================================================================================================")
	fmt.Println("                                    DIAGNOSTIC SUMMARY & KEY FINDINGS")
	fmt.Println("=========================================================================================================================")
	fmt.Printf("1. Total Occupancy-Novel vs Access-Novel Moves:\n")
	fmt.Printf("   - Difficult: Total Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%)\n",
		diffTot.totalMoves, diffTot.totalOcc, float64(diffTot.totalOcc)/float64(diffTot.totalMoves)*100,
		diffTot.totalAccess, float64(diffTot.totalAccess)/float64(diffTot.totalMoves)*100)
	fmt.Printf("   - Control:   Total Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%)\n",
		ctrlTot.totalMoves, ctrlTot.totalOcc, float64(ctrlTot.totalOcc)/float64(ctrlTot.totalMoves)*100,
		ctrlTot.totalAccess, float64(ctrlTot.totalAccess)/float64(ctrlTot.totalMoves)*100)
	fmt.Printf("   - Overall:   Total Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%)\n\n",
		allTot.totalMoves, allTot.totalOcc, float64(allTot.totalOcc)/float64(allTot.totalMoves)*100,
		allTot.totalAccess, float64(allTot.totalAccess)/float64(allTot.totalMoves)*100)

	fmt.Printf("2. Exclusive Access-Novel Moves (Access-Novel BUT NOT Occ-Novel):\n")
	fmt.Printf("   - Difficult: Exclusive Access-Novel Moves = %d (%.1f%% of all moves, %.1f%% of Access-Novel)\n",
		diffTot.totalExcl, float64(diffTot.totalExcl)/float64(diffTot.totalMoves)*100,
		float64(diffTot.totalExcl)/float64(diffTot.totalAccess)*100)
	fmt.Printf("   - Control:   Exclusive Access-Novel Moves = %d (%.1f%% of all moves, %.1f%% of Access-Novel)\n",
		ctrlTot.totalExcl, float64(ctrlTot.totalExcl)/float64(ctrlTot.totalMoves)*100,
		float64(ctrlTot.totalExcl)/float64(ctrlTot.totalAccess)*100)
	fmt.Printf("   - Overall:   Exclusive Access-Novel Moves = %d (%.1f%% of all moves, %.1f%% of Access-Novel)\n\n",
		allTot.totalExcl, float64(allTot.totalExcl)/float64(allTot.totalMoves)*100,
		float64(allTot.totalExcl)/float64(allTot.totalAccess)*100)

	fmt.Printf("3. Plateau Continuation (expansions after lastPackedStep):\n")
	fmt.Printf("   - Difficult: Plateau Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%), Excl-Access=%d (%.1f%%)\n",
		diffTot.platMoves, diffTot.platOcc, float64(diffTot.platOcc)/float64(diffTot.platMoves)*100,
		diffTot.platAccess, float64(diffTot.platAccess)/float64(diffTot.platMoves)*100,
		diffTot.platExcl, float64(diffTot.platExcl)/float64(diffTot.platMoves)*100)
	fmt.Printf("     Newly reachable push sides (mark[support] != st) on plateau: %d / %d Access-Novel (%s)\n",
		diffTot.platNewPush, diffTot.platAccess, diffPlatFrac)
	fmt.Printf("   - Control:   Plateau Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%), Excl-Access=%d (%.1f%%)\n",
		ctrlTot.platMoves, ctrlTot.platOcc, float64(ctrlTot.platOcc)/float64(ctrlTot.platMoves)*100,
		ctrlTot.platAccess, float64(ctrlTot.platAccess)/float64(ctrlTot.platMoves)*100,
		ctrlTot.platExcl, float64(ctrlTot.platExcl)/float64(ctrlTot.platMoves)*100)
	fmt.Printf("     Newly reachable push sides (mark[support] != st) on plateau: %d / %d Access-Novel (%s)\n",
		ctrlTot.platNewPush, ctrlTot.platAccess, ctrlPlatFrac)
	fmt.Printf("   - Overall:   Plateau Moves=%d, Occ-Novel=%d (%.1f%%), Access-Novel=%d (%.1f%%), Excl-Access=%d (%.1f%%)\n",
		allTot.platMoves, allTot.platOcc, float64(allTot.platOcc)/float64(allTot.platMoves)*100,
		allTot.platAccess, float64(allTot.platAccess)/float64(allTot.platMoves)*100,
		allTot.platExcl, float64(allTot.platExcl)/float64(allTot.platMoves)*100)
	fmt.Printf("     Newly reachable push sides (mark[support] != st) on plateau: %d / %d Access-Novel (%s)\n\n",
		allTot.platNewPush, allTot.platAccess, allPlatFrac)

	fmt.Printf("4. Runtime Overhead:\n")
	diffOh := float64(diffTot.diagWallMs-diffTot.baseWallMs) / float64(diffTot.baseWallMs) * 100.0
	ctrlOh := float64(ctrlTot.diagWallMs-ctrlTot.baseWallMs) / float64(ctrlTot.baseWallMs) * 100.0
	allOh := float64(allTot.diagWallMs-allTot.baseWallMs) / float64(allTot.baseWallMs) * 100.0
	fmt.Printf("   - Difficult: Base=%dms, Diag=%dms, Overhead=%+.1f%% (internal check time=%dms)\n",
		diffTot.baseWallMs, diffTot.diagWallMs, diffOh, diffTot.checkNs/1e6)
	fmt.Printf("   - Control:   Base=%dms, Diag=%dms, Overhead=%+.1f%% (internal check time=%dms)\n",
		ctrlTot.baseWallMs, ctrlTot.diagWallMs, ctrlOh, ctrlTot.checkNs/1e6)
	fmt.Printf("   - Overall:   Base=%dms, Diag=%dms, Overhead=%+.1f%% (internal check time=%dms)\n",
		allTot.baseWallMs, allTot.diagWallMs, allOh, allTot.checkNs/1e6)
	fmt.Println("=========================================================================================================================")
}

func TestAccessNoveltyOverhead(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: skipped in fast tier (go test -short); see research/exp-371")
	}
	defer func() {
		AccessNovelty = true
		DebugCandidateMove = nil
		DebugExpand = nil
	}()
	defer func() {
		AccessNovelty = false
		DebugCandidateMove = nil
		DebugExpand = nil
	}()

	type modeConfig struct {
		name string
		acc  bool
	}

	modes := []modeConfig{
		{"Baseline", false},
		{"AccessNovelty", true},
	}

	type runSummary struct {
		cfg       modeConfig
		solved    int
		nodes     int64
		wallMs    int64
		perLevel  []int64
		solLevels []string
	}

	summaries := make([]runSummary, len(modes))

	for mIdx, m := range modes {
		AccessNovelty = m.acc
		DebugCandidateMove = nil
		DebugExpand = nil

		sum := runSummary{cfg: m, perLevel: make([]int64, len(diagnosticLevels))}
		start := time.Now()

		for lIdx, item := range diagnosticLevels {
			data, err := os.ReadFile(item.file)
			if err != nil {
				t.Fatalf("failed reading %s: %v", item.file, err)
			}
			rows := splitCollection(data)[item.index-1]
			lv, err := parseLevel(rows)
			if err != nil {
				t.Fatalf("failed parsing %s: %v", item.label, err)
			}

			s := newSolverUntil(lv, time.Now().Add(1500*time.Millisecond))
			s.novelty = 100
			s.nodeLimit = 5000

			t0 := time.Now()
			res := s.solve(time.Now().Add(1500*time.Millisecond), hugeMem)
			el := time.Since(t0).Milliseconds()

			sum.nodes += s.expandedN
			sum.perLevel[lIdx] = el
			if res.Solved {
				sum.solved++
				sum.solLevels = append(sum.solLevels, fmt.Sprintf("%s (%dms)", item.label, el))
			}
		}

		sum.wallMs = time.Since(start).Milliseconds()
		summaries[mIdx] = sum
	}

	fmt.Println()
	fmt.Println("=========================================================================================================================")
	fmt.Println("                        STANDALONE NOVELTY WORKER COMPARISON (18 Diagnostic Levels)")
	fmt.Println("=========================================================================================================================")
	fmt.Printf("%-28s | %6s | %10s | %10s | %8s | %s\n",
		"Configuration", "Solved", "Expansions", "Wall Time", "Overhead", "Solved Levels")
	fmt.Println(strings.Repeat("-", 105))

	baseMs := summaries[0].wallMs
	for _, sum := range summaries {
		ohStr := "-"
		if sum.cfg != modes[0] && baseMs > 0 {
			oh := float64(sum.wallMs-baseMs) / float64(baseMs) * 100.0
			ohStr = fmt.Sprintf("%+.1f%%", oh)
		}
		fmt.Printf("%-28s |  %2d/18 | %10d | %8dms | %8s | %s\n",
			sum.cfg.name, sum.solved, sum.nodes, sum.wallMs, ohStr, strings.Join(sum.solLevels, ", "))
	}
	fmt.Println("=========================================================================================================================")
}
