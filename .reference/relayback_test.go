package main

import (
	"slices"
	"testing"
	"time"

	"soksolv/verify"
)

// exp-761: a backward post from a pull tree tip composes with a forward
// solution of its child level (start -> goals = B) into a verified solution.
func TestRelayBackCompose(t *testing.T) {
	rows := []string{
		"#########",
		"#@ $  . #",
		"#  $  . #",
		"#  $  . #",
		"#########",
	}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	oldT, oldB := RelayTrial, RelayBack
	RelayTrial, RelayBack = true, true
	defer func() { RelayTrial, RelayBack = oldT, oldB; relayReset(nil, nil) }()
	relayReset(lv, rows)
	ps := newPullSolver(lv, time.Now().Add(5*time.Second))
	ps.pfOn, ps.pfSty = true, festXStyle()
	ps.nodeLimit = 200
	ps.solve(time.Now().Add(5*time.Second), 1<<30)
	rb := newRelayBack(ps, lv)
	if rb == nil {
		t.Fatal("no back state")
	}
	rb.next = 1
	rb.step(ps, false)
	var b *rootBoard
	for _, x := range relay.boards {
		if x.back {
			b = x
		}
	}
	if b == nil {
		t.Fatal("nothing posted")
	}
	clv, err := parseLevel(b.rows)
	if err != nil || !slices.Equal(clv.Wall, lv.Wall) || clv.Player != lv.Player {
		t.Fatalf("child rows: %v %v", err, b.rows)
	}
	for _, c := range b.boxes {
		if !clv.Goal[c] {
			t.Fatalf("goal %d missing", c)
		}
	}
	// The child: solve start -> B ending in the tip's region.
	relayEnd, relayEndLv = int(b.player), clv
	defer func() { relayEnd, relayEndLv = -1, nil }()
	cs := newSolver(clv)
	cs.rows = b.rows
	r := cs.solve(time.Now().Add(10*time.Second), 1<<30)
	if !r.Solved {
		t.Fatalf("child unsolved: %s", r.Reason)
	}
	relayEnd, relayEndLv = -1, nil
	full, why := relayBackCompose(b, r.Solution)
	if why != "" && why != "back-region" {
		t.Fatalf("compose: %s", why)
	}
	if why == "" && verify.Check(rows, full) != nil {
		t.Fatalf("composed solution fails: %q", full)
	}
	t.Logf("depth %d suffix %d why %q", b.depth, len(b.tail), why)
}
