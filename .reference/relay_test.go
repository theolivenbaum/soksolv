package main

import (
	"slices"
	"testing"

	"soksolv/verify"
	"time"
)

// exp-744: a posted board's rows parse to the same walls and goals with the
// board's boxes and player, and relayFix turns an X-relative solution into a
// solution of the original level.
func TestRelayRootRows(t *testing.T) {
	rows := []string{
		"########",
		"#@$  .##",
		"#  $ . #",
		"########",
	}
	lv, err := parseLevelRaw(rows)
	if err != nil {
		t.Fatal(err)
	}
	old := ReviveRoot
	ReviveRoot = true
	defer func() { ReviveRoot = old; relayReset(nil, nil) }()
	relayReset(lv, rows)
	prefix := "R" // the first box one cell right
	box := slices.Clone(lv.Boxes)
	for i, b := range box {
		if b == lv.Player+1 {
			box[i] = b + 1
		}
	}
	player := lv.Player + 1
	relayPost(box, player, prefix, "macro", 0, nil)
	if relay.n.Load() != 1 {
		t.Fatalf("not posted: %d", relay.n.Load())
	}
	b := relayPick("pull", 1)
	if b == nil || relayPick("pull", 0) != nil {
		t.Fatal("pick")
	}
	lv2, err := parseLevelRaw(b.rows)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lv2.Wall, lv.Wall) || !slices.Equal(lv2.Goal, lv.Goal) || lv2.Player != player || !slices.Equal(lv2.Boxes, b.boxes) {
		t.Fatalf("rows differ: %v", b.rows)
	}
	if relayPrefixFor(lv2) != prefix || relayPrefixFor(lv) != "" {
		t.Fatal("prefix lookup")
	}
	// A solution from X: solve the rest by brute force via the solver.
	s := newSolver(b.lv)
	s.rows = b.rows
	r := s.solve(time.Now().Add(10*time.Second), 1<<30)
	if !r.Solved || verify.Check(b.rows, r.Solution) != nil {
		t.Fatalf("X not solved: %+v", r.Reason)
	}
	fx := relayFix(r)
	if !fx.Solved || verify.Check(rows, fx.Solution) != nil || fx.Solution != prefix+r.Solution {
		t.Fatalf("fix: %+v", fx)
	}
	if bad := relayFix(Result{Solved: true, Solution: "uuuu"}); bad.Solved || bad.Reason != "reroot-invalid" {
		t.Fatalf("bad kept: %+v", bad)
	}
}
