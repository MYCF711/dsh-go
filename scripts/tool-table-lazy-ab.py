#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tool-table-lazy-ab.py —— compact vs lazy 的真实 A/B。

要回答的问题只有一个：**模型会不会去拉工具？**
藏起 27 个工具能省表，但如果模型不知道/不愿调 tool_load，那被藏起来的工具
就等于没有——`merged` 那次翻车就是同一种失败（工具在那儿，模型不照做）。

判据（每个任务、每种形态各跑一遍，全部机器判定，不靠人看）：
  1. 目标工具被调用了吗
  2. 那次调用成功了吗（isError=false）
  3. 撞满步数了吗（"用满了 N 步仍未收敛"）
  4. 可选的答案正则命中了吗
lazy 侧额外记录：调没调 tool_load、拉的名字对不对。

用法：
    python scripts/tool-table-lazy-ab.py                 # 11 任务 × 2 形态
    python scripts/tool-table-lazy-ab.py --runs 2        # 每个组合重复 2 次（看抖动）
    python scripts/tool-table-lazy-ab.py --only web_search_now_line
    python scripts/tool-table-lazy-ab.py --max-steps 7

前置：go build -o "tmp/DSH GO.exe" ./cmd/dshd（脚本会自己检查）
"""

import argparse
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join(ROOT, "tmp", "DSH GO.exe")
MODEL = ""   # 由 --model 填；空 = 用 dshd 的默认模型
RULES = os.path.join(ROOT, "tmp", "ab-rules.txt")
MODES = ("compact", "lazy")

# 任务集：7 个必须用**表外工具**（hidden=True），4 个用常驻工具（对照组）。
# 表外名单来自 scripts/tool-table-hot.py 的重算结果——注意 web_search / github /
# subagent / send_message 都在常驻 30 名里，它们测不出 lazy。
TASKS = [
    # --- 表外（必须知道去 tool_load） ---
    dict(name="now_line", hidden=True, want="now_line",
         prompt="用 now_line 告诉我现在是几点、几号、星期几。",
         expect=r"\d{1,2}[:：]\d{2}"),
    dict(name="job_list", hidden=True, want="job_list",
         prompt="用 job_list 看看现在有哪些后台任务，把结果告诉我。",
         expect=None),
    dict(name="schedule_list", hidden=True, want="schedule_list",
         prompt="用 schedule_list 列出本会话的定时任务。",
         expect=None),
    dict(name="team_task_list", hidden=True, want="team_task_list",
         prompt="用 team_task_list 列出任务看板里现在有什么。",
         expect=None),
    dict(name="watch_status", hidden=True, want="watch_status",
         prompt="用 watch_status 看现在在盯哪些仓库。",
         expect=None),
    dict(name="repo_here", hidden=True, want="repo_here",
         prompt="用 repo_here 报告当前目录属于哪个 GitHub 仓库、在哪个分支。",
         expect=None),
    dict(name="working_directory", hidden=True, want="working_directory",
         prompt="用 working_directory 告诉我当前工作目录是什么。",
         expect=None),
    # --- 常驻（对照组：lazy 不该把普通任务弄坏） ---
    dict(name="bash", hidden=False, want="bash",
         prompt="用 bash 跑一条命令：echo lazy-ab-ok，把输出原样告诉我。",
         expect=r"lazy-ab-ok"),
    dict(name="read", hidden=False, want="read",
         prompt="用 read 看 README.md 的前 5 行，把内容告诉我。",
         expect=None),
    dict(name="todo_write", hidden=False, want="todo_write",
         prompt="用 todo_write 记一条待办：跑 lazy A/B。",
         expect=None),
    dict(name="glob", hidden=False, want="glob",
         prompt="用 glob 找 cmd/dshd 下所有 *_test.go，告诉我找到几个。",
         expect=r"\d"),
    # --- 表外，但提示词**不点名工具**：这一组才真正问"模型会不会去拉" ---
    # 点名的那 7 个任务证明的是"机制通了"；这一组证明的是"模型自己意识到缺工具"。
    # 它可能绕过（比如用常驻的 bash 跑 date 拿时间），那也是一种真实结果，
    # 所以答案对不对（answer_ok）和工具选得对不对（want_called）分开记。
    dict(name="nameless_now_line", hidden=True, want="now_line",
         prompt="现在几点了？几号？星期几？",
         expect=r"\d{1,2}[:：]\d{2}"),
    dict(name="nameless_job_list", hidden=True, want="job_list",
         prompt="有没有还在跑的后台任务？",
         expect=None),
    dict(name="nameless_schedule_list", hidden=True, want="schedule_list",
         prompt="我这个会话里设了哪些定时任务？",
         expect=None),
    dict(name="nameless_history_search", hidden=True, want="history_search",
         prompt="在会话原始日志里搜一下「工具表」，有命中吗？",
         expect=None),
    # 一次要用两个表外工具：考的是"一次取多个"这条路（tool_load 支持 names 数组）。
    dict(name="two_hidden_tools", hidden=True, want="schedule_list",
         prompt="我这个会话里设了哪些定时任务？另外，有没有还在跑的后台任务？",
         expect=None),
    # 目录里只有一句话的工具（present）：名字不像"声明交付物"，靠一句话认出来。
    dict(name="nameless_present", hidden=True, want="present",
         prompt="把 README.md 作为这次交付的文件展示出来。",
         expect=None),
]


def setup_stdout():
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass


# ---------------------------------------------------------------- 读会话日志

def read_events(path):
    """解一个多帧 zstd 会话文件，返回事件列表。"""
    import zstandard as zstd

    dctx = zstd.ZstdDecompressor()
    data = open(path, "rb").read()
    raw = b""
    while data:
        obj = dctx.decompressobj()
        raw += obj.decompress(data)
        data = obj.unused_data
    out = []
    for line in raw.decode("utf-8", "replace").split("\n"):
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            out.append(json.loads(line))
        except Exception:
            pass
    return out


def blocks_text(blocks):
    return "".join(b.get("text", "") for b in (blocks or []) if isinstance(b, dict))


def analyze(events):
    calls, results, steps, ptokens = [], {}, 0, []
    final = ""
    for ev in events:
        t, d = ev.get("type"), ev.get("data") or {}
        if t == "tool/call":
            calls.append({"step": d.get("step"), "name": d.get("name"),
                          "args": d.get("arguments"), "callId": d.get("callId")})
        elif t == "tool/result":
            m = d.get("message") or {}
            results[m.get("toolCallId") or f"{d.get('step')}"] = bool(m.get("isError"))
        elif t == "assistant/message":
            steps += 1
            u = d.get("usage") or {}
            if u.get("inputTokens") is not None:
                ptokens.append(int(u["inputTokens"]))
            txt = blocks_text((d.get("message") or {}).get("content"))
            if txt.strip():
                final = txt
    return {"calls": calls, "results": results, "steps": steps,
            "prompt_tokens": ptokens, "final": final}


# ---------------------------------------------------------------- 跑一轮

def run_one(mode, task, max_steps, timeout):
    home = tempfile.mkdtemp(prefix=f"lazyab-{mode}-")
    args = [EXE, "run", "--tools-mode", mode, "--home", home, "--policy", "allow",
            "--max-steps", str(max_steps), "--workspace-bytes", "1",
            "--agents", "tmp/ab-rules.txt", "--prompt", task["prompt"]]
    if MODEL:
        args += ["--model", MODEL]
    t0 = time.time()
    try:
        p = subprocess.run(args, capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=timeout, cwd=ROOT)
    except subprocess.TimeoutExpired:
        shutil.rmtree(home, ignore_errors=True)
        return {"mode": mode, "task": task["name"], "timeout": True, "good": False,
                "dur": timeout, "err": "timeout"}
    dur = time.time() - t0
    out = (p.stdout or "") + (p.stderr or "")
    # 会话日志：优先用 stderr 里那一行给的路径（不猜），找不到才退回去找最新的。
    log = None
    m = re.search(r"会话：(.+?)（\d+ 事件）", out)
    if m and os.path.exists(m.group(1)):
        log = m.group(1)
    else:
        found = glob.glob(os.path.join(home, "**", "session.v4.jsonl.zstd"), recursive=True)
        if found:
            log = max(found, key=os.path.getmtime)
    a = analyze(read_events(log)) if log else {"calls": [], "results": {}, "steps": 0,
                                              "prompt_tokens": [], "final": ""}
    shutil.rmtree(home, ignore_errors=True)

    stats = re.search(r"stats: tool_calls=(\d+) input=(\d+) output=(\d+)", out)
    cramped = "用满了" in out
    called = [c["name"] for c in a["calls"]]
    want_calls = [c for c in a["calls"] if c["name"] == task["want"]]
    want_ok = any(not a["results"].get(c["callId"], True) for c in want_calls)
    loads = [c for c in a["calls"] if c["name"] == "tool_load"]
    load_names = []
    for c in loads:
        try:
            raw = json.loads(c["args"] or "{}")
        except Exception:
            raw = {}
        for k in ("names", "tools", "name", "tool", "tool_name"):
            v = raw.get(k)
            if isinstance(v, str):
                load_names.append(v)
            elif isinstance(v, list):
                load_names += [x for x in v if isinstance(x, str)]
    expect_ok = True if not task.get("expect") else bool(re.search(task["expect"], a["final"] or ""))
    good = bool(want_calls) and want_ok and not cramped and expect_ok
    return {
        "mode": mode, "task": task["name"], "hidden": task["hidden"], "want": task["want"],
        "called": called, "want_called": bool(want_calls), "want_ok": want_ok,
        "cramped": cramped, "steps": a["steps"], "prompt_tokens": a["prompt_tokens"],
        "input": int(stats.group(2)) if stats else None,
        "output": int(stats.group(3)) if stats else None,
        "loads": len(loads), "load_names": load_names,
        "load_correct": task["want"] in load_names,
        "answer_ok": expect_ok,
        "good": good, "dur": round(dur, 1),
        "final": (a["final"] or "").strip().replace("\n", " ")[:160],
        "exit": p.returncode,
    }


def main():
    setup_stdout()
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--max-steps", type=int, default=6)
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--out", default="tmp/tool-table-lazy-ab.json")
    ap.add_argument("--md", default="", help="把结果写成 markdown 表（贴进文档用）")
    ap.add_argument("--md-from", default="", help="不跑模型：把已有 --out json 重新渲染成 markdown")
    ap.add_argument("--only", default="", help="只跑这些任务名（逗号分隔）")
    ap.add_argument("--model", default="", help="换一个模型跑（默认用 dshd 的默认模型）；换模型必须重跑，不能外推")
    a = ap.parse_args()

    if a.md_from:
        with open(os.path.join(ROOT, a.md_from), encoding="utf-8") as f:
            rows = json.load(f)["rows"]

        def agg(mode, hidden=None, _rows=rows):
            sel = [r for r in _rows if r["mode"] == mode and (hidden is None or r["hidden"] == hidden)]
            if not sel:
                return None
            firsts = [r["prompt_tokens"][0] for r in sel if r.get("prompt_tokens")]
            return {"n": len(sel), "good": sum(1 for r in sel if r.get("good")),
                    "first_tokens_mean": round(sum(firsts) / len(firsts), 1) if firsts else None,
                    "steps_mean": round(sum(r.get("steps", 0) for r in sel) / len(sel), 2),
                    "loads": sum(r.get("loads", 0) for r in sel),
                    "load_correct": sum(1 for r in sel if r.get("load_correct"))}

        write_md(os.path.join(ROOT, a.md), rows, agg, a.runs, a.max_steps)
        print(f"表格：{a.md}")
        return

    global MODEL
    MODEL = a.model
    if not os.path.exists(EXE):
        sys.exit('先 go build -o "tmp/DSH GO.exe" ./cmd/dshd')
    if not os.path.exists(RULES):
        with open(RULES, "w", encoding="utf-8") as f:
            f.write("A/B 用的最小规则文件：屏蔽工作区 AGENTS.md 对 prompt_tokens 的干扰。\n")

    tasks = TASKS
    if a.only:
        want = {x.strip() for x in a.only.split(",") if x.strip()}
        tasks = [t for t in TASKS if t["name"] in want]
        if not tasks:
            sys.exit("--only 没匹配到任务")

    rows = []
    for task in tasks:
        for mode in MODES:
            for i in range(a.runs):
                r = run_one(mode, task, a.max_steps, a.timeout)
                rows.append(r)
                mark = "✓" if r.get("good") else "✗"
                pt = (r.get("prompt_tokens") or [None])[0]
                print(f"{mark} {task['name']:24s} {mode:7s} "
                      f"{'表外' if task['hidden'] else '常驻'} "
                      f"步={r.get('steps')} 首步token={pt} 总input={r.get('input')} "
                      f"tool_load={r.get('loads')}"
                      f"{'(对)' if r.get('load_correct') else ''}"
                      f"{' 撞满步数' if r.get('cramped') else ''}"
                      f"{'' if r.get('want_ok') else ' 目标工具没好'}"
                      f"{'' if r.get('answer_ok', True) else ' 答案没命中'}"
                      f" 用了={','.join(r.get('called') or []) or '-'}"
                      f" {r.get('dur')}s", flush=True)

    def agg(mode, hidden=None):
        sel = [r for r in rows if r["mode"] == mode and (hidden is None or r["hidden"] == hidden)]
        if not sel:
            return None
        firsts = [r["prompt_tokens"][0] for r in sel if r.get("prompt_tokens")]
        return {
            "n": len(sel),
            "good": sum(1 for r in sel if r.get("good")),
            "first_tokens_mean": round(sum(firsts) / len(firsts), 1) if firsts else None,
            "steps_mean": round(sum(r.get("steps", 0) for r in sel) / len(sel), 2),
            "loads": sum(r.get("loads", 0) for r in sel),
            "load_correct": sum(1 for r in sel if r.get("load_correct")),
        }

    print("\n== 汇总 ==")
    for mode in MODES:
        for label, hid in (("全部", None), ("表外任务", True), ("常驻任务", False)):
            s = agg(mode, hid)
            if s:
                print(f"{mode:7s} {label:6s} 正确 {s['good']}/{s['n']}  "
                      f"首步token均值 {s['first_tokens_mean']}  步数均值 {s['steps_mean']}  "
                      f"tool_load {s['loads']}（拉对 {s['load_correct']}）")
    c, l = agg("compact"), agg("lazy")
    if c and l and c["first_tokens_mean"] and l["first_tokens_mean"]:
        print(f"\n首步 prompt_tokens：compact {c['first_tokens_mean']} → lazy {l['first_tokens_mean']}"
              f"（{100*(l['first_tokens_mean']-c['first_tokens_mean'])/c['first_tokens_mean']:+.1f}%）")
    if c and l:
        print(f"成功率：compact {c['good']}/{c['n']} → lazy {l['good']}/{l['n']}")
        hc, hl = agg("compact", True), agg("lazy", True)
        if hc and hl:
            print(f"表外任务成功率：compact {hc['good']}/{hc['n']} → lazy {hl['good']}/{hl['n']}"
                  f"（lazy 里 tool_load 共 {hl['loads']} 次，拉对 {hl['load_correct']} 次）")

    with open(os.path.join(ROOT, a.out), "w", encoding="utf-8") as f:
        json.dump({"rows": rows, "compact": c, "lazy": l}, f, ensure_ascii=False, indent=2)
    print(f"\n明细：{a.out}")
    if a.md:
        write_md(os.path.join(ROOT, a.md), rows, agg, a.runs, a.max_steps)
        print(f"表格：{a.md}")


def write_md(path, rows, agg, runs, max_steps):
    """把这一轮的结果写成 markdown —— 文档里的表就贴它，避免手抄出错。"""
    by_task = {}
    for r in rows:
        key = r["task"]
        by_task.setdefault(key, {"hidden": r.get("hidden", False), "want": r.get("want"),
                                 "compact": [], "lazy": []})
        by_task[key][r["mode"]].append(r)

    def cell(rs, field, default="-"):
        vals = [r.get(field) for r in rs if r.get(field) is not None]
        return ", ".join(str(v) for v in vals) if vals else default

    lines = []
    lines.append(f"<!-- 由 scripts/tool-table-lazy-ab.py --md 生成；每组合 {runs} 次，"
                 f"max-steps={max_steps} -->\n")
    lines.append("| 任务 | 用哪类工具 | 目标工具 | 形态 | 知道去拉？ | 拉对？ | 跑通？ | "
                 "步数 | 每步 prompt_tokens | 总 input | 实际调用序列 |")
    lines.append("|---|---|---|---|---|---|---|---:|---|---:|---|")
    for name, d in by_task.items():
        for mode in MODES:
            rs = d[mode]
            if not rs:
                continue
            loads = sum(r.get("loads", 0) for r in rs)
            correct = sum(1 for r in rs if r.get("load_correct"))
            good = sum(1 for r in rs if r.get("good"))
            steps = ["/".join(str(x) for x in (r.get("prompt_tokens") or [])) or "-" for r in rs]
            inputs = [r.get("input") for r in rs if r.get("input")]
            lines.append(
                f"| {name} | {'表外' if d['hidden'] else '常驻'} | `{d['want']}` | {mode} | "
                f"{('是（%d/%d）' % (loads, len(rs))) if loads else '—'} | "
                f"{('%d/%d' % (correct, len(rs))) if loads else '—'} | "
                f"{good}/{len(rs)} | {cell(rs, 'steps')} | "
                f"{'; '.join(steps)} | "
                f"{round(sum(inputs)/len(inputs)) if inputs else '-'} | "
                f"{'; '.join(','.join(r.get('called') or []) or '-' for r in rs)} |")
    lines.append("\n汇总：\n")
    lines.append("| 形态 | 分组 | 正确 | 首步 tokens 均值 | 步数均值 | tool_load 次数 |")
    lines.append("|---|---|---:|---:|---:|---:|")
    for mode in MODES:
        for label, hid in (("全部", None), ("表外任务", True), ("常驻任务", False)):
            s = agg(mode, hid)
            if s:
                lines.append(f"| {mode} | {label} | {s['good']}/{s['n']} | "
                             f"{s['first_tokens_mean']} | {s['steps_mean']} | {s['loads']} |")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
