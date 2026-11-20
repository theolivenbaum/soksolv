package main

import (
	"fmt"
	"slices"
)

// open evaluates all legal macros once. Every advisor has an independent
// eligibility rule and picks at most one; the union receives zero weight.
func (s *solver) open(id int32, cur []int32, h int32, st uint32) {
	s.packPrepared = false
	s.oopSt++ // exp-659: oopChild's per-node counts
	s.accBuf = s.accBuf[:0]
	s.mvStart[id] = int32(len(s.mv))
	reopening := s.mvOff[id] > 0
	gid := int32(-1)
	pB0, pT0, pPlayer := int32(-1), int32(-1), int32(-1)
	p := s.parent[id]
	if RevGroups || (RetargetReuse && s.retargetNodes > 0) {
		copy(s.revKey, s.boxes[int(id)*s.nw:int(id+1)*s.nw])
		gid = s.findRevGroup(id)
		if p >= 0 && s.bucket[p] == s.bucket[id] {
			pB0 = s.pushFrom[id]
			pT0 = s.pushTo[id]
			pPlayer = s.player[p]
		}
	}
	if !s.feasible(cur) {
		pdW.note("assignment", -1)
		s.stats.Assignment++
		s.mvLen[id], s.mvLeft[id], s.mvNext[id] = 0, 0, 0
		return
	}
	var cmdMask, unpinMask uint64
	if DisplacementLandmarksOn && !s.pull && !s.hfRank && !s.pfOn && len(s.goals) <= 64 {
		if !s.dlInit {
			s.dlInit = true
			s.dl = NewDisplacementLandmarks(s.lv, s.dirs, s.goals)
		}
		if s.dl != nil {
			cmdMask = s.dl.ComputeSinglePinCMD(cur, s.player[id])
			if cmdMask == 0 {
				_, unpinMask = s.dl.ComputeCollectiveUnpacking(cur, s.player[id])
			}
		}
	}
	counts := s.hot.counts(s, cur)
	var parentSpread int32
	if s.pull {
		for _, b := range cur {
			parentSpread += s.spreadDist[b]
		}
	}
	parentFrozen := false
	parent := fessScore{s.outOfPlan(cur), s.packed(), int(s.reg[id]), s.roomConnParent(), hotspotCount(counts), 0}
	bk := int(s.bucket[id])
	unpackedRegime := bk < len(s.frontier.cells) && parent.packed < s.frontier.cells[bk].packed
	if s.connFirst {
		parent.oop = int(s.reg[id])
	}
	if s.hfRank {
		// Festival's forward HF order: connectivity, boxes on target,
		// hotspots against the target, min-cost matching distance (the room
		// and hotspot advisors then pursue the last two).
		parent.oop, parent.room, parent.hot = int(s.reg[id]), s.hotRel(cur), s.hfMatchParent(cur)
		if HFFrozenMatch && s.hfFrozenOff(cur) {
			parent.hot = max(parent.hot, 1<<20)
			parentFrozen = true
		}
		if HFMatchFirst && parent.hot >= 1<<20 {
			parent.oop += hfNoMatch
		}
	}
	if s.pull {
		parent.packed = int(parentSpread) / SpreadBucket
	}
	if s.pfOn {
		s.pfInit()
		pd := int32(s.hfMatchParent(cur)) // also primes the one-row updates
		if pd >= assignmentInf {
			pd = -1
		}
		pt := pfTuple{reg: int32(s.reg[id]), rooms: int32(parent.room), dist: pd}
		s.pfFill(&pt, cur)
		parent = pfScore(pt)
		if s.pfAdvFix() {
			// exp-605: children carry their region count in conn (m.score()), so the
			// parent must too; with conn 0 no same-region child beat its parent and
			// the backward search had no advisor unless the region count fell.
			parent.conn = int(pt.reg)
		}
	}
	moves := s.candBuf[:0] // reused per open: no per-node allocation
	var corral uint32
	if !s.pull { // PI-corral pruning is a push argument
		if HubNoCorral && s.hfRank {
			// exp-449: no corral pruning in the toward search
		} else if FestCorral && s.hfRank {
			corral = s.festCorral(s.player[id], st)
			pdW.corralKind("corral-fest")
		} else {
			if FrontierCorral {
				corral = s.firstPushCorral(s.player[id], st)
				pdW.corralKind("corral-frontier")
			}
			if corral == 0 {
				corral = s.piCorral(st)
				pdW.corralKind("corral-pi")
			}
		}
	}
	if pdNoCorral {
		corral = 0
	}
	if corral != 0 {
		s.stats.Corrals++
	}
	if PackingObligations {
		s.preparePackingObligations()
	}
	if s.tseen == nil {
		s.tseen = make([]uint32, 4*len(s.lv.Wall))
	}
	if s.bb != nil {
		s.bb.node, s.bb.nodeBuilt, s.bb.parentBuilt = true, false, false
	}
	if s.plan != nil {
		s.plan.nodeSetup(cur)
	}
	for i, b := range cur {
		if !TransactionalOpen && s.expired() {
			break
		}
		if corral != 0 && s.cmark[b] != corral {
			pdW.noteCorral(b)
			continue
		}
		s.tst++
		ts := s.tst
		cands := 0 // the articulation pass pays off from a box's second candidate
		s.boxSearch(b, st, func(k, t int32) bool {
			if s.meetAudit != nil && !s.pull {
				s.meetAudit.record(s, id, cur, i, t, k%4)
			}
			if !CleanPerimeter && s.fwd != nil && DensePerimeter {
				// Every candidate child joins this searcher's perimeter at
				// once (as Festival's generated nodes do), not only the moves
				// FESS later applies. s.queue holds this endpoint's player
				// flood.
				s.publishCandidate(id, cur, i, t, k%4)
			}
			canonical := int32(0)
			for side, d := range s.dirs {
				if s.mark3[t-d] == s.pstamp {
					canonical = int32(side)
					break
				}
			}
			pdW.visit(b, t)
			if t == b && s.hfRank && HFNoReturn {
				pdW.noteT("no-return")
				return true // exp-268: no macro returning its box to its square
			}
			endpoint := t*4 + canonical
			if s.tseen[endpoint] == ts {
				return true
			}
			s.tseen[endpoint] = ts
			s.occ[t] = true
			dr := s.pushDead(cur, i, t, k, canonical)
			if pdW != nil && dr != alive {
				pdW.noteT(dr.String())
			}
			switch dr {
			case deadPullFreeze, deadFreeze:
				s.stats.Freeze++
			case dead2x2:
				s.stats.Frozen2x2++
			case deadGoalArea:
				s.stats.GoalArea++
			case deadPair, deadPairSide:
				s.stats.PairDead++
			case deadSide:
				s.stats.SideDead++
			case deadInefficient:
				s.stats.Inefficient++
			case deadCluster:
				s.stats.ClusterDead++
			case deadCorridorFlow:
				s.stats.CorridorFlow++
			case deadDiag:
				s.stats.DiagDead++
			case deadFrozenHall:
				s.stats.FrozenHall++
			default:
				s.stats.Generated++
				if CleanPerimeter && !reopening && s.fwd != nil && DensePerimeter {
					s.publishCandidate(id, cur, i, t, k%4)
				}
				if DebugCandidateMove != nil {
					DebugCandidateMove(s, id, cur, i, b, t, st, cmdMask, unpinMask)
				}
				m := macroCandidate{i: int32(i), b: b, t: t, side: k % 4, plen: 1}
				for x := s.bprev[k]; x >= 0; x = s.bprev[x] {
					m.plen++
				}
				cands++
				switch cands {
				case 1:
					m.r = s.regionsChild(t)
				case 2:
					s.occ[t] = false // the pass analyses the board without this box
					s.computeArticulation()
					s.occ[t] = true
					fallthrough
				default:
					m.r = s.childRegions(t)
				}
				if s.tgtPocket != nil && m.r > 1 {
					if r := s.regions(); r != m.r { // target-relative (occ holds the child)
						m.r = r
						s.stats.RegRelAdj++
					}
				}
				m.room = s.roomConnChild(b, t)
				if checkRooms && m.room != s.roomConnectivity() {
					panic("incremental room connectivity disagrees with roomConnectivity()")
				}
				if checkRegions && m.r != s.regions() {
					panic("incremental region count disagrees with regions()")
				}
				if checkRegions && t != b {
					occN := 0
					for _, o := range s.occ {
						if o {
							occN++
						}
					}
					if occN != s.n || s.occ[b] || !s.occ[t] {
						panic(fmt.Sprintf("occupancy at child scoring: %d cells for %d boxes, origin %v, target %v", occN, s.n, s.occ[b], s.occ[t]))
					}
				}
				m.hot = s.hot.after(s, cur, counts, i, t)
				m.mobility, m.explorer, m.accStart, m.accLen = s.moveAccess(cur, i, b, t, st, cmdMask, unpinMask, unpackedRegime)
				if s.connFirst {
					m.oop = m.r // the first key: fewer regions first (Festival's HF order)
				}
				if s.pull {
					// How far the boxes have spread from the goals: the
					// progress feature of the backward search (packed slot:
					// larger is better, and a FESS cell key, so rising spread
					// keeps opening new cells and drives the search deep).
					m.packed = int(parentSpread-s.spreadDist[b]+s.spreadDist[t]) / SpreadBucket
				}
				cphase, cpacked := -2, -1
				if s.plan != nil {
					cphase, cpacked = s.plan.childStatus(b, t)
					if checkRegions {
						if ph, pk := s.plan.status(s.occ); ph != cphase || pk != cpacked {
							panic(fmt.Sprintf("incremental plan status %d/%d disagrees with status() %d/%d", cphase, cpacked, ph, pk))
						}
					}
				}
				if !s.pull {
					// Pull mode's packed is the incremental spread sum set above
					// (the festb path zeroes it below): packedWith would rescan
					// every floor cell per child for the same value.
					if LazyChild {
						m.packed = s.packedChild(b, t, cpacked)
						if checkRegions && m.packed != s.packedWith(cpacked) {
							panic("incremental packed count disagrees with packedWith()")
						}
					} else {
						m.packed = s.packedWith(cpacked)
					}
				}
				if s.plan != nil {
					m.progress = cphase
				}
				// outOfPlan reads box coordinates, while occupancy supplies the
				// child's packed-goal status. Keep cur in its original order.
				old := cur[i]
				cur[i] = t
				if LazyChild {
					m.oop = s.oopChild(cur, i, old, cphase, m.packed)
					if checkRegions && m.oop != s.outOfPlanStep(cur, cphase, m.packed) {
						panic("incremental out-of-plan count disagrees with outOfPlanStep()")
					}
				} else {
					m.oop = s.outOfPlanStep(cur, cphase, m.packed)
				}
				if s.hfRank {
					m.oop, m.room, m.hot = m.r, s.hotRel(cur), s.hfMatchChild(cur, i, old)
					// Festival predicts a child's distance with only the
					// moved box (and the parent's frozen boxes) pinned.
					if HFFrozenMatch && (parentFrozen || s.hfFrozenOff(cur[i:i+1])) {
						m.hot = max(m.hot, 1<<20)
					}
					if HFMatchFirst && m.hot >= 1<<20 {
						m.oop += hfNoMatch
					}
				}
				cur[i] = old
				if s.pfOn {
					cur[i] = t
					cd := int32(s.hfMatchChild(cur, i, old))
					if cd >= assignmentInf {
						cd = -1
					}
					pt := pfTuple{reg: int32(m.r), rooms: int32(m.room), dist: cd}
					if RootBareScore && s.pfSty.rootBare && s.parent[id] < 0 {
						// exp-286: rootBare score-only: rank root moves by bare-board features (reg=1, rooms=0, hot=0)
						// without corrupting legal occupancy or move generation.
						pt.reg = 1
						pt.rooms = 0
						if s.pfSty.spread && pt.dist >= 0 {
							pt.dist = s.pfSpreadSum(cur)
						}
					} else {
						s.pfFill(&pt, cur)
					}
					cur[i] = old
					sc := pfScore(pt)
					m.oop, m.packed, m.room, m.hot, m.mobility = sc.oop, 0, sc.room, sc.hot, sc.mobility
					m.r = int(pt.reg)
				}
				if cmdMask != 0 && s.dl != nil && m.r <= parent.conn {
					gIdxB := s.dl.goalIdx[b]
					if gIdxB >= 0 && (cmdMask&(uint64(1)<<gIdxB)) != 0 {
						gIdxT := s.dl.goalIdx[t]
						if gIdxT < 0 || (cmdMask&(uint64(1)<<gIdxT)) == 0 {
							if !s.pull && !s.pfOn && !s.hfRank {
								m.packed = max(m.packed, parent.packed)
							}
							if !s.connFirst && !s.hfRank && !s.pfOn {
								m.oop = min(m.oop, parent.oop)
							}
						}
					}
				}
				if RevGroups || (RetargetReuse && s.retargetNodes > 0) {
					m.candNP = s.candNP
					if !reopening || (RetargetReuse && s.retargetNodes > 0) {
						// 1. Direct parent undo check (exp-290)
						if !reopening && RevGroups && pB0 >= 0 && b == pT0 && t == pB0 && s.candNP == pPlayer {
							s.unionRevGroup(p, id)
							gid = s.findRevGroup(id)
							m.undo = true
						}
						// 2. Incremental 2-bit state lookup (exp-290)
						var existingID int32 = -1
						var fp uint32
						if b != t {
							fb := s.fidx[b]
							ft := s.fidx[t]
							s.revKey[fb>>6] &^= 1 << (fb & 63)
							s.revKey[ft>>6] |= 1 << (ft & 63)
							_, existingID, fp = s.find(s.revKey, s.candNP)
							s.revKey[ft>>6] &^= 1 << (ft & 63)
							s.revKey[fb>>6] |= 1 << (fb & 63)
						} else {
							_, existingID, fp = s.find(s.revKey, s.candNP)
						}

						m.stored = existingID >= 0
						if RetargetReuse && s.retargetNodes > 0 && existingID >= 0 && existingID < s.retargetNodes {
							improvesCell := s.hfRank && HFFestSched && int(id) < len(s.hfBest) && int(existingID) < len(s.hfBest) && hfBetter(s.hfBest[id], s.hfBest[existingID])
							if s.dead[existingID] || (!improvesCell && s.g[id]+int32(m.plen) >= s.g[existingID]) {
								m.alreadyUsed = true
								if !reopening && !s.dead[existingID] && s.parent[existingID] != id {
									s.recordEdge(id, existingID)
								}
							}
						}

						if !reopening && RevGroups {
							if existingID >= 0 {
								if s.dead[existingID] {
									m.deadChild = true
								} else if s.bucket[existingID] == s.bucket[id] && s.w[id] >= s.w[existingID] {
									if existingID == p || s.findRevGroup(existingID) == gid {
										m.internal = true
									} else {
										isAnc := false
										for anc, depth := p, 0; anc >= 0 && depth < 8; anc, depth = s.parent[anc], depth+1 {
											if s.bucket[anc] != s.bucket[id] {
												break
											}
											if anc == existingID {
												isAnc = true
												break
											}
										}
										if isAnc {
											s.unionRevGroup(existingID, id)
											gid = s.findRevGroup(id)
											m.internal = true
										} else if s.w[id] > s.w[existingID] || (s.w[id] == s.w[existingID] && s.g[id]+int32(m.plen) >= s.g[existingID]) {
											m.redundantDup = true
										}
									}
								}
							} else if gid >= 0 && int(gid) < len(s.revSize) && s.revSize[gid] >= 2 {
								exitKey := (uint64(gid) << 40) | uint64(fp)
								if s.revExits.has(exitKey) {
									m.groupDupExit = true
								} else {
									m.distinctExit = true
									m.exitKey = exitKey
								}
							}
						}
					}
				}
				moves = append(moves, m)
			}
			s.occ[t] = false
			return true
		})
	}
	if s.bb != nil {
		s.bb.node = false
	}
	s.candBuf = moves[:0]
	chosen := s.advise(cur, counts, parent, moves, cmdMask|unpinMask)
	s.wBuf = slices.Grow(s.wBuf[:0], len(moves))[:len(moves)]
	weights := s.wBuf
	for i := range weights {
		weights[i] = 1
		if s.noveltyBudget() > 0 {
			moves[i].tier = 3
		} else {
			moves[i].tier = 0
		}
	}
	for advisor, k := range chosen {
		if k >= 0 {
			weights[k] = 0
			if s.noveltyBudget() > 0 {
				switch advisor {
				case packingAdvisor, connectivityAdvisor, roomAdvisor, displacementAdvisor:
					moves[k].tier = 0
				default:
					moves[k].tier = 1
				}
			}
			s.stats.Advisors[advisor]++
			if RevGroups && moves[k].distinctExit {
				s.revExits.insert(moves[k].exitKey)
			}
		}
	}
	bestExit := -1
	if RevGroups {
		if !reopening {
			if gid >= 0 && int(gid) < len(s.revSize) && s.revSize[gid] >= 2 {
				hasDistinctAdvisorExit := false
				for _, k := range chosen {
					if k >= 0 && moves[k].distinctExit {
						hasDistinctAdvisorExit = true
						break
					}
				}
				if !hasDistinctAdvisorExit && !(s.pfFestAdv() && s.pfOn) { // exp-609: Festival's only zero-weight move is its advisor
					for k, m := range moves {
						if !m.distinctExit || m.undo || m.internal || m.deadChild {
							continue
						}
						progress := m.packed > parent.packed || m.r < parent.conn || m.room < parent.room ||
							m.hot < parent.hot || m.explorer
						if !progress && (cmdMask|unpinMask) != 0 && s.dl != nil && m.r <= parent.conn && m.t != m.b {
							gIdxB := s.dl.goalIdx[m.b]
							if gIdxB >= 0 && ((cmdMask|unpinMask)&(uint64(1)<<gIdxB)) != 0 {
								gIdxT := s.dl.goalIdx[m.t]
								if gIdxT < 0 || ((cmdMask|unpinMask)&(uint64(1)<<gIdxT)) == 0 {
									progress = true
								}
							}
						}
						if progress {
							if bestExit < 0 || m.score().compare(moves[bestExit].score()) < 0 ||
								(m.score().compare(moves[bestExit].score()) == 0 && m.plen < moves[bestExit].plen) {
								bestExit = k
							}
						}
					}
					if bestExit >= 0 {
						weights[bestExit] = 0
						if s.noveltyBudget() > 0 {
							if moves[bestExit].packed > parent.packed || moves[bestExit].r < parent.conn || moves[bestExit].room < parent.room {
								moves[bestExit].tier = 0
							} else {
								moves[bestExit].tier = 1
							}
						}
						s.revExits.insert(moves[bestExit].exitKey)
					}
				}
			}
			if int(id) < len(s.revBestExit) {
				s.revBestExit[id] = int16(bestExit)
			}
			nw := (len(moves) + 63) / 64
			revOff := int32(len(s.revWords))
			s.revWords = append(s.revWords, make([]uint64, nw)...)
			if int(id) < len(s.revOffset) {
				s.revOffset[id] = revOff + 1
			}
			for k, m := range moves {
				if m.undo || m.internal || m.deadChild {
					s.revWords[revOff+int32(k/64)] |= uint64(1) << (k % 64)
				}
			}
		} else {
			if int(id) < len(s.revOffset) && s.revOffset[id] > 0 {
				revOff := s.revOffset[id] - 1
				for k := range moves {
					if (s.revWords[revOff+int32(k/64)] & (uint64(1) << (k % 64))) != 0 {
						moves[k].undo = true
					}
				}
			}
			if int(id) < len(s.revBestExit) && s.revBestExit[id] >= 0 {
				be := int(s.revBestExit[id])
				if be < len(moves) {
					weights[be] = 0
					if s.noveltyBudget() > 0 {
						if moves[be].packed > parent.packed || moves[be].r < parent.conn || moves[be].room < parent.room {
							moves[be].tier = 0
						} else {
							moves[be].tier = 1
						}
					}
				}
			}
		}
	}
	if StableWindows {
		if novelty := s.noveltyBudget(); novelty > 0 {
			if !reopening {
				bk := int(s.bucket[id])
				for len(s.novSeen) <= bk {
					s.novSeen = append(s.novSeen, nil)
				}
				if s.novSeen[bk] == nil {
					s.novSeen[bk] = make([]uint64, (len(s.lv.Wall)+63)/64)
				}
				seen := s.novSeen[bk]
				if AccessNovelty {
					for len(s.novPushSeen) <= bk {
						s.novPushSeen = append(s.novPushSeen, nil)
					}
					if s.novPushSeen[bk] == nil {
						s.novPushSeen[bk] = make([]uint64, (4*len(s.lv.Wall)+63)/64)
					}
				}
				nov := 0
				nw := (len(moves) + 63) / 64
				novOff := int32(len(s.novWords))
				s.novWords = append(s.novWords, make([]uint64, nw)...)
				if AccessNovelty {
					s.novTier0Words = append(s.novTier0Words, make([]uint64, nw)...)
					s.novTier1Words = append(s.novTier1Words, make([]uint64, nw)...)
				}
				if int(id) < len(s.novOffset) {
					s.novOffset[id] = novOff + 1
				}
				parentConn := parent.conn
				for k, m := range moves {
					w, bit := m.t/64, uint64(1)<<(m.t%64)
					w1Novel := seen[w]&bit == 0
					w2Novel := false
					if Novelty2 {
						for j, b := range cur {
							if int32(j) == m.i {
								continue
							}
							a, c := m.t, b
							if a > c {
								a, c = c, a
							}
							if !s.novPairs.has(uint64(bk)<<40 | uint64(a)<<20 | uint64(c)) {
								w2Novel = true
								break
							}
						}
					}
					accessNovel := false
					if AccessNovelty && m.accLen > 0 {
						for _, rawKey := range s.accBuf[m.accStart : m.accStart+m.accLen] {
							tag := (rawKey >> 40) & 0xF
							if tag == 0 {
								psi := rawKey
								pw, pbit := psi/64, uint64(1)<<(psi%64)
								if s.novPushSeen[bk][pw]&pbit == 0 {
									accessNovel = true
									break
								}
							} else {
								factKey := (uint64(bk) << 44) | rawKey
								if !s.novAccess.has(factKey) {
									accessNovel = true
									break
								}
							}
						}
					}
					novel := w1Novel || w2Novel || accessNovel
					if RevGroups && (m.undo || m.internal || m.deadChild) {
						novel = false
					}
					if novel && nov < novelty {
						seen[w] |= bit
						if Novelty2 {
							for j, b := range cur {
								if int32(j) == m.i {
									continue
								}
								a, c := m.t, b
								if a > c {
									a, c = c, a
								}
								s.novPairs.insert(uint64(bk)<<40 | uint64(a)<<20 | uint64(c))
							}
						}
						if AccessNovelty && m.accLen > 0 {
							for _, rawKey := range s.accBuf[m.accStart : m.accStart+m.accLen] {
								tag := (rawKey >> 40) & 0xF
								if tag == 0 {
									psi := rawKey
									s.novPushSeen[bk][psi/64] |= uint64(1) << (psi % 64)
								} else {
									factKey := (uint64(bk) << 44) | rawKey
									s.novAccess.insert(factKey)
								}
							}
						}
						weights[k] = 0
						nov++
						s.novWords[novOff+int32(k/64)] |= uint64(1) << (k % 64)
						connMaintained := m.r <= parentConn
						tier := int8(2)
						if moves[k].tier == 0 {
							tier = 0
						} else if AccessNovelty && accessNovel && connMaintained {
							tier = 0
						} else if w1Novel && connMaintained {
							tier = 1
						} else if moves[k].tier == 1 {
							tier = 1
						}
						if RevGroups && m.groupDupExit {
							tier = 2
						}
						moves[k].tier = tier
						if AccessNovelty {
							if tier == 0 {
								s.novTier0Words[novOff+int32(k/64)] |= uint64(1) << (k % 64)
							} else if tier == 1 {
								s.novTier1Words[novOff+int32(k/64)] |= uint64(1) << (k % 64)
							}
						}
					} else {
						if moves[k].tier > 2 {
							moves[k].tier = 3
						}
					}
				}
				s.stats.Novel += nov
			} else {
				if int(id) < len(s.novOffset) && s.novOffset[id] > 0 {
					novOff := s.novOffset[id] - 1
					for k := range moves {
						if (s.novWords[novOff+int32(k/64)] & (uint64(1) << (k % 64))) != 0 {
							weights[k] = 0
							if AccessNovelty {
								if len(s.novTier0Words) > 0 && (s.novTier0Words[novOff+int32(k/64)]&(uint64(1)<<(k%64))) != 0 {
									moves[k].tier = 0
								} else if len(s.novTier1Words) > 0 && (s.novTier1Words[novOff+int32(k/64)]&(uint64(1)<<(k%64))) != 0 {
									moves[k].tier = 1
								} else {
									moves[k].tier = 2
								}
							} else {
								moves[k].tier = 2
							}
						} else {
							if moves[k].tier > 2 {
								moves[k].tier = 3
							}
						}
					}
				}
			}
		}
	} else {
		if novelty := s.noveltyBudget(); novelty > 0 && !reopening { // forward, and Festival-ordered pull (exp-279): it hurts the plain pull lane (pull screen +27 -35, lost xsokoban-90)
			// Novelty advisor (exp-192; best-first width search, Lipovetzky &
			// Geffner): within this node's FESS cell, a move whose box lands on
			// a square no box has reached before in the cell is novel and costs
			// nothing, like an advisor's pick. On deceptive plateaus (the
			// unpacked opening, unpacking endgames) it drives exploration that
			// the feature order alone does not.
			bk := int(s.bucket[id])
			for len(s.novSeen) <= bk {
				s.novSeen = append(s.novSeen, nil)
			}
			if s.novSeen[bk] == nil {
				s.novSeen[bk] = make([]uint64, (len(s.lv.Wall)+63)/64)
			}
			seen := s.novSeen[bk]
			nov := 0
			for k, m := range moves {
				w, bit := m.t/64, uint64(1)<<(m.t%64)
				novel := seen[w]&bit == 0
				seen[w] |= bit
				if Novelty2 {
					// Width 2: the moved box's square paired with each other box's
					// square, new together in this cell.

					for j, b := range cur {
						if int32(j) == m.i {
							continue
						}
						a, c := m.t, b
						if a > c {
							a, c = c, a
						}
						if s.novPairs.insert(uint64(bk)<<40 | uint64(a)<<20 | uint64(c)) {
							novel = true
						}
					}
				}
				if novel && nov < novelty {
					weights[k] = 0
					nov++
				}
			}
			s.stats.Novel += nov
		}
	}
	if limit := s.expansionLimit(); s.hfRank && HFUnweighted && (limit <= 0 || s.expandedN < limit*3/4) {
		// Festival's HF search runs its first 3/4 unweighted: within a
		// cell, plain best-first by the HF order.
		clear(weights)
	}
	if chosen[packingAdvisor] >= 0 {
		s.stats.Macros++
	}
	if chosen[connectivityAdvisor] >= 0 {
		s.stats.ConnAdvisor++
	}
	if chosen[roomAdvisor] >= 0 {
		s.stats.RoomAdvisor++
	}
	s.ordBuf = slices.Grow(s.ordBuf[:0], len(moves))[:len(moves)]
	order := s.ordBuf
	// Sort by a precomputed key (exp-638): the former stable comparator,
	// flattened, with the generation index as the last tie-break, so the
	// unstable pdqsort gives the same order.
	s.keyBuf = slices.Grow(s.keyBuf[:0], len(moves))[:len(moves)]
	keys := s.keyBuf
	useTier := s.noveltyBudget() > 0 && AccessNovelty
	for i := range keys {
		m := &moves[i]
		k := &keys[i]
		k.bad = 0
		if RevGroups && (m.undo || m.internal || m.deadChild) {
			k.bad = 1
		}
		k.w = weights[i]
		k.tier = 0
		if useTier {
			k.tier = m.tier
		}
		if FXFestOrder {
			k.sc = [6]int{-m.packed, m.r, m.room, m.oop, m.hot, -m.mobility}
		} else {
			k.sc = [6]int{m.oop, -m.packed, m.r, m.room, m.hot, -m.mobility}
		}
		k.h = 0
		if Seed != 0 {
			k.h = seedHash(m.t*4 + m.side + m.i<<16)
		}
		if s.pfOn {
			k.x, k.y = m.b, m.t
		} else {
			k.x, k.y = m.plen, 0
		}
		k.idx = int32(i)
	}
	if need := s.rankedNeed(id, len(keys)); need < len(keys) {
		// exp-659 -lazy-child: only the ranks up to the end of this window
		// are read; the key's total order makes them the full sort's.
		selectKeys(keys, need)
		keys = keys[:need]
		order = order[:need]
	} else {
		slices.SortFunc(keys, cmpMoveKey)
	}
	for i := range keys {
		order[i] = int(keys[i].idx)
	}
	if DebugOpen != nil {
		DebugOpen(s, id, cur, moves, weights, order)
	}
	s.maskBuf = slices.Grow(s.maskBuf[:0], len(moves))[:len(moves)]
	advMask := s.maskBuf
	clear(advMask)
	for advisor, k := range chosen {
		if k >= 0 {
			advMask[k] |= 1 << advisor
		}
	}
	// MoveWindow: store only the window [off, off+MoveWindow) of the sorted
	// moves (most are never applied); enqueue re-opens the node for the next
	// window when this one runs out with moves left.
	off := min(int(s.mvOff[id]), len(order))
	win := order[off:]
	if MoveWindow > 0 && len(win) > MoveWindow {
		win = win[:MoveWindow]
	}
	usedInWin := uint32(0)
	for _, k := range win {
		m := moves[k]
		mvWord := encodeMove(m.i, m.t, weights[k], m.r, m.side) | advMask[k]<<moveAdvShift | uint64(m.tier)<<moveTierShift
		if m.alreadyUsed {
			mvWord |= moveUsed
			usedInWin++
		}
		s.mv = append(s.mv, mvWord)
		s.mvScore = append(s.mvScore, m.score().compact())
	}
	s.mvLen[id] = uint32(len(win))
	s.mvNext[id] = 0
	s.mvTotal[id] = uint32(len(moves))
	left := uint32(0)
	if len(order) < len(moves) {
		for k := range moves {
			if !moves[k].alreadyUsed {
				left++
			}
		}
		for _, k := range order[:off] {
			if !moves[k].alreadyUsed {
				left--
			}
		}
	} else {
		for _, k := range order[off:] {
			if !moves[k].alreadyUsed {
				left++
			}
		}
	}
	s.mvLeft[id] = left
	if reopening {
		s.stats.Reopens++
	}
}

// HFNoReturn (exp-268): the toward-hf search skips macros that bring the
// box back to its own square (only the player's region changes).
var HFNoReturn = false

// DebugOpen (tests): called with every opened node's scored, ordered moves.
var DebugOpen func(s *solver, id int32, cur []int32, moves []macroCandidate, weights []int32, order []int)

// DebugCandidateMove (tests only) sees every non-dead candidate move during open.
var DebugCandidateMove func(s *solver, id int32, cur []int32, i int, b, t int32, st uint32, cmdMask, unpinMask uint64)

// Novelty (exp-192): at most this many novel moves per opened node cost
// nothing (0: off), for FESS run alone (-strategy macro). In the portfolio
// the macro lane keeps plain FESS (the pull lane's xsokoban wins meet its
// tree) and a separate novelty lane runs FESS with every novel move free.
var Novelty = 0

// AccessNovelty (exp-289): access-aware capability novelty in FESS (push-side, passage, displacement).
var AccessNovelty = true

// RevGroups (exp-290): reversible preparation groups and exit prioritization.
var RevGroups = true

// checkRooms (tests): verify incremental room connectivity on every candidate.
var checkRooms = false

// LazyCluster (exp-199): the cluster deadlock search (a third of FESS's time
// when run on every candidate) runs only when a move is applied; FESS applies
// a few percent of the moves it generates.
var LazyCluster = true

// Novelty2 (exp-194): novelty of width 2 (new box-square pairs in the cell).
var Novelty2 = true

// NoveltyLane: the portfolio's extra forward FESS lane with novelty.
var NoveltyLane = true

// MoveWindow (exp-165; 0: store all) bounds the moves stored per opened
// node. Measured: only 2-9% of stored moves are ever applied, mostly the
// first four of a node.
var MoveWindow = 8

// Search integrity and exploration invariants (exp-286).
var FixInvariants = true
var StableWindows = true
var TransactionalOpen = true
var RootBareScore = true

// PullDeadlocks enables pull-side deadlock checks in pull mode (exp-183).
var PullDeadlocks = true

// Why a candidate push is pruned (alive: generated).
type deadReason uint8

const (
	alive deadReason = iota
	deadPullFreeze
	dead2x2
	deadFreeze
	deadGoalArea
	deadPair
	deadSide
	deadInefficient
	deadPairSide
	deadCluster
	deadCorridorFlow
	deadDiag
	deadFrozenHall
)

var deadNames = [...]string{"alive", "pull-freeze", "2x2", "freeze", "goal-area", "pair", "side-dead", "inefficient", "pair-side", "cluster", "corridor-flow", "diag", "frozen-hall"}

func (r deadReason) String() string { return deadNames[r] }

// pushDead applies the pruning rules to the box cur[i] pushed to t (s.occ
// holds the child: cur[i] off, t on; s.mark3 == s.pstamp is the player's
// region after the push; canonical is the region's first side of t). Pull
// mode: the dead-square table (pull distance to a start square) already
// filtered t; the push deadlock family does not apply.
func (s *solver) pushDead(cur []int32, i int, t, k, canonical int32) deadReason {
	side := t - s.dirs[canonical]
	inRegion := s.mark3[side] == s.pstamp
	switch {
	case s.pull:
		if PullDeadlocks && s.pullFreezeDead(t) {
			return deadPullFreeze
		}
		if (PFIneff && s.pfOn || DiveIneff && s.dive) && s.pfInefficient(t, cur[i]) {
			return deadInefficient // exp-696
		}
		return alive
	case s.frozen2x2(t):
		return dead2x2
	case s.freezeDeadlock(t):
		return deadFreeze
	case s.goalAreaDeadlock(t):
		return deadGoalArea
	case s.pairDeadlock(t):
		return deadPair
	case SideDead && s.sideDeadAfter(cur, i, t, canonical):
		return deadSide
	case s.ineffOn && s.inefficient(t, k/4):
		return deadInefficient
	case FestIneff && s.hfRank && s.festInefficient(t):
		return deadInefficient
	case s.dd != nil && (DiagDead == diagAll || s.hfRank) && s.dd.Dead(s.occ, t, side):
		return deadDiag
	case s.pairSideOn && inRegion && s.pairSideDeadlock(t, side):
		return deadPairSide
	case !LazyCluster && s.cl != nil && inRegion && s.cl.Dead(s.occ, t, side, func(y int32) bool { return s.mark3[y] == s.pstamp }):
		return deadCluster
	case CorridorFlow && s.corridorFlowDeadlock(cur, i, t, canonical):
		return deadCorridorFlow
	case FrozenHall && s.frozenHallDead(cur, i, t, side, inRegion):
		return deadFrozenHall
	}
	return alive
}

// u64set is an open-addressing set of non-zero uint64 keys (exp-199: the
// width-2 novelty pairs; a Go map cost ~5% of FESS's time).
type u64set struct {
	slots []uint64
	n     int
}

// insert adds k (k+1 stored, so 0 marks empty) and reports whether it is new.
func (t *u64set) insert(k uint64) bool {
	k++
	if t.n*4 >= len(t.slots)*3 {
		old := t.slots
		t.slots = make([]uint64, max(1024, 2*len(old)))
		t.n = 0
		for _, v := range old {
			if v != 0 {
				t.put(v)
			}
		}
	}
	return t.put(k)
}

func (t *u64set) put(k uint64) bool {
	mask := uint64(len(t.slots) - 1)
	i := (k * 0x9E3779B97F4A7C15) >> 32 & mask
	for {
		switch t.slots[i] {
		case 0:
			t.slots[i] = k
			t.n++
			return true
		case k:
			return false
		}
		i = (i + 1) & mask
	}
}

func (t *u64set) has(k uint64) bool {
	if len(t.slots) == 0 {
		return false
	}
	k++
	mask := uint64(len(t.slots) - 1)
	i := (k * 0x9E3779B97F4A7C15) >> 32 & mask
	for {
		switch t.slots[i] {
		case 0:
			return false
		case k:
			return true
		}
		i = (i + 1) & mask
	}
}

// noveltyBudget is the number of novel moves per opened node that cost
// nothing: the forward novelty setting (pull searches have none).
func (s *solver) noveltyBudget() int {
	if !s.pull {
		return s.novelty
	}
	return 0
}

// moveKey is one candidate's flattened sort key (exp-638).
// rankedNeed is how many of n sorted moves open() reads (exp-659
// -lazy-child): the ranks up to the end of node id's window.
func (s *solver) rankedNeed(id int32, n int) int {
	if !LazyChild || MoveWindow <= 0 || DebugOpen != nil {
		return n
	}
	need := int(s.mvOff[id]) + MoveWindow
	if need*4 > n {
		return n // a full sort is as cheap
	}
	return need
}

// selectKeys moves the k smallest keys (cmpMoveKey, a total order) to
// keys[:k] in order: a bounded max-heap in keys[:k], then a sort of it.
func selectKeys(keys []moveKey, k int) {
	h := keys[:k]
	down := func(i int) {
		for {
			c := 2*i + 1
			if c >= k {
				return
			}
			if c+1 < k && cmpMoveKey(h[c+1], h[c]) > 0 {
				c++
			}
			if cmpMoveKey(h[c], h[i]) <= 0 {
				return
			}
			h[i], h[c] = h[c], h[i]
			i = c
		}
	}
	for i := k/2 - 1; i >= 0; i-- {
		down(i)
	}
	for j := k; j < len(keys); j++ {
		if cmpMoveKey(keys[j], h[0]) < 0 {
			h[0], keys[j] = keys[j], h[0]
			down(0)
		}
	}
	slices.SortFunc(h, cmpMoveKey)
}

type moveKey struct {
	sc   [6]int
	h    uint64
	w    int32
	x, y int32
	idx  int32
	bad  int8
	tier int8
}

func cmpMoveKey(a, b moveKey) int {
	if a.bad != b.bad {
		return int(a.bad) - int(b.bad)
	}
	if a.w != b.w {
		if a.w < b.w {
			return -1
		}
		return 1
	}
	if a.tier != b.tier {
		return int(a.tier) - int(b.tier)
	}
	for i, v := range a.sc {
		if v != b.sc[i] {
			if v < b.sc[i] {
				return -1
			}
			return 1
		}
	}
	if a.h != b.h {
		if a.h < b.h {
			return -1
		}
		return 1
	}
	if a.x != b.x {
		if a.x < b.x {
			return -1
		}
		return 1
	}
	if a.y != b.y {
		if a.y < b.y {
			return -1
		}
		return 1
	}
	if a.idx < b.idx {
		return -1
	}
	return 1
}
