#!/bin/bash
# Patched Go copy for the timed hub lanes: SOKSOLV_NODESLICES=K replaces the wall-clock slices of raceAlternate (hub-pull) by expansion counts
# (pull slice = K pull expansions, forward "time" = forward expansions) and the global -nodes N caps the lane's total expansions
# (deterministic: NodeBudget > 0 already disables every wall-clock decision, stall finishing included). Prints HUBRACE counters to stderr.
# Run: SOKSOLV_NODESLICES=1024 $SCRATCH/soksolv-hub -strategy hub-pull -procs 1 -nodes N -no-meet -retarget-reuse=false -lane-revive=false -t 1000000 file idx
# Output: $SCRATCH/soksolv-hub
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/../.."
S=${SCRATCH:-/tmp}; D=$S/go-hub-ref
rm -rf "$D"; mkdir -p "$D"
tar --exclude=levels -C .reference -cf - . | tar -xf - -C "$D"
python3 - "$D/hubrace.go" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
def rep(old,new,cnt=1):
    global s
    assert s.count(old)>=1, old
    s=s.replace(old,new,cnt)
rep('import (\n\t"math/bits"','import (\n\t"fmt"\n\t"math/bits"\n\t"os"\n\t"strconv"')
rep('\tpullSlice := festbUnit\n','''\tnsl := int64(0)
\tif v := os.Getenv("SOKSOLV_NODESLICES"); v != "" {
\t\tnsl, _ = strconv.ParseInt(v, 10, 64)
\t}
\tunit := festbUnit
\tif nsl > 0 {
\t\tunit = time.Duration(nsl)
\t}
\tpullSlice := unit
\tdefer func() {
\t\tfmt.Fprintf(os.Stderr, "HUBRACE pull=%d base=%d trials=%d fwd=%d strata=%d added=%d reached=%d ext=%d retries=%d shared=%d baseRuns=%d\\n", ps.expandedN, S.baseExp, S.trials, S.exp, len(st.sig), st.added, S.reached, S.extensions, S.retries, S.sharedSkip, S.baseRuns)
\t}()
''')
rep('\tlive := func() bool { return !Stop.Load() && (deadline.IsZero() || time.Now().Before(deadline)) }',
'\tlive := func() bool {\n\t\tif nsl > 0 {\n\t\t\treturn !Stop.Load() && (NodeBudget <= 0 || ps.expandedN+S.exp+S.baseExp < NodeBudget)\n\t\t}\n\t\treturn !Stop.Load() && (deadline.IsZero() || time.Now().Before(deadline))\n\t}')
rep('\t\t\t\tpullSlice = max(festbUnit, fwdT/time.Duration(festbFwdRatio))\n\t\t\t}\n\t\t\tr := ps.solve(roundEnd(pullSlice, deadline), memLimit)',
'''\t\t\t\tpullSlice = max(unit, fwdT/time.Duration(festbFwdRatio))
\t\t\t}
\t\t\tif nsl > 0 {
\t\t\t\troom := nsl
\t\t\t\tif NodeBudget > 0 {
\t\t\t\t\troom = min(nsl, max(1, NodeBudget-(ps.expandedN+S.exp+S.baseExp)))
\t\t\t\t}
\t\t\t\tps.nodeLimit = ps.expandedN + room
\t\t\t}
\t\t\tr := ps.solve(roundEnd(pullSlice, deadline), memLimit)''')
rep('\t\t\t\tfwdT += time.Since(t0)\n\t\t\t\tnodes += int(used)\n','\t\t\t\tif nsl > 0 {\n\t\t\t\t\tfwdT += time.Duration(used)\n\t\t\t\t} else {\n\t\t\t\t\tfwdT += time.Since(t0)\n\t\t\t\t}\n\t\t\t\tnodes += int(used)\n')
rep('\n\t\tfwdT += time.Since(t0)\n\t\tnodes += int(used)\n\t\tif sol != "" {\n\t\t\treport()','\n\t\tif nsl > 0 {\n\t\t\tfwdT += time.Duration(used)\n\t\t} else {\n\t\t\tfwdT += time.Since(t0)\n\t\t}\n\t\tnodes += int(used)\n\t\tif sol != "" {\n\t\t\treport()')
open(p,'w').write(s)
PY
python3 - "$D/fess_search.go" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
old='\t\ts.recordEdge(id, s.lastAdded)\n\t\tif DebugApplied != nil {'
assert old in s
s=s.replace(old,'\t\tif s1Trace {\n\t\t\tfmt.Fprintf(os.Stderr, "S1 %d node=%d move=%d child=%d w=%d stored=%d\\n", expanded, id, e.move-s.mvStart[id], cid, s.w[id], len(s.player))\n\t\t}\n'+old,1)
s=s.replace('import (','import (\n\t"os"',1) if '"os"' not in s else s
s+='\nvar s1Trace = os.Getenv("SOKSOLV_S1TRACE") == "1"\n'
open(p,'w').write(s)
PY
(cd "$D" && go build -o "$S/soksolv-hub" .)
echo "built $S/soksolv-hub"
