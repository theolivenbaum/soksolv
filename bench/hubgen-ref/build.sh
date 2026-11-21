#!/bin/bash
# Patched Go copy for the hub-gen / hub lane (hubgen.go + hub.go):
#  * -strategy hub-gen -hub-gen-only -nodes N prints a deterministic fingerprint line `HUBGENFP ...` (generator counters, node checksum, pool, best hubs)
#    with an unbounded harvest (SOKSOLV_GENDET=1);
#  * SOKSOLV_NODESLICES=K replaces the hub lane's wall-clock decisions by expansion counts: forward slice q = K expansions, generator burst 4K,
#    generator slice = half the round's forward expansions, harvest unbounded, no idle sleeping, -nodes N caps generator + forward expansions.
#    Prints `HUBRACE <counters>` to stderr (same text as the C# fingerprint, bench/hub-parity.py with HUB_STRATEGY=hub-gen).
# Output: $SCRATCH/soksolv-hubgen
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/../.."
S=${SCRATCH:-/tmp}; D=$S/go-hubgen-ref
rm -rf "$D"; mkdir -p "$D"
tar --exclude=levels -C .reference -cf - . | tar -xf - -C "$D"
python3 - "$D" <<'PY'
import sys
D=sys.argv[1]
def patch(fn, edits, imports=None):
    p=D+'/'+fn; s=open(p).read()
    for old,new in edits:
        assert s.count(old)>=1, (fn, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

# ---- hubgen.go: fingerprint in genOnly
patch('hubgen.go', [
('\tel := time.Since(t0)\n\th.harvestG(harvest)\n',
 '\tel := time.Since(t0)\n\tif os.Getenv("SOKSOLV_GENDET") != "" {\n\t\tharvest = 100000 * time.Hour\n\t}\n\th.harvestG(harvest)\n'),
('\tps := h.ps\n\tout := map[string]any{\n',
 '''\tif os.Getenv("SOKSOLV_GENDET") != "" {
\t\tvar sb strings.Builder
\t\trch := -1
\t\tif g.reach != nil {
\t\t\trch = g.reach.nCls
\t\t}
\t\tfull := 0
\t\tif g.full {
\t\t\tfull = 1
\t\t}
\t\tfmt.Fprintf(&sb, "exp=%d cre=%d dup=%d nodes=%d cand=%d best=%d chk=%x full=%d reach=%d pool=%d tierseen=%d,%d,%d,%d,%d scanned=%d elig=%d", g.expanded, g.created, g.dupes, len(g.nodes), len(g.cand), g.best, g.chk(), full, rch, h.nPool, h.tierSeen[0], h.tierSeen[1], h.tierSeen[2], h.tierSeen[3], h.tierSeen[4], h.scanned, h.eligible)
\t\tfor i, c := range rows {
\t\t\tif i >= 5 {
\t\t\t\tbreak
\t\t\t}
\t\t\tnd := g.nodes[c.id]
\t\t\tfeas := 1
\t\t\tif g.reach != nil {
\t\t\t\th.unpackNode(c.id, h.buf)
\t\t\t\tif !g.reach.feasible(h.buf) {
\t\t\t\t\tfeas = 0
\t\t\t\t}
\t\t\t}
\t\t\tpk := 0
\t\t\tif c.pocket {
\t\t\t\tpk = 1
\t\t\t}
\t\t\tfmt.Fprintf(&sb, " | %d cut=%d pin=%d trap=%d dist=%d tier=%d pk=%d d=%d reg=%d trp=%d mob=%d bic=%d far=%d g=%d feas=%d", c.id, c.cut, c.pinned, c.trapped, c.dist, c.tier, pk, nd.depth, nd.reg, nd.trap, nd.mob, nd.bic, nd.far, nd.g, feas)
\t\t}
\t\tfmt.Fprintln(os.Stderr, "HUBGENFP "+sb.String())
\t\treturn Result{Reason: "hub ended"}
\t}
\tps := h.ps
\tout := map[string]any{
'''),
('import (\n\t"os"','import (\n\t"fmt"\n\t"os"'),
('\tover := g.run(h.sliceEnd(d), 0)\n','\tvar over bool\n\tif hubNsl > 0 {\n\t\tover = g.run(time.Time{}, min(int64(d), h.room()))\n\t} else {\n\t\tover = g.run(h.sliceEnd(d), 0)\n\t}\n'),
])
open(D+'/hubgen.go','a').write('''
func (g *hubGen) chk() uint64 {
\th := uint64(14695981039346656037)
\tmix := func(v uint32) {
\t\th ^= uint64(v)
\t\th *= 1099511628211
\t}
\tfor _, nd := range g.nodes {
\t\tmix(uint32(nd.parent))
\t\tmix(nd.mv)
\t\tmix(uint32(nd.player))
\t\tmix(uint32(nd.reg))
\t\tmix(uint32(nd.trap))
\t\tmix(uint32(nd.mob))
\t\tmix(uint32(nd.bic))
\t\tmix(uint32(nd.far))
\t\tmix(uint32(nd.g))
\t\tmix(uint32(nd.depth))
\t}
\treturn h
}
''')

# ---- hub.go: node-slice mode
LIVE_A='!Stop.Load() && (h.deadline.IsZero() || time.Now().Before(h.deadline))'
LIVE_B='Stop.Load() || !h.deadline.IsZero() && !time.Now().Before(h.deadline)'
p=D+'/hub.go'; s=open(p).read()
assert s.count(LIVE_A)>=3 and s.count(LIVE_B)>=1
s=s.replace(LIVE_A,'h.live()').replace(LIVE_B,'!h.live()')
def rep(old,new):
    global s
    assert s.count(old)==1,(old,s.count(old))
    s=s.replace(old,new)
rep('import (','import (\n\t"fmt"',)
rep('\tq := U / 4\n','\tq := U / 4\n\tif hubNsl > 0 {\n\t\tq = time.Duration(hubNsl)\n\t}\n')
rep('\tburst := time.Duration(float64(max(total, 0)) * hubGenInit)\n\tif total <= 0 {\n\t\tburst = 10 * time.Second\n\t}\n',
    '\tburst := time.Duration(float64(max(total, 0)) * hubGenInit)\n\tif total <= 0 {\n\t\tburst = 10 * time.Second\n\t}\n\tif hubNsl > 0 {\n\t\tburst = time.Duration(4 * hubNsl)\n\t}\n')
rep('\t\th.harvest(budget)\n','\t\tif hubNsl > 0 {\n\t\t\tbudget = 100000 * time.Hour\n\t\t}\n\t\th.harvest(budget)\n')
rep('\t\t\tt0 := time.Now()\n\t\t\tranFwd = true\n\t\t\tsol, r, joined := h.forward(c, slice)\n\t\t\tif HubRace {\n\t\t\t\tsumFwd += time.Since(t0) // exp-686: an expansion-budget trial may end early\n\t\t\t} else {',
    '\t\t\tt0 := time.Now()\n\t\t\tranFwd = true\n\t\t\tbefore := c.fwd.expandedN\n\t\t\tsol, r, joined := h.forward(c, slice)\n\t\t\th.fwdSum += c.fwd.expandedN - before\n\t\t\tif hubNsl > 0 {\n\t\t\t\tsumFwd += time.Duration(c.fwd.expandedN - before)\n\t\t\t} else if HubRace {\n\t\t\t\tsumFwd += time.Since(t0) // exp-686: an expansion-budget trial may end early\n\t\t\t} else {')
rep('\t\t\t\tif w > 0 {\n\t\t\t\t\ttime.Sleep(w)','\t\t\t\tif w > 0 && hubNsl == 0 {\n\t\t\t\t\ttime.Sleep(w)')
rep('\tf := c.fwd\n\tstop := drain.StopCheck()','\tf := c.fwd\n\tif hubNsl > 0 {\n\t\tf.nodeLimit = min(c.winExp, f.expandedN+min(int64(d), h.room()))\n\t}\n\tstop := drain.StopCheck()')
rep('func (h *hub) sliceEnd(d time.Duration) time.Time { return roundEnd(d, h.deadline) }',
    'func (h *hub) sliceEnd(d time.Duration) time.Time {\n\tif hubNsl > 0 {\n\t\treturn h.deadline\n\t}\n\treturn roundEnd(d, h.deadline)\n}')
rep('\tdefer func() {\n\t\tfwdTracked.Store(0)\n\t\tTracked11.Store(0)\n\t\th.report("end")',
    '\tdefer func() {\n\t\tfwdTracked.Store(0)\n\t\tTracked11.Store(0)\n\t\tif hubNsl > 0 {\n\t\t\tfmt.Fprintf(os.Stderr, "HUBRACE nominated=%d committed=%d reached=%d abandoned=%d ext=%d memfull=%d exhausted=%d tail=%d infeasible=%d reachrej=%d fallbackrej=%d gen=%d/%d/%d fwd=%d rounds=%d idle=%d rescans=%d pool=%d scanned=%d seen=%d elig=%d best_on=%d\\n", h.nominated, h.committed, h.reached, h.abandoned, h.extensions, h.memfull, h.exhausted, h.tailFail, h.infeasible, h.reachRej, h.fallbackRej, h.gen.expanded, h.gen.created, h.gen.dupes, h.fwdSum, h.rounds, h.idle, h.rescans, h.nPool, h.scanned, h.nSeen, h.eligible, h.bestOnAll)\n\t\t}\n\t\th.report("end")')
s+='''
var hubNsl = func() int64 {
\tv, _ := strconv.ParseInt(os.Getenv("SOKSOLV_NODESLICES"), 10, 64)
\treturn v
}()

func (h *hub) live() bool {
\tif hubNsl > 0 {
\t\treturn !Stop.Load() && (NodeBudget <= 0 || h.gen.expanded+h.fwdSum < NodeBudget)
\t}
\treturn !Stop.Load() && (h.deadline.IsZero() || time.Now().Before(h.deadline))
}

func (h *hub) room() int64 {
\tif NodeBudget > 0 {
\t\treturn max(1, NodeBudget-(h.gen.expanded+h.fwdSum))
\t}
\treturn 1 << 62
}
'''
s=s.replace('\tidleEnd                                              string   // exp-758: why the config ended idle ("zero-cand", "idle")','\tidleEnd                                              string   // exp-758: why the config ended idle ("zero-cand", "idle")\n\tfwdSum                                               int64')
open(p,'w').write(s)
PY
(cd "$D" && gofmt -l . >/dev/null; go build -o "$S/soksolv-hubgen" .)
echo "built $S/soksolv-hubgen"
