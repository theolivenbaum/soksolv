package reverse

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// exp-397: with Compact the node store is delta-coded; a node-bounded search
// must be identical to the plain store: same expansions, same nodes, and for
// every node the same parent, player and box bitset.
func TestCompactStoreIdentical(t *testing.T) {
	files, _ := filepath.Glob("../levels/Large Test Suite/*.xsb")
	if len(files) == 0 {
		t.Skip("no levels")
	}
	nodes, stride := int64(3000), 17
	if testing.Short() {
		nodes, stride = 600, 53
	}
	old, oldC := NodeBudget, Compact
	NodeBudget = nodes
	defer func() { NodeBudget, Compact = old, oldC }()
	lanes := []Lane{{Free: false, Cells: true}, {Free: true}}
	checkedLevels, checkedNodes, deltas := 0, 0, 0
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for li, rows := range splitCollection(data) {
			n++
			if n%stride != 0 {
				continue
			}
			for _, ln := range lanes {
				run := func(c bool) (*solver, Result) {
					Compact = c
					return runLaneForTest(rows, ln, time.Minute)
				}
				a, ra := run(false)
				b, rb := run(true)
				if a == nil || b == nil {
					continue
				}
				if ra.Nodes != rb.Nodes || ra.Solved != rb.Solved || ra.Solution != rb.Solution || a.nodes.Len() != b.nodes.Len() {
					t.Fatalf("%s #%d lane %+v: plain %d nodes solved=%v, compact %d nodes solved=%v", f, li+1, ln, a.nodes.Len(), ra.Solved, b.nodes.Len(), rb.Solved)
				}
				if !b.nodes.compact {
					continue // level too big for delta headers: nothing to compare
				}
				for id := range int32(a.nodes.Len()) {
					if a.nodeParent(id) != b.nodeParent(id) || a.nodePlayer(id) != b.nodePlayer(id) ||
						!slices.Equal(a.nodes.Boxes(id, a.nodes.bufA), b.nodes.Boxes(id, b.nodes.bufA)) {
						t.Fatalf("%s #%d lane %+v: node %d differs", f, li+1, ln, id)
					}
					checkedNodes++
					if _, _, ok := b.nodes.Pulled(id); ok {
						deltas++
					}
				}
				checkedLevels++
			}
		}
	}
	t.Logf("levels checked %d, nodes %d (delta %d)", checkedLevels, checkedNodes, deltas)
	if checkedLevels < 10 || deltas == 0 {
		t.Errorf("too little coverage")
	}
}
