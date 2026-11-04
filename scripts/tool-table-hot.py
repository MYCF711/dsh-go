#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tool-table-hot.py —— 从真实会话语料重算 lazy 模式的常驻集（前 N 名）。

为什么要有这个脚本：常驻哪 30 个工具**不能手抄**。手抄的名单一旦和语料漂了，
没人知道是该改名单还是该信语料；而且新工具加进来时，手抄名单不会自己更新。
这里把"从语料算"这件事固定下来，结论落成一张表（docs/tool-table-hot.md），
Go 侧只是把同一份结论以字面量嵌进去（--go 直接生成那段）。

用法：
    python scripts/tool-table-hot.py                 # 打印排名，并核对 Go 里的常驻集
    python scripts/tool-table-hot.py --go            # 只输出 defaultHotTools 的字面量
    python scripts/tool-table-hot.py --write docs/tool-table-hot.md
    python scripts/tool-table-hot.py --top 35        # 换 N（Go 侧默认 30）
    python scripts/tool-table-hot.py --sessions DIR  # 换语料目录

只读语料（只 open 'rb'），绝不写会话文件。退出码：
    0 = Go 里的名单与本次重算一致
    1 = 不一致（打印差异）——换语料后这是正常现象，按提示重跑 --go
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_SESSIONS = Path("D:/DeepSeek Harness/home/sessions")
GO_FILE = ROOT / "cmd" / "dshd" / "tool_lazy.go"
MARK_BEGIN = "var defaultHotTools = []string{"


def setup_stdout():
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass


def eprint(*a):
    print(*a, file=sys.stderr)


def load_gap():
    """复用 tool-gap.py 的解码器：一份语料只该有一种读法。"""
    import importlib.util

    spec = importlib.util.spec_from_file_location("tool_gap", ROOT / "scripts" / "tool-gap.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def scan(sessions: Path, gap):
    """扫全部会话，返回 (counts, files, truncated_files)。"""
    counts, files, trunc = {}, 0, 0
    for dirpath, _, names in os.walk(sessions):
        for n in names:
            if not n.endswith(".zstd"):
                continue
            path = Path(dirpath) / n      # 必须是 Path：frames_complete 会调 read_bytes
            try:
                _, t = gap.scan_file(path, counts)
            except Exception as exc:      # 坏文件不该让整次统计失败
                eprint(f"跳过 {path}: {exc}")
                continue
            files += 1
            trunc += 1 if t else 0
    return counts, files, trunc


def our_tools(exe: Path):
    """本仓全部工具（名字 → 完整条目），走真实 registry，不用手抄名单。"""
    proc = subprocess.run(
        [str(exe), "tools", "--manifest", "dsh.toml"],
        capture_output=True, text=True, encoding="utf-8", errors="replace",
        cwd=str(ROOT), timeout=180,
    )
    if proc.returncode != 0:
        eprint(proc.stderr)
        sys.exit(f"错误: {exe} tools 失败（exit={proc.returncode}）")
    return json.loads(proc.stdout)


def parse_hot_from_go() -> list:
    """从 Go 源里读回当前常驻集，用来核对（不执行 Go）。"""
    text = GO_FILE.read_text(encoding="utf-8")
    i = text.find(MARK_BEGIN)
    if i < 0:
        return []
    j = text.find("\n}", i)
    body = text[i + len(MARK_BEGIN):j]
    out = []
    for line in body.splitlines():
        line = line.strip()
        if line.startswith('"') and line.endswith('",'):
            out.append(line[1:-2])
    return out


def wire_bytes(entry) -> int:
    """真正发给模型的字节：名字 + 描述 + 参数（与 goto 侧 wireBytesOf 同一定义）。"""
    return len(json.dumps({
        "name": entry["name"],
        "description": entry["description"],
        "parameters": entry["parameters"],
    }, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))


def main():
    setup_stdout()
    ap = argparse.ArgumentParser()
    ap.add_argument("--sessions", type=Path, default=DEFAULT_SESSIONS)
    ap.add_argument("--top", type=int, default=30)
    ap.add_argument("--exe", type=Path, default=ROOT / "tmp" / "dshd.exe")
    ap.add_argument("--go", action="store_true", help="只输出 Go 字面量")
    ap.add_argument("--write", type=Path, default=None, help="把结论表写到这个路径")
    a = ap.parse_args()

    if not a.exe.is_file():
        sys.exit("先 go build -o tmp/dshd.exe ./cmd/dshd")
    if not a.sessions.is_dir():
        sys.exit(f"语料目录不存在: {a.sessions}")

    gap = load_gap()
    counts, files, trunc = scan(a.sessions, gap)
    table = our_tools(a.exe)
    ours = {e["name"]: e for e in table}
    total = sum(counts.values())
    rows = sorted(((n, c) for n, c in counts.items() if n in ours), key=lambda kv: (-kv[1], kv[0]))
    if len(rows) < a.top:
        sys.exit(f"语料里只命中 {len(rows)} 个我们的工具，不够取前 {a.top} 名")

    hot = [n for n, _ in rows[: a.top]]
    if a.go:
        print(MARK_BEGIN)
        for n in hot:
            print(f'\t"{n}",')
        print("}")
        return 0 if a.top == 30 else 0

    hidden = [n for n in sorted(ours) if n not in set(hot)]
    hot_bytes = sum(wire_bytes(ours[n]) for n in hot)
    hidden_bytes = sum(wire_bytes(ours[n]) for n in hidden)
    full_bytes = hot_bytes + hidden_bytes
    cum = 0
    print(f"语料 {files} 个会话（尾部不完整 {trunc}），{total} 次 tool/call")
    print(f"我们的 {len(ours)} 个工具里命中 {len(rows)} 个；常驻 {a.top} 个，表外 {len(hidden)} 个")
    print()
    print(f"{'#':>3} {'工具':32s} {'调用':>8} {'占比':>7} {'累计':>7}")
    for i, (n, c) in enumerate(rows, 1):
        cum += c
        mark = "常驻" if i <= a.top else "表外"
        print(f"{i:3d} {n:32s} {c:8d} {100*c/total:6.2f}% {100*cum/total:6.2f}%  {mark}")
    never = sorted(set(ours) - set(counts))
    print(f"\n语料里一次没出现过（{len(never)} 个）：{', '.join(never)}")
    print(f"\n表字节（名字+描述+参数）：全表 {full_bytes}，常驻 {hot_bytes}，表外 {hidden_bytes}")
    hot_cum = cum_of(rows, a.top)
    print(f"常驻 {a.top} 个覆盖 {100*hot_cum/total:.2f}% 的调用；"
          f"表外 {len(hidden)} 个只占 {100*(total-hot_cum)/total:.2f}%")

    if a.write:
        write_doc(a.write, rows, hot, hidden, ours, files, trunc, total, a.top,
                  hot_bytes, hidden_bytes)
        print(f"\n结论表已写入 {a.write}", file=sys.stderr)

    # 核对 Go 里的名单
    if a.top == 30:
        cur = parse_hot_from_go()
        if cur == hot:
            print("\n核对：Go 里的 defaultHotTools 与本次重算一致。")
        else:
            print("\n核对：**不一致**。重跑 `python scripts/tool-table-hot.py --go` 替换 "
                  "cmd/dshd/tool_lazy.go 里的 defaultHotTools。")
            only_go = [n for n in cur if n not in hot]
            only_new = [n for n in hot if n not in cur]
            if only_go:
                print("  只在 Go 里:", ", ".join(only_go))
            if only_new:
                print("  只在语料里:", ", ".join(only_new))
            return 1
    return 0


def cum_of(rows, upto):
    return sum(c for _, c in rows[:upto])


def write_doc(path: Path, rows, hot, hidden, ours, files, trunc, total, top,
              hot_bytes, hidden_bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = []
    lines.append("# 工具表常驻集（lazy 模式的前 N 名）\n")
    lines.append("> 本文件由 `python scripts/tool-table-hot.py --write docs/tool-table-hot.md` 生成，**别手改**。\n")
    lines.append(f"语料：`D:/DeepSeek Harness/home/sessions`，{files} 个会话"
                 f"（尾部不完整 {trunc}），{total} 次 `tool/call`。\n")
    lines.append(f"常驻集取我们自己的工具里调用次数前 {top} 名——"
                 f"覆盖 {100*cum_of(rows,top)/total:.2f}% 的调用；"
                 f"表外 {len(hidden)} 个只占 {100*(total-cum_of(rows,top))/total:.2f}%。\n")
    lines.append("| # | 工具 | 调用次数 | 占比 | 累计 | 去留 |")
    lines.append("|---:|---|---:|---:|---:|---|")
    cum = 0
    for i, (n, c) in enumerate(rows, 1):
        cum += c
        lines.append(f"| {i} | `{n}` | {c} | {100*c/total:.2f}% | {100*cum/total:.2f}% | "
                     f"{'常驻' if i <= top else '表外'} |")
    lines.append(f"\n表外（{len(hidden)} 个，只在系统提示的目录里留名字+一句话）：\n")
    for n in hidden:
        lines.append(f"- `{n}`")
    lines.append(f"\n表字节（名字+描述+参数，真正发给模型的那部分）：全表 {hot_bytes+hidden_bytes}，"
                 f"常驻 {hot_bytes}，表外 {hidden_bytes}。")
    lines.append("")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


if __name__ == "__main__":
    sys.exit(main())
