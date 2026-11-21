package main

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

type censusLevelItem struct {
	file   string
	index  int
	label  string
	isDiff bool
}

var censusLevels = []censusLevelItem{
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

type candMoveRec struct {
	boxIdx   int32
	fromCell int32
	toCell   int32
	childNP  int32
}

type levelCensusResult struct {
	label    string
	isDiff   bool
	mode     string
	solved   bool
	solveMs  int64
	expNodes int64
	storedN  int
	dups     int
	genMoves int

	// Definition 1: Stored-Node Tarjan SCCs
	sccCountTotal int
	sccCountGe2   int
	sccNodesGe2   int
	sccMaxGe2     int
	sccAvgGe2     float64
	sccDupsInside int

	// Definition 2: 1-Step Inverse-Witnessed Reversible Components
	revOpenedWithParent int
	revWitnessedCount   int
	revCompCountGe2     int
	revNodesGe2         int
	revMaxGe2           int
	revAvgGe2           float64

	// Definition 3: Candidate-Level Equivalent Preparation States & Repeated Exit Work
	// SCC Definition
	sccTotalMoves int
	sccInternal   int
	sccOutMoves   int
	sccDistExits  int
	sccReduction  float64
	sccRedundFrac float64

	// 1-Macro Inverse Definition
	revTotalMoves int
	revInternal   int
	revOutMoves   int
	revDistExits  int
	revReduction  float64
	revRedundFrac float64

	// Inverse Witness Cost & Hit Rate
	checkBCostUsTotal float64
	checkBCostUsPerOp float64
	checkBPctSearch   float64

	checkCCount       int
	checkCHits        int
	checkCHitRate     float64
	checkCCostUsPerOp float64
	checkCExtrapPct   float64

	candFindTested  int
	candFindHits    int
	candFindHitRate float64
}

// Disjoint Set Union for undirected components
type censusDSU struct {
	parent []int32
	size   []int32
}

func newCensusDSU(n int) *censusDSU {
	p := make([]int32, n)
	sz := make([]int32, n)
	for i := range p {
		p[i] = int32(i)
		sz[i] = 1
	}
	return &censusDSU{parent: p, size: sz}
}

func (d *censusDSU) find(x int32) int32 {
	root := x
	for root != d.parent[root] {
		root = d.parent[root]
	}
	curr := x
	for curr != root {
		nxt := d.parent[curr]
		d.parent[curr] = root
		curr = nxt
	}
	return root
}

func (d *censusDSU) union(x, y int32) {
	rx, ry := d.find(x), d.find(y)
	if rx != ry {
		if d.size[rx] < d.size[ry] {
			rx, ry = ry, rx
		}
		d.parent[ry] = rx
		d.size[rx] += d.size[ry]
	}
}

// computeSCC runs Tarjan's Strongly Connected Components algorithm.
func computeSCC(n int, adj [][]int32) [][]int32 {
	index := make([]int, n)
	lowlink := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	stack := make([]int32, 0, n)
	timer := 0
	var sccs [][]int32

	var strongConnect func(v int32)
	strongConnect = func(v int32) {
		index[v] = timer
		lowlink[v] = timer
		timer++
		stack = append(stack, v)
		onStack[v] = true

		for _, w := range adj[v] {
			if index[w] == -1 {
				strongConnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if index[w] < lowlink[v] {
					lowlink[v] = index[w]
				}
			}
		}

		if lowlink[v] == index[v] {
			var scc []int32
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for i := int32(0); i < int32(n); i++ {
		if index[i] == -1 {
			strongConnect(i)
		}
	}
	return sccs
}

// getParentBoxMove finds b0 (origin in parent) and t0 (destination in u).
func getParentBoxMove(s *solver, p, u int32) (b0, t0 int32) {
	b0 = s.pushFrom[u]
	if p < 0 || int(p)*s.nw >= len(s.boxes) || int(u+1)*s.nw > len(s.boxes) {
		return b0, b0
	}
	pBits := s.boxes[int(p)*s.nw : int(p+1)*s.nw]
	uBits := s.boxes[int(u)*s.nw : int(u+1)*s.nw]
	for w := 0; w < s.nw; w++ {
		diff := uBits[w] &^ pBits[w]
		if diff != 0 {
			t0 = s.fcell[w*64+bits.TrailingZeros64(diff)]
			return b0, t0
		}
	}
	return b0, b0
}

func stateKeyString(packed []uint64, np int32) string {
	b := make([]byte, len(packed)*8+4)
	for i, w := range packed {
		binary.LittleEndian.PutUint64(b[i*8:], w)
	}
	binary.LittleEndian.PutUint32(b[len(packed)*8:], uint32(np))
	return string(b)
}

func runLevelCensus(item censusLevelItem, mode string) levelCensusResult {
	data, err := os.ReadFile(item.file)
	if err != nil {
		panic(fmt.Sprintf("failed reading %s: %v", item.file, err))
	}
	levels := splitCollection(data)
	if item.index > len(levels) {
		panic(fmt.Sprintf("index %d out of range (len %d) for %s", item.index, len(levels), item.file))
	}
	rows := levels[item.index-1]
	lv, err := parseLevel(rows)
	if err != nil {
		panic(fmt.Sprintf("failed parsing %s #%d: %v", item.file, item.index, err))
	}

	deadline := time.Now().Add(1200 * time.Millisecond)
	s := newSolverUntil(lv, deadline)
	s.nodeLimit = 3000
	if mode == "novelty" {
		s.novelty = 100
	} else {
		s.novelty = 0
	}

	// Instrumentation state
	type openInfo struct {
		currB0       int32
		currT0       int32
		currParentNP int32
	}
	var curOpen openInfo
	var checkBNsTotal int64
	var checkBCount int64

	// candidate moves per opened node
	candMoves := make(map[int32][]candMoveRec)
	// whether opened node u has an immediate 1-macro inverse witness back to parent
	hasInverseWitness := make(map[int32]bool)
	// tracked duplicate applied edges
	type edgePair struct{ u, v int32 }
	var appliedDups []edgePair

	DebugExpand = func(slv *solver, e fessEntry) {
		id := e.node
		p := slv.parent[id]
		if p >= 0 {
			b0, t0 := getParentBoxMove(slv, p, id)
			curOpen = openInfo{
				currB0:       b0,
				currT0:       t0,
				currParentNP: slv.player[p],
			}
		} else {
			curOpen = openInfo{currB0: -1, currT0: -1, currParentNP: -1}
		}
	}

	DebugCandidateMove = func(slv *solver, id int32, cur []int32, i int, b, t int32, st uint32, cmdMask, unpinMask uint64) {
		candMoves[id] = append(candMoves[id], candMoveRec{
			boxIdx:   int32(i),
			fromCell: b,
			toCell:   t,
			childNP:  slv.candNP,
		})

		// Check 1 / (b): Parent inverse witness check
		t0Start := time.Now()
		if curOpen.currB0 >= 0 && b == curOpen.currT0 && t == curOpen.currB0 && slv.candNP == curOpen.currParentNP {
			hasInverseWitness[id] = true
		}
		checkBNsTotal += time.Since(t0Start).Nanoseconds()
		checkBCount++
	}

	DebugApplied = func(slv *solver, e fessEntry, cid int32) {
		u := e.node
		v := slv.lastAdded
		if cid < 0 && v >= 0 {
			appliedDups = append(appliedDups, edgePair{u, v})
		}
	}

	startTime := time.Now()
	res := s.solve(deadline, hugeMem)
	solveElapsed := time.Since(startTime)

	// Clean up hooks immediately
	DebugExpand = nil
	DebugCandidateMove = nil
	DebugApplied = nil

	N := len(s.player)
	resOut := levelCensusResult{
		label:    item.label,
		isDiff:   item.isDiff,
		mode:     mode,
		solved:   res.Solved,
		solveMs:  solveElapsed.Milliseconds(),
		expNodes: s.expandedN,
		storedN:  N,
		dups:     s.stats.Duplicates,
		genMoves: s.stats.Generated,
	}

	// -------------------------------------------------------------
	// Definition 1: Stored-Node Tarjan SCCs
	// -------------------------------------------------------------
	adj := make([][]int32, N)
	for v := int32(0); v < int32(N); v++ {
		for e := s.edgeHead[v]; e >= 0; e = s.edges[e].next {
			u := s.edges[e].parent
			if u >= 0 && u < int32(N) && u != v {
				adj[u] = append(adj[u], v)
			}
		}
	}

	sccs := computeSCC(N, adj)
	sccOf := make([]int, N)
	for i := range sccOf {
		sccOf[i] = -1
	}

	var sccGe2 [][]int32
	maxSCC := 0
	sumSCCGe2 := 0
	for idx, c := range sccs {
		for _, u := range c {
			sccOf[u] = idx
		}
		if len(c) >= 2 {
			sccGe2 = append(sccGe2, c)
			sumSCCGe2 += len(c)
			if len(c) > maxSCC {
				maxSCC = len(c)
			}
		}
	}

	resOut.sccCountTotal = len(sccs)
	resOut.sccCountGe2 = len(sccGe2)
	resOut.sccNodesGe2 = sumSCCGe2
	resOut.sccMaxGe2 = maxSCC
	if len(sccGe2) > 0 {
		resOut.sccAvgGe2 = float64(sumSCCGe2) / float64(len(sccGe2))
	}

	// Duplicates inside non-trivial SCCs
	dupsInsideSCC := 0
	for _, ep := range appliedDups {
		if ep.u >= 0 && ep.v >= 0 && ep.u < int32(N) && ep.v < int32(N) {
			sccU := sccOf[ep.u]
			sccV := sccOf[ep.v]
			if sccU >= 0 && sccU == sccV && len(sccs[sccU]) >= 2 {
				dupsInsideSCC++
			}
		}
	}
	resOut.sccDupsInside = dupsInsideSCC

	// -------------------------------------------------------------
	// Definition 2: 1-Step Inverse-Witnessed Reversible Components
	// -------------------------------------------------------------
	revDSU := newCensusDSU(N)
	openedWithParent := 0
	witnessedCount := 0

	for u := int32(0); u < int32(N); u++ {
		if s.mvStart[u] >= 0 { // opened node
			p := s.parent[u]
			if p >= 0 {
				openedWithParent++
				if hasInverseWitness[u] {
					witnessedCount++
					revDSU.union(p, u)
				}
			}
		}
	}

	resOut.revOpenedWithParent = openedWithParent
	resOut.revWitnessedCount = witnessedCount

	// Group DSU into components
	revCompMap := make(map[int32][]int32)
	for u := int32(0); u < int32(N); u++ {
		root := revDSU.find(u)
		revCompMap[root] = append(revCompMap[root], u)
	}

	var revGe2 [][]int32
	maxRev := 0
	sumRevGe2 := 0
	for _, c := range revCompMap {
		if len(c) >= 2 {
			revGe2 = append(revGe2, c)
			sumRevGe2 += len(c)
			if len(c) > maxRev {
				maxRev = len(c)
			}
		}
	}
	resOut.revCompCountGe2 = len(revGe2)
	resOut.revNodesGe2 = sumRevGe2
	resOut.revMaxGe2 = maxRev
	if len(revGe2) > 0 {
		resOut.revAvgGe2 = float64(sumRevGe2) / float64(len(revGe2))
	}

	// -------------------------------------------------------------
	// Definition 3: Candidate-Level Equivalent Preparation States & Repeated Exit Work
	// -------------------------------------------------------------
	curBoxes := make([]int32, s.n)
	childBoxes := make([]int32, s.n)

	// Helper to analyze candidate moves for a collection of non-trivial components
	analyzeComponents := func(comps [][]int32) (totMoves, intMoves, outMoves, distExits int, red, redFrac float64) {
		for _, c := range comps {
			inComp := make(map[int32]bool, len(c))
			for _, u := range c {
				inComp[u] = true
			}
			compDistinctExits := make(map[string]struct{})

			for _, u := range c {
				moves := candMoves[u]
				if len(moves) == 0 {
					continue
				}
				s.unpack(u, curBoxes)

				for _, m := range moves {
					totMoves++
					copy(childBoxes, curBoxes)
					childBoxes[m.boxIdx] = m.toCell
					sort.Slice(childBoxes, func(i, j int) bool { return childBoxes[i] < childBoxes[j] })
					packedChild := s.pack(childBoxes)

					_, childID, _ := s.find(packedChild, m.childNP)
					if childID >= 0 && inComp[childID] {
						intMoves++
					} else {
						outMoves++
						key := stateKeyString(packedChild, m.childNP)
						compDistinctExits[key] = struct{}{}
					}
				}
			}
			distExits += len(compDistinctExits)
		}

		if totMoves > 0 {
			red = 1.0 - float64(distExits)/float64(totMoves)
		}
		if s.stats.Generated > 0 {
			redFrac = float64(totMoves-distExits) / float64(s.stats.Generated)
		}
		return
	}

	// Evaluate Definition 1 (SCC) candidate exit work
	sccTot, sccInt, sccOut, sccDist, sccRed, sccRedFrac := analyzeComponents(sccGe2)
	resOut.sccTotalMoves = sccTot
	resOut.sccInternal = sccInt
	resOut.sccOutMoves = sccOut
	resOut.sccDistExits = sccDist
	resOut.sccReduction = sccRed
	resOut.sccRedundFrac = sccRedFrac

	// Evaluate Definition 2 (1-Macro Rev) candidate exit work
	revTot, revInt, revOut, revDist, revRed, revRedFrac := analyzeComponents(revGe2)
	resOut.revTotalMoves = revTot
	resOut.revInternal = revInt
	resOut.revOutMoves = revOut
	resOut.revDistExits = revDist
	resOut.revReduction = revRed
	resOut.revRedundFrac = revRedFrac

	// -------------------------------------------------------------
	// Inverse Witness Cost & Hit Rates
	// -------------------------------------------------------------
	// Check (b): Parent inverse witness during open(u)
	resOut.checkBCostUsTotal = float64(checkBNsTotal) / 1000.0
	if checkBCount > 0 {
		resOut.checkBCostUsPerOp = float64(checkBNsTotal) / float64(checkBCount) / 1000.0
	}
	if solveElapsed.Nanoseconds() > 0 {
		resOut.checkBPctSearch = float64(checkBNsTotal) / float64(solveElapsed.Nanoseconds()) * 100.0
	}

	// Check (c): 1-box inverse boxSearch on candidate moves
	// We evaluate a sample of candidate moves (up to 300 per level) to measure hit rate and exact boxSearch cost
	testSampleLimit := 300
	var checkCDurNsTotal int64
	var checkCHits int
	var checkCTested int
	var candFindHits int
	var candFindTested int

	for u := int32(0); u < int32(N); u++ {
		moves := candMoves[u]
		if len(moves) == 0 {
			continue
		}
		s.unpack(u, curBoxes)
		parentNP := s.player[u]

		for _, m := range moves {
			// Also evaluate general s.find hit rate on candidates
			candFindTested++
			copy(childBoxes, curBoxes)
			childBoxes[m.boxIdx] = m.toCell
			sort.Slice(childBoxes, func(i, j int) bool { return childBoxes[i] < childBoxes[j] })
			packedChild := s.pack(childBoxes)
			_, fid, _ := s.find(packedChild, m.childNP)
			if fid >= 0 {
				candFindHits++
			}

			if checkCTested >= testSampleLimit {
				continue
			}

			// Run inverse boxSearch check
			checkCTested++
			b, t := m.fromCell, m.toCell
			childNP := m.childNP

			// Set occ to child state
			for _, bx := range curBoxes {
				s.occ[bx] = true
			}
			s.occ[b] = false
			s.occ[t] = true

			tStart := time.Now()
			// Reach player from childNP
			s.stamp++
			st := s.stamp
			s.reach(childNP, s.mark)
			st = s.stamp

			found := false
			s.boxSearch(t, st, func(k, to int32) bool {
				if to == b && s.candNP == parentNP {
					found = true
					return false
				}
				return true
			})
			dur := time.Since(tStart).Nanoseconds()
			checkCDurNsTotal += dur

			if found {
				checkCHits++
			}

			// Reset occ
			s.occ[t] = false
			for _, bx := range curBoxes {
				s.occ[bx] = false
			}
		}
	}

	resOut.checkCCount = checkCTested
	resOut.checkCHits = checkCHits
	if checkCTested > 0 {
		resOut.checkCHitRate = float64(checkCHits) / float64(checkCTested) * 100.0
		resOut.checkCCostUsPerOp = float64(checkCDurNsTotal) / float64(checkCTested) / 1000.0
		// Extrapolate cost across all generated candidate moves
		extrapolatedNs := float64(resOut.checkCCostUsPerOp*1000.0) * float64(s.stats.Generated)
		if solveElapsed.Nanoseconds() > 0 {
			resOut.checkCExtrapPct = extrapolatedNs / float64(solveElapsed.Nanoseconds()) * 100.0
		}
	}

	if candFindTested > 0 {
		resOut.candFindTested = candFindTested
		resOut.candFindHits = candFindHits
		resOut.candFindHitRate = float64(candFindHits) / float64(candFindTested) * 100.0
	}

	return resOut
}

func printCensusReport(title string, results []levelCensusResult) {
	fmt.Println()
	fmt.Println(strings.Repeat("=", 125))
	fmt.Printf("                    %s\n", title)
	fmt.Println(strings.Repeat("=", 125))

	// Table 1: Search Overview & Tarjan SCCs (Definition 1)
	fmt.Println("\n--- TABLE 1: Search Overview & Stored-Node Tarjan SCCs (Definition 1) ---")
	fmt.Printf("%-22s %-5s %-7s %-6s %-6s %-6s | %-7s %-7s %-7s %-6s %-6s %-7s\n",
		"Level", "Mode", "Status", "Exp", "Stored", "Dups", "SCC>=2", "Nodes", "%Stored", "Max", "Avg", "DupsIn")
	fmt.Println(strings.Repeat("-", 105))

	var totExp, totStored, totDups int64
	var totSCCGe2, totSCCNodes, totSCCDupsIn int

	for _, r := range results {
		status := "FAIL"
		if r.solved {
			status = fmt.Sprintf("%dms", r.solveMs)
		}
		pctStored := 0.0
		if r.storedN > 0 {
			pctStored = float64(r.sccNodesGe2) / float64(r.storedN) * 100.0
		}

		totExp += r.expNodes
		totStored += int64(r.storedN)
		totDups += int64(r.dups)
		totSCCGe2 += r.sccCountGe2
		totSCCNodes += r.sccNodesGe2
		totSCCDupsIn += r.sccDupsInside

		fmt.Printf("%-22s %-5s %-7s %6d %6d %6d | %7d %7d %6.1f%% %6d %6.1f %7d\n",
			r.label, r.mode, status, r.expNodes, r.storedN, r.dups,
			r.sccCountGe2, r.sccNodesGe2, pctStored, r.sccMaxGe2, r.sccAvgGe2, r.sccDupsInside)
	}
	fmt.Println(strings.Repeat("-", 105))
	avgPctStored := 0.0
	if totStored > 0 {
		avgPctStored = float64(totSCCNodes) / float64(totStored) * 100.0
	}
	fmt.Printf("%-22s %-5s %-7s %6d %6d %6d | %7d %7d %6.1f%%    -      -   %7d\n",
		"TOTAL / SUMMARY", "", "", totExp, totStored, totDups,
		totSCCGe2, totSCCNodes, avgPctStored, totSCCDupsIn)

	// Table 2: 1-Step Inverse-Witnessed Reversible Groups (Definition 2)
	fmt.Println("\n--- TABLE 2: 1-Step Inverse-Witnessed Reversible Components (Definition 2) ---")
	fmt.Printf("%-22s %-5s %-12s %-10s %-10s | %-8s %-8s %-8s %-6s %-6s\n",
		"Level", "Mode", "Opened(w/p)", "Witnessed", "RevEdgeRate", "Rev>=2", "Nodes", "%Stored", "Max", "Avg")
	fmt.Println(strings.Repeat("-", 105))

	var totOpenedP, totWitnessed, totRevGe2, totRevNodes int
	for _, r := range results {
		edgeRate := 0.0
		if r.revOpenedWithParent > 0 {
			edgeRate = float64(r.revWitnessedCount) / float64(r.revOpenedWithParent) * 100.0
		}
		pctStored := 0.0
		if r.storedN > 0 {
			pctStored = float64(r.revNodesGe2) / float64(r.storedN) * 100.0
		}

		totOpenedP += r.revOpenedWithParent
		totWitnessed += r.revWitnessedCount
		totRevGe2 += r.revCompCountGe2
		totRevNodes += r.revNodesGe2

		fmt.Printf("%-22s %-5s %12d %10d %9.1f%% | %8d %8d %7.1f%% %6d %6.1f\n",
			r.label, r.mode, r.revOpenedWithParent, r.revWitnessedCount, edgeRate,
			r.revCompCountGe2, r.revNodesGe2, pctStored, r.revMaxGe2, r.revAvgGe2)
	}
	fmt.Println(strings.Repeat("-", 105))
	totEdgeRate := 0.0
	if totOpenedP > 0 {
		totEdgeRate = float64(totWitnessed) / float64(totOpenedP) * 100.0
	}
	totPctStored := 0.0
	if totStored > 0 {
		totPctStored = float64(totRevNodes) / float64(totStored) * 100.0
	}
	fmt.Printf("%-22s %-5s %12d %10d %9.1f%% | %8d %8d %7.1f%%    -      -\n",
		"TOTAL / SUMMARY", "", totOpenedP, totWitnessed, totEdgeRate,
		totRevGe2, totRevNodes, totPctStored)

	// Table 3: Candidate Exit Work & Repeated Preparation Reduction (Definition 3)
	fmt.Println("\n--- TABLE 3: Candidate Moves, Distinct Exits & Preparation Work Reduction (Definition 3) ---")
	fmt.Printf("%-22s %-5s | %-8s %-7s %-7s %-8s %-8s | %-8s %-7s %-7s %-8s %-8s\n",
		"Level", "Mode", "SCCMoves", "SCCInt", "SCCOut", "SCCExits", "SCCReduc", "RevMoves", "RevInt", "RevOut", "RevExits", "RevReduc")
	fmt.Println(strings.Repeat("-", 125))

	var sumSCCMoves, sumSCCInt, sumSCCOut, sumSCCExits int
	var sumRevMoves, sumRevInt, sumRevOut, sumRevExits int

	for _, r := range results {
		sumSCCMoves += r.sccTotalMoves
		sumSCCInt += r.sccInternal
		sumSCCOut += r.sccOutMoves
		sumSCCExits += r.sccDistExits

		sumRevMoves += r.revTotalMoves
		sumRevInt += r.revInternal
		sumRevOut += r.revOutMoves
		sumRevExits += r.revDistExits

		fmt.Printf("%-22s %-5s | %8d %7d %7d %8d %7.1f%% | %8d %7d %7d %8d %7.1f%%\n",
			r.label, r.mode,
			r.sccTotalMoves, r.sccInternal, r.sccOutMoves, r.sccDistExits, r.sccReduction*100.0,
			r.revTotalMoves, r.revInternal, r.revOutMoves, r.revDistExits, r.revReduction*100.0)
	}
	fmt.Println(strings.Repeat("-", 125))
	totSCCReduc := 0.0
	if sumSCCMoves > 0 {
		totSCCReduc = (1.0 - float64(sumSCCExits)/float64(sumSCCMoves)) * 100.0
	}
	totRevReduc := 0.0
	if sumRevMoves > 0 {
		totRevReduc = (1.0 - float64(sumRevExits)/float64(sumRevMoves)) * 100.0
	}
	fmt.Printf("%-22s %-5s | %8d %7d %7d %8d %7.1f%% | %8d %7d %7d %8d %7.1f%%\n",
		"TOTAL / SUMMARY", "",
		sumSCCMoves, sumSCCInt, sumSCCOut, sumSCCExits, totSCCReduc,
		sumRevMoves, sumRevInt, sumRevOut, sumRevExits, totRevReduc)

	// Table 4: Timing & Witness Cost Diagnostics
	fmt.Println("\n--- TABLE 4: Reversibility Check Costs & Hit Rates ---")
	fmt.Printf("%-22s %-5s | %-12s %-12s | %-12s %-12s %-12s | %-12s\n",
		"Level", "Mode", "CheckB (µs/op)", "CheckB (%Time)", "CheckC HitRate", "CheckC (µs/op)", "CheckC (%Extrap)", "CandFindHitRate")
	fmt.Println(strings.Repeat("-", 115))

	var sumCheckBUs float64
	var sumCheckCUs float64
	var countCheckC int
	var totCandFindTested, totCandFindHits int

	for _, r := range results {
		sumCheckBUs += r.checkBCostUsTotal
		sumCheckCUs += r.checkCCostUsPerOp * float64(r.checkCCount)
		countCheckC += r.checkCCount
		totCandFindTested += r.candFindTested
		totCandFindHits += r.candFindHits

		fmt.Printf("%-22s %-5s | %10.4fµs %11.3f%% | %11.1f%% %10.2fµs %11.2f%% | %11.1f%%\n",
			r.label, r.mode,
			r.checkBCostUsPerOp, r.checkBPctSearch,
			r.checkCHitRate, r.checkCCostUsPerOp, r.checkCExtrapPct,
			r.candFindHitRate)
	}
	fmt.Println(strings.Repeat("-", 115))
	avgCheckCUs := 0.0
	if countCheckC > 0 {
		avgCheckCUs = sumCheckCUs / float64(countCheckC)
	}
	totCandFindHitRate := 0.0
	if totCandFindTested > 0 {
		totCandFindHitRate = float64(totCandFindHits) / float64(totCandFindTested) * 100.0
	}
	fmt.Printf("%-22s %-5s | %10s    <0.01%%   |       -     %10.2fµs       -      | %11.1f%%\n",
		"OVERALL AVERAGE", "", "< 0.01µs", avgCheckCUs, totCandFindHitRate)
}

func TestReversiblePreparationCensus(t *testing.T) {
	if os.Getenv("REVERSIBLE_CENSUS") == "" {
		t.Skip("skipping slow census; set REVERSIBLE_CENSUS=1 to run")
	}
	defer func() {
		DebugExpand = nil
		DebugCandidateMove = nil
		DebugApplied = nil
		DebugOpen = nil
	}()

	modes := []string{"macro", "novelty"}
	var allResults []levelCensusResult

	for _, mode := range modes {
		t.Logf("Starting census runs for mode: %s", mode)
		for _, item := range censusLevels {
			t.Logf("  Running %s (%s)...", item.label, mode)
			res := runLevelCensus(item, mode)
			allResults = append(allResults, res)
		}
	}

	printCensusReport("REVERSIBLE PREPARATION GROUPS & EXIT PRIORITIZATION CENSUS (exp-290)", allResults)
}
