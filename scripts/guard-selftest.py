#!/usr/bin/env python3
"""守卫自测：把每个守卫**真的弄坏一次**，看它红不红。

规矩：新增任何守卫，都要在这里加一条"弄坏它、它必须红"。
理由很直接——跑不红的守卫等于没有守卫，而"它看起来在检查"这件事
没有任何外部信号能反驳。2026-11-03 的教训：`grep default "0"` 那条约等于恒真，
绿了很久才发现它从来没看过东西。

做法：在 tmp/ 里造一份被改坏的副本，用守卫自己的入参指过去，断言退出码非 0。
不改仓库里的任何文件。
"""
import os
import re
import shutil
import subprocess
import sys

TMP = "tmp/guard-selftest"


def run(args):
    p = subprocess.run([sys.executable, "-u"] + args, capture_output=True, text=True)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def main():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail)[:300])

    shutil.rmtree(TMP, ignore_errors=True)
    os.makedirs(TMP, exist_ok=True)

    # ---- 1) contract-evidence：把 ✅ 换成点名一个不存在的测试 ----
    src = open("docs/transport-contract.md", encoding="utf-8").read()
    broken = src.replace("`TestLoadPassesViewThrough`", "`TestThisDoesNotExistAnywhere`", 1)
    p = os.path.join(TMP, "contract.md")
    open(p, "w", encoding="utf-8", newline="").write(broken)
    code, out = run(["scripts/contract-evidence.py", p])
    chk("contract-evidence 对不存在的证据会红", code != 0 and "不存在" in out, out.strip()[-160:])

    # ---- 2) flag-wired：造一个定义了却没用的开关 ----
    d = os.path.join(TMP, "cmd")
    os.makedirs(d, exist_ok=True)
    open(os.path.join(d, "x.go"), "w", encoding="utf-8", newline="").write(
        'package main\n\nfunc f(args []string) {\n\tfs := flag.NewFlagSet("x", flag.ExitOnError)\n'
        '\tdead := fs.String("dead", "", "没人用的开关")\n\t_ = fs.Parse(args)\n}\n')
    code, out = run(["scripts/flag-wired.py", d])
    chk("flag-wired 对没接线的开关会红", code != 0 and "--dead" in out, out.strip()[-160:])

    # ---- 3) method-wired：删掉一个分发 case ----
    d2 = os.path.join(TMP, "tr")
    os.makedirs(d2, exist_ok=True)
    shutil.copy("internal/transport/api.go", os.path.join(d2, "api.go"))
    srv = open("internal/transport/server.go", encoding="utf-8").read()
    srv = srv.replace("\tcase MethodSessionApprove:", "\tcase MethodSessionApproveGone:", 1)
    open(os.path.join(d2, "server.go"), "w", encoding="utf-8", newline="").write(srv)
    code, out = run(["scripts/method-wired.py",
                     os.path.join(d2, "api.go"), os.path.join(d2, "server.go")])
    chk("method-wired 对缺失的分发会红", code != 0 and "MethodSessionApprove" in out,
        out.strip()[-160:])

    # ---- 4) 空转保护：守卫在没有输入时不许报绿 ----
    empty = os.path.join(TMP, "empty")
    os.makedirs(empty, exist_ok=True)
    code, out = run(["scripts/flag-wired.py", empty])
    chk("flag-wired 对空目录不报绿", code == 0 and "0 个开关" in out, out.strip()[-120:])

    shutil.rmtree(TMP, ignore_errors=True)
    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
