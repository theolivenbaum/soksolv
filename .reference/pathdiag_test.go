package main

import "testing"

func TestPathdiagParse(t *testing.T) {
	lv, err := parseLevel([]string{"#######", "#@ $ .#", "#######"})
	if err != nil {
		t.Fatal(err)
	}
	// RR walks to the box; the pushes rr move it two squares: one macro of 2 pushes.
	p := pdParse(lv, "rRR")
	if len(p.moves) != 1 || p.pushes[0] != 2 || len(p.states) != 2 {
		t.Fatalf("parse: %+v", p)
	}
	if p.moves[0][1]-p.moves[0][0] != 2 {
		t.Fatalf("macro %v", p.moves[0])
	}
}
