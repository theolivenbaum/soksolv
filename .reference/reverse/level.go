package reverse

import (
	"errors"
	"fmt"
	"soksolv/holes"
	"strings"
)

// Level is a padded grid: the outer ring is always wall, so neighbor lookups
// never go out of bounds. Cells unreachable by the player are turned into walls.
type Level struct {
	W, H   int
	Wall   []bool
	Goal   []bool
	Boxes  []int32
	Player int32
}

func isBoardLine(s string) bool {
	hasWall := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '#':
			hasWall = true
		case ' ', '-', '_', '@', '+', '$', '*', '.':
		default:
			return false
		}
	}
	return hasWall
}

// splitCollection returns each level in an .xsb file as its board rows.
func splitCollection(data []byte) [][]string {
	var levels [][]string
	var cur []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if isBoardLine(line) {
			cur = append(cur, line)
		} else if cur != nil {
			levels = append(levels, cur)
			cur = nil
		}
	}
	if cur != nil {
		levels = append(levels, cur)
	}
	return levels
}

func parseLevel(rows []string) (*Level, error) {
	w := 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	lv := &Level{W: w + 2, H: len(rows) + 2, Player: -1}
	n := lv.W * lv.H
	if n > 32767 {
		return nil, fmt.Errorf("board too large (%d cells)", n) // solver stores cells as int16
	}
	lv.Wall = make([]bool, n)
	lv.Goal = make([]bool, n)
	isBox := make([]bool, n)
	for i := range lv.Wall {
		lv.Wall[i] = true
	}
	for y, r := range rows {
		for x := 0; x < len(r); x++ {
			c := int32((y+1)*lv.W + x + 1)
			ch := r[x]
			lv.Wall[c] = ch == '#'
			switch ch {
			case '@':
				lv.Player = c
			case '+':
				lv.Player = c
				lv.Goal[c] = true
			case '$':
				isBox[c] = true
			case '*':
				isBox[c] = true
				lv.Goal[c] = true
			case '.':
				lv.Goal[c] = true
			}
		}
	}
	if lv.Player < 0 {
		return nil, errors.New("level has no player")
	}

	// Flood fill from the player (through boxes); everything else becomes wall.
	inside := make([]bool, n)
	stack := []int32{lv.Player}
	inside[lv.Player] = true
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range dirs {
			if nb := c + d; !lv.Wall[nb] && !inside[nb] {
				inside[nb] = true
				stack = append(stack, nb)
			}
		}
	}
	goals := 0
	for c := range n {
		if !inside[c] {
			lv.Wall[c] = true
			lv.Goal[c] = false
			continue
		}
		if isBox[c] {
			lv.Boxes = append(lv.Boxes, int32(c))
		}
		if lv.Goal[c] {
			goals++
		}
	}
	if len(lv.Boxes) != goals {
		return nil, fmt.Errorf("%d boxes but %d goals", len(lv.Boxes), goals)
	}
	if holes.Enabled { // exp-464: dead-end cells become walls
		holes.Close(lv.Wall, lv.Goal, lv.W, lv.Boxes, lv.Player)
	}
	return lv, nil
}
