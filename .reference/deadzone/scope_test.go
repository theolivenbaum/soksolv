package deadzone

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
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			wall[y*w+x] = x == 0 || y == 0 || x == w-1 || y == w-1
			targetDead[y*w+x] = false
		}
	}
	wall[15], wall[19] = true, true
	wall[30], wall[31], wall[32] = true, true, true
	boxes := []int32{16, 17, 18}
	realGoals[16], realGoals[18], realGoals[24] = true, true, true
	targetGoals[9], targetGoals[10], targetGoals[11] = true, true, true
	targetDead = inverseDead(wall, targetGoals, w)
	if err := verify.Check([]string{"#######", "#  @  #", "##*$*##", "#  .  #", "# ### #", "#     #", "#######"}, "D"); err != nil {
		t.Fatalf("real goal fixture is not solvable: %v", err)
	}
	prev := Shared.Swap(NewTable(10))
	defer Shared.Store(prev)
	makeChecker := func(goals, dead []bool, private bool) *Checker {
		c := NewChecker(NewBoard(wall, goals, dead, w), 1000)
		c.Private = private
		c.zone = append(c.zone, boxes...)
		c.realMin = 10
		return c
	}
	pt := makeChecker(targetGoals, targetDead, true)
	k := pt.key(boxes, 10)
	if !pt.zoneDead(boxes, 10) {
		t.Fatal("target fixture did not prove dead")
	}
	if Shared.Load().slots[k>>2&1023].Load() != 0 {
		t.Fatal("private target exported its proof")
	}
	target := makeChecker(targetGoals, targetDead, false)
	if !target.zoneDead(boxes, 10) || Shared.Load().slots[k>>2&1023].Load()&3 != 1 {
		t.Fatal("target proof not published")
	}
	real := makeChecker(realGoals, realDead, true)
	if real.zoneDead(boxes, 10) || real.Stats.CacheHits != 0 {
		t.Fatal("private checker imported foreign target verdict")
	}
	foreign := makeChecker(realGoals, realDead, false)
	if !foreign.zoneDead(boxes, 10) {
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
