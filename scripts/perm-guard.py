#!/usr/bin/env python3
"""权限分支守卫：每个决定"放不放行"的函数都必须有一条测试钉着它。

为什么单独有这么一个检查：权限相关的分支**改坏了不一定报错**。
把 c.Destructive 从 NeedsApproval 里删掉，所有测试照样绿，而破坏性工具从此不再问人。
这类改动只要混进来一次，后面所有"测试全过"都不再可信。

做法：
1. 自动扫出"看起来是权限判定"的函数（函数体里出现 "allow" / "deny" / NeedsApproval / modeApproval）
2. 每个扫出来的函数必须在 scripts/perm-gates.json 里登记，并写明钉住它的测试函数名
3. 登记了但函数不见了、或者测试函数找不到 → 都算失败

新加一个权限分支却不登记，这条检查会红——这正是它的用途。
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
REG = ROOT / "scripts" / "perm-gates.json"

SKIP_DIRS = {"refs", "tmp", "node_modules", ".git"}
NEEDLE = re.compile('"allow"|"deny"|NeedsApproval[(]|modeApproval')
FUNC = re.compile(r"(?m)^func\s+(?:[(][^)]*[)]\s*)?([A-Za-z_]\w*)\s*[(]")


def walk_go():
    for p in ROOT.rglob("*.go"):
        parts = set(p.relative_to(ROOT).parts)
        if parts & SKIP_DIRS:
            continue
        if p.name.endswith("_test.go"):
            continue
        yield p


def gates():
    """[(相对路径, 函数名)]，按文件行序。"""
    out = []
    for p in walk_go():
        src = p.read_text(encoding="utf-8", errors="replace")
        starts = [m for m in FUNC.finditer(src)]
        for i, m in enumerate(starts):
            end = starts[i + 1].start() if i + 1 < len(starts) else len(src)
            if NEEDLE.search(src[m.start():end]):
                out.append((p.relative_to(ROOT).as_posix(), m.group(1)))
    return sorted(set(out))


def has_test(pkg_dir: pathlib.Path, test_name: str) -> bool:
    pat = re.compile(r"(?m)^func\s+" + re.escape(test_name) + r"\s*[(]")
    for t in pkg_dir.glob("*_test.go"):
        if pat.search(t.read_text(encoding="utf-8", errors="replace")):
            return True
    return False


def has_func(path: pathlib.Path, fn: str) -> bool:
    pat = re.compile(r"(?m)^func\s+(?:[(][^)]*[)]\s*)?" + re.escape(fn) + r"\s*[(]")
    return bool(pat.search(path.read_text(encoding="utf-8", errors="replace")))


def main() -> int:
    reg = json.loads(REG.read_text(encoding="utf-8")) if REG.exists() else {}
    known, exempt = reg.get("gates", {}), reg.get("exempt", {})
    found = gates()
    problems = []

    for path, fn in found:
        key = f"{path}::{fn}"
        if key in exempt or key in known:
            continue
        problems.append(
            f"新的权限分支没登记：{key}（登记到 scripts/perm-gates.json，或写进 exempt 并说明理由）"
        )

    for key, test in known.items():
        path, _, fn = key.rpartition("::")
        f = ROOT / path
        if not f.exists():
            problems.append(f"登记的文件不见了：{key}")
            continue
        if not has_func(f, fn):
            problems.append(f"登记的函数不见了：{key}（改名了就同步改登记）")
            continue
        if not has_test(f.parent, test):
            problems.append(f"{key} 的测试 {test} 找不到（在 {f.parent.as_posix()}/ 下的 *_test.go 里）")

    for key in exempt:
        if key not in {f"{p}::{n}" for p, n in found}:
            problems.append(f"豁免项已不存在，删掉它：{key}")

    if problems:
        print("权限分支守卫：")
        for x in problems:
            print("  ✗", x)
        return 1
    print(
        f"权限分支守卫：{len(found)} 个判定，{len(known)} 个有测试钉着，{len(exempt)} 个有理由豁免"
    )
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
