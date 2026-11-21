package diagdead

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type board struct {
	w, h         int
	wall, goal   []bool
	occ          []bool
	player       int32
	boxes, goals int
}

// parse reads rows with # wall, $ box, * box on goal, . goal, @ player, + player on goal.
func parse(rows []string) *board {
	b := &board{h: len(rows)}
	for _, r := range rows {
		b.w = max(b.w, len(r))
	}
	n := b.w * b.h
	b.wall, b.goal, b.occ = make([]bool, n), make([]bool, n), make([]bool, n)
	b.player = -1
	for y, r := range rows {
		for x := 0; x < b.w; x++ {
			c := y*b.w + x
			ch := byte('#')
			if x < len(r) {
				ch = r[x]
			}
			switch ch {
			case '#':
				b.wall[c] = true
			case '$':
				b.occ[c] = true
				b.boxes++
			case '*':
				b.occ[c], b.goal[c] = true, true
				b.boxes++
				b.goals++
			case '.':
				b.goal[c] = true
				b.goals++
			case '@':
				b.player = int32(c)
			case '+':
				b.player, b.goal[c] = int32(c), true
				b.goals++
			}
		}
	}
	return b
}

// dead reports whether Dead fires for any box of the board as the pushed box t.
func (b *board) dead() bool {
	c := New(b.wall, b.goal, b.w)
	for t, o := range b.occ {
		if o && c.Dead(b.occ, int32(t), b.player) {
			return true
		}
	}
	return false
}

// solvable exhaustively searches pushes; ok=false when the state cap was hit.
func (b *board) solvable(maxStates int) (solvable, ok bool) {
	n := b.w * b.h
	dirs := [4]int{-b.w, b.w, -1, 1}
	stackBuf := make([]int, 0, n)
	reach := func(boxes []bool, p int, out []bool) int {
		for i := range out {
			out[i] = false
		}
		out[p] = true
		lo := p
		stack := append(stackBuf[:0], p)
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range dirs {
				y := x + d
				if b.wall[y] || boxes[y] || out[y] {
					continue
				}
				out[y] = true
				lo = min(lo, y)
				stack = append(stack, y)
			}
		}
		return lo
	}
	// reachLo is reach for callers that only need the lowest reachable cell: a visit stamp
	// replaces clearing the whole mark array on every call.
	stamp := make([]uint32, n)
	var gen uint32
	reachLo := func(boxes []bool, p int) int {
		gen++
		stamp[p] = gen
		lo := p
		stack := append(stackBuf[:0], p)
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range dirs {
				y := x + d
				if b.wall[y] || boxes[y] || stamp[y] == gen {
					continue
				}
				stamp[y] = gen
				lo = min(lo, y)
				stack = append(stack, y)
			}
		}
		return lo
	}
	keyBuf := make([]byte, 0, 64)
	encode := func(boxes []bool, p int) []byte { // valid until the next call
		buf := keyBuf[:0]
		for c, o := range boxes {
			if o {
				buf = append(buf, byte(c), byte(c>>8))
			}
		}
		return append(buf, byte(p), byte(p>>8))
	}
	solved := func(boxes []bool) bool {
		for c, o := range boxes {
			if o && !b.goal[c] {
				return false
			}
		}
		return true
	}
	type node struct {
		boxes []bool
		p     int
	}
	start := node{slices_clone(b.occ), 0}
	start.p = reachLo(start.boxes, int(b.player))
	seen := map[string]bool{string(encode(start.boxes, start.p)): true}
	queue := []node{start}
	region := make([]bool, n)
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if solved(k.boxes) {
			return true, true
		}
		reach(k.boxes, k.p, region)
		for bx := 0; bx < n; bx++ {
			if !k.boxes[bx] {
				continue
			}
			for _, d := range dirs {
				from, to := bx-d, bx+d
				if !region[from] || b.wall[to] || k.boxes[to] {
					continue
				}
				k.boxes[bx], k.boxes[to] = false, true // apply in place, undo below
				np := reachLo(k.boxes, bx)
				key := encode(k.boxes, np)
				if !seen[string(key)] {
					if len(seen) >= maxStates {
						return false, false
					}
					seen[string(key)] = true
					queue = append(queue, node{slices_clone(k.boxes), np})
				}
				k.boxes[bx], k.boxes[to] = true, false
			}
		}
	}
	return false, true
}

func slices_clone(a []bool) []bool { return append([]bool(nil), a...) }

func TestHandMade(t *testing.T) {
	// Each board is padded by walls; dead is Dead() and unsolv is the exhaustive truth
	// (checked when the board is small enough).
	cases := []struct {
		name string
		rows []string
		dead bool
	}{
		{"k1 R chain, caps on goals, pair boxes off goal", []string{
			"#########",
			"#       #",
			"#  *$   #",
			"#  $.$  #",
			"#   $*  #",
			"#...    #",
			"# @     #",
			"#########"}, true},
		{"k1 near miss: inner cell open below", []string{
			"#########",
			"#       #",
			"#  *$   #",
			"#  $.$  #",
			"#       #",
			"#..     #",
			"# @     #",
			"#########"}, false},
		{"k1 near miss: every box on a goal", []string{
			"#########",
			"#       #",
			"#  **   #",
			"#  * *  #",
			"#   **  #",
			"#       #",
			"# @     #",
			"#########"}, false},
		{"k1 near miss: end pair of walls closes the chain", []string{
			"#########",
			"#       #",
			"#  *$   #",
			"#  $.## #",
			"#   ##  #",
			"#.      #",
			"# @     #",
			"#########"}, false},
		{"k1 near miss: wall-closed start, inner cell is a goal", []string{
			"#########",
			"#  ##   #",
			"#  #.$  #",
			"#   $*  #",
			"#.      #",
			"# @     #",
			"#########"}, false},
		{"k1 pocket: wall-closed start, inner cell is not a goal", []string{
			"#########",
			"#  ##   #",
			"#  # $  #",
			"#   $*  #",
			"#.      #",
			"# @     #",
			"#########"}, true},
		{"k2 R chain, all pair boxes off goal", []string{
			"##########",
			"#        #",
			"#  *$    #",
			"#  $.$   #",
			"#   $.$  #",
			"#    $*  #",
			"#....    #",
			"# @      #",
			"##########"}, true},
		{"k2 near miss: middle pair on goals", []string{
			"##########",
			"#        #",
			"#  *$    #",
			"#  $.*   #",
			"#   *.$  #",
			"#    $*  #",
			"#..      #",
			"# @      #",
			"##########"}, false},
		{"k3 L chain (the Sasquatch II 17 shape)", []string{
			"###########",
			"#         #",
			"#     $*  #",
			"#    $.$  #",
			"#   $.$   #",
			"#  $.$    #",
			"#  *$     #",
			"#.....    #",
			"# @       #",
			"###########"}, true},
		{"k3 near miss: open start cap", []string{
			"###########",
			"#         #",
			"#     $   #",
			"#    $.$  #",
			"#   $.$   #",
			"#  $.$    #",
			"#  *$     #",
			"#...      #",
			"# @       #",
			"###########"}, false},
	}
	for _, tc := range cases {
		b := parse(tc.rows)
		if got := b.dead(); got != tc.dead {
			t.Errorf("%s: Dead=%v want %v", tc.name, got, tc.dead)
		}
		if tc.dead && b.boxes == b.goals && b.w*b.h <= 80 {
			if sol, ok := b.solvable(400_000); !ok || sol {
				t.Errorf("%s: flagged dead but exhaustive search says solvable=%v complete=%v", tc.name, sol, ok)
			}
		}
	}
}

// TestSoundRandom: every board the rule calls dead is unsolvable, checked exhaustively on random
// small boards that contain a planted chain.
func TestSoundRandom(t *testing.T) {
	const W, H = 8, 8
	rng := rand.New(rand.NewSource(459))
	trials := 6000
	if v, err := strconv.Atoi(os.Getenv("SOKSOLV_DIAG_TRIALS")); err == nil && v > 0 { // fulltest.sh: a prefix of the same seeded boards
		trials = v
	}
	if testing.Short() {
		trials = 120
	}
	var flagged, verified, inconclusive, unsolvableAlive, total int
	var byK [4]int
	type job struct {
		trial int
		rows  []string
		b     *board
		k     int
	}
	var jobs []job
	var unsound []string
	for trial := 0; trial < trials; trial++ {
		g := make([][]byte, H)
		for y := range g {
			g[y] = []byte(strings.Repeat("#", W))
			if y > 0 && y < H-1 {
				for x := 1; x < W-1; x++ {
					g[y][x] = ' '
				}
			}
		}
		// random interior walls
		for y := 1; y < H-1; y++ {
			for x := 1; x < W-1; x++ {
				if rng.Intn(100) < 8 {
					g[y][x] = '#'
				}
			}
		}
		k := 1 + rng.Intn(3)
		delta := 1 - 2*rng.Intn(2) // +1: R chain, -1: L chain
		y0 := 2 + rng.Intn(H-4-k)
		x0 := 2 + rng.Intn(W-4-k)
		if delta < 0 {
			x0 = k + rng.Intn(W-2-k)
		}
		put := func(y, x int, kind int) { // 0 wall, 1 box, 2 box on goal
			switch kind {
			case 0:
				g[y][x] = '#'
			case 1:
				g[y][x] = '$'
			default:
				g[y][x] = '*'
			}
		}
		pick := func() int {
			switch r := rng.Intn(10); {
			case r < 2:
				return 0
			case r < 6:
				return 1
			default:
				return 2
			}
		}
		okBoard := true
		for m := 0; m < k; m++ {
			y, x := y0+m, x0+int(delta)*m
			if y < 1 || y >= H-1 || x < 1 || x >= W-1 {
				okBoard = false
				break
			}
			g[y][x] = ' '
			if rng.Intn(2) == 0 {
				g[y][x] = '.'
			}
			put(y-1, x, pick())
			put(y, x+int(-delta), pick())
		}
		if !okBoard {
			continue
		}
		yl, xl := y0+k-1, x0+int(delta)*(k-1)
		put(yl+1, xl, pick())
		put(yl, xl+int(delta), pick())
		if rng.Intn(5) != 0 {
			put(y0-1, x0-int(delta), pick())
		}
		if rng.Intn(5) != 0 {
			put(yl+1, xl+int(delta), pick())
		}
		// extra boxes
		for e := rng.Intn(3); e > 0; e-- {
			y, x := 1+rng.Intn(H-2), 1+rng.Intn(W-2)
			if g[y][x] == ' ' {
				g[y][x] = "$*"[rng.Intn(2)]
			}
		}
		rows := make([]string, H)
		for y := range g {
			rows[y] = string(g[y])
		}
		b := parse(rows)
		if b.boxes < b.goals {
			// too many goals: drop inner goals until equal
			for c := range b.goal {
				if b.goals > b.boxes && b.goal[c] && !b.occ[c] {
					b.goal[c] = false
					b.goals--
				}
			}
		}
		if b.boxes < b.goals {
			continue
		}
		// add goals on empty floor cells until goals == boxes
		for tries := 0; b.goals < b.boxes && tries < 200; tries++ {
			c := rng.Intn(len(b.goal))
			if !b.wall[c] && !b.occ[c] && !b.goal[c] {
				b.goal[c] = true
				b.goals++
			}
		}
		if b.goals != b.boxes {
			continue
		}
		// player on a random free floor cell
		b.player = -1
		for tries := 0; tries < 200 && b.player < 0; tries++ {
			c := rng.Intn(len(b.wall))
			if !b.wall[c] && !b.occ[c] {
				b.player = int32(c)
			}
		}
		if b.player < 0 {
			continue
		}
		total++
		jobs = append(jobs, job{trial, rows, b, k})
	}
	// Board generation above is sequential (one seeded rng, so the board set is unchanged);
	// the dead / solvable checks are independent per board and run on a worker pool.
	// SOKSOLV_SHARD=i/n (diag/screen/fulltest.sh) checks every n-th board in this process.
	si, sn := 0, 1
	fmt.Sscanf(os.Getenv("SOKSOLV_SHARD"), "%d/%d", &si, &sn)
	workers := min(4, runtime.GOMAXPROCS(0))
	if sn > 1 {
		workers = 1 // the sharding script already runs one process per core
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	next := make(chan job)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range next {
				if !j.b.dead() {
					continue
				}
				sol, ok := j.b.solvable(600_000)
				mu.Lock()
				flagged++
				byK[j.k]++
				switch {
				case !ok:
					inconclusive++
				case sol:
					unsound = append(unsound, fmt.Sprintf("UNSOUND: flagged dead but solvable (trial %d)\n%s", j.trial, strings.Join(j.rows, "\n")))
				default:
					verified++
				}
				mu.Unlock()
			}
		}()
	}
	for ji, j := range jobs {
		if ji%sn == si {
			next <- j
		}
	}
	close(next)
	wg.Wait()
	for _, u := range unsound {
		t.Error(u)
	}
	_ = unsolvableAlive
	t.Logf("boards %d flagged dead %d (planted k=1: %d, k=2: %d, k=3: %d), verified unsolvable %d, inconclusive %d", total, flagged, byK[1], byK[2], byK[3], verified, inconclusive)
	if flagged < 20 {
		t.Fatalf("only %d boards flagged: the generator has no teeth", flagged)
	}
	if inconclusive*6 > flagged {
		t.Fatalf("too many inconclusive checks: %d of %d", inconclusive, flagged)
	}
}

// TestBoardsFile prints our verdict for every board of an xsb file with ';' header lines
// (SOKSOLV_DIAG_BOARDS=path), one "index:dead|alive" per board, for comparison with
// Festival's deadoracle.
func TestBoardsFile(t *testing.T) {
	path := os.Getenv("SOKSOLV_DIAG_BOARDS")
	if path == "" {
		t.Skip("SOKSOLV_DIAG_BOARDS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	var rows []string
	flush := func() {
		if len(rows) > 0 {
			v := "alive"
			if parse(rows).dead() {
				v = "dead"
			}
			out = append(out, fmt.Sprintf("%d:%s", len(out)+1, v))
		}
		rows = nil
	}
	for _, ln := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(ln, ";"):
			flush()
		case strings.HasPrefix(ln, "#"):
			rows = append(rows, ln)
		}
	}
	flush()
	fmt.Println(strings.Join(out, " "))
}
