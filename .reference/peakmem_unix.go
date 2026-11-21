//go:build !windows

package main

import (
	"math"
	"runtime"
	"syscall"
)

// peakMemoryMB is the process's peak resident set size (getrusage).
func peakMemoryMB() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	b := float64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		b *= 1024 // Linux reports KiB
	}
	return math.Round(b/(1<<20)*10) / 10
}
