# soksolv

> My blog post: [Introducing the World's Strongest Sokoban Solver](https://healeycodes.com/introducing-the-worlds-strongest-sokoban-solver)

A state of the art Sokoban solver written in Go, which runs a portfolio of search lanes in parallel.

As of writing (16/11/26), I believe it is the world's strongest Sokoban solver.

For further detail, please read my 5k+ word blog post linked at the top!

<br>

## Benchmark results

Both solvers on the same hardware: 600 s per level, 8 dedicated cores, 15 GB memory.

| | soksolv | Festival 3.1 | difference |
|---|---|---|---|
| Large Test Suite (3,272) | **3,218** | 3,208 | +10 |
| Open Test Suite (10,043) | **9,620** | 9,602 | +18 |

See [benchmark/README.md](benchmark/README.md) for the method, per-level results and hardware.

## Build

Requires Go 1.27 or later. Pure Go, no dependencies.

```sh
go build -o soksolv .
```

## Quick start

```sh
./run-xsokoban.sh
```

Builds the solver and runs the 90 XSokoban levels (60 s limit each).

## Usage

```sh
./soksolv [-t seconds] [-mem MiB] [-procs N] <file.xsb> [level index]
```

| Flag | Default | Meaning |
|---|---|---|
| `-t` | 0 (no limit) | time limit in seconds |
| `-mem` | 15360 | memory budget in MiB |
| `-procs` | 8 | threads running search code |

The level index is 1-based (default 1). Output is JSON on stdout: `solved`, `time_ms`, `moves`,
`pushes`, `peak_memory_mb`, and the `solution` in LURD notation (upper case = push). Exit code is
1 if the level was not solved.

Many other flags exist; they switch experimental mechanisms on or off and are not needed to
reproduce the results.

## Checking a solution

```sh
go build -o checksol ./cmd/verify
echo "<LURD solution>" | ./checksol <file.xsb> <level index>
```

It prints `OK` or `FAIL: reason`.

## Levels and benchmark

`levels/` holds the Large Test Suite (3,272 levels) and the Open Test Suite (10,043 levels).
`benchmark/` has the published results, the hardware they were measured on and a script to
reproduce them.
