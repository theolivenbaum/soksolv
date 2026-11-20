package main

// Circular levels: boards that start with every box on a goal. The community
// rule is that a solution must contain at least one push, so the start position
// does not count as solved. Rather than teach every lane to ignore the start,
// the solver tries each legal first push and solves the resulting position (a
// normal, unsolved level) with the full portfolio in a child process. Children
// take turns with doubling time slices until one is solved or time runs out.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// circularPeakMB is the largest peak memory reported by a child run.
var circularPeakMB float64

func startsSolved(lv *Level) bool {
	if len(lv.Boxes) == 0 {
		return false
	}
	for _, b := range lv.Boxes {
		if !lv.Goal[b] {
			return false
		}
	}
	return true
}

type firstPush struct {
	prefix string   // walk to the box, then the push (LURD)
	rows   []string // the board after the push (never solved: every goal starts occupied)
}

// firstPushes lists every legal first push of the board drawn by rows.
func firstPushes(rows []string) []firstPush {
	h, w := len(rows), 0
	for _, r := range rows {
		w = max(w, len(r))
	}
	grid := make([][]byte, h)
	for y, r := range rows {
		grid[y] = []byte(r + strings.Repeat(" ", w-len(r)))
	}
	px, py := -1, -1
	for y := range h {
		for x := range w {
			if c := grid[y][x]; c == '@' || c == '+' {
				px, py = x, y
			}
		}
	}
	if px < 0 {
		return nil
	}
	inside := func(x, y int) bool { return x >= 0 && y >= 0 && x < w && y < h }
	wall := func(x, y int) bool { return !inside(x, y) || grid[y][x] == '#' }
	box := func(x, y int) bool { return inside(x, y) && (grid[y][x] == '$' || grid[y][x] == '*') }
	goal := func(x, y int) bool {
		c := grid[y][x]
		return c == '.' || c == '*' || c == '+'
	}

	// Walking paths from the worker (boxes block).
	type pt struct{ x, y int }
	dirs := []struct {
		dx, dy     int
		walk, push byte
	}{{-1, 0, 'l', 'L'}, {1, 0, 'r', 'R'}, {0, -1, 'u', 'U'}, {0, 1, 'd', 'D'}}
	path := map[pt]string{{px, py}: ""}
	queue := []pt{{px, py}}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, d := range dirs {
			q := pt{p.x + d.dx, p.y + d.dy}
			if _, seen := path[q]; seen || wall(q.x, q.y) || box(q.x, q.y) {
				continue
			}
			path[q] = path[p] + string(d.walk)
			queue = append(queue, q)
		}
	}

	var out []firstPush
	for y := range h {
		for x := range w {
			if !box(x, y) {
				continue
			}
			for _, d := range dirs {
				walk, ok := path[pt{x - d.dx, y - d.dy}]
				tx, ty := x+d.dx, y+d.dy
				if !ok || wall(tx, ty) || box(tx, ty) {
					continue
				}
				g := make([][]byte, h)
				for i := range grid {
					g[i] = append([]byte(nil), grid[i]...)
				}
				floor := func(x, y int) byte {
					if goal(x, y) {
						return '.'
					}
					return ' '
				}
				g[py][px] = floor(px, py)
				g[ty][tx] = map[bool]byte{true: '*', false: '$'}[goal(tx, ty)]
				g[y][x] = map[bool]byte{true: '+', false: '@'}[goal(x, y)]
				fp := firstPush{prefix: walk + string(d.push)}
				for _, r := range g {
					fp.rows = append(fp.rows, strings.TrimRight(string(r), " "))
				}
				out = append(out, fp)
			}
		}
	}
	return out
}

// solveCircular solves a level that starts solved; rows is the original board.
func solveCircular(rows []string, start, deadline time.Time) (Result, string) {
	pushes := firstPushes(rows)
	if len(pushes) == 0 {
		return Result{Reason: "circular: no legal push"}, "circular"
	}
	exe, err := os.Executable()
	if err != nil {
		return Result{Reason: "circular: " + err.Error()}, "circular"
	}
	files := make([]string, len(pushes))
	for i, fp := range pushes {
		f, err := os.CreateTemp("", "soksolv-circular-*.xsb")
		if err != nil {
			return Result{Reason: "circular: " + err.Error()}, "circular"
		}
		fmt.Fprintf(f, "; circular child %d\n%s\n", i+1, strings.Join(fp.rows, "\n"))
		f.Close()
		files[i] = f.Name()
		defer os.Remove(f.Name())
	}
	var args []string // every flag the user set, except the time limit
	flag.Visit(func(f *flag.Flag) {
		if f.Name != "t" {
			args = append(args, "-"+f.Name+"="+f.Value.String())
		}
	})

	nodes := 0
	for slice := time.Second; ; slice *= 2 {
		for i, fp := range pushes {
			left := time.Until(deadline)
			if left < 200*time.Millisecond {
				return Result{Reason: "timeout", Nodes: nodes}, "circular"
			}
			t := min(slice, left)
			cmd := exec.Command(exe, append(append([]string{}, args...), "-t", fmt.Sprintf("%.3f", t.Seconds()), files[i], "1")...)
			cmd.Stderr = nil
			b, _ := cmd.Output() // exit code 1 just means "not solved"
			var o Output
			if json.Unmarshal(b, &o) != nil {
				continue
			}
			nodes += o.Nodes
			circularPeakMB = max(circularPeakMB, o.PeakMemoryMB)
			if o.Solved {
				return Result{Solved: true, Solution: fp.prefix + o.Solution, Nodes: nodes}, "circular:" + o.Strategy
			}
		}
	}
}
