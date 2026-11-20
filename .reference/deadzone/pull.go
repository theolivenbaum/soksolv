package deadzone

// Pull-mode corral deadlocks (exp-232), for searches that pull boxes from
// the solved position back to the start squares.
//
// The player's region R is fenced by boxes. If some box outside R ∪ fence is
// not on a start square, or a start square there is empty, a real pull
// solution must at some point put the player beyond the fence (pulling a
// box requires standing next to it; filling a square requires standing
// past it). Keep only the fence boxes, remove the rest (fewer boxes only
// help the player), make never-pullable boxes on start squares walls, and
// search the pulls: if the player can never reach a cell beyond the fence,
// the state is dead. Boxes that stop touching the beyond area are dropped
// (a relaxation), as in push mode.

// PullBoard adds the start squares and pull-dead squares for pull mode.
type PullBoard struct {
	*Board
	Start     []bool // start squares (the pull search's targets)
	PullDead  []bool // a box there can never be pulled to a start square
	startList []int32
}

// MaxRegion (0: off): check only when the player's region is at most
// 1/MaxRegion of the free floor.
var MaxRegion = 0

// PullChecker proves pull-mode corral deadlocks.
type PullChecker struct {
	c     *Checker
	pb    *PullBoard
	occ0  []bool
	mark0 []uint32
	rs0   uint32
	zs0   uint32
	Stats Stats
}

// NewPullChecker returns a pull-mode prover.
func NewPullChecker(pb *PullBoard, capStates int) *PullChecker {
	return &PullChecker{c: NewChecker(pb.Board, capStates), pb: pb}
}

// pullFixed: the greatest set of boxes none of which can ever be pulled
// (every direction needs two free cells in line beside the box). A fixed
// box off a start square is a deadlock.
func (pc *PullChecker) pullFixed(boxes []int32) bool {
	c, b := pc.c, pc.pb
	for _, x := range boxes {
		c.fixed[x] = true
	}
	blk := func(x int32) bool { return b.Wall[x] || c.fixed[x] }
	for changed := true; changed; {
		changed = false
		for _, x := range boxes {
			if !c.fixed[x] {
				continue
			}
			for _, d := range b.dirs {
				if !blk(x+d) && !blk(x+2*d) && !b.PullDead[x+d] {
					c.fixed[x] = false
					changed = true
					break
				}
			}
		}
	}
	for _, x := range boxes {
		if c.fixed[x] && !b.Start[x] {
			return true
		}
	}
	return false
}

// Dead reports whether the pull-mode state is a proved deadlock.
func (pc *PullChecker) Dead(occ []bool, boxes []int32, player int32) bool {
	c, b := pc.c, pc.pb
	c.pst++
	if c.pst == 0 {
		clear(c.pmark)
		c.pst = 1
	}
	rs := c.pst
	c.q = append(c.q[:0], player)
	c.pmark[player] = rs
	for i := 0; i < len(c.q); i++ {
		for _, d := range b.dirs {
			if y := c.q[i] + d; c.pmark[y] != rs && !b.Wall[y] && !occ[y] {
				c.pmark[y] = rs
				c.q = append(c.q, y)
			}
		}
	}
	return pc.DeadRegion(occ, boxes, c.q, c.pmark, rs)
}

// DeadRegion is Dead with the player's region given: its cells and a mark
// array holding stamp rs on them (the caller's own flood).
func (pc *PullChecker) DeadRegion(occ []bool, boxes, region []int32, mark []uint32, rs uint32) bool {
	c, b := pc.c, pc.pb
	pc.Stats.Calls++
	if len(region) == c.floorN-len(boxes) {
		return false // no fence
	}
	if MaxRegion > 0 && len(region)*MaxRegion > c.floorN-len(boxes) {
		return false // only enclosed players (cheap filter)
	}
	defer func() {
		for _, x := range boxes {
			c.fixed[x] = false
		}
	}()
	if pc.pullFixed(boxes) {
		pc.Stats.Frozen++
		return true
	}
	// Fence: non-fixed boxes 8-adjacent to the region.
	c.zst++
	if c.zst == 0 {
		clear(c.zmark)
		clear(c.area)
		c.zst = 1
	}
	zs := c.zst
	c.zone = c.zone[:0]
	w := b.W
	for _, x := range region {
		for _, d := range [8]int32{-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1} {
			y := x + d
			if occ[y] && !c.fixed[y] && c.zmark[y] != zs {
				c.zmark[y] = zs
				c.zone = append(c.zone, y)
			}
		}
	}
	if len(c.zone) == 0 {
		return false
	}
	// Work outside the region and fence: a box there off its start square,
	// or an empty start square there (fixed boxes are on start squares).
	work := false
	for _, x := range boxes {
		if !b.Start[x] && c.zmark[x] != zs {
			work = true
			break
		}
	}
	if !work {
		for _, x := range pc.pb.starts() {
			if !occ[x] && mark[x] != rs {
				work = true
				break
			}
		}
	}
	if !work {
		return false
	}
	pc.occ0, pc.mark0, pc.rs0, pc.zs0 = occ, mark, rs, zs
	pc.Stats.Corrals++
	if pc.search(boxes, region[0], 0) {
		pc.Stats.Dead++
		return true
	}
	return false
}

// isTarget: beyond the region and fence (free once others are removed), or
// beside an outside box (the first outside work needs the player there).
func (pc *PullChecker) isTarget(y int32) bool {
	c, b := pc.c, pc.pb
	if b.Wall[y] {
		return false
	}
	if pc.mark0[y] != pc.rs0 && c.zmark[y] != pc.zs0 && !c.fixed[y] {
		return true
	}
	for _, d := range b.dirs {
		x := y + d
		if pc.occ0[x] && c.zmark[x] != pc.zs0 && !c.fixed[x] && pc.mark0[x] != pc.rs0 {
			return true
		}
	}
	return false
}

// search: BFS over pulls of the fence boxes; true when the player can never
// reach a beyond cell (exhausted), false when it can or at the cap.
func (pc *PullChecker) search(boxes []int32, player int32, _ uint32) bool {
	c, b := pc.c, pc.pb
	k := len(c.zone)
	for _, x := range boxes {
		if c.fixed[x] {
			c.wall2[x] = true
		}
	}
	defer func() {
		for _, x := range boxes {
			c.wall2[x] = false
		}
	}()
	stride := k + 1
	c.arena = c.arena[:0]
	for _, z := range c.zone {
		c.occ[z] = true
	}
	p0, _ := c.childReach(player)
	escaped := pc.escaped()
	for _, z := range c.zone {
		c.occ[z] = false
	}
	if escaped {
		pc.Stats.Opened++
		return false
	}
	for _, z := range c.zone {
		c.arena = append(c.arena, int16(z))
	}
	sortInt16(c.arena[:k])
	c.arena = append(c.arena, int16(p0))
	need := 1
	for need < 4*c.Cap {
		need <<= 1
	}
	if len(c.slots) < need {
		c.slots = make([]int32, need)
	} else {
		clear(c.slots[:need])
	}
	slots := c.slots[:need]
	mask := uint64(need - 1)
	insert := func(st []int16) bool {
		n := int32(len(c.arena)/stride - 1)
		h := uint64(0xCBF29CE484222325)
		for _, v := range st {
			h = (h ^ uint64(uint16(v))) * 0x100000001B3
		}
		for i := (h ^ h>>29) & mask; ; i = (i + 1) & mask {
			if slots[i] == 0 {
				slots[i] = n + 1
				return true
			}
			o := int(slots[i]-1) * stride
			eq := true
			for j := range stride {
				if c.arena[o+j] != st[j] {
					eq = false
					break
				}
			}
			if eq {
				return false
			}
		}
	}
	insert(c.arena[:stride])
	states := 1
	if cap(c.cur) < k {
		c.cur = make([]int32, k)
	}
	cur := c.cur[:k]
	for head := 0; head*stride < len(c.arena); head++ {
		o := head * stride
		for i := range k {
			cur[i] = int32(c.arena[o+i])
			if cur[i] != 0 {
				c.occ[cur[i]] = true
			}
		}
		p := int32(c.arena[o+k])
		_, _, rs := c.reach(p)
		for i := range k {
			x := cur[i]
			if x == 0 {
				continue
			}
			for _, d := range b.dirs {
				q, q2 := x+d, x+2*d // player on q steps to q2, box x -> q
				if c.rmark[q] != rs || b.Wall[q2] || c.wall2[q2] || c.occ[q2] || b.PullDead[q] {
					continue
				}
				nxt := append(c.nxt[:0], cur...)
				nxt[i] = q
				c.occ[x], c.occ[q] = false, true
				pn, _ := c.childReach(q2)
				done := pc.escaped()
				if !done {
					for j, y := range nxt {
						if y != 0 && !pc.fencing(y) {
							c.occ[y] = false
							nxt[j] = 0
						}
					}
					pn, _ = c.childReach(q2)
					done = pc.escaped()
				}
				for _, y := range nxt {
					c.occ[y] = false
				}
				for _, y := range cur {
					if y != 0 {
						c.occ[y] = true
					}
				}
				c.nxt = nxt
				if done {
					for _, y := range cur {
						c.occ[y] = false
					}
					c.occ[0] = false
					pc.Stats.Opened++
					return false
				}
				base := len(c.arena)
				for _, v := range nxt {
					c.arena = append(c.arena, int16(v))
				}
				sortInt16(c.arena[base : base+k])
				c.arena = append(c.arena, int16(pn))
				if insert(c.arena[base:]) {
					states++
					if states > c.Cap {
						for _, y := range cur {
							c.occ[y] = false
						}
						c.occ[0] = false
						pc.Stats.Unknown++
						return false
					}
				} else {
					c.arena = c.arena[:base]
				}
			}
		}
		for _, y := range cur {
			c.occ[y] = false
		}
		c.occ[0] = false
	}
	return true
}

// escaped reports whether the last child flood reached a target cell: the
// first outside work (pulling an outside box, filling an outside start
// square) needs the player on one.
func (pc *PullChecker) escaped() bool {
	for _, y := range pc.c.zq {
		if pc.isTarget(y) {
			return true
		}
	}
	return false
}

// fencing: the box at y touches (8-neighbourhood) a cell beyond the region
// and fence.
func (pc *PullChecker) fencing(y int32) bool {
	c, w := pc.c, pc.pb.W
	for _, d := range [8]int32{-w - 1, -w, -w + 1, -1, 1, w - 1, w, w + 1} {
		u := y + d
		if !pc.pb.Wall[u] && pc.mark0[u] != pc.rs0 && c.zmark[u] != pc.zs0 && !c.fixed[u] {
			return true
		}
	}
	return false
}

func (pb *PullBoard) starts() []int32 {
	if pb.startList == nil {
		for x, s := range pb.Start {
			if s {
				pb.startList = append(pb.startList, int32(x))
			}
		}
	}
	return pb.startList
}

func sortInt16(a []int16) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
