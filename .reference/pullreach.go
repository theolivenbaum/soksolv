package main

// PFReach (exp-619, default off): Festival-ordered pull searches (festx,
// festb) use EXACT single-box reach, with the player's access checked, in the
// start-square matching that prunes pull states (open-time feasible()) and
// filters the best state (the forward target, PullBestFeasible).
//
// The pull solver's goalReach was box-only (a box at c can be pulled to start
// square g if a pull path exists with a free cell behind, ignoring whether the
// player can get there). exp-619 found that festx's fixed pull nominated hubs
// that Festival's own pull-mode matching proves unreachable (JPK #40 hubs 2-3,
// Sven_1623 #1146 hub 3), which the forward then chased for the whole run.
// hubReach (exp-463) computes, per start box, the cells it can be pushed to
// on the walls-only board with the player's component tracked: a superset of
// the real game's reach (other boxes only restrict), so intersecting it into
// goalReach never rejects a state on a solution path.
var PFReach = true // exp-619 promoted 2026-11-11

// pfReachInit intersects the pull solver's goalReach with hubReach's exact
// single-box reach (once per solver).
func (s *solver) pfReachInit() {
	if !s.pull || len(s.goalReach) == 0 || s.pfReachOf == &s.goalReach[0] {
		return
	}
	s.pfReachOf = &s.goalReach[0] // goalReach can be released and rebuilt
	r := newHubReach(s)
	if r == nil || len(r.rb) != len(s.goals) {
		return
	}
	cells := len(s.lv.Wall)
	gw := s.goalW
	for g := range s.goals {
		rb := r.rb[g]
		for c := range cells {
			if rb[c>>6]&(1<<(c&63)) == 0 {
				s.goalReach[c*gw+g>>6] &^= 1 << (g & 63)
			}
		}
	}
	s.tr.SetExtra("pf_reach", 1)
}
