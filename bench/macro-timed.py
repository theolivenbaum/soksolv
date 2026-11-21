#!/usr/bin/env python3
"""Timed-mode sanity check of the macro lane: C# vs the patched Go reference on a level list at -t SECONDS (not -nodes), same stage/feature set.
usage: bench/macro-timed.py [-t 10] [-list specs.tsv]   env: STAGE FEAT GO_S1 CS_BIN"""
import os, subprocess, sys, json, time
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_S1", f"{S}/soksolv-s1"); cs = os.environ.get("CS_BIN", f"{root}/artifacts/aot-macro3/Soksolv.Cli")
stage, feat = os.environ.get("STAGE", "5"), os.environ.get("FEAT", "31")
t = 10.0; specs = []; a = sys.argv[1:]
while a:
    x = a.pop(0)
    if x == "-t": t = float(a.pop(0))
    elif x == "-list":
        for l in open(a.pop(0)): f, i = l.rstrip("\n").split("\t")[:2]; specs.append((f, int(i)))
env = dict(os.environ, DOTNET_ROOT="/root/.dotnet")
print(subprocess.run(f"{cs} -header -strategy macro -nodes 1 '{specs[0][0]}' 1 2>&1 >/dev/null | head -8", shell=True, capture_output=True, text=True, env=env).stdout, end="")
print(f"timed -t {t}; stage {stage} feat {feat}; go={go} cs={cs}")
def run(cmd, extra):
    t0 = time.time(); r = subprocess.run(cmd, shell=True, capture_output=True, text=True, env=dict(env, **extra)); el = time.time() - t0
    try: j = json.loads(r.stdout)
    except Exception: return False, el, 0
    return bool(j.get("solved")), j.get("time_ms", el * 1000) / 1000, j.get("nodes_expanded", 0)
sg = sc = 0; tg = tc = 0.0
for f, i in specs:
    g = run(f"{go} -strategy macro -procs 1 -t {t} '{f}' {i}", {"SOKSOLV_STAGE": stage, "SOKSOLV_FEAT": feat})
    c = run(f"{cs} -parity -macro-stage {stage} -macro-features {feat} -strategy macro -t {t} '{f}' {i}", {})
    sg += g[0]; sc += c[0]
    if g[0] and c[0]: tg += g[1]; tc += c[1]
    print(f"{os.path.basename(f)[:-4]}#{i:<4} go {'solved' if g[0] else 'no    '} {g[1]:6.2f}s n={g[2]:8}   cs {'solved' if c[0] else 'no    '} {c[1]:6.2f}s n={c[2]:8}", flush=True)
print(f"solved: go {sg} cs {sc} of {len(specs)}; time on commonly solved: go {tg:.1f}s cs {tc:.1f}s")
