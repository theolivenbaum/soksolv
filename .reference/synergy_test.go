package main

import (
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"soksolv/meet"
	"soksolv/verify"
)

// TestCleanPerimeterOnlyPublishesLiveUnique verifies that CleanPerimeter publishes
// only post-pushDead, non-reopening, live candidate states and that reconstructRef
// produces valid move sequences matching candidate states without mutating arena state.
func TestCleanPerimeterOnlyPublishesLiveUnique(t *testing.T) {
	CleanPerimeter = true
	DensePerimeter = true

	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip("Microban_155.xsb not found:", err)
	}
	allLevels := verify.SplitLevels(data)

	// Test on 10 levels
	for idx := 1; idx <= 10; idx++ {
		rows := allLevels[idx-1]
		lv, err := parseLevel(rows)
		if err != nil {
			continue
		}

		s := newSolver(lv)
		fwdTable := meet.New(lv.W*lv.H, 16, 6)
		s.fwd = fwdTable

		// Expand a modest number of nodes
		s.solve(time.Now().Add(100*time.Millisecond), hugeMem)

		if len(s.refs) == 0 {
			continue
		}

		// Inspect all published refs
		checkedRefs := 0
		for rIdx := 0; rIdx < len(s.refs); rIdx += 2 {
			k := s.refs[rIdx]
			if k == refEmpty {
				continue
			}
			ref := s.refs[rIdx+1]
			checkedRefs++

			// Test reconstructRef
			prefix := s.reconstructRef(ref)
			if prefix == "" {
				t.Fatalf("level %d: reconstructRef returned empty for ref %x", idx, ref)
			}

			// Verify prefix produces a legal path on level
			p, i, toCell, _ := unpackRef(ref)
			pc := make([]int32, s.n)
			s.unpack(p, pc)
			expectedChild := slices.Clone(pc)
			expectedChild[i] = toCell
			slices.Sort(expectedChild)

			// Replay prefix from start
			curBoxes := slices.Clone(lv.Boxes)
			curPlayer := lv.Player
			for _, ch := range prefix {
				d := -1
				switch ch {
				case 'u', 'U':
					d = 0
				case 'd', 'D':
					d = 1
				case 'l', 'L':
					d = 2
				case 'r', 'R':
					d = 3
				}
				np := curPlayer + s.dirs[d]
				boxIdx := slices.Index(curBoxes, np)
				if boxIdx >= 0 {
					curBoxes[boxIdx] += s.dirs[d]
				}
				curPlayer = np
			}
			slices.Sort(curBoxes)

			if !slices.Equal(curBoxes, expectedChild) {
				t.Fatalf("level %d: reconstructRef box mismatch: got %v, expected %v", idx, curBoxes, expectedChild)
			}
		}

		if checkedRefs == 0 {
			t.Fatalf("level %d: expected at least some published refs", idx)
		}
	}
}

// TestRetainForwardMailResolvesAcrossCompactedTrees verifies that when previous forward
// search trees are compacted and retained in prevTrees, meeting messages sent to the new
// searcher successfully resolve via checkMail and pass verification.
func TestRetainForwardMailResolvesAcrossCompactedTrees(t *testing.T) {
	RetainFwdMail = true
	CleanPerimeter = true

	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Skip("Microban_155.xsb not found:", err)
	}
	allLevels := verify.SplitLevels(data)

	// Level 1: simple solvable level
	rows := allLevels[0]
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatalf("parseLevel: %v", err)
	}

	// 1. Solve completely with backward/full to get a known solution tail
	fullSolver := newSolver(lv)
	fullRes := fullSolver.solve(time.Now().Add(500*time.Millisecond), hugeMem)
	if !fullRes.Solved {
		t.Fatalf("baseline solve failed on level 1")
	}

	// 2. Start tree 1 (simulating first forward attempt)
	fwd1 := newSolver(lv)
	table := meet.New(lv.W*lv.H, 16, 6)
	fwd1.fwd = table

	// Run fwd1 for a tiny slice (e.g. 50ms)
	fwd1.solve(time.Now().Add(50*time.Millisecond), hugeMem)
	if len(fwd1.boxes) == 0 {
		t.Fatalf("fwd1 generated 0 states")
	}

	// Pick a state reached by fwd1 (state 1 if exists, or state 0)
	testStateID := int32(min(1, len(fwd1.boxes)-1))
	boxes1 := make([]int32, fwd1.n)
	fwd1.unpack(testStateID, boxes1)
	player1 := fwd1.player[testStateID]
	prefix1 := fwd1.reconstruct(testStateID)
	at1 := meet.EndCell(prefix1, lv.Player, lv.W)

	// Find tail from boxes1, at1 to goals
	tailLv := *lv
	tailLv.Boxes = slices.Clone(boxes1)
	tailLv.Player = at1
	tailSolver := newSolver(&tailLv)
	tailRes := tailSolver.solve(time.Now().Add(500*time.Millisecond), hugeMem)
	if !tailRes.Solved {
		t.Fatalf("failed to solve tail from state %d", testStateID)
	}

	// Convert tail solution to pushes
	tailPushes := parseSolutionToPushes(&tailLv, tailRes.Solution)

	// 3. Compact fwd1
	fwd1.compactForMail()
	if len(fwd1.frontier.cells) != 0 || len(fwd1.frontier.queues) != 0 {
		t.Fatalf("expected compacted fwd1 frontier to be cleared")
	}

	// 4. Start tree 2 (simulating new forward attempt after retarget)
	fwd2 := newSolver(lv)
	fwd2.fwd = table
	fwd2.addPrevTree(fwd1)

	if len(fwd2.prevTrees) != 1 {
		t.Fatalf("expected 1 prevTree, got %d", len(fwd2.prevTrees))
	}

	// 5. Post meeting matching state from fwd1 into table.Mail[fwd2.owner]
	m := meet.Meeting{
		Boxes:  slices.Clone(boxes1),
		Player: player1,
		Tail:   tailPushes,
	}
	table.Mail[fwd2.owner] <- m

	// 6. Check mail on fwd2
	sol, ok := fwd2.checkMail()
	if !ok {
		t.Fatalf("fwd2.checkMail() failed to find meeting in prevTrees")
	}

	// 7. Verify full solution
	if err := verify.Check(rows, sol); err != nil {
		t.Fatalf("solution resolved via prevTrees failed verification: %v", err)
	}
}

func parseSolutionToPushes(lv *Level, sol string) []meet.Push {
	var pushes []meet.Push
	curBoxes := slices.Clone(lv.Boxes)
	curPlayer := lv.Player
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}

	for _, ch := range sol {
		d := -1
		switch ch {
		case 'u', 'U':
			d = 0
		case 'd', 'D':
			d = 1
		case 'l', 'L':
			d = 2
		case 'r', 'R':
			d = 3
		}
		np := curPlayer + dirs[d]
		boxIdx := slices.Index(curBoxes, np)
		if boxIdx >= 0 {
			pushes = append(pushes, meet.Push{From: np, Dir: int8(d)})
			curBoxes[boxIdx] += dirs[d]
		}
		curPlayer = np
	}
	return pushes
}

// Candidate meetings must not insert unqueued search nodes or rewrite existing
// nodes. A candidate can be used as a path without becoming search work.
func TestCandidateMeetingDoesNotMutateFrontier(t *testing.T) {
	oldTable := meet.Fwd.Load()
	defer meet.Fwd.Store(oldTable)
	oldRetain := RetainFwdMail
	defer func() { RetainFwdMail = oldRetain }()
	data, err := os.ReadFile("levels/Large Test Suite/Microban_155.xsb")
	if err != nil {
		t.Fatal(err)
	}
	var s *solver
	var ref uint64
	var boxes []int32
	var np int32
	for _, rows := range verify.SplitLevels(data) {
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		trial := newSolver(lv)
		meet.Fwd.Store(meet.New(lv.W*lv.H, 16, 6))
		trial.solve(time.Now().Add(20*time.Millisecond), hugeMem)
		for j := 0; j < len(trial.refs); j += 2 {
			if trial.refs[j] == refEmpty {
				continue
			}
			r := trial.refs[j+1]
			p, i, to, _ := unpackRef(r)
			b := make([]int32, trial.n)
			trial.unpack(p, b)
			b[i] = to
			prefix := trial.reconstructRef(r)
			if prefix == "" {
				continue
			}
			at := meet.EndCell(prefix, lv.Player, lv.W)
			for _, x := range b {
				trial.occ[x] = true
			}
			trial.reach(at, trial.mark)
			player := trial.queue[0]
			for _, x := range trial.queue[1:] {
				player = min(player, x)
			}
			for _, x := range b {
				trial.occ[x] = false
			}
			if _, id, _ := trial.find(trial.pack(b), player); id < 0 {
				s = trial
				ref = r
				boxes = b
				np = player
				break
			}
		}
		if s != nil {
			break
		}
	}
	if s == nil {
		t.Fatal("fixture needs a published but unexpanded candidate")
	}
	s.back = meet.New(s.lv.W*s.lv.H, 16, 1)
	key := s.fwd.Key(boxes, np)
	// Keep the fixture reference explicit even if another equivalent key won.
	s.refs = nil
	s.refN = 0
	s.refPut(key, ref)
	beforeNodes := len(s.player)
	beforeParents := slices.Clone(s.parent)
	beforeTable := slices.Clone(s.table)
	s.pendCands = []pendingCand{{k: key, np: np}}
	s.postMeetings(make([]int32, s.n))
	m, ok := s.back.Receive(0)
	if !ok || m.Prefix == "" || m.Player != np || m.From != "macro" || m.At != meet.EndCell(m.Prefix, s.lv.Player, s.lv.W) {
		t.Fatalf("bad outgoing meeting: %+v", m)
	}
	slices.Sort(m.Boxes)
	slices.Sort(boxes)
	if !slices.Equal(m.Boxes, boxes) {
		t.Fatalf("outgoing boxes %v want %v", m.Boxes, boxes)
	}
	if len(s.player) != beforeNodes || !slices.Equal(s.parent, beforeParents) || !slices.Equal(s.table, beforeTable) {
		t.Fatal("posting changed search arena")
	}
	for _, retain := range []bool{false, true} {
		RetainFwdMail = retain
		s.fwd.Mail[s.owner] <- meet.Meeting{Boxes: slices.Clone(boxes), Player: np}
		prefix, ok := s.checkMail()
		if !ok || prefix != m.Prefix {
			t.Fatalf("retain=%v: bad incoming prefix %q", retain, prefix)
		}
		if len(s.player) != beforeNodes || !slices.Equal(s.parent, beforeParents) || !slices.Equal(s.table, beforeTable) {
			t.Fatalf("retain=%v: receiving changed search arena", retain)
		}
	}
}
func TestRetainedMailTreesOwnOnlyDirectCappedList(t *testing.T) {
	var trees []*solver
	all := make([]*solver, 8)
	for i := range all {
		all[i] = &solver{prevTrees: slices.Clone(trees)}
		trees = appendPrevTree(trees, all[i])
		if all[i].prevTrees != nil {
			t.Fatal("compacted tree retained predecessor list")
		}
		if len(trees) > 4 {
			t.Fatal("tree cap exceeded")
		}
		for _, p := range trees[len(trees):cap(trees)] {
			if p != nil {
				t.Fatal("evicted tree remains in backing array")
			}
		}
	}
	if !slices.Equal(trees, all[4:]) {
		t.Fatal("incorrect retained generations")
	}
}

func TestAddPrevTreeClearsBudgetEvictions(t *testing.T) {
	var budget atomic.Uint64
	budget.Store(1)
	lv, err := parseLevel([]string{"#####", "#@$.#", "#####"})
	if err != nil {
		t.Fatal(err)
	}
	s := newSolver(lv)
	s.budgetP = &budget
	for range 5 {
		prev := &solver{boxes: make([]uint64, 1<<17)}
		s.addPrevTree(prev)
	}
	if len(s.prevTrees) != 1 {
		t.Fatalf("got %d trees, want 1", len(s.prevTrees))
	}
	for _, p := range s.prevTrees[len(s.prevTrees):cap(s.prevTrees)] {
		if p != nil {
			t.Fatal("budget-evicted tree remains in backing array")
		}
	}
}
