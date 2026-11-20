#!/bin/bash
# Build soksolv and run the 90 XSokoban levels with a 60 s limit each.
# Prints each level's result as it finishes, then the total and average time.
set -e
cd "$(dirname "$0")"
go build -o soksolv .

file="levels/Large Test Suite/XSokoban_90.xsb"
solved=0; total_ms=0
for i in $(seq 1 90); do
  out=$(./soksolv -t 60 "$file" "$i" || true)
  ms=$(echo "$out" | sed -n 's/^  "time_ms": \([0-9.]*\).*/\1/p' | head -1)
  if echo "$out" | grep -q '"solved": true'; then
    solved=$((solved + 1)); status=solved
  else
    status=unsolved
  fi
  printf 'XSokoban #%-2d  %-8s  %8.2f s\n' "$i" "$status" "$(echo "${ms:-0} / 1000" | bc -l)"
  total_ms=$(echo "$total_ms + ${ms:-0}" | bc -l)
done
printf '\nsolved %d/90  total %.2f s  average %.2f s per level\n' \
  "$solved" "$(echo "$total_ms / 1000" | bc -l)" "$(echo "$total_ms / 90000" | bc -l)"
