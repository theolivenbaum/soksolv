// Package meet connects the backward (pull) search with the forward
// searches: bidirectional search by meeting in the middle.
//
// Each side records every state it stores in a lossy, lock-free "perimeter"
// of 64-bit state keys (Back for the backward search, Fwd for the forward
// ones) and probes the other side's perimeter with each new state. Probing
// both ways matters: a search only probes a state when it creates it, and
// the other side may reach that state much later.
//
// On a hit, the prober posts the state with its own half of the path to the
// owner's mailbox. The owner confirms the state exactly in its own table and
// joins the halves: forward moves from the start to the state, then the
// backward search's pulls from the state to the solved position, reversed.
//
// A state is its sorted box cells plus the normalized player (the minimum
// cell the player can reach). All strategies parse the same rows into the
// same padded grid, so cells and normalization agree.
package meet

import (
	"sync/atomic"
)

// Table is the perimeter: a direct-mapped set of state keys (a newer key
// overwrites an older one in its slot; a lost entry only costs a missed
// meeting, never a wrong one, because hits are confirmed exactly).
type Table struct {
	cur  atomic.Pointer[[]atomic.Uint64] // power-of-two slots; grows by rehashing
	adds atomic.Int64
	max  int // slot cap
	zbox []uint64
	zpl  []uint64
	// Mail carries candidate meetings to the table's owners (one mailbox
	// per owning search). Sends never block (a full mailbox drops it).
	Mail []chan Meeting
	// Posted, Confirmed count meetings (diagnostics).
	Posted, Confirmed atomic.Int64
	Dropped           atomic.Int64   // posts lost to a full mailbox (diagnostics)
	DroppedAt         []atomic.Int64 // per mailbox
}

// Meeting is a state that hit the other side's perimeter.
type Meeting struct {
	Boxes  []int32 // sorted box cells
	Player int32   // normalized player
	Prefix string  // posted by a forward search: LURD moves from the start to this state
	At     int32   // with Prefix: the player's actual cell after it
	Tail   []Push  // posted by the backward search: pushes from this state to the solved position
	From   string  // which strategy found it
	Lane   int     // exp-545 diagnostic: the posting forward searcher's laneID
	Owner  int     // exp-545 diagnostic: the posting forward searcher's owner
}

// Push moves the box on From one cell in direction Dir (0..3 = u d l r).
type Push struct {
	From int32
	Dir  int8
}

// New makes a perimeter for a grid of the given number of cells with at
// most 2^logSize slots and the given number of owner mailboxes. It starts
// small (allocating 2^logSize slots up front costs ~1 ms per level, most
// levels are solved in less) and grows as states arrive.
func New(cells, logSize, owners int) *Table {
	t := &Table{
		max:  1 << logSize,
		zbox: make([]uint64, cells),
		zpl:  make([]uint64, cells),
	}
	first := make([]atomic.Uint64, min(1<<16, t.max))
	t.cur.Store(&first)
	for range owners {
		t.Mail = append(t.Mail, make(chan Meeting, 64))
	}
	t.DroppedAt = make([]atomic.Int64, owners)
	x := uint64(0x9E3779B97F4A7C15)
	next := func() uint64 {
		// splitmix64
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ z>>30) * 0xBF58476D1CE4E5B9
		z = (z ^ z>>27) * 0x94D049BB133111EB
		return z ^ z>>31
	}
	for c := range cells {
		t.zbox[c] = next()
		t.zpl[c] = next()
	}
	return t
}

// Bytes is the table's memory.
func (t *Table) Bytes() uint64 {
	if t == nil {
		return 0
	}
	return uint64(len(*t.cur.Load())) * 8
}

// Key is a state's 64-bit key (never 0).
func (t *Table) Key(boxes []int32, player int32) uint64 {
	k := t.zpl[player]
	for _, b := range boxes {
		k ^= t.zbox[b]
	}
	return k | 1
}

// Add records a state in its 4-way bucket: into an empty way, else over a
// way picked by the key. Once the adds reach half the slots, the table
// grows 8x (up to its cap), rehashing the keys it holds. Concurrent writers
// may race on growth or write into the old slots meanwhile; such keys are
// lost, which only costs a meeting.
func (t *Table) Add(k uint64) {
	sp := t.cur.Load()
	sl := *sp
	insert(sl, k)
	if n := t.adds.Add(1); n > int64(len(sl)/2) && len(sl) < t.max {
		next := make([]atomic.Uint64, min(8*len(sl), t.max))
		for i := range sl {
			if x := sl[i].Load(); x != 0 {
				insert(next, x)
			}
		}
		t.cur.CompareAndSwap(sp, &next)
	}
}

func insert(sl []atomic.Uint64, k uint64) {
	base := k & uint64(len(sl)-1) &^ 3
	for w := range uint64(4) {
		x := sl[base+w].Load()
		if x == k {
			return
		}
		if x == 0 && sl[base+w].CompareAndSwap(0, k) {
			return
		}
	}
	sl[base+(k>>62)].Store(k)
}

// Has reports whether a state key is (probably) in the perimeter.
func (t *Table) Has(k uint64) bool {
	sl := *t.cur.Load()
	base := k & uint64(len(sl)-1) &^ 3
	for w := range uint64(4) {
		if sl[base+w].Load() == k {
			return true
		}
	}
	return false
}

// Post offers a meeting to every owner without blocking.
func (t *Table) Post(m Meeting) {
	t.Posted.Add(1)
	for i, c := range t.Mail {
		select {
		case c <- m:
		default:
			t.Dropped.Add(1)
			t.DroppedAt[i].Add(1)
		}
	}
}

// Receive returns a pending meeting for owner i, if any.
func (t *Table) Receive(i int) (Meeting, bool) {
	select {
	case m := <-t.Mail[i]:
		return m, true
	default:
		return Meeting{}, false
	}
}

// Back holds the backward search's states (owner 0: reverse); Fwd holds the
// forward searches' states (owner 0: full, 1: macro). Nil when not running
// as a portfolio.
var Back, Fwd atomic.Pointer[Table]

// Complete returns LURD moves that make the given pushes, starting with the
// boxes on boxes and the player on player, walking the player between
// pushes by BFS. wall and w describe the padded grid. It returns "" if some
// push can't be reached (callers verify the joined solution anyway).
func Complete(wall []bool, w int, boxes []int32, player int32, pushes []Push) string {
	dirs := [4]int32{-int32(w), int32(w), -1, 1}
	occ := make([]bool, len(wall))
	for _, b := range boxes {
		occ[b] = true
	}
	prev := make([]int8, len(wall))
	seen := make([]uint32, len(wall))
	var stamp uint32
	var out []byte
	p := player
	q := []int32{}
	for _, pu := range pushes {
		target := pu.From - dirs[pu.Dir]
		stamp++
		q = append(q[:0], p)
		seen[p] = stamp
		for i := 0; i < len(q) && seen[target] != stamp; i++ {
			c := q[i]
			for k, d := range dirs {
				if nb := c + d; seen[nb] != stamp && !wall[nb] && !occ[nb] {
					seen[nb] = stamp
					prev[nb] = int8(k)
					q = append(q, nb)
				}
			}
		}
		if seen[target] != stamp || wall[pu.From+dirs[pu.Dir]] || occ[pu.From+dirs[pu.Dir]] || !occ[pu.From] {
			return ""
		}
		var walk []byte
		for c := target; c != p; c -= dirs[prev[c]] {
			walk = append(walk, "udlr"[prev[c]])
		}
		for i, j := 0, len(walk)-1; i < j; i, j = i+1, j-1 {
			walk[i], walk[j] = walk[j], walk[i]
		}
		out = append(out, walk...)
		out = append(out, "UDLR"[pu.Dir])
		occ[pu.From], occ[pu.From+dirs[pu.Dir]] = false, true
		p = pu.From
	}
	return string(out)
}

// Target is a backward-search state offered to forward "bridge" searches as
// an intermediate goal: reaching its boxes with the player in its region,
// then making Tail, solves the level.
type Target struct {
	Boxes  []int32
	Player int32 // normalized
	Tail   []Push
	H      int32 // the publishing lane's heuristic value (lower = closer to the start)
}

// Best holds each reverse lane's latest best state.
var Best [7]atomic.Pointer[Target] // reverse lanes 0-2, pull-FESS lane 3, festb lanes 4, 5, dive 6

// EndCell returns the player's cell after walking moves from start on a
// grid w cells wide.
func EndCell(moves string, start int32, w int) int32 {
	p := start
	for i := 0; i < len(moves); i++ {
		switch moves[i] | 0x20 {
		case 'u':
			p -= int32(w)
		case 'd':
			p += int32(w)
		case 'l':
			p--
		case 'r':
			p++
		}
	}
	return p
}

// Adds is the number of keys inserted (diagnostics).
func (t *Table) Adds() int64 {
	if t == nil {
		return 0
	}
	return t.adds.Load()
}

// StoreBest publishes lane i's best state.
func StoreBest(i int, tgt *Target) { Best[i].Store(tgt) }
