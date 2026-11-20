package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

// FA2Replay (exp-491 diagnostic): walk a state path (research/exp-491/fpath.py:
// 'boxes|player' per line in our cells, root first) through fa2's expansion
// and print, per step, whether the next state is a generated child, its
// rank, weight, advisors and features.
var FA2Replay string

func init() {
	flag.StringVar(&FA2Replay, "fa2-replay", "", "exp-491 diagnostic: replay a state path through fa2's child generation")
}

func (f *fa2) stateKey(cur []int32) uint64 {
	s := f.s
	var h int32
	for _, b := range cur {
		h += s.dist[b]
	}
	pk := f.packedNow()
	return fa2Key(pk, int32(s.regions()), f.roomsNow(), f.oopNow(cur, pk), int32(hotspotCount(s.hot.counts(s, cur))), h)
}

func fa2ReplayRun(f *fa2) Result {
	s := f.s
	fh, err := os.Open(FA2Replay)
	if err != nil {
		return Result{Reason: err.Error()}
	}
	defer fh.Close()
	type st struct {
		boxes  []int32
		player int32
	}
	var path []st
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "|")
		var b []int32
		for _, x := range strings.Fields(p[0]) {
			v, _ := strconv.Atoi(x)
			b = append(b, int32(v))
		}
		slices.Sort(b)
		pl, _ := strconv.Atoi(strings.TrimSpace(p[1]))
		if pl < 0 {
			pl = int(s.lv.Player)
		}
		path = append(path, st{b, int32(pl)})
	}
	keyOf := func(x st) (uint64, uint64, int32) {
		var bh uint64
		for _, b := range x.boxes {
			bh ^= f.zb[b]
			s.occ[b] = true
		}
		np := s.reach(x.player, s.mark)
		return bh, bh ^ f.zp[np], np
	}
	w := 0
	for k := 0; k+1 < len(path); k++ {
		cur := slices.Clone(path[k].boxes)
		bh, _, np := keyOf(path[k])
		stp := s.stamp
		key := f.stateKey(cur)
		id := f.addNode(cur, np, -1, -1, -1, 0, key, key, 0, bh)
		f.expand(id, cur, stp)
		clear(s.occ)
		_, h2, _ := keyOf(path[k+1])
		clear(s.occ)
		pp, pc, pr, po, ph := fa2Fields(key)
		j := -1
		for x := int32(0); x < f.eLen[id]; x++ {
			if f.hashOf(id, f.ed.at(f.eOff[id]+x)) == h2 {
				j = int(x)
				break
			}
		}
		if j < 0 {
			fmt.Printf("k%d parent(pck %d cn %d rm %d oop %d hs %d) n=%d NOT GENERATED\n", k+1, pp, pc, pr, po, ph, f.eLen[id])
			continue
		}
		e := *f.ed.at(f.eOff[id] + int32(j))
		nfree := 0
		for x := int32(0); x < f.eLen[id]; x++ {
			if f.ed.at(f.eOff[id]+x).w() == 0 {
				nfree++
			}
		}
		cp, cc, cr, co, ch := fa2Fields(e.key)
		w += int(e.w())
		if FA2Debug {
			for x := int32(0); x < f.eLen[id]; x++ {
				if ee := f.ed.at(f.eOff[id] + x); ee.w() == 0 {
					a, b2, c2, d2, e2 := fa2Fields(ee.key)
					fmt.Printf("   w0 adv=%08b box %d->%d (pck %d cn %d rm %d oop %d hs %d dist %d)\n", ee.adv(), ee.b, ee.t, a, b2, c2, d2, e2, 0xFFFFFF-int(ee.key&0xFFFFFF))
				}
			}
			fmt.Printf("   path box %d->%d dist %d\n", e.b, e.t, 0xFFFFFF-int(e.key&0xFFFFFF))
		}
		fmt.Printf("k%d parent(pck %d cn %d rm %d oop %d hs %d) n=%d free=%d rank=%d w=%d adv=%07b child(pck %d cn %d rm %d oop %d hs %d) cumw=%d\n", k+1, pp, pc, pr, po, ph, f.eLen[id], nfree, j, e.w(), e.adv(), cp, cc, cr, co, ch, w)
	}
	return Result{Reason: "replay"}
}
