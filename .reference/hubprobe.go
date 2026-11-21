package main

// exp-449: hub cross-test tooling (diagnostics only).
//
//	-hub-dump FILE       festb/festx append every forward target ("hub") they choose to FILE
//	-target-file FILE    -strategy tfwd: run our toward (festb-fwd) search at a fixed target read from FILE
//	-target-index N      which board of FILE (default -1: the last)
//	-hub-nocorral        toward search without any corral pruning
//	-hub-char            tfwd: only characterise the board(s) of FILE
//
// Board files: blocks that start with a line "HUB ..." (ours) or "IMAGINED" (Festival's debug build,
// diag/oracle/festival-build/debug-dump.patch) followed by one row per level row, with
// '#' wall, '$' box, '@' player, ' ' floor.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"soksolv/meet"
)

var (
	HubDump     string
	TargetFile  string
	TargetIndex = -1
	HubNoCorral bool
	HubLine     string // exp-459: tfwd writes the root-to-best-node line as xsb boards to this file
	HubChar     bool
	hubT0       = time.Now()
	hubMu       sync.Mutex
	hubWallDiff int
)

func hubBoard(lv *Level, boxes []int32, player int32) string {
	isBox := map[int32]bool{}
	for _, b := range boxes {
		isBox[b] = true
	}
	var sb strings.Builder
	for y := 1; y < lv.H-1; y++ {
		for x := 1; x < lv.W-1; x++ {
			c := int32(y*lv.W + x)
			switch {
			case lv.Wall[c]:
				sb.WriteByte('#')
			case isBox[c]:
				sb.WriteByte('$')
			case c == player:
				sb.WriteByte('@')
			default:
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// dumpHub appends a forward target chosen by a festb/festx lane.
func dumpHub(lv *Level, lane string, tgt *meet.Target, backExp int64, run int, commit bool) {
	if HubDump == "" || tgt == nil {
		return
	}
	hubMu.Lock()
	defer hubMu.Unlock()
	f, err := os.OpenFile(HubDump, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "HUB lane=%s t_ms=%d back_exp=%d run=%d tail=%d commit=%v\n%s", lane, time.Since(hubT0).Milliseconds(), backExp, run, len(tgt.Tail), commit, hubBoard(lv, tgt.Boxes, tgt.Player))
}

// readHubBoards returns the boards (rows) and headers of every HUB / IMAGINED block in path.
func readHubBoards(path string, h int) ([][]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var boards [][]string
	var heads []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "HUB") || lines[i] == "IMAGINED" {
			if i+h >= len(lines)+0 && i+h > len(lines)-0 {
				break
			}
			if i+1+h > len(lines) {
				break
			}
			boards = append(boards, slices.Clone(lines[i+1:i+1+h]))
			heads = append(heads, lines[i])
			i += h
		}
	}
	return boards, heads, nil
}

// boardTarget converts a hub board to a target (player normalised to the smallest reachable cell).
func boardTarget(lv *Level, board []string) (*meet.Target, error) {
	var boxes []int32
	player := int32(-1)
	for y, r := range board {
		for x := 0; x < len(r); x++ {
			c := int32((y+1)*lv.W + x + 1)
			switch r[x] {
			case '$', '*':
				boxes = append(boxes, c)
			case '@', '+':
				player = c
			}
			if r[x] == '#' && !lv.Wall[c] {
				// Festival fills useless floor, and boxes frozen on goals, with walls: tolerated, reported;
				// a frozen start box is a box of the hub too.
				hubWallDiff++
				if slices.Contains(lv.Boxes, c) && lv.Goal[c] {
					boxes = append(boxes, c)
				}
			}
		}
	}
	if len(boxes) != len(lv.Boxes) {
		return nil, fmt.Errorf("board has %d boxes, level %d", len(boxes), len(lv.Boxes))
	}
	if player < 0 {
		return nil, fmt.Errorf("board has no player")
	}
	for _, b := range boxes {
		if lv.Wall[b] {
			return nil, fmt.Errorf("box in wall")
		}
	}
	occ := make([]bool, len(lv.Wall))
	for _, b := range boxes {
		occ[b] = true
	}
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	seen := map[int32]bool{player: true}
	q := []int32{player}
	minC := player
	for i := 0; i < len(q); i++ {
		for _, d := range dirs {
			nb := q[i] + d
			if !lv.Wall[nb] && !occ[nb] && !seen[nb] {
				seen[nb] = true
				q = append(q, nb)
				minC = min(minC, nb)
			}
		}
	}
	return &meet.Target{Boxes: boxes, Player: minC}, nil
}

type probeReport struct {
	Header       string           `json:"header"`
	Boxes        int              `json:"boxes"`
	CorralMode   string           `json:"corral"`
	NodeLimit    int64            `json:"node_limit"`
	Expanded     int64            `json:"expanded"`
	Stored       int              `json:"stored"`
	BestOn       int              `json:"best_boxes_on_target"`
	BestOnAt     int64            `json:"best_at_exp"`
	ReachedBox   int64            `json:"reached_boxset_exp"`
	ReachedExact int64            `json:"reached_exact_exp"`
	StartOn      int              `json:"start_boxes_on_target"`
	StartMatch   int              `json:"start_match_dist"`
	Reason       string           `json:"reason"`
	Seconds      float64          `json:"seconds"`
	Trajectory   [][2]int64       `json:"best_traj,omitempty"`
	BestMatch    int              `json:"best_match_dist"`
	BestMatchAt  int64            `json:"best_match_at_exp"`
	FirstMatchLE map[string]int64 `json:"first_exp_match_le"` // matching distance to the hub <= k (Festival hits at idst 2-9)
	CorralDead   int              `json:"apply_corral_dead"`  // exp-459: children rejected by the apply-time prover (each costs an expansion)
	DiagDead     int              `json:"gen_diag_dead"`      // exp-459: candidate pushes pruned by -diag-dead at generation
	Generated    int              `json:"gen_generated"`
	Duplicates   int              `json:"apply_duplicates"`
}

// hubProbe runs our toward search at a fixed target and reports whether (and when) it attains it.
func hubProbe(rows []string, lv *Level, deadline time.Time, memLimit uint64) Result {
	boards, heads, err := readHubBoards(TargetFile, lv.H-2)
	if err != nil || len(boards) == 0 {
		return Result{Reason: fmt.Sprintf("tfwd: no boards in %s (%v)", TargetFile, err)}
	}
	idx := TargetIndex
	if idx < 0 {
		idx = len(boards) + idx
	}
	if idx < 0 || idx >= len(boards) {
		return Result{Reason: "tfwd: bad -target-index"}
	}
	tgt, err := boardTarget(lv, boards[idx])
	fmt.Fprintf(os.Stderr, "HUBWALLDIFF %d\n", hubWallDiff)
	if err != nil {
		return Result{Reason: "tfwd: " + err.Error()}
	}
	if HubChar {
		hubCharacterise(rows, lv, tgt, heads[idx], deadline)
		return Result{Reason: "char only"}
	}
	t0 := time.Now()
	s := newTowardReal(rows, lv, tgt, deadline, nil, &Budget3, &Tracked3, 2, NodeBudget)
	s.stopCheck = nil
	rep := probeReport{Header: heads[idx], Boxes: len(tgt.Boxes), NodeLimit: NodeBudget, BestOnAt: -1, ReachedBox: -1, ReachedExact: -1}
	switch {
	case HubNoCorral:
		rep.CorralMode = "none"
	case FestCorral:
		rep.CorralMode = "fest"
	default:
		rep.CorralMode = "ours"
	}
	for _, b := range lv.Boxes {
		if s.tgt[b] {
			rep.StartOn++
		}
	}
	rep.StartMatch = s.matchDist(lv.Boxes)
	buf := make([]int32, s.n)
	rep.BestMatch, rep.BestMatchAt = 1<<30, -1
	rep.FirstMatchLE = map[string]int64{}
	bestID := int32(-1)
	DebugApplied = func(s *solver, e fessEntry, cid int32) {
		if cid < 0 {
			return
		}
		s.unpack(cid, buf)
		on := 0
		for _, b := range buf {
			if s.tgt[b] {
				on++
			}
		}
		if on > rep.BestOn {
			rep.BestOn, rep.BestOnAt, bestID = on, s.expandedN, cid
			rep.Trajectory = append(rep.Trajectory, [2]int64{s.expandedN, int64(on)})
		}
		if md := s.matchDist(buf); md < assignmentInf {
			if md < rep.BestMatch {
				rep.BestMatch, rep.BestMatchAt = md, s.expandedN
			}
			for _, k := range []int{2, 5, 10, 20} {
				if key := fmt.Sprint(k); md <= k {
					if _, ok := rep.FirstMatchLE[key]; !ok {
						rep.FirstMatchLE[key] = s.expandedN
					}
				}
			}
		}
		if on == len(buf) {
			if rep.ReachedBox < 0 {
				rep.ReachedBox = s.expandedN
			}
			if s.player[cid] == tgt.Player && rep.ReachedExact < 0 {
				rep.ReachedExact = s.expandedN
				s.cancelled = true // stop: the target is attained
				s.nodeLimit = s.expandedN
			}
		}
	}
	defer func() { DebugApplied = nil }()
	_, r := s.towardRun(deadline, memLimit)
	rep.Expanded, rep.Stored, rep.Reason = s.expandedN, len(s.player), r.Reason
	rep.Seconds = time.Since(t0).Seconds()
	rep.CorralDead, rep.DiagDead, rep.Generated, rep.Duplicates = s.stats.CorralDead, s.stats.DiagDead, s.stats.Generated, s.stats.Duplicates
	if HubLine != "" && bestID >= 0 {
		var line []int32
		for id := bestID; id >= 0; id = s.parent[id] {
			line = append(line, id)
		}
		var sb strings.Builder
		for i := len(line) - 1; i >= 0; i-- {
			s.unpack(line[i], buf)
			fmt.Fprintf(&sb, "; %d depth=%d id=%d\n%s", len(line)-i, len(line)-1-i, line[i], xsbBoard(lv, buf, s.player[line[i]]))
		}
		os.WriteFile(HubLine, []byte(sb.String()), 0o644)
	}
	b, _ := json.Marshal(rep)
	fmt.Fprintf(os.Stderr, "TFWD %s\n", b)
	return Result{Reason: "tfwd done", Nodes: int(s.expandedN)}
}

// hubCharacterise prints the board's structure in the terms of Festival's backward order and ours.
func hubCharacterise(rows []string, lv *Level, tgt *meet.Target, head string, deadline time.Time) {
	ps := newPullSolver(lv, deadline)
	ps.pfOn = true
	ps.pfSty = pfStyle{types: PFTypes, spread: true, bicon: true}
	ps.pfInit()
	clear(ps.occ)
	for _, b := range tgt.Boxes {
		ps.occ[b] = true
	}
	out := map[string]any{"header": head, "boxes": len(tgt.Boxes)}
	out["regions_total"] = ps.regions() // free-floor components
	out["rooms_festRooms"] = ps.festRooms()
	out["rooms_connectivity"] = ps.roomConnectivity()
	d := ps.pfMatch(tgt.Boxes)
	out["pf_dist_from_goals"] = d
	out["pf_spread_sum"] = ps.pfSpreadSum(tgt.Boxes)
	out["hot_pfHot_unpullable_boxes"] = ps.pfHot(tgt.Boxes)
	{
		fs := newSolverUntil(lv, deadline)
		clear(fs.occ)
		for _, b := range tgt.Boxes {
			fs.occ[b] = true
		}
		counts := newHotspotTable(fs).counts(fs, tgt.Boxes)
		sum := 0
		for _, c := range counts {
			sum += c
		}
		out["hot_hotspotsgo_blocker_boxes"] = hotspotCount(counts)
		out["hot_hotspotsgo_pairs"] = sum
		out["hot_hotRel_toward"] = fs.hotRel(tgt.Boxes)
	}
	out["bicon_cells_off_cycle"] = ps.pfBicon()
	onGoal, onStart, offGoalFromGoal := 0, 0, 0
	for _, b := range tgt.Boxes {
		if lv.Goal[b] {
			onGoal++
		}
		if slices.Contains(lv.Boxes, b) {
			onStart++
		}
	}
	for _, g := range lv.Boxes { // pull view: the solved position is lv.Boxes' complement? goals are lv.Goal cells
		_ = g
	}
	out["boxes_on_goals"] = onGoal
	out["boxes_off_goals"] = len(tgt.Boxes) - onGoal
	out["boxes_on_start_squares"] = onStart
	_ = offGoalFromGoal
	// player region and free-floor graph structure
	occ := ps.occ
	dirs := [4]int32{-int32(lv.W), int32(lv.W), -1, 1}
	cells := len(lv.Wall)
	reg := make([]int32, cells)
	for i := range reg {
		reg[i] = -1
	}
	var sizes []int
	for c := range int32(cells) {
		if lv.Wall[c] || occ[c] || reg[c] >= 0 {
			continue
		}
		id := int32(len(sizes))
		reg[c] = id
		q := []int32{c}
		for i := 0; i < len(q); i++ {
			for _, dd := range dirs {
				nb := q[i] + dd
				if !lv.Wall[nb] && !occ[nb] && reg[nb] < 0 {
					reg[nb] = id
					q = append(q, nb)
				}
			}
		}
		sizes = append(sizes, len(q))
	}
	pr := reg[tgt.Player]
	out["player_region_cells"] = sizes[pr]
	out["free_regions"] = len(sizes)
	var small int
	for i, z := range sizes {
		if int32(i) != pr && z > 0 {
			small += z
		}
	}
	out["free_cells_outside_player_region"] = small
	// articulation points and dead ends of the player's region
	deg := func(c int32) int {
		n := 0
		for _, dd := range dirs {
			if nb := c + dd; !lv.Wall[nb] && !occ[nb] {
				n++
			}
		}
		return n
	}
	art, cut := hubArticulation(lv, occ, tgt.Player, dirs)
	out["cut_score"] = cut
	dead, corr := 0, 0
	for c := range int32(cells) {
		if reg[c] != pr {
			continue
		}
		switch deg(c) {
		case 1:
			dead++
		case 2:
			corr++
		}
	}
	out["articulation_cells"] = art
	out["dead_end_cells"] = dead
	out["degree2_cells"] = corr
	// forward closeness: matching distance from the start to this hub
	s := newTowardReal(rows, lv, tgt, deadline, nil, &Budget3, &Tracked3, 2, 1)
	out["match_dist_start_to_hub"] = s.matchDist(lv.Boxes)
	onT := 0
	for _, b := range lv.Boxes {
		if s.tgt[b] {
			onT++
		}
	}
	out["start_boxes_on_hub_squares"] = onT
	b, _ := json.Marshal(out)
	fmt.Fprintf(os.Stderr, "HUBCHAR %s\n", b)
}

// hubArticulation returns the number of articulation cells of the free-floor graph in the region of p, and
// the cut score: over articulation cells that are not empty goals, the size of everything the cell separates
// from the region's largest remaining component (exp-448 section 2b).
func hubArticulation(lv *Level, occ []bool, p int32, dirs [4]int32) (int, int) {
	cells := len(lv.Wall)
	disc := make([]int32, cells)
	low := make([]int32, cells)
	size := make([]int32, cells)
	seen := make([]bool, cells)
	type fr struct{ u, par, d int32 }
	st := []fr{{p, -1, 0}}
	seen[p] = true
	t := int32(1)
	disc[p], low[p] = t, t
	size[p] = 1
	// per articulation cell: sum and max of the separated child-subtree sizes
	sepSum := make([]int32, cells)
	sepMax := make([]int32, cells)
	isArt := make([]bool, cells)
	rootKids := 0
	for len(st) > 0 {
		f := &st[len(st)-1]
		if f.d < 4 {
			v := f.u + dirs[f.d]
			f.d++
			if lv.Wall[v] || occ[v] || v == f.par {
				continue
			}
			if seen[v] {
				low[f.u] = min(low[f.u], disc[v])
				continue
			}
			seen[v] = true
			t++
			disc[v], low[v] = t, t
			size[v] = 1
			st = append(st, fr{v, f.u, 0})
			continue
		}
		u, par := f.u, f.par
		st = st[:len(st)-1]
		if par >= 0 {
			size[par] += size[u]
			low[par] = min(low[par], low[u])
			if par == p {
				rootKids++
				sepSum[p] += size[u]
				sepMax[p] = max(sepMax[p], size[u])
			} else if low[u] >= disc[par] {
				isArt[par] = true
				sepSum[par] += size[u]
				sepMax[par] = max(sepMax[par], size[u])
			}
		}
	}
	if rootKids > 1 {
		isArt[p] = true
	}
	total := size[p]
	n, cut := 0, 0
	for c, a := range isArt {
		if !a {
			continue
		}
		n++
		if lv.Goal[c] {
			continue // an empty goal is exempt
		}
		rest := total - 1 - sepSum[c] // the part above the cell
		if int32(c) == p {
			rest = 0
		}
		cut += int(total - 1 - max(rest, sepMax[c]))
	}
	return n, cut
}

// xsbBoard renders a position with the level's goals ('*' box on goal, '+' player on goal).
func xsbBoard(lv *Level, boxes []int32, player int32) string {
	isBox := map[int32]bool{}
	for _, b := range boxes {
		isBox[b] = true
	}
	var sb strings.Builder
	for y := 1; y < lv.H-1; y++ {
		for x := 1; x < lv.W-1; x++ {
			c := int32(y*lv.W + x)
			ch := byte(' ')
			switch {
			case lv.Wall[c]:
				ch = '#'
			case isBox[c] && lv.Goal[c]:
				ch = '*'
			case isBox[c]:
				ch = '$'
			case c == player && lv.Goal[c]:
				ch = '+'
			case c == player:
				ch = '@'
			case lv.Goal[c]:
				ch = '.'
			}
			sb.WriteByte(ch)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
