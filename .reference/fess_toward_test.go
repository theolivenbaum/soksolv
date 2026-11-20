package main

import (
	"os"
	"strconv"
	"testing"
	"time"

	"soksolv/reverse"
)

// TestFessTowardProbe (diagnostic, SOKSOLV_TOWARD="file index"): run the
// backward search for 3 s keeping its best state (lowest h, and lowest h
// among connected states), then FESS toward each for 7 s.
func TestFessTowardProbe(t *testing.T) {
	spec := os.Getenv("SOKSOLV_TOWARD")
	if spec == "" {
		t.Skip("set SOKSOLV_TOWARD")
	}
	f := splitLast(spec)
	data, err := os.ReadFile(f[0])
	if err != nil {
		t.Fatal(err)
	}
	idx, _ := strconv.Atoi(f[1])
	rows := splitCollection(data)[idx-1]
	lv, _ := parseLevel(rows)
	for _, connected := range []bool{false, true} {
		rr, _ := reverse.SolveLane(rows, time.Now().Add(3*time.Second), 1<<30, 0, reverse.Lane{Cells: true, KeepBest: true, Private: true, Connected: connected})
		if rr.Solved {
			t.Logf("connected=%v: reverse solved it", connected)
			continue
		}
		if rr.Best == nil {
			t.Logf("connected=%v: no best state", connected)
			continue
		}
		start := time.Now()
		sol, res := fessToward(rows, lv, rr.Best, time.Now().Add(7*time.Second), hugeMem, nil)
		t.Logf("connected=%v: best h=%d tail=%d -> FESS toward: solved=%v (%s) nodes=%d in %v", connected, rr.Best.H, len(rr.Best.Tail), sol != "", res.Reason, res.Nodes, time.Since(start).Round(time.Millisecond))
	}
}

// splitLast splits "path with spaces 12 5" into [path, 12, 5] (the path may contain spaces).
func splitLast(s string) []string {
	var tail []string
	for range 2 {
		i := len(s) - 1
		for i >= 0 && s[i] != ' ' {
			i--
		}
		if i < 0 {
			break
		}
		if _, err := strconv.ParseFloat(s[i+1:], 64); err != nil {
			break
		}
		tail = append([]string{s[i+1:]}, tail...)
		s = s[:i]
	}
	return append([]string{s}, tail...)
}
