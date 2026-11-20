package main

import "testing"

// A row of only floor ('-') inside a level must not split it (OTS "Master Head 29");
// blank lines still separate levels.
func TestSplitCollectionFloorRow(t *testing.T) {
	data := []byte("; 1\n### #\n-\n#####\n#@$.#\n#####\n\n; 2\n####\n#@$.#\n####\n")
	lv := splitCollection(data)
	if len(lv) != 2 || len(lv[0]) != 5 || len(lv[1]) != 3 {
		t.Fatalf("got %d levels: %q", len(lv), lv)
	}
}
