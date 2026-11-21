package main

// exp-747 -relay-trial (default off; design research/exp-745/REPORT.md §2).
//
// The macro and novelty lanes post their best live board (relay.go: most
// boxes on goals, ties smaller h; at 100k lane expansions, then doubling;
// distinct, residualLive) with the moves that reach it. A weight-0 lane
// `relay` spends a bank of phi x elapsed wall time on short exclusive trials:
// a fresh child process of this binary runs the whole portfolio on a posted
// board's residual (frozen-on-goal boxes as walls; the full re-rooted board
// when none is frozen) for a slice of u x 2^k seconds (attempt k on that
// board), and is killed at the slice's end. While a trial runs the parent
// drops to runtime.GOMAXPROCS(1) and the child gets the parent's previous
// -procs, so the process pair never runs more Go threads than the off arm
// plus the parent's one. A child solve is prefix + child solution, verified
// on the original level, and wins as lane `relay`.
//
// Order: fewest attempts first, newest post first on ties. Memory: the child
// gets -relay-child-mem x -mem, only while the parent's mapped memory leaves
// that much headroom (else the trial waits). Nothing depends on -t: the bank
// grows with elapsed time and the slices double per board.
//
// Pure Go and portable: the child is os/exec of os.Executable(); it exits
// when its stdin closes (-relay-child), so it never outlives the parent.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"soksolv/full"
	"soksolv/reverse"
	"soksolv/trace"
	"soksolv/verify"
)

var (
	RelayTrial      bool
	relayPhi        = 0.125 // share of elapsed wall time the trials may spend
	relayUnit       = 2.0   // seconds: attempt k on a board gets relayUnit x 2^k
	relayChildMem   = 0.125 // child -mem as a fraction of the parent's
	relayChild      bool    // this process is a relay child
	relayTrialFirst int64   = 100000
	relayPostOn     bool    // a portfolio run with posting on (-revive-root or -relay-trial)
	relayT0         = time.Now()
	relayWake       = make(chan struct{}, 1)
	// relayDiag: the relay lane's trials so far (copied into the winning lane's summary, since a stopped lane's is bare).
	relayDiag atomic.Pointer[map[string]any]
	// BudgetRelay, TrackedRelay: the relay lane's slot (weight 0: it allocates nothing).
	BudgetRelay, TrackedRelay atomic.Uint64
)

func init() {
	flag.BoolVar(&RelayTrial, "relay-trial", true, "exp-747 (default on since its promotion, 2026-11-13; -relay-trial=false = old path): macro/novelty post best live boards (100k expansions, doubling); a weight-0 lane spends phi x elapsed on exclusive whole-portfolio child trials on their residuals (slice u x 2^k), prefix + child solution")
	flag.Float64Var(&relayPhi, "relay-phi", relayPhi, "exp-747: -relay-trial share of elapsed wall time spent on trials")
	flag.Float64Var(&relayUnit, "relay-unit", relayUnit, "exp-747: -relay-trial first slice per board in seconds (attempt k: u x 2^k)")
	flag.Float64Var(&relayChildMem, "relay-child-mem", relayChildMem, "exp-747: -relay-trial child's -mem as a fraction of -mem (skipped while headroom is short)")
	flag.BoolVar(&relayChild, "relay-child", false, "exp-747 (internal): this process is a relay trial child; exit when stdin closes")
}

// relayWatchParent (child): exit as soon as the parent closes our stdin or dies.
func relayWatchParent() {
	if !relayChild {
		return
	}
	go func() {
		io.Copy(io.Discard, os.Stdin)
		os.Exit(3)
	}()
}

// relayTrialRec is one trial in the trace.
type relayTrialRec struct {
	Board  int     `json:"board"`
	From   string  `json:"from"`
	On     int32   `json:"on"`
	Walls  bool    `json:"walls"`
	Att    int     `json:"att"`
	At     float64 `json:"at_s"`
	Slice  float64 `json:"slice_s"`
	Wall   float64 `json:"wall_s"`
	End    string  `json:"end"`
	Lane   string  `json:"lane,omitempty"`
	PeakMB float64 `json:"peak_mb,omitempty"`
	Back   bool    `json:"back,omitempty"` // exp-761: a backward post
}

// relayRun is the relay lane's scheduler.
func relayRun(rows []string, deadline time.Time, memLimit uint64, rec *trace.Recorder) Result {
	start := time.Now()
	stopped := func() bool {
		return Stop.Load() || full.Stop.Load() || reverse.Stop.Load() || time.Until(deadline) < time.Second
	}
	var trials []relayTrialRec
	spent, skippedMem := 0.0, 0
	over := 0.3 // a trial's wall past its slice (start-up, kill latency on one P): the largest seen
	report := func(solvedBy string) {
		relay.mu.Lock()
		rec.SetExtra("posts", relay.posts)
		rec.SetExtra("dups", relay.dups)
		rec.SetExtra("dead", relay.dead)
		posts := make([]map[string]any, len(relay.boards))
		for i, b := range relay.boards {
			posts[i] = map[string]any{"from": b.from, "on": b.on, "at_s": math.Round(b.at*100) / 100, "prefix": len(b.prefix), "walls": b.lvW != nil, "att": b.att}
			if b.back {
				posts[i]["back"], posts[i]["depth"], posts[i]["suffix"] = true, b.depth, len(b.tail)
			}
		}
		relay.mu.Unlock()
		rec.SetExtra("boards", posts)
		rec.SetExtra("trials", trials)
		rec.SetExtra("spent_s", math.Round(spent*1000)/1000)
		rec.SetExtra("elapsed_s", math.Round(time.Since(start).Seconds()*1000)/1000)
		rec.SetExtra("skipped_mem", skippedMem)
		if solvedBy != "" {
			rec.SetExtra("solved_by", solvedBy)
		}
		relayDiag.Store(&map[string]any{"trials": slices.Clone(trials), "spent_s": math.Round(spent*1000) / 1000, "posts": len(posts), "elapsed_s": math.Round(time.Since(start).Seconds()*1000) / 1000})
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if stopped() {
			report("")
			return Result{Reason: fmt.Sprintf("relay: stopped (%d trials, %.1f s)", len(trials), spent)}
		}
		select {
		case <-relayWake:
		case <-tick.C:
		}
		// Pick: fewest attempts, newest on ties.
		relay.mu.Lock()
		pick := -1
		rank := func(b *rootBoard) int { // exp-761: a back post ranks as one attempt more (forward first attempts come first)
			if relayFreshFirst { // exp-767: every first attempt (forward, then back) before any retry
				if b.att == 0 && b.back {
					return 1
				}
				return 2 * b.att
			}
			if b.back {
				return 2*b.att + 3 // att+1, and after a forward board with that count
			}
			return 2 * b.att
		}
		for i := len(relay.boards) - 1; i >= 0; i-- {
			if relay.boards[i].dead || relayFreshFirst && relay.boards[i].back && relay.boards[i].att > 0 {
				continue // exp-767 -relay-fresh-first: back posts get one attempt
			}
			if pick < 0 || rank(relay.boards[i]) < rank(relay.boards[pick]) {
				pick = i
			}
		}
		var b *rootBoard
		if pick >= 0 {
			b = relay.boards[pick]
		}
		relay.mu.Unlock()
		if b == nil {
			continue
		}
		slice := relayUnit * math.Pow(2, float64(b.att))
		if relayPhi*time.Since(start).Seconds()-spent < slice+over {
			continue
		}
		if left := time.Until(deadline).Seconds() - 0.5; left < slice {
			slice = left // the run's end cuts the last trial short
			if slice < 1 {
				continue
			}
		}
		childMB := int(float64(memLimit>>20) * relayChildMem)
		if float64(mappedBytes())+float64(childMB)*(1<<20) > 0.9*float64(memLimit) {
			skippedMem++ // headroom short: wait for it
			continue
		}
		res, walls := b.rows, false
		if b.lvW != nil {
			res, walls = b.rowsW, true
		}
		relay.mu.Lock()
		att := b.att
		b.att++
		relay.mu.Unlock()
		t0 := time.Now()
		tr := relayTrialRec{Board: pick, From: b.from, On: b.on, Walls: walls, Att: att, At: math.Round(t0.Sub(start).Seconds()*100) / 100, Slice: slice, Back: b.back}
		end := -1
		if b.back {
			end = int(b.player)
		}
		sol, why, lane, peak := relayChildRun(res, slice, childMB, deadline, end)
		wall := time.Since(t0).Seconds()
		spent += wall
		over = max(over, wall-slice)
		tr.Wall, tr.End, tr.Lane, tr.PeakMB = math.Round(wall*1000)/1000, why, lane, peak
		if why == "infeasible" && b.back {
			relay.mu.Lock()
			b.dead = true // exp-761: start -> (B, region) does not exist; the unused slice stays in the bank
			relay.mu.Unlock()
		}
		if relayDebug {
			fmt.Fprintf(os.Stderr, "relay: trial board %d (%s on %d) att %d slice %.1f: %s %s %.2fs (spent %.1f of %.1f)\n", pick, b.from, b.on, att, slice, why, lane, wall, spent, relayPhi*time.Since(start).Seconds())
		}
		if sol != "" {
			full, why := b.prefix+sol, ""
			if b.back {
				full, why = relayBackCompose(b, sol)
			}
			if why != "" {
				tr.End = why
			} else if err := verify.Check(relay.rows, full); err == nil {
				trials = append(trials, tr)
				report(lane)
				return Result{Solved: true, Solution: full}
			} else {
				tr.End = "verify-failed"
			}
		}
		trials = append(trials, tr)
		report("")
	}
}

// relayChildRun runs the whole portfolio on res in a child process for slice
// seconds while the parent runs on one P. It returns the child's solution (or
// ""), why it ended, its winning lane and its peak memory.
func relayChildRun(res []string, slice float64, memMB int, deadline time.Time, endCell int) (sol, why, lane string, peak float64) {
	exe, err := os.Executable()
	if err != nil {
		return "", "exec: " + err.Error(), "", 0
	}
	tmp, err := os.CreateTemp("", "soksolv-relay-*.xsb")
	if err != nil {
		return "", "tmp: " + err.Error(), "", 0
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(strings.Join(res, "\n") + "\n")
	tmp.Close()
	t0 := time.Now()
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	cmd := exec.Command(exe, relayArgs(slice, memMB, prev, endCell, tmp.Name())...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", "stdin: " + err.Error(), "", 0
	}
	defer in.Close()
	var out strings.Builder
	cmd.Stdout = &out
	if relayDebug {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return "", "start: " + err.Error(), "", 0
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	end := t0.Add(time.Duration((slice + 0.35) * float64(time.Second))) // the child stops at slice - 0.25 s (its -t) plus its 250 ms shutdown grace; killed if it has not reported by then
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			var o Output
			if err := json.Unmarshal([]byte(out.String()), &o); err != nil {
				return "", "child output", "", 0
			}
			if o.Solved {
				return o.Solution, "solved", o.Strategy, o.PeakMemoryMB
			}
			if o.Reason == "" {
				o.Reason = "unsolved"
			}
			return "", o.Reason, o.Strategy, o.PeakMemoryMB
		case <-tick.C:
			if Stop.Load() || full.Stop.Load() || time.Now().After(end) || time.Now().After(deadline.Add(shutdownGrace)) {
				cmd.Process.Kill()
				<-done
				return "", "killed", "", 0
			}
		}
	}
}

// relayArgs: the parent's explicit flags for the child, with its own -t,
// -mem and -procs, minus the parent-only ones.
func relayArgs(t float64, memMB, procs, end int, file string) []string {
	var args []string
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "t", "mem", "procs", "relay-trial", "relay-child", "cpuprofile", "memprofile", "memtrace", "strategy",
			"revive-root", "revive-root-seed", "revive-root-all", "best-dump", "relay-end", "relay-dump":
			return
		}
		args = append(args, "-"+f.Name+"="+f.Value.String())
	})
	if end >= 0 {
		args = append(args, fmt.Sprintf("-relay-end=%d", end)) // exp-761: back trial
	}
	return append(args, fmt.Sprintf("-procs=%d", procs), fmt.Sprintf("-t=%.3f", max(t-0.25, 0.5)), fmt.Sprintf("-mem=%d", memMB), "-relay-trial=false", "-relay-child", file, "1") // no recursive relay in children (explicit since -relay-trial defaults on)
}
