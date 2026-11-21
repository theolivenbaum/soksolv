package main

// Incremental region counts for FESS children. For box b removed from the
// board, one iterative Tarjan pass over the free cells gives the number of
// empty-floor components and, for each free cell t, how many pieces t's
// component splits into when a box is placed on t. A child moving b to t
// then has comps - 1 + pieces[t] regions: O(1) per candidate instead of a
// flood fill (regions) per candidate.
type articulation struct {
	node  []artNode // per cell, one cache line per visit
	st    uint32
	comps int
	stack []artFrame
}

type artNode struct {
	disc, low, pieces int32
	seen              uint32
}

type artFrame struct {
	c, parent int32
	next      int8
}

// computeArticulation analyses the current s.occ (with the moving box
// already removed by the caller).
func (s *solver) computeArticulation() {
	a := &s.art
	n := len(s.lv.Wall)
	if a.node == nil {
		a.node = make([]artNode, n)
	}
	a.st++
	st := a.st
	a.comps = 0
	if s.floor == nil {
		s.regions() // fills s.floor
	}
	wall, occ, node := s.lv.Wall, s.occ, a.node
	dirs := s.dirs
	timer := int32(0)
	for _, root := range s.floor {
		if wall[root] || occ[root] || node[root].seen == st {
			continue
		}
		a.comps++
		timer++
		node[root] = artNode{timer, timer, 0, st}
		a.stack = append(a.stack[:0], artFrame{root, -1, 0})
		for len(a.stack) > 0 {
			f := &a.stack[len(a.stack)-1]
			if f.next < 4 {
				d := dirs[f.next]
				f.next++
				y := f.c + d
				if wall[y] || occ[y] {
					continue
				}
				if ny := &node[y]; ny.seen != st {
					timer++
					*ny = artNode{timer, timer, 1, st}
					a.stack = append(a.stack, artFrame{y, f.c, 0})
				} else if y != f.parent {
					nc := &node[f.c]
					nc.low = min(nc.low, ny.disc)
				}
				continue
			}
			// Finished f.c: report to its parent.
			c, p := f.c, f.parent
			a.stack = a.stack[:len(a.stack)-1]
			if p < 0 {
				continue
			}
			np, nc := &node[p], &node[c]
			np.low = min(np.low, nc.low)
			if nc.low >= np.disc {
				np.pieces++ // c's subtree separates from p's other parts
			}
		}
		// The root's pieces are its DFS children (counted above from 0);
		// a non-root's count started at 1 for the part containing its parent.
	}
}

// childRegions is the region count after placing a box on free cell t.
func (s *solver) childRegions(t int32) int {
	return s.art.comps - 1 + int(s.art.node[t].pieces)
}

// checkRegions makes open() compare every incremental count with a full
// flood (tests only).
var checkRegions = false
