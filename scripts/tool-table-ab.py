#!/usr/bin/env python3
"""工具表开销 A/B：同一批任务，分别用完整表与压缩表跑，比较"结果对不对"和"花多少 token"。

为什么要这个脚本：`--tool-schema-compact` 能省两成上下文的输入，但它把参数的说明
文字去掉了——那是帮模型填对参数的一部分。省 token 不是理由，**不把活干坏**才是前提。

判定"对了"靠三条，都不需要人眼看：
  1. 任务要求的工具确实被调用了
  2. 那次调用返回 ok（不是 ERR）
  3. 没有撞满步数（撞满说明它在原地打转）

用法：
  python scripts/tool-table-ab.py            # 跑默认任务集
  python scripts/tool-table-ab.py --runs 3   # 每个任务重复 3 次（看抖动）
"""
import argparse, json, os, re, subprocess, sys, tempfile, shutil

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join(ROOT, "tmp", "dshd.exe")

# 每个任务：(名字, 提示词, 期望被调用的工具)
TASKS = [
    ("建任务", "用 team_task_create 建一条任务：subject「修 CI」，description「红灯」。", "team_task_create"),
    ("认领", "用 team_task_create 建一条任务（subject「A」，description「B」），然后认领它。", "team_task_update"),
    ("建目标", "用 create_goal 建一个目标：「跑通冒烟」。", "create_goal"),
    ("改目标", "先 create_goal 建目标「X」，再把它暂停。", "update_goal"),
    ("建定时", "用 schedule_create 建一个每 600 秒跑一次的定时任务，标题「体检」。", "schedule_create"),
    ("列定时", "用 schedule_list 看看现在有哪些定时任务。", "schedule_list"),
    ("盯仓库", "用 watch_start 盯 owner/name 这个仓库。", "watch_start"),
    ("待办", "用 todo_write 记两条待办：写测试、发版。", "todo_write"),
    ("搜索文件", "用 glob 找一下 cmd/dshd 下所有 *_test.go。", "glob"),
    ("读文件", "用 read 看 README.md 的前 20 行。", "read"),
]

STATS = re.compile(r"stats: tool_calls=(\d+) input=(\d+) output=(\d+)")


def run_one(prompt, compact):
    home = tempfile.mkdtemp(prefix="ab-")
    args = [EXE, "run", "--home", home, "--policy", "allow", "--workspace-bytes", "0",
            "--max-steps", "6", "--prompt", prompt]
    # 默认已经是 compact，要完整表得显式写
    args.insert(2, "--tool-schema=" + ("compact" if compact else "full"))
    try:
        p = subprocess.run(args, capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=300, cwd=ROOT)
    finally:
        shutil.rmtree(home, ignore_errors=True)
    out = (p.stdout or "") + (p.stderr or "")
    m = STATS.search(out)
    stats = tuple(int(x) for x in m.groups()) if m else (0, 0, 0)
    # 多步任务的第一步未必是目标工具（比如先 create 再 update），
    # 所以判"调过没有"看**全部**调用，判"干成没有"看有没有 [ERR]。
    called = re.findall(r"→ (\w+)", out)
    errs = len(re.findall(r"← \w+ \[ERR\]", out))
    oks = len(re.findall(r"← \w+ \[ok\]", out))
    cramped = "用满了" in out or "仍未收敛" in out
    return {"stats": stats, "called": called, "n_err": errs, "n_ok": oks, "cramped": cramped}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--out", default="tmp/tool-table-ab.json")
    a = ap.parse_args()
    if not os.path.exists(EXE):
        sys.exit("先 go build -o tmp/dshd.exe ./cmd/dshd")

    rows = []
    for name, prompt, want in TASKS:
        for compact in (False, True):
            for i in range(a.runs):
                r = run_one(prompt, compact)
                good = want in r["called"] and r["n_err"] == 0 and not r["cramped"]
                rows.append({"task": name, "want": want, "compact": compact, **r, "good": good})
                mark = "✓" if good else "✗"
                print(f"{mark} {name:8s} {'压缩' if compact else '完整'} "
                      f"调用={len(r['called'])} err={r['n_err']} input={r['stats'][1]}")

    def agg(compact):
        sel = [r for r in rows if r["compact"] == compact]
        if not sel:
            return (0, 0, 0)
        return (sum(1 for r in sel if r["good"]), len(sel), sum(r["stats"][1] for r in sel))

    full, comp = agg(False), agg(True)
    print("\n完整表: %d/%d 正确，合计 input=%d" % full)
    print("压缩表: %d/%d 正确，合计 input=%d" % comp)
    if full[0] == comp[0]:
        saved = 100 * (full[2] - comp[2]) / full[2] if full[2] else 0
        print(f"结果相同，省 {saved:.0f}% 输入")
    elif comp[0] < full[0]:
        print("压缩表**更差**：不能拿它当默认")
    else:
        print("数据不够/有波动，先别改默认")
    os.makedirs(os.path.dirname(os.path.join(ROOT, a.out)), exist_ok=True)
    with open(os.path.join(ROOT, a.out), "w", encoding="utf-8") as f:
        json.dump({"full": full, "compact": comp, "rows": rows}, f, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
