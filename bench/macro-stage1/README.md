# Macro lane Stage-1 Go reference

The Stage-1 C# macro lane is validated against a *patched copy* of the Go source (never `.reference`):

```sh
bench/macro-stage1/build.sh      # copies .reference to $SCRATCH/go-macro, applies reference-patch.diff + stage1.go, builds $SCRATCH/soksolv-s1
bench/macro-parity.py -nodes 20000 [-list specs.tsv]    # C# `-strategy macro -parity` vs the reference: S1HASH fingerprints
```
`SOKSOLV_STAGE1=1` switches the Go copy to the reduced lane (rooms/hotspots/move-access/out-of-plan/sink plan and all
default-on experiment features off, single engine); `SOKSOLV_S1TRACE=1` prints every applied move for divergence hunting.
