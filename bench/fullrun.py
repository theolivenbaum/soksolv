#!/usr/bin/env python3
"""Full-suite Go vs C# measurement (resumable). Enumerates every level of LTS + OTS, de-duplicates identical boards
(like .reference/benchmark: each distinct board is run once and counts for every level that has it), runs one solver
per board with a wall-clock limit, verifies every solution with the frozen Go verifier (cmd/verify), and appends one
TSV row per board.   usage:
  bench/fullrun.py <go|cs> -t SECONDS -out results-go.tsv [-limit N] [-shard K/M] [-go-procs P]
Always prints the machine header first (calibration + CPU/mem) and writes it as '# ' comment lines in the TSV."""
import os, sys, subprocess, time, json, glob, re, hashlib

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
scratch = os.environ.get("SCRATCH", "/tmp")
go_bin = os.environ.get("GO_SOKSOLV", f"{scratch}/soksolv")
chk_bin = os.environ.get("GO_CHECKSOL", f"{scratch}/checksol")
cs_bin = os.environ.get("CS_BIN", f"{root}/artifacts/aot/Soksolv.Cli")
env = dict(os.environ, PATH=os.environ["PATH"] + ":/root/.dotnet:/usr/local/go/bin", DOTNET_ROOT="/root/.dotnet")

a = sys.argv[1:]
who = a.pop(0); tl = 10.0; out = None; limit = 0; shard = (0, 1); gop = 4; extra = ""
while a:
    x = a.pop(0)
    if x == "-t": tl = float(a.pop(0))
    elif x == "-out": out = a.pop(0)
    elif x == "-limit": limit = int(a.pop(0))
    elif x == "-shard": k, m = a.pop(0).split("/"); shard = (int(k), int(m))
    elif x == "-go-procs": gop = int(a.pop(0))
    elif x == "-flags": extra = a.pop(0)
out = out or f"results-{who}.tsv"

def isrow(s): return '#' in s and all(c in ' -_@+$*.#' for c in s)
def isfloor(s): return any(c in '-_' for c in s) and all(c in ' -_' for c in s)
def split(data):
    lv = []; cur = None
    for l in data.split('\n'):
        l = l.rstrip('\r')
        if isrow(l): cur = (cur or []) + [l]
        elif cur is not None and isfloor(l): cur.append(l)
        elif cur is not None: lv.append(cur); cur = None
    if cur is not None: lv.append(cur)
    res = []
    for r in lv:
        while r and isfloor(r[-1]): r = r[:-1]
        res.append(r)
    return res

def levels():
    for suite, tag in (("Large Test Suite", "LTS"), ("Open Test Suite", "OTS")):
        for f in sorted(glob.glob(f"{root}/.reference/levels/{suite}/*.xsb")):
            for i, rows in enumerate(split(open(f, encoding="latin-1").read()), 1):
                yield tag, f, i, rows

def sh(c, **k): return subprocess.run(c, shell=True, capture_output=True, text=True, env=env, **k)

# machine header
hdr = sh(f"'{cs_bin}' -header -nodes 1 '{root}/testdata/Microban_155_31.xsb' 1 2>&1 >/dev/null").stdout
hdr += f"go: {sh(f'{go_bin} -h 2>&1 | head -0; /usr/local/go/bin/go version').stdout.strip()}\nsolver: {who}  limit: {tl}s  go-procs: {gop}  flags: {extra}\n"
print(hdr)
seen = {}
done = set()
if os.path.exists(out):
    for l in open(out):
        if not l.startswith("#") and not l.startswith("suite"): done.add(l.split("\t")[3])
f = open(out, "a")
if os.path.getsize(out) == 0 if os.path.exists(out) else True:
    for l in hdr.splitlines(): f.write("# " + l + "\n")
    f.write("suite\tfile\tlevel\tboard\tsolved\ttime_s\tmoves\tpushes\tpeak_mb\tsame_board_as\n")
n = 0; ok = 0
for tag, fn, i, rows in levels():
    key = hashlib.sha1("\n".join(r.rstrip() for r in rows).encode()).hexdigest()[:16]
    if key in seen: continue
    seen[key] = (tag, os.path.basename(fn), i)
    if hash(key) % shard[1] != shard[0] and shard[1] > 1: continue
    if key in done: continue
    n += 1
    if limit and n > limit: break
    cmd = f"'{go_bin}' -t {tl} -mem 12000 -procs {gop} {extra} '{fn}' {i}" if who == "go" else f"'{cs_bin}' -t {tl} -mem 12000 {extra} '{fn}' {i}"
    t0 = time.time()
    try: r = subprocess.run(cmd, shell=True, capture_output=True, text=True, env=env, timeout=tl + 30)
    except subprocess.TimeoutExpired: r = None
    el = time.time() - t0
    solved = False; moves = pushes = 0; peak = 0.0; ts = el
    if r:
        try:
            j = json.loads(r.stdout); sol = j.get("solution", "")
            ts = j.get("time_ms", el * 1000) / 1000
            if j.get("solved") and ts <= tl + 0.5 and re.search("[LURD]", sol):
                v = subprocess.run([chk_bin, fn, str(i)], input=sol, capture_output=True, text=True).stdout.strip()
                solved = v == "OK"
            moves, pushes, peak = j.get("moves", 0), j.get("pushes", 0), j.get("peak_memory_mb", 0)
        except Exception: pass
    ok += solved
    f.write(f"{tag}\t{os.path.basename(fn)}\t{i}\t{key}\t{'yes' if solved else 'no'}\t{ts:.3f}\t{moves}\t{pushes}\t{peak}\t\n"); f.flush()
    if n % 100 == 0: print(f"{n} boards, solved {ok}", flush=True)
print(f"done: {n} boards run, solved {ok}")
