#!/usr/bin/env python3
"""每个工具的失败率：tool/call 之后紧跟的 tool/result 是不是 isError。

为什么值得单独量：工具面"有没有"早就齐了（14 种缺口全是 1-2 次的长尾），
真正会伤人的是"有但用不对"——schema 写歪、描述误导、参数必填错。
corpus 里每次调用后面都跟着结果，失败率是可以算出来的。
"""
import importlib.util, os, json, collections

spec = importlib.util.spec_from_file_location("gap", "scripts/tool-gap.py")
gap = importlib.util.module_from_spec(spec); spec.loader.exec_module(gap)

import sys
root = sys.argv[1] if len(sys.argv) > 1 else "D:/DeepSeek Harness/home/sessions"
calls = collections.Counter()
errs = collections.Counter()


def scan(path):
    fobj, reader = gap.open_stream(path)
    try:
        raw = reader.read()
    finally:
        try: reader.close()
        except Exception: pass
        try: fobj.close()
        except Exception: pass
    pending = None
    for line in raw.split(b"\n"):
        line = line.strip()
        if not line.startswith(b"{"):
            continue
        try:
            ev = json.loads(line.decode("utf-8", "replace"))
        except Exception:
            continue
        if not isinstance(ev, dict):
            continue
        t = ev.get("type")
        if t == "tool/call":
            pending = gap.tool_name_of(ev)
            if pending:
                calls[pending] += 1
        elif t in ("tool/result", "tool/error") and pending:
            d = ev.get("data") or {}
            res = d.get("result") if isinstance(d.get("result"), dict) else d
            bad = bool(res.get("isError")) or bool(res.get("error")) or t == "tool/error"
            if bad:
                errs[pending] += 1
            pending = None


for dirpath, _, names in os.walk(root):
    for n in names:
        if n.endswith(".zstd"):
            try: scan(os.path.join(dirpath, n))
            except Exception: pass

rows = []
for name, c in calls.items():
    if c < 50:
        continue
    rows.append((errs[name] / c, name, c, errs[name]))
rows.sort(reverse=True)
print("失败率最高的 12 个（调用 >=50 次）：")
for r, name, c, e in rows[:12]:
    print(f"  {r*100:5.1f}%  {name:24s} {e:5d}/{c}")
print()
print("失败率最低的 5 个：")
for r, name, c, e in rows[-5:]:
    print(f"  {r*100:5.1f}%  {name:24s} {e:5d}/{c}")
tot_c = sum(calls.values()); tot_e = sum(errs.values())
print(f"\n总调用 {tot_c}，判为失败 {tot_e}（{tot_e/tot_c*100:.1f}%）")
