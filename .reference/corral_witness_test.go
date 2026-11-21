package main

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"soksolv/verify"
)

// Checks the actual commutation argument on independently verified solutions:
// move the first barrier push before all its outside preparation, replay that
// preparation, and require the same resulting position and player component.
func TestCorralFrontierCommutesOracleWitnesses(t *testing.T) {
	path := os.Getenv("SOKSOLV_CORRAL_SOLUTIONS")
	if path == "" {
		t.Skip("set SOKSOLV_CORRAL_SOLUTIONS to a verified festablate JSONL")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type push struct{ from, d int32 }
	checked, reordered := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 65536), 16<<20)
	for sc.Scan() {
		var r struct {
			File     string
			Index    int
			Mode     string
			Solved   bool
			Solution string
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if !r.Solved || r.Mode != "base" {
			continue
		}
		data, err := os.ReadFile(r.File)
		if err != nil {
			t.Fatal(err)
		}
		rows := verify.SplitLevels(data)[r.Index-1]
		if err := verify.Check(rows, r.Solution); err != nil {
			t.Fatal(err)
		}
		lv, err := parseLevel(rows)
		if err != nil {
			t.Fatal(err)
		}
		s := newSolver(lv)
		for _, b := range lv.Boxes {
			s.occ[b] = true
		}
		player := lv.Player
		var seq []push
		for _, ch := range r.Solution {
			var d int32
			switch ch {
			case 'l', 'L':
				d = -1
			case 'r', 'R':
				d = 1
			case 'u', 'U':
				d = -int32(lv.W)
			case 'd', 'D':
				d = int32(lv.W)
			default:
				t.Fatal("invalid witness move")
			}
			x := player + d
			if s.occ[x] {
				seq = append(seq, push{x, d})
				s.occ[x] = false
				s.occ[x+d] = true
			}
			player = x
		}
		clear(s.occ)
		for _, b := range lv.Boxes {
			s.occ[b] = true
		}
		player = lv.Player
		for i, m := range seq {
			if i%7 == 0 {
				s.reach(player, s.mark)
				cs := s.firstPushCorral(player, s.stamp)
				if cs != 0 {
					j := i
					for j < len(seq) && s.cmark[seq[j].from] != cs {
						j++
					}
					if j == len(seq) {
						t.Fatalf("unresolved obligation in solved witness %s:%d push%d", r.File, r.Index, i)
					}
					root := append([]bool(nil), s.occ...)
					p := player
					apply := func(m push) {
						s.reach(p, s.mark)
						if !s.occ[m.from] || s.occ[m.from+m.d] || s.lv.Wall[m.from+m.d] || s.mark[m.from-m.d] != s.stamp {
							t.Fatalf("noncommuting frontier %s:%d prefix%d first%d push%v", r.File, r.Index, i, j, m)
						}
						s.occ[m.from] = false
						s.occ[m.from+m.d] = true
						p = m.from
					}
					apply(seq[j])
					for k := i; k < j; k++ {
						apply(seq[k])
					}
					s.reach(p, s.mark)
					if s.mark[seq[j].from] != s.stamp {
						t.Fatal("commuted player's component differs")
					}
					copy(s.occ, root)
					checked++
					if j > i {
						reordered++
					}
				}
			}
			s.occ[m.from] = false
			s.occ[m.from+m.d] = true
			player = m.from
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checked == 0 || reordered == 0 {
		t.Fatalf("empty commutation coverage: %d checked, %d reordered", checked, reordered)
	}
	t.Logf("%d obligations checked; %d moved ahead of outside preparation", checked, reordered)
}
