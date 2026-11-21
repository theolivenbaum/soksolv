package main

import (
	"sync/atomic"
	"time"
)

// mailDrain delivers meetings posted to an idle search while another search
// of the same lane runs: a 5 ms polling goroutine.
type mailDrain struct {
	check func() (string, bool)
	met   *atomic.Bool
	sol   string
	done  chan struct{}
	end   chan struct{}
}

func startDrain(check func() (string, bool), met *atomic.Bool) *mailDrain {
	d := &mailDrain{check: check, met: met}
	d.done, d.end = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(d.end)
		for {
			select {
			case <-d.done:
				return
			case <-time.After(5 * time.Millisecond):
			}
			if sol, ok := check(); ok {
				d.sol = sol
				met.Store(true)
				return
			}
		}
	}()
	return d
}

// StopCheck is the search's stopCheck: it ends the search once a meeting was
// delivered.
func (d *mailDrain) StopCheck() func() bool {
	return d.met.Load
}

// Stop ends the drain and returns the delivered solution ("" if none).
func (d *mailDrain) Stop() string {
	close(d.done)
	<-d.end
	return d.sol
}

// res is a lane's result.
type res struct {
	r    Result
	name string
}
