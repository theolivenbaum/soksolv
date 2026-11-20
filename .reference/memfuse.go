package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"time"
)

// startMemFuse (exp-346) is the last-resort enforcement of -mem: lanes
// police themselves against budgets and a 76-92% mapped backstop, but a
// rare transient (exp-340 measured 5-8x the limit on a few levels) escapes
// them, and at the production limit that means swap or an OOM kill. Every
// 20 ms the fuse reads the Go-mapped bytes; over fusePct% of the limit it
// forces a GC and scavenge, and if the process is still over 100 ms later
// it prints a "memory (fuse)" result and exits. Below that nothing changes.
// onTrip prints the result and exits.
func startMemFuse(limit uint64, fusePct uint64, onTrip func()) {
	if fusePct == 0 {
		return
	}
	go func() {
		sample := []metrics.Sample{
			{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"},
			{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/free:bytes"}}
		over := func() bool {
			metrics.Read(sample)
			return sample[0].Value.Uint64()-sample[1].Value.Uint64() > limit/100*fusePct
		}
		for range time.Tick(20 * time.Millisecond) {
			if !over() {
				continue
			}
			debug.FreeOSMemory()
			time.Sleep(100 * time.Millisecond)
			if over() {
				fmt.Fprintf(os.Stderr, "MEMFUSE mapped=%dMB objects=%dMB free=%dMB limit=%dMB\n",
					(sample[0].Value.Uint64()-sample[1].Value.Uint64())>>20, sample[2].Value.Uint64()>>20, sample[3].Value.Uint64()>>20, limit>>20)
				if p := os.Getenv("SOKSOLV_FUSEPROF"); p != "" { // diagnostics: heap profile at the trip
					if f, err := os.Create(p); err == nil {
						pprof.Lookup("heap").WriteTo(f, 0)
						f.Close()
					}
				}
				onTrip()
				return
			}
		}
	}()
}
