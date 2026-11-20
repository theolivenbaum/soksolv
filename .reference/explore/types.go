// Package explore holds search-exploration structures shared by the greedy
// engines (full search, reverse lanes).
package explore

// Types is type-based exploration (Xie, Müller, Holte et al. 2014, "Type-
// based exploration with multiple search queues"): open nodes are grouped
// by a type (e.g. heuristic value and goal count); a pop picks a random
// non-empty type, then a random node in it. Greedy search alternates with
// it, so plateaus are explored across their whole width instead of in one
// fixed order. Random choices come from a fixed-seed generator: a run is
// deterministic.
type Types struct {
	idx   map[uint64]int32
	dense [][]int32    // idx for keys h<<16|k with small h, k (no map lookup)
	nodes [][][2]int32 // (node, payload)
	live  []int32      // indices of non-empty types
	pos   []int32      // per type: its index in live, -1 if empty
	rng   uint64
	N     int
}

func NewTypes() *Types { return &Types{idx: map[uint64]int32{}, rng: 0x9E3779B97F4A7C15} }

func (t *Types) next() uint64 {
	t.rng ^= t.rng << 13
	t.rng ^= t.rng >> 7
	t.rng ^= t.rng << 17
	return t.rng
}

// Push adds node id (with a payload, e.g. its h) under type key.
func (t *Types) Push(key uint64, id, payload int32) {
	h, k := key>>16, key&0xFFFF
	var i int32
	ok := false
	if h < 4096 && k < 256 {
		for uint64(len(t.dense)) <= h {
			t.dense = append(t.dense, nil)
		}
		row := t.dense[h]
		for uint64(len(row)) <= k {
			row = append(row, -1)
		}
		t.dense[h] = row
		if i = row[k]; i >= 0 {
			ok = true
		}
	} else {
		i, ok = t.idx[key]
	}
	if !ok {
		i = int32(len(t.nodes))
		if h < 4096 && k < 256 {
			t.dense[h][k] = i
		} else {
			t.idx[key] = i
		}
		t.nodes = append(t.nodes, nil)
		t.pos = append(t.pos, -1)
	}
	if len(t.nodes[i]) == 0 {
		t.pos[i] = int32(len(t.live))
		t.live = append(t.live, i)
	}
	t.nodes[i] = append(t.nodes[i], [2]int32{id, payload})
	t.N++
}

// Pop removes and returns a random node (and its payload) of a random
// non-empty type.
func (t *Types) Pop() (int32, int32, bool) {
	if len(t.live) == 0 {
		return 0, 0, false
	}
	i := t.live[t.next()%uint64(len(t.live))]
	b := t.nodes[i]
	j := t.next() % uint64(len(b))
	e := b[j]
	b[j] = b[len(b)-1]
	t.nodes[i] = b[:len(b)-1]
	t.N--
	if len(t.nodes[i]) == 0 {
		p := t.pos[i]
		last := t.live[len(t.live)-1]
		t.live[p], t.pos[last] = last, p
		t.live = t.live[:len(t.live)-1]
		t.pos[i] = -1
	}
	return e[0], e[1], true
}
