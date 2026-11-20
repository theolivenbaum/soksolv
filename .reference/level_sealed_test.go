package main

import (
	"os"
	"path/filepath"
	"testing"
)

// exp-645: a box or empty goal outside the player's region is reported as unsolvable instead of being dropped.
func TestParseSealedPlayer(t *testing.T) {
	rows := []string{
		"#######",
		"#@#  .#",
		"### $ #",
		"#######",
	}
	if _, err := parseLevelRaw(rows); err == nil {
		t.Fatal("sealed player: want an error")
	}
	ok := []string{
		"#######",
		"#@ $.##",
		"####*##",
		"#######",
	}
	if _, err := parseLevelRaw(ok); err != nil {
		t.Fatal("box on goal outside the region must still parse:", err)
	}
}

// Every suite level parses as before (the new error only fires on sealed sub-levels).
func TestParseSealedSuites(t *testing.T) {
	files, _ := filepath.Glob("levels/*/*")
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, rows := range splitCollection(data) {
			if _, err := parseLevelRaw(rows); err != nil && err.Error() == "unsolvable: box or goal outside the player's region" {
				t.Errorf("%s #%d: %v", f, i+1, err)
			}
			n++
		}
	}
	t.Logf("%d levels checked", n)
}
