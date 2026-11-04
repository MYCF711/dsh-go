#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""工具表 A/B：旧 compact vs 新 compact（去掉冗余 schema 骨架之后）。

为什么要这个脚本：新投影在字节上一定更小（那是构造出来的），但"更小"不是理由——
**模型还选不选得对、参数还填不填得对**才是。这里用真网关跑同一批任务，
两个**二进制**各跑一遍（旧 compact 只存在于旧二进制里，所以按 exe 分档，
而不是按 flag 分档），逐条记：选了哪个工具、跑没跑通、prompt_tokens 多少。

判定（全部机器可判）：
  1. 关键工具必须真的被调用
  2. 关键工具必须成功（参数错后自己补对算"自纠"，单独记一笔，不算严格通过）
  3. 没撞满步数（撞满说明它在原地打转）
另有"多余调用"：不在任务允许集合里的工具（例如去 bash 里翻文件）。

prompt_tokens 取的是**服务端 usage**：会话日志里每条 assistant/message 的
data.usage.inputTokens（就是 usage.prompt_tokens），按步求和。不是估算器。
两种表的差别只在工具表，所以第一笔 inputTokens 就是"这张表贵多少"的最干净口径。

用法：
  python scripts/tool-table-tight-ab.py                    # 12 个任务 × 2 个二进制
  python scripts/tool-table-tight-ab.py --runs 2           # 每格采样 2 次（本轮用这个跑了两轮）
  python scripts/tool-table-tight-ab.py --only new
  python scripts/tool-table-tight-ab.py --jobs 6           # 并发（每个 run 一个临时 home，互不干扰）

两个二进制怎么来（**旧档必须来自收紧之前**，否则 A/B 就是自己比自己）：

  git worktree add /tmp/dshd-base <收紧前的 rev>          # 见 docs/tool-table-budget.md 的日期
  (cd /tmp/dshd-base && go build -o "$OLDPWD/tmp/dshd-baseline.exe" ./cmd/dshd)
  go build -o tmp/dshd-tight.exe ./cmd/dshd               # 当前树 = 新档
"""
import argparse
import concurrent.futures
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE_OLD = os.path.join(ROOT, "tmp", "dshd-baseline.exe")
EXE_NEW = os.path.join(ROOT, "tmp", "dshd-tight.exe")
BASE = os.environ.get("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1")
MODEL = os.environ.get("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017")

# 每个任务：(名字, 提示词, 允许出现的工具集合, 必须出现且成功的工具, 说明)
# 提示词分两类：一类点名工具（现实里 AGENTS/文档会点名），一类只说要做的事。
TASKS = [
    ("看板", "看看任务看板现在有哪些任务。",
     {"team_task_list", "team_task_get"}, "team_task_list", "不点名工具"),
    ("建任务", "用 team_task_create 建一条任务：subject「修 CI」，description「红灯」。",
     {"team_task_create", "team_task_get"}, "team_task_create", "description 是必填参数"),
    ("建并认领", "先建一条任务（subject「A」，description「B」），然后认领它，owner 写「me」。",
     {"team_task_create", "team_task_update", "team_task_list", "team_task_get"},
     "team_task_update", "两步：create → update(claim)"),
    ("任务详情", "建一条任务（subject「X」，description「Y」），然后用 team_task_get 按 id 读它。",
     {"team_task_create", "team_task_get"}, "team_task_get", ""),
    ("列定时", "列出本会话的定时任务。",
     {"schedule_list"}, "schedule_list", "不点名工具"),
    ("建定时", "用 schedule_create 建一个每 600 秒跑一次的定时任务，标题「体检」，prompt「检查健康」。",
     {"schedule_create", "schedule_list"}, "schedule_create", ""),
    ("删定时", "先建一个每天 09:30 的定时任务（标题「晨检」，prompt「跑一遍」），再按 id 删掉它。",
     {"schedule_create", "schedule_delete", "schedule_list"}, "schedule_delete",
     "两步：create → delete"),
    ("看盯仓", "现在盯了哪些仓库？",
     {"watch_status"}, "watch_status", "不点名工具"),
    ("盯仓库", "用 watch_start 盯 octocat/Hello-World 这个仓库。",
     {"watch_start", "watch_doctor"}, "watch_start", ""),
    ("搜记忆", "用 hindsight_search_knowledge_pages 搜一下「工具表」相关的记忆。",
     {"hindsight_search_knowledge_pages", "hindsight_list_knowledge_pages"},
     "hindsight_search_knowledge_pages", ""),
    ("列记忆", "本机存了哪些记忆？列一下。",
     {"hindsight_list_knowledge_pages"}, "hindsight_list_knowledge_pages", "不点名工具"),
    ("存记忆", "用 hindsight_ingest_document 把「A/B 结论：收紧骨架省 6% 输入」存进本机记忆，标题「tight-ab」。",
     {"hindsight_ingest_document", "hindsight_read_knowledge_page"},
     "hindsight_ingest_document", ""),
]

STATS = re.compile(r"stats: tool_calls=(\d+) input=(\d+) output=(\d+)")
CALL = re.compile(r"→ (\w+)\s*(.*)")
RESULT = re.compile(r"← (\w+) \[(ok|ERR)\](.*)")


def prompt_tokens(home):
    """从会话日志里读服务端 usage（= usage.prompt_tokens），逐步返回。"""
    files = glob.glob(os.path.join(home, "sessions", "**", "session.v4.jsonl.zstd"), recursive=True)
    if not files:
        return None, []
    try:
        import zstandard
    except ImportError:
        return None, []
    per_step = []
    try:
        with open(files[0], "rb") as f:
            raw = zstandard.ZstdDecompressor().stream_reader(f).read()
    except Exception:
        return None, []
    for line in raw.decode("utf-8", "replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        if ev.get("type") != "assistant/message":
            continue
        usage = (ev.get("data") or {}).get("usage") or {}
        if "inputTokens" in usage:
            per_step.append(int(usage["inputTokens"]))
    return (sum(per_step) if per_step else None), per_step


def run_one(exe, prompt):
    home = tempfile.mkdtemp(prefix="ab-tight-")
    args = [exe, "run", "--home", home, "--policy", "allow", "--workspace-bytes", "0",
            "--max-steps", "6", "--model", MODEL, "--base", BASE,
            "--tools-mode", "compact", "--prompt", prompt]
    try:
        p = subprocess.run(args, capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=300, cwd=ROOT)
    except subprocess.TimeoutExpired:
        shutil.rmtree(home, ignore_errors=True)
        return {"calls": [], "errs": ["超时"], "n_err": 1, "stats": (0, 0, 0),
                "prompt_tokens": None, "steps": [], "cramped": False, "exit": "timeout",
                "results": []}
    out = (p.stdout or "") + (p.stderr or "")
    calls, errs, results = [], [], []
    for line in out.splitlines():
        s = line.strip()
        if s.startswith("→ "):
            cm = CALL.match(s)
            if cm:
                calls.append((cm.group(1), cm.group(2)))
        elif s.startswith("← "):
            rm = RESULT.match(s)
            if rm:
                ok = rm.group(2) == "ok"
                results.append((rm.group(1), ok, rm.group(3).strip()))
                if not ok:
                    errs.append(rm.group(3).strip() or "(无说明)")
    st = STATS.search(out)
    stats = tuple(int(x) for x in st.groups()) if st else (0, 0, 0)
    total, steps = prompt_tokens(home)
    shutil.rmtree(home, ignore_errors=True)
    return {"calls": calls, "errs": errs, "n_err": len(errs), "stats": stats,
            "prompt_tokens": total, "steps": steps, "results": results,
            "cramped": ("用满了" in out or "仍未收敛" in out),
            "exit": p.returncode}


def judge(allowed, must, r):
    """返回 (ok, reason, strict)。strict = 一次过：关键工具成功、无 ERR、无多余调用。"""
    names = [n for n, _ in r["calls"]]
    if not r["calls"]:
        return False, "没调工具（run 退出码 %s）" % r["exit"], False
    if r["cramped"]:
        return False, "撞满步数（原地打转）", False
    if must not in names:
        return False, "没调关键工具 %s（调了 %s）" % (must, ", ".join(names)), False
    # 结果行按调用顺序回来，名字就是模型看到的那个。
    aligned = []
    for i, (rname, ok, _) in enumerate(r["results"]):
        cname = names[i] if i < len(names) else rname
        aligned.append((cname, ok))
    key_ok = any(name == must and ok for name, ok in aligned)
    if not key_ok:
        return False, "关键工具没跑通：" + ("；".join(r["errs"])[:160] or "无结果"), False
    extra = [n for n in names if n not in allowed]
    if r["n_err"]:
        return True, "自纠（%d 次 ERR：%s）" % (r["n_err"], "；".join(r["errs"])[:120]), False
    if extra:
        return True, "多调：%s" % ", ".join(sorted(set(extra))), False
    return True, "", True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=1, help="每格重复几次")
    ap.add_argument("--only", choices=["old", "new"], default=None)
    ap.add_argument("--jobs", type=int, default=4, help="并发跑几个（网关在本机，4 个够）")
    ap.add_argument("--out", default="tmp/tool-table-tight-ab.json")
    a = ap.parse_args()
    for exe in (EXE_OLD, EXE_NEW):
        if not os.path.exists(exe):
            sys.exit("缺二进制 %s（旧档：go build -o tmp/dshd-baseline.exe ./cmd/dshd；"
                     "新档：go build -o tmp/dshd-tight.exe ./cmd/dshd）" % exe)

    modes = [a.only] if a.only else ["old", "new"]
    exes = {"old": EXE_OLD, "new": EXE_NEW}

    # 串行跑 24 次要 40 分钟（每次约 1m50s，模型带推理），并发 4 个约 12 分钟。
    # 每个 run 用独立临时 home，互不干扰。
    plan = []
    for ti, (name, prompt, allowed, must, note) in enumerate(TASKS):
        for mi, mode in enumerate(modes):
            for r in range(a.runs):
                plan.append((ti, mi, r, name, prompt, allowed, must, note, mode))

    rows = [None] * len(plan)
    lock = threading.Lock()

    def work(item):
        idx, ti, mi, r, name, prompt, allowed, must, note, mode = item
        res = run_one(exes[mode], prompt)
        ok, reason, strict = judge(allowed, must, res)
        row = {
            "task": name, "mode": mode, "ok": ok, "strict": strict, "reason": reason,
            "note": note, "picked": [n for n, _ in res["calls"]],
            "raw_calls": [[n, x] for n, x in res["calls"]],
            "results": [[n, "ok" if o else "ERR", t] for n, o, t in res["results"]],
            "n_err": res["n_err"], "errs": res["errs"],
            "prompt_tokens": res["prompt_tokens"], "steps": res["steps"],
            "stats_input": res["stats"][1], "cramped": res["cramped"],
            "exit": res["exit"],
        }
        rows[idx] = row
        step1 = res["steps"][0] if res["steps"] else None
        with lock:
            print("%s %-6s %-4s step1=%-6s picked=%-34s %s" % (
                "✓" if ok else "✗", name, mode, step1,
                ",".join(row["picked"]) or "-", reason), flush=True)

    plan = [(i,) + p for i, p in enumerate(plan)]
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, a.jobs)) as pool:
        list(pool.map(work, plan))

    print()
    summary = {}
    for mode in modes:
        sel = [r for r in rows if r and r["mode"] == mode]
        toks = [r["prompt_tokens"] for r in sel if r["prompt_tokens"] is not None]
        step1 = [r["steps"][0] for r in sel if r["steps"]]
        summary[mode] = {
            "correct": sum(1 for r in sel if r["ok"]),
            "strict": sum(1 for r in sel if r["strict"]),
            "n": len(sel),
            "step1_sum": sum(step1) if step1 else None,
            "step1_n": len(step1),
            "prompt_tokens_sum": sum(toks) if toks else None,
            "stats_input_sum": sum(r["stats_input"] for r in sel),
        }
        s = summary[mode]
        print("%-4s 正确 %d/%d（一次过 %d/%d）  第一步 input 合计 %s（%d 样本均 %.0f）  "
              "全程 prompt_tokens 合计 %s" % (
                  mode, s["correct"], s["n"], s["strict"], s["n"],
                  s["step1_sum"], s["step1_n"],
                  (s["step1_sum"] / s["step1_n"]) if s["step1_n"] else 0,
                  s["prompt_tokens_sum"]))

    if len(modes) == 2:
        o, n = summary["old"], summary["new"]
        if o["step1_sum"] and n["step1_sum"]:
            print("\n第一步（纯工具表口径）：%d → %d token，省 %.1f%%" % (
                o["step1_sum"], n["step1_sum"],
                100 * (o["step1_sum"] - n["step1_sum"]) / o["step1_sum"]))
        if o["prompt_tokens_sum"] and n["prompt_tokens_sum"]:
            print("整轮（含模型自己多跑几步）：%d → %d token，%+.1f%%" % (
                o["prompt_tokens_sum"], n["prompt_tokens_sum"],
                100 * (n["prompt_tokens_sum"] - o["prompt_tokens_sum"]) / o["prompt_tokens_sum"]))
        d = o["correct"] - n["correct"]
        print("正确率：%d/%d → %d/%d（差 %+d）" % (o["correct"], o["n"], n["correct"], n["n"], -d))
        if d > 2:
            print("→ 掉得太多（>2/12），**不建议改默认**。")
        elif d == 0:
            print("→ 正确率持平。改默认仍要看整轮 token 是否真的更省（见上）。")
        else:
            print("→ 掉 %d 条，看上面 ✗ 是哪一类再决定。" % d)

    out = os.path.join(ROOT, a.out)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"summary": summary, "rows": rows, "model": MODEL, "base": BASE,
                   "exe_old": EXE_OLD, "exe_new": EXE_NEW},
                  f, ensure_ascii=False, indent=2)
    print("明细：%s" % out)


if __name__ == "__main__":
    main()
