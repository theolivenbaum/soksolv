#!/usr/bin/env python3
"""Go portfolio (`soksolv -t T -procs 4`) vs the C# portfolio on a list of boards, one run at a time, both verified by the C# verifier.
usage: bench/portfolio-cmp.py <T> <list.tsv> [reps]    env: GO_SOKSOLV, CS_BIN (AOT binary or dll), SCRATCH, CS_EXTRA (flags), GO_EXTRA
Prints one row per board: solved/time for Go and C#, and a summary of Go-only / C#-only solves."""
import json, os, subprocess, sys
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv")
cs = os.environ.get("CS_BIN") or f"{root}/artifacts/aot/Soksolv.Cli"
cs = f"dotnet {cs}" if cs.endswith(".dll") else cs
T = sys.argv[1]; src = sys.argv[2]; reps = int(sys.argv[3]) if len(sys.argv) > 3 else 1
specs = [(l.split("\t")[0], int(l.split("\t")[1])) for l in open(src) if l.strip()]
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet", DOTNET_ROOT="/root/.dotnet")
def run(c):
    r = subprocess.run(c, shell=True, capture_output=True, text=True, env=env, timeout=float(T) * 3 + 60)
    try: j = json.loads(r.stdout); return (bool(j["solved"]), j["time_ms"] / 1000)
    except Exception: return (False, float(T))
gonly, csonly, both, gs, cc = [], [], 0, 0, 0
print(f"{'board':44} {'go':>12} {'cs':>12}")
for f, i in specs:
    g = [run(f"{go} -t {T} -procs 4 {os.environ.get('GO_EXTRA','')} '{f}' {i}") for _ in range(reps)]
    c = [run(f"{cs} -t {T} {os.environ.get('CS_EXTRA','')} '{f}' {i}") for _ in range(reps)]
    gn, cn = sum(x[0] for x in g), sum(x[0] for x in c)
    gt = min((x[1] for x in g if x[0]), default=float("nan")); ct = min((x[1] for x in c if x[0]), default=float("nan"))
    gs += gn; cc += cn
    name = os.path.basename(f)[:-4] + "#" + str(i)
    if gn and not cn: gonly.append(name)
    if cn and not gn: csonly.append(name)
    print(f"{name:44} {gn}/{reps} {gt:7.2f}s   {cn}/{reps} {ct:7.2f}s", flush=True)
print(f"TOTAL T={T} reps={reps}: go solved {gs}, cs solved {cc}; Go-only {len(gonly)}: {gonly}; C#-only {len(csonly)}: {csonly}")
