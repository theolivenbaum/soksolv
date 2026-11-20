// Package soloracle (exp-357, diagnostics only): an env-gated probe that records which states of a
// known solution each search lane stored. Searches call Probe from their add paths when On is set;
// nothing else changes. A state is (sorted boxes, normalised player cell = min cell of the player's
// region), the key every engine already uses for its transposition table.
package soloracle

import (
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

var On bool // set before the searches start; read-only afterwards

var (
	keys   map[uint64]int32 // state key -> index of the solution state (0 = start, i = after push i)
	total  int32
	t0     time.Time
	mu     sync.Mutex
	lanes  = map[string]*laneRec{}
	probes atomic.Int64
)

type laneRec struct {
	Lane      string `json:"lane"`
	Min, Max  int32
	Hits      int32
	FirstHitS float64 `json:"first_hit_s"`
	Stored    int64   `json:"stored_probed"`
}

// Key hashes a normalised state; boxes must be sorted ascending.
func Key(boxes []int32, player int32) uint64 {
	h := uint64(player)*0x9E3779B97F4A7C15 + 1
	for _, b := range boxes {
		h = (h ^ uint64(uint32(b))) * 0xBF58476D1CE4E5B9
		h ^= h >> 29
	}
	return h
}

// Load installs the solution's states (index -> key) and turns the probe on.
func Load(ks []uint64) {
	keys = make(map[uint64]int32, len(ks))
	for i, k := range ks {
		keys[k] = int32(i)
	}
	total = int32(len(ks) - 1)
	t0 = time.Now()
	On = true
}

// Probe is called for every newly stored node of lane (e.g. "F2", "B4"); boxes sorted.
func Probe(lane string, boxes []int32, player int32) {
	if !slices.IsSorted(boxes) {
		boxes = slices.Sorted(slices.Values(boxes))
	}
	idx, ok := keys[Key(boxes, player)]
	mu.Lock()
	defer mu.Unlock()
	r := lanes[lane]
	if r == nil {
		r = &laneRec{Lane: lane, Min: 1 << 30, Max: -1}
		lanes[lane] = r
	}
	r.Stored++
	if !ok {
		return
	}
	if r.Hits == 0 {
		r.FirstHitS = time.Since(t0).Seconds()
	}
	r.Hits++
	r.Min = min(r.Min, idx)
	r.Max = max(r.Max, idx)
}

// Report returns the per-lane summary as one JSON line.
func Report() string {
	mu.Lock()
	defer mu.Unlock()
	var out []*laneRec
	for _, r := range lanes {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b *laneRec) int {
		if a.Lane < b.Lane {
			return -1
		}
		return 1
	})
	b, _ := json.Marshal(map[string]any{"solution_pushes": total, "lanes": out})
	return string(b)
}
