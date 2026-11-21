#!/usr/bin/env python3
"""Equal-work CPU-time comparison Go vs C# for one lane on a list of levels (robust to a loaded machine: uses child
user+sys CPU time from wait4 rusage, not wall time). Machine header first.
usage: bench/wide-timing.py <strategy> <list.tsv: file<TAB>index<TAB>nodes> [-reps R] [-cs BIN] [-csflags F]"""
import json, os, subprocess, sys, resource, time
root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
S = os.environ.get("SCRATCH", "/tmp/claude-0/-home-user-soksolv/880d39ab-73e1-5759-a28d-f73a31dbad1d/scratchpad")
go = os.environ.get("GO_SOKSOLV", f"{S}/soksolv")
env = dict(os.environ, DOTNET_ROOT="/root/.dotnet")
a = sys.argv[1:]; strat = a.pop(0); lst = a.pop(0); reps = 2; cs = f"{root}/artifacts/aot-wide/Soksolv.Cli"; csf = "-parity"
while a:
    x = a.pop(0)
    if x == "-reps": reps = int(a.pop(0))
    elif x == "-cs": cs = a.pop(0)
    elif x == "-csflags": csf = a.pop(0)
print(subprocess.run(f"'{cs}' -header -nodes 1 '{root}/testdata/Microban_155_31.xsb' 1 2>&1 >/dev/null", shell=True, capture_output=True, text=True, env=env).stdout)
print(open("/proc/loadavg").read().strip(), "(load; CPU-time metric used)")
def run(argv):
    best = None
    for _ in range(reps):
        before = resource.getrusage(resource.RUSAGE_CHILDREN)
        r = subprocess.run(argv, capture_output=True, text=True, env=env)
        after = resource.getrusage(resource.RUSAGE_CHILDREN)
        cpu = (after.ru_utime - before.ru_utime) + (after.ru_stime - before.ru_stime)
        try: j = json.loads(r.stdout)
        except Exception: return None
        j["cpu"] = cpu
        if best is None or cpu < best["cpu"]: best = j
    return best
tg = tc = 0; n = 0; bad = 0
print(f"{'level':34} {'nodes':>7} {'go cpu':>7} {'cs cpu':>7} {'cs/go':>6}  parity")
for l in open(lst):
    if not l.strip() or l.startswith("#"): continue
    f, i, nodes = l.rstrip("\n").split("\t")
    g = run([go, "-strategy", strat, "-procs", "1", "-nodes", nodes, "-t", "300", f, i])
    c = run([cs, "-strategy", strat, "-nodes", nodes, "-t", "300", *csf.split(), f, i])
    if not g or not c: print(f"{os.path.basename(f)}#{i}: run failed"); continue
    same = (g["solved"], g["nodes_expanded"], g["pushes"]) == (c["solved"], c["nodes_expanded"], c["pushes"])
    bad += not same; n += 1; tg += g["cpu"]; tc += c["cpu"]
    print(f"{os.path.basename(f)[:-4] + '#' + i:34} {nodes:>7} {g['cpu']:7.2f} {c['cpu']:7.2f} {c['cpu']/g['cpu']:6.2f}  {'ok' if same else 'DIFF ' + str((g['nodes_expanded'], c['nodes_expanded']))}", flush=True)
print(f"TOTAL {strat}: go {tg:.1f}s cpu  cs {tc:.1f}s cpu  ratio {tc/tg:.2f}   parity diffs {bad}/{n}")
