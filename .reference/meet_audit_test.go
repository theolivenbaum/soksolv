package main

import (
	"testing"
	"time"

	"soksolv/meet"
)

// A one-cell corridor prevents walking around the box. This distinguishes
// legal reverse edges from mere box proximity and protects the two-push cap.
func TestMeetAuditGeometryLegalEdges(t *testing.T) {
	lv, err := parseLevelRaw([]string{"#########", "# @ $ . #", "#########"})
	if err != nil {
		t.Fatal(err)
	}
	b := lv.Boxes[0]
	for _, tc := range []struct {
		name      string
		boxDelta  int32
		player    int32
		distance  int
		direction string
	}{
		{"same", 0, b - 3, 0, "push-from-witness"},
		{"push-one", 1, b - 3, 1, "push-from-witness"},
		{"push-two", 2, b - 3, 2, "push-from-witness"},
		{"outside-cap", 3, b - 3, -1, ""},
		{"pull-one", -1, b - 3, 1, "pull-from-witness"},
		{"pull-two", -2, b - 3, 2, "pull-from-witness"},
		{"pull-no-player-space", -3, b - 2, -1, ""},
		{"wrong-region", 0, b + 1, -1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := newSolverUntil(lv, time.Time{})
			boxes := []int32{b + tc.boxDelta}
			packed := fw.pack(boxes)
			slot, _, fp := fw.find(packed, tc.player)
			fw.boxes = append(fw.boxes, packed...)
			fw.player = append(fw.player, tc.player)
			fw.table[slot], fw.tfp[slot] = 0, fp
			table := meet.New(len(lv.Wall), 10, 1)
			table.Add(table.Key(boxes, tc.player))
			h := auditHit{Boxes: []int32{b}, Player: b - 3}
			got := auditGeometryOf(lv, fw, table, h)
			if got.Distance != tc.distance || got.Direction != tc.direction {
				t.Fatalf("got distance=%d direction=%q, want %d %q", got.Distance, got.Direction, tc.distance, tc.direction)
			}
		})
	}
}

func TestMeetAuditGeometryRejectsUnconfirmedHash(t *testing.T) {
	lv, err := parseLevelRaw([]string{"#########", "# @ $ . #", "#########"})
	if err != nil {
		t.Fatal(err)
	}
	fw := newSolverUntil(lv, time.Time{})
	b := lv.Boxes[0]
	h := auditHit{Boxes: []int32{b}, Player: b - 3}
	table := meet.New(len(lv.Wall), 10, 1)
	table.Add(table.Key(h.Boxes, h.Player))
	if got := auditGeometryOf(lv, fw, table, h); got.Distance != -1 {
		t.Fatalf("stale perimeter key confirmed without a node/ref: %+v", got)
	}
}
