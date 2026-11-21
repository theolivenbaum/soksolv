// Package memtrace samples where a solver process's memory goes (diagnostics,
// -memtrace FILE): every searcher publishes its per-structure capacity and
// state count at its regular memory check, and a sampler writes them with
// Go runtime memory metrics as JSON lines. cmd/memreport analyses the file.
package memtrace

import (
	"encoding/json"
	"os"
	"runtime/metrics"
	"sync"
	"time"
)

// Enabled is set when a trace file was requested; publishers check it first
// so the hooks cost nothing otherwise.
var Enabled bool

// Snap is one searcher's latest memory picture.
type Snap struct {
	States int                `json:"states"`
	MB     map[string]float64 `json:"mb"` // capacity per structure
	Len    map[string]float64 `json:"len_mb,omitempty"`
}

var snaps sync.Map // name -> Snap

// Publish records searcher name's breakdown (MB per structure, capacity and
// optionally length).
func Publish(name string, states int, capMB, lenMB map[string]float64) {
	if Enabled {
		snaps.Store(name, Snap{States: states, MB: capMB, Len: lenMB})
	}
}

var runtimeNames = []string{
	"/memory/classes/total:bytes",
	"/memory/classes/heap/released:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes",
	"/memory/classes/heap/stacks:bytes",
	"/memory/classes/metadata/other:bytes",
	"/gc/heap/live:bytes",
	"/gc/cycles/total:gc-cycles",
	"/gc/heap/goal:bytes",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
	"/cpu/classes/scavenge/total:cpu-seconds",
}

// Line is one sample.
type Line struct {
	T       float64            `json:"t"`
	Runtime map[string]float64 `json:"runtime"`
	Snaps   map[string]Snap    `json:"snaps"`
}

// Start samples every interval into path until the returned stop is called
// (which writes a final sample).
func Start(path string, interval time.Duration) (stop func()) {
	f, err := os.Create(path)
	if err != nil {
		return func() {}
	}
	Enabled = true
	enc := json.NewEncoder(f)
	t0 := time.Now()
	samples := make([]metrics.Sample, len(runtimeNames))
	for i, n := range runtimeNames {
		samples[i].Name = n
	}
	write := func() {
		metrics.Read(samples)
		l := Line{T: time.Since(t0).Seconds(), Runtime: map[string]float64{}, Snaps: map[string]Snap{}}
		for _, s := range samples {
			switch s.Value.Kind() {
			case metrics.KindUint64:
				l.Runtime[s.Name] = float64(s.Value.Uint64())
			case metrics.KindFloat64:
				l.Runtime[s.Name] = s.Value.Float64()
			}
		}
		snaps.Range(func(k, v any) bool {
			l.Snaps[k.(string)] = v.(Snap)
			return true
		})
		enc.Encode(l)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				write()
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
		write()
		f.Close()
	}
}
