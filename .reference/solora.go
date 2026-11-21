package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"

	"soksolv/soloracle"
)

// oracleLane names a search for the solution-oracle probe (exp-357): F/B for forward/backward, then its owner id.
func (s *solver) oracleLane() string {
	d := "F"
	if s.pull {
		d = "B"
	}
	return d + strconv.Itoa(int(s.owner))
}

// initSolOracle (exp-357, diagnostics): SOKSOLV_SOLUTION=<LURD of a known solution> turns on the probe of
// the states of that solution each lane stores; the per-lane summary is printed to stderr at the end.
func initSolOracle(lv *Level) {
	lurd := os.Getenv("SOKSOLV_SOLUTION")
	if lurd == "" {
		return
	}
	s := newSolver(lv)
	dirs := map[byte]int32{'u': -int32(lv.W), 'd': int32(lv.W), 'l': -1, 'r': 1}
	boxes := slices.Clone(lv.Boxes)
	p := lv.Player
	key := func() uint64 {
		clear(s.occ)
		for _, b := range boxes {
			s.occ[b] = true
		}
		norm := s.reach(p, s.mark)
		sorted := slices.Clone(boxes)
		slices.Sort(sorted)
		return soloracle.Key(sorted, norm)
	}
	ks := []uint64{key()}
	for i := 0; i < len(lurd); i++ {
		c := lurd[i]
		d := dirs[c|0x20]
		if c >= 'A' && c <= 'Z' {
			k := slices.Index(boxes, p+d)
			boxes[k] = p + 2*d
			p += d
			ks = append(ks, key())
			continue
		}
		p += d
	}
	soloracle.Load(ks)
}

func reportSolOracle() {
	if soloracle.On {
		fmt.Fprintln(os.Stderr, "SOLORACLE "+soloracle.Report())
	}
}
