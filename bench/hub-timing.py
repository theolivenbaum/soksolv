#!/usr/bin/env python3
"""Real (wall-clock) behaviour of a hub lane, Go vs C#: solve rate and time per level over R runs at -t T.
usage: bench/hub-timing.py <strategy> <T> <reps> [list.tsv|testdata]      env: GO_SOKSOLV, CS_BIN (dll or AOT binary), SCRATCH
Single-lane runs (`-strategy S -procs 1`), so the lane gets one core; the other agents' load makes absolute times noisy: compare solve counts first."""
import json, os, subprocess, sys, glob, statistics
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv")
cs = os.environ.get("CS_BIN") or f"{root}/src/Soksolv.Cli/bin/Release/net11.0/Soksolv.Cli.dll"
cs = f"dotnet {cs}" if cs.endswith(".dll") else cs
strat, T, reps = sys.argv[1], sys.argv[2], int(sys.argv[3])
src = sys.argv[4] if len(sys.argv) > 4 else "testdata"
if src == "testdata": specs = [(f, 1) for f in sorted(glob.glob(f"{root}/../soksolv/testdata/*.xsb"))]
else: specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
def run(c):
    r = subprocess.run(f"nice -n 5 {c}", shell=True, capture_output=True, text=True, env=env, timeout=float(T) * 4 + 120)
    try: j = json.loads(r.stdout); return (j["solved"], j["time_ms"] / 1000, j.get("peak_memory_mb", 0))
    except Exception: return (False, float(T), 0)
tot = {"go": [0, 0.0], "cs": [0, 0.0]}
print(f"{'level':28} {'go solved/time(s)':>22} {'cs solved/time(s)':>22}")
for f, i in specs:
    row = {}
    for who, cmd in (("go", f"'{go}' -strategy {strat} -procs 1 -t {T} '{f}' {i}"), ("cs", f"{cs} -strategy {strat} -t {T} '{f}' {i}")):
        rs = [run(cmd) for _ in range(reps)]
        n = sum(1 for r in rs if r[0]); med = statistics.median([r[1] for r in rs if r[0]]) if n else float("nan")
        row[who] = (n, med); tot[who][0] += n; tot[who][1] += sum(r[1] for r in rs)
    print(f"{os.path.basename(f)[:-4]+'#'+str(i):28} {row['go'][0]}/{reps} {row['go'][1]:8.2f}s        {row['cs'][0]}/{reps} {row['cs'][1]:8.2f}s", flush=True)
print(f"TOTAL {strat} T={T}: go solved {tot['go'][0]} ({tot['go'][1]:.0f}s)   cs solved {tot['cs'][0]} ({tot['cs'][1]:.0f}s)")
