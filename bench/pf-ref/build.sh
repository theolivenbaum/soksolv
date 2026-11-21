#!/bin/bash
# Patched Go copy for pfOn (Festival-ordered pull, hub-pull's solver) parity: SOKSOLV_PFON=1 makes `-strategy pull` run the pull solver with
# pfOn + festXStyle() (as hub-pull builds it, main.go:1093), deterministic under -nodes. Output: $SCRATCH/soksolv-pf
set -e
export PATH=$PATH:/usr/local/go/bin
cd "$(dirname "$0")/../.."
S=${SCRATCH:-/tmp}; D=$S/go-pf-ref
rm -rf "$D"; mkdir -p "$D"
tar --exclude=levels -C .reference -cf - . | tar -xf - -C "$D"
python3 - "$D/fess_pull.go" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
old='\ts.pfSty = pfStyle{types: PFTypes} // hub-pull sets spread (festXStyle); exp-640 removed -pf-spread\n\treturn s'
assert old in s
s=s.replace(old,'\ts.pfSty = pfStyle{types: PFTypes} // hub-pull sets spread (festXStyle); exp-640 removed -pf-spread\n\tif os.Getenv("SOKSOLV_PFON") == "1" {\n\t\ts.pfOn, s.owner, s.bestSlot = true, FestXLane, FestXLane\n\t\ts.pfSty = festXStyle()\n\t}\n\treturn s')
if '"os"' not in s.split(')')[0]:
    s=s.replace('import (','import (\n\t"os"',1) if 'import (' in s else s.replace('import "slices"','import (\n\t"os"\n\t"slices"\n)',1)
open(p,'w').write(s)
PY
head -20 "$D/fess_pull.go" | grep -n "import" -A8 | head -12
(cd "$D" && go build -o "$S/soksolv-pf" .)
echo "built $S/soksolv-pf"
