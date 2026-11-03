#!/usr/bin/env python3
"""一条命令跑完 §12 的所有真机探针，并把结果对回需求表。

为什么要有它：探针散在 scripts/ 里，**下一个人不会去跑**。
散着放的验证等于没有验证——这一轮的结论只有跑过的人记得。

用法：
  python scripts/h-suite.py            # 全跑（约 6–10 分钟，含真模型调用）
  python scripts/h-suite.py --fast     # 只跑不需要模型的（约 30 秒）
  python scripts/h-suite.py --only h13 # 只跑名字里带 h13 的

每条探针只碰自己的临时 home，只回收自己启动的 PID。
"""
import re
import subprocess
import sys
import time

# (脚本, 它证明哪几条 H, 是否需要真模型, 超时秒)
PROBES = [
    ("help-guard", "CLI 帮助：三入口同一开关、无过期说法、都有说明", False, 180),
    ("contract-evidence", "契约表：每个 ✅ 都点名真实存在的证据", False, 120),
    ("flag-wired", "开关：定义了就一定接到调用链上，同函数内不撞名", False, 120),
    ("method-wired", "Transport 方法：声明了就一定在分发里，快照不越界", False, 120),
    ("guard-selftest", "把每个守卫弄坏一次：必须红（空转也必须红）", False, 200),
    ("h11-stdout", "H11 stdout 恰好一行 ready", False, 90),
    ("h10-chain", "H10 create→list→每个 id 两种入参名 load→prompt", False, 240),
    ("h14-isolation", "H14 两会话各自 seq、互不串流", True, 300),
    ("h13-approval-e2e", "H13 允许真执行 / 拒绝真不执行", True, 400),
    ("h13-timeout", "H13 超时按拒绝 + approval/timeout 帧", True, 300),
    ("h13-replay", "H13 断线重连重放挂起审批", True, 300),
    ("h13-allow-always", "H13 以后都允许 → 不再问", True, 300),
    ("ui-e2e", "U0/H8/H14 界面调用序列（16 项）", True, 400),
]

SUM = re.compile(r"合计 OK=(\d+) FAIL=(\d+)")


def run(name, timeout):
    path = "scripts/%s.py" % name
    started = time.time()
    try:
        p = subprocess.run([sys.executable, "-u", path], capture_output=True,
                           text=True, timeout=timeout)
        out = (p.stdout or "") + (p.stderr or "")
        code = p.returncode
    except subprocess.TimeoutExpired:
        return None, None, timeout, "超时（%ds）" % timeout, ""
    except FileNotFoundError:
        return None, None, 0, "脚本不存在", ""
    took = int(time.time() - started)

    m = SUM.findall(out)
    if not m:
        # 有的探针把明细写进 tmp/<name>.out：取不到就再看那里一眼，
        # 但仍然**不接受"什么都没有"**——没有结果就是没验。
        try:
            with open("tmp/%s.out" % name, encoding="utf-8") as f:
                m = SUM.findall(f.read())
        except OSError:
            pass
    if not m:
        # 没有汇总行 = 探针没跑到底（崩了/被跳过）。**不能当成功**。
        tail = "\n".join(out.strip().splitlines()[-3:])
        return None, None, took, "没有汇总行，退出码 %d：%s" % (code, tail), ""
    oks = sum(int(a) for a, _ in m)
    fails = sum(int(b) for _, b in m)
    # 把红的那几条带出去：调用方要能说出"哪一条"，光一个 4/5 等于没说。
    red = "；".join(ln.strip() for ln in out.splitlines() if "FAIL" in ln)[:200]
    return oks, fails, took, "", red


def main():
    args = sys.argv[1:]
    fast = "--fast" in args
    only = None
    if "--only" in args:
        i = args.index("--only")
        if i + 1 < len(args):
            only = args[i + 1]

    rows = []
    bad = 0
    for name, label, needs_model, timeout in PROBES:
        if only and only not in name:
            continue
        if fast and needs_model:
            rows.append((name, label, "跳过", "", ""))
            continue
        print("跑 %-18s %s …" % (name, label), flush=True)
        oks, fails, took, err, red = run(name, timeout)
        if err:
            bad += 1
            rows.append((name, label, "FAIL", "", err))
            print("  → %s\n" % err, flush=True)
            continue
        if fails:
            bad += 1
        # 只报 "4/5" 等于没说：要说出**哪一条**红了，否则下次还得手动重跑。
        # 红行由 run() 带回来——它才持有探针的完整输出。
        # （第一版在这里引用了一个不存在的局部变量 out，于是**只有探针失败时**才炸；
        #  那意味着所有跑绿的日子都发现不了它。红路径上的代码最容易烂，因为它最少被执行。）
        detail = red if fails else ""
        rows.append((name, label, "OK" if fails == 0 else "FAIL",
                     "%d/%d" % (oks, oks + fails), detail))

    if not rows:
        # --only 打错字／探针改名后最容易落到这里：
        # 一条都没跑却报"全部通过"，是**最坏的一种绿**。
        print("一个探针都没跑（检查 --only 的匹配）")
        return 2

    print()
    print("%-18s %-44s %-6s %-6s" % ("探针", "证明什么", "结果", "通过"))
    print("-" * 80)
    for name, label, verdict, score, err in rows:
        print("%-18s %-44s %-6s %-6s" % (name, label[:44], verdict, score))
        if err:
            print("     ↳ %s" % err.splitlines()[0][:100])
    print()
    print("总体：%s" % ("全部通过" if bad == 0 else "%d 条需要处理" % bad))
    return 0 if bad == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
