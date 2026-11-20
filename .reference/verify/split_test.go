package verify

import "testing"

// A row of only floor ('-') inside a level must not split it (OTS "Master Head 29");
// blank lines still separate levels.
func TestSplitLevelsFloorRow(t *testing.T) {
	data := []byte("; 1\n### #\n-\n#####\n#@$.#\n#####\n\n; 2\n####\n#@$.#\n####\n")
	lv := SplitLevels(data)
	if len(lv) != 2 || len(lv[0]) != 5 || len(lv[1]) != 3 {
		t.Fatalf("got %d levels: %q", len(lv), lv)
	}
}

// A level that starts solved needs a solution with at least one push.
func TestCheckSolutionNeedsPush(t *testing.T) {
	rows := []string{"######", "#@*  #", "#    #", "######"}
	if CheckSolution(rows, "") == nil || CheckSolution(rows, "rl") == nil {
		t.Fatal("accepted a solution with no push")
	}
	if err := CheckSolution(rows, "RdrruL"); err != nil {
		t.Fatal(err)
	}
}
