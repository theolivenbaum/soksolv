package main

// exp-477 divergence trace (debug only, off unless -fx-trace is set): one line
// per FESS expansion of a single-strategy lane, and at the end every stored
// node, so the run can be aligned with Festival's FEST_FWD forward trace.
//
//	X seq turn node parent w entryW from to child | node boxes | player | bucket | actual | childcell | score
//	N id parent w g dead expandedAt bucket actual | boxes | player
//	M id from to wd used score   (moves of nodes listed in -fx-path)

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

var FXTrace, FXPath string

func init() {
	flag.StringVar(&FXTrace, "fx-trace", "", "exp-477: write a per-expansion FESS trace and a final node dump to this file")
	flag.StringVar(&FXPath, "fx-path", "", "exp-477: file of sorted box sets (one per line); with -fx-trace, dump the move lists of stored nodes holding them")
}

var (
	fxTW      *bufio.Writer
	fxSeq     int
	fxExpAt   []int32
	fxPendBox []int32
)

func fxTraceInstall() {
	if FXTrace == "" {
		return
	}
	f, err := os.Create(FXTrace)
	if err != nil {
		return
	}
	fxTW = bufio.NewWriterSize(f, 1<<20)
	prevE, prevA := DebugExpand, DebugApplied
	DebugExpand = func(s *solver, e fessEntry) {
		if prevE != nil {
			prevE(s, e)
		}
		if len(fxPendBox) != int(s.n) {
			fxPendBox = make([]int32, s.n)
		}
		s.unpack(e.node, fxPendBox)
	}
	DebugApplied = func(s *solver, e fessEntry, cid int32) {
		if prevA != nil {
			prevA(s, e, cid)
		}
		fxSeq++
		id := e.node
		for int(id) >= len(fxExpAt) {
			fxExpAt = append(fxExpAt, make([]int32, len(fxExpAt)+1024)...)
		}
		if fxExpAt[id] == 0 {
			fxExpAt[id] = int32(fxSeq)
		}
		i, t, wd, _ := decodeMove(s.mv[e.move] &^ moveUsed)
		from := fxPendBox[i]
		cc := featureCell{-1, -1, -1, -1}
		if cid >= 0 && int(cid) < len(s.actual) {
			cc = s.actual[cid]
		}
		fmt.Fprintf(fxTW, "X %d %c %d %d %d %d %d %d %d %d | %s | %d | %v | %v | %v | %v\n", fxSeq, s.frontier.lastTurn, id, s.parent[id], s.w[id], e.weight, wd, from, t, cid,
			fxBoxStr(fxPendBox), s.player[id], s.frontier.cells[s.bucket[id]], s.actual[id], cc, s.mvScore[e.move])
	}
}

func fxBoxStr(b []int32) string {
	c := slices.Clone(b)
	slices.Sort(c)
	var sb strings.Builder
	for k, x := range c {
		if k > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(strconv.Itoa(int(x)))
	}
	return sb.String()
}

func fxTraceEnd(s *solver) {
	if fxTW == nil || s == nil {
		return
	}
	want := map[string]bool{}
	if FXPath != "" {
		if raw, err := os.ReadFile(FXPath); err == nil {
			for _, ln := range strings.Split(string(raw), "\n") {
				if ln = strings.TrimSpace(ln); ln != "" {
					want[ln] = true
				}
			}
		}
	}
	cur := make([]int32, s.n)
	for id := int32(0); int(id) < len(s.player); id++ {
		s.unpack(id, cur)
		bs := fxBoxStr(cur)
		ea := int32(0)
		if int(id) < len(fxExpAt) {
			ea = fxExpAt[id]
		}
		bk := featureCell{-1, -1, -1, -1}
		if int(s.bucket[id]) < len(s.frontier.cells) {
			bk = s.frontier.cells[s.bucket[id]]
		}
		fmt.Fprintf(fxTW, "N %d %d %d %d %v %d %v %v | %s | %d\n", id, s.parent[id], s.w[id], s.g[id], s.dead[id], ea, bk, s.actual[id], bs, s.player[id])
		if want[bs] && s.mvStart[id] >= 0 {
			st, n := s.mvStart[id], int32(s.mvLen[id])
			for k := st; k < st+n; k++ {
				i, t, wd, _ := decodeMove(s.mv[k] &^ moveUsed)
				fmt.Fprintf(fxTW, "M %d %d %d %d %v %v off=%d\n", id, cur[i], t, wd, s.mv[k]&moveUsed != 0, s.mvScore[k], s.mvOff[id])
			}
		}
	}
	fxTW.Flush()
}

// FXReplay (exp-477): at the lane's first expansion, walk a given move path
// ("from to" per line, our cell indices) from the root: for each step report
// whether our move generation lists the move, its window, its rank in the
// node's (weight, score) order and its weight bit, then apply it. Exits.
var FXReplay string

func init() {
	flag.StringVar(&FXReplay, "fx-replay", "", "exp-477: replay a move path from the root, report our weight/rank of each move, then exit")
}

func fxReplayInstall() {
	if FXReplay == "" {
		return
	}
	prev := DebugExpand
	DebugExpand = func(s *solver, e fessEntry) {
		if prev != nil {
			prev(s, e)
		}
		fxReplayRun(s)
		os.Exit(0)
	}
}

func fxReplayRun(s *solver) {
	raw, err := os.ReadFile(FXReplay)
	if err != nil {
		return
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	cur, child := make([]int32, s.n), make([]int32, s.n)
	id := int32(0)
	for k, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var from, to int32
		fmt.Sscan(ln, &from, &to)
		s.unpack(id, cur)
		var h int32
		for _, b := range cur {
			s.occ[b] = true
			h += s.dist[b]
		}
		s.reach(s.player[id], s.mark)
		if s.mvStart[id] < 0 {
			s.open(id, cur, h, s.stamp)
		}
		found, win, rank, nzero := -1, 0, 0, 0
		var wd int32
		var sc moveScore
		for win = 0; win < 40; win++ {
			st, n := s.mvStart[id], int32(s.mvLen[id])
			nzero = 0
			for q := st; q < st+n; q++ {
				i, t, w, _ := decodeMove(s.mv[q] &^ moveUsed)
				if w == 0 {
					nzero++
				}
				if cur[i] == from && t == to && found < 0 {
					found, wd, sc, rank = int(q), w, s.mvScore[q], int(q-st)
				}
			}
			if found >= 0 || s.mvLeft[id] <= 0 || n == 0 {
				break
			}
			s.mvOff[id] += s.mvLen[id]
			s.reach(s.player[id], s.mark)
			s.open(id, cur, h, s.stamp)
		}
		if found < 0 {
			fmt.Fprintf(out, "R %d %d %d NOTGEN node=%d cell=%v windows=%d\n", k+1, from, to, id, s.actual[id], win)
			for _, b := range cur {
				s.occ[b] = false
			}
			return
		}
		s.reach(s.player[id], s.mark)
		cid, _ := s.apply(id, s.mv[found]&^moveUsed, cur, child, h, s.stamp)
		for _, b := range cur {
			s.occ[b] = false
		}
		note := "new"
		if cid < 0 {
			cid = s.lastAdded
			note = "dup"
			if cid < 0 {
				fmt.Fprintf(out, "R %d %d %d DEADATAPPLY node=%d\n", k+1, from, to, id)
				return
			}
		}
		fmt.Fprintf(out, "R %d %d %d wd=%d win=%d rank=%d zeros=%d score=%v nodecell=%v childcell=%v %s\n", k+1, from, to, wd, win, rank, nzero, sc, s.actual[id], s.actual[cid], note)
		id = cid
	}
}
