#!/usr/bin/env python3
"""生成 internal/wasmhost 的测试夹具 .wasm（手写二进制，不依赖任何工具链）。

为什么不拿 Go 编 wasip1 产物当夹具：
  - 那种产物 1.9 MB，而我们要验的只是"能装载、能调、能卸载"；
  - 它需要 Go 运行时先跑起来（`_initialize`），装载成功不等于能调用，
    测出来的是 wazero 对 Go 产物的支持，不是我们这层的能力。

手写一个 60 字节的模块：导出 add(i32,i32)->i32，等价于键盘上敲出来的 wasm。
生成物入库，测试只读它——测试不该依赖本机有没有 Go 工具链。
"""
import os

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

name = lambda s: uleb(len(s)) + s.encode()

body = bytes([0x00]) + bytes([0x20, 0x00]) + bytes([0x20, 0x01]) + bytes([0x6A]) + bytes([0x0B])

wasm = b"\0asm" + bytes([1, 0, 0, 0])
wasm += section(1, vec([bytes([0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F])]))      # type: (i32,i32)->i32
wasm += section(3, vec([uleb(0)]))                                          # func 0 : type 0
wasm += section(7, vec([bytes([name("add")[0]]) + name("add")[1:] + bytes([0x00]) + uleb(0)]))  # export add
wasm += section(10, vec([uleb(len(body)) + body]))                          # code

os.makedirs("internal/wasmhost/testdata", exist_ok=True)
out = "internal/wasmhost/testdata/fixture.wasm"
open(out, "wb").write(wasm)
print("生成", out, len(wasm), "字节")
