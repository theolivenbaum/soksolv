# Benchmark

soksolv against Festival 3.1 on the Large Test Suite (LTS, 3,272 levels) and the Open Test Suite
(OTS, 10,043 levels), run on the same hardware in November 2026.

| | soksolv | Festival 3.1 | difference |
|---|---|---|---|
| LTS solved | **3,218** | 3,208 | +10 |
| OTS solved | **9,620** | 9,602 | +18 |

| | both solve | only soksolv | only Festival | neither |
|---|---|---|---|---|
| LTS | 3,190 | 28 | 18 | 36 |
| OTS | 9,502 | 118 | 100 | 323 |

soksolv solved 24 boards (30 levels, counting copies in both suites) that, as far as we know, no
solver had solved before; Festival solved none of them in this run.

## Method

- Settings: 600 s per level, 8 dedicated cores, 15 GB memory cap (cgroup, no swap). Hardware:
  [hardware.txt](hardware.txt) (AMD EPYC 9555P, Ubuntu 24.04).
- Commands: `soksolv -t 600 <file> <level>` (defaults: `-mem 15360 -procs 8`) and
  `festival <file> -level <level> -time 600 -cores 8`.
- A level counts as solved if the solution replays correctly on the original level (`cmd/verify`),
  contains at least one push, and the solver's own reported time is at most 600 s. The 18 levels
  that start solved were re-run with the same settings after the solver learned that rule.
- The two suites share many boards. Each distinct board (9,756) was run once per solver, and the
  result counts for every level with that board. `same_board_as` in results.tsv shows the copies.
- Each solver ran once per board, so a few levels near the time limit can go either way between
  runs. An earlier soksolv run of the same set solved LTS 3,217 / OTS 9,594.

## Festival notes

- Built from the published Festival 3.1 source with g++, unchanged apart from two small Linux
  compatibility shims (core count and memory size queries) and larger thread stacks.
- Festival's compile-time limits (200 boxes, 51 columns) reject 33 boards; they count as unsolved.
  A rebuild with raised limits, run separately, solved 1 of those 33.
- One level (LTS Holland_81 #25) has trailing spaces that push its width past Festival's limit; it
  was run from its identical OTS copy instead (solved).

## Files

- `results.tsv`: one row per level (13,315): solved, time, moves, pushes and peak memory for
  soksolv; solved, time (whole seconds, as Festival reports it) and peak memory for Festival.
- `hardware.txt`: CPU, memory, OS and build details.
- `run.sh`: runs soksolv on a suite with these settings and checks every solution. It runs one
  level at a time, so a full suite takes days.
