# 写一个 wasm 工具插件

> 这份是**给插件作者**的。想知道宿主为什么这么设计，读 `docs/wasm-tool-abi.md`。
> 一句话：写两个导出函数、吃一段 JSON、吐一段 JSON，就这么多。
> 一个 `.wasm` 可以挂**多个**工具（§5），也可以只挂一个（§1）。

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
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -buildvcs=false -o echo.wasm .
```

**必须 `-buildmode=c-shared`。** 默认 `go build` 出来的是 command 形态（只有 `_start`），
跑完 `main` 就 `proc_exit`，宿主调不到你的导出函数——装载时会明确报这个错。

**建议 `-buildvcs=false`。** 不加的话 go 会把仓库的 `vcs.revision` / `vcs.modified`
写进产物：同一份源码换个 commit 就编出不同字节。插件包一般按内容摘要发布/校验
（宿主 manifest 里的 `digest` 就是 sha256），产物会随仓库指针漂是纯粹的麻烦。

> **这段样例是验过的**（2026-11-04）：照抄上面的代码 + `-buildmode=c-shared` 编出来
> 1,908,996 字节的 `guide.wasm`，写进 manifest 后 `dshd exec --tool guide_echo --args '{"text":"hi"}'`
> 真的返回 `Summary: 收到 / Text: echo:你好`。手册里的代码不是"看着应该行"。

## 2. 信封的两个方向

**宿主 → 你（写进 `alloc` 那块内存）：**

```json
{"v": 1, "tool": "echo", "args": {"text": "你好"}}
```

| 字段 | 说明 |
|---|---|
| `v` | 协议版本，只认 `1` |
| `tool` | **这次是哪个工具**。宿主每次调用都会写，一个模块挂多个工具时靠它分流（§5）；一个模块只有一个工具时它就是你那个工具名 |
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

### 4.1 一个工具（最省事，也是既有插件的写法）

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

### 4.2 同一个 `.wasm` 里挂多个工具

```toml
[[module]]
id       = "wasm-text"
backend  = "wasm"
path     = "text.wasm"
provides = ["tool:echo", "tool:upper"]   # 一个工具一个键

[[module.tool]]
name        = "echo"
description = "把文本原样回显"
schema      = '{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'
caps        = []

[[module.tool]]
name        = "upper"
description = "把文本里的 ASCII 字母转成大写"
schema      = '{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'
timeout_ms  = 5000
```

四条规矩（写错了都是**启动时**报错，不会等到运行期）：

1. **两套写法不许混用**：模块级写了 `tool_name` 那几行，就不能再写 `[[module.tool]]`，反之亦然。宿主不会替你猜哪个优先。
2. **模块级字段必须写在第一节 `[[module.tool]]` 之前**。TOML 里写在后面就属于那个子表了，宿主看不到它（所以也不会报"混写"）。
3. **`provides` 与工具逐个对齐**：`[[module.tool]]` 有几个工具就写几个 `tool:<工具名>`，名字一个都不能错。只有一个工具时可以用缩写 `provides = ["tool"]`（全署只能出现一次）；多个工具还写 `["tool"]` 会直接报错。
4. **工具名在同一个模块里必须唯一**，跨模块也不能撞（撞了会在注册工具表时报错）。

两个必填容易被忽略：

- **`tool_description` / `description`**：模型靠它判断「什么时候该用这个工具」。缺了装配期直接报错——不是洁癖，是缺了它模型几乎不会正确调用。
- **`tool_schema` / `schema`**：模型靠它拼参数。

## 5. 一个模块写多个工具

客人这边的全部工作就是**看 `tool` 字段分流**：

```go
switch req.Tool {
case "upper":
    return writeReply(upperReply(args.Text))
case "echo":
    return writeReply(echoReply(args.Text))
default:
    // 兜底要写：漏了它，一个改名/拼错的工具会安静地跑顺手的那条分支。
    return writeReply(reply{V: 1, Summary: "不认识的工具", Text: req.Tool, IsError: true,
        Hint: "宿主要求的工具名不在这个插件里：报告给用户，别换工具名重试"})
}
```

几条经验：

- `writeReply` 就是 §1 里那段「把结果放进线性内存、返回 `ptr<<32|len`」的写法，只是抽成了函数；完整的两个工具见夹具 `internal/wasmhost/testdata/toolplugin/main.go`。
- **两个工具的输出必须能区分**。验收 `tool` 字段有没有被真正路由，判据是"同一份 args 走两个工具，结果不同"；只断言"没报错"的测试在路由坏掉时也全绿——因为错误的路由通常表现为**成功返回别人的结果**。
- **宿主不会替你校验分流**：客人完全可以无视 `tool` 字段，宿主看不出区别。这是你自己的责任。
- **别为了多工具去复制 `.wasm` 文件**。同一个模块里的多个工具共用一份编译产物和一个实例，`tool` 字段就是为这件事留的；复制成 N 个模块（N 份 4.4 MB、N 次 `_initialize`）是退回上一版的做法。
- 一个实例同一时刻只跑一个 `call`：同一个模块上的多个工具是**串行**的，别指望它们并行。
- 每个工具的名字/描述/schema/caps **都以 manifest 为准**，你在信封里说什么都不影响模型看到什么。

参考实现：`internal/wasmhost/testdata/toolplugin/main.go`（`echo` 与 `upper` 两个工具，按 `req.Tool` 分流）。

## 6. 你能碰什么、不能碰什么

**不能**：文件系统（没有 preopen）、网络、环境变量、命令行参数。
这些不是"没开放"，是**宿主根本没给**。要读写文件就让工具去调别的手段，别指望 wasm 里能直接干。

**能力声明只在 manifest 里**（`caps` / `tool_caps`），不从插件里读——审批看的是宿主信的那份声明，
而不是插件自称的。

## 7. 排查

```bash
dshd modules graph --manifest dsh.toml      # 看它有没有进装配、digest 前 12 位对不对、TOOL 列有几个工具
dshd tools --manifest dsh.toml              # 看工具名/描述/schema 有没有进表（多工具就多行）
dshd exec --tool echo  --args '{"text":"hi"}'   # 不经过模型直接调一次
dshd exec --tool upper --args '{"text":"hi"}'   # 另一个工具，结果应当不同
dshd doctor --manifest dsh.toml             # 体检里有一项专门看 wasm 模块装不装得起来
```

`modules graph` 的 wasm 小节里，一个模块挂多个工具时 TOOL 列是**逗号分隔的一串**：

```
MODULE                 BACKEND   DIGEST       EXPORTS            TOOL                   PATH
wasm-text              wasm      ec3391fa4cea _initialize,alloc,call echo,upper             D:\…\text.wasm
```

插件崩了的时候，错误里会带上**你自己打到 stderr 的那段**（宿主留了 4 KiB 环形缓冲），
所以 `fmt.Fprintln(os.Stderr, ...)` 是有用的排查手段。
