package main

import (
	"flag"
	"strings"
	"sync"
	"time"
)

// exp-690: the fa2 lane's plan stream. Each registered plan source becomes an
// extra baton engine (slot batonOld+i) that builds its plan in baton slices
// (never up front), drops it when its event list repeats a plan the lane
// already runs, and then searches under the baton's stream schedule: a
// 2,048-expansion trial first, then doubling rounds ordered by the packed
// (Hall) credit reached. The fixed engines keep at least half the slices.
//
// To add a source (exp-689's planner, say): append to fa2PlanSources.
type fa2PlanSource struct {
	name string
	// build plans in engine me's baton slices (s.fa2OwnPlan4 shows the loop)
	// and returns a complete plan, or nil.
	build func(s *solver, deadline time.Time, bat *baton, me int) *packingPlan
}

// exp-689 levers (config A); B (A when it completes, so A is not a separate source) adds the plain best-first retry when A's
// planner ends incomplete.
var p4LeversA = p4Levers{BestPast: true, NewCell: true, ConnAdv: true, NoNull: true, RevPlay: true, PullDead: true}

// fa2PlanA / fa2PlanB: exp-689 plan4 with levers A (B: then a best-first retry).
func fa2PlanA(retry bool) func(s *solver, deadline time.Time, bat *baton, me int) *packingPlan {
	return func(s *solver, deadline time.Time, bat *baton, me int) *packingPlan {
		s.p4lv = p4LeversA
		p := s.newPlanner4(deadline, 4096, false)
		res, ok := fa2StepPlanner(p, deadline, bat, me)
		if !ok && retry && !Stop.Load() {
			q := s.newPlanner4(deadline, 4096, false)
			q.bf, q.fest, q.roomSep = true, false, false
			var r2 *packingPlan
			if r2, ok = fa2StepPlanner(q, deadline, bat, me); ok {
				p, res = q, r2
			}
		}
		if !ok {
			return nil
		}
		res = s.endPlan4(p, res, false, deadline)
		res.festPacked = true
		return res
	}
}

var fa2PlanSources = []fa2PlanSource{
	{"b", fa2PlanA(true)},
	{"bf", func(s *solver, deadline time.Time, bat *baton, me int) *packingPlan {
		s.planBF = true // plain best-first plan4 (Sonic -3- #18)
		return s.fa2OwnPlan4(deadline, bat, me, false)
	}},
	{"fsbf", func(s *solver, deadline time.Time, bat *baton, me int) *packingPlan {
		s.planBF = true // Festival-style plan4 (festsink 2 + roomsep) best-first (Sonic -3- #15, #29, Sasquatch II #43)
		return s.fa2OwnPlan4(deadline, bat, me, true)
	}},
}

var FA2PlanStream bool
var FA2StreamSources string

func init() {
	flag.BoolVar(&FA2PlanStream, "fa2-plan-stream", true, "exp-690: the fa2 lane runs extra plan sources (fa2PlanSources) as stream engines: planned in baton slices, deduped by event list, 2,048-expansion trial then doubling rounds by Hall credit; the fixed engines keep >= half the slices")
	flag.StringVar(&FA2StreamSources, "fa2-stream-sources", "", "exp-690: comma list of stream source names (empty: all registered)")
}

func fa2StreamKind(i int) string { return "stream:" + fa2PlanSources[i].name }

func fa2StreamSource(kind string) (fa2PlanSource, bool) {
	n, ok := strings.CutPrefix(kind, "stream:")
	if !ok {
		return fa2PlanSource{}, false
	}
	for _, p := range fa2PlanSources {
		if p.name == n {
			return p, true
		}
	}
	return fa2PlanSource{}, false
}

// fa2ActiveSources lists the sources -fa2-plan-stream runs.
func fa2ActiveSources() []int {
	var out []int
	if !FA2PlanStream {
		return nil
	}
	for i, p := range fa2PlanSources {
		if FA2StreamSources == "" || strings.Contains(","+FA2StreamSources+",", ","+p.name+",") {
			out = append(out, i)
		}
	}
	return out
}

// streamPlans is the lane's registry of stream plans (dedup).
type streamPlans struct {
	mu    sync.Mutex
	plans []*festPlan
}

// claim records p unless an equal plan is known (the lane's other engines
// included); false means duplicate.
func (r *streamPlans) claim(p *festPlan, others ...*festPlan) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range others {
		if samePlan(p, o) {
			return false
		}
	}
	for _, o := range r.plans {
		if samePlan(p, o) {
			return false
		}
	}
	r.plans = append(r.plans, p)
	return true
}

func (r *streamPlans) has(p *festPlan) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.plans {
		if samePlan(p, o) {
			return true
		}
	}
	return false
}

// fa2StreamPlan runs a source in engine me's slices, then drops duplicates
// and flips the engine from planning to searching.
func (s *solver) fa2StreamPlan(src fa2PlanSource, deadline time.Time, bat *baton, me int) *packingPlan {
	pl := src.build(s, deadline, bat, me)
	if pl == nil || pl.fest == nil || len(pl.fest.e) == 0 {
		return nil
	}
	sh := s.fa2Peer
	if sh != nil && !sh.streams.claim(pl.fest, sh.p4.Load(), sh.fest.Load(), fa2LanePlan.Load()) {
		s.tr.SetExtra("fa2_stream_dup_"+src.name, 1)
		return nil
	}
	if bat != nil {
		bat.setPlanning(me, false)
	}
	return pl
}
