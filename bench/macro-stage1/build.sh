#!/bin/bash
# Build the patched Go Stage-1 reference lane into $SCRATCH/soksolv-s1 (default /tmp).
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/../.."
S=${SCRATCH:-/tmp}; D=$S/go-macro
rm -rf "$D"; mkdir -p "$D"
tar --exclude=levels -C .reference -cf - . | tar -xf - -C "$D"
cp bench/macro-stage1/stage1.go "$D/"
(cd "$D" && patch -p1 -s -i "$OLDPWD/bench/macro-stage1/reference-patch.diff")
# alt-engine planner switch (see stage1.go altOn)
(cd "$D" && sed -i 's|\ts.noPlan = stageN < 3|\ts.noPlan = stageN < 3\n\t\ts.planAlt = altOn|' solver.go)
(cd "$D" && go build -o "$S/soksolv-s1" .)
echo "built $S/soksolv-s1"
