#!/usr/bin/env python3
"""工具表 A/B：compact（现状默认）vs merged（同族合成"一个工具 + action"）。

为什么要这个脚本：merged 能把四个族（watch/hindsight/schedule/team_task）
18 个工具压成 4 个，每步少付六七百 prompt token。但省 token 不是理由——
**模型还选不选得对、参数还填不填得对**才是。这里用真网关跑同一批任务，
两种表各跑一遍，逐条记：选了哪个工具/action、跑没跑通、prompt_tokens 多少。

判定（全部机器可判）：
  1. 关键工具必须真的被调用（merged 下要先把 action 还原成原工具名）
  2. 关键工具的最后一次调用是 ok（参数错后自己补对，算"自纠"，单独记一笔）
  3. 没撞满步数（撞满说明它在原地打转）
另有"多余调用"：不在任务允许集合里的工具（例如去 bash 里翻 gh 状态）。

prompt_tokens 取的是**服务端 usage**：会话日志里每条 assistant/message 的
data.usage.inputTokens（就是 usage.prompt_tokens），按步求和。不是估算器。
两种表的差别只在工具表，所以第一笔 inputTokens 就是"这张表贵多少"的最干净口径。

用法：
  python scripts/tool-table-merged-ab.py                # 12 个任务 × 2 种表
  python scripts/tool-table-merged-ab.py --runs 2       # 每格跑 2 次看抖动
  python scripts/tool-table-merged-ab.py --only merged  # 只跑一种表
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

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join(ROOT, "tmp", "DSH GO.exe")
BASE = os.environ.get("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1")
MODEL = os.environ.get("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017")

# merged 表里"模型看到的调用" -> 原工具名。脚本自己也要认这套映射，
# 否则没法判断 merged 下它到底选对没有。
MERGED = {
    "watch": {
        "start": "watch_start",
        "status": "watch_status",
        "stop": "watch_stop",
        "ack": "watch_ack",
        "doctor": "watch_doctor",
    },
    "hindsight": {
        "search_knowledge_pages": "hindsight_search_knowledge_pages",
        "list_knowledge_pages": "hindsight_list_knowledge_pages",
        "read_knowledge_page": "hindsight_read_knowledge_page",
        "ingest_document": "hindsight_ingest_document",
        "capture_initiative": "hindsight_capture_initiative",
    },
    "schedule": {
        "create": "schedule_create",
        "list": "schedule_list",
        "update": "schedule_update",
        "delete": "schedule_delete",
    },
    "team_task": {
        "create": "team_task_create",
        "list": "team_task_list",
        "get": "team_task_get",
        "update": "team_task_update",
    },
}

# 每个任务：(名字, 提示词, 允许出现的原工具集合, 必须出现且成功的原工具, 说明)
# 提示词分两类：一类点名工具（现实里 AGENTS/文档会点名），一类只说要做的事。
TASKS = [
    ("看板", "看看任务看板现在有哪些任务。",
     {"team_task_list", "team_task_get"}, "team_task_list", "不点名工具"),
    ("建任务", "用 team_task_create 建一条任务：subject「修 CI」，description「红灯」。",
     {"team_task_create", "team_task_get"}, "team_task_create", ""),
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
    ("存记忆", "用 hindsight_ingest_document 把「A/B 结论：merged 省一成输入」存进本机记忆，标题「merged-ab」。",
     {"hindsight_ingest_document", "hindsight_read_knowledge_page"},
     "hindsight_ingest_document", ""),
]

STATS = re.compile(r"stats: tool_calls=(\d+) input=(\d+) output=(\d+)")
CALL = re.compile(r"→ (\w+)\s*(.*)")
RESULT = re.compile(r"← (\w+) \[(ok|ERR)\](.*)")
ACT = re.compile(r'"action"\s*:\s*"([^"]*)"')


def canonical(name, args):
    """模型看到的调用 → (原工具名, action)。

    dshd 打到 stderr 的参数是**截断过的**（120 字），所以不能指望它是合法
    JSON：这里用正则抓 action 字段，抓不到就当"没给 action"。
    """
    if name not in MERGED:
        return name, None
    m = ACT.search(args or "")
    act = m.group(1) if m else None
    if act in MERGED[name]:
        return MERGED[name][act], act
    return "%s?%s" % (name, act), act


def prompt_tokens(home):
    """从会话日志里读服务端 usage（= usage.prompt_tokens），逐步返回。"""
    files = glob.glob(os.path.join(home, "sessions", "**", "session.v4.jsonl.zstd"), recursive=True)
    if not files:
        return None, []
    try:
        import zstandard  # 本机有；没有就退回 None，绝不自己估算
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


def run_one(prompt, mode):
    home = tempfile.mkdtemp(prefix="ab-merged-")
    args = [EXE, "run", "--home", home, "--policy", "allow", "--workspace-bytes", "0",
            "--max-steps", "6", "--model", MODEL, "--base", BASE,
            "--tools-mode", mode, "--prompt", prompt]
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
    picked = [canonical(n, a) for n, a in r["calls"]]
    names = [c[0] for c in picked]
    if not r["calls"]:
        return False, "没调工具（run 退出码 %s）" % r["exit"], False
    if r["cramped"]:
        return False, "撞满步数（原地打转）", False
    if must not in names:
        acts = [c[1] for c in picked if c[1]]
        return False, "没调关键工具 %s（调了 %s%s）" % (
            must, ", ".join(names), ("；action=" + ",".join(acts)) if acts else ""), False
    # 结果行是按调用顺序回来的，名字是**模型看到的**那个（watch/team_task），
    # 所以要按位置对齐再还原成原工具名，否则 merged 永远匹配不上 must。
    aligned = []
    for i, (rname, ok, _) in enumerate(r["results"]):
        cname = picked[i][0] if i < len(picked) else rname
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
    ap.add_argument("--only", choices=["compact", "merged"], default=None)
    ap.add_argument("--out", default="tmp/tool-table-merged-ab.json")
    a = ap.parse_args()
    if not os.path.exists(EXE):
        sys.exit('先 go build -o "tmp/DSH GO.exe" ./cmd/dshd')

    modes = [a.only] if a.only else ["compact", "merged"]
    rows = []
    for name, prompt, allowed, must, note in TASKS:
        for mode in modes:
            for _ in range(a.runs):
                r = run_one(prompt, mode)
                ok, reason, strict = judge(allowed, must, r)
                picked = [canonical(n, args)[0] for n, args in r["calls"]]
                rows.append({
                    "task": name, "mode": mode, "ok": ok, "strict": strict, "reason": reason,
                    "note": note, "picked": picked,
                    "raw_calls": [[n, x] for n, x in r["calls"]],
                    "results": [[n, "ok" if ok else "ERR", t] for n, ok, t in r["results"]],
                    "n_err": r["n_err"], "errs": r["errs"],
                    "prompt_tokens": r["prompt_tokens"], "steps": r["steps"],
                    "stats_input": r["stats"][1], "cramped": r["cramped"],
                    "exit": r["exit"],
                })
                step1 = r["steps"][0] if r["steps"] else None
                print("%s %-6s %-7s step1=%-6s picked=%-30s %s" % (
                    "✓" if ok else "✗", name, mode, step1,
                    ",".join(picked) or "-", reason))

    print()
    summary = {}
    for mode in modes:
        sel = [r for r in rows if r["mode"] == mode]
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
        print("%-7s 正确 %d/%d（一次过 %d/%d）  第一步 input 合计 %s（%d 样本均 %.0f）  "
              "全程 prompt_tokens 合计 %s" % (
                  mode, s["correct"], s["n"], s["strict"], s["n"],
                  s["step1_sum"], s["step1_n"],
                  (s["step1_sum"] / s["step1_n"]) if s["step1_n"] else 0,
                  s["prompt_tokens_sum"]))

    if len(modes) == 2:
        c, m = summary["compact"], summary["merged"]
        if c["step1_sum"] and m["step1_sum"]:
            print("\n第一步（纯工具表口径）：%d → %d token，省 %.1f%%" % (
                c["step1_sum"], m["step1_sum"],
                100 * (c["step1_sum"] - m["step1_sum"]) / c["step1_sum"]))
        if c["prompt_tokens_sum"] and m["prompt_tokens_sum"]:
            print("整轮（含模型自己多跑几步）：%d → %d token，%+.1f%%" % (
                c["prompt_tokens_sum"], m["prompt_tokens_sum"],
                100 * (m["prompt_tokens_sum"] - c["prompt_tokens_sum"]) / c["prompt_tokens_sum"]))
        d = c["correct"] - m["correct"]
        print("正确率：%d/%d → %d/%d（差 %+d）" % (c["correct"], c["n"], m["correct"], m["n"], -d))
        if d > 2:
            print("→ 掉得太多（>2/10），**不建议改默认**。")
        elif d == 0:
            print("→ 正确率持平。改默认仍要看整轮 token 是否真的更省（见上）。")
        else:
            print("→ 掉 %d 条，看上面 ✗ 是哪一类再决定。" % d)

    out = os.path.join(ROOT, a.out)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        json.dump({"summary": summary, "rows": rows, "model": MODEL, "base": BASE},
                  f, ensure_ascii=False, indent=2)
    print("明细：%s" % out)


if __name__ == "__main__":
    main()
