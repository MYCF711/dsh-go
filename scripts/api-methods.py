#!/usr/bin/env python3
"""数 /api 的方法个数，并与 U0 契约的期望值比对。

用途：契约少一个方法时 CI 能红。

读 internal/transport/api.go 里的 `Method = "session.xxx"` 这类常量声明，
排序打印方法名，再打印总数；数量不对就 exit 1。

退出码：
  0  数量等于期望值
  1  数量不对（能读到定义，但不是 EXPECTED 个）
  2  文件不存在，或一条方法定义都没匹配到（与"数量不对"必须能区分）

只用标准库。
"""

import re
import sys
from pathlib import Path

# UIPLAN.md 定的 U0 方法数：13 个
# （会话 8 + 工作区 1 + 文件 2 + 配置 2）。
# 这是契约快照 testdata/api.snapshot.json 里 "methods" 的条数，
# 改动它 = 破坏 U0 契约，必须先改快照与 UIPLAN.md。
EXPECTED = 13

API_GO = "internal/transport/api.go"

# `Method = "session.list"` —— 数值前面允许任意空白，方法名只收字母数字与点。
METHOD_RE = re.compile(r'\bMethod\s*=\s*"([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)"')


def api_go_path() -> Path:
    """仓库根 = 本脚本所在目录的上一级。"""
    return Path(__file__).resolve().parent.parent / API_GO


def find_methods(src: str) -> list:
    """返回源码里声明的方法名（去重、排序）。注释行不算。"""
    names = set()
    for line in src.splitlines():
        # 整行注释直接跳过，避免把注释里举例的方法名算进来。
        if line.lstrip().startswith("//"):
            continue
        # 行尾注释先剥掉，再匹配。
        code = line.split("//", 1)[0]
        for m in METHOD_RE.finditer(code):
            names.add(m.group(1))
    return sorted(names)


def main() -> int:
    path = api_go_path()
    if not path.is_file():
        print("找不到方法定义")
        return 2

    try:
        src = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        print("找不到方法定义")
        return 2

    methods = find_methods(src)
    if not methods:
        print("找不到方法定义")
        return 2

    for name in methods:
        print(name)

    count = len(methods)
    print("共 %d 个（期望 %d）" % (count, EXPECTED))
    return 0 if count == EXPECTED else 1


if __name__ == "__main__":
    sys.exit(main())
