#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tool-gap.py —— 数真实会话里的工具调用，跟我们已有的工具表对比，列出还缺的。

用法:
    python scripts/tool-gap.py                 # 全量
    python scripts/tool-gap.py --top 10        # 只看前 10 行
    python scripts/tool-gap.py --sessions DIR  # 换语料目录

只读语料（只 open 'rb'），绝不写。
退出码: 缺的里面有出现 >=10 次的 -> 1（还有值得做的）；否则 0。
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

DEFAULT_SESSIONS = Path("D:/DeepSeek Harness/home/sessions")
SESSION_NAME = "session.v4.jsonl.zstd"
CHUNK = 1 << 20          # 1 MB 一片，流式解，不整个读进内存
MISSING_THRESHOLD = 10   # 缺的工具出现这么多次，就值得做


# ---------------------------------------------------------------- 输出辅助

def setup_stdout():
    """Windows 控制台默认 cp936，中文/emoji 会直接炸，能重配就重配。"""
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass


def eprint(*a):
    print(*a, file=sys.stderr)


# ---------------------------------------------------------------- 读语料

def open_stream(path):
    """逐帧流式解压一个多帧 zstd 文件，返回一个只读文本流。"""
    import zstandard as zstd

    fobj = open(path, "rb")          # 唯一一次接触语料：只读
    dctx = zstd.ZstdDecompressor()
    try:
        # 多帧必须 read_across_frames=True，否则读完第一帧就 EOF
        reader = dctx.stream_reader(fobj, read_across_frames=True)
    except TypeError:
        # 老版本 python-zstandard 没这个参数
        reader = dctx.stream_reader(fobj)
    return fobj, reader


def tool_name_of(ev):
    """从事件里取工具名；data.name / data.tool 两种都试，不假设只有一种。"""
    if not isinstance(ev, dict):
        return None
    data = ev.get("data")
    if isinstance(data, dict):
        for key in ("name", "tool", "tool_name"):
            v = data.get(key)
            if isinstance(v, str) and v:
                return v
    for key in ("name", "tool", "tool_name"):
        v = ev.get(key)
        if isinstance(v, str) and v:
            return v
    return None


def frames_complete(path):
    """逐帧走一遍，判断最后一帧是否完整。"""
    import zstandard as zstd

    dctx = zstd.ZstdDecompressor()
    data = path.read_bytes()
    while data:
        obj = dctx.decompressobj()
        try:
            obj.decompress(data)
        except zstd.ZstdError:
            return False
        if not obj.eof:
            return False
        data = obj.unused_data
    return True


def scan_file(path, counts):
    """扫一个会话文件，把 tool/call 的名字累加进 counts。

    返回 (事件数, 是否尾部不完整)。坏文件抛异常由调用方接。

    尾部不完整**必须报出来**：我们照常统计读到的部分（那部分是真数据），
    但"少读了几个事件"这件事不能瞒着——不做成沉默，否则一份 99% 的统计
    看起来和 100% 的统计一模一样。
    """
    fobj, reader = open_stream(path)
    events = 0
    truncated = False
    try:
        buf = b""
        if hasattr(reader, "read"):
            while True:
                chunk = reader.read(CHUNK)
                if not chunk:
                    break
                buf += chunk
                if b"\n" not in buf:
                    continue
                lines = buf.split(b"\n")
                buf = lines.pop()          # 最后一段可能是半行，留到下一片
                events += _scan_lines(lines, counts)
            if buf.strip():
                events += _scan_lines([buf], counts)
        else:                              # 万一不是 reader，退回逐行
            for line in reader:
                events += _scan_lines([line], counts)
        # stream_reader 遇到半帧只是提前 EOF，不报错——不能用它判断完整性。
        # 单独走一遍逐帧解：每帧 eof=True 才算完，最后一帧 eof=False 就是截断。
        # 代价是把文件再读一遍；单个会话文件都很小，值得。
        truncated = not frames_complete(path)
    finally:
        try:
            reader.close()
        except Exception:
            pass
        try:
            fobj.close()
        except Exception:
            pass
    return events, truncated


def _scan_lines(lines, counts):
    n = 0
    for raw in lines:
        raw = raw.strip()
        if not raw or not raw.startswith(b"{"):
            continue
        try:
            ev = json.loads(raw.decode("utf-8", "replace"))
        except Exception:
            continue
        n += 1
        if not isinstance(ev, dict) or ev.get("type") != "tool/call":
            continue
        name = tool_name_of(ev)
        if name:
            counts[name] = counts.get(name, 0) + 1
    return n


# ---------------------------------------------------------------- 我们的工具表

def repo_root():
    return Path(__file__).resolve().parents[1]


def load_our_tools():
    exe = repo_root() / "tmp" / "dshd.exe"
    if not exe.is_file():
        eprint("错误: 找不到 %s" % exe)
        eprint("先 go build -o tmp/dshd.exe ./cmd/dshd")
        sys.exit(2)
    try:
        proc = subprocess.run(
            [str(exe), "tools"],
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            cwd=str(repo_root()), timeout=120,
        )
    except Exception as exc:
        eprint("错误: 跑 %s tools 失败: %s" % (exe, exc))
        sys.exit(2)
    if proc.returncode != 0:
        eprint("错误: %s tools 退出码 %d" % (exe, proc.returncode))
        if proc.stderr:
            eprint(proc.stderr.strip()[:2000])
        sys.exit(2)
    out = (proc.stdout or "").strip()
    try:
        data = json.loads(out)
    except Exception as exc:
        eprint("错误: tools 的 stdout 不是 JSON: %s" % exc)
        eprint(out[:1000])
        sys.exit(2)
    if isinstance(data, dict):
        for key in ("tools", "data", "items"):
            if isinstance(data.get(key), list):
                data = data[key]
                break
    if not isinstance(data, list):
        eprint("错误: tools 的输出不是数组，实际是 %s" % type(data).__name__)
        sys.exit(2)
    names = set()
    for item in data:
        if isinstance(item, dict):
            n = item.get("name") or item.get("Name")
            if isinstance(n, str) and n:
                names.add(n)
        elif isinstance(item, str) and item:
            names.add(item)
    if not names:
        eprint("错误: tools 输出里一个 name 都没取到")
        sys.exit(2)
    return names


# ---------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser(description="数真实会话里的工具调用，找我们的工具缺口")
    ap.add_argument("--top", type=int, default=20, help="使用最多表显示前几行（默认 20）")
    ap.add_argument("--sessions", default=str(DEFAULT_SESSIONS), help="语料目录")
    args = ap.parse_args()
    setup_stdout()

    sessions_dir = Path(args.sessions)
    if not sessions_dir.is_dir():
        eprint("错误: 语料目录不存在: %s" % sessions_dir)
        return 2

    ours = load_our_tools()

    files = sorted(sessions_dir.rglob(SESSION_NAME))
    if not files:
        eprint("错误: %s 下一个 %s 都没有" % (sessions_dir, SESSION_NAME))
        return 2

    counts = {}
    ok_files = 0
    bad = []
    partial = []
    for path in files:
        try:
            _, truncated = scan_file(path, counts)
            ok_files += 1
            if truncated:
                partial.append(path)
        except Exception as exc:            # 坏文件跳过，不整体崩
            bad.append((path, exc))

    total_calls = sum(counts.values())
    ranked = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))
    missing = [(n, c) for n, c in ranked if n not in ours]

    top = max(1, args.top)
    print("=== 使用最多的工具（前 %d）===" % top)
    if not ranked:
        print("  （没有找到任何 tool/call 事件）")
    for name, cnt in ranked[:top]:
        mark = "✅" if name in ours else "❌ 缺"
        print("  %-22s %7d  %s" % (name, cnt, mark))

    print()
    print("=== 我们缺的（按次数）===")
    if not missing:
        print("  （没有缺口）")
    for name, cnt in missing:
        print("  %-22s %7d" % (name, cnt))

    print()
    if partial:
        print("注意：%d 个文件尾部不完整（已按读到的部分统计，数字可能偏小）" % len(partial))
        for path in partial[:3]:
            print("  - %s" % path.name)
    if bad:
        print("跳过 %d 个坏文件" % len(bad))
        for path, exc in bad[:5]:
            print("  - %s: %s" % (path.name, str(exc)[:160]))
        if len(bad) > 5:
            print("  ... 还有 %d 个" % (len(bad) - 5))
    print("共 %d 个会话、%d 次调用、%d 种工具；缺 %d 种"
          % (ok_files, total_calls, len(ranked), len(missing)))

    worth = [(n, c) for n, c in missing if c >= MISSING_THRESHOLD]
    if worth:
        print("还有值得做的：%s" % "、".join("%s(%d)" % (n, c) for n, c in worth[:10]))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
