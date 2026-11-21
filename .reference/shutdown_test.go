package main

import (
	"testing"

	"soksolv/trace"
)

// exp-468: the watchdog must stamp "stopped" only on lanes still running and
// their companions, leaving finished lanes' summaries alone.
func TestMarkStoppedKeepsFinishedLanes(t *testing.T) {
	done := &trace.Summary{End: "exhausted"}
	summaries := map[string]*trace.Summary{
		"reverse": {}, "full": done, "bridge": done,
		"macro": done, "hub-gen": done, "hub-gen-fwd": done,
		"hub-pull": {}, "hub-pull-fwd": {},
	}
	markStopped(summaries, map[string]uint64{"reverse": 1, "hub-pull": 1})
	for _, n := range []string{"reverse", "hub-pull", "hub-pull-fwd"} {
		if summaries[n].End != "stopped" {
			t.Errorf("%s: want stopped, got %q", n, summaries[n].End)
		}
	}
	for _, n := range []string{"full", "bridge", "macro", "hub-gen", "hub-gen-fwd"} {
		if summaries[n] != done {
			t.Errorf("%s: finished lane was overwritten", n)
		}
	}
}
