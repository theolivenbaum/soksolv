package main

// fessScore follows the draft's lexicographic order. Connectivity and room
// connectivity are distinct. Hotspots and mobility break ties but do not
// create feature cells.
type fessScore struct{ oop, packed, conn, room, hot, mobility int }

func (a fessScore) compare(b fessScore) int {
	x := [6]int{a.oop, -a.packed, a.conn, a.room, a.hot, -a.mobility}
	y := [6]int{b.oop, -b.packed, b.conn, b.room, b.hot, -b.mobility}
	if FXFestOrder { // exp-477: Festival's is_better_score order (OOP after rooms)
		x = [6]int{-a.packed, a.conn, a.room, a.oop, a.hot, -a.mobility}
		y = [6]int{-b.packed, b.conn, b.room, b.oop, b.hot, -b.mobility}
	}
	for i, v := range x {
		if v < y[i] {
			return -1
		}
		if v > y[i] {
			return 1
		}
	}
	return 0
}

type moveScore struct {
	oop, packed, conn, hot uint16
	room, mobility         uint32
}

func (a fessScore) compact() moveScore {
	hot := a.hot
	if SaturateScores {
		hot = min(hot, 65535) // the toward search's matching distance is 1<<20 when there is none
	}
	return moveScore{uint16(a.oop), uint16(a.packed), uint16(a.conn), uint16(hot), uint32(a.room), uint32(a.mobility)}
}

// SaturateScores (exp-254 fix): a huge score saturates instead of wrapping
// to 0 (which ranked moves with no matching to the target first).
var SaturateScores = true

func (a moveScore) full() fessScore {
	return fessScore{int(a.oop), int(a.packed), int(a.conn), int(a.room), int(a.hot), int(a.mobility)}
}

func (a fessScore) rank() uint64 {
	return uint64(min(a.oop, 511))<<51 |
		uint64(511-min(a.packed, 511))<<42 |
		uint64(min(a.conn, 1023))<<32 |
		uint64(min(a.room, 4095))<<20 |
		uint64(min(a.hot, 511))<<11 |
		uint64(2047-min(a.mobility, 2047))
}

type macroCandidate struct {
	i, b, t                             int32
	r, room, hot, mobility, oop, packed int
	plen, side                          int32
	progress                            int
	explorer                            bool
	accStart, accLen                    int32
	tier                                int8 // 0: width-1 / access-novel, 1: width-2 / advisor, 2: other
	candNP                              int32
	undo                                bool
	internal                            bool
	deadChild                           bool
	redundantDup                        bool
	groupDupExit                        bool
	distinctExit                        bool
	alreadyUsed                         bool
	stored                              bool // exp-609: the child is already a node of this tree
	exitKey                             uint64
}

func (m macroCandidate) score() fessScore {
	return fessScore{m.oop, m.packed, m.r, m.room, m.hot, m.mobility}
}

const (
	packingAdvisor = iota
	connectivityAdvisor
	roomAdvisor
	hotspotsAdvisor
	explorerAdvisor
	openerAdvisor
	oopAdvisor
	displacementAdvisor
	advisorCount
)

func (s *solver) advise(cur []int32, counts []int, parent fessScore, moves []macroCandidate, displaceMask uint64) [advisorCount]int {
	parentPhase := -1
	if s.plan != nil {
		parentPhase, _ = s.plan.status(s.occ)
	}
	chosen := [advisorCount]int{}
	for i := range chosen {
		chosen[i] = -1
	}
	isRevBad := func(m macroCandidate) bool {
		return RevGroups && (m.undo || m.internal || m.deadChild)
	}
	if s.pfOn {
		// Festival's backward search: one advisor, the best child better
		// than the parent (children are sorted later; ties: first).
		best := -1
		for k, m := range moves {
			if isRevBad(m) {
				continue
			}
			if s.pfFestAdv() && m.stored {
				continue // exp-609: Festival's advisor skips children already expanded
			}
			if m.score().compare(parent) >= 0 {
				continue
			}
			if best < 0 {
				best = k
				continue
			}
			c := m.score().compare(moves[best].score())
			if c < 0 || s.pfFestAdv() && c == 0 && (m.b < moves[best].b || m.b == moves[best].b && m.t < moves[best].t) {
				// exp-609: equal children tie in board order (box, then
				// destination), as Festival generates them, not in our flood order.
				best = k
			}
		}
		chosen[packingAdvisor] = best
		return chosen
	}
	if s.hfRank && HFOneAdvisor {
		// Festival's HF search has one advisor: the best child strictly
		// better than the parent in its HF order.
		pk := hfKey{parent.hot >= 1<<20, parent.conn, parent.packed, parent.room, parent.hot}
		best := -1
		for k, m := range moves {
			if isRevBad(m) {
				continue
			}
			mk := hfKey{m.hot >= 1<<20, m.r, m.packed, m.room, m.hot}
			if mk.better(pk) && (best < 0 || mk.better(hfKey{moves[best].hot >= 1<<20, moves[best].r, moves[best].packed, moves[best].room, moves[best].hot})) {
				best = k
			}
		}
		chosen[packingAdvisor] = best
		return chosen
	}
	choose := func(advisor, k int) {
		if isRevBad(moves[k]) {
			return
		}
		old := chosen[advisor]
		if old < 0 || moves[k].score().compare(moves[old].score()) < 0 || moves[k].score().compare(moves[old].score()) == 0 && moves[k].plen < moves[old].plen {
			chosen[advisor] = k
		}
	}
	hottest := -1
	for i, n := range counts {
		if n > 0 && (hottest < 0 || n > counts[hottest]) {
			hottest = i
		}
	}
	var hotFrom int32
	if hottest >= 0 {
		hotFrom = cur[hottest]
	}
	nearest := -1
	if s.basin != nil {
		for i, b := range cur {
			if !s.basin.inside[b] && !s.placed(b) && (nearest < 0 || s.basin.distance[b] < s.basin.distance[cur[nearest]]) {
				nearest = i
			}
		}
	}
	var oopFrom int32
	if nearest >= 0 {
		oopFrom = cur[nearest]
	}
	openerNear, oopNear := int32(1<<30), int32(1<<30)
	for k, m := range moves {
		if m.packed > parent.packed || s.plan != nil && m.progress > parentPhase {
			choose(packingAdvisor, k)
		}
		if m.r < parent.conn {
			choose(connectivityAdvisor, k)
		}
		if m.room < parent.room {
			choose(roomAdvisor, k)
		}
		if m.hot < parent.hot {
			choose(hotspotsAdvisor, k)
		}
		if m.explorer {
			choose(explorerAdvisor, k)
		}
		if hottest >= 0 && int(m.i) != hottest && m.r <= parent.conn && s.walk(hotFrom, m.t) > s.walk(hotFrom, m.b) {
			d := s.walk(hotFrom, m.b)
			if d < openerNear {
				openerNear = d
				chosen[openerAdvisor] = -1
			}
			if d == openerNear {
				choose(openerAdvisor, k)
			}
		}
		if nearest >= 0 && m.r <= parent.conn && s.basin.inside[m.t] && !s.basin.inside[m.b] {
			d := s.walk(oopFrom, m.b)
			if d < oopNear {
				oopNear = d
				chosen[oopAdvisor] = -1
			}
			if d == oopNear {
				choose(oopAdvisor, k)
			}
		}
		if displaceMask != 0 && s.dl != nil && m.t != m.b && m.r <= parent.conn {
			gIdxB := s.dl.goalIdx[m.b]
			if gIdxB >= 0 && (displaceMask&(uint64(1)<<gIdxB)) != 0 {
				gIdxT := s.dl.goalIdx[m.t]
				if gIdxT < 0 || (displaceMask&(uint64(1)<<gIdxT)) == 0 {
					choose(displacementAdvisor, k)
				}
			}
		}
	}
	if chosen[packingAdvisor] < 0 && s.packPrepared {
		clearing := s.packingObligationAdvice(moves, func(k int) { choose(packingAdvisor, k) })
		if chosen[packingAdvisor] >= 0 {
			s.stats.PackingPrepPicks++
			if clearing {
				s.stats.PackingPrepClears++
			}
		}
	}
	return chosen
}

// walk is the player's walking distance (walls only) between two floor
// cells, from a table of all floor-cell pairs built once per level: it
// depends on nothing but the walls. Levels with very large floors fall back
// to a BFS per call (-1: unreachable).
func (s *solver) walk(from, to int32) int32 {
	if s.walkF > 0 {
		return int32(s.walkTab[int(s.walkIdx[from])*s.walkF+int(s.walkIdx[to])])
	}
	return s.walkBFS(from, to)
}

const walkTableMax = 1500 // floor cells; the table is walkTableMax^2 int16 (4.5 MB)

func (s *solver) buildWalk() {
	n := len(s.lv.Wall)
	s.walkIdx = make([]int32, n)
	var floor []int32
	for c := range int32(n) {
		s.walkIdx[c] = -1
		if !s.lv.Wall[c] {
			s.walkIdx[c] = int32(len(floor))
			floor = append(floor, c)
		}
	}
	F := len(floor)
	if F > walkTableMax {
		s.walkTab = []int16{} // non-nil: built (no table)
		return
	}
	s.walkF = F
	s.walkTab = make([]int16, F*F)
	q := make([]int32, 0, F)
	for fi, start := range floor {
		row := s.walkTab[fi*F : (fi+1)*F]
		for i := range row {
			row[i] = -1
		}
		row[fi] = 0
		q = append(q[:0], start)
		for i := 0; i < len(q); i++ {
			dq := row[s.walkIdx[q[i]]]
			for _, d := range s.dirs {
				x := q[i] + d
				if !s.lv.Wall[x] && row[s.walkIdx[x]] < 0 {
					row[s.walkIdx[x]] = dq + 1
					q = append(q, x)
				}
			}
		}
	}
}

func (s *solver) walkBFS(from, to int32) int32 {
	n := len(s.lv.Wall)
	if len(s.walkScratch) != n {
		s.walkScratch = make([]int32, n)
	}
	dist := s.walkScratch
	for i := range dist {
		dist[i] = -1
	}
	dist[from] = 0
	q := append(s.walkQ[:0], from)
	for i := 0; i < len(q); i++ {
		if q[i] == to {
			break
		}
		for _, d := range s.dirs {
			x := q[i] + d
			if !s.lv.Wall[x] && dist[x] < 0 {
				dist[x] = dist[q[i]] + 1
				q = append(q, x)
			}
		}
	}
	s.walkQ = q
	return dist[to]
}

// moveAccessScan reuses the final-player flood from boxSearch. Explorer needs
// newly reachable support for a legal push of another, unchanged box.
// When AccessNovelty is enabled, it also records access capability facts.
// It reads mark3 box by box and direction by direction: the path for boards
// wider than 64 cells and the reference for moveAccessBits (exp-419).
func (s *solver) moveAccessScan(cur []int32, moved int, origB, target int32, st uint32, cmdMask, unpinMask uint64, unpackedRegime bool) (mobility int, explorer bool, accStart, accLen int32) {
	cs := s.pstamp
	recordAcc := AccessNovelty && s.noveltyBudget() > 0
	if recordAcc {
		accStart = int32(len(s.accBuf))
	}
	displaced := recordAcc && (unpackedRegime || (cmdMask|unpinMask) != 0 || (s.lv.Goal[origB] && !s.lv.Goal[target]))
	goalVacated := recordAcc && ((cmdMask|unpinMask) != 0 || (s.lv.Goal[origB] && !s.lv.Goal[target]))

	for i, b := range cur {
		if i == moved {
			b = target
		}
		for dirIdx, d := range s.dirs {
			// The player's cell to move b in direction d, and the cell that
			// must be free: the box's destination (push) or the player's
			// step back (pull). The box lands on b+d either way.
			support, to := s.front(b, d), b+d
			if s.pull {
				to = b + 2*d
			}
			if s.mark3[support] != cs {
				continue
			}
			mobility++
			// Legality is only needed for a newly reachable support of an
			// unchanged box (not at all once explorer is set and nothing is
			// recorded) or for goal-vacated facts.
			fresh := i != moved && s.mark[support] != st
			if !goalVacated && (!fresh || explorer && !recordAcc) {
				continue
			}
			legal := !s.lv.Wall[to] && !s.occ[to] && s.dist[b+d] >= 0
			if fresh && legal {
				explorer = true
				if recordAcc {
					psi := uint32(b*4 + int32(dirIdx))
					// Width 1 push-side capability (tag 0 in bits 40..43)
					s.accBuf = append(s.accBuf, uint64(psi))
					// Tag 1: Width 2 push-side + moved box target (always when support is newly reachable!)
					s.accBuf = append(s.accBuf, (uint64(1)<<40)|(uint64(psi)<<20)|uint64(target))
					if displaced {
						// Tag 2 in bits 40..43: push-side + vacated origin
						s.accBuf = append(s.accBuf, (uint64(2)<<40)|(uint64(psi)<<20)|uint64(origB))
					}
				}
			}
			if goalVacated && legal && (s.mark[support] != st || b == target) {
				psi := uint32(b*4 + int32(dirIdx))
				// Fact C: tag 4 in bits 40..43
				s.accBuf = append(s.accBuf, (uint64(4)<<40)|(uint64(origB)<<20)|uint64(psi))
			}
		}
	}
	if recordAcc {
		// Fact B: newly reachable canonical player region / passage crossing
		if s.candNP >= 0 && s.mark[s.candNP] != st {
			// Tag 3 in bits 40..43
			s.accBuf = append(s.accBuf, (uint64(3)<<40)|(uint64(s.candNP)<<20)|uint64(target))
		}
		accLen = int32(len(s.accBuf)) - accStart
	}
	return
}

// HFOneAdvisor (exp-254): the toward search's only advisor is Festival's HF
// default advisor (a child strictly better than its parent).
var HFOneAdvisor = false

// HFUnweighted (exp-254): the toward search's moves all cost 0 for the first
// 3/4 of its expansions (Festival: weighted_search = 0 for 3/4 of HF time).
var HFUnweighted = false

// hfKey is Festival's forward HF order: a matching to the target exists,
// fewer regions, more boxes on the target, fewer hotspots, smaller matching
// distance.
type hfKey struct {
	noMatch             bool
	regions, on, hot, d int
}

func (a hfKey) better(b hfKey) bool {
	if a.noMatch != b.noMatch {
		return !a.noMatch
	}
	if a.regions != b.regions {
		return a.regions < b.regions
	}
	if a.on != b.on {
		return a.on > b.on
	}
	if a.hot != b.hot {
		return a.hot < b.hot
	}
	return a.d < b.d
}
