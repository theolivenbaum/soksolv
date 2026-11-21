#!/bin/bash
# Which Go lines does `-strategy $STRAT` (default flags) execute? (coverage-instrumented Go build)
# usage: STRAT=fa2 bench/lane-scope.sh [nodes=20000]
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/.."
OUT=${SCOPE_OUT:-/tmp/lane-scope-$STRAT}; N=${1:-20000}
rm -rf $OUT; mkdir -p $OUT/covdir
(cd .reference && go build -cover -o $OUT/soksolv-cov .)
for f in testdata/*.xsb; do GOCOVERDIR=$OUT/covdir $OUT/soksolv-cov -strategy "$STRAT" -procs 1 -nodes $N -t 60 $f 1 >/dev/null 2>&1 || true; done
(cd .reference && go tool covdata textfmt -i=$OUT/covdir -o $OUT/cov.txt)
python3 - "$OUT/cov.txt" <<'PY'
import re,sys,collections
cl=collections.defaultdict(set); tot=collections.defaultdict(set)
for l in open(sys.argv[1]):
    if l.startswith('mode'): continue
    m=re.match(r'(.+?):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)',l)
    f,a,b,c=m.group(1),int(m.group(2)),int(m.group(3)),int(m.group(5))
    for i in range(a,b+1):
        tot[f].add(i)
        if c>0: cl[f].add(i)
rows=sorted(((len(cl[f]),len(tot[f]),f) for f in tot),reverse=True)
s=0
for c,t,f in rows:
    if c: s+=c; print(f"{c:6} {t:6}  {f.replace('soksolv/','')}")
print("TOTAL covered lines:",s)
PY
