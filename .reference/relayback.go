package main

// exp-761 -relay-back (default off; on top of -relay-trial): backward relay
// trials. The hub-pull lane's pull tree holds states B (boxes on squares the
// backward search reached from the goals) whose pull path, replayed forward,
// is a suffix from B to the goals. Between its pull rounds, at doubling pull
// expansion milestones (relayBackFirst = 20k, then x2) and when the pull tree ends,
// the lane scans its new nodes into hub strata tables (hubstrata.go; v0 and
// v1, the best node per signature) and posts the deepest unposted tip to the relay bank: B, the tip's player cell (its
// region is where the suffix starts) and the suffix's pushes. Distinct by box
// set and player region, at most relayBackCap per run.
//
// The relay scheduler (relaytrial.go) picks back posts like forward ones. A
// back trial's child solves a rewritten level: original walls, start boxes
// and player, goals = B's squares. The composed solution is the child's
// moves, then the suffix walked from the child's final player
// (meet.Complete), verified on the original level. If the child ends with
// the player outside B's region (Complete fails), its end state is posted as
// a forward relay board (prefix = child's moves, original goals) instead.
//
// The scan runs on the lane's own goroutine while its pull solver is idle,
// so the search itself is unchanged; with the flag off nothing runs.

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"soksolv/meet"
)

var (
	RelayBack      bool
	relayBackPull  bool  // -relay-back-pull: the pull lane posts too (its deepest nodes)
	relayBackFirst int64 = 20000
)

const relayBackCap = 12

// relayBackQ: depth quantiles posted per milestone (0 = the deepest unposted tip).
var relayBackQ = []float64{0}

// relayEnd (internal, a back-trial child): a solution counts only if it
// ends with the player in this cell's region (the suffix's start region).
// relayFix and checkSol turn a wrong-region solution into a non-solve, so
// the lane's ring revives and searches on.
var (
	relayEnd   = -1
	relayEndLv *Level
)

func relayEndOK(sol string) bool {
	lv := relayEndLv
	if lv == nil {
		return true
	}
	boxes, pl, ok := applyPath(lv, sol)
	return !ok || sameRegion(lv, boxes, pl, int32(relayEnd))
}

func init() {
	flag.BoolVar(&relayBackPull, "relay-back-pull", false, "exp-761: with -relay-back, the pull lane also posts its deepest pull nodes (off: hub-pull only; offline its tips failed at 16 s)")
	flag.IntVar(&relayEnd, "relay-end", -1, "exp-761 (internal): back-trial child; a solution must end with the player in this cell's region")
	flag.BoolVar(&RelayBack, "relay-back", true, "exp-761 (default on in the 2026-11-14 benchmark build; -relay-back=false = incumbent 7d19656f9 behaviour): hub-pull posts pull-tree strata tips B (deepest first, at 20k pull expansions then doubling) to the relay bank; a trial's child solves start -> goals=B, composed with B's pull suffix")
}

// relayBackState is one hub-pull pull solver's posting state.
type relayBackState struct {
	next    int64
	scanned int
	st      []*strata // pf-ordered pull trees (hub-pull): best node per stratum
	depth   []uint16  // depth per scanned node (parents come first)
	deep    []int32   // trees without pf order (the pull lane): the deepest nodes seen
	final   bool
	tried   map[int32]bool
}

// newRelayBack: posting state for the hub-pull pull solver ps, or nil.
func newRelayBack(ps *solver, lv *Level) *relayBackState {
	if !RelayBack || !relayPostOn || ReviveRoot || relayChild || !ps.pull {
		return nil
	}
	relay.mu.Lock()
	lv0 := relay.lv
	relay.mu.Unlock()
	if lv0 == nil || lv != lv0 {
		return nil
	}
	rb := &relayBackState{next: relayBackFirst, tried: map[int32]bool{}}
	if ps.pfOn { // strata order needs pfBest
		rb.st = []*strata{newStrata(ps, 0), newStrata(ps, 1)}
	}
	return rb
}

// step runs after a pull round (ps idle): at a milestone or at the pull
// tree's end, scan new nodes and post tips.
func (rb *relayBackState) step(ps *solver, done bool) {
	if rb == nil || relayBackCount() >= relayBackCap {
		return
	}
	if ps.expandedN < rb.next && !(done && !rb.final) {
		return
	}
	if done {
		rb.final = true
	}
	if relayDebug {
		println("relay-back: step exp", ps.expandedN, "nodes", len(ps.player), "done", done)
	}
	for rb.next <= ps.expandedN {
		rb.next *= 2
	}
	n := len(ps.player)
	for id := rb.scanned; id < n; id++ {
		d := uint16(0)
		if p := ps.parent[id]; p >= 0 && int(p) < id {
			d = min(rb.depth[p]+1, 65535)
		}
		rb.depth = append(rb.depth, d)
		for _, t := range rb.st {
			t.offer(ps, int32(id))
		}
		if rb.st == nil && d > 0 && !ps.dead[id] {
			if len(rb.deep) < strataMax {
				rb.deep = append(rb.deep, int32(id))
			} else if w := slices.MinFunc(rb.deep, func(a, b int32) int { return int(rb.depth[a]) - int(rb.depth[b]) }); rb.depth[w] < d {
				rb.deep[slices.Index(rb.deep, w)] = int32(id)
			}
		}
	}
	rb.scanned = n
	type cand struct {
		id    int32
		depth int
	}
	var cs []cand
	add := func(id int32) {
		if id < 0 || int(id) >= n || ps.dead[id] || slices.ContainsFunc(cs, func(c cand) bool { return c.id == id }) {
			return
		}
		cs = append(cs, cand{id, int(rb.depth[id])})
	}
	add(ps.frontier.best)
	for _, t := range rb.st {
		for _, id := range t.best {
			add(id)
		}
	}
	for _, id := range rb.deep {
		add(id)
	}
	if len(cs) == 0 {
		return
	}
	slices.SortFunc(cs, func(a, b cand) int { // deepest first, newest on ties
		if a.depth != b.depth {
			return b.depth - a.depth
		}
		return int(b.id - a.id)
	})
	for _, q := range relayBackQ {
		for i := int(q * float64(len(cs)-1)); i < len(cs); i++ {
			c := cs[i]
			if rb.tried[c.id] || c.depth == 0 {
				continue
			}
			rb.tried[c.id] = true
			boxes := make([]int32, ps.n)
			ps.unpack(c.id, boxes)
			goal := cellSet(len(ps.lv.Wall), ps.lv.Boxes) // the pull tree's root: the real goals
			on := int32(0)
			for _, b := range boxes {
				if goal[b] {
					on++
				}
			}
			ok := relayPostBack(boxes, ps.player[c.id], ps.pullTail(c.id), on, c.depth, map[int]string{6: "pull", 9: "hub-pull"}[ps.laneID])
			if relayDebug {
				println("relay-back: post id", c.id, "depth", c.depth, "on", on, "ok", ok, "of", len(cs))
			}
			if ok {
				break
			}
		}
	}
}

func relayBackCount() int {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	k := 0
	for _, b := range relay.boards {
		if b.back {
			k++
		}
	}
	return k
}

// relayPostBack records tip B (boxes, player, suffix) if new. Its child
// level: original walls, start boxes and player, goals = B.
func relayPostBack(boxes []int32, player int32, tail []meet.Push, on int32, depth int, from string) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	lv := relay.lv
	if lv == nil || len(relay.boards) >= relayCap {
		return false
	}
	bx := slices.Clone(boxes)
	slices.Sort(bx)
	if lv.Wall[player] || slices.ContainsFunc(bx, func(c int32) bool { return lv.Wall[c] }) || slices.Equal(bx, sortedCopy(lv.Boxes)) {
		return false
	}
	norm := normPlayer(lv, bx, player)
	k := 0
	for _, b := range relay.boards {
		if b.back {
			k++
			if b.norm == norm && slices.Equal(b.boxes, bx) {
				relay.dups++
				return false
			}
		}
	}
	if k >= relayBackCap {
		return false
	}
	rows := goalRows(relay.rows, lv, bx)
	if w, err := parseLevel(rows); err != nil || w.W != lv.W || w.H != lv.H {
		return false
	}
	rb := &rootBoard{boxes: bx, player: player, norm: norm, rows: rows, from: from, on: on, back: true, tail: slices.Clone(tail), depth: depth}
	rb.at = timeSinceRelay()
	if d := os.Getenv("SOKSOLV_RELAY_BACK_DUMP"); d != "" { // diagnostic: the child level and its -relay-end cell
		os.WriteFile(fmt.Sprintf("%s/back-%02d-%s-d%d-end%d.xsb", d, len(relay.boards), from, depth, player), []byte(strings.Join(rows, "\n")+"\n"), 0o644)
	}
	relay.boards = append(relay.boards, rb)
	relay.posts++
	relay.n.Store(int32(len(relay.boards)))
	select {
	case relayWake <- struct{}{}:
	default:
	}
	return true
}

// goalRows rewrites the original rows with goals = goals (walls, start boxes
// and player unchanged).
func goalRows(rows []string, lv *Level, goals []int32) []string {
	goal := cellSet(len(lv.Wall), goals)
	box := cellSet(len(lv.Wall), lv.Boxes)
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
			c := int32((y+1)*lv.W + x + 1)
			g := goal[c]
			switch {
			case box[c] && g:
				ch = '*'
			case box[c]:
				ch = '$'
			case c == lv.Player && g:
				ch = '+'
			case c == lv.Player:
				ch = '@'
			case g:
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

// relayBackCompose: the child's moves sol (start -> B) plus B's suffix, or
// "" and why. On a player-region miss the child's end board is posted as a
// forward relay board.
func relayBackCompose(b *rootBoard, sol string) (string, string) {
	relay.mu.Lock()
	lv := relay.lv
	relay.mu.Unlock()
	boxes, pl, ok := applyPath(lv, sol)
	if !ok {
		return "", "back-replay"
	}
	slices.Sort(boxes)
	if !slices.Equal(boxes, b.boxes) {
		return "", "back-not-B"
	}
	suf := meet.Complete(lv.Wall, lv.W, b.boxes, pl, b.tail)
	if suf == "" && len(b.tail) > 0 {
		relayPost(boxes, pl, sol, "back-end", b.on, nil)
		return "", "back-region"
	}
	return sol + suf, ""
}
