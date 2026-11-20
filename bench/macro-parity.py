#!/usr/bin/env python3
"""Macro lane parity: C# `-strategy macro -parity` vs the patched Go reference (SOKSOLV_STAGE1=1), compared by the
S1HASH fingerprint (expanded/stored/generated/duplicates + hash over every applied (node, move, child)).
usage: bench/macro-parity.py [-nodes N] [-list specs.tsv | files...]   env: GO_S1 (patched Go binary), CS_BIN (C# CLI command)"""
import os, subprocess, sys, glob, re, time
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
scratch = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_S1", f"{scratch}/soksolv-s1")
aot = f"{root}/artifacts/aot-macro/Soksolv.Cli"
cs = os.environ.get("CS_BIN", aot if os.path.exists(aot) else f"dotnet {root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll")
nodes = 20000; specs = []; a = sys.argv[1:]; files = []
while a:
    x = a.pop(0)
    if x == "-nodes": nodes = int(a.pop(0))
    elif x == "-list":
        for l in open(a.pop(0)): f, i = l.rstrip("\n").split("\t")[:2]; specs.append((f, int(i)))
    else: files.append(x)
if not specs: specs = [(f, 1) for f in (files or sorted(glob.glob(f"{root}/testdata/*.xsb")))]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
def run(cmd, extra):
    e = dict(env, **extra)
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, env=e)
    m = re.search(r"S1HASH (.*)", r.stderr)
    return (m.group(1) if m else "NO-S1HASH " + r.stderr[-150:].replace("\n", " ")), r.stdout
print(subprocess.run(f"{cs} -header -strategy macro -nodes 1 '{specs[0][0]}' 1 2>&1 >/dev/null | head -8", shell=True, capture_output=True, text=True, env=env).stdout, end="")
print(f"nodes cap {nodes}; go={go}; cs={cs}")
bad = 0; tg = tc = 0
for f, i in specs:
    t0 = time.time(); g, gout = run(f"{go} -strategy macro -procs 1 -nodes {nodes} -t 120 '{f}' {i}", {"SOKSOLV_STAGE1": "1"}); t1 = time.time()
    c, cout = run(f"{cs} -parity -strategy macro -nodes {nodes} -t 120 '{f}' {i}", {"SOKSOLV_S1": "1"}); t2 = time.time()
    tg += t1 - t0; tc += t2 - t1
    ok = g == c
    if not ok: bad += 1
    print(f"{'OK  ' if ok else 'DIFF'} {os.path.basename(f)[:-4]}#{i:<4} go {t1-t0:6.2f}s cs {t2-t1:6.2f}s  {g if ok else 'GO: ' + g + '  CS: ' + c}", flush=True)
print(f"{len(specs)} levels, {bad} differences; wall go {tg:.1f}s cs {tc:.1f}s")
