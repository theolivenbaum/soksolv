//go:build windows

package main

import (
	"math"
	"runtime"
)

// peakMemoryMB on Windows: memory obtained from the OS by the Go runtime
// (an upper bound on the heap's resident footprint; no getrusage there).
func peakMemoryMB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return math.Round(float64(ms.Sys)/(1<<20)*10) / 10
}
