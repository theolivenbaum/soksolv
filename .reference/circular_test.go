package main

import (
	"strings"
	"testing"

	"soksolv/verify"
)

// Every first push of a circular level is legal and gives the board after it.
func TestFirstPushesCircular(t *testing.T) {
	rows := []string{"######", "#@*  #", "#    #", "######"}
	pushes := firstPushes(rows)
	if len(pushes) != 2 { // push right; walk round and push up
		t.Fatalf("got %d first pushes, want 2", len(pushes))
	}
	for _, fp := range pushes {
		// The prefix is legal and leaves a box off its goal.
		if err := verify.Check(rows, fp.prefix); err == nil || !strings.Contains(err.Error(), "not on a goal") {
			t.Fatalf("push %q: %v", fp.prefix, err)
		}
		if !strings.Contains(strings.Join(fp.rows, "\n"), "$") {
			t.Fatalf("push %q: board after it has no box off a goal", fp.prefix)
		}
	}
}
