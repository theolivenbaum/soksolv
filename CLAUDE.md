# soksolv-net — .NET 11 port of the soksolv Sokoban solver

The original Go implementation lives, untouched, in `.reference/` (read-only; never edit it, never
build into it). It is a portfolio of search lanes (~52k lines of Go) that solves Sokoban levels
in parallel. This repo is a modern C# (.NET 11, C# latest) port.

## Goals (in priority order)

1. **Correctness** — every solution the port prints must replay OK on the original level
   (`Soksolv.Core.Verifier`, ported from `.reference/cmd/verify`). Solved-counts must not regress
   against the Go solver on the same levels/time limits.
2. **Performance** — match or beat the Go solver on solve time and nodes/sec, per lane, on the same
   machine. Benchmarks are always run against the Go binary built from `.reference/`.
3. **Memory** — minimum bytes per stored state, zero allocations in search hot paths
   (steady state: `GC.GetAllocatedBytesForCurrentThread()` flat across expansions), search data
   in unmanaged/chunked arenas rather than the GC heap (no LOH copies when tables grow).
4. **Idiomatic modern C#** — `Span<T>`, `stackalloc`, `ref struct`, `readonly struct`,
   `System.Numerics.BitOperations`, `System.Runtime.Intrinsics` (Vector128/256/512 with
   `IsHardwareAccelerated` guards and a scalar fallback), generic math where it pays, no LINQ/
   delegates/closures/boxing in hot paths.

## Layout

```
.reference/            original Go repo (levels/, benchmark/, tests, all Go source)
src/Soksolv.Core/      library: level model, parser, verifier, solver lanes
src/Soksolv.Cli/       `soksolv` executable (same flags/JSON as the Go CLI where sensible)
tests/Soksolv.Tests/   xunit tests (ports of the Go *_test.go + golden solutions)
bench/Soksolv.Bench/   BenchmarkDotNet micro-benchmarks + the Go-vs-C# comparison harness
testdata/              small subset of levels used for the exploration/validation (copied from .reference/levels)
todo.md                port plan and status — keep it current
```

## Conventions

- **Benchmark with the Native AOT build** (`bench/publish-aot.sh` → `artifacts/aot/Soksolv.Cli`): it has no JIT
  warm-up and is the fastest mode (see docs/data-model.md §4). Use the JIT build only for debugging/profiling.

- Toolchain: .NET SDK 11.0 (installed under `/root/.dotnet`; `export PATH=$PATH:/root/.dotnet`).
  Go 1.27 is at `/usr/local/go/bin` for building the reference (`cd .reference && go build -o <out> .`).
- Level data is parsed by `Core` into a padded grid (outer ring is wall) exactly like Go's
  `Level`; cell ids are `y*W+x` so Go and C# traces/diagnostics are directly comparable.
- Every benchmark run **must print a machine header first**: CPU model, logical cores, RAM, OS,
  .NET/Go versions and a fixed calibration micro-benchmark number (`Soksolv.Core.Diagnostics.MachineInfo`
  / `bench/run-compare.sh`). Runs may land on different VMs; never compare numbers across headers
  without normalising by the calibration score.
- Compare like with like: same level, same lane, same time limit, same thread count; record Go's
  nodes_expanded vs ours (search order differences make equal node counts a correctness signal).
- Don't commit `bin/`, `obj/`, result TSVs. Do commit `todo.md` updates with each milestone.
- Don't port Go experiment flags (`-exp-NNN`) unless they're on by default in the Go solver; port the
  default configuration first, flags later (tracked in todo.md).

## Porting rules of thumb

- Port package by package, leaf-first (`reach`, `sidedead`, `diagdead`, `cluster`, `deadzone`, …),
  each with a differential test against the Go behaviour (golden outputs produced by Go helper
  programs under `bench/golden/`, never by hand).
- Keep Go's algorithmic behaviour (tie-breaking, heap order) identical until correctness is
  validated; optimise data layout afterwards, re-validating node counts.
