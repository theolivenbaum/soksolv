#!/usr/bin/env python3
"""Go vs C# comparison on testdata levels. Always prints a machine header first.
usage: bench/compare.py [-t seconds] [-nodes N] [-j parallel] [files...]   (default: all testdata/*.xsb)
Both solvers run single-lane (`-strategy full` for Go, -procs 1), one level at a time."""
import json, os, subprocess, sys, glob, time, platform

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
tl, nodes, files = 20, 0, []
a = sys.argv[1:]
while a:
    x = a.pop(0)
    if x == "-t": tl = float(a.pop(0))
    elif x == "-nodes": nodes = int(a.pop(0))
    else: files.append(x)
files = files or sorted(glob.glob(f"{root}/testdata/*.xsb"))
scratch = os.environ.get("SCRATCH", "/tmp")
go_bin = os.environ.get("GO_SOKSOLV", f"{scratch}/soksolv-go")
cs_dll = f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet:/usr/local/go/bin", DOTNET_TieredPGO="1")

def sh(c, **k): return subprocess.run(c, shell=True, capture_output=True, text=True, env=env, **k)
if not os.path.exists(go_bin) or os.environ.get("REBUILD_GO"):
    r = sh(f"cd {root}/.reference && go build -o {go_bin} ."); assert r.returncode == 0, r.stderr
r = sh(f"cd {root} && dotnet build src/Soksolv.Cli -c Release 2>&1 | tail -1")
# machine header (C# prints cpu/mem/calibration; Go side shares the machine)
print(sh(f"dotnet {cs_dll} -header {files[0]} 1 -nodes 1 2>&1 >/dev/null").stdout, end="")
print(f"go: {sh('go version').stdout.strip()}   limit={tl}s nodes={nodes or '-'}   python {platform.python_version()}")
print(f"{'level':34} {'go ms':>8} {'cs ms':>8} {'cs/go':>6} | {'go nodes':>9} {'cs nodes':>9} | {'go push':>7} {'cs push':>7} | {'go MB':>6} {'cs MB':>6}")
tg = tc = 0; bad = 0; solved_g = solved_c = 0
for f in files:
    name = os.path.basename(f)[:-4]
    ng = f"-nodes {nodes}" if nodes else ""
    g = sh(f"{go_bin} -strategy full -procs 1 -t {tl} {ng} {f} 1")
    c = sh(f"dotnet {cs_dll} -t {tl} {ng} {f} 1")
    try: gj = json.loads(g.stdout)
    except Exception: gj = {"solved": False, "time_ms": 0, "nodes_expanded": 0, "pushes": 0, "peak_memory_mb": 0}
    try: cj = json.loads(c.stdout)
    except Exception: cj = {"solved": False, "time_ms": 0, "nodes_expanded": 0, "pushes": 0, "peak_memory_mb": 0, "verify_error": c.stderr[-200:]}
    flag = ""
    if gj["solved"] != cj["solved"]: flag += " SOLVED-MISMATCH"
    if gj["solved"] and cj["solved"] and (gj["nodes_expanded"] != cj["nodes_expanded"] or gj["pushes"] != cj["pushes"]): flag += " DIVERGED"
    if cj.get("verify_error"): flag += " VERIFY-FAIL"
    if gj["solved"]: solved_g += 1
    if cj["solved"]: solved_c += 1
    if gj["solved"] and cj["solved"]: tg += gj["time_ms"]; tc += cj["time_ms"]
    ratio = cj["time_ms"] / gj["time_ms"] if gj["time_ms"] else 0
    print(f"{name:34} {gj['time_ms']:8.0f} {cj['time_ms']:8.0f} {ratio:6.2f} | {gj['nodes_expanded']:9} {cj['nodes_expanded']:9} | {gj['pushes']:7} {cj['pushes']:7} | {gj['peak_memory_mb']:6.1f} {cj['peak_memory_mb']:6.1f}{flag}", flush=True)
print(f"solved go {solved_g}/{len(files)}  cs {solved_c}/{len(files)}   time on commonly solved: go {tg/1000:.1f}s cs {tc/1000:.1f}s  ratio {tc/tg if tg else 0:.2f}")
