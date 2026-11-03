#!/usr/bin/env python3
"""race-guard：把"哪些东西跨 goroutine"写成一份可执行清单。

背景：本机没有 C 编译器，`go test -race` 跑不起来（CGO_ENABLED=0）。
`-race` 能发现的那类 bug，我们只能靠审读——而"审读过了"和"每条都审过"长得一样。

这个检查做不到 `-race` 的事，它只做一件更笨但可执行的事：
**仓库里每一处 `go ` 启动的 goroutine 都必须在 race-goroutines.json 里登记**，
写明它碰哪些共享状态、靠什么同步。

新加一个 goroutine 不登记 → 退出码 1。
"""
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST = os.path.join(ROOT, "scripts", "race-goroutines.json")
SCAN_DIRS = ["internal", "cmd", "modules"]

# 只认"真起 goroutine"的写法：go func / go name( / go <- 等，排除 go: 指令与注释
GO_RE = re.compile(r'^\s*go\s+(func\b|[A-Za-z_][\w.]*\()')


def sites():
    out = []
    for d in SCAN_DIRS:
        for dirpath, _, files in os.walk(os.path.join(ROOT, d)):
            for fn in files:
                if not fn.endswith(".go") or fn.endswith("_test.go"):
                    continue
                p = os.path.join(dirpath, fn)
                try:
                    lines = open(p, encoding="utf-8").read().split("\n")
                except OSError:
                    continue
                for i, line in enumerate(lines, 1):
                    if GO_RE.match(line):
                        rel = os.path.relpath(p, ROOT).replace("\\", "/")
                        out.append(f"{rel}:{i}")
    return out


def main():
    found = sites()
    if "--list" in sys.argv:
        for s in found:
            print(s)
        print(f"共 {len(found)} 处", file=sys.stderr)
        return 0

    if not os.path.exists(MANIFEST):
        print(f"FAIL  找不到清单 {MANIFEST}", file=sys.stderr)
        return 1
    data = json.load(open(MANIFEST, encoding="utf-8"))
    declared = {e["site"]: e for e in data["goroutines"]}

    missing = [s for s in found if s not in declared]
    stale = [s for s in declared if s not in found]

    if missing:
        for s in missing:
            print(f"FAIL  新 goroutine 没登记：{s}（在 race-goroutines.json 里写清它碰什么、靠什么同步）",
                  file=sys.stderr)
    if stale:
        for s in stale:
            print(f"WARN  清单里的位置已经不存在：{s}（删掉这条，或它被挪走了）", file=sys.stderr)

    # 登记了但没读：和没登记一样危险——"有人写了名字"不等于"有人想过"。
    unread = [e["site"] for e in data["goroutines"]
              if not e.get("verified_at") or len(e.get("sync", "")) < 12]
    for s in unread:
        print(f"FAIL  登记了但没读过：{s}（sync 写清靠什么同步，并填 verified_at）", file=sys.stderr)

    if missing or unread:
        return 1
    print(f"PASS  跨 goroutine 的起点已全部登记（{len(found)} 处）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
