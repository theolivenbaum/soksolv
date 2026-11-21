#!/usr/bin/env python3
"""Equal-work CPU-time comparison (user+sys of the child, best of R) Go vs C# for one lane, `-nodes N`, on testdata or a level list.
usage: bench/lane-cpu.py <strategy> <nodes> <reps> <testdata|list.tsv> ["go flags"] ["cs flags"]
list.tsv lines: <path relative to the repo root or absolute>\t<level index>
env: GO_SOKSOLV (default $SCRATCH/soksolv), CS_BIN (default artifacts/aot/Soksolv.Cli)
Prints per level the Go / C# CPU seconds, the ratio and the C# alloc_mb, then the totals. A node-count mismatch prints NODEDIFF."""
import glob, json, os, resource, subprocess, sys

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
scratch = os.environ.get("SCRATCH", "/tmp")
go = os.environ.get("GO_SOKSOLV", f"{scratch}/soksolv")
cs = os.environ.get("CS_BIN", f"{root}/artifacts/aot/Soksolv.Cli")
strat, nodes, reps, src = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
go_flags = sys.argv[5].split() if len(sys.argv) > 5 else []
cs_flags = sys.argv[6].split() if len(sys.argv) > 6 else []
env = dict(os.environ, DOTNET_ROOT=os.environ.get("DOTNET_ROOT", "/root/.dotnet"))


def cpu(argv):
    b = resource.getrusage(resource.RUSAGE_CHILDREN)
    r = subprocess.run(["nice", "-n", "10"] + argv, capture_output=True, text=True, env=env)
    a = resource.getrusage(resource.RUSAGE_CHILDREN)
    try:
        j = json.loads(r.stdout)
    except Exception:
        j = None
    return a.ru_utime - b.ru_utime + a.ru_stime - b.ru_stime, j


if src == "testdata":
    specs = [(f, 1) for f in sorted(glob.glob(f"{root}/testdata/*.xsb")) if "synth" not in f and "Microban_155_31" not in f]
else:
    specs = []
    for l in open(src):
        if l.strip():
            p, i = l.rstrip("\n").split("\t")[:2]
            specs.append((p if os.path.isabs(p) else f"{root}/{p}", int(i)))
tg = tc = 0.0
n = 0
print(f"{'level':36} {'go s':>7} {'cs s':>7} {'cs/go':>6} {'alloc MB':>8} {'nodes':>8}")
for f, i in specs:
    gr = [cpu([go, "-strategy", strat, "-procs", "1", "-nodes", nodes] + go_flags + [f, str(i)]) for _ in range(reps)]
    cr = [cpu([cs, "-strategy", strat, "-parity", "-nodes", nodes] + cs_flags + [f, str(i)]) for _ in range(reps)]
    g, c = min(x[0] for x in gr), min(x[0] for x in cr)
    gj, cj = gr[0][1], cr[0][1]
    if not gj or not cj:
        continue
    if gj["nodes_expanded"] != cj["nodes_expanded"]:
        print("NODEDIFF", f, i, gj["nodes_expanded"], cj["nodes_expanded"])
    n += 1
    tg += g
    tc += c
    print(f"{os.path.basename(f)[:-4] + '#' + str(i):36} {g:7.3f} {c:7.3f} {c / max(g, 1e-9):6.2f} {cj.get('alloc_mb', 0):8.1f} {cj['nodes_expanded']:8d}")
print(f"TOTAL {strat} -nodes {nodes} n={n}: go {tg:.2f}s  cs {tc:.2f}s  ratio {tc / max(tg, 1e-9):.3f}")
