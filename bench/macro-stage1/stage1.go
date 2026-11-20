package main

import (
	"fmt"
	"os"
	"soksolv/cluster"
)

// Stage 1 reference lane (C# port validation): the macro lane with rooms, hotspots, move access, the
// sink plan and every default-on experiment feature switched off. Enabled by SOKSOLV_STAGE1=1.
var stage1On = os.Getenv("SOKSOLV_STAGE1") == "1"

func stage1Overrides() {
	RevGroups = false
	MoveWindow = 0
	Novelty = 0
	LazyChild = false
	AccessNovelty = false
	Novelty2 = false
	DisplacementLandmarksOn = false
	CorridorFlow = false
	CorralDead = 0
	PairSide = false
	FrontierCorral = false
	PackingObligations = false
	Plan4Macro = false
	Plan4 = false
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
	fmt.Fprintf(os.Stderr, "S1HASH expanded=%d stored=%d generated=%d duplicates=%d hash=%016x\n", expanded, len(s.player), s.stats.Generated, s.stats.Duplicates, s1Hash)
}
