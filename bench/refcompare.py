#!/usr/bin/env python3
"""Compare a fullrun.py C# TSV (600 s run) with the published Go results in .reference/benchmark/results.tsv.
usage: refcompare.py ours.tsv [reference results.tsv]
Counts per suite are over LEVELS (copies of a board count once per level, like the reference README table);
only boards already covered by ours.tsv are included. Reference times come from other hardware (8 pinned
EPYC cores): times are informational, not a like-for-like speed comparison."""
import sys, os, collections
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ours_p = sys.argv[1]; ref_p = sys.argv[2] if len(sys.argv) > 2 else f"{root}/.reference/benchmark/results.tsv"
ours = {}
hdr = []
for l in open(ours_p):
    if l.startswith("# "): hdr.append(l[2:].rstrip()); continue
    f = l.rstrip("\n").split("\t")
    if f[0] == "suite": continue
    ours[(f[0], f[1], f[2])] = (f[4] == "yes", float(f[5]))
ref = []
for l in open(ref_p):
    f = l.rstrip("\n").split("\t")
    if f[0] == "suite": continue
    ref.append(dict(suite=f[0], file=f[1], level=f[2], same=f[3], solved=f[4] == "yes", t=float(f[5] or 0)))
tally = {s: collections.Counter() for s in ("LTS", "OTS")}
only_cs = []; only_go = []; tg = tc = 0.0; nb = 0
for r in ref:
    if r["same"]:
        st, rest = r["same"].split(" ", 1); fn, lv = rest.rsplit(" #", 1); key = (st, fn, lv)
    else: key = (r["suite"], r["file"], r["level"])
    if key not in ours: continue
    cs, ct = ours[key]; go = r["solved"]
    t = tally[r["suite"]]; t["levels"] += 1; t["go"] += go; t["cs"] += cs
    t["both" if go and cs else "only_go" if go else "only_cs" if cs else "neither"] += 1
    if go and cs and not r["same"]: tg += r["t"]; tc += ct; nb += 1
    if not r["same"]:
        if cs and not go: only_cs.append(f"{r['file']}#{r['level']}")
        if go and not cs: only_go.append(f"{r['file']}#{r['level']}")
print("\n".join(hdr[:8]))
print(f"\nreference Go: 600 s, 8 pinned EPYC cores, 15 GB   |   C#: this VM (see header), limit per header")
print(f"{'':6}{'levels':>8}{'go':>7}{'cs':>7}{'both':>7}{'only go':>9}{'only cs':>9}{'neither':>9}")
tot = collections.Counter()
for s, t in tally.items():
    tot.update(t); print(f"{s:6}{t['levels']:>8}{t['go']:>7}{t['cs']:>7}{t['both']:>7}{t['only_go']:>9}{t['only_cs']:>9}{t['neither']:>9}")
t = tot; print(f"{'ALL':6}{t['levels']:>8}{t['go']:>7}{t['cs']:>7}{t['both']:>7}{t['only_go']:>9}{t['only_cs']:>9}{t['neither']:>9}")
print(f"\ndistinct boards both solved: {nb}; summed solve time go {tg:.0f}s (ref hardware)  cs {tc:.0f}s (this VM)")
print("only Go solved (boards):", ", ".join(only_go[:40]))
print("only C# solved (boards):", ", ".join(only_cs[:40]))
