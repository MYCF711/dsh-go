#!/usr/bin/env python3
"""检查 AGENTS.md 里引用的 docs/ 与 scripts/ 路径是否还存在、引用的脚本是否还能解析。

引用过期没人知道（文件改名/删除后手册还在指），所以每次门禁跑一遍。
手册指着一个语法坏掉的脚本，和指着一个不存在的脚本一样糟——只是 `ls` 看得见，
所以 `scripts/*.py` 额外过一遍 `ast.parse`。

缺失 > 0 或语法坏 > 0 时退出码 1；AGENTS.md 找不到时退出码 2。
只用标准库。
"""

import ast
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


def syntax_error_name(path: Path) -> str | None:
    """返回语法错误的异常类名；能正常解析则返回 None。"""
    try:
        source = path.read_text(encoding="utf-8", errors="replace")
        ast.parse(source, filename=str(path))
    except SyntaxError as exc:
        return type(exc).__name__
    except (OSError, ValueError) as exc:
        return type(exc).__name__
    return None


def main() -> int:
    root = find_root()
    if root is None:
        print(f"\u627e\u4e0d\u5230 {MANUAL}")
        return 2

    text = (root / MANUAL).read_text(encoding="utf-8", errors="replace")
    refs = sorted(set(REF_RE.findall(text)))

    missing = 0
    bad = 0
    for ref in refs:
        target = root / ref
        if not target.is_file():
            print(f"MISS {ref}")
            missing += 1
            continue
        if ref.startswith("scripts/") and ref.endswith(".py"):
            err = syntax_error_name(target)
            if err is not None:
                print(f"BAD  {ref}\uff08\u8bed\u6cd5\u9519\u8bef\uff1a{err}\uff09")
                bad += 1
                continue
        print(f"OK   {ref}")

    print(
        f"\u5171 {len(refs)} \u4e2a\u5f15\u7528\uff0c"
        f"\u7f3a\u5931 {missing} \u4e2a\uff0c"
        f"\u8bed\u6cd5\u574f {bad} \u4e2a"
    )
    return 1 if (missing > 0 or bad > 0) else 0


if __name__ == "__main__":
    # CI 的 Windows 控制台不是 UTF-8（cp1252），中文 print 会 UnicodeEncodeError。
    # 那是"打印失败"，不是"检查失败"——两者在门禁输出里不能长得一样，所以强制 UTF-8。
    for _s in (sys.stdout, sys.stderr):
        try:
            _s.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError):
            pass
    sys.exit(main())
