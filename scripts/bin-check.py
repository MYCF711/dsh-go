#!/usr/bin/env python3
"""二进制体检：探针用的二进制，比源码旧吗？两份副本还是同一份吗？

为什么需要：2026-11-03 有一轮把 h10 的红记成了"宿主丢消息"，实际是
`scripts/h10-chain.py` 用的 `tmp/serve-dir/dshd.exe`（17:35）比 `tmp/dshd.exe`（18:48）旧。
**探针跑的是一个已经不存在的版本，结论却记在当前代码头上。**

这比大多数 bug 都贵：它让人去改没坏的地方。

两条判据：
  1. 二进制 mtime 不早于任何 `.go` 源码（`cmd/ internal/ modules/ codecs/`）
  2. 两份副本（如果都在）内容一致——sha256 相同

判红的处理方式只有一种：重新 `go build`。看下面的提示照着做即可。
"""
import hashlib
import os
import sys

BINS = ["tmp/dshd.exe", "tmp/serve-dir/dshd.exe"]
# 允许传若干二进制路径，只为**反证**用（见 scripts/guard-selftest.py）。
if len(sys.argv) > 1:
    BINS = sys.argv[1:]
SRC_ROOTS = ["cmd", "internal", "modules", "codecs"]


def newest_source():
    newest, path = 0.0, ""
    for root in SRC_ROOTS:
        for dirpath, _, files in os.walk(root):
            for f in files:
                # 跳过 _test.go：改测试**不会**改变二进制，
                # 把它算进来就会天天喊"二进制旧了"——假阳性会把这条检查废掉。
                if not f.endswith(".go") or f.endswith("_test.go"):
                    continue
                p = os.path.join(dirpath, f)
                try:
                    m = os.path.getmtime(p)
                except OSError:
                    continue
                if m > newest:
                    newest, path = m, p
    return newest, path


def sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ok = fail = 0

    def chk(name, cond, detail=""):
        nonlocal ok, fail
        if cond:
            ok += 1
            print("  OK   " + name)
        else:
            fail += 1
            print("  FAIL " + name + " " + str(detail))

    src_t, src_p = newest_source()
    if not src_p:
        # 一条 .go 都没扫到 = 走错目录，**不能报绿**。
        print("  FAIL 没找到任何 .go 源码（工作目录不对？）")
        print("合计 OK=0 FAIL=1")
        return 2

    present = [b for b in BINS if os.path.exists(b)]
    for b in BINS:
        if b not in present:
            print("  --   %s 不存在（这条探针不会用它）" % b)

    if not present:
        print("  FAIL 一个二进制都没有：先 go build -o tmp/dshd.exe ./cmd/dshd")
        print("合计 OK=0 FAIL=1")
        return 1

    stale = []
    for b in present:
        t = os.path.getmtime(b)
        if t < src_t:
            stale.append("%s（比 %s 旧）" % (b, src_p))
    chk("%d 个二进制都不比源码旧" % len(present), not stale, stale)

    if len(present) > 1:
        hashes = {b: sha(b) for b in present}
        same = len(set(hashes.values())) == 1
        chk("多份二进制内容一致（没有分叉）", same,
            {b: h[:8] for b, h in hashes.items()})

    if stale:
        print("  → 修法：go build -o tmp/dshd.exe ./cmd/dshd && cp tmp/dshd.exe tmp/serve-dir/dshd.exe")

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
