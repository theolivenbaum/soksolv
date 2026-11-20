package main

import "testing"

func TestFESSChoosesActualMoveWeightAndChildScore(t *testing.T) {
	s := &solver{dead: make([]bool, 3), w: []int32{0, 1, 0}, version: make([]uint32, 3), bucket: make([]uint32, 3),
		mvStart: []int32{0, 1, 2}, mvLen: []uint32{1, 1, 1}, mvLeft: []uint32{1, 1, 1}, mvNext: make([]uint32, 3),
		mv:      []uint64{encodeMove(0, 1, 1, 1, 0), encodeMove(0, 1, 0, 1, 0), encodeMove(0, 1, 1, 1, 0)},
		mvScore: []moveScore{{oop: 3}, {oop: 1}, {oop: 2}}, frontier: fessFrontier{cells: []featureCell{{}}, queues: make([]fessHeap, 1)}}
	for id := int32(0); id < 3; id++ {
		s.enqueue(id)
	}
	// All next moves cost one. The node with a weight-one parent wins
	// because its actual child has better features, despite its node weight.
	for _, want := range []int32{1, 2, 0} {
		e, ok := s.selectMove()
		if !ok || e.node != want {
			t.Fatalf("got %+v/%v want node %d", e, ok, want)
		}
	}
}

func TestFESSVisitsCellsRoundRobinAndDiscardsStaleEntries(t *testing.T) {
	s := &solver{version: []uint32{1, 0}, bucket: []uint32{0, 1}, mv: []uint64{0, 0}, frontier: fessFrontier{cells: make([]featureCell, 2), queues: make([]fessHeap, 2)}}
	s.frontier.queues[0] = fessHeap{{node: 0, move: 0, weight: 0, version: 0}, {node: 0, move: 0, weight: 9, version: 1}}
	s.frontier.queues[1] = fessHeap{{node: 1, move: 1, weight: 0}}
	e, ok := s.selectMove()
	if !ok || e.node != 0 || e.weight != 9 {
		t.Fatalf("stale entry selected: %+v", e)
	}
	e, ok = s.selectMove()
	if !ok || e.node != 1 {
		t.Fatalf("cell one starved: %+v", e)
	}
}

func TestWorseDescendantsKeepBestAncestorCell(t *testing.T) {
	s := &solver{}
	root := featureCell{packed: 2, connectivity: 1, roomConnectivity: 0, oop: 0}
	id := s.project(root, -1)
	s.bucket = append(s.bucket, uint32(id))
	worse := featureCell{packed: 1, connectivity: 3, roomConnectivity: 2, oop: 0}
	if got := s.project(worse, 0); got != id || len(s.frontier.cells) != 1 {
		t.Fatal("regression received a new feature cell")
	}
	better := featureCell{packed: 3, connectivity: 4, oop: 0}
	if got := s.project(better, 0); got == id || len(s.frontier.cells) != 2 {
		t.Fatal("packing progress failed to create a cell")
	}
}

func TestFESSReopensLowerWeightTransposition(t *testing.T) {
	s := testSolver(t, "########", "#@ $  .#", "########")
	boxes := s.lv.Boxes
	for _, b := range boxes {
		s.occ[b] = true
	}
	p := s.reach(s.lv.Player, s.mark)
	id := s.add(boxes, p, -1, -1, -1, -1, nil, 10, 3, 5, -1)
	s.open(id, boxes, 3, s.stamp)
	if s.mvLen[id] == 0 {
		t.Fatal("fixture has no moves")
	}
	s.mv[s.mvStart[id]] |= moveUsed
	s.mvLeft[id]--
	s.enqueue(id)
	// A lower difficult-move count can have more pushes. g must not hide it.
	if got := s.add(boxes, p, -1, -1, -1, -1, nil, 20, 3, 2, -1); got != id {
		t.Fatal("lower weight was ignored")
	}
	if s.version[id] != 1 || s.mvLeft[id] != s.mvLen[id] || s.mv[s.mvStart[id]]&moveUsed != 0 {
		t.Fatal("moves were not reopened")
	}
	s.enqueue(id)
	e, ok := s.selectMove()
	if !ok || e.version != 1 || e.weight > 3 {
		t.Fatalf("stale path survived relaxation: %+v", e)
	}
}

func TestFESSComparisonDoesNotClampLargeFeatures(t *testing.T) {
	a, b := fessScore{oop: 512, room: 5000, mobility: 10000}, fessScore{oop: 513, room: 0}
	if a.compare(b) >= 0 || a.compact().full() != a {
		t.Fatal("feature values were clamped")
	}
}
