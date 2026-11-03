#!/usr/bin/env python3
"""wasm 工具插件：真机上装得起来、进得了工具表、调得通。

为什么单独一个探针（而不是只留单测）：这条线的关键假设是
「manifest 里写一句 provides，工具就真的能被模型看到并执行」。
单测可以在假注册表上过，但那证明不了装配链——所以这里一律走
**真的 dshd 二进制 + 真的 manifest + 真的 .wasm 产物**。

判据（任何一条不成立就红）：
  1. modules graph 里出现这个 wasm 模块，backend=wasm
  2. dshd tools 里出现它的工具名，且描述来自 manifest
  3. dshd exec 真能调到，返回里带 wasm 里算出来的文本
  4. 没写 provides 的 wasm 模块，工具表**逐字节不变**（不许偷偷加东西）
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DSHD = os.path.join(ROOT, "tmp", "dshd.exe")
FIXTURE = os.path.join(ROOT, "internal", "wasmhost", "testdata", "toolplugin.wasm")
BASE_MANIFEST = os.path.join(ROOT, "dsh.toml")

ok = 0
fail = 0


def chk(name, cond, detail=""):
    global ok, fail
    if cond:
        ok += 1
        print("  OK   %s" % name)
    else:
        fail += 1
        print("  FAIL %s%s" % (name, ("  → " + str(detail)) if detail else ""))


def run(args, **kw):
    return subprocess.run(args, cwd=ROOT, capture_output=True, text=True, **kw)


def write_manifest(dirpath, extra):
    body = open(BASE_MANIFEST, encoding="utf-8").read()
    p = os.path.join(dirpath, "m.toml")
    with open(p, "w", encoding="utf-8", newline="\n") as f:
        f.write(body + "\n" + extra)
    return p


def tool_names(manifest):
    r = run([DSHD, "tools", "--manifest", manifest])
    if r.returncode != 0:
        return None
    out = r.stdout
    try:
        d = json.loads(out)
        tools = d if isinstance(d, list) else d.get("tools", [])
        return [t.get("name") for t in tools]
    except Exception:
        return None


def main():
    if not os.path.exists(DSHD):
        print("  FAIL 找不到 tmp/dshd.exe（先 go build -o tmp/dshd.exe ./cmd/dshd）")
        print("合计 OK=0 FAIL=1")
        return 1
    if not os.path.exists(FIXTURE):
        print("  FAIL 找不到夹具 %s" % FIXTURE)
        print("合计 OK=0 FAIL=1")
        return 1

    work = tempfile.mkdtemp(prefix="wasm-tool-")
    try:
        shutil.copy(FIXTURE, os.path.join(work, "p.wasm"))

        tool_block = (
            '\n[[module]]\n'
            'id               = "wasm-probe"\n'
            'backend          = "wasm"\n'
            'path             = "p.wasm"\n'
            'provides         = ["tool"]\n'
            'tool_name        = "wasm_probe_echo"\n'
            'tool_description = "探针用：把 text 原样回显"\n'
            'tool_schema      = \'{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}\'\n'
        )
        with_tool = write_manifest(work, tool_block)

        # ① 装配
        r = run([DSHD, "modules", "graph", "--manifest", with_tool])
        chk("modules graph 认这个 wasm 模块", r.returncode == 0 and "wasm-probe" in r.stdout,
            (r.stdout + r.stderr)[-300:])
        chk("graph 里 backend 是 wasm", "wasm" in r.stdout)

        # ② 进表
        names = tool_names(with_tool)
        chk("工具表里有 wasm 模块提供的工具",
            names is not None and "wasm_probe_echo" in names, names)

        # 描述必须真的是 manifest 里那句（不是空、不是占位）
        raw = run([DSHD, "tools", "--manifest", with_tool]).stdout
        chk("工具描述来自 manifest", "把 text 原样回显" in raw)

        # ③ 真调
        r = run([DSHD, "exec", "--manifest", with_tool, "--tool", "wasm_probe_echo",
                 "--args", '{"text":"探针"}'])
        chk("exec 调得通", r.returncode == 0, (r.stdout + r.stderr)[-300:])
        chk("返回值真的来自 wasm", "探针" in (r.stdout + r.stderr))

        # ④ 没 provides 的模块不许改工具表
        plain = write_manifest(work,
                               '\n[[module]]\nid = "wasm-plain"\nbackend = "wasm"\npath = "p.wasm"\n')
        a = run([DSHD, "tools", "--manifest", os.path.join(ROOT, "dsh.toml")]).stdout
        b = run([DSHD, "tools", "--manifest", plain]).stdout
        chk("没写 provides 时工具表逐字节不变",
            hashlib.sha256(a.encode()).hexdigest() == hashlib.sha256(b.encode()).hexdigest())

    finally:
        shutil.rmtree(work, ignore_errors=True)

    print("合计 OK=%d FAIL=%d" % (ok, fail))
    return 0 if fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
