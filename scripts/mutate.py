#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""变异测试：临时把实现改坏，看对应的测试会不会红。

用法:
    python scripts/mutate.py                # 跑全部
    python scripts/mutate.py --only 名字     # 只跑名字包含该子串的
    python scripts/mutate.py --list         # 只列名字

退出码: 全 CAUGHT 才 0；出现 MISSED / BAD / ERROR 为 1。
"""

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent
MUTATIONS_FILE = SCRIPT_DIR / "mutations.json"
GO_TIMEOUT = 300


def load_mutations(path):
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    if not isinstance(data, list):
        raise ValueError("mutations.json 顶层必须是数组")
    for i, m in enumerate(data):
        for key in ("name", "file", "old", "new", "pkg", "test"):
            if key not in m:
                raise ValueError("第 %d 项缺少字段 %r" % (i + 1, key))
    return data


def go_env():
    env = dict(os.environ)
    env["PATH"] = "/c/Program Files/Go/bin:" + env.get("PATH", "")
    return env


BUILD_FAILURE_MARKERS = (
    "[build failed]",
    "undefined:",
    "cannot use ",
    "syntax error",
    "declared and not used",
)


def build_failure_marker(out):
    """输出里有没有"包根本没编译过"的特征。返回命中的标记或 None。"""
    for marker in BUILD_FAILURE_MARKERS:
        if marker in out:
            return marker
    return None


def build_detail(out):
    """挑一行最能说明构建失败的输出（给表尾的 -> 用）。"""
    for line in out.splitlines():
        if any(marker in line for marker in BUILD_FAILURE_MARKERS):
            return line.strip()
    return out.strip().splitlines()[-1].strip() if out.strip() else "构建失败"


def run_go_test(pkg, test):
    """跑一条测试。返回 (状态, 耗时秒, 输出)。

    状态: caught / missed / bad / error
    """
    cmd = ["go", "test", pkg, "-count=1", "-run", test]
    started = time.time()
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(REPO_ROOT),
            env=go_env(),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=GO_TIMEOUT,
        )
        elapsed = time.time() - started
        out = proc.stdout.decode("utf-8", errors="replace")
        # go test 返回 0 = 通过（变异没被抓住）
        if proc.returncode == 0:
            return "missed", elapsed, out
        # 非 0：先分清"测试真的跑了且失败"和"包根本没编译过"。
        # 编译失败不是测试抓住了变异——测试一行都没跑，任何坏变异都能这么伪装被抓住。
        marker = build_failure_marker(out)
        if marker:
            return "bad", elapsed, "%s（构建失败标记: %s）" % (out, marker)
        return "caught", elapsed, out
    except subprocess.TimeoutExpired:
        elapsed = time.time() - started
        return "error", elapsed, "超时（>%ds）" % GO_TIMEOUT
    except Exception as exc:  # noqa: BLE001
        elapsed = time.time() - started
        return "error", elapsed, "子进程异常: %s" % exc


def _variants(js):
    """把 JSON 里的片段编成字节，同时兼容 LF 与 CRLF 文件。

    仓库统一 LF，但 Windows 工作区里有的文件是 CRLF，直接按 '\\n' 找会 0 命中。
    """
    raw = js.encode("utf-8")
    crlf = js.replace("\n", "\r\n").encode("utf-8")
    return [raw] if raw == crlf else [raw, crlf]


def apply_mutation(path, old, new):
    """把 old 恰好替换一次成 new。返回 (错误描述|None, 原始字节)。

    全程按字节读写：Python 文本模式在 Windows 上会把 '\\n' 翻成 '\\r\\n'，
    那等于悄悄改掉整个文件的每一行——恢复时必须一字节不差。
    """
    original = path.read_bytes()
    for old_b, new_b in zip(_variants(old), _variants(new)):
        count = original.count(old_b)
        if count == 1:
            path.write_bytes(original.replace(old_b, new_b, 1))
            return None, original
        if count > 1:
            return "old 片段出现了 %d 次，不唯一" % count, original
    return "文件里找不到 old 片段（出现 0 次）", original


def restore(path, original):
    """恢复原文件并回读校验。返回 None 表示一致，否则返回错误描述。"""
    path.write_bytes(original)
    now = path.read_bytes()
    if now != original:
        return "恢复后回读与原文不一致！"
    return None


def run_one(m, index, total):
    name = m["name"]
    print("\n[%d/%d] %s" % (index, total, name))

    path = REPO_ROOT / m["file"]
    if not path.is_file():
        print("  结果: BAD    文件不存在: %s" % m["file"])
        return {"name": name, "result": "BAD", "elapsed": 0.0,
                "detail": "文件不存在: %s" % m["file"]}

    err, original = apply_mutation(path, m["old"], m["new"])
    if err:
        # 没改成，也要确保文件是原样（apply 失败时通常没写，但统一走恢复）
        rerr = restore(path, original)
        detail = err
        if rerr:
            detail += "；" + rerr
        print("  结果: BAD    %s" % detail)
        return {"name": name, "result": "BAD", "elapsed": 0.0, "detail": detail}

    status = "error"
    elapsed = 0.0
    out = ""
    restore_err = None
    try:
        status, elapsed, out = run_go_test(m["pkg"], m["test"])
    finally:
        restore_err = restore(path, original)
        if restore_err:
            print("  !!! 恢复失败: %s" % restore_err)
            print("  !!! 原文件内容已丢失，请立即用编辑器/备份手工恢复: %s" % m["file"])

    label = {"caught": "CAUGHT", "missed": "MISSED",
             "bad": "BAD", "error": "ERROR"}[status]
    print("  结果: %-6s  %.1fs" % (label, elapsed))
    if status == "missed":
        print("  测试是废的：改坏了实现它还是绿的")
    elif status == "bad":
        print("  变异本身不合法：包没编译过，测试一行都没跑")
        print("  %s" % out.strip()[-500:])
    elif status == "error":
        print("  %s" % out.strip()[-500:])
    if restore_err:
        label = "ERROR"

    return {"name": name, "result": label, "elapsed": elapsed,
            "detail": restore_err or (build_detail(out) if status == "bad" else "")}


def print_table(results):
    print("\n" + "=" * 72)
    print("%-32s %-8s %10s" % ("name", "结果", "耗时"))
    print("-" * 72)
    for r in results:
        print("%-32s %-8s %9.1fs" % (r["name"], r["result"], r["elapsed"]))
        if r.get("detail"):
            print("    -> %s" % r["detail"])
    print("=" * 72)

    counts = {}
    for r in results:
        counts[r["result"]] = counts.get(r["result"], 0) + 1
    parts = ["%s=%d" % (k, counts[k]) for k in
             ("CAUGHT", "MISSED", "BAD", "ERROR") if k in counts]
    print("汇总: " + "  ".join(parts))


def main():
    ap = argparse.ArgumentParser(description="变异测试：改坏实现，看测试会不会红")
    ap.add_argument("--only", metavar="名字",
                    help="只跑名字包含该子串的变异")
    ap.add_argument("--list", action="store_true", dest="do_list",
                    help="只列出变异名字，不跑")
    args = ap.parse_args()

    try:
        mutations = load_mutations(MUTATIONS_FILE)
    except Exception as exc:  # noqa: BLE001
        print("读不了 %s: %s" % (MUTATIONS_FILE, exc))
        return 1

    if args.only:
        mutations = [m for m in mutations if args.only in m["name"]]
        if not mutations:
            print("没有名字包含 %r 的变异" % args.only)
            return 1

    if args.do_list:
        for m in mutations:
            print("%-32s %s :: %s" % (m["name"], m["pkg"], m["test"]))
        print("\n共 %d 条" % len(mutations))
        return 0

    if not mutations:
        print("mutations.json 里没有变异")
        return 0

    print("仓库根: %s" % REPO_ROOT)
    print("共 %d 条变异" % len(mutations))

    results = []
    for i, m in enumerate(mutations, 1):
        results.append(run_one(m, i, len(mutations)))

    print_table(results)

    bad = [r for r in results if r["result"] != "CAUGHT"]
    if bad:
        print("有 %d 条没抓住（%s）" % (
            len(bad), ", ".join(r["name"] for r in bad)))
        return 1
    print("全部被测试抓住")
    return 0


if __name__ == "__main__":
    # CI 的 Windows 控制台不是 UTF-8（cp1252），中文 print 会 UnicodeEncodeError。
    # 那是"打印失败"，不是"检查失败"——两者在门禁输出里不能长得一样，所以强制 UTF-8。
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    sys.exit(main())
