#!/usr/bin/env python3
"""ui-contract-usage.py —— 界面线 / 宿主线 method 契约对账。

A 侧（我们实现了什么）：读 internal/transport/api.go，抓常量声明里的
    Method = "xxx.yyy"
B 侧（界面在调什么）：递归扫 ../dsh-ui 下的 *.go（跳过 node_modules / .git），
    只在**字符串字面量**里找形如 session.xxx / fs.xxx / config.xxx / workspace.xxx
    的名字。注释、标识符（config.Paint 这种）一律不算。

为什么要对账：
  * 只在界面侧 → 界面会撞 404，是我们欠的债；
  * 只在我们侧 → 界面对着空气发请求，是契约里的死码。
  两个方向的差都是有信息量的，所以这**不是门禁**：对不上也不红。

退出码：
  * ../dsh-ui 目录不存在 → 打印「找不到 ../dsh-ui」，exit 2
  * 其余一律 exit 0

只用标准库。
"""

import os
import re
import sys
from pathlib import Path

# 方法名的命名空间（U0 契约里的四类）与「点后面那一段」的规则。
NAMESPACES = ("session", "fs", "config", "workspace")
METHOD_BODY = r"[A-Za-z][A-Za-z0-9_]*"

# 在字符串字面量内部找方法名：
#   前一个字符不能是标识符字符或点（排除 foo.session.list、session.v4.jsonl）
#   后一个字符同理（排除 session.v4.jsonl.zstd、session.list2.x）
METHOD_IN_LITERAL = re.compile(
    r"(?<![A-Za-z0-9_.])(?:" + "|".join(NAMESPACES) + r")\." + METHOD_BODY +
    r"(?![A-Za-z0-9_.])"
)

# api.go 里「Method = "xxx.yyy"」——只认常量声明这种形态，避免把注释里的例子算进去。
METHOD_IN_API = re.compile(
    r"\bMethod\s*=\s*\"(" + r"(?:" + "|".join(NAMESPACES) + r")\." + METHOD_BODY + r")\""
)

SKIP_DIRS = {"node_modules", ".git", ".hg", ".svn", "__pycache__", "vendor"}


def repo_root() -> Path:
    """脚本在 <root>/scripts/ 下，所以 root 是脚本目录的上一级。cwd 无关。"""
    return Path(__file__).resolve().parent.parent


def strip_go_source(src: str) -> str:
    """把注释和 rune 字面量抹掉，但**保留字符串字面量**。

    不这么做的话，注释里举个例子（"... 见 session.history"）会被当成引用，
    而注释里的引号又会把后面的扫描状态搞乱。
    """
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        # 行注释
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            while i < n and src[i] != "\n":
                i += 1
        # 块注释
        elif c == "/" and i + 1 < n and src[i + 1] == "*":
            i += 2
            while i + 1 < n and not (src[i] == "*" and src[i + 1] == "/"):
                i += 1
            i = min(i + 2, n)
        # 解释型字符串：原样保留（含转义）
        elif c == '"':
            j = i + 1
            while j < n:
                if src[j] == "\\":
                    j += 2
                    continue
                if src[j] == '"':
                    j += 1
                    break
                if src[j] == "\n":  # 不合法，兜底
                    break
                j += 1
            out.append(src[i:j])
            i = j
        # 原始字符串：原样保留
        elif c == "`":
            j = src.find("`", i + 1)
            j = n if j < 0 else j + 1
            out.append(src[i:j])
            i = j
        # rune 字面量：抹掉（'a'、'\n'、'"' 这类会把引号配错）
        elif c == "'":
            j = i + 1
            while j < n:
                if src[j] == "\\":
                    j += 2
                    continue
                if src[j] == "'":
                    j += 1
                    break
                if src[j] == "\n":
                    break
                j += 1
            i = j
        else:
            out.append(c)
            i += 1
    return "".join(out)


def string_literals(src: str):
    """从（已去注释的）源码里吐出每个字符串字面量的内容。"""
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == '"':
            j = i + 1
            buf = []
            while j < n and src[j] != '"':
                if src[j] == "\\" and j + 1 < n:
                    buf.append(src[j: j + 2])
                    j += 2
                    continue
                buf.append(src[j])
                j += 1
            yield "".join(buf)
            i = j + 1
        elif c == "`":
            j = src.find("`", i + 1)
            if j < 0:
                yield src[i + 1:]
                return
            yield src[i + 1: j]
            i = j + 1
        else:
            i += 1


def implemented_methods(root: Path):
    """A 侧：internal/transport/api.go 里声明出来的全部 method。"""
    api = root / "internal" / "transport" / "api.go"
    if not api.is_file():
        print("警告: 读不到 %s，A 侧按 0 个算" % api, file=sys.stderr)
        return set()
    src = api.read_text(encoding="utf-8", errors="replace")
    return set(METHOD_IN_API.findall(strip_go_source(src)))


def referenced_methods(ui_dir: Path):
    """B 侧：../dsh-ui 下 *.go 的字符串字面量里出现的方法名。"""
    found = set()
    for dirpath, dirnames, filenames in os.walk(ui_dir):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            if not name.endswith(".go"):
                continue
            path = Path(dirpath) / name
            try:
                src = path.read_text(encoding="utf-8", errors="replace")
            except OSError as exc:  # 权限/坏链接：跳过，不当成结论
                print("警告: 跳过 %s (%s)" % (path, exc), file=sys.stderr)
                continue
            for lit in string_literals(strip_go_source(src)):
                for hit in METHOD_IN_LITERAL.findall(lit):
                    found.add(hit)
    return found


def fmt(names) -> str:
    names = sorted(names)
    return ", ".join(names) if names else "（无）"


def main() -> int:
    root = repo_root()
    ui_dir = root.parent / "dsh-ui"

    if not ui_dir.is_dir():
        print("找不到 ../dsh-ui")
        return 2

    ours = implemented_methods(root)
    theirs = referenced_methods(ui_dir)

    only_theirs = theirs - ours
    only_ours = ours - theirs
    both = ours & theirs

    print("我们实现: %d 个" % len(ours))
    print("界面引用: %d 个" % len(theirs))
    print("只在界面侧（我们会 404）: %s" % fmt(only_theirs))
    print("只在我们侧（界面还没用）: %s" % fmt(only_ours))
    print("两边都有: %d 个" % len(both))
    print("对账完成")
    return 0


if __name__ == "__main__":
    sys.exit(main())
