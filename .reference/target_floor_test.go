package main

import (
	"reflect"
	"soksolv/meet"
	"soksolv/verify"
	"testing"
	"time"
)

func TestRowsWithGoalsFloorCharacters(t *testing.T) {
	rows := []string{"##########", "#@-$_. * #", "##########"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	cell := func(x int) int32 { return int32(2*lv.W + x + 1) }
	goals := []int32{cell(2), cell(4)}
	got := rowsWithGoals(rows, lv.W, goals)
	want := []string{"##########", "#@.$.  $ #", "##########"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	changed, err := parseLevel(got)
	if err != nil {
		t.Fatal(err)
	}
	if changed.W != lv.W || changed.H != lv.H || changed.Player != lv.Player || !reflect.DeepEqual(changed.Boxes, lv.Boxes) || !reflect.DeepEqual(changed.Wall, lv.Wall) {
		t.Fatal("geometry or occupants changed")
	}
	for i, goal := range changed.Goal {
		expected := false
		for _, g := range goals {
			if int(g) == i {
				expected = true
			}
		}
		if goal != expected {
			t.Fatalf("goal at %d = %v, want %v", i, goal, expected)
		}
	}
}

func TestRowsWithGoalsOccupantsAndLegacyFloor(t *testing.T) {
	rows := []string{"#########", "#+*-$ _ #", "#########"}
	w := len(rows[0]) + 2
	cell := func(x int) int32 { return int32(2*w + x + 1) }
	goals := []int32{cell(1), cell(2), cell(3), cell(4), cell(6)}
	want := []string{"#########", "#+*.* . #", "#########"}
	if got := rowsWithGoals(rows, w, goals); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	TargetFloorFix = false
	defer func() { TargetFloorFix = true }()
	legacy := rowsWithGoals(rows, w, goals)
	if legacy[1][3] != '-' || legacy[1][6] != '_' {
		t.Fatalf("legacy path unexpectedly rewrote alternate floors: %q", legacy)
	}
}

func TestTowardAlternateFloorTarget(t *testing.T) {
	rows := []string{"########", "#@ $- .#", "########"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	square := int32(2*lv.W + 5)
	target := &meet.Target{Boxes: []int32{square}, Tail: []meet.Push{{From: square, Dir: 3}, {From: square + 1, Dir: 3}}}
	savedHF, savedFix := TowardHF, TargetFloorFix
	defer func() { TowardHF, TargetFloorFix = savedHF, savedFix }()
	TowardHF = false
	for _, fix := range []bool{false, true} {
		TargetFloorFix = fix
		sol, res := fessToward(rows, lv, target, time.Now().Add(time.Second), hugeMem, nil)
		if !fix {
			if sol != "" || res.Reason == "" {
				t.Fatalf("legacy unexpectedly reached target: %q %+v", sol, res)
			}
			continue
		}
		if sol == "" {
			t.Fatalf("fixed target failed: %+v", res)
		}
		if err := verify.Check(rows, sol); err != nil {
			t.Fatal(err)
		}
	}
}
