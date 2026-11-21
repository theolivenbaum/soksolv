package main

// Lane IDs (exp-639). The portfolio's lanes are named by mechanism
// (docs/LANES.md). Their historical IDs stay valid: -strategy accepts them,
// and diag/lanes.py maps them for the ledger and the scorers, since older
// results, events and scripts use them. Go identifiers (festX, FestBLane,
// festbRun, the -festx/-festb/-pf-* flags) keep their historical names.
var laneAliases = map[string]string{
	"festx":     "hub-pull",     // pull-tree hub search (was Festival strategy D)
	"festx-fwd": "hub-pull-fwd", // its forward search toward the hub
	"festb":     "hub-gen",      // hub-generator search (was Festival strategy B; -hub=false restores B)
	"festb-fwd": "hub-gen-fwd",  // its forward contexts
}

// canonLane maps a historical lane ID to its current name.
func canonLane(name string) string {
	if n, ok := laneAliases[name]; ok {
		return n
	}
	return name
}
