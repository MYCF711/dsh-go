#!/usr/bin/env python3
"""工具活性体检：每个工具在这台机器上**真的有东西可做吗**。

为什么需要：`skill` 曾经全绿——schema 对、调用返回 [ok]、但 `<home>/skills` 根本不存在，
它能返回的只有"没装任何 skill"。**"装了"和"有用"是两件事**，
而单测、快照、工具表都看不出这个差别。

做法：一个临时 home，一次模型调用让它**按顺序调一批工具**，然后看每一条的
`→ tool` / `← tool [ok|ERR]`。比一个工具开一轮便宜得多（一批 5–6 个）。

默认**不在** h-suite 的常规档里：它要真调模型，慢且花钱。用 `--tool-live` 打开。

判据：
  - 每个工具都必须出现一次（漏调 = 没验，不是通过）
  - 出现 `[ERR]` 的，只接受**明确写进 EXPECT_ERR 的**那一条（例如空 skills 目录）
"""
import os
import re
import shutil
import subprocess
import sys

BIN = "tmp/serve-dir/dshd.exe"
HOME = "tmp/tool-live"

# 允许"空着就是对的"的工具：没内容时它必须报错并给出怎么补，而不是假装能用。
EXPECT_ERR = {"skill"}

BATCHES = [
    (
        ["hindsight_ingest_document", "hindsight_list_knowledge_pages",
         "schedule_create", "schedule_list", "team_task_create", "team_task_list"],
        "严格按顺序调用这 6 个工具，每个只调一次，不要解释、不要跳过："
        "hindsight_ingest_document(title=体检,content=一句测试)；hindsight_list_knowledge_pages()；"
        "schedule_create(title=t1,in_seconds=3600,prompt=ping)；schedule_list()；"
        "team_task_create(subject=s,description=d)；team_task_list()。全部做完只回答'完'。",
    ),
    (
        # 计划模式**单独一批**：exit_plan_mode 的 Stop=true 会让这一轮就结束，
        # 后面再挂别的工具，模型根本没机会调——那样它俩会被记成"没验"。
        ["enter_plan_mode", "exit_plan_mode"],
        "严格按顺序调用这两个工具，每个只调一次，不要解释："
        "enter_plan_mode(reason=体检)；然后 exit_plan_mode(plan=体检方案)。",
    ),
    (
        ["plugin", "workflow"],
        "严格按顺序调用这两个工具，每个只调一次，不要解释："
        "plugin(action=list)；workflow(script=[{\"tool\":\"repo_here\",\"args\":{}}])。"
        "全部做完只回答'完'。",
    ),
    (
        ["watch_status", "load_workspace_dependencies", "repo_here",
         "unlag_refresh", "skill"],
        "严格按顺序调用这 5 个工具，每个只调一次，不要解释：watch_status、"
        "load_workspace_dependencies、repohere（工具真名 repo_here）、"
        "unlag_refresh(requestId=demo)、skill（不带参数）。全部做完只回答'完'。",
    ),
]

CALL = re.compile(r"^[\s←→]*(\w+)\s+\[(ok|ERR)\]", re.M)


def run_batch(names, prompt):
    shutil.rmtree(HOME, ignore_errors=True)
    os.makedirs(HOME, exist_ok=True)
    p = subprocess.run([BIN, "run", "--home", HOME, "--policy", "allow",
                        "--max-steps", "14", "--prompt", prompt],
                       capture_output=True, text=True, timeout=900)
    out = (p.stdout or "") + (p.stderr or "")
    # 只认"结果"行：← 开头；→ 是请求行，不算结论
    seen = {}
    for line in out.splitlines():
        if not line.strip().startswith("←"):
            continue
        m = re.match(r"\s*←\s+(\w+)\s+\[(ok|ERR)\]", line)
        if m:
            seen[m.group(1)] = m.group(2)
    return seen, out


def main():
    if not os.path.exists(BIN):
        print("  FAIL 找不到 %s（先 go build -o tmp/serve-dir/dshd.exe ./cmd/dshd）" % BIN)
        print("合计 OK=0 FAIL=1")
        return 1

    ok = fail = 0
    for names, prompt in BATCHES:
        seen, out = run_batch(names, prompt)
        for n in names:
            if n not in seen:
                # 没被调用 = 没验。**不能算通过。**
                fail += 1
                print("  FAIL %s 根本没被调用（这一条等于没验）" % n)
                continue
            verdict = seen[n]
            if verdict == "ok":
                ok += 1
                print("  OK   %s 活着" % n)
            elif n in EXPECT_ERR:
                ok += 1
                print("  OK   %s 空着时如实报错（预期）" % n)
            else:
                fail += 1
                print("  FAIL %s 返回 ERR —— 在这台机器上它做不了事" % n)
        if not seen:
            tail = "\n".join(out.strip().splitlines()[-3:])
            print("  → 这一批一条结论都没有，原始尾部：%s" % tail)
            fail += 1

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
