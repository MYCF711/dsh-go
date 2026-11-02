#!/usr/bin/env python3
"""量一个工具文件里 Description() 的字符数（和测试同一个口径：按 rune 数）。

用法：
    python scripts/desc-len.py modules/tool/foo/foo.go

为什么需要它：`TestEveryToolHasDescription` 按 **rune** 数算，中文标点也算一个字。
模型自己数不准（实测连着四次超限），所以给一条能直接跑的命令。

退出码：0 = 合格；1 = 太长；2 = 找不到 Description。
"""
import sys

LIMIT = 120
KEY = "func (t *Tool) Description() string {"


def body_of(src):
    i = src.find(KEY)
    if i < 0:
        raise SystemExit(f"找不到 Description()（{KEY}）")
    i += len(KEY)
    j = src.find("\n}", i)
    if j < 0:
        j = len(src)
    return src[i:j]


def string_literals(body):
    """取函数体里的字符串字面量。用状态机而不是正则——反斜杠转义太容易写错。"""
    out, buf, inside, escape = [], [], False, False
    for ch in body:
        if inside:
            if escape:
                buf.append(ch)
                escape = False
            elif ch == "\\":
                escape = True
            elif ch == '"':
                out.append("".join(buf))
                buf = []
                inside = False
            else:
                buf.append(ch)
        elif ch == '"':
            inside = True
    return out


def main():
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    path = sys.argv[1]
    src = open(path, encoding="utf-8").read()
    body = body_of(src)
    # 去掉行注释，免得注释里的引号被算进去
    body = "\n".join(line.split("//")[0] for line in body.splitlines())
    parts = string_literals(body)
    if not parts:
        print(f"{path}: Description() 里没有字符串字面量")
        return 2
    n = len("".join(parts))
    if n <= LIMIT:
        print(f"{n}/{LIMIT} 字 · OK")
        return 0
    print(f"{n}/{LIMIT} 字 · 太长了，砍到 {LIMIT} 以内（中文标点也算一个字）")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
