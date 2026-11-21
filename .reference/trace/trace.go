// Package trace records how a search progresses over time, for diagnosing
// failures: when the heuristic or packing last improved, how memory and the
// node count grew, and how the search ended. It is behavior-neutral: the
// searches only report to it, never read from it.
//
// A nil *Recorder is valid and records nothing.
package trace

import (
	"math"
	"sync"
	"time"
)

const maxEvents = 24 // improvements kept (older ones thinned)
const maxSamples = 48

// Root, Step, Sample and Finish belong to one search goroutine. SetExtra
// and Snapshot are safe concurrently with summary publication.
type Recorder struct {
	start      time.Time
	h0         int
	bestH      int
	bestPacked int
	lastImp    time.Duration
	events     [][4]int64 // ms, nodes, h, packed
	samples    [][4]int64 // ms, nodes, stored, tracked MB
	every      time.Duration
	nextSample time.Duration
	summary    Summary
	mu         sync.Mutex // protects published summary and extra values
}

// Summary is the per-strategy report embedded in the solver's JSON stats.
type Summary struct {
	End          string         `json:"end"`              // solved, timeout, memory, exhausted, stopped, error
	Ms           int64          `json:"ms"`               // when it ended (or solved)
	Nodes        int            `json:"nodes"`            // expansions
	Stored       int            `json:"stored,omitempty"` // states stored
	TrackedMB    float64        `json:"tracked_mb,omitempty"`
	H0           int            `json:"h0"`
	BestH        int            `json:"best_h"`
	BestPacked   int            `json:"best_packed"`
	Progress     float64        `json:"progress"` // 1 - best_h/h0
	LastImprove  int64          `json:"last_improve_ms"`
	Improvements [][4]int64     `json:"improvements,omitempty"` // [ms, nodes, h, packed], thinned
	Samples      [][4]int64     `json:"samples,omitempty"`      // [ms, nodes, stored, tracked MB]; only with detail
	Extra        map[string]any `json:"extra,omitempty"`
}

// Detail enables periodic samples in the summary (the -trace flag).
var Detail bool

// New starts a recorder's clock now (the portfolio start, so strategies are
// comparable).
func New() *Recorder {
	return &Recorder{start: time.Now(), bestH: math.MaxInt, bestPacked: -1, every: 500 * time.Millisecond}
}

// Root sets the root's heuristic value, the scale for progress.
func (r *Recorder) Root(h0 int) {
	if r != nil {
		r.h0 = h0
	}
}

// Step reports one expansion of a state with heuristic h and packed boxes.
func (r *Recorder) Step(nodes, h, packed int) {
	if r == nil || (h >= r.bestH && packed <= r.bestPacked) {
		return
	}
	r.bestH = min(r.bestH, h)
	r.bestPacked = max(r.bestPacked, packed)
	r.lastImp = time.Since(r.start)
	r.events = append(r.events, [4]int64{r.lastImp.Milliseconds(), int64(nodes), int64(r.bestH), int64(r.bestPacked)})
	if len(r.events) > maxEvents {
		// Keep the first, the last and every other one in between.
		k := 1
		for i := 1; i < len(r.events)-1; i += 2 {
			r.events[k] = r.events[i]
			k++
		}
		r.events[k] = r.events[len(r.events)-1]
		r.events = r.events[:k+1]
	}
}

// Sample reports resource use; call it from the search's periodic check.
func (r *Recorder) Sample(nodes, stored int, tracked uint64) {
	if r == nil || !Detail {
		return
	}
	el := time.Since(r.start)
	if el < r.nextSample {
		return
	}
	r.nextSample = el + r.every
	r.samples = append(r.samples, [4]int64{el.Milliseconds(), int64(nodes), int64(stored), int64(tracked >> 20)})
	if len(r.samples) >= maxSamples {
		k := 0
		for i := 0; i < len(r.samples); i += 2 {
			r.samples[k] = r.samples[i]
			k++
		}
		r.samples = r.samples[:k]
		r.every *= 2
	}
}

// Finish fills the summary. Fields of Summary set by the caller beforehand
// (Extra) are kept.
func (r *Recorder) Finish(end string, nodes, stored int, tracked uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.summary
	s.End, s.Ms, s.Nodes, s.Stored = end, time.Since(r.start).Milliseconds(), nodes, stored
	s.TrackedMB = math.Round(float64(tracked)/(1<<20)*10) / 10
	s.H0, s.BestH, s.BestPacked = r.h0, r.bestH, r.bestPacked
	if r.bestH == math.MaxInt {
		s.BestH = r.h0
	}
	if end == "solved" {
		s.BestH = 0
	}
	if r.h0 > 0 {
		s.Progress = math.Round((1-float64(s.BestH)/float64(r.h0))*1000) / 1000
	}
	s.LastImprove = r.lastImp.Milliseconds()
	s.Improvements = append([][4]int64(nil), r.events...)
	s.Samples = append([][4]int64(nil), r.samples...)
}

// SetExtra records a strategy-specific diagnostic.
func (r *Recorder) SetExtra(k string, v any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summary.Extra == nil {
		r.summary.Extra = map[string]any{}
	}
	r.summary.Extra[k] = cloneExtra(v)
}

// Snapshot returns a detached copy of the last finished summary and extras.
// Root, Step and Sample remain private to the owning search goroutine.
func (r *Recorder) Snapshot() Summary {
	if r == nil {
		return Summary{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.summary
	s.Improvements = append([][4]int64(nil), s.Improvements...)
	s.Samples = append([][4]int64(nil), s.Samples...)
	if s.Extra != nil {
		s.Extra = cloneExtra(s.Extra).(map[string]any)
	}
	return s
}

// cloneExtra owns the mutable container types used by current diagnostics.
// Other values, including arrays and value-only stats structs, are copied by
// value; callers must not mutate unsupported reference values after SetExtra.
func cloneExtra(v any) any {
	switch x := v.(type) {
	case map[string]any:
		y := make(map[string]any, len(x))
		for k, v := range x {
			y[k] = cloneExtra(v)
		}
		return y
	case []int64:
		return append([]int64(nil), x...)
	case []any:
		y := make([]any, len(x))
		for i, v := range x {
			y[i] = cloneExtra(v)
		}
		return y
	default:
		return v
	}
}

// Ended maps a result reason to a summary end label.
func Ended(solved bool, reason string) string {
	switch {
	case solved:
		return "solved"
	case reason == "":
		return "unknown"
	}
	return reason
}
