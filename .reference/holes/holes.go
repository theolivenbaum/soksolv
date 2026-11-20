// Package holes closes dead-end cells at level load (exp-464, -close-holes).
//
// A floor cell with at least three wall neighbours (walls include cells
// already closed), no goal, no box and not the player's start can be turned
// into a wall without changing the level's solutions:
//   - A box pushed into it is stuck for good: it can only leave by a push
//     that starts with the player on the far side of the cell, which is wall.
//     With no goal there, that is a dead box.
//   - The player can enter the cell only through its single open neighbour N.
//     There he could push a box standing on N, but a box gets onto N only by a
//     push from another side, which leaves the player outside the cell, and N
//     is then blocked. So the player never pushes anything from the cell,
//     unless he STARTS in it (a start box on N); the player's start cell is
//     therefore never closed.
//
// Closing cascades: a corridor dead end is eaten cell by cell until a goal, a
// box, the start cell or a junction stops it. The rule is Festival's
// close_holes_in_board, re-derived here. Solutions of the closed level are
// solutions of the original (the player only loses dead-end steps).
package holes

import "os"

// Enabled turns closing on. It is the default (promoted in exp-464);
// -close-holes=false or SOKSOLV_CLOSE_HOLES=0 (for whole test binaries) turns it off.
var Enabled = os.Getenv("SOKSOLV_CLOSE_HOLES") != "0"

// Close walls off every closable cell of the padded board in place and
// returns how many it closed. wall, goal are indexed like the level (width w,
// outer ring is wall).
func Close(wall, goal []bool, w int, boxes []int32, player int32) int {
	keep := make([]bool, len(wall))
	for _, b := range boxes {
		keep[b] = true
	}
	if player >= 0 {
		keep[player] = true
	}
	dirs := [4]int{-w, w, -1, 1}
	walls := func(c int) int {
		n := 0
		for _, d := range dirs {
			if wall[c+d] {
				n++
			}
		}
		return n
	}
	var stack []int
	for c := w; c < len(wall)-w; c++ {
		if !wall[c] && !goal[c] && !keep[c] && walls(c) >= 3 {
			stack = append(stack, c)
		}
	}
	closed := 0
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if wall[c] || goal[c] || keep[c] || walls(c) < 3 {
			continue
		}
		wall[c] = true
		closed++
		for _, d := range dirs {
			if n := c + d; !wall[n] && !goal[n] && !keep[n] && walls(n) >= 3 {
				stack = append(stack, n)
			}
		}
	}
	return closed
}
