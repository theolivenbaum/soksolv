package main

import "testing"

func testSolver(t *testing.T, rows ...string) *solver {
	t.Helper()
	lv, err := parseLevel(rows)
	if err != nil {
		t.Fatal(err)
	}
	return newSolver(lv)
}

func TestRoomConnectivityDetectsBlockedDirectPath(t *testing.T) {
	s := testSolver(t,
		"#############",
		"#   #####   #",
		"# @         #",
		"#   #####   #",
		"## ####### ##",
		"#   #####   #",
		"#   $     . #",
		"#   #####   #",
		"#############")
	if got := len(s.rooms.rooms); got != 4 {
		t.Fatalf("rooms=%d, want 4", got)
	}
	if got := s.roomConnectivity(); got != 0 {
		t.Fatalf("empty board broken edges=%d", got)
	}
	if got := s.regions(); got != 1 {
		t.Fatalf("empty board regions=%d", got)
	}
	s.occ[s.lv.Boxes[0]] = true
	if got := s.regions(); got != 1 {
		t.Fatalf("alternate route should preserve connectivity: %d", got)
	}
	if got := s.roomConnectivity(); got != 2 {
		t.Fatalf("blocked corridor should break two directed edges: %d", got)
	}
}

func TestRoomDefinitionRequiresTwoByThree(t *testing.T) {
	s := testSolver(t, "######", "#@ $.#", "######")
	if len(s.rooms.rooms) != 0 {
		t.Fatal("one-wide passage is not a room")
	}
	s = testSolver(t, "######", "#@ $ #", "# .  #", "######")
	if len(s.rooms.rooms) != 1 {
		t.Fatalf("2x4 area should be one room, got %d", len(s.rooms.rooms))
	}
}

func TestRoomFragmentsAndScratchReuse(t *testing.T) {
	s := testSolver(t, "#############", "#   #####   #", "# @         #", "#   #####   #", "## ####### ##", "#   #####   #", "#   $     . #", "#   #####   #", "#############")
	for y := 2; y <= 4; y++ {
		s.occ[y*s.lv.W+3] = true
	}
	if got := s.roomConnectivity(); got != 4 {
		t.Fatalf("split room hides broken direct paths: %d", got)
	}
	allocs := testing.AllocsPerRun(100, func() { s.roomConnectivity() })
	if allocs != 0 {
		t.Fatalf("room feature allocates %.1f times per call", allocs)
	}
	s.rooms.coverStamp = ^uint32(0)
	s.rooms.touchStamp = ^uint32(0)
	s.rooms.stamp = ^uint32(0)
	if got := s.roomConnectivity(); got != 4 {
		t.Fatalf("stamp rollover changed feature: %d", got)
	}
}
