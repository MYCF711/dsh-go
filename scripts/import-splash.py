#!/usr/bin/env python3
"""把 dsh-TUI 的开屏点阵字体原样搬成 Go 表。

为什么要机器搬而不是手抄：那是 5 行 × 几十个字形的像素点阵，
手抄必错，而且错了看起来只是"这个字母有点怪"——最难发现的那种错。

来源（MIT）：
  https://github.com/ccch1mneyyy/dsh-TUI  src/components/splashFonts.ts
  Copyright (c) 2026 chimney (ccch1mneyyy)

用法：python scripts/import-splash.py <splashFonts.ts> > cmd/dshtui/splash_gen.go
"""
import re
import sys


def parse_table(src, name):
    """抓 `const <name>: GlyphTable = { ... }` 里的 5 行点阵。"""
    m = re.search(r"const %s: GlyphTable = \{(.*?)\n\}" % name, src, re.S)
    if not m:
        raise SystemExit("没找到 %s" % name)
    body = m.group(1)
    out = {}
    # 形如  A: ['.....', '.....', ...],
    for gm in re.finditer(r"([A-Za-z0-9·])\s*:\s*\[(.*?)\]", body, re.S):
        key, rows_src = gm.group(1), gm.group(2)
        rows = re.findall(r"'([^']*)'", rows_src)
        if len(rows) != 5:
            raise SystemExit("字形 %s 不是 5 行：%r" % (key, rows))
        out[key] = rows
    return out


def go_string_lit(s):
    # Go 字符串里只需要转义反斜杠和引号
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def main():
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    src = open(sys.argv[1], encoding="utf-8").read()

    bold = parse_table(src, "BOLD_GLYPHS")
    fb = re.search(r"const BOLD_FALLBACK: GlyphRows = \[(.*?)\]", src, re.S).group(1)
    fallback = re.findall(r"'([^']*)'", fb)
    if len(fallback) != 5:
        raise SystemExit("fallback 不是 5 行：%r" % fallback)

    print("// 由 scripts/import-splash.py 生成，别手改。")
    print("//")
    print("// 点阵字体来源：dsh-TUI（MIT）")
    print("//   https://github.com/ccch1mneyyy/dsh-TUI  src/components/splashFonts.ts")
    print("//   Copyright (c) 2026 chimney (ccch1mneyyy)")
    print("//")
    print("// 生成的文件和源文件逐格一致；要改字体请改源、重新生成，")
    print("// 不要在这里改——手抄 5 行点阵错一格，看起来只会像\"这个字母有点怪\"。")
    print()
    print("package main")
    print()
    print("// splashGlyphs 是 bold 基准款：5 行 × %d 个字形。" % len(bold))
    print("var splashGlyphs = map[rune][5]string{")
    for k in sorted(bold):
        rows = ", ".join(go_string_lit(r) for r in bold[k])
        print("\t'%s': {%s}," % (k, rows))
    print("}")
    print()
    print("// splashFallback 是表里没有的字形用的占位块。")
    print("var splashFallback = [5]string{")
    for r in fallback:
        print("\t%s," % go_string_lit(r))
    print("}")


if __name__ == "__main__":
    main()
