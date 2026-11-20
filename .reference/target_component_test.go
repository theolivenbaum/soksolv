package main

import (
	"soksolv/meet"
	"soksolv/verify"
	"sync/atomic"
	"testing"
	"time"
)

func TestTowardContinuesAfterInaccessibleTarget(t *testing.T) {
	rows := []string{"#########", "#   #   #", "#@ $   .#", "#   #   #", "#########"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	// The first push reaches the target boxes, but the tail needs the player
	// on the other side of the box, inaccessible in this corridor.
	targetBox := lv.Boxes[0] + 1
	tgt := &meet.Target{Boxes: []int32{targetBox}, Player: targetBox + 1, Tail: []meet.Push{{From: targetBox, Dir: 2}, {From: targetBox - 1, Dir: 2}, {From: targetBox - 2, Dir: 3}, {From: targetBox - 1, Dir: 3}, {From: targetBox, Dir: 3}, {From: targetBox + 1, Dir: 3}, {From: targetBox + 2, Dir: 3}}}
	tail := meet.Complete(lv.Wall, lv.W, tgt.Boxes, tgt.Player, tgt.Tail)
	targetRows := []string{"#########", "#   #   #", "#   $@ .#", "#   #   #", "#########"}
	if tail == "" || verify.Check(targetRows, tail) != nil {
		t.Fatalf("fixture suffix must solve from target region: %q", tail)
	}

	var b, tr atomic.Uint64
	deadline := time.Now().Add(3 * time.Second)
	s := newTowardReal(rows, lv, tgt, deadline, nil, &b, &tr, 2, 1000)
	sol, res := s.towardRun(deadline, hugeMem)
	if !res.Solved || verify.Check(rows, sol) != nil {
		t.Fatalf("expected actual solution after rejected target; res=%+v sol=%q", res, sol)
	}
}

func TestTowardRootTarget(t *testing.T) {
	rows := []string{"#######", "#@ $. #", "#######"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	tgt := &meet.Target{Boxes: lv.Boxes, Player: lv.Player, Tail: []meet.Push{{From: lv.Boxes[0], Dir: 3}}}
	var b, tr atomic.Uint64
	deadline := time.Now().Add(3 * time.Second)
	s := newTowardReal(rows, lv, tgt, deadline, nil, &b, &tr, 2, 1000)
	sol, res := s.towardRun(deadline, hugeMem)
	if !res.Solved || verify.Check(rows, sol) != nil || res.Nodes != 0 {
		t.Fatalf("root target: res=%+v sol=%q", res, sol)
	}
}

func TestSyntheticTowardContinuesAfterInaccessibleTarget(t *testing.T) {
	old := TowardHF
	TowardHF = false
	defer func() { TowardHF = old }()
	rows := []string{"#########", "#   #   #", "#@ $   .#", "#   #   #", "#########"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	box := lv.Boxes[0] + 1
	tgt := &meet.Target{Boxes: []int32{box}, Player: box + 1, Tail: []meet.Push{{From: box, Dir: 2}, {From: box - 1, Dir: 2}, {From: box - 2, Dir: 3}, {From: box - 1, Dir: 3}, {From: box, Dir: 3}, {From: box + 1, Dir: 3}, {From: box + 2, Dir: 3}}}
	sol, res := fessToward(rows, lv, tgt, time.Now().Add(3*time.Second), hugeMem, nil)
	if !res.Solved || verify.Check(rows, sol) != nil {
		t.Fatalf("expected actual solution after rejected target; res=%+v sol=%q", res, sol)
	}
}

func TestBoxOnlyTargetStillStopsWithoutFullTarget(t *testing.T) {
	rows := []string{"#########", "#@ $  . #", "#########"}
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	tgt := &meet.Target{Boxes: []int32{lv.Boxes[0] + 1}}
	var b, tr atomic.Uint64
	deadline := time.Now().Add(3 * time.Second)
	s := newTowardReal(rows, lv, tgt, deadline, nil, &b, &tr, 2, 1000)
	s.tgtFull = nil
	res := s.solve(deadline, hugeMem)
	if !res.Solved {
		t.Fatalf("box-only target not reached: %+v", res)
	}
}
