# 600 s benchmark: C# port vs the published Go results

The settings of `.reference/benchmark`: 600 s per level, 15 GB, 196 batches (`-shard K/196`), all 3272 LTS and
10043 OTS levels. Boards are de-duplicated by content: a repeated board is solved once and counted for each level. Every solution is
replayed with the Go checker and a board counts as solved only if the verified solution has at least one push and
finishes within the limit. The C# side is the default portfolio of the Native AOT build
(`artifacts/aot-full/Soksolv.Cli`, `-mem 15000`), run with `bench/fullrun.py cs -t 600 -shard K/196`. The comparison
against the published Go results is `bench/refcompare.py`.

**The two sides ran on different hardware.** The published Go numbers come from 8 pinned AMD EPYC 9555P cores;
this run used a 4-core Xeon 2.3 GHz VM (header below). Solved counts are comparable in spirit (same limit, same
memory) but the C# side had half the cores, so times are not a like-for-like speed comparison. The like-for-like
comparison is the Go default portfolio run on this same VM, described at the end of this page.

```
=== machine ===
cpu:     Intel(R) Xeon(R) Processor @ 2.30GHz
cores:   4 logical
memory:  16087 MiB
os:      Ubuntu 24.04.5 LTS
runtime: .NET 11.0.0-rc.3.26522.148  simd: sse/neon
calib:   alu 591 Mop/s, mem-latency 5.8 Mop/s
go: go version go1.24.7 linux/amd64
```

## Result (all 13315 levels)

| Suite | Levels | Go (published) | C# (this VM) | Both | Only Go | Only C# | Neither |
|---|---|---|---|---|---|---|---|
| LTS | 3272 | 3218 | 3198 | 3194 | 24 | 4 | 50 |
| OTS | 10043 | 9620 | 9542 | 9530 | 90 | 12 | 411 |
| All | 13315 | 12838 | 12740 | 12724 | 114 | 16 | 461 |

- C# solves **12740 of 13315 levels (95.7 %)** against 12838 for the published Go results, 98 fewer (0.7 %).
- 12724 levels are solved by both. 114 levels are solved only by Go and 16 only by C#.
- Of the 9279 boards C# solved, 7898 took 1 s or less, 8777 took 10 s or less, 9017 took 60 s or less and
  9235 took 300 s or less.
- Peak memory on solved boards: median 47 MB, 99th percentile 3.4 GB, maximum 7.6 GB.
- On the 9252 distinct boards both solved, summed solve time is 40178 s for the published Go run (8 EPYC cores)
  and 54953 s for C# (4 Xeon cores). Different hardware, so informational only.

### Boards only Go solved (first 40 of 114 levels)

Grigr2001_100#2, Holland_81#75, Sasquatch II_50#43, Sasquatch_III_50#22, #41, Sasquatch_IV_50#44,
Sasquatch_VI_50#18, Sasquatch_V_50#50, Sven_1623 #23, #221, #271, #829, #1035, #1048, #1236, #1245, #1253,
#1254, #1320, #1341, #1381, #1424, #1434, #1530, ABHT -01- #18, #99, Andre Bernier #7, #8, #10, #12,
Bruno Druille#20, CrazyMonk Disciple#31, David Holland dh#13, Frantisek Pokorny #3, #79,
GRIGoRusha Remodel Club#99, Galadriel #8, #9, Haikemono #3, #16 (the full list comes from
`bench/refcompare.py` on the run's TSV).

### Boards only C# solved

Sasquatch II_50#46, Sasquatch_IV_50#40, Sasquatch_VI_50#42, Sven_1623#224, Marcus Palstra#8, Sasquatch X#29,
Small chessboards#34, Sokoban Revenge#266, Sonic -2-#38, Tertius gradus ad Olympo#35,
The Bagatelle Collection 2#9, Zbigniew Kornas#30.

## What the gap looks like

The only-Go levels cluster in the hardest sets (Sven_1623, the Sasquatch sets, Sven Egevad Sharpen, Sonic,
Frantisek Pokorny, Andre Bernier). That is where the Go portfolio's lanes that C# lacks or schedules differently
matter: the `relay` lane is not ported, some toward-lane scheduling details differ, and with 8 cores Go runs more
lanes at full speed than the 4-core VM can. The same-VM Go run below separates the two effects.

## Same-VM Go run (default portfolio, 4 cores)

Go's default portfolio (all lanes, `-procs 4`, 600 s, 15 GB, 196 batches, same split) on the same VM, run after the
C# run so the two never competed for cores. Results are added to this page when it finishes (see the next section).

_Pending._
