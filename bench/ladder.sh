#!/bin/bash
# One rung of the measurement ladder: both solvers on every STRIDE-th distinct board with limit T seconds.
#   bench/ladder.sh <stride> <T> <tag>      results: $SCRATCH/ladder_<tag>_{go,cs}.tsv ; summary printed by bench/summarize.py
set -e
cd "$(dirname "$0")/.."
S=${SCRATCH:-/tmp}; stride=$1; T=$2; tag=$3
export SCRATCH=$S GO_SOKSOLV=${GO_SOKSOLV:-$S/soksolv} GO_CHECKSOL=${GO_CHECKSOL:-$S/checksol} CS_BIN=${CS_BIN:-$PWD/artifacts/aot/Soksolv.Cli}
rm -f $S/ladder_${tag}_go.tsv $S/ladder_${tag}_cs.tsv
python3 bench/fullrun.py go -t $T -stride $stride -out $S/ladder_${tag}_go.tsv > $S/ladder_${tag}_go.log 2>&1
python3 bench/fullrun.py cs -t $T -stride $stride -out $S/ladder_${tag}_cs.tsv > $S/ladder_${tag}_cs.log 2>&1
python3 bench/summarize.py $S/ladder_${tag}_go.tsv $S/ladder_${tag}_cs.tsv
