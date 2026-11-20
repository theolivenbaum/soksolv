#!/usr/bin/env python3
"""Compare two fullrun TSVs (go, cs): solved counts, only-X lists, time on commonly solved, per-suite."""
import sys, collections
def load(p):
    d = {}; hdr = []
    for l in open(p):
        if l.startswith("# "): hdr.append(l[2:].rstrip()); continue
        f = l.rstrip("\n").split("\t")
        if f[0] == "suite": continue
        d[f[3]] = dict(suite=f[0], file=f[1], level=f[2], solved=f[4] == "yes", t=float(f[5]), pushes=int(f[7] or 0), mb=float(f[8] or 0))
    return d, hdr
g, hg = load(sys.argv[1]); c, hc = load(sys.argv[2])
keys = [k for k in g if k in c]
print("\n".join(hc[:8]))
n = len(keys)
sg = sum(g[k]["solved"] for k in keys); sc = sum(c[k]["solved"] for k in keys)
both = [k for k in keys if g[k]["solved"] and c[k]["solved"]]
og = [k for k in keys if g[k]["solved"] and not c[k]["solved"]]; oc = [k for k in keys if c[k]["solved"] and not g[k]["solved"]]
tg = sum(g[k]["t"] for k in both); tc = sum(c[k]["t"] for k in both)
print(f"\nboards {n}   solved: go {sg}  cs {sc}   both {len(both)}  only-go {len(og)}  only-cs {len(oc)}  neither {n-len(both)-len(og)-len(oc)}")
print(f"time on commonly solved: go {tg:.1f}s  cs {tc:.1f}s  ratio {tc/tg if tg else 0:.2f}")
mg = [g[k]["mb"] for k in both]; mc = [c[k]["mb"] for k in both]
if both: print(f"mean peak MB on commonly solved: go {sum(mg)/len(mg):.1f}  cs {sum(mc)/len(mc):.1f}")
for suite in ("LTS", "OTS"):
    ks = [k for k in keys if g[k]["suite"] == suite]
    if ks: print(f"  {suite}: boards {len(ks)}  go {sum(g[k]['solved'] for k in ks)}  cs {sum(c[k]['solved'] for k in ks)}")
if og: print("only go solved:", ", ".join(f"{g[k]['file']}#{g[k]['level']}" for k in og[:30]))
if oc: print("only cs solved:", ", ".join(f"{c[k]['file']}#{c[k]['level']}" for k in oc[:30]))
# slowest ratios
rs = sorted(((c[k]["t"] / max(g[k]["t"], 0.01), k) for k in both if g[k]["t"] > 0.2), reverse=True)[:8]
print("worst cs/go time ratios (go>0.2s):", ", ".join(f"{c[k]['file']}#{c[k]['level']} {r:.1f}x" for r, k in rs))
