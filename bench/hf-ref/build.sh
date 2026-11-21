#!/bin/bash
# Patched Go copy for hf parity: prints "HFSTORED pull=.. toward=.. on_start=.. tail=.." to stderr after the toward phase.
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/../.."
S=${SCRATCH:-/tmp}; D=$S/go-hf
rm -rf "$D"; mkdir -p "$D"
tar --exclude=levels -C .reference -cf - . | tar -xf - -C "$D"
python3 - "$D/main.go" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
old='sol, r := fessTowardReal(rows, lv, tgt, deadline, memLimit, recs["toward"], &Budget3, &Tracked3, 2, forwardNodes)'
assert old in s
s=s.replace(old,'sol, r, tws := fessTowardRealWithPrev(rows, lv, tgt, deadline, memLimit, recs["toward"], &Budget3, &Tracked3, 2, forwardNodes, nil)\n\t\t\tfmt.Fprintf(os.Stderr, "HFSTORED pull=%d toward=%d on_start=%d tail=%d\\n", len(ps.player), len(tws.player), onStart, len(tgt.Tail))')
open(p,'w').write(s)
PY
(cd "$D" && go build -o "$S/soksolv-hf" .)
echo "built $S/soksolv-hf"
