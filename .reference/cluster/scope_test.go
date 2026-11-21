package cluster

import (
	"soksolv/verify"
	"testing"
)

func TestPrivateCheckerCrossGoalProof(t *testing.T) {
	const w = 7
	wall := make([]bool, w*w)
	realGoals := make([]bool, w*w)
	targetGoals := make([]bool, w*w)
	targetDead := make([]bool, w*w)
	realDead := make([]bool, w*w)
	occ := make([]bool, w*w)
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			wall[y*w+x] = x == 0 || y == 0 || x == w-1 || y == w-1
			targetDead[y*w+x] = false
		}
	}
	wall[15], wall[19] = true, true
	wall[30], wall[31], wall[32] = true, true, true
	boxes := []int32{16, 17, 18}
	for _, b := range boxes {
		occ[b] = true
	}
	realGoals[16], realGoals[18], realGoals[24] = true, true, true
	targetGoals[9], targetGoals[10], targetGoals[11] = true, true, true
	targetDead = inverseDead(wall, targetGoals, w)
	if err := verify.Check([]string{"#######", "#  @  #", "##*$*##", "#  .  #", "# ### #", "#     #", "#######"}, "D"); err != nil {
		t.Fatalf("real goal fixture is not solvable: %v", err)
	}
	prev := Shared.Swap(NewTable(10))
	defer Shared.Store(prev)
	privateTarget := NewChecker(NewBoard(wall, targetGoals, targetDead, w))
	privateTarget.Private = true
	if !privateTarget.Dead(occ, 17, 10, nil) {
		t.Fatal("target fixture did not prove dead")
	}
	if Shared.Load().has(key(boxes, 10)) {
		t.Fatal("private target exported its proof")
	}
	target := NewChecker(NewBoard(wall, targetGoals, targetDead, w))
	if !target.Dead(occ, 17, 10, nil) || !Shared.Load().has(key(boxes, 10)) {
		t.Fatal("target proof not published")
	}
	privateReal := NewChecker(NewBoard(wall, realGoals, realDead, w))
	privateReal.Private = true
	if privateReal.Dead(occ, 17, 10, nil) {
		t.Fatal("foreign target proof pruned solvable real board")
	}
	foreign := NewChecker(NewBoard(wall, realGoals, realDead, w))
	if !foreign.Dead(occ, 17, 10, nil) {
		t.Fatal("foreign proof fixture missed")
	}
}

func inverseDead(wall, goals []bool, w int) []bool {
	reach := make([]bool, len(wall))
	q := []int32{}
	for i, goal := range goals {
		if goal {
			reach[i] = true
			q = append(q, int32(i))
		}
	}
	dirs := [4]int32{-int32(w), int32(w), -1, 1}
	for i := 0; i < len(q); i++ {
		for _, d := range dirs {
			p := q[i] - d
			support := p - d
			if p >= 0 && support >= 0 && int(p) < len(wall) && int(support) < len(wall) && !wall[p] && !wall[support] && !reach[p] {
				reach[p] = true
				q = append(q, p)
			}
		}
	}
	dead := make([]bool, len(wall))
	for i := range dead {
		dead[i] = !reach[i]
	}
	return dead
}
