package reverse

import (
	"testing"
	"time"
)

// A node budget must select the same search schedule independently of CPU
// load or a candidate's speed. Wall time still governs production runs.
func TestNodeBudgetScheduleIgnoresClock(t *testing.T) {
	oldBudget, oldAfter, oldExp, oldNovelty, oldTypes := NodeBudget, CellsAfter, CellsAfterExp, Novelty, Types
	defer func() {
		NodeBudget, CellsAfter, CellsAfterExp, Novelty, Types = oldBudget, oldAfter, oldExp, oldNovelty, oldTypes
	}()
	NodeBudget, CellsAfter, CellsAfterExp, Novelty, Types = 1000, time.Second, 50, false, false
	for _, start := range []time.Time{time.Now().Add(-time.Hour), time.Now().Add(time.Hour)} {
		s := &solver{cells: &cellQueue{}, started: start, expandedTotal: 100}
		s.q.push(0, 0)
		s.q.push(1, 1)
		s.cells.push(0, 0, 0)
		s.cells.push(1, 1, 1)
		id, _ := s.popNext()
		if id != 1 {
			t.Fatalf("bounded schedule depended on wall clock: got %d, want round-robin cell node 1", id)
		}
	}
}
