package main

import (
	"fmt"
	"os"
)

// diagMode selects where the diagonal-chain deadlock prune (package diagdead, exp-459) applies.
type diagMode uint8

const (
	diagOff    diagMode = iota
	diagTarget          // toward lanes only: toward / festb / festx forward and the hub closer (solvers with hfRank)
	diagAll             // every forward lane, including macro and novelty
)

func (m diagMode) String() string {
	switch m {
	case diagTarget:
		return "target"
	case diagAll:
		return "true"
	}
	return "false"
}

// Set accepts the boolean spellings (a bare -diag-dead means all) and "target".
func (m *diagMode) Set(v string) error {
	switch v {
	case "", "1", "t", "T", "true", "TRUE", "True", "all":
		*m = diagAll
	case "0", "f", "F", "false", "FALSE", "False", "off":
		*m = diagOff
	case "target":
		*m = diagTarget
	default:
		return fmt.Errorf("want false, true/all or target, got %q", v)
	}
	return nil
}

func (m *diagMode) IsBoolFlag() bool { return true } // -diag-dead alone is -diag-dead=true

// DiagDead (-diag-dead[=target|all|false]) prunes pushes that create a diagonal-chain deadlock.
// Default: target (promoted 2026-11-09). Pull lanes are unaffected. SOKSOLV_DIAG_DEAD=1|all|target sets the default before flag parsing,
// so whole test binaries can run under it (the SOKSOLV_COMPACT seam of exp-397).
var DiagDead = func() (m diagMode) {
	m = diagTarget // default on for the toward family (exp-459 promotion); macro/novelty identical
	if v := os.Getenv("SOKSOLV_DIAG_DEAD"); v != "" {
		m.Set(v)
	}
	return
}()
