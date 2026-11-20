#!/bin/bash
# Run soksolv on every level of a suite with the benchmark settings (600 s, 15 GB, 8 threads) and
# check each solution with cmd/verify. Writes one line per level to a TSV and prints a tally.
#   benchmark/run.sh ["Large Test Suite"|"Open Test Suite"] [out.tsv]
# The published runs also pinned each job to 8 dedicated cores and capped it at 15 GB with a cgroup
# (see hardware.txt); -mem 15360 is the solver's own budget, the same as its default.
set -e
cd "$(dirname "$0")/.."
suite=${1:-Large Test Suite}; out=${2:-results-$(echo "$suite" | tr -d ' ').tsv}
go build -o soksolv . && go build -o checksol ./cmd/verify
printf 'file\tlevel\tsolved\ttime_s\n' > "$out"
n=0; solved=0
for f in "levels/$suite"/*.xsb; do
  i=1
  while ! echo | ./checksol "$f" "$i" | grep -q "out of range"; do
    res=$(./soksolv -t 600 -mem 15360 -procs 8 "$f" "$i" || true)
    sol=$(echo "$res" | sed -n 's/^  "solution": "\(.*\)",$/\1/p')
    ms=$(echo "$res" | sed -n 's/^  "time_ms": \([0-9.]*\).*/\1/p')
    ok=no
    if echo "$res" | grep -q '"solved": true' && [ "$(echo "$sol" | ./checksol "$f" "$i")" = OK ]; then
      ok=yes; solved=$((solved + 1))
    fi
    n=$((n + 1))
    printf '%s\t%d\t%s\t%s\n' "$(basename "$f")" "$i" "$ok" "$(echo "${ms:-0} / 1000" | bc -l | xargs printf '%.3f')" | tee -a "$out"
    i=$((i + 1))
  done
done
echo "solved $solved/$n ($suite), results in $out"
