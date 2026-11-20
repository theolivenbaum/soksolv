// Package sidedead: player-side dead squares, shared by the forward searches.
package sidedead

// Player-side dead squares. The static dead-square table assumes the player
// can stand anywhere. With a single box on the board, the floor minus the
// box's cell splits into at most four player components around it, and
// whether the box can still reach a goal can depend on which one the
// player is in: a box below a dead-end notch can only be pushed down from
// the notch, and once the player has left, the notch is unreachable.
//
// Table precomputes, for every floor cell c and every player component k
// around c (other boxes removed), whether a goal is reachable by pushing the
// box. A state where some box off a goal sits in a dead (cell, component)
// is dead: removing the other boxes only adds possibilities, so the test is
// sound.
type Table struct {
	fi    []int32  // cell -> floor index, -1 for walls
	label [][]int8 // per floor index: component of every floor cell with the box there (nil: one component)
	live  []bool   // per floor index * 4 + component
}

func New(wall, goal []bool, w int) *Table {
	dirs := [4]int32{-int32(w), int32(w), -1, 1}
	lv := struct{ Wall, Goal []bool }{wall, goal}
	n := len(lv.Wall)
	sd := &Table{fi: make([]int32, n)}
	var floor []int32
	for c := range int32(n) {
		sd.fi[c] = -1
		if !lv.Wall[c] {
			sd.fi[c] = int32(len(floor))
			floor = append(floor, c)
		}
	}
	nf := len(floor)
	sd.label = make([][]int8, nf)
	sd.live = make([]bool, nf*4)
	// Components of the floor with a box on c, labelled from c's
	// neighbours (at most 4).
	comp := make([]int8, nf)
	queue := make([]int32, 0, nf)
	for i, c := range floor {
		for j := range comp {
			comp[j] = -1
		}
		k := int8(0)
		for _, d := range dirs {
			x := c + d
			if lv.Wall[x] || comp[sd.fi[x]] >= 0 {
				continue
			}
			comp[sd.fi[x]] = k
			queue = append(queue[:0], x)
			for q := 0; q < len(queue); q++ {
				for _, d2 := range dirs {
					y := queue[q] + d2
					if y != c && !lv.Wall[y] && comp[sd.fi[y]] < 0 {
						comp[sd.fi[y]] = k
						queue = append(queue, y)
					}
				}
			}
			k++
		}
		if k > 1 {
			sd.label[i] = append([]int8(nil), comp...)
		}
	}
	side := func(c, player int32) int8 { // component of player with the box on c
		if l := sd.label[sd.fi[c]]; l != nil {
			return l[sd.fi[player]]
		}
		return 0
	}
	// Backward BFS over (box cell, player component) from the goals: a
	// state is live if a push leads to a live state or the box is on a goal.
	q := []int32{}
	for i, c := range floor {
		if lv.Goal[c] {
			for k := range 4 {
				sd.live[i*4+k] = true
				q = append(q, int32(i*4+k))
			}
		}
	}
	for len(q) > 0 {
		v := q[0]
		q = q[1:]
		y := floor[v/4]
		ky := int8(v % 4)
		// Predecessor: the box was on x = y - d and was pushed by the
		// player standing on x - d; afterwards the player is on x, which
		// must lie in component ky around y.
		for _, d := range dirs {
			x, p := y-d, y-2*d
			if lv.Wall[x] || lv.Wall[p] || side(y, x) != ky {
				continue
			}
			u := sd.fi[x]*4 + int32(side(x, p))
			if !sd.live[u] {
				sd.live[u] = true
				q = append(q, u)
			}
		}
	}
	return sd
}

// dead reports whether a box on c, with the player on cell player, can no
// longer reach any goal.
func (sd *Table) Dead(c, player int32) bool {
	i := sd.fi[c]
	k := int8(0)
	if l := sd.label[i]; l != nil {
		k = l[sd.fi[player]]
	}
	return k < 0 || !sd.live[int(i)*4+int(k)]
}
