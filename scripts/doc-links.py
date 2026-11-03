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

# **`../` 前缀必须一起匹配**：正则是从 `docs/` 起匹配的，
# 于是 `../dsh-ui/docs/progress.md` 会被截成 `docs/progress.md`，
# 再拿去本仓校验就是必然的 MISS——而那个文件本来就在**姊妹仓**里。
# 跨仓引用不在本仓能校验的范围，识别出来跳过即可。
# **alphabet 顺序有意义**：`../` 那条必须排在 `docs/` 前面。
# 写成可选前缀 `(?:\.\./)?docs/...` 时，正则引擎会优先尝试"不带前缀"那条，
# 于是 `../dsh-ui/docs/progress.md` 仍然被截成 `docs/progress.md`
# ——加了前缀等于没加（实测过）。
REF_RE = re.compile(
    r"(\.\./[A-Za-z0-9_.\-/]+\.md"
    r"|docs/[A-Za-z0-9_.\-/]+\.md"
    r"|scripts/[A-Za-z0-9_.\-/]+\.(?:py|sh))"
)

MANUAL = "AGENTS.md"


def find_root(manual: str = MANUAL) -> Path | None:
    """脚本在 <root>/scripts/ 下，先按这个推；失败再退回 CWD。"""
    script_root = Path(__file__).resolve().parent.parent
    if (script_root / manual).is_file():
        return script_root
    cwd = Path.cwd()
    if (cwd / manual).is_file():
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
    # 可选参数：换一份「手册」来查。**只给自测用**——
    # 正式跑（scripts/check.sh）不带参数，查的是 AGENTS.md。
    # 需要这个开关是为了反证「弄坏必红」：得造一份坏手册，
    # 而不能去改仓库里真实的 AGENTS.md（那会把别人的文档搞坏）。
    manual = sys.argv[1] if len(sys.argv) > 1 else MANUAL
    root = find_root(manual)
    if root is None:
        print(f"找不到 {manual}")
        return 2

    text = (root / manual).read_text(encoding="utf-8", errors="replace")
    refs = sorted(set(REF_RE.findall(text)))

    missing = 0
    bad = 0
    for ref in refs:
        # 跨仓引用（`../dsh-ui/...`）不在本仓校验：文件在姊妹仓里，
        # 本仓永远看不到它。误报会让人去"修"一个根本不缺的引用。
        if ref.startswith("../"):
            print(f"SKIP {ref}（跨仓）")
            continue
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
