# Full-set benchmark: Go vs C# (10 s per board, whole portfolio)

All 9771 distinct boards of LTS + OTS, one board at a time per solver, batches of ~50 boards (Go then C# per batch), every solution verified with the Go checker. C# = AOT build at commit d8126c7. Go portfolio = default lanes (no `full` lane); the C# portfolio also runs `full`.

```
=== machine ===
cpu:     Intel(R) Xeon(R) Processor @ 2.30GHz
cores:   4 logical
memory:  16087 MiB
os:      Ubuntu 24.04.5 LTS
runtime: .NET 11.0.0-rc.3.26522.148  simd: sse/neon
calib:   alu 591 Mop/s, mem-latency 5.8 Mop/s
go: go version go1.24.7 linux/amd64

boards 9771   solved: go 7049  cs 8735   both 7041  only-go 8  only-cs 1694  neither 1028
time on commonly solved: go 4592.5s  cs 1475.4s  ratio 0.32
mean peak MB on commonly solved: go 30.8  cs 44.8
  LTS: boards 3259  go 2648  cs 3043
  OTS: boards 6512  go 4401  cs 5692
only go solved: Sven_1623.xsb#1248, Sven Egevad Sharpen.xsb#119, Primus gradus ad Olympo.xsb#72, SokHard_163.xsb#138, Kevin B. Reilly.xsb#460, Sasquatch_VI_50.xsb#18, Premysl Zika.xsb#182, SokHard_163.xsb#124
only cs solved: Sasquatch II_50.xsb#6, Sven_1623.xsb#1334, Sven_1623.xsb#1555, Bruno Druille.xsb#21, CrazyMonk Disciple.xsb#40, Jean-Pierre Kent.xsb#2, Jean-Pierre Kent.xsb#21, Kevin B. Reilly.xsb#562, Sokoban Perfect.xsb#105, Sokoban Perfect.xsb#258, Sokoban Revenge.xsb#198, Sasquatch II_50.xsb#47, Sven_1623.xsb#273, CrazyMonk Disciple.xsb#38, Haikemono.xsb#11, Kevin B. Reilly.xsb#230, Kevin B. Reilly.xsb#485, Marcus Palstra.xsb#7, Master Head.xsb#39, Sasquatch_VII_50.xsb#27, Sven_1623.xsb#1081, Sven_1623.xsb#1473, ABHT -01-.xsb#10, CrazyMonk Disciple.xsb#12, KEAS Collection.xsb#13, Secundus gradus ad Olympo.xsb#33, Sonic -1-.xsb#15, Sven Egevad Sharpen.xsb#97, CrazyMonk Disciple.xsb#2, Kevin B. Reilly.xsb#437
worst cs/go time ratios (go>0.2s): Small chessboards.xsb#22 10.3x, A.K.K. Informatika.xsb#17 9.2x, Grigr2001_100.xsb#40 5.7x, Sasquatch VIII.xsb#34 5.1x, Sven_1623.xsb#112 4.7x, Grigr2001_100.xsb#66 4.6x, Sven_1623.xsb#109 4.3x, Small chessboards.xsb#24 4.3x
```
