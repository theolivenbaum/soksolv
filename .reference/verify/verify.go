// Package verify independently replays Sokoban solutions.
//
// FROZEN: this package is part of the evaluation contract (see PLAN.md).
// Candidate solver changes must never modify it. It deliberately shares no
// code with the solver: it has its own level splitter and board model.
package verify

import (
	"fmt"
	"strings"
)

// SplitLevels returns the board rows of each level in an .xsb collection.
// A board row consists only of Sokoban symbols and contains at least one wall.
func SplitLevels(data []byte) [][]string {
	var levels [][]string
	var cur []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if isRow(line) {
			cur = append(cur, line)
		} else if cur != nil && isFloorRow(line) {
			// A row of only floor ('-' or '_') inside a level, e.g. a spacer
			// between parts of OTS "Master Head 29": it does not end the level.
			cur = append(cur, line)
		} else if cur != nil {
			levels = append(levels, trimFloorRows(cur))
			cur = nil
		}
	}
	if cur != nil {
		levels = append(levels, trimFloorRows(cur))
	}
	return levels
}

// isFloorRow reports whether s is a row of floor symbols only ('-', '_',
// spaces) with at least one '-' or '_'. Blank lines still end a level.
func isFloorRow(s string) bool {
	floor := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '-', '_':
			floor = true
		case ' ':
		default:
			return false
		}
	}
	return floor
}

// trimFloorRows drops floor-only rows at the end of a level.
func trimFloorRows(rows []string) []string {
	for len(rows) > 0 && isFloorRow(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	return rows
}

func isRow(s string) bool {
	wall := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '#':
			wall = true
		case ' ', '-', '_', '@', '+', '$', '*', '.':
		default:
			return false
		}
	}
	return wall
}

// Check replays a LURD solution (lowercase = walk, uppercase = push) from the
// original board and reports an error unless every move is legal and every
// box ends on a goal.
func Check(rows []string, lurd string) error {
	h := len(rows)
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	at := func(x, y int) byte {
		if y < 0 || y >= h || x < 0 || x >= len(rows[y]) {
			return '#' // outside the drawn board is treated as solid
		}
		return rows[y][x]
	}
	box := make(map[[2]int]bool)
	goals := 0
	px, py, players := 0, 0, 0
	for y := range h {
		for x := range w {
			switch at(x, y) {
			case '@':
				px, py, players = x, y, players+1
			case '+':
				px, py, players = x, y, players+1
				goals++
			case '$':
				box[[2]int{x, y}] = true
			case '*':
				box[[2]int{x, y}] = true
				goals++
			case '.':
				goals++
			}
		}
	}
	if players != 1 {
		return fmt.Errorf("board has %d players", players)
	}
	if len(box) == 0 || len(box) != goals {
		return fmt.Errorf("board has %d boxes and %d goals", len(box), goals)
	}
	isGoal := func(x, y int) bool { c := at(x, y); return c == '.' || c == '*' || c == '+' }
	isWall := func(x, y int) bool { return at(x, y) == '#' }

	for i := 0; i < len(lurd); i++ {
		var dx, dy int
		switch lurd[i] {
		case 'l', 'L':
			dx = -1
		case 'r', 'R':
			dx = 1
		case 'u', 'U':
			dy = -1
		case 'd', 'D':
			dy = 1
		default:
			return fmt.Errorf("move %d: invalid character %q", i, lurd[i])
		}
		push := lurd[i] >= 'A' && lurd[i] <= 'Z'
		nx, ny := px+dx, py+dy
		if isWall(nx, ny) {
			return fmt.Errorf("move %d: walks into wall", i)
		}
		if box[[2]int{nx, ny}] != push {
			if push {
				return fmt.Errorf("move %d: push with no box", i)
			}
			return fmt.Errorf("move %d: walk into a box (should be a push)", i)
		}
		if push {
			bx, by := nx+dx, ny+dy
			if isWall(bx, by) || box[[2]int{bx, by}] {
				return fmt.Errorf("move %d: box blocked", i)
			}
			delete(box, [2]int{nx, ny})
			box[[2]int{bx, by}] = true
		}
		px, py = nx, ny
	}
	for b := range box {
		if !isGoal(b[0], b[1]) {
			return fmt.Errorf("box at (%d,%d) not on a goal", b[0], b[1])
		}
	}
	return nil
}

// CheckSolution is Check plus the community rule for a submitted solution: it
// must contain at least one push. A level that starts solved (a "circular"
// level) has to be left and solved again.
func CheckSolution(rows []string, lurd string) error {
	if !strings.ContainsAny(lurd, "LURD") {
		return fmt.Errorf("solution has no push")
	}
	return Check(rows, lurd)
}
