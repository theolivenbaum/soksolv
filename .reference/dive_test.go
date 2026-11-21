package main

import (
	"os"
	"testing"
	"time"

	"soksolv/verify"
)

// exp-691: the dive's activation peel depth matches the ledger's maxpeel.
func TestDiveMaxPeel(t *testing.T) {
	for _, c := range []struct {
		file string
		idx  int
		want int
	}{
		{"levels/Large Test Suite/Sven_1623.xsb", 196, 50},
		{"levels/Large Test Suite/Sven_1623.xsb", 209, 23},
		{"levels/Large Test Suite/Sven_1623.xsb", 829, 11},
		{"levels/Large Test Suite/Sven_1623.xsb", 1187, 10},
		{"levels/Open Test Suite/Life.xsb", 61, 25},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Skip("levels not available:", err)
		}
		lv, err := parseLevel(splitCollection(data)[c.idx-1])
		if err != nil {
			t.Fatal(err)
		}
		if got := diveMaxPeel(lv); got != c.want {
			t.Errorf("%s #%d: maxpeel %d, want %d", c.file, c.idx, got, c.want)
		}
	}
}

// exp-691: -strategy dive solves Sven_1623 #196 (a buried-hole spiral) in a
// few thousand expansions, with a verified solution.
func TestDiveSolvesSven196(t *testing.T) {
	const file = "levels/Large Test Suite/Sven_1623.xsb"
	data, err := os.ReadFile(file)
	if err != nil {
		t.Skip("levels not available:", err)
	}
	lv, err := parseLevel(splitCollection(data)[195])
	if err != nil {
		t.Fatal(err)
	}
	old := NodeBudget
	NodeBudget = 8000
	defer func() { NodeBudget = old }()
	s := newPullSolver(lv, time.Now().Add(time.Minute))
	s.dive = true
	res := s.solve(time.Now().Add(time.Minute), 4<<30)
	if !res.Solved {
		t.Fatalf("not solved: %s after %d", res.Reason, res.Nodes)
	}
	if err := verify.Check(verify.SplitLevels(data)[195], res.Solution); err != nil {
		t.Fatal(err)
	}
}
