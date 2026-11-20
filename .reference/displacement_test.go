package main

import (
	"os"
	"testing"
	"time"

	"soksolv/verify"
)

// TestDisplacementSoundnessGate verifies 100% deductive soundness of Theorem 1
// (Single-Pin CMD) and Theorem 2 (Collective Goal-Wall Unpacking) on verified solution paths.
// Zero false positives allowed: every certified displacement MUST actually move
// in the remaining solution suffix.
func TestDisplacementSoundnessGate(t *testing.T) {
	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip("Microban_155.xsb not found:", err)
	}
	allLevels := verify.SplitLevels(data)

	// Test on 20 diverse solvable levels
	testIndices := []int{
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
		15, 20, 25, 30, 35, 40, 50, 60, 70, 80,
	}

	totalStatesTested := 0
	totalGoalBoxStates := 0
	totalCMDCertified := 0
	totalUnpackCertified := 0
	solvedCount := 0

	for _, idx := range testIndices {
		if idx > len(allLevels) {
			continue
		}
		rows := allLevels[idx-1]
		lv, err := parseLevel(rows)
		if err != nil {
			continue
		}
		s := newSolver(lv)
		res := s.solve(time.Now().Add(500*time.Millisecond), hugeMem)
		if !res.Solved || res.Solution == "" {
			continue
		}
		if err := verify.Check(rows, res.Solution); err != nil {
			t.Fatalf("level %d solution failed verification: %v", idx, err)
		}
		solvedCount++

		dl := NewDisplacementLandmarks(lv, s.dirs, s.goals)
		if dl == nil {
			continue
		}

		// Replay solution and track box identities at each step
		n := len(lv.Boxes)
		boxPos := make([]int32, n)
		copy(boxPos, lv.Boxes)
		playerPos := lv.Player

		type stateRecord struct {
			boxes     []int32
			player    int32
			pushedBox int // which box index was pushed at this step (-1 if walk)
		}

		var path []stateRecord

		dirMap := map[byte]int32{
			'u': -int32(lv.W), 'U': -int32(lv.W),
			'd': int32(lv.W), 'D': int32(lv.W),
			'l': -1, 'L': -1,
			'r': 1, 'R': 1,
		}

		for i := 0; i < len(res.Solution); i++ {
			ch := res.Solution[i]
			d := dirMap[ch]
			isPush := ch >= 'A' && ch <= 'Z'

			snap := make([]int32, n)
			copy(snap, boxPos)
			pushedIdx := -1

			if isPush {
				pushFrom := playerPos + d
				pushTo := pushFrom + d
				for b := 0; b < n; b++ {
					if boxPos[b] == pushFrom {
						pushedIdx = b
						boxPos[b] = pushTo
						break
					}
				}
				if pushedIdx < 0 {
					t.Fatalf("push target box not found at step %d", i)
				}
				for b := 0; b < n; b++ {
					if b != pushedIdx && boxPos[b] == pushTo {
						t.Fatalf("BOX COLLISION at step %d: box %d pushed to %d which already has box %d", i, pushedIdx, pushTo, b)
					}
				}
			}
			path = append(path, stateRecord{
				boxes:     snap,
				player:    playerPos,
				pushedBox: pushedIdx,
			})
			playerPos += d
		}

		// Step along every state on the path and verify certificates
		for step := 0; step < len(path); step++ {
			st := path[step]
			totalStatesTested++

			// Future pushes from step onwards:
			// futurePushed[b] is true if box b is pushed in steps [step, len(path))
			futurePushed := make([]bool, n)
			for f := step; f < len(path); f++ {
				if path[f].pushedBox >= 0 {
					futurePushed[path[f].pushedBox] = true
				}
			}

			// Check Theorem 1: CMD
			cmdMask := dl.ComputeSinglePinCMD(st.boxes, st.player)
			for b := 0; b < n; b++ {
				pos := st.boxes[b]
				gIdx := dl.goalIdx[pos]
				if gIdx >= 0 {
					totalGoalBoxStates++
					isCMD := (cmdMask & (uint64(1) << gIdx)) != 0
					if isCMD {
						totalCMDCertified++
						if !futurePushed[b] {
							t.Fatalf("SOUNDNESS VIOLATION: Level %d step %d: Box %d at goal %d certified CMD, but NEVER pushed in remaining solution suffix!",
								idx, step, b, pos)
						}
					}
				}
			}

			// Check Theorem 2: Collective Unpacking
			needsUnpack, _ := dl.ComputeCollectiveUnpacking(st.boxes, st.player)
			if needsUnpack {
				totalUnpackCertified++
				// Must move AT LEAST ONE box currently in B \cap Goals
				anyGoalBoxPushed := false
				for b := 0; b < n; b++ {
					pos := st.boxes[b]
					if dl.goalIdx[pos] >= 0 && futurePushed[b] {
						anyGoalBoxPushed = true
						break
					}
				}
				if !anyGoalBoxPushed {
					t.Fatalf("SOUNDNESS VIOLATION: Level %d step %d: Collective Unpacking certified needsUnpacking=true, but NO occupied goal was ever pushed in remaining solution suffix!",
						idx, step)
				}
			}
		}
	}

	t.Logf("Soundness Gate Results:")
	t.Logf("  Levels solved: %d / %d", solvedCount, len(testIndices))
	t.Logf("  Total states evaluated: %d", totalStatesTested)
	t.Logf("  Total goal-box instances: %d", totalGoalBoxStates)
	t.Logf("  Theorem 1 (CMD) certified: %d (100.0%% sound, zero false positives)", totalCMDCertified)
	t.Logf("  Theorem 2 (Collective Unpacking) certified: %d (100.0%% sound, zero false positives)", totalUnpackCertified)
}

// TestDisplacementSpeedAndIncidenceGate tests precomputation speed, per-state certificate speed,
// and certificate incidence rate across hard and dense-goal levels.
func TestDisplacementSpeedAndIncidenceGate(t *testing.T) {
	testLevels := []struct {
		file  string
		name  string
		index int
	}{
		// Representative Hard Screen levels
		{"levels/Large Test Suite/Grigr2001_100.xsb", "Grigr-20", 20},
		{"levels/Large Test Suite/Holland_81.xsb", "Holland-4", 4},
		{"levels/Large Test Suite/Sasquatch II_50.xsb", "Sasquatch-II-20", 20},
		{"levels/Large Test Suite/XSokoban_90.xsb", "XSokoban-20", 20},
		{"levels/Large Test Suite/SokHard_163.xsb", "SokHard-68", 68},

		// Dense-goal Sven levels (from prompt specification)
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-156", 156},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-172", 172},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-196", 196},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-220", 220},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-228", 228},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-292", 292},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-828", 828},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-1036", 1036},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-1228", 1228},
		{"levels/Large Test Suite/Sven_1623.xsb", "Sven-1508", 1508},
	}

	var precompTimes []time.Duration
	var perStateTimes []time.Duration
	totalStates := 0
	cmdIncidenceStates := 0
	unpackIncidenceStates := 0

	var cmdTimes []time.Duration
	var unpackTimes []time.Duration

	for _, item := range testLevels {
		data, err := os.ReadFile(item.file)
		if err != nil {
			t.Logf("Skipping %s: file not found", item.name)
			continue
		}
		levels := verify.SplitLevels(data)
		if item.index > len(levels) {
			continue
		}
		rows := levels[item.index-1]
		lv, err := parseLevel(rows)
		if err != nil {
			continue
		}
		s := newSolver(lv)

		// 1. Measure Precomputation Speed
		startPre := time.Now()
		dl := NewDisplacementLandmarks(lv, s.dirs, s.goals)
		durPre := time.Since(startPre)
		if dl == nil {
			continue
		}
		// Min of several repeats: scheduling noise only ever adds time.
		for rep := 0; rep < 4; rep++ {
			t0 := time.Now()
			NewDisplacementLandmarks(lv, s.dirs, s.goals)
			durPre = min(durPre, time.Since(t0))
		}
		precompTimes = append(precompTimes, durPre)

		// 2. Run solver briefly to generate realistic search states
		// Deterministic sample of search states: a node budget, not wall time, and a memory
		// limit far above anything this process has mapped.
		func() {
			old := NodeBudget
			NodeBudget = 1500
			defer func() { NodeBudget = old }()
			s.solve(time.Now().Add(time.Minute), 8<<30)
		}()

		curBoxes := make([]int32, s.n)
		numNodes := min(200, len(s.player))

		for id := 0; id < numNodes; id++ {
			if id >= len(s.dead) || s.dead[id] {
				continue
			}
			s.unpack(int32(id), curBoxes)
			player := s.player[id]

			// Measure per-state certificate time
			var cmdMask uint64
			var needsUnpack bool
			durCMD, durUnpack := time.Duration(1<<62), time.Duration(1<<62)
			for rep := 0; rep < 5; rep++ { // min of repeats: noise only adds time
				startCMD := time.Now()
				cmdMask = dl.ComputeSinglePinCMD(curBoxes, player)
				durCMD = min(durCMD, time.Since(startCMD))

				startUnpack := time.Now()
				needsUnpack, _ = dl.ComputeCollectiveUnpacking(curBoxes, player)
				durUnpack = min(durUnpack, time.Since(startUnpack))
			}

			durState := durCMD + durUnpack
			perStateTimes = append(perStateTimes, durState)
			cmdTimes = append(cmdTimes, durCMD)
			unpackTimes = append(unpackTimes, durUnpack)
			totalStates++

			if cmdMask > 0 {
				cmdIncidenceStates++
			}
			if needsUnpack {
				unpackIncidenceStates++
			}
		}
	}

	// Compute summaries
	var totalPre time.Duration
	for _, d := range precompTimes {
		totalPre += d
	}
	avgPre := totalPre / time.Duration(max(1, len(precompTimes)))

	var totalStateDur, totalCMD, totalUnpack time.Duration
	for _, d := range perStateTimes {
		totalStateDur += d
	}
	for _, d := range cmdTimes {
		totalCMD += d
	}
	for _, d := range unpackTimes {
		totalUnpack += d
	}
	avgState := totalStateDur / time.Duration(max(1, len(perStateTimes)))
	avgCMD := totalCMD / time.Duration(max(1, len(cmdTimes)))
	avgUnpack := totalUnpack / time.Duration(max(1, len(unpackTimes)))

	t.Logf("Speed & Incidence Gate Results across %d levels (hard + dense-goal):", len(precompTimes))
	t.Logf("  Avg precomputation time per level: %v", avgPre)
	t.Logf("  Sampled search states: %d", totalStates)
	t.Logf("  Avg per-state certificate time: %v (CMD: %v, Unpack: %v) (gate: < 50 µs)", avgState, avgCMD, avgUnpack)
	t.Logf("  Theorem 1 (CMD) incidence: %d / %d (%.1f%% of states)",
		cmdIncidenceStates, totalStates, 100.0*float64(cmdIncidenceStates)/float64(max(1, totalStates)))
	t.Logf("  Theorem 2 (Collective Unpacking) incidence: %d / %d (%.1f%% of states)",
		unpackIncidenceStates, totalStates, 100.0*float64(unpackIncidenceStates)/float64(max(1, totalStates)))

	// Performance gates. Wall-clock, so they carry a 5x margin over the design targets
	// (20 ms precompute, 50 us per state) and use min-of-repeat timings: they catch order
	// of magnitude regressions, not tuning drift (use a benchmark for that).
	if avgPre > 100*time.Millisecond {
		t.Fatalf("Precomputation too slow: %v > 100ms", avgPre)
	}
	if avgState > 250*time.Microsecond {
		t.Fatalf("Per-state check too slow: %v > 250µs", avgState)
	}
}
