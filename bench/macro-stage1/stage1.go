package main

import (
	"fmt"
	"os"
	"soksolv/cluster"
)

// Stage 1 reference lane (C# port validation): the macro lane with rooms, hotspots, move access, the
// sink plan and every default-on experiment feature switched off. Enabled by SOKSOLV_STAGE1=1.
var stageN = func() int {
	if v := os.Getenv("SOKSOLV_STAGE"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	if os.Getenv("SOKSOLV_STAGE1") == "1" {
		return 1
	}
	return 0
}()
var stage1On = stageN >= 1

// stage-6 feature mask (SOKSOLV_FEAT, decimal): bit0 move windows, bit1 deadzone corral proofs + frontier corral,
// bit2 displacement landmarks, bit3 corridor flow, bit4 packing obligations, bit5 plan4 (macro), bit6 rev-groups/novelty
// hooks, bit7 pair-side deadlock. A set bit leaves that feature at its flag default (on); a clear bit forces it off.
var featMask = func() int {
	n := 0
	fmt.Sscanf(os.Getenv("SOKSOLV_FEAT"), "%d", &n)
	return n
}()

func stage1Overrides() {
	on := func(bit int) bool { return featMask&(1<<bit) != 0 }
	if !on(6) {
		RevGroups = false
		Novelty = 0
		Novelty2 = false
		AccessNovelty = false
	}
	if !on(0) {
		MoveWindow = 0
	}
	LazyChild = false
	if !on(2) {
		DisplacementLandmarksOn = false
	}
	if !on(3) {
		CorridorFlow = false
	}
	if !on(1) {
		CorralDead = 0
		FrontierCorral = false
	}
	if !on(7) {
		PairSide = false
	}
	if !on(4) {
		PackingObligations = false
	}
	if !on(5) {
		Plan4Macro = false
		Plan4 = false
	}
	FrozenHall = false
	InefficientPrune = false
	_ = cluster.MaxBoxes
}

var s1Hash uint64 = 1469598103934665603
var s1Trace = os.Getenv("SOKSOLV_S1TRACE") == "1"

func s1mix(v uint64) { s1Hash = (s1Hash ^ v) * 1099511628211 }

func s1step(s *solver, n int, node, local, cid int32) {
	s1mix(uint64(uint32(node)))
	s1mix(uint64(uint32(local)))
	s1mix(uint64(uint32(cid)))
	if s1Trace {
		fmt.Fprintf(os.Stderr, "S1 %d node=%d move=%d child=%d w=%d stored=%d\n", n, node, local, cid, s.w[node], len(s.player))
	}
}

func s1report(s *solver, expanded int) {
	fmt.Fprintf(os.Stderr, "S1FEAT corral_dead=%d frontier_corrals=%d disp_adv=%d corridor=%d\n", s.stats.CorralDead, s.stats.FrontierCorrals, s.stats.Advisors[7], s.stats.CorridorFlow)
	fmt.Fprintf(os.Stderr, "S1HASH expanded=%d stored=%d generated=%d duplicates=%d hash=%016x\n", expanded, len(s.player), s.stats.Generated, s.stats.Duplicates, s1Hash)
}
