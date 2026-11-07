#!/usr/bin/env python3
"""把 dsh-TUI 的九款开屏点阵字体原样搬成 Go 表。

为什么要机器搬而不是手抄：那是 5 行 × 九款 × 18 个字形的像素点阵，
手抄必错，而且错了看起来只是"这个字母有点怪"——最难发现的那种错。

来源（MIT）：
  https://github.com/ccch1mneyyy/dsh-TUI  src/components/splashFonts.ts
  Copyright (c) 2026 chimney (ccch1mneyyy)

用法：python scripts/import-splash.py <splashFonts.ts> > cmd/dshtui/splash_gen.go

官方轮换池固定为 bold/square/bevel/wide/dot/stencil/classic/slab/shadow。
上游变换（SQUARE/BEVEL/WIDE/DOT/STENCIL）在这里逐格执行，产物是完整字形表；
shadow 直接搬 SHADOW_GLYPHS/FALLBACK，并校验官方 uniformKerning=1。
缺表、缺字、非 5 行或行宽不一致都直接失败，不生成静默 fallback。
"""
import hashlib
import math
import re
import sys

FONT_ORDER = ("bold", "square", "bevel", "wide", "dot", "stencil", "classic", "slab", "shadow")


def parse_table(src, name):
    """抓 `const <name>: GlyphTable = { ... }` 里的 5 行点阵。"""
    m = re.search(r"const %s: GlyphTable = \{(.*?)\n\}" % name, src, re.S)
    if not m:
        raise SystemExit("没找到字形表 %s" % name)
    body = m.group(1)
    out = {}
    # 形如  A: ['.....', '.....', ...],
    for gm in re.finditer(r"([A-Za-z0-9·])\s*:\s*\[(.*?)\]", body, re.S):
        key, rows_src = gm.group(1), gm.group(2)
        rows = re.findall(r"'([^']*)'", rows_src)
        if len(rows) != 5:
            raise SystemExit("字形 %s/%s 不是 5 行：%r" % (name, key, rows))
        out[key] = rows
    if not out:
        raise SystemExit("字形表 %s 为空" % name)
    return out


def parse_rows(src, name):
    m = re.search(r"const %s: GlyphRows = \[(.*?)\]" % name, src, re.S)
    if not m:
        raise SystemExit("没找到 fallback %s" % name)
    rows = re.findall(r"'([^']*)'", m.group(1))
    if len(rows) != 5:
        raise SystemExit("fallback %s 不是 5 行：%r" % (name, rows))
    return rows


def parse_shadow_uniform_kerning(src):
    """读取注册表里的 shadow 固定字距；缺声明或不是官方 1 都 fail-loud。"""
    m = re.search(r"\bshadow:\s*font\('shadow',\s*\{(.*?)\}\s*\)", src, re.S)
    if not m:
        raise SystemExit("没找到 shadow 字体注册项")
    value = re.search(r"\buniformKerning:\s*(\d+)", m.group(1))
    if not value:
        raise SystemExit("shadow 缺 uniformKerning=1")
    kerning = int(value.group(1))
    if kerning != 1:
        raise SystemExit("shadow uniformKerning=%d，应为官方 1" % kerning)
    return kerning


def is_ink(cell):
    return cell in ("█", "▀", "▄")


def apply_rows(rows, fn):
    return [fn(row, y, rows) for y, row in enumerate(rows)]


def apply_table(table, fn):
    return {ch: apply_rows(rows, fn) for ch, rows in table.items()}


def square(row, _y, _rows):
    return "".join("█" if cell in ("▀", "▄") else cell for cell in row)


def dot(row, _y, _rows):
    return "".join("▓" if cell == "█" else "▒" if cell in ("▀", "▄") else cell for cell in row)


def bevel(row, y, rows):
    def solid(source, x):
        return 0 <= x < len(source) and is_ink(source[x])

    out = []
    for x, cell in enumerate(row):
        if not is_ink(cell):
            out.append(cell)
            continue
        up = solid(rows[y - 1] if y > 0 else "", x)
        down = solid(rows[y + 1] if y + 1 < len(rows) else "", x)
        if not up and not down:
            out.append("▓" if y == len(rows) - 1 else "█")
        elif not down:
            out.append("▓")
        elif not up:
            out.append("█")
        else:
            left = solid(row, x - 1)
            right = solid(row, x + 1)
            out.append("█" if right or not left else "▓")
    return "".join(out)


def wide(row, _y, _rows):
    # JS Math.round((x+0.5)*len/8-0.5)，再夹到源字身范围。
    cells = list(row)
    out = []
    for x in range(8):
        source = math.floor((x + 0.5) * len(cells) / 8 - 0.5 + 0.5)
        out.append(cells[max(0, min(len(cells) - 1, source))])
    return "".join(out)


def stencil(row, y, rows):
    if y == 0 or y == len(rows) - 1:
        return row
    above = rows[y - 1]
    below = rows[y + 1]

    def vertical(x):
        up = above[x] if x < len(above) else " "
        down = below[x] if x < len(below) else " "
        return is_ink(up) and is_ink(down)

    bridge = False
    out = []
    for x, cell in enumerate(row):
        if not is_ink(cell) or not vertical(x):
            bridge = False
            out.append(cell)
        elif not bridge:
            bridge = True
            out.append(cell)
        else:
            out.append(" ")
    return "".join(out)


def validate_face(name, glyphs, fallback):
    if len(fallback) != 5 or not fallback[0]:
        raise SystemExit("%s fallback 不是非空 5 行" % name)
    width = len(fallback[0])
    if any(len(row) != width for row in fallback):
        raise SystemExit("%s fallback 行宽不等于 %d" % (name, width))
    if len(glyphs) != 18:
        raise SystemExit("%s 字形数=%d，应为 18" % (name, len(glyphs)))
    for ch, rows in glyphs.items():
        if len(rows) != 5 or any(len(row) != width for row in rows):
            raise SystemExit("%s/%s 必须是 5 行且每行 %d 列" % (name, ch, width))


def go_string_lit(s):
    # Go 字符串里只需要转义反斜杠和引号。
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def generate(src, source_sha256):
    bold = parse_table(src, "BOLD_GLYPHS")
    classic = parse_table(src, "CLASSIC_GLYPHS")
    slab = parse_table(src, "SLAB_GLYPHS")
    shadow = parse_table(src, "SHADOW_GLYPHS")
    bold_fallback = parse_rows(src, "BOLD_FALLBACK")

    # 三元组第三项是官方 uniformKerning；0 表示未声明，走普通等宽求解器。
    faces = {
        "bold": (bold, bold_fallback, 0),
        "square": (apply_table(bold, square), apply_rows(bold_fallback, square), 0),
        "bevel": (apply_table(bold, bevel), apply_rows(bold_fallback, bevel), 0),
        "wide": (apply_table(bold, wide), apply_rows(bold_fallback, wide), 0),
        "dot": (apply_table(bold, dot), apply_rows(bold_fallback, dot), 0),
        "stencil": (apply_table(bold, stencil), apply_rows(bold_fallback, stencil), 0),
        "classic": (classic, parse_rows(src, "CLASSIC_FALLBACK"), 0),
        "slab": (slab, parse_rows(src, "SLAB_FALLBACK"), 0),
        "shadow": (shadow, parse_rows(src, "SHADOW_FALLBACK"), parse_shadow_uniform_kerning(src)),
    }
    expected_chars = set(bold)
    for name in FONT_ORDER:
        glyphs, fallback, _uniform_kerning = faces[name]
        if set(glyphs) != expected_chars:
            raise SystemExit("%s 的字母集合与 bold 不一致" % name)
        validate_face(name, glyphs, fallback)

    lines = [
        "// 由 scripts/import-splash.py 生成，别手改。",
        "//",
        "// 点阵字体来源：dsh-TUI（MIT）",
        "//   https://github.com/ccch1mneyyy/dsh-TUI  src/components/splashFonts.ts",
        "//   Copyright (c) 2026 chimney (ccch1mneyyy)",
        "//",
        "// 九款字形均与源文件逐格一致；派生款的变换也在生成器中执行。",
        "// shadow 的 uniformKerning=1 来自官方注册表，不走等宽字距求解。",
        "// 要改字体请改源、重新生成，不要在这里手抄。",
        "",
        "package main",
        "",
        '// splashFontSourceSHA256 是生成时读取的 splashFonts.ts 原始字节摘要。',
        'const splashFontSourceSHA256 = "%s"' % source_sha256,
        "",
        "// splashFontAssets 的顺序就是官方九款每日轮换的取模顺序。",
        "var splashFontAssets = []splashFontAsset{",
    ]
    for name in FONT_ORDER:
        glyphs, fallback, uniform_kerning = faces[name]
        lines.extend([
            "\t{",
            '\t\tid: "%s",' % name,
        ])
        lines.append("\t\tglyphs: map[rune][5]string{")
        for key in sorted(glyphs):
            rows = ", ".join(go_string_lit(row) for row in glyphs[key])
            lines.append("\t\t\t'%s': {%s}," % (key, rows))
        lines.extend(["\t\t},", "\t\tfallback: [5]string{"])
        for row in fallback:
            lines.append("\t\t\t%s," % go_string_lit(row))
        lines.append("\t\t},")
        if uniform_kerning:
            lines.append("\t\tuniformKerning: %d," % uniform_kerning)
        lines.append("\t},")
        if name != FONT_ORDER[-1]:
            lines.append("")
    lines.extend(["}", ""])
    return "\n".join(lines)


def main():
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    with open(sys.argv[1], "rb") as f:
        raw = f.read()
    try:
        src = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise SystemExit("splashFonts.ts 不是 UTF-8：%s" % exc)
    output = generate(src, hashlib.sha256(raw).hexdigest())
    # 二进制 stdout 保证 Windows 也生成 LF，重复运行逐字节一致。
    sys.stdout.buffer.write(output.encode("utf-8"))


if __name__ == "__main__":
    main()
