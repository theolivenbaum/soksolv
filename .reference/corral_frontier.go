package main

// FrontierCorral certifies first pushes with all outside boxes removed.
var FrontierCorral bool

type corralFrontier struct {
	zone, barrier, reach []uint32
	zstamp, rstamp       uint32
	q, rq, boxes, best   []int32
}

// firstPushCorral returns a stamp on the selected barrier boxes, or zero.
// Let B fence an inaccessible empty-floor component U. Remove all other
// boxes. Reject if the player can now enter U. Require every legal first B
// push to go into U and be executable on the full board now. Any solution's
// first B push can then precede its outside preparation: it opens its old
// square and occupies only a square inaccessible before that first push.
// Outside preparation cannot use U or the occupied old square. Thus B
// pushes suffice, without requiring each B box to be individually pushable.
func (s *solver) firstPushCorral(player int32, st uint32) uint32 {
	f, lv := &s.cf, s.lv
	if f.zone == nil {
		f.zone = make([]uint32, len(lv.Wall))
		f.barrier = make([]uint32, len(lv.Wall))
		f.reach = make([]uint32, len(lv.Wall))
	}
	if f.zstamp > 1<<31 {
		clear(f.zone)
		clear(f.barrier)
		f.zstamp = 0
	}
	base := f.zstamp
	f.best = f.best[:0]
	for start := range int32(len(lv.Wall)) {
		if lv.Wall[start] || s.occ[start] || s.mark[start] == st || f.zone[start] > base {
			continue
		}
		f.zstamp++
		zs := f.zstamp
		f.zone[start] = zs
		f.q = append(f.q[:0], start)
		f.boxes = f.boxes[:0]
		unsolved := false
		for head := 0; head < len(f.q); head++ {
			x := f.q[head]
			unsolved = unsolved || lv.Goal[x]
			for _, d := range s.dirs {
				y := x + d
				if lv.Wall[y] {
					continue
				}
				if s.occ[y] {
					if f.barrier[y] != zs {
						f.barrier[y] = zs
						f.boxes = append(f.boxes, y)
						unsolved = unsolved || !lv.Goal[y]
					}
				} else if f.zone[y] != zs {
					f.zone[y] = zs
					f.q = append(f.q, y)
				}
			}
		}
		if !unsolved || len(f.boxes) == 0 || len(f.best) > 0 && len(f.boxes) >= len(f.best) {
			continue
		}
		if !TransactionalOpen && s.expired() {
			break
		}
		if f.rstamp > 1<<31 {
			clear(f.reach)
			f.rstamp = 0
		}
		f.rstamp++
		rs := f.rstamp
		f.reach[player] = rs
		f.rq = append(f.rq[:0], player)
		ok := true
		for head := 0; head < len(f.rq); head++ {
			x := f.rq[head]
			if f.zone[x] == zs {
				ok = false
				break
			}
			for _, d := range s.dirs {
				y := x + d
				if !lv.Wall[y] && f.barrier[y] != zs && f.reach[y] != rs {
					f.reach[y] = rs
					f.rq = append(f.rq, y)
				}
			}
		}
		pushes := 0
		if ok {
			for _, b := range f.boxes {
				for _, d := range s.dirs {
					t, stand := b+d, b-d
					if lv.Wall[t] || f.barrier[t] == zs || f.reach[stand] != rs || s.dist[t] < 0 {
						continue
					}
					if f.zone[t] != zs || s.mark[stand] != st || s.occ[t] {
						ok = false
						break
					}
					pushes++
				}
				if !ok {
					break
				}
			}
		}
		if ok && pushes > 0 {
			f.best = append(f.best[:0], f.boxes...)
		}
	}
	if len(f.best) == 0 {
		return 0
	}
	if s.cstamp > 1<<31 {
		clear(s.cmark)
		s.cstamp = 0
	}
	s.cstamp++
	for _, b := range f.best {
		s.cmark[b] = s.cstamp
	}
	s.stats.FrontierCorrals++
	return s.cstamp
}
