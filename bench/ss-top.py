#!/usr/bin/env python3
"""Self/inclusive time per method from a speedscope JSON (dotnet-trace --format Speedscope). usage: ss-top.py file.json [N]"""
import json, sys, collections
d = json.load(open(sys.argv[1])); n = int(sys.argv[2]) if len(sys.argv) > 2 else 30
frames = [f["name"] for f in d["shared"]["frames"]]
self_ = collections.Counter(); incl = collections.Counter(); total = 0
for p in d["profiles"]:
    if p["type"] == "sampled":
        w = p.get("weights") or [1] * len(p["samples"])
        for st, wt in zip(p["samples"], w):
            st = [f for f in st if frames[f] not in ("UNMANAGED_CODE_TIME", "CPU_TIME")]
            if not st or not any("StartCallback" in frames[f] for f in st): continue  # only the solver thread
            total += wt; self_[st[-1]] += wt
            for fidx in set(st): incl[fidx] += wt
    else:  # evented
        stack = []; last = 0
        pseudo = {i for i, f in enumerate(frames) if f in ("UNMANAGED_CODE_TIME", "CPU_TIME")}
        solver = {i for i, f in enumerate(frames) if "StartCallback" in f}
        for e in p["events"]:
            t = e["at"]
            real = [f for f in stack if f not in pseudo]
            if real and any(f in solver for f in real):
                dt = t - last; total += dt; self_[real[-1]] += dt
                for fidx in set(real): incl[fidx] += dt
            last = t
            if e["type"] == "O": stack.append(e["frame"])
            else: stack.pop()
print(f"total {total}")
print(f"{'self%':>6} {'incl%':>6}  method")
for fidx, v in self_.most_common(n):
    print(f"{100*v/total:6.1f} {100*incl[fidx]/total:6.1f}  {frames[fidx][:110]}")
