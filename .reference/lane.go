package main

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"soksolv/full"
	"soksolv/reverse"
)

// The portfolio's one lane model (exp-640; design: research/exp-629/DESIGN.md).
//
// A lane is one goroutine. It declares (laneMem, below):
//   - a memory share: its weight in the 70% split of -mem (laneShares), and
//     the budget slot that receives it;
//   - the tracked-byte slots it charges (the governor sums them; a restart
//     clears them) and the companion summaries it owns;
//   - a ring of configurations run(k), k = 0, 1, ... (ringRun): when config
//     k ends, the lane's restart rule says whether config k+1 starts and how
//     (revive after a memory death, replan after a plan event).
//
// Inside one configuration a lane may share its core between engines, by one
// of two slice policies:
//   - fixed expansion slices, round robin (baton, below: the fa2 lane's v2,
//     plan4 and Festival-style-plan engines, 1024 expansions each, the lane's
//     memory split between the live engines);
//   - geometric wall-time slices (doubling): hub-pull's and hub-gen's
//     backward/forward alternation (festbAlternate), the toward lane's
//     minimum slice before a retarget, the hub lane's commitment windows.
//
// No rule switches configurations on staleness: a stale trigger cannot tell
// "about to finish" from "stuck" (exp-626). Memory beyond the shares is
// elastic (elastic_mem.go); the mapped-memory backstop ends a configuration
// with "memory", which is what the revive rule reacts to.

// laneMem is a lane's memory declaration.
type laneMem struct {
	budget *atomic.Uint64   // receives the lane's share of the split
	slots  []*atomic.Uint64 // tracked bytes it charges; the first is its own tree
	owns   []string         // companion summaries (stamped "stopped" with the lane)
}

func slots(s ...*atomic.Uint64) []*atomic.Uint64 { return s }

// laneTable: every lane (and -strategy) the portfolio can run.
var laneTable = map[string]laneMem{
	"macro":      {&Budget, slots(&Tracked, &TrackedMacroAlt), nil},
	"relay":      {&BudgetRelay, slots(&TrackedRelay), nil}, // exp-747 (weight 0)
	"novelty":    {&Budget6, slots(&Tracked6), nil},
	"fa2":        {&Budget12, slots(&Tracked12, &Tracked14, &TrackedFest), nil},
	"pull":       {&Budget4, slots(&Tracked4, &TrackedDive), nil}, // TrackedDive: -dive-share pull
	"toward":     {&Budget3, slots(&Tracked3), nil},
	"hub-gen":    {&Budget7, slots(&Tracked7, &Tracked8, &Tracked11), []string{"hub-gen-fwd"}},
	"hub":        {&Budget7, slots(&Tracked7, &Tracked8, &Tracked11), []string{"hub-gen-fwd"}},
	"hub-pull":   {&Budget9, slots(&Tracked9, &Tracked10, &TrackedRace), []string{"hub-pull-fwd"}},
	"full":       {&full.Budget, slots(&full.Tracked), []string{"bridge"}},
	"reverse":    {&reverse.Budget, slots(&reverse.Tracked), nil},
	"hf":         {&Budget4, slots(&Tracked4), []string{"pull", "toward"}},
	"tfwd":       {&Budget3, slots(&Tracked3), nil},
	"hub-strata": {&Budget9, slots(&Tracked9), nil}, // exp-686 diagnostic
	"pathdiag":   {&Budget, slots(&Tracked), nil},
	"factor":     {&BudgetF, slots(&TrackedF), nil},                      // exp-645
	"dive":       {&BudgetDive, slots(&TrackedDive, &TrackedDiveA), nil}, // exp-691 (exp-693: A engine)
}

// laneShares is the portfolio's lane set with each lane's memory weight
// (only != "all": that one lane, weight 1).
func laneShares(only string) map[string]uint64 {
	if only != "all" {
		return map[string]uint64{only: 1}
	}
	sh := map[string]uint64{"reverse": 3}
	on := func(name string, ok bool, w uint64) {
		if ok {
			sh[name] = w
		}
	}
	on("macro", macroLane, 1)
	on("full", fullLane, 3)
	on("hub-pull", festX != "" && festBLane, 1) // exp-283
	on("toward", towardOn, 1)
	on("fa2", FA2Lane, 1) // exp-491
	on("novelty", NoveltyLane, 1)
	on("hub-gen", festBLane, 1)
	on("pull", pullOn, 1)
	on("dive", DiveLane && diveGated && DiveShare == "", DiveMemW) // exp-691: gated on the level's peel depth (exp-693: or shared; weight -dive-mem-w)
	if DiveLane && diveGated && DiveShare == "pull" && pullOn {
		sh["pull"] = 1 + DiveMemW // exp-693: the lane's two engines split it
	}
	if DiveLane && diveGated && DiveReplace != "" {
		delete(sh, DiveReplace) // exp-693: the dive takes this lane's core on gated boards
	}
	on("factor", FactorLane && factorGated, 1) // exp-645: gated on the level (main sets factorGated after parsing)
	return sh
}

// restart is a ring's decision after a configuration ends.
type restart uint8

const (
	stop   restart = iota
	revive         // memory death: fresh tree; the dead one is collected and quick failures back off
	replan         // plan event (macro: plan4 completed): restart at once, carrying the plan
)

// ringRun runs a lane's configurations k = 0, 1, ...: cfg(k) runs config k,
// next decides what follows. A restart clears the lane's tracked slots;
// revived is called after each revive (k = the config that died).
func ringRun(deadline time.Time, tracked []*atomic.Uint64, cfg func(k int) Result, next func(Result) restart, revived func(k int)) Result {
	var bo restartBackoff
	for k := 0; ; k++ {
		begun := time.Now()
		r := cfg(k)
		why := next(r)
		if why == stop {
			return r
		}
		for _, t := range tracked {
			t.Store(0)
		}
		if why == revive {
			runtime.GC() // release the dead tree before the next one allocates
			bo.pause(begun, deadline)
			if revived != nil {
				revived(k)
			}
		}
	}
}

// doubling is slice k of a geometric schedule: unit, 2 unit, 4 unit, ...
// (at most unit << maxShift).
func doubling(unit time.Duration, k, maxShift int) time.Duration {
	return unit << min(k, maxShift)
}

// baton is the fixed-slice policy: engines share one lane's core one at a
// time (deterministic). A token passes round robin at every slice (an engine
// calls yield every 1024 expansions); a finished engine passes it on for
// good, and a solve stops the others. With dynMem the lane's memory is split
// between the live engines.
//
// exp-690 (plan stream): slots 0..2 are the fixed engines; slots 3.. are
// stream engines (batonOld = 3). While any stream engine lives the token
// alternates old, stream, old, stream: the old engines keep at least half the
// slices. The stream turn goes to pickStream.
type baton struct {
	ch              []chan struct{}
	alive           []bool
	stop            bool
	mu              sync.Mutex
	dynMem          bool    // exp-594: memory divisor = live engines (else fixed 2)
	spawn           func()  // exp-594: engine 0 calls it at each yield until it starts engine 1
	turns, maxTurns int64   // exp-618 (-fa2-lane-solo): slices handed out; > 0 bounds the lane
	lastOld         int     // exp-690: the old engine that last held the token
	flip            bool    // exp-690: alternate planner / trial turns among stream engines
	planning        []bool  // exp-690 (stream slots): still building its plan
	used            []int   // exp-690: search slices a stream engine has had
	credit          []int32 // exp-690: best packed credit a stream engine reached
	pturns          []int   // exp-690: planning slices a stream engine has had
	// exp-711 (-fa2-ring): one memory pool for the lane's engines (ringOver).
	ring  bool
	mem   []uint64 // per slot: tracked bytes at its last check (0: no tree / ended)
	stale []int64  // per slot: expansions since its last improvement
	kill  []bool   // per slot: retired by the ring (stops at its next check)
	held  uint64   // elastic bytes the lane borrowed
	trk   *atomic.Uint64
	kills []string // retirement order (slot@expansions) for the trace
	// exp-769 (-macro-alt-plan): doubling slices. With dbl > 0 an engine keeps
	// the token for 1 << min(k, dbl) yields in its k-th turn.
	dbl   int
	quota []int64 // per slot: yields left in its current turn
	turnK []int   // per slot: turns it has had
}

const batonOld = 3

func newBaton(alive [3]bool) *baton {
	b := &baton{}
	for _, a := range alive {
		b.addSlot(a, false)
	}
	return b
}

// addSlot appends an engine slot (alive from the start) and returns its index.
func (b *baton) addSlot(alive, planning bool) int {
	b.ch = append(b.ch, make(chan struct{}, 1))
	b.alive = append(b.alive, alive)
	b.planning = append(b.planning, planning)
	b.used = append(b.used, 0)
	b.credit = append(b.credit, 0)
	b.pturns = append(b.pturns, 0)
	b.mem = append(b.mem, 0)
	b.stale = append(b.stale, 0)
	b.kill = append(b.kill, false)
	return len(b.ch) - 1
}

// setPlanning / report: a stream engine's state for the scheduler.
func (b *baton) setPlanning(me int, v bool) {
	b.mu.Lock()
	b.planning[me] = v
	b.mu.Unlock()
}
func (b *baton) report(me int, credit int32) {
	b.mu.Lock()
	b.credit[me] = max(b.credit[me], credit)
	b.mu.Unlock()
}

func (b *baton) live() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := uint64(0)
	for _, a := range b.alive {
		if a {
			n++
		}
	}
	return max(n, 1)
}

func (b *baton) stopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stop
}

func (b *baton) wait(me int) { <-b.ch[me] }

// pickStream (exp-690) chooses the next stream engine, or -1. b.mu held.
// Planning engines and trial engines alternate; planning engines share
// their turns evenly (fewest planning slices first). Trial engines run in doubling rounds: round r gives each
// engine 2<<r slices (2,048 expansions first), most credit first within a round.
func (b *baton) pickStream() int {
	pl, tr := -1, -1
	for i := batonOld; i < len(b.alive); i++ {
		if b.alive[i] && b.planning[i] && (pl < 0 || b.pturns[i] < b.pturns[pl]) {
			pl = i
		}
	}
	for r := 0; r < 24 && tr < 0; r++ {
		cum := 2 * (1<<(r+1) - 1)
		for i := batonOld; i < len(b.alive); i++ {
			if b.alive[i] && !b.planning[i] && b.used[i] < cum && (tr < 0 || b.credit[i] > b.credit[tr]) {
				tr = i
			}
		}
	}
	switch {
	case pl >= 0 && tr >= 0:
		b.flip = !b.flip
		if b.flip {
			return pl
		}
		return tr
	case pl >= 0:
		return pl
	}
	return tr
}

func (b *baton) liveStream() bool {
	for i := batonOld; i < len(b.alive); i++ {
		if b.alive[i] {
			return true
		}
	}
	return false
}

// next returns the engine that gets the token after me, or -1. b.mu held.
func (b *baton) next(me int) int {
	if me < batonOld {
		b.lastOld = me
	}
	if me < batonOld && b.liveStream() {
		return b.pickStream()
	}
	from := me
	if me >= batonOld {
		from = b.lastOld
	}
	for k := 1; k <= batonOld; k++ {
		if j := (from + k) % batonOld; b.alive[j] && (j != me || me >= batonOld) {
			return j
		}
	}
	if me >= batonOld {
		return b.pickStream()
	}
	return -1
}

// add (exp-615) marks engine i live; when no other engine lives it also
// hands i the token (nobody else will).
func (b *baton) add(i int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	alone := b.next(i) < 0
	b.alive[i] = true
	if alone {
		b.ch[i] <- struct{}{}
	}
}

// yield hands the token to the next live engine and waits for it back;
// false means stop.
func (b *baton) yield(me int) bool {
	if me == 0 && b.spawn != nil {
		b.spawn()
	}
	b.mu.Lock()
	if b.dbl > 0 {
		if b.quota[me]--; b.quota[me] > 0 {
			defer b.mu.Unlock()
			return !b.stop
		}
		b.turnK[me]++
		b.quota[me] = 1 << min(b.turnK[me], b.dbl)
	}
	b.turns++
	if b.maxTurns > 0 && b.turns >= b.maxTurns {
		b.stop = true
	}
	if me >= batonOld {
		if b.planning[me] {
			b.pturns[me]++
		} else {
			b.used[me]++
		}
	}
	other := b.next(me)
	b.mu.Unlock()
	if other >= 0 {
		b.ch[other] <- struct{}{}
		<-b.ch[me]
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.stop
}

// ringOver (exp-711, -fa2-ring) is the lane's memory check: engine me
// reports its tracked bytes and its expansions since its last improvement.
// The live engines share one pool, the lane's share lim (plus elastic
// headroom): while their sum exceeds it, the live engine with a tree that has
// gone longest without improving is retired (it stops at its own next check,
// so its bytes leave the sum at once). A lone engine thus borrows the whole
// share. Reports whether me is retired.
func (b *baton) ringOver(me int, tb uint64, stale int64, lim uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mem[me], b.stale[me] = tb, stale
	if b.kill[me] {
		return true
	}
	var sum uint64
	for i, a := range b.alive {
		if a && !b.kill[i] {
			sum += b.mem[i]
		}
	}
	if b.trk != nil {
		b.trk.Store(sum)
	}
	if lim == 0 || sum <= lim {
		if b.held > 0 {
			releaseElastic(12, b.held)
			b.held = 0
		}
		return false
	}
	need := sum - lim
	if need <= b.held {
		return false
	}
	if acquireElastic(12, need-b.held) {
		b.held = need
		return false
	}
	v := -1
	for i, a := range b.alive {
		if a && !b.kill[i] && b.mem[i] > 0 && (v < 0 || b.stale[i] > b.stale[v] || (b.stale[i] == b.stale[v] && b.mem[i] > b.mem[v])) {
			v = i
		}
	}
	if v < 0 {
		return false
	}
	b.kill[v] = true
	if len(b.kills) < 32 {
		b.kills = append(b.kills, strconv.Itoa(v)+"@"+strconv.FormatInt(b.stale[v], 10)+"/"+strconv.FormatUint(b.mem[v]>>20, 10)+"MB")
	}
	return v == me
}

// ringRelease returns the lane's elastic bytes (exp-711).
func (b *baton) ringRelease() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held > 0 {
		releaseElastic(12, b.held)
		b.held = 0
	}
}

func (b *baton) done(me int, solved bool) {
	b.mu.Lock()
	b.alive[me] = false
	b.mem[me] = 0
	if solved {
		b.stop = true
	}
	other := b.next(me)
	b.mu.Unlock()
	if other >= 0 {
		b.ch[other] <- struct{}{}
	}
}
