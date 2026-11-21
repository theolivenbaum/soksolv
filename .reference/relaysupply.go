package main

// exp-767 relay post supply (research/exp-766/REPORT.md P1; all default off).
//
// -macro-revive: the macro lane, when its tree dies on memory with time
// left, restarts as a fresh seeded tree (reviveDiversify) instead of idling
// for the rest of the run. The new solver's relay posting milestones start
// again (100k lane expansions, then doubling), so the lane keeps posting.
//
// -relay-fa2: the fa2 lane's engines post their best live boards too
// (the same rule and milestones as macro/novelty).
//
// -relay-fzset: besides its best board, a posting solver keeps the best
// board (smallest h) per distinct set of boxes on goals among boards within
// fzSlack boxes-on-goals of its best, and at each milestone also posts up to
// fzK of them whose frozen-on-goal set differs from every set it posted.
//
// -relay-fresh-first (relaytrial.go): every first attempt (forward posts,
// then back posts) is tried before any retry; back posts get one attempt.
//
// -relay-dump DIR (census diagnostic): every accepted post is written to DIR
// (residual rows, prefix, info); single lanes (-strategy) post too.

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"soksolv/trace"
)

var (
	MacroRevive     bool
	RelayFA2        bool
	RelayFzSet      bool
	relayFreshFirst bool
	relayDumpDir    string
	fzSlack         int32        = 2
	fzK                          = 3
	relayDumpExp    atomic.Int64 // lane expansions at the post being dumped
)

const fzCap = 4096

var macroReviveT0 = time.Now()

func init() {
	flag.BoolVar(&MacroRevive, "macro-revive", true, "exp-767 (default on since 2026-11-14): the macro lane revives on memory death as a fresh seeded tree (its relay posts restart)")
	flag.BoolVar(&RelayFA2, "relay-fa2", false, "exp-767: the fa2 lane's engines post their best live boards to the relay")
	flag.BoolVar(&RelayFzSet, "relay-fzset", true, "exp-767 (default on since 2026-11-14): posting lanes also post their best board per distinct frozen-on-goal set (up to -relay-fz-k per milestone)")
	flag.IntVar(&fzK, "relay-fz-k", fzK, "exp-767: -relay-fzset extra posts per milestone")
	flag.BoolVar(&relayFreshFirst, "relay-fresh-first", true, "exp-767 (default on since 2026-11-14): relay trials try every fresh post before any retry; back posts get one attempt")
	flag.StringVar(&relayDumpDir, "relay-dump", "", "exp-767 census diagnostic: write every relay post (residual, prefix, info) to this directory; single lanes post too")
}

// macroReviveNote records revive k's time in the macro lane's trace.
func macroReviveNote(tr *trace.Recorder, k int) {
	if tr == nil {
		return
	}
	tr.SetExtra("revives", k)
	tr.SetExtra("revive_last_ms", time.Since(macroReviveT0).Milliseconds())
}

type fzEnt struct{ id, on, h int32 }

// fzState: a posting solver's best board per on-goal set, and the frozen sets it posted.
type fzState struct {
	tab    map[uint64]fzEnt
	posted map[uint64]bool
}

func fzMix(c int32) uint64 {
	x := uint64(c)*0x9E3779B97F4A7C15 + 0x632BE59BD9B4E019
	x ^= x >> 31
	x *= 0xBF58476D1CE4E5B9
	return x ^ x>>29
}

func fzHash(frozen []int32) uint64 {
	h := uint64(len(frozen)) + 1
	for _, c := range frozen {
		h ^= fzMix(c)
	}
	return h
}

// fzNote (relayNote, -relay-fzset): node id's board (on boxes on goals, h).
func (s *solver) fzNote(id int32, boxes []int32, on, h int32) {
	if on == 0 || on+fzSlack < s.rlOn {
		return
	}
	if s.rlFz == nil {
		s.rlFz = &fzState{tab: map[uint64]fzEnt{}, posted: map[uint64]bool{}}
	}
	var key uint64
	for _, b := range boxes {
		if s.lv.Goal[b] {
			key ^= fzMix(b)
		}
	}
	e, ok := s.rlFz.tab[key]
	if ok && h >= e.h {
		return
	}
	if !ok && len(s.rlFz.tab) >= fzCap {
		for k, x := range s.rlFz.tab {
			if x.on+fzSlack < s.rlOn {
				delete(s.rlFz.tab, k)
			}
		}
		if len(s.rlFz.tab) >= fzCap {
			return
		}
	}
	s.rlFz.tab[key] = fzEnt{id, on, h}
}

// fzFlush (a posting milestone, after the best board's post): up to fzK
// boards with frozen-on-goal sets this solver has not posted.
func (s *solver) fzFlush() {
	if s.rlFz == nil || !s.relayPoster() || s.rlID == 0 {
		return
	}
	best := s.rlID - 1
	s.relayFlushID(best, s.rlOn, "", func(fr []int32) bool { s.rlFz.posted[fzHash(fr)] = true; return false }) // the best boards' sets count as posted
	var c []fzEnt
	for k, x := range s.rlFz.tab {
		if x.on+fzSlack < s.rlOn {
			delete(s.rlFz.tab, k)
			continue
		}
		if x.id != best {
			c = append(c, x)
		}
	}
	slices.SortFunc(c, func(a, b fzEnt) int {
		return cmp.Or(cmp.Compare(b.on, a.on), cmp.Compare(a.h, b.h), cmp.Compare(a.id, b.id))
	})
	n := 0
	for _, x := range c {
		if n >= fzK || relay.n.Load() >= relayCap {
			break
		}
		s.relayFlushID(x.id, x.on, "-fz", func(fr []int32) bool {
			h := fzHash(fr)
			if s.rlFz.posted[h] {
				return false
			}
			s.rlFz.posted[h] = true
			n++
			return true
		})
	}
}

// relayDump (-relay-dump, under relay.mu) writes post i.
func relayDump(rb *rootBoard, i int, frozen []int32) {
	os.MkdirAll(relayDumpDir, 0o755)
	base := filepath.Join(relayDumpDir, fmt.Sprintf("%02d-%s", i, rb.from))
	rows := rb.rows
	if rb.rowsW != nil {
		rows = rb.rowsW
	}
	os.WriteFile(base+".xsb", []byte(strings.Join(rows, "\n")+"\n"), 0o644)
	os.WriteFile(base+".path", []byte(rb.prefix+"\n"), 0o644)
	ok := false
	if bx, pl, good := applyPath(relay.lv, rb.prefix); good {
		ok = sortedEqual(rb.boxes, bx) && sameRegion(relay.lv, rb.boxes, pl, rb.player)
	}
	os.WriteFile(base+".info", []byte(fmt.Sprintf("from %s on %d frozen %d prefix %d at_s %.2f exp %d prefix_ok %v\n", rb.from, rb.on, len(frozen), len(rb.prefix), rb.at, relayDumpExp.Load(), ok)), 0o644)
}
