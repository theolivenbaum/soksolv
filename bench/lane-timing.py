#!/usr/bin/env python3
"""Equal-work timing: Go vs C# for one lane at -nodes N on testdata levels. Prints the machine header first.
usage: bench/lane-timing.py <strategy> [-nodes N] [-reps R] [-cs-flags F]"""
import json, os, subprocess, sys, glob, time
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp/claude-0/-home-user-soksolv/880d39ab-73e1-5759-a28d-f73a31dbad1d/scratchpad")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv"); cs = os.environ.get("CS_BIN", f"{root}/artifacts/aot/Soksolv.Cli")
env = dict(os.environ, DOTNET_ROOT="/root/.dotnet")
a = sys.argv[1:]; strat = a.pop(0); nodes = 20000; reps = 2; csf = "-parity"
while a:
    x = a.pop(0)
    if x == "-nodes": nodes = int(a.pop(0))
    elif x == "-reps": reps = int(a.pop(0))
    elif x == "-cs-flags": csf = a.pop(0)
print(subprocess.run(f"'{cs}' -header -nodes 1 '{root}/testdata/Microban_155_31.xsb' 1 2>&1 >/dev/null", shell=True, capture_output=True, text=True, env=env).stdout)
def run(cmd):
    best = None
    for _ in range(reps):
        r = subprocess.run(f"nice -n 5 {cmd}", shell=True, capture_output=True, text=True, env=env)
        try: j = json.loads(r.stdout)
        except Exception: return None
        if best is None or j["time_ms"] < best["time_ms"]: best = j
    return best
tg = tc = 0; mg = mc = 0; n = 0
print(f"{'level':30} {'go ms':>8} {'cs ms':>8} {'cs/go':>6} {'go MB':>7} {'cs MB':>7}")
for f in sorted(glob.glob(f"{root}/testdata/*.xsb")):
    if "Microban_155_31" in f or "synth" in f: continue
    g = run(f"'{go}' -strategy {strat} -procs 1 -nodes {nodes} '{f}' 1"); c = run(f"'{cs}' -strategy {strat} -nodes {nodes} {csf} '{f}' 1")
    if not g or not c: continue
    n += 1; tg += g["time_ms"]; tc += c["time_ms"]; mg += g["peak_memory_mb"]; mc += c["peak_memory_mb"]
    print(f"{os.path.basename(f)[:-4]:30} {g['time_ms']:8.0f} {c['time_ms']:8.0f} {c['time_ms']/g['time_ms']:6.2f} {g['peak_memory_mb']:7.1f} {c['peak_memory_mb']:7.1f}")
print(f"TOTAL {strat} -nodes {nodes}: go {tg/1000:.1f}s  cs {tc/1000:.1f}s  ratio {tc/tg:.2f}   mean peak MB go {mg/n:.1f} cs {mc/n:.1f}")
