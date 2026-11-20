package main

import (
	"encoding/json"
	"os"
)

// PlanFile (exp-472, oracle debug): a single-level JSON {"W":rowWidth,"E":[[from,to],...],"init":n}
// holding the entry list of Festival's strategy-A packing plan (Festival cell
// indices y*W+x). It replaces the packed feature (Festival prefix semantics)
// and, with PlanFileOOP, the out-of-plan cell set of the solver's own plan.
var PlanFile string
var PlanFileOOP = true

type planFileJSON struct {
	W    int       `json:"W"`
	E    [][]int32 `json:"E"`
	Init int       `json:"init"`
	// P (exp-740, oracle diagnostic): extra park cells [cell, step] that feed
	// only the out-of-plan zones: the cell is a target of zone i when
	// step < 0 or i < step. Absent = unchanged behaviour.
	P [][2]int32 `json:"P"`
}

func loadPlanFile(path string, lv *Level) (*festPlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j planFileJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	w2 := int32(j.W + 2)
	tf := func(c int32) int32 {
		if c < 0 {
			return c
		}
		return (c/int32(j.W)+1)*w2 + c%int32(j.W) + 1
	}
	fp := &festPlan{initNum: j.Init, mode: 1}
	fp.festTo = make([]bool, len(lv.Wall))
	for _, e := range j.E {
		to := tf(e[1])
		fp.e = append(fp.e, festEntry{from: tf(e[0]), to: to})
		if int(to) < len(fp.festTo) {
			fp.festTo[to] = true
		}
	}
	for _, p := range j.P {
		fp.oopPark = append(fp.oopPark, [2]int32{tf(p[0]), p[1]})
	}
	fp.recomputeUntil()
	return fp, nil
}
