#!/usr/bin/env python3
"""检查 AGENTS.md 里引用的 docs/ 与 scripts/ 路径是否还存在。

引用过期没人知道（文件改名/删除后手册还在指），所以每次门禁跑一遍。
缺失 > 0 时退出码 1；AGENTS.md 找不到时退出码 2。
只用标准库。
"""

import re
import sys
from pathlib import Path

REF_RE = re.compile(
    r"(?:docs/[A-Za-z0-9_.\-/]+\.md"
    r"|scripts/[A-Za-z0-9_.\-/]+\.(?:py|sh))"
)

MANUAL = "AGENTS.md"


def find_root() -> Path | None:
    """脚本在 <root>/scripts/ 下，先按这个推；失败再退回 CWD。"""
    script_root = Path(__file__).resolve().parent.parent
    if (script_root / MANUAL).is_file():
        return script_root
    cwd = Path.cwd()
    if (cwd / MANUAL).is_file():
        return cwd
    return None


def main() -> int:
    root = find_root()
    if root is None:
        print(f"\u627e\u4e0d\u5230 {MANUAL}")
        return 2

    text = (root / MANUAL).read_text(encoding="utf-8", errors="replace")
    refs = sorted(set(REF_RE.findall(text)))

    missing = 0
    for ref in refs:
        if (root / ref).is_file():
            print(f"OK   {ref}")
        else:
            print(f"MISS {ref}")
            missing += 1

    print(f"\u5171 {len(refs)} \u4e2a\u5f15\u7528\uff0c\u7f3a\u5931 {missing} \u4e2a")
    return 1 if missing > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
