#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""量每个工具的参数字节数与描述大小，按参数字节降序打一张表。

用量:
    python scripts/tool-weight.py

为什么要有这个脚本:
    工具表已经顶到 32 KB 警戒线。要决定削谁，得先知道**字节到底长在谁身上**——
    params_bytes 是 parameters 那段 JSON 的体积（大头通常是参数说明的"键壳"），
    desc_bytes / desc_runes 是工具自身 description 的字节与字符数。
    中文一个字 3 字节，所以 desc_bytes 与 desc_runes 必须分开看，
    否则会把"描述很长"和"描述里全是中文"混为一谈。

口径:
    params_bytes 用 json.dumps(..., ensure_ascii=False, separators=(",", ":"))，
    即紧凑、不转义中文，贴近它真正进模型上下文时的形状（与 Go 的 json.Marshal 一致）。

退出码:
    0  正常（至少量到一个非空工具表）
    2  拿不到结构化输出 / 工具表为空（绝不打印空表当成功）
"""

import json
import subprocess
import sys

GO_CMD = ["go", "run", "./cmd/dshd"]

# 依次尝试的命令后缀。第一个不认 --json 就会走第二个。
ATTEMPTS = [
    (["tools", "--json"], "tools --json"),
    (["tools"], "tools"),
]


def parse_json_output(text):
    """从 stdout 里取出 JSON 值；前面混了别的东西也能容忍。失败抛 ValueError。"""
    s = text.strip()
    if not s:
        raise ValueError("输出为空")

    decoder = json.JSONDecoder()
    # 先试整段直接解析，再试从第一个 '[' 或 '{' 开始
    for candidate in (s, s[s.find("["):] if "[" in s else "", s[s.find("{"):] if "{" in s else ""):
        if not candidate:
            continue
        try:
            value, _ = decoder.raw_decode(candidate)
            return value
        except ValueError:
            continue
    raise ValueError("整段不是可解析的 JSON")


def load_tools():
    """返回 (tools, used_desc)。拿不到就抛 SystemExit(2) 并说清试过什么。"""
    tried = []

    for args, label in ATTEMPTS:
        cmd = GO_CMD + args
        try:
            proc = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",
            )
        except OSError as exc:
            tried.append("%s -> 无法执行: %s" % (label, exc))
            continue

        if proc.returncode != 0:
            # 取 stderr 的第一条非空行，别把整段 usage 甩出来
            first = ""
            for line in (proc.stderr or "").splitlines():
                if line.strip():
                    first = line.strip()
                    break
            tried.append(
                "%s -> 退出码 %d%s"
                % (label, proc.returncode, (": " + first) if first else "（无 stderr）")
            )
            continue

        try:
            value = parse_json_output(proc.stdout)
        except ValueError as exc:
            tried.append("%s -> 退出码 0 但 %s" % (label, exc))
            continue

        if not isinstance(value, list):
            tried.append("%s -> 顶层是 %s，不是工具数组" % (label, type(value).__name__))
            continue

        tools = [t for t in value if isinstance(t, dict) and t.get("name")]
        if not tools:
            tried.append("%s -> 解析出 0 个带 name 的工具" % label)
            continue

        return tools, label

    sys.stderr.write("错误: 拿不到工具表的结构化输出。试过:\n")
    for line in tried:
        sys.stderr.write("  - %s\n" % line)
    sys.stderr.write(
        "提示: 在仓库根目录跑，并确认 `go run ./cmd/dshd tools` 能输出 JSON。\n"
    )
    raise SystemExit(2)


def measure(tool):
    name = str(tool.get("name", ""))
    desc = tool.get("description") or ""
    params = tool.get("parameters")
    if params is None:
        params = {}

    params_json = json.dumps(params, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    return {
        "name": name,
        "params_bytes": len(params_json.encode("utf-8")),
        "desc_bytes": len(desc.encode("utf-8")),
        "desc_runes": len(desc),
    }


def main():
    tools, used = load_tools()
    rows = [measure(t) for t in tools]

    # 降序；同样大按名字，保证多次跑结果稳定
    rows.sort(key=lambda r: (-r["params_bytes"], r["name"]))

    w_p = max([len("PARAMS")] + [len(str(r["params_bytes"])) for r in rows])
    w_d = max([len("DESC")] + [len(str(r["desc_bytes"])) for r in rows])
    w_r = max([len("RUNES")] + [len(str(r["desc_runes"])) for r in rows])

    sys.stdout.write(
        "%*s  %*s  %*s  %s\n" % (w_p, "PARAMS", w_d, "DESC", w_r, "RUNES", "TOOL")
    )
    for r in rows:
        sys.stdout.write(
            "%*d  %*d  %*d  %s\n"
            % (
                w_p,
                r["params_bytes"],
                w_d,
                r["desc_bytes"],
                w_r,
                r["desc_runes"],
                r["name"],
            )
        )

    total_p = sum(r["params_bytes"] for r in rows)
    total_d = sum(r["desc_bytes"] for r in rows)
    sys.stdout.write(
        "合计 %d 个工具，params %d 字节，desc %d 字节\n" % (len(rows), total_p, total_d)
    )
    sys.stdout.write("（数据来源: go run ./cmd/dshd %s）\n" % used)
    return 0


if __name__ == "__main__":
    sys.exit(main())
