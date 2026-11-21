package reverse

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestReverseCorridor (diagnostic, SOKSOLV_CORRIDOR=solution-file): how deep
// along a known sven-1244 solution (pulls from the goal) a reverse lane's
// stored states reach in 5 s.
func TestReverseCorridor(t *testing.T) {
	path := os.Getenv("SOKSOLV_CORRIDOR")
	if path == "" {
		t.Skip()
	}
	solb, _ := os.ReadFile(path)
	sol := strings.TrimSpace(string(solb))
	data, _ := os.ReadFile("../levels/Large Test Suite/Sven_1623.xsb")
	rows := splitCollection(data)[1243]
	lv, _ := parseLevel(rows)
	dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
	type pos struct {
		boxes  []int32
		player int32
	}
	boxes := slices.Clone(lv.Boxes)
	p := lv.Player
	var path0 []pos
	path0 = append(path0, pos{slices.Clone(boxes), p})
	for i := 0; i < len(sol); i++ {
		d := dirs[sol[i]|0x20]
		if sol[i] >= 'A' && sol[i] <= 'Z' {
			boxes[slices.Index(boxes, p+d)] = p + 2*d
			p += d
			b := slices.Clone(boxes)
			slices.Sort(b)
			path0 = append(path0, pos{b, p})
			continue
		}
		p += d
	}
	for _, ln := range []Lane{{Cells: true}, {Cells: true, Connected: true}} {
		s, rr := runLaneForTest(rows, ln, 5*time.Second)
		var hits []int
		for i, x := range path0 {
			if s.storedPos(x.boxes, x.player) {
				hits = append(hits, i)
			}
		}
		t.Logf("lane %+v: %d nodes (solved %v); solution positions stored at pushes %v", ln, s.nodes.Len(), rr.Solved, hits)
	}
}
