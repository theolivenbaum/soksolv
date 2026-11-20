package main

import (
	"flag"
	"runtime"
	"slices"
	"soksolv/trace"
	"sync/atomic"
	"time"
)

// exp-594: an anytime dedicated fa2 lane (-fa2-lane -fa2-lane-any). Engine 0
// starts at t=0 on fa2's own plan (-fa2-plan, v2 by default). When the macro
// lane holds a complete, packed plan4 plan it publishes the event list
// (fa2LanePlan); engine 0 then starts engine 1 on that list (no replanning)
// and the two alternate 1024-expansion slices. Memory: the lane's own share
// (Budget12), split between the live engines; an engine that dies frees its
// half for the other. Budgets are counted in expansions (-nodes), not time.

var FA2LaneAny bool
var Tracked14, TrackedFest atomic.Uint64

// FA2FestPlan (exp-615): a third engine in the anytime lane on a Festival-style
// plan4 plan (festsink 2 + roomsep) computed in the lane's own slices.
var FA2FestPlan bool
var FA2FestSlice = 8192

// exp-618: FA2LaneSolo runs the whole anytime lane under -strategy fa2 (a
// deterministic Tier 1 stand-in: engine 1 builds the default plan4 in its own
// slices instead of waiting for the macro lane, and the lane's total is
// bounded by baton turns = -nodes/1024). FA2FestDedup ends the Festival-style
// engine when its event list equals engine 1's (no CPU spent on a duplicate).
var FA2LaneSolo bool
var FA2FestDedup = true

// exp-711: FA2Ring (-fa2-ring) and its revive (-fa2-ring-revive).
var FA2Ring = true // exp-711 promoted (gate: Sven_1623 #1434 3/3 exclusive at 280 s with -fa2-edge-win 15)
var FA2RingRevive = true

// fa2LanePlan is the macro lane's complete plan4 event list (nil until then).
var fa2LanePlan atomic.Pointer[festPlan]

func init() {
	flag.BoolVar(&FA2FestPlan, "fa2-festplan", true, "exp-618 promoted 2026-11-11 (exp-615): with -fa2-lane-any: a third lane engine on a Festival-style plan4 plan (-plan4-festsink 2 + -plan4-roomsep for this planner only; the macro lane keeps the default plan4). Planning runs in the engine's own baton slices, so its cost is the lane's and does not depend on the time limit")
	flag.BoolVar(&FA2LaneSolo, "fa2-lane-solo", false, "exp-618 diagnostic: -strategy fa2 runs the anytime lane (engine 1 plans the default plan4 itself; -nodes bounds the lane's summed baton turns)")
	flag.BoolVar(&FA2FestDedup, "fa2-festplan-dedup", true, "exp-618: with -fa2-festplan: drop the Festival-style engine (or skip engine 1) when the two plan4 event lists are identical")
	flag.IntVar(&FA2FestSlice, "fa2-festplan-slice", 8192, "exp-615: planner expansions per baton slice while a lane-built plan4 is planned (exp-618: 8192 = one 1024-expansion fa2 slice at -plan4-anytime-ratio 8, so a planning turn costs about one search turn of wall time)")
	flag.BoolVar(&FA2Ring, "fa2-ring", FA2Ring, "exp-711 (default on): the anytime lane's engines share one memory pool (the lane's share plus elastic headroom); when over it the engine with the most expansions since its last improvement is retired, so the last survivor gets the whole share")
	flag.BoolVar(&FA2RingRevive, "fa2-ring-revive", true, "exp-711: with -fa2-ring: after a lane memory death (time left), revive the lane plan4-only (the macro lane's plan4, else its own); a further revive needs a 25% larger share")
	flag.BoolVar(&FA2LaneAny, "fa2-lane-any", true, "exp-594 (promoted 2026-11-10, default on): with -fa2-lane: anytime lane: own v2 plan from t=0, plus a second engine on the macro lane's plan4 list once it completes (alternating slices, shared lane memory)")
}

// fa2PublishPlan (macro lane, laneID 1) hands its complete plan4 list to the lane.
func (s *solver) fa2PublishPlan() {
	if Plan4Scope == "macro" || s.planAlt {
		return // exp-742: the macro lane's list is not the default plan4; the fa2 lane plans its own
	}
	if !FA2LaneAny || s.isolated || s.laneID != 1 || !s.relayOriginal() || s.plan == nil || !s.plan.complete || !s.plan.festPacked || s.plan.fest == nil || len(s.plan.fest.e) == 0 {
		return
	}
	fa2LanePlan.CompareAndSwap(nil, s.plan.fest)
}

// fa2FestPlan builds the Festival-style plan4 plan inside engine me's baton
// slices (FA2FestSlice planner expansions per slice); nil when it cannot
// complete (the engine then ends and frees its share).
func (s *solver) fa2FestPlan(deadline time.Time, bat *baton, me int) *packingPlan {
	return s.fa2OwnPlan4(deadline, bat, me, true)
}

// fa2OwnPlan4 (exp-618) builds plan4 in engine me's slices, Festival-style
// when fest (else the default plan4, for -fa2-lane-solo's engine 1).
func (s *solver) fa2OwnPlan4(deadline time.Time, bat *baton, me int, fest bool) *packingPlan {
	s.festPlan = fest
	p := s.newPlanner4(deadline, 4096, false)
	res, ok := fa2StepPlanner(p, deadline, bat, me)
	if fest {
		s.tr.SetExtra("fa2_fest_plan_exp", p.expanded)
	}
	if !ok {
		return nil
	}
	res = s.endPlan4(p, res, false, deadline)
	res.festPacked = true
	return res
}

// fa2StepPlanner (exp-690) runs planner p in engine me's baton slices
// (FA2FestSlice planner expansions each). ok: a complete plan.
func fa2StepPlanner(p *planner4, deadline time.Time, bat *baton, me int) (*packingPlan, bool) {
	if !p.start4() {
		return nil, false
	}
	p.deadline = deadline
	for !p.done && !p.exhausted && p.expanded < p.maxExp && !Stop.Load() && time.Until(deadline) > 0 {
		p.step4(FA2FestSlice)
		if bat != nil && !bat.yield(me) {
			return nil, false
		}
	}
	res := p.finish4()
	return res, res != nil && res.complete
}

// fa2LaneShared (exp-618) holds the lane's two plan4 lists for dedup.
type fa2LaneShared struct {
	p4, fest atomic.Pointer[festPlan]
	streams  streamPlans // exp-690
}

// samePlan (exp-618) reports identical plan4 event lists.
func samePlan(a, b *festPlan) bool {
	return a != nil && b != nil && a.initNum == b.initNum && slices.Equal(a.e, b.e)
}

// fa2LaneRun is the lane body: s is engine 0's solver (laneID 12,
// Budget12/Tracked12). With -fa2-ring a lane memory death (every engine ended
// before the deadline, one on memory) revives the lane plan4-only (exp-711).
func fa2LaneRun(s *solver, deadline time.Time, memLimit uint64, tr *trace.Recorder) Result {
	r, death, turns := fa2LaneRun1(s, deadline, memLimit, tr)
	if !FA2Ring || !FA2RingRevive {
		return r
	}
	var at []int64
	var lastLim uint64
	start := time.Now()
	for k := 1; death && !r.Solved && LaneRevive && !Stop.Load() && time.Until(deadline) > time.Second; k++ {
		lim := Budget12.Load()
		if lastLim > 0 && lim < lastLim/4*5 {
			break // the same search on the same share would end the same way
		}
		Tracked12.Store(0)
		Tracked14.Store(0)
		TrackedFest.Store(0)
		runtime.GC()
		at = append(at, time.Since(start).Milliseconds())
		tr.SetExtra("fa2_revive_ms", at)
		n := r.Nodes
		r, death, turns = fa2P4Only(s, relayPick("fa2", k), deadline, memLimit, tr, turns)
		r.Nodes += n
		lastLim = lim
	}
	return r
}

// fa2P4Only (exp-711) is the revived lane: one plan4 engine (the macro lane's
// list, else its own default plan4) alone in the lane's pool. turns: the
// baton turns left under -fa2-lane-solo (0: unbounded).
func fa2P4Only(s *solver, root *rootBoard, deadline time.Time, memLimit uint64, tr *trace.Recorder, turns int64) (Result, bool, int64) {
	bat := newBaton([3]bool{false, true, false})
	bat.ring, bat.trk = true, &Tracked12
	if FA2LaneSolo && NodeBudget > 0 {
		if turns <= 0 {
			return Result{Reason: "timeout"}, false, 0
		}
		bat.maxTurns = turns
	}
	lvk, rowsk := rootOf(root, s.lv, s.rows) // exp-744 -revive-root: odd revives start from a posted board
	x := newSolverUntil(lvk, deadline)
	x.laneID, x.rows, x.budgetP, x.trackedP, x.tr = 12, rowsk, s.budgetP, &Tracked12, trace.New()
	x.fa2Lane = true
	x.nodeLimit = 1 << 62
	kind := "p4own"
	if p := fa2LanePlan.Load(); p != nil && root == nil {
		x.fa2Preset, kind = p, "v2"
	}
	bat.ch[1] <- struct{}{}
	r := relayFix(fa2Run(x, deadline, memLimit, kind, bat, 1))
	bat.ringRelease()
	r.Nodes = int(x.expandedN)
	tr.SetExtra("fa2_revive_end", r.Reason)
	tr.SetExtra("fa2_revive_nodes", x.expandedN)
	if r.Solved {
		tr.SetExtra("fa2_won", "revive")
	}
	left := int64(0)
	if bat.maxTurns > 0 {
		left = max(bat.maxTurns-bat.turns, 0)
	}
	death := !r.Solved && (r.Reason == "memory" || r.Reason == "reroot-invalid") && time.Until(deadline) > time.Second && !Stop.Load()
	return r, death, left
}

func fa2LaneRun1(s *solver, deadline time.Time, memLimit uint64, tr *trace.Recorder) (Result, bool, int64) {
	fa2Wide = true
	s.fa2Lane = true
	start := time.Now()
	solo := FA2LaneSolo && NodeBudget > 0
	own := solo || Plan4Scope == "macro" // exp-742: engine 1 plans the default plan4 itself
	bat := newBaton([3]bool{true, own, FA2FestPlan})
	bat.dynMem = true
	if FA2Ring {
		bat.ring, bat.trk = true, &Tracked12 // exp-711: the lane's sum goes to its first slot
		Tracked14.Store(0)
		TrackedFest.Store(0)
	}
	srcs := fa2ActiveSources()
	sh := &fa2LaneShared{}
	var s2, s3 *solver
	var res2, res3 Result
	done2, done3 := make(chan struct{}), make(chan struct{})
	if solo {
		bat.maxTurns = max(NodeBudget/1024, 1)
		s.nodeLimit = 1 << 62
	}
	lim := func(x *solver) {
		if solo {
			x.nodeLimit = 1 << 62
		} else if l := s.expansionLimit(); l > 0 {
			x.nodeLimit = max(l-s.expandedN, 1)
		}
	}
	p4done := false // engine 1 started or skipped
	mk := func() *solver {
		p := fa2LanePlan.Load()
		if p == nil {
			return nil
		}
		x := newSolverUntil(s.lv, deadline)
		x.laneID, x.rows, x.budgetP, x.trackedP, x.tr = 12, s.rows, s.budgetP, &Tracked14, trace.New()
		x.fa2Preset, x.fa2Lane, x.fa2Peer = p, true, sh
		lim(x)
		return x
	}
	// start2 starts engine 1 on the macro lane's plan once it exists.
	start2 := func() bool {
		if p4done {
			return true
		}
		p := fa2LanePlan.Load()
		if p == nil {
			return false
		}
		p4done = true
		bat.spawn = nil
		sh.p4.Store(p)
		if (FA2FestPlan && FA2FestDedup && samePlan(p, sh.fest.Load())) || sh.streams.has(p) {
			tr.SetExtra("fa2_p4_dup", s.expandedN) // exp-618: the fest engine already runs this plan
			return true
		}
		s2 = mk()
		tr.SetExtra("fa2_p4_at", s.expandedN)
		tr.SetExtra("fa2_p4_ms", time.Since(start).Milliseconds())
		bat.add(1)
		go func() {
			res2 = fa2Run(s2, deadline, memLimit, "v2", bat, 1)
			close(done2)
		}()
		return true
	}
	if own {
		p4done = true
		s2 = newSolverUntil(s.lv, deadline)
		s2.laneID, s2.rows, s2.budgetP, s2.trackedP, s2.tr = 12, s.rows, s.budgetP, &Tracked14, trace.New()
		s2.fa2Lane, s2.fa2Peer = true, sh
		lim(s2)
		go func() {
			res2 = fa2Run(s2, deadline, memLimit, "p4own", bat, 1)
			close(done2)
		}()
	} else {
		bat.spawn = func() { start2() }
	}
	if FA2FestPlan {
		s3 = newSolverUntil(s.lv, deadline)
		s3.laneID, s3.rows, s3.budgetP, s3.trackedP, s3.tr = 12, s.rows, s.budgetP, &TrackedFest, trace.New()
		s3.fa2Lane, s3.festPlan, s3.fa2Peer = true, true, sh
		lim(s3)
		go func() {
			res3 = fa2Run(s3, deadline, memLimit, "fest", bat, 2)
			close(done3)
		}()
	}
	// exp-690: the plan stream's engines (slots batonOld..)
	type strm struct {
		s    *solver
		res  Result
		done chan struct{}
		name string
	}
	var strms []*strm
	for _, i := range srcs {
		slot := bat.addSlot(true, true)
		x := newSolverUntil(s.lv, deadline)
		x.laneID, x.rows, x.budgetP, x.trackedP, x.tr = 12, s.rows, s.budgetP, &TrackedFest, trace.New()
		x.fa2Lane, x.fa2Peer = true, sh
		lim(x)
		st := &strm{s: x, done: make(chan struct{}), name: fa2PlanSources[i].name}
		strms = append(strms, st)
		go func(kind string) {
			st.res = fa2Run(x, deadline, memLimit, kind, bat, slot)
			close(st.done)
		}(fa2StreamKind(i))
	}
	bat.ch[0] <- struct{}{}
	r := fa2Run(s, deadline, memLimit, FA2Plan, bat, 0)
	n0 := s.expandedN
	if !p4done && !r.Solved {
		// Engine 0 ended before the plan arrived: wait for it (idle, no CPU);
		// engine 1 then shares the lane with whatever else still runs.
		for !p4done && !Stop.Load() && time.Until(deadline) > 300*time.Millisecond {
			if bat.stopped() {
				break
			}
			if !start2() {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	if s2 != nil {
		<-done2
	}
	if s3 != nil {
		<-done3
	}
	tr.SetExtra("fa2_v2_nodes", n0)
	tr.SetExtra("fa2_v2_end", r.Reason)
	if s2 != nil {
		tr.SetExtra("fa2_p4_nodes", s2.expandedN)
		tr.SetExtra("fa2_p4_end", res2.Reason)
		n0 += s2.expandedN
	}
	if s3 != nil {
		tr.SetExtra("fa2_fest_nodes", s3.expandedN)
		tr.SetExtra("fa2_fest_end", res3.Reason)
		n0 += s3.expandedN
	}
	var rs *strm
	for _, st := range strms {
		<-st.done
		tr.SetExtra("fa2_stream_"+st.name+"_nodes", st.s.expandedN)
		tr.SetExtra("fa2_stream_"+st.name+"_end", st.res.Reason)
		n0 += st.s.expandedN
		if st.res.Solved && rs == nil {
			rs = st
		}
	}
	if r.Solved {
		tr.SetExtra("fa2_won", "v2")
	} else if res2.Solved {
		tr.SetExtra("fa2_won", "p4")
		r = res2
	} else if res3.Solved {
		tr.SetExtra("fa2_won", "fest")
		r = res3
	} else if rs != nil {
		tr.SetExtra("fa2_won", "stream:"+rs.name)
		r = rs.res
	}
	r.Nodes = int(n0)
	if !FA2Ring {
		return r, false, 0
	}
	bat.ringRelease()
	if len(bat.kills) > 0 {
		tr.SetExtra("fa2_ring_kills", bat.kills)
	}
	// a lane memory death: every engine ended early, at least one on memory,
	// and plan4's tree (when it ran) was not exhausted
	mem := r.Reason == "memory" || res2.Reason == "memory" || res3.Reason == "memory"
	for _, st := range strms {
		mem = mem || st.res.Reason == "memory"
	}
	death := !r.Solved && mem && res2.Reason != "exhausted" && !Stop.Load() && time.Until(deadline) > time.Second && !(bat.maxTurns > 0 && bat.turns >= bat.maxTurns)
	left := int64(0)
	if bat.maxTurns > 0 {
		left = max(bat.maxTurns-bat.turns, 0)
	}
	return r, death, left
}
