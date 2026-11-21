package main

import (
	"time"
)

// restartBackoff paces a reverse lane's memory restarts (exp-346). A phase
// that returns "memory" almost at once means the process is already over
// the mapped-memory backstop for reasons the lane cannot fix (other lanes
// hold the memory). Restarting immediately spins newSolver allocations
// faster than the GC (capped at 50% CPU by the soft limit) can free them:
// hundreds of phases per second, and measured RSS of 5-8x -mem. A phase that
// ran for a while restarts at once as before; quick failures sleep with
// exponential backoff (20 ms .. 1 s), never past the point where
// the lane's caller would stop restarting.
type restartBackoff struct{ quick int }

const (
	backoffQuick = 250 * time.Millisecond // phases shorter than this count as quick failures
	backoffMin   = 20 * time.Millisecond
	backoffMax   = time.Second
)

// pause is called after a "memory" phase that started at begun.
func (b *restartBackoff) pause(begun, deadline time.Time) {
	if !RestartBackoff {
		return
	}
	if time.Since(begun) >= backoffQuick {
		b.quick = 0
		return
	}
	d := min(backoffMin<<min(b.quick, 6), backoffMax)
	b.quick++
	if left := time.Until(deadline) - time.Second; left < d {
		d = max(left, 0)
	}
	time.Sleep(d)
}
