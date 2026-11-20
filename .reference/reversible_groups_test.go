package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"soksolv/verify"
)

// TestIncrementalBitFlipCorrectness verifies that 2-bit flip on s.revKey
// matches s.pack(child) 100% on >= 1,000 generated state transitions.
func TestIncrementalBitFlipCorrectness(t *testing.T) {
	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Fatal(err)
	}
	levels := splitCollection(data)
	verifiedCount := 0

	for _, rows := range levels {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		s := newSolverUntil(lv, time.Now().Add(2*time.Second))

		cur := slices.Clone(lv.Boxes)
		for _, b := range cur {
			s.occ[b] = true
		}
		p := s.reach(lv.Player, s.mark)
		st := s.stamp

		s.add(cur, p, -1, -1, -1, -1, nil, 0, 0, 0, -1)
		copy(s.revKey, s.boxes[0:s.nw])

		var moves []macroCandidate
		DebugCandidateMove = func(slv *solver, id int32, cur []int32, i int, b, t int32, st uint32, cmdMask, unpinMask uint64) {
			moves = append(moves, macroCandidate{i: int32(i), b: b, t: t})
		}
		s.open(0, cur, 0, st)
		DebugCandidateMove = nil

		childBoxes := make([]int32, len(cur))
		for _, m := range moves {
			// 1. Bit flip on revKey
			fb := s.fidx[m.b]
			ft := s.fidx[m.t]
			if m.b != m.t {
				s.revKey[fb>>6] &^= 1 << (fb & 63)
				s.revKey[ft>>6] |= 1 << (ft & 63)
			}

			// 2. Exact child boxes pack
			copy(childBoxes, cur)
			childBoxes[m.i] = m.t
			slices.Sort(childBoxes)
			expectedKey := slices.Clone(s.pack(childBoxes))

			if !slices.Equal(s.revKey, expectedKey) {
				t.Fatalf("bit-flip mismatch for box %d: %d -> %d\n  got:  %016x\n  want: %016x",
					m.i, m.b, m.t, s.revKey, expectedKey)
			}
			verifiedCount++

			// Restore revKey
			if m.b != m.t {
				s.revKey[ft>>6] &^= 1 << (ft & 63)
				s.revKey[fb>>6] |= 1 << (fb & 63)
			}
			if !slices.Equal(s.revKey, s.boxes[0:s.nw]) {
				t.Fatalf("restoration mismatch: got %016x, want %016x", s.revKey, s.boxes[0:s.nw])
			}
		}

		if verifiedCount >= 1000 {
			break
		}
	}

	t.Logf("Verified %d/1,000 state bit-flips against s.pack(child): 100.0%% match", verifiedCount)
	if verifiedCount < 1000 {
		t.Fatalf("only verified %d states, expected >= 1000", verifiedCount)
	}
}

// TestReversibleGroupSolutionVerification verifies that RevGroups=true preserves
// 100% solution soundness on solvable levels with zero verifier failures.
func TestReversibleGroupSolutionVerification(t *testing.T) {
	savedRev := RevGroups
	RevGroups = true
	defer func() { RevGroups = savedRev }()

	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Fatal(err)
	}
	levels := splitCollection(data)
	solvedCount := 0

	for i, rows := range levels[:20] {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(1 * time.Second)
		s := newSolverUntil(lv, deadline)
		s.nodeLimit = 2000
		s.rows = rows
		res := s.solve(deadline, hugeMem)

		if res.Solved {
			solvedCount++
			if err := verify.Check(rows, res.Solution); err != nil {
				t.Fatalf("Level %d solution does not verify under RevGroups=true: %v", i+1, err)
			}
		}
	}
	t.Logf("Solved and verified %d/20 Microban levels with RevGroups=true (0 verifier failures)", solvedCount)
	if solvedCount < 10 {
		t.Fatalf("expected >= 10 solves, got %d", solvedCount)
	}
}

// TestReversibleGroupsDiagnosticAB runs the 18 diagnostic levels A/B comparing
// RevGroups=false (Baseline) vs RevGroups=true (exp-290).
func TestReversibleGroupsDiagnosticAB(t *testing.T) {
	if os.Getenv("REVERSIBLE_BENCH") == "" {
		t.Skip("skipping slow diagnostic A/B; set REVERSIBLE_BENCH=1 to run")
	}
	savedRev := RevGroups
	defer func() {
		RevGroups = savedRev
		DebugCandidateMove = nil
		DebugExpand = nil
		DebugApplied = nil
		DebugOpen = nil
	}()

	type abResult struct {
		label    string
		mode     string
		baseSol  bool
		baseMs   int64
		baseExp  int64
		baseDups int
		expSol   bool
		expMs    int64
		expExp   int64
		expDups  int
	}

	var results []abResult
	modes := []string{"macro", "novelty"}

	for _, mode := range modes {
		for _, item := range censusLevels {
			data, err := os.ReadFile(item.file)
			if err != nil {
				t.Fatalf("failed reading %s: %v", item.file, err)
			}
			levels := splitCollection(data)
			rows := levels[item.index-1]
			lv, err := parseLevel(rows)
			if err != nil {
				t.Fatalf("failed parsing %s #%d: %v", item.file, item.index, err)
			}

			// 1. Run Baseline (RevGroups = false)
			RevGroups = false
			deadline1 := time.Now().Add(1200 * time.Millisecond)
			s1 := newSolverUntil(lv, deadline1)
			s1.nodeLimit = 3000
			s1.rows = rows
			if mode == "novelty" {
				s1.novelty = 100
			} else {
				s1.novelty = 0
			}
			t1 := time.Now()
			res1 := s1.solve(deadline1, hugeMem)
			dur1 := time.Since(t1)

			// 2. Run Experiment (RevGroups = true)
			RevGroups = true
			deadline2 := time.Now().Add(1200 * time.Millisecond)
			s2 := newSolverUntil(lv, deadline2)
			s2.nodeLimit = 3000
			s2.rows = rows
			if mode == "novelty" {
				s2.novelty = 100
			} else {
				s2.novelty = 0
			}
			t2 := time.Now()
			res2 := s2.solve(deadline2, hugeMem)
			dur2 := time.Since(t2)

			if res2.Solved {
				if err := verify.Check(rows, res2.Solution); err != nil {
					t.Fatalf("%s (%s): solution does not verify under RevGroups=true: %v", item.label, mode, err)
				}
			}

			results = append(results, abResult{
				label:    item.label,
				mode:     mode,
				baseSol:  res1.Solved,
				baseMs:   dur1.Milliseconds(),
				baseExp:  s1.expandedN,
				baseDups: s1.stats.Duplicates,
				expSol:   res2.Solved,
				expMs:    dur2.Milliseconds(),
				expExp:   s2.expandedN,
				expDups:  s2.stats.Duplicates,
			})
		}
	}

	// Print comparison table
	fmt.Println()
	fmt.Println(strings.Repeat("=", 120))
	fmt.Println("             REVERSIBLE PREPARATION GROUPS (exp-290) A/B SCREEN (18 Diagnostic Levels)")
	fmt.Println(strings.Repeat("=", 120))
	fmt.Printf("%-22s %-5s | %-7s %-6s %-6s | %-7s %-6s %-6s | %-9s %-9s\n",
		"Level", "Mode", "BaseSol", "BaseExp", "BaseDup", "ExpSol", "ExpExp", "ExpDup", "DupDrop", "ExpChange")
	fmt.Println(strings.Repeat("-", 120))

	var totBaseExp, totExpExp int64
	var totBaseDup, totExpDup int
	var totBaseSol, totExpSol int

	for _, r := range results {
		baseStatus := "FAIL"
		if r.baseSol {
			baseStatus = fmt.Sprintf("%dms", r.baseMs)
			totBaseSol++
		}
		expStatus := "FAIL"
		if r.expSol {
			expStatus = fmt.Sprintf("%dms", r.expMs)
			totExpSol++
		}

		totBaseExp += r.baseExp
		totExpExp += r.expExp
		totBaseDup += r.baseDups
		totExpDup += r.expDups

		dupDelta := ""
		if r.baseDups > 0 {
			pct := float64(r.baseDups-r.expDups) / float64(r.baseDups) * 100.0
			dupDelta = fmt.Sprintf("%+5.1f%%", -pct)
		} else {
			dupDelta = "  0.0%"
		}

		expDelta := ""
		if r.baseExp > 0 {
			pct := float64(r.expExp-r.baseExp) / float64(r.baseExp) * 100.0
			expDelta = fmt.Sprintf("%+5.1f%%", pct)
		}

		fmt.Printf("%-22s %-5s | %-7s %6d %6d | %-7s %6d %6d | %9s %9s\n",
			r.label, r.mode,
			baseStatus, r.baseExp, r.baseDups,
			expStatus, r.expExp, r.expDups,
			dupDelta, expDelta)
	}
	fmt.Println(strings.Repeat("-", 120))
	totDupDrop := 0.0
	if totBaseDup > 0 {
		totDupDrop = float64(totBaseDup-totExpDup) / float64(totBaseDup) * 100.0
	}
	totExpChange := 0.0
	if totBaseExp > 0 {
		totExpChange = float64(totExpExp-totBaseExp) / float64(totBaseExp) * 100.0
	}
	fmt.Printf("%-22s %-5s | %4d/36 %6d %6d | %4d/36 %6d %6d | %+8.1f%% %+8.1f%%\n",
		"TOTAL / SUMMARY", "",
		totBaseSol, totBaseExp, totBaseDup,
		totExpSol, totExpExp, totExpDup,
		-totDupDrop, totExpChange)
	fmt.Println(strings.Repeat("=", 120))
}

func TestHolland37Verification(t *testing.T) {
	data, err := os.ReadFile("levels/Large Test Suite/Holland_81.xsb")
	if err != nil {
		t.Fatal(err)
	}
	rows := splitCollection(data)[36] // level 37
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	withNodeBudget(t, 400000) // deterministic bound; was a 4 s wall deadline
	dl := time.Now().Add(time.Minute)
	s := newSolverUntil(lv, dl)
	s.rows = rows
	savedRev := RevGroups
	RevGroups = true
	defer func() { RevGroups = savedRev }()
	res := s.solve(dl, hugeMem)
	if !res.Solved {
		t.Fatalf("Holland 37 failed under RevGroups=true: nodes=%d", res.Nodes)
	}
	t.Logf("Holland 37 solved under RevGroups=true in %d nodes", res.Nodes)
}

// TestSearchInvariantsRevGroupsWindows verifies that MoveWindow reopenings under
// RevGroups = true produce 100% bit-exact move partitions (0 missing, 0 duplicate
// candidate moves across windows) preserving the StableWindows invariant.
func TestSearchInvariantsRevGroupsWindows(t *testing.T) {
	savedRev := RevGroups
	RevGroups = true
	defer func() {
		RevGroups = savedRev
		DebugOpen = nil
	}()

	for _, nov := range []int{0, 100} {
		total, missing, duplicates, boards := 0, 0, 0, 0
		for _, file := range []string{"Microban_155.xsb", "XSokoban_90.xsb"} {
			data, err := os.ReadFile("levels/Large Test Suite/" + file)
			if err != nil {
				t.Fatal(err)
			}
			for _, rows := range splitCollection(data)[:30] {
				lv, err := parseLevel(rows)
				if err != nil {
					t.Fatal(err)
				}
				s := newSolverUntil(lv, time.Now().Add(2*time.Second))
				s.novelty = nov
				s.pairOffsets()
				s.buildWalk()
				s.dpair = make([]int8, len(s.lv.Wall)*len(s.poff))
				cur := slices.Clone(lv.Boxes)
				slices.Sort(cur)
				var h int32
				for _, b := range cur {
					s.occ[b] = true
					h += s.dist[b]
				}
				p := s.reach(lv.Player, s.mark)
				st := s.stamp
				id := s.add(cur, p, -1, -1, -1, -1, nil, 0, h, 0, -1)
				type key struct{ i, t, side int32 }
				expected := map[key]bool{}
				got := map[key]int{}
				DebugOpen = func(_ *solver, _ int32, _ []int32, ms []macroCandidate, _ []int32, _ []int) {
					for _, m := range ms {
						expected[key{m.i, m.t, m.side}] = true
					}
				}
				s.open(id, cur, h, st)
				DebugOpen = nil
				for round := 0; round < 100; round++ {
					for k := s.mvStart[id]; k < s.mvStart[id]+int32(s.mvLen[id]); k++ {
						i, to, _, _ := decodeMove(s.mv[k])
						got[key{i, to, moveSide(s.mv[k])}]++
					}
					if s.mvOff[id]+s.mvLen[id] >= s.mvTotal[id] || s.mvLen[id] == 0 {
						break
					}
					s.mvOff[id] += s.mvLen[id]
					s.reach(p, s.mark)
					s.open(id, cur, h, s.stamp)
				}
				bad := false
				for k := range expected {
					total++
					if got[k] == 0 {
						missing++
						bad = true
					}
				}
				for _, n := range got {
					if n > 1 {
						duplicates += n - 1
						bad = true
					}
				}
				if bad {
					boards++
				}
			}
		}
		t.Logf("RevGroups=true, novelty=%d: missing=%d/%d duplicates=%d affected=%d/60", nov, missing, total, duplicates, boards)
		if missing > 0 || duplicates > 0 {
			t.Fatalf("RevGroups=true violated StableWindows invariant: %d missing, %d duplicates across %d boards", missing, duplicates, boards)
		}
	}
}
