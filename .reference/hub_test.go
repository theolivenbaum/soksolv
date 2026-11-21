package main

import (
	"testing"
	"time"
)

// hubTestLevel is small enough that the pull search reaches the start and
// big enough to have several pull nodes the forward search can meet.
var hubTestLevel = []string{
	"#########",
	"#       #",
	"# $ $ . #",
	"#@  $ . #",
	"#     . #",
	"#########",
}

// TestHubDispatch: the hub lane is on by default (exp-480) at every T
// (exp-640 removed -hub-from and -hub-prefix).
func TestHubDispatch(t *testing.T) {
	if !hubOn || !hubGenReach || !hubRegRel || hubGenCap != 0.75 {
		t.Fatal("exp-480: hub lane defaults changed")
	}
	if u := hubUnit(90 * time.Second); u != 7500*time.Millisecond {
		t.Errorf("U at T=90 = %v, want 7.5s", u)
	}
	if u := hubUnit(600 * time.Second); u != 30*time.Second {
		t.Errorf("U at T=600 = %v, want 30s", u)
	}
	if u := hubUnit(0); u != 10*time.Second {
		t.Errorf("U at T=0 = %v, want 10s", u)
	}
}
