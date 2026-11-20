package main

// exp-744 -revive-root (default off): the portfolio's start becomes a
// variable. The forward lanes (macro, novelty, fa2) post their best live
// board X (most boxes on goals, ties: smaller h) with the moves that reach
// it from the start. When a lane's ring restarts, odd restarts build the
// lane from a copy of the level whose start is X (same walls and goals;
// frozen boxes stay boxes), even restarts from the original start. Every
// family re-roots, the backward ones included, so a backward lane can pack
// from where a forward lane got to. Successive odd restarts of one lane take
// successive distinct posted boards (newest unused first).
//
// Solutions: a lane rooted at X returns moves from X, or moves from the
// start when it completed a meeting whose forward half came from an
// original-rooted lane; an X-rooted forward search posts its meetings with
// X's prefix prepended. relayFix makes every lane result a solution of the
// original level (as is, else with a posted prefix) and drops it, letting
// the ring continue, if neither verifies. Nothing depends on the time limit
// and no thread is added.
//
// Start-dependent state: every solver builds its distance, reach, dead-square
// and room tables from its own Level, and the process-wide cluster/deadzone
// verdict tables are salted with the dead-square mask (-verdict-salt), so an
// X-rooted solver never writes a verdict another root reads under a
// different mask. The macro lane's plan4 is published to the fa2 lane only
// from the original root.

import (
	"flag"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"soksolv/meet"
	"soksolv/verify"
)

var ReviveRoot bool

var relayDebug = os.Getenv("SOKSOLV_RELAY_DEBUG") != ""

var relaySeedFile string

// RelayWalls (exp-744 -revive-root-walls): re-rooted lanes run on the
// residual (frozen-on-goal boxes as walls, exp-739's census setting) instead
// of the full board. A residual solution is a solution from X: the frozen
// boxes never move, and to the player a wall and an immovable box are alike.
var RelayWalls bool
var relayAll bool

// relayFirst: lane expansions of a solver instance at its first post (then doubling).
var relayFirst int64 = 50000

func init() {
	flag.BoolVar(&ReviveRoot, "revive-root", false, "exp-744: forward lanes post their best live board; odd ring restarts of every lane start from a posted board (with its prefix), even ones from the original start")
	flag.StringVar(&relaySeedFile, "revive-root-seed", "", "exp-744 diagnostic: a LURD file (moves from the start) posted as the first board")
	flag.BoolVar(&RelayWalls, "revive-root-walls", false, "exp-744: a re-rooted lane's level has the posted board's frozen-on-goal boxes as walls (a residual; its states meet only lanes of the same residual)")
	flag.BoolVar(&relayAll, "revive-root-all", false, "exp-744 diagnostic: every config (even k too) starts from a posted board")
	flag.Int64Var(&relayFirst, "revive-root-first", relayFirst, "exp-744: a forward solver's first best-board post after this many expansions (then doubling, and at its end)")
}

// rootBoard is a posted start: X's boxes (sorted) and player cell, the moves
// from the original start, and the level/rows rooted there.
type rootBoard struct {
	boxes  []int32
	player int32
	norm   int32 // normalized player (distinctness)
	prefix string
	lv     *Level
	rows   []string
	from   string
	on     int32
	// The residual (frozen-on-goal boxes as walls; nil if it does not parse).
	lvW   *Level
	rowsW []string
	at    float64 // exp-747: seconds since the run's start at the post
	att   int     // exp-747: relay trials run on this board
	// exp-761 -relay-back: a backward post (rows: start -> goals = boxes;
	// tail: the suffix's pushes from boxes, player's region, to the goals).
	back  bool
	dead  bool // exp-761: a back trial proved start -> (B, region) infeasible
	tail  []meet.Push
	depth int
}

func timeSinceRelay() float64 { return time.Since(relayT0).Seconds() }

const relayCap = 64

var relay struct {
	mu     sync.Mutex
	lv     *Level
	rows   []string
	boards []*rootBoard
	used   map[string][]bool
	picks  map[string][]int // lane -> board index per odd restart (trace)
	posts  int
	dups   int
	dead   int
	fixed  int // solutions that needed a prefix
	bad    int // solutions dropped (no root verified)
	n      atomic.Int32
}

// relayReset starts a portfolio run's record.
func relayReset(lv *Level, rows []string) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	relay.lv, relay.rows = nil, nil
	if ReviveRoot || RelayTrial {
		relay.lv, relay.rows = lv, rows
	}
	relayPostOn = (ReviveRoot || RelayTrial) && lv != nil
	relayEndLv = nil
	if relayEnd >= 0 {
		relayEndLv = lv // exp-761 back-trial child
	}
	relay.boards = nil
	relay.used = map[string][]bool{}
	relay.picks = map[string][]int{}
	relay.posts, relay.dups, relay.dead, relay.fixed, relay.bad = 0, 0, 0, 0, 0
	relay.n.Store(0)
	relayT0 = time.Now()
	if ReviveRoot && lv != nil && relaySeedFile != "" {
		if b, err := os.ReadFile(relaySeedFile); err == nil {
			path := strings.TrimSpace(string(b))
			if boxes, pl, ok := applyPath(lv, path); ok {
				relay.mu.Unlock()
				relayPost(boxes, pl, path, "seed", 0, seedFrozen(lv, boxes))
				relay.mu.Lock()
			}
		}
	}
}

// applyPath plays LURD moves from lv's start: the boxes and player after them.
func applyPath(lv *Level, path string) ([]int32, int32, bool) {
	occ := cellSet(len(lv.Wall), lv.Boxes)
	p := lv.Player
	for i := 0; i < len(path); i++ {
		var d int32
		switch path[i] | 0x20 {
		case 'u':
			d = -int32(lv.W)
		case 'd':
			d = int32(lv.W)
		case 'l':
			d = -1
		case 'r':
			d = 1
		default:
			return nil, 0, false
		}
		y := p + d
		if lv.Wall[y] {
			return nil, 0, false
		}
		if occ[y] {
			if path[i] >= 'a' || lv.Wall[y+d] || occ[y+d] {
				return nil, 0, false
			}
			occ[y], occ[y+d] = false, true
		}
		p = y
	}
	var boxes []int32
	for c, o := range occ {
		if o {
			boxes = append(boxes, int32(c))
		}
	}
	return boxes, p, true
}

// relayPick is the root of lane's config k: nil (the original start) for
// even k or when nothing was posted, else the newest board this lane has not
// started from yet (all used: the newest).
func relayPick(lane string, k int) *rootBoard {
	if !ReviveRoot || k%2 == 0 && !relayAll || relay.n.Load() == 0 {
		return nil
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.boards) == 0 {
		return nil
	}
	u := relay.used[lane]
	for len(u) < len(relay.boards) {
		u = append(u, false)
	}
	pick := len(relay.boards) - 1
	for i := len(relay.boards) - 1; i >= 0; i-- {
		if !u[i] {
			pick = i
			break
		}
	}
	u[pick] = true
	relay.used[lane] = u
	relay.picks[lane] = append(relay.picks[lane], pick)
	return relay.boards[pick]
}

// rootOf: the level and rows of root b (nil: the original).
func rootOf(b *rootBoard, lv *Level, rows []string) (*Level, []string) {
	if b == nil {
		return lv, rows
	}
	if RelayWalls && b.lvW != nil {
		return b.lvW, b.rowsW
	}
	return b.lv, b.rows
}

// rootRowsOf: the rows a reverse phase parses for root b (walls variant), or nil.
func rootRowsW(b *rootBoard) []string {
	if b != nil && RelayWalls && b.lvW != nil {
		return b.rowsW
	}
	return nil
}

// relayPrefixFor is the posted prefix of the board whose start lv has, ""
// for the original start.
func relayPrefixFor(lv *Level) string {
	if relay.n.Load() == 0 {
		return ""
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	for _, b := range relay.boards {
		if b.player == lv.Player && len(b.boxes) == len(lv.Boxes) && sortedEqual(b.boxes, lv.Boxes) {
			return b.prefix
		}
		if b.lvW != nil && b.player == lv.Player && len(b.lvW.Boxes) == len(lv.Boxes) && sortedEqual(b.lvW.Boxes, lv.Boxes) {
			return b.prefix
		}
	}
	return ""
}

func sortedEqual(sorted, x []int32) bool {
	if slices.IsSorted(x) {
		return slices.Equal(sorted, x)
	}
	y := slices.Clone(x)
	slices.Sort(y)
	return slices.Equal(sorted, y)
}

// relayRootPrefix: this forward solver's prefix from the original start (cached).
func (s *solver) relayRootPrefix() string {
	if !ReviveRoot || s.pull {
		return ""
	}
	if !s.rootPfxOK {
		s.rootPfx, s.rootPfxOK = relayPrefixFor(s.lv), true
	}
	return s.rootPfx
}

// relayOriginal: s starts from the original start.
func (s *solver) relayOriginal() bool {
	return !ReviveRoot || s.relayRootPrefix() == ""
}

// relaySolution returns sol as a verified solution of the original level:
// as is, else after a posted prefix ("" if none verifies).
func relaySolution(sol string) (string, bool) {
	relay.mu.Lock()
	rows, boards := relay.rows, slices.Clone(relay.boards)
	relay.mu.Unlock()
	if rows == nil {
		return sol, true
	}
	if verify.Check(rows, sol) == nil {
		return sol, true
	}
	for i := len(boards) - 1; i >= 0; i-- {
		if full := boards[i].prefix + sol; verify.Check(rows, full) == nil {
			return full, true
		}
	}
	return "", false
}

// relayFix makes a lane result a solution of the original level, or turns
// it into a non-solve ("reroot-invalid") that the lane's ring revives from.
func relayFix(r Result) Result {
	if r.Solved && !relayEndOK(r.Solution) {
		return Result{Reason: "reroot-invalid", Nodes: r.Nodes, Stats: r.Stats} // exp-761: wrong end region
	}
	if !r.Solved || !ReviveRoot || relay.n.Load() == 0 {
		return r
	}
	sol, ok := relaySolution(r.Solution)
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if !ok {
		relay.bad++
		return Result{Reason: "reroot-invalid", Nodes: r.Nodes, Stats: r.Stats}
	}
	if sol != r.Solution {
		relay.fixed++
	}
	r.Solution = sol
	return r
}

// checkSol is verify.Check for a lane's rows that also accepts a solution
// of the original level or of a posted root (-revive-root: a re-rooted lane
// may complete a meeting whose forward half starts at the real start).
func checkSol(rows []string, sol string) bool {
	if !relayEndOK(sol) {
		return false
	}
	if verify.Check(rows, sol) == nil {
		return true
	}
	if !ReviveRoot || relay.n.Load() == 0 {
		return false
	}
	_, ok := relaySolution(sol)
	return ok
}

// relayNote (forward lanes, -revive-root) tracks the solver's best board
// (node id holding boxes) and posts it at its expansion milestones.
func (s *solver) relayNote(id int32, boxes []int32) {
	var on, h int32
	for _, b := range boxes {
		if s.lv.Goal[b] {
			on++
		}
		h += s.dist[b]
	}
	if s.rlID == 0 || on > s.rlOn || on == s.rlOn && h < s.rlH {
		s.rlOn, s.rlH, s.rlID = on, h, id+1
	}
	if s.rlNext == 0 {
		s.rlNext = relayFirst
		if !ReviveRoot {
			s.rlNext = relayTrialFirst // exp-747 -relay-trial: 100k, then doubling
		}
	}
	if RelayFzSet {
		s.fzNote(id, boxes, on, h) // exp-767
	}
	if s.expandedN >= s.rlNext {
		s.rlNext *= 2
		s.relayFlush()
		if RelayFzSet {
			s.fzFlush()
		}
	}
}

// relayPoster: s is a forward lane solver that posts boards.
func (s *solver) relayPoster() bool {
	return relayPostOn && !s.pull && !s.isolated && s.tgtFull == nil && (s.laneID == 1 || s.laneID == 5 || (ReviveRoot || RelayFA2) && s.laneID == 12)
}

// relayFlush posts the solver's best board if it is live and not the start.
func (s *solver) relayFlush() {
	if !s.relayPoster() || s.rlID == 0 || relay.n.Load() >= relayCap {
		return
	}
	s.relayFlushID(s.rlID-1, s.rlOn, "", nil)
}

// relayFlushID posts node id's board (on boxes on goals) as lane+tag if it is
// live and accept (nil: all) takes its frozen-on-goal set.
func (s *solver) relayFlushID(id, rlOn int32, tag string, accept func(frozen []int32) bool) {
	relay.mu.Lock()
	lv0 := relay.lv
	relay.mu.Unlock()
	if lv0 == nil || len(s.lv.Wall) != len(lv0.Wall) {
		return
	}
	if relayDebug {
		println("relay: flush lane", s.laneID, "exp", s.expandedN, "best", id, "on", rlOn, "h", s.rlH, tag)
	}
	saved := slices.Clone(s.occ)
	clear(s.occ)
	best := make([]int32, s.n)
	s.unpack(id, best)
	for _, b := range best {
		s.occ[b] = true
	}
	var frozen []int32
	for _, b := range best {
		if s.lv.Goal[b] {
			off := false
			if s.frozen(b, &off) {
				frozen = append(frozen, b)
			}
		}
	}
	clear(s.occ)
	if accept != nil && !accept(frozen) {
		copy(s.occ, saved)
		return
	}
	pm, pa := s.stats.PathMoves, s.stats.PathAdvisors
	path := s.reconstruct(id)
	s.stats.PathMoves, s.stats.PathAdvisors = pm, pa
	copy(s.occ, saved)
	if path == "" {
		return
	}
	pl := s.lv.Player
	for i := 0; i < len(path); i++ {
		switch path[i] | 0x20 {
		case 'u':
			pl += s.dirs[0]
		case 'd':
			pl += s.dirs[1]
		case 'l':
			pl += s.dirs[2]
		case 'r':
			pl += s.dirs[3]
		}
	}
	nc := len(s.lv.Wall)
	walls := cellSet(nc, frozen)
	var rb, rg []int32
	for _, x := range best {
		if !walls[x] {
			rb = append(rb, x)
		}
	}
	for c := range nc {
		if s.lv.Goal[c] && !walls[c] {
			rg = append(rg, int32(c))
		}
	}
	if !residualLive(s.lv, walls, rb, rg) {
		relay.mu.Lock()
		relay.dead++
		relay.mu.Unlock()
		return
	}
	lane := map[int]string{1: "macro", 5: "novelty", 12: "fa2"}[s.laneID] + tag
	if s.relayRootPrefix() == "" && len(best) != len(lv0.Boxes) {
		return // a residual solver whose root is unknown
	}
	if len(best) != len(lv0.Boxes) {
		// a residual (walls) solver: its frozen walls are boxes of the full board
		for c := range nc {
			if s.lv.Wall[c] && !lv0.Wall[c] && lv0.Goal[c] {
				best = append(best, int32(c))
				frozen = append(frozen, int32(c))
			}
		}
		if len(best) != len(lv0.Boxes) {
			return
		}
	}
	if relayDumpDir != "" {
		relayDumpExp.Store(s.expandedN)
	}
	relayPost(best, pl, s.relayRootPrefix()+path, lane, rlOn, frozen)
}

// relayPost records board (boxes, player) reached by prefix, if new.
func relayPost(boxes []int32, player int32, prefix, from string, on int32, frozen []int32) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	lv := relay.lv
	if lv == nil || len(relay.boards) >= relayCap {
		return
	}
	bx := slices.Clone(boxes)
	slices.Sort(bx)
	if lv.Wall[player] || slices.ContainsFunc(bx, func(c int32) bool { return lv.Wall[c] }) {
		return
	}
	if slices.Equal(bx, sortedCopy(lv.Boxes)) && sameRegion(lv, bx, player, lv.Player) {
		return // the start itself
	}
	norm := normPlayer(lv, bx, player)
	for _, b := range relay.boards {
		if !b.back && b.norm == norm && slices.Equal(b.boxes, bx) {
			relay.dups++
			return
		}
	}
	nlv := &Level{W: lv.W, H: lv.H, Wall: slices.Clone(lv.Wall), Goal: slices.Clone(lv.Goal), Boxes: bx, Player: player}
	rb := &rootBoard{boxes: bx, player: player, norm: norm, prefix: prefix, lv: nlv, rows: rootRows(relay.rows, lv, bx, player, nil), from: from, on: on}
	if len(frozen) > 0 {
		rb.rowsW = rootRows(relay.rows, lv, bx, player, frozen)
		if w, err := parseLevel(rb.rowsW); err == nil && w.W == lv.W && w.H == lv.H {
			rb.lvW = w
		}
	}
	rb.at = time.Since(relayT0).Seconds()
	if relayDumpDir != "" {
		relayDump(rb, len(relay.boards), frozen) // exp-767 census
	}
	relay.boards = append(relay.boards, rb)
	relay.posts++
	relay.n.Store(int32(len(relay.boards)))
	select { // exp-747: wake the relay scheduler
	case relayWake <- struct{}{}:
	default:
	}
}

func sortedCopy(x []int32) []int32 {
	y := slices.Clone(x)
	slices.Sort(y)
	return y
}

// normPlayer is the smallest cell the player reaches with boxes in place.
func normPlayer(lv *Level, boxes []int32, player int32) int32 {
	occ := make([]bool, len(lv.Wall))
	for _, b := range boxes {
		occ[b] = true
	}
	seen := make([]bool, len(lv.Wall))
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	q := []int32{player}
	seen[player] = true
	m := player
	for i := 0; i < len(q); i++ {
		c := q[i]
		m = min(m, c)
		for _, d := range dirs {
			if y := c + d; !lv.Wall[y] && !occ[y] && !seen[y] {
				seen[y] = true
				q = append(q, y)
			}
		}
	}
	return m
}

func sameRegion(lv *Level, boxes []int32, a, b int32) bool {
	return normPlayer(lv, boxes, a) == normPlayer(lv, boxes, b)
}

// rootRows rewrites the original rows with boxes and the player moved
// (walls, goals and widths unchanged, so cells and meet keys agree).
func rootRows(rows []string, lv *Level, boxes []int32, player int32, walls []int32) []string {
	box := cellSet(len(lv.Wall), boxes)
	wall := cellSet(len(lv.Wall), walls)
	out := make([]string, len(rows))
	for y, r := range rows {
		b := []byte(r)
		for x := range b {
			ch := b[x]
			switch ch {
			case ' ', '-', '_', '.', '$', '*', '@', '+':
			default:
				continue
			}
			goal := ch == '.' || ch == '*' || ch == '+'
			c := int32((y+1)*lv.W + x + 1)
			switch {
			case wall[c]:
				ch = '#'
			case box[c] && goal:
				ch = '*'
			case box[c]:
				ch = '$'
			case c == player && goal:
				ch = '+'
			case c == player:
				ch = '@'
			case goal:
				ch = '.'
			default:
				if ch != '-' && ch != '_' {
					ch = ' '
				}
			}
			b[x] = ch
		}
		out[y] = string(b)
	}
	return out
}

// relayReport writes the record's diagnostics to a trace extra map.
func relayReport() map[string]any {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.lv == nil {
		return nil
	}
	from := make([]string, len(relay.boards))
	on := make([]int32, len(relay.boards))
	pre := make([]int, len(relay.boards))
	for i, b := range relay.boards {
		from[i], on[i], pre[i] = b.from, b.on, len(b.prefix)
	}
	return map[string]any{"posts": relay.posts, "dups": relay.dups, "dead": relay.dead, "fixed": relay.fixed, "bad": relay.bad, "picks": relay.picks, "from": from, "on": on, "prefix_len": pre}
}

// seedFrozen: the seed board's frozen-on-goal boxes (a solver's freeze test).
func seedFrozen(lv *Level, boxes []int32) []int32 {
	s := newSolver(lv)
	for _, b := range boxes {
		s.occ[b] = true
	}
	var fr []int32
	for _, b := range boxes {
		off := false
		if lv.Goal[b] && s.frozen(b, &off) {
			fr = append(fr, b)
		}
	}
	return fr
}
