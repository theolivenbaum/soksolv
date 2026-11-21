package main

import "testing"

// withNodeBudget makes solves in this test deterministic (expansion-bounded, wall time
// ignored) and restores the global afterwards.
func withNodeBudget(t *testing.T, n int64) {
	t.Helper()
	old := NodeBudget
	NodeBudget = n
	t.Cleanup(func() { NodeBudget = old })
}

// hugeMem is a memLimit no test process approaches. solve() compares process-wide mapped
// memory against a fraction of memLimit, so a small limit makes a test depend on what
// earlier tests left on the heap (the "memory" flake of exp-377).
const hugeMem = 64 << 30
