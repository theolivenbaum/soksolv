package reverse

import (
	"math/bits"
	"os"

	"soksolv/arena"
)

// Compact (exp-397, -compact): store reverse nodes as delta records. A pull
// moves one box one step, so a node is its parent's box bitset plus one
// (from, direction) move. Only every compactChain-th node down a parent
// chain keeps a full bitset (a keyframe), so reconstructing a node costs at
// most compactChain-1 parent hops. Node ids, hashing and search order are
// unchanged; only the representation differs.
var Compact bool

// Default on (promoted 2026-11-08). SOKSOLV_COMPACT=0 turns it off before
// flag parsing, so whole test binaries can run under the plain store.
func init() { Compact = os.Getenv("SOKSOLV_COMPACT") != "0" }

// compactChain bounds the chain: a delta node is at most compactChain-1
// moves from its keyframe (2 header bits: 4 at most).
const compactChain = 4

// maxCompactFloor is the largest box-cell count a delta header addresses
// (12 bits); larger levels fall back to plain records.
const maxCompactFloor = 1 << 12

// nodeStore holds one record per node: parent, normalized player and the
// box bitset over floor indices. Plain mode keeps (1+nw) words per node;
// compact mode keeps one header word per node plus nw words per keyframe.
//
// Header word: bits 0-31 parent+1, 32-47 player, 48-49 chain (0 = keyframe),
// 50-51 pull direction, 52-63 floor index the box was pulled from.
type nodeStore struct {
	compact bool
	nw      int
	n       int
	plain   arena.Slice[uint64]
	hdr     arena.Slice[uint64]
	kf      arena.Slice[uint64]
	isKey   []uint64 // bit per node: keyframe
	base    []uint32 // per 64 nodes: keyframes before them (rank base)
	nKey    int
	nb      []int32 // nb[f*4+d]: floor index of f's neighbour in direction d (-1: none)
	bufA    []uint64
	bufB    []uint64
	bufU    []uint64
	rec     []uint64 // plain mode: the record being appended
}

func newNodeStore(nw int, compact bool, nb []int32, floor int) *nodeStore {
	t := &nodeStore{nw: nw, compact: compact && floor <= maxCompactFloor, nb: nb}
	if t.compact {
		t.hdr = arena.Make[uint64](1)
		t.kf = arena.Make[uint64](nw)
		t.bufA, t.bufB, t.bufU = make([]uint64, nw), make([]uint64, nw), make([]uint64, nw)
	} else {
		t.plain = arena.Make[uint64](nw + 1)
		t.rec = make([]uint64, 0, nw+1)
	}
	return t
}

func (t *nodeStore) Len() int { return t.n }

// Bytes is the capacity held (memory accounting).
func (t *nodeStore) Bytes() uint64 {
	if !t.compact {
		return uint64(t.plain.Elems()) * 8
	}
	return uint64(t.hdr.Elems()+t.kf.Elems()+cap(t.isKey))*8 + uint64(cap(t.base))*4
}

func (t *nodeStore) head(id int32) uint64 {
	if t.compact {
		return t.hdr.At(id)
	}
	return t.plain.At(id)
}

func (t *nodeStore) Parent(id int32) int32 { return int32(uint32(t.head(id))) - 1 }
func (t *nodeStore) Player(id int32) int32 { return int32(uint16(t.head(id) >> 32)) }

func (t *nodeStore) rank(id int32) int32 {
	w := id >> 6
	return int32(t.base[w]) + int32(bits.OnesCount64(t.isKey[w]&(1<<(uint(id)&63)-1)))
}

// Boxes returns node id's box bitset: a view into the store when one is
// stored, else buf filled by reconstruction. The result is read-only.
func (t *nodeStore) Boxes(id int32, buf []uint64) []uint64 {
	if !t.compact {
		return t.plain.Item(id)[1:]
	}
	h := t.hdr.At(id)
	if h>>48&3 == 0 {
		return t.kf.Item(t.rank(id))
	}
	var hs [compactChain]uint64
	c := 0
	for ; h>>48&3 != 0; c++ {
		hs[c] = h
		h = t.hdr.At(int32(uint32(h)) - 1)
	}
	// h is now the keyframe's header; its id is the last parent walked.
	kid := int32(uint32(hs[c-1])) - 1
	copy(buf, t.kf.Item(t.rank(kid)))
	for i := c - 1; i >= 0; i-- {
		f := int(hs[i] >> 52)
		g := t.nb[f*4+int(hs[i]>>50&3)]
		buf[f>>6] &^= 1 << (uint(f) & 63)
		buf[g>>6] |= 1 << (uint(g) & 63)
	}
	return buf
}

// Pulled returns the floor indices a delta node's box moved between, if the
// node is stored as a delta.
func (t *nodeStore) Pulled(id int32) (from, to int32, ok bool) {
	if !t.compact {
		return 0, 0, false
	}
	h := t.hdr.At(id)
	if h>>48&3 == 0 {
		return 0, 0, false
	}
	f := int32(h >> 52)
	return f, t.nb[int(f)*4+int(h>>50&3)], true
}

// Append stores a new node; from is the floor index its box was pulled
// from and dir the pull direction (ignored for roots, parent < 0).
func (t *nodeStore) Append(parent, player int32, boxes []uint64, from int32, dir int8) {
	h := uint64(uint32(parent+1)) | uint64(uint16(player))<<32
	if !t.compact {
		t.rec = append(append(t.rec[:0], h), boxes...)
		t.plain.Append(t.rec...)
		t.n++
		return
	}
	chain := uint64(0)
	if parent >= 0 {
		if pc := t.hdr.At(parent) >> 48 & 3; pc+1 < compactChain {
			chain = pc + 1
		}
	}
	id := t.n
	if id&63 == 0 {
		t.isKey = append(t.isKey, 0)
		t.base = append(t.base, uint32(t.nKey))
	}
	if chain == 0 {
		t.isKey[id>>6] |= 1 << (uint(id) & 63)
		t.kf.Append(boxes...)
		t.nKey++
	} else {
		h |= chain<<48 | uint64(dir)<<50 | uint64(from)<<52
	}
	t.hdr.Append(h)
	t.n++
}
