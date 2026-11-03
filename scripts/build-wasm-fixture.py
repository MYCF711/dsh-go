#!/usr/bin/env python3
"""生成 internal/wasmhost 的两个测试夹具 .wasm。

两个产物，用途不同，缺一不可：

  1. testdata/fixture.wasm —— 手写的 60 字节**纯 wasm**（不依赖任何工具链）。
     add(i32,i32)->i32：验「装载 + 调用 + 返回值」；
     spin()->()：死循环，自己不会结束，用来验「ctx 取消能真的把执行打断」
     （wazero 的 CloseOnContextDone 在循环回边上检查 ctx）。

  2. testdata/toolplugin.wasm —— Go 源码夹具（testdata/toolplugin/main.go）
     用 `-buildmode=c-shared` 编出来的 **reactor**：导出 _initialize / alloc / call，
     实现 docs/wasm-tool-abi.md §3 的工具 ABI。
     它是唯一能同时钉住「_initialize 真跑过」和「ABI 往返」的夹具，
     所以产物必须入库；**测试只读产物，不在 go test 里现场编译**——
     那会把「这台机器有没有 Go 工具链 / wasip1 能不能编」变成测试前提。

为什么不能只留手写那个：它没有 _initialize、没有 Go 运行时，
测不出「宿主必须先跑 _initialize 才能调导出」这条约定——而那正是上一轮
spike 的根因（docs/wasm-tool-abi.md §1.3：少跑一个函数，报出来的是 unreachable）。

产物入库，两个都按进制文件处理（.gitattributes: *.wasm binary）。
"""
import os
import shutil
import subprocess
import sys

# 仓库根 = 本脚本所在目录的上一级。不按 cwd 算：从任何目录跑都产出到同一个地方。
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTDATA = os.path.join(ROOT, "internal", "wasmhost", "testdata")


def uleb(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        out.append(b | (0x80 if n else 0))
        if not n:
            return bytes(out)


def section(sid, payload):
    return bytes([sid]) + uleb(len(payload)) + payload


def vec(items):
    return uleb(len(items)) + b"".join(items)


def name(s):
    return uleb(len(s)) + s.encode()


def write_plain_fixture():
    """手写一个 60 字节的模块：导出 add / spin。"""
    # 函数 0：add(i32,i32)->i32
    #   local.get 0 ; local.get 1 ; i32.add ; end
    add_body = bytes([0x00]) + bytes([0x20, 0x00]) + bytes([0x20, 0x01]) + bytes([0x6A]) + bytes([0x0B])

    # 函数 1：spin()->()
    #   loop ; br 0 ; end ; end
    # 没有调用、没有内存、没有 sleep —— 最纯粹的「自己不会结束」。
    # 打断只能靠 wazero 在每个循环回边上查 ctx（CloseOnContextDone），
    # 这正是 `dshd wasm call --timeout` 与 TestCallTimeoutInterrupts 要证明的事。
    spin_body = bytes([0x00, 0x03, 0x40, 0x0C, 0x00, 0x0B, 0x0B])

    wasm = b"\0asm" + bytes([1, 0, 0, 0])
    # type 0: (i32,i32)->i32；type 1: ()->()
    wasm += section(1, vec([
        bytes([0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F]),
        bytes([0x60, 0x00, 0x00]),
    ]))
    wasm += section(3, vec([uleb(0), uleb(1)]))                             # func 0/1 分别用 type 0/1
    wasm += section(7, vec([                                               # export add, spin
        name("add") + bytes([0x00]) + uleb(0),
        name("spin") + bytes([0x00]) + uleb(1),
    ]))
    wasm += section(10, vec([                                              # code
        uleb(len(add_body)) + add_body,
        uleb(len(spin_body)) + spin_body,
    ]))

    os.makedirs(TESTDATA, exist_ok=True)
    out = os.path.join(TESTDATA, "fixture.wasm")
    with open(out, "wb") as f:
        f.write(wasm)
    return out, len(wasm)


def build_tool_plugin():
    """把 Go 源码夹具编成 reactor（-buildmode=c-shared）。

    为什么显式 `-buildvcs=false`（`-trimpath` 在 go1.27.3 上**并没有**关掉它）：
    go 会把仓库的 VCS 身份写进 main 包的 build info —— 实测产物里有
    `build vcs=git` / `vcs.revision=<HEAD>` / `vcs.modified=true`。
    后果是同一个源文件在不同 commit 上编出来的 .wasm **字节不同**：
    本仓真机踩过——同一份源码在 HEAD=553e148 时是 ec3391fa…，别人提交后
    HEAD=b55853a 再编同一份源码就变成 f62023ad…（大小一样、只差 build info）。
    .wasm 是入库夹具，manifest 里的 digest 又按内容钉死，那就不能让它随
    仓库指针漂：关掉 VCS 之后产物只取决于源码 + 工具链。
    """
    go = shutil.which("go")
    if go is None:
        sys.exit("找不到 go：这个夹具必须用 Go 工具链编（PATH 里要有 go，或先 export PATH=\"/c/Program Files/Go/bin:$PATH\"）")

    src = os.path.join(TESTDATA, "toolplugin")
    out = os.path.join(TESTDATA, "toolplugin.wasm")
    cmd = [go, "build", "-buildmode=c-shared", "-trimpath", "-buildvcs=false", "-o", out, "."]
    env = dict(os.environ)
    env.update({"GOOS": "wasip1", "GOARCH": "wasm", "CGO_ENABLED": "0"})
    print("+ (cd %s && %s)" % (os.path.relpath(src, ROOT), " ".join(cmd[1:])))
    subprocess.run(cmd, cwd=src, env=env, check=True)
    return out, os.path.getsize(out)


def main():
    p1, n1 = write_plain_fixture()
    print("生成 %s %d 字节（手写纯 wasm：add / spin）" % (os.path.relpath(p1, ROOT), n1))
    p2, n2 = build_tool_plugin()
    print("生成 %s %d 字节（Go reactor：_initialize / alloc / call）" % (os.path.relpath(p2, ROOT), n2))


if __name__ == "__main__":
    main()
