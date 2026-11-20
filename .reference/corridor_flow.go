package main

// CorridorFlow enables corridor net flow and transit capacity obligation pruning (exp-330).
var CorridorFlow = true

type flowCorridor struct {
	cells []int32 // ordered cells c_0, c_1, ..., c_{m-1} in the 1-wide corridor
	goals int32   // number of goals in this corridor
	inP   []bool  // inP[c] == true if c is in corridor

	// Direction along the corridor from cells[0] to cells[1]
	dir int32

	// Adjacent cells at ends:
	// endA: adjacent to cells[0] in component A
	// endB: adjacent to cells[m-1] in component B
	endA int32
	endB int32

	// isCut: true if removing this corridor disconnects endA and endB
	isCut bool
}

type flowManager struct {
	corridors []flowCorridor

	// corrsForCell[c] lists indices of corridors containing c
	corrsForCell [][]int
}

// initCorridorFlow initializes the static flow and corridor structures for the level.
func (s *solver) initCorridorFlow() {
	if s.fm != nil {
		return
	}
	lv := s.lv
	n := len(lv.Wall)
	if s.floor == nil {
		s.regions()
	}
	isFloor := make([]bool, n)
	for _, c := range s.floor {
		isFloor[c] = true
	}

	fm := &flowManager{
		corrsForCell: make([][]int, n),
	}

	visited := make([]bool, n)

	// Detect 1-wide corridors.
	// A 1-wide corridor cell has walls on opposite sides along one axis.
	isCorridor := make([]bool, n)
	axisDir := make([]int32, n)
	w := int32(lv.W)
	for _, c := range s.floor {
		hWall := lv.Wall[c-1] && lv.Wall[c+1]
		vWall := lv.Wall[c-w] && lv.Wall[c+w]
		if hWall && !vWall && isFloor[c-w] && isFloor[c+w] {
			isCorridor[c] = true
			axisDir[c] = w
		} else if vWall && !hWall && isFloor[c-1] && isFloor[c+1] {
			isCorridor[c] = true
			axisDir[c] = 1
		}
	}

	// Trace maximal 1-wide corridor segments.
	seenCorr := make([]bool, n)
	for _, c := range s.floor {
		if !isCorridor[c] || seenCorr[c] {
			continue
		}
		ad := axisDir[c]
		// Trace in -ad direction
		start := c
		for isCorridor[start-ad] && axisDir[start-ad] == ad {
			start -= ad
		}
		// Collect corridor cells from start in +ad direction
		var corrCells []int32
		curr := start
		for isCorridor[curr] && axisDir[curr] == ad {
			seenCorr[curr] = true
			corrCells = append(corrCells, curr)
			curr += ad
		}

		endA := start - ad
		endB := curr
		if !isFloor[endA] || !isFloor[endB] {
			continue
		}

		// Check if removing this corridor disconnects endA and endB.
		clear(visited)
		for _, x := range corrCells {
			visited[x] = true
		}
		q := []int32{endA}
		visited[endA] = true
		isCutCorr := true
		for len(q) > 0 {
			u := q[0]
			q = q[1:]
			if u == endB {
				isCutCorr = false
				break
			}
			for _, d := range s.dirs {
				nxt := u + d
				if isFloor[nxt] && !visited[nxt] {
					visited[nxt] = true
					q = append(q, nxt)
				}
			}
		}

		goals := int32(0)
		inP := make([]bool, n)
		for _, x := range corrCells {
			inP[x] = true
			if lv.Goal[x] {
				goals++
			}
		}

		corrIdx := len(fm.corridors)
		fm.corridors = append(fm.corridors, flowCorridor{
			cells: corrCells,
			goals: goals,
			inP:   inP,
			dir:   ad,
			endA:  endA,
			endB:  endB,
			isCut: isCutCorr,
		})

		for _, x := range corrCells {
			fm.corrsForCell[x] = append(fm.corrsForCell[x], corrIdx)
		}
	}

	s.fm = fm
}

// corridorFlowDeadlock evaluates candidate move t to detect transit capacity deadlocks.
func (s *solver) corridorFlowDeadlock(cur []int32, i int, t, canonical int32) bool {
	if s.fm == nil {
		s.initCorridorFlow()
	}
	fm := s.fm
	if len(fm.corridors) == 0 {
		return false
	}

	lv := s.lv

	// Check 1-wide cut corridors.
	for _, corrIdx := range fm.corrsForCell[t] {
		corr := &fm.corridors[corrIdx]
		if !corr.isCut {
			continue
		}

		k := int32(0)
		firstBox := -1
		lastBox := -1
		for idx, c := range corr.cells {
			if s.occ[c] {
				k++
				if firstBox == -1 {
					firstBox = idx
				}
				lastBox = idx
			}
		}
		if k == 0 {
			continue
		}

		// Case 1: Multiple boxes in 1-wide corridor exceeding corridor goals.
		if k >= 2 && k > corr.goals {
			// Check if player is between firstBox and lastBox.
			playerBetween := false
			for j := firstBox + 1; j < lastBox; j++ {
				if s.mark3[corr.cells[j]] == s.pstamp {
					playerBetween = true
					break
				}
			}
			if !playerBetween {
				return true
			}
		}

		// Case 2: Exactly 1 box on non-goal in 1-wide cut corridor.
		if k == 1 && corr.goals == 0 {
			bIdx := firstBox
			bCell := corr.cells[bIdx]
			if !lv.Goal[bCell] {
				// Check which end the player is on.
				playerAtA := s.mark3[corr.endA] == s.pstamp
				playerAtB := s.mark3[corr.endB] == s.pstamp

				if playerAtA && !playerAtB {
					// Player can only push toward B.
					var nxt int32
					if bIdx+1 < len(corr.cells) {
						nxt = corr.cells[bIdx+1]
					} else {
						nxt = corr.endB
					}
					if s.occ[nxt] || lv.Wall[nxt] {
						return true
					}
				} else if playerAtB && !playerAtA {
					// Player can only push toward A.
					var nxt int32
					if bIdx-1 >= 0 {
						nxt = corr.cells[bIdx-1]
					} else {
						nxt = corr.endA
					}
					if s.occ[nxt] || lv.Wall[nxt] {
						return true
					}
				}
			}
		}
	}

	return false
}
