# 写一个 wasm 工具插件

> 这份是**给插件作者**的。想知道宿主为什么这么设计，读 `docs/wasm-tool-abi.md`。
> 一句话：写两个导出函数、吃一段 JSON、吐一段 JSON，就这么多。

## 1. 最小可用的插件

```go
package main

import "unsafe"

// keep 必须留：宿主只拿到一个偏移，Go 的 GC 看不见"还有人在用这块内存"。
// 省掉它，宿主写 args 时那块内存可能已经没了。
var keep [][]byte

//go:wasmexport alloc
func alloc(n uint32) uint32 {
	b := make([]byte, n)
	keep = append(keep, b)
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

//go:wasmexport call
func call(ptr, n uint32) uint64 {
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), n)

	// 请求长这样：{"v":1,"tool":"echo","args":{"text":"你好"}}
	// 你要做的就是读 args、干活、把结果按下面的形状写回去。
	out := []byte(`{"v":1,"summary":"收到","text":"echo:你好","isError":false}`)

	keep = append(keep, out)
	return uint64(uint32(uintptr(unsafe.Pointer(&out[0]))))<<32 | uint64(len(out))
}

func main() {} // reactor 不跑它，但 c-shared 要求有 main
```

构建：

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o echo.wasm .
```

**必须 `-buildmode=c-shared`。** 默认 `go build` 出来的是 command 形态（只有 `_start`），
跑完 `main` 就 `proc_exit`，宿主调不到你的导出函数——装载时会明确报这个错。

## 2. 信封的两个方向

**宿主 → 你（写进 `alloc` 那块内存）：**

```json
{"v": 1, "tool": "echo", "args": {"text": "你好"}}
```

| 字段 | 说明 |
|---|---|
| `v` | 协议版本，只认 `1` |
| `tool` | 工具名。一个模块将来要提供多个工具时靠它区分 |
| `args` | 模型给的参数原样搬过来（是对象，不是字符串） |

**你 → 宿主（`call` 的返回值指向的那段）：**

```json
{"v": 1, "summary": "一行给人看", "text": "给模型看的正文", "isError": false, "hint": ""}
```

| 字段 | 必填 | 规矩 |
|---|---|---|
| `v` | 是 | 只认 `1` |
| `summary` | 是 | 一行动词短语，显示在终端上（`← echo [ok] 收到 你好`） |
| `text` | 是 | 模型看到的正文。可以很长，但**别指望它替你省 token**——宿主会自己截 |
| `isError` | 是 | **业务失败只能用这个表达** |
| `hint` | 否 | 失败时告诉模型「下一步怎么办」；不给你就只得到一句「失败了」 |

## 3. 三种"出错"，出口完全不一样

| 情况 | 你怎么表达 | 宿主怎么处理 | 模型看到 |
|---|---|---|---|
| 参数不对 / 业务失败 | 信封里 `"isError": true` + `hint` | 当**工具失败**：`Result{Hint}, err` | 能自己改参数重试 |
| 你的代码崩了（trap） | 不用表达，直接崩 | 当**插件崩溃**：提示「不要原样重试」 | 会换方法 |
| 返回的不是合法信封 | — | 当**协议错** | 告诉你"插件坏了" |

**别用 trap 表达业务失败。** 那是"插件内部崩了"的语义，宿主会告诉模型「不要重试」——
而参数写错恰恰是应该重试的。

## 4. manifest 里怎么声明

```toml
[[module]]
id               = "wasm-echo"
backend          = "wasm"
path             = "echo.wasm"          # 相对 manifest 所在目录
digest           = "<sha256>"           # 可选；给了就在启动时校验，内容被换过直接炸
provides         = ["tool"]
tool_name        = "echo"
tool_description = "把文本原样回显（调试用）"
tool_schema      = '{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'
tool_caps        = ["writes"]            # 可选；决定要不要审批
timeout_ms       = 10000                 # 可选；超时会把实例关掉再重建
```

两个必填容易被忽略：

- **`tool_description`**：模型靠它判断「什么时候该用这个工具」。缺了装配期直接报错——不是洁癖，是缺了它模型几乎不会正确调用。
- **`tool_schema`**：模型靠它拼参数。

## 5. 你能碰什么、不能碰什么

**不能**：文件系统（没有 preopen）、网络、环境变量、命令行参数。
这些不是"没开放"，是**宿主根本没给**。要读写文件就让工具去调别的手段，别指望 wasm 里能直接干。

**能力声明只在 manifest 里**（`tool_caps`），不从插件里读——审批看的是宿主信的那份声明，
而不是插件自称的。

## 6. 排查

```bash
dshd modules graph --manifest dsh.toml      # 看它有没有进装配、digest 前 12 位对不对
dshd tools --manifest dsh.toml              # 看工具名/描述/schema 有没有进表
dshd exec --tool echo --args '{"text":"hi"}'   # 不经过模型直接调一次
dshd doctor --manifest dsh.toml             # 体检里有一项专门看 wasm 模块装不装得起来
```

插件崩了的时候，错误里会带上**你自己打到 stderr 的那段**（宿主留了 4 KiB 环形缓冲），
所以 `fmt.Fprintln(os.Stderr, ...)` 是有用的排查手段。
