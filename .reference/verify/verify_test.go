package verify

import "testing"

var microban1 = []string{
	"####",
	"# .#",
	"#  ###",
	"#*@  #",
	"#  $ #",
	"#  ###",
	"####",
}

func TestCheck(t *testing.T) {
	if err := Check(microban1, "dlUrrrdLullddrUluRuulDrddrruLdlUU"); err != nil {
		t.Fatalf("valid solution rejected: %v", err)
	}
	bad := map[string]string{
		"empty":          "",
		"incomplete":     "dlUrrrdLullddrUluRuulDrddrruLdlU",
		"push as walk":   "dluRRrdLullddrUluRuulDrddrruLdlUU",
		"walk as push":   "DlUrrrdLullddrUluRuulDrddrruLdlUU",
		"into wall":      "uu",
		"bad char":       "x",
		"push into wall": "lLL",
	}
	for name, lurd := range bad {
		if Check(microban1, lurd) == nil {
			t.Errorf("%s: invalid solution %q accepted", name, lurd)
		}
	}
}
