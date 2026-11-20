package main

import "testing"

func TestDiagModeSet(t *testing.T) {
	for _, c := range []struct {
		in   string
		want diagMode
		ok   bool
	}{{"", diagAll, true}, {"true", diagAll, true}, {"1", diagAll, true}, {"all", diagAll, true}, {"target", diagTarget, true},
		{"false", diagOff, true}, {"0", diagOff, true}, {"nope", diagOff, false}} {
		var m diagMode
		err := m.Set(c.in)
		if (err == nil) != c.ok || err == nil && m != c.want {
			t.Errorf("Set(%q) = %v, %v", c.in, m, err)
		}
	}
}
