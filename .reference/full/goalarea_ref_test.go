package full

// survivorsOld is the pre-exp-171 fixpoint (repeated full passes), kept as
// the reference for TestSurvivorsAgree.
func (ga *goalArea) survivorsOld(wall []bool, goals []int32, dirs [4]int32) [][]int32 {
	copy(ga.wall, wall)
	for _, f := range ga.fset {
		ga.wall[f] = true
	}
	free := func(c int32) bool { return !ga.wall[c] && !ga.box[c] }
	reset := func() int {
		clear(ga.box)
		n := 0
		for _, g := range goals {
			if !ga.wall[g] {
				ga.box[g] = true
				n++
			}
		}
		return n
	}
	if reset() == 0 {
		return nil
	}
	// Zones of the solved board: try each as the player's final region.
	ga.stamp++
	zoneStamp := ga.stamp
	var seeds []int32
	var zones [][]int32
	for c := range int32(len(wall)) {
		if free(c) && ga.mark[c] != zoneStamp {
			seeds = append(seeds, c)
			ga.flood(c, zoneStamp, free, dirs)
		}
	}
	for _, seed := range seeds {
		left := reset()
		for changed := true; changed && left > 0; {
			changed = false
			ga.stamp++
			st := ga.stamp
			ga.flood(seed, st, free, dirs)
			for _, g := range goals {
				if !ga.box[g] {
					continue
				}
				for _, d := range dirs {
					if p := g + d; ga.mark[p] == st && free(p+d) {
						ga.box[g] = false
						left--
						changed = true
						break
					}
				}
			}
		}
		surv := []int32{}
		for _, g := range goals {
			if ga.box[g] {
				surv = append(surv, g)
			}
		}
		if len(surv) == 0 {
			return [][]int32{surv} // always covered: never dead
		}
		zones = append(zones, surv)
	}
	return zones
}

