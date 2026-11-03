#!/usr/bin/env python3
"""生成 internal/wasmhost 的测试夹具 .wasm（手写二进制，不依赖任何工具链）。

为什么不拿 Go 编 wasip1 产物当夹具：
  - 那种产物 1.9 MB，而我们要验的只是"能装载、能调、能卸载"；
  - 它需要 Go 运行时先跑起来（`_initialize`），装载成功不等于能调用，
    测出来的是 wazero 对 Go 产物的支持，不是我们这层的能力。

手写一个 60 字节的模块，导出两个函数，等价于键盘上敲出来的 wasm：
  - add(i32,i32)->i32：能算，用来验「装载 + 调用 + 返回值」；
  - spin()->()：死循环，自己不会返回，用来验「ctx 取消能真的把执行打断」
    （wazero 的 CloseOnContextDone 在循环回边上检查 ctx）。

注意：两个都是**被调用**的库函数，没有 `_start`；装载不等于运行。
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
wasm += section(3, vec([uleb(0), uleb(1)]))                                 # func 0/1 分别用 type 0/1
wasm += section(7, vec([                                                   # export add, spin
    name("add") + bytes([0x00]) + uleb(0),
    name("spin") + bytes([0x00]) + uleb(1),
]))
wasm += section(10, vec([                                                   # code
    uleb(len(add_body)) + add_body,
    uleb(len(spin_body)) + spin_body,
]))

os.makedirs("internal/wasmhost/testdata", exist_ok=True)
out = "internal/wasmhost/testdata/fixture.wasm"
open(out, "wb").write(wasm)
print("生成", out, len(wasm), "字节")
