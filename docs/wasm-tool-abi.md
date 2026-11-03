# wasm 工具 ABI 技术验证（spike）

- 日期：2026-11-04
- 基线：`ade4b00`（M334）+ 工作树；`internal/wasmhost/` 与 `cmd/dshd/wasmload.go` 本轮**无改动**（`git status` 里没有它们）。
- 目标：回答「wasm 里的一段逻辑，能不能变成 dsh 的一个工具」。**只做验证，不做落地**。
- 全部产物与脚本在 `tmp/abispike/`（gitignored），完整转录在 `tmp/abispike/out/evidence.txt`。下面的每一段输出都是从那里原样摘的，没有重跑、没有润色。
- 环境：`go1.27.3 windows/amd64`、`wazero v1.12.0`、`tinygo 0.44.0 (go1.27.3, LLVM 22.1.4)`、`binaryen version_133`。

---

## 0. 结论（先看这张表）

| 问题 | 结论 | 一句话证据 |
|---|---|---|
| 1. Go 产出的 wasip1 模块，导出函数能不能被 wazero 调？ | **能，但有两个硬前提**：必须 `-buildmode=c-shared`，且宿主必须用 `WithStartFunctions("_initialize")` 跑过 `_initialize` | 同一份源码两种构建：`exe` 构建调 `add(2,3)` 报 `runtime.notInitialized()` → `unreachable`；`c-shared` 构建 + `_initialize` 得到 `[5]` |
| 2. TinyGo 能不能当候选？ | **能，本机可装可跑**，同一套 ABI 通吃；代价是多两套外部二进制（TinyGo 180 MB + Binaryen 111 MB 下载） | `tinygo build -buildmode=c-shared -target=wasi` + `_initialize` → `add(2,3)=[5]`、ABI 往返成功 |
| 3. 工具 ABI 怎么设计？ | 见 §3：`alloc(len)->ptr` + `call(ptr,len)->ptr<<32\|len`，args/结果都是线性内存里的一段 JSON，已按草案真跑通 | `ABI: 写入 17 字节到 2957312；call 返回 ptr=3047552 len=64；读回 = {"summary":"收到 你好",...}` |

**推荐路线**：宿主侧固定在 wazero + 两个导出（§3），客人侧**默认用 Go `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`**；TinyGo 作为同一 ABI 下的可选产出，切换客人编译器**不需要改宿主**。理由与代价见 §4，为什么不选另外三条见 §5。

之前那次「调用时报 unreachable、原因没查清」的**根因已查明**（§1.3）：Go 在每个 wasmexport 包装函数开头插了 `if SP == 0 { runtime.notInitialized() }`，Go 运行时没起来就 abort；而 `wasmhost.Load` 恰好把客人 stderr 丢掉了（`wasmhost.go:81` 的 `WithStderr(nil)`），Go 自己打印的那句 `\tcall _initialize first` 被吃掉了。**不是 wazero 不支持 Go 产物，是少跑了一个函数、又看不见提示。**

---

## 1. 问题一：Go 产出的 wasip1 模块，导出函数到底能不能被 wazero 调？

### 1.1 复现装置（临时代码，全部在 `tmp/`）

- 客人：`tmp/abispike/guest/`（独立 module，`//go:wasmexport add/alloc/call/boom/spin`）
- 宿主探针：`tmp/abispike/host/`（同一个 wazero v1.12.0，只做「装载 → 按 mode 决定跑不跑 start → 调导出」）
- 复现脚本：`tmp/abispike/run-evidence.sh`，转录：`tmp/abispike/out/evidence.txt`

```bash
export GOCACHE=/tmp/gocache-abi          # 私有缓存，避免并发进程互删
cd tmp/abispike/guest
GOOS=wasip1 GOARCH=wasm go build -o ../out/guest-exe.wasm .                    # 4,464,676 B
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../out/guest-cshared.wasm .   # 4,464,916 B
```

两种构建的导出表（这就是答案的第一半）：

| 构建 | 导出 |
|---|---|
| `go build`（默认 exe） | `[_start add alloc boom call spin]` |
| `go build -buildmode=c-shared` | `[_initialize add alloc boom call spin]` |

`go help buildmode` 的原话（本机 `go1.27.3`）：

```
-buildmode=c-shared
	Build the listed main package, plus all packages it imports,
	into a C shared library. The only callable symbols will
	be those functions exported using a cgo //export comment.
	On wasip1, this mode builds it to a WASI reactor/library,
	of which the callable symbols are those functions exported
	using a //go:wasmexport directive. Requires exactly one
	main package to be listed.
```

### 1.2 三条路，逐个真跑

**(a) 直接 `InstantiateModule` 后调（不跑任何 start）**

```
$ ./out/host.exe -wasm ./out/guest-exe.wasm -mode noinit -fn add
导出函数: [_start add alloc boom call spin]
调用 add([2 3]) 失败: "wasm error: unreachable\nwasm stack trace:\n\t.runtime.abort(i32) i32\n\t.runtime.notInitialized()\n\t.add(i32,i32) i32"
```

同一个探针，只是把客人的 stderr 接回来（`-v`），Go 自己就说了原因：

```
$ ./out/host.exe -wasm ./out/guest-exe.wasm -mode noinit -fn add -v
runtime: wasmexport function called before runtime initialization
	call _start first
调用 add([2 3]) 失败: "wasm error: unreachable\n..."
```

c-shared 构建在同样条件下**一模一样**地失败，只是提示改成 `_initialize`：

```
$ ./out/host.exe -wasm ./out/guest-cshared.wasm -mode noinit -fn add -v
导出函数: [_initialize add alloc boom call spin]
runtime: wasmexport function called before runtime initialization
	call _initialize first
调用 add([2 3]) 失败: "wasm error: unreachable\n..."
```

**(b) 先把 `_start` 跑起来再调**

exe 构建：`_start` 会跑 `main()` 然后 `proc_exit(0)`，模块当场关闭。

```
$ ./out/host.exe -wasm ./out/guest-exe.wasm -mode default -fn add      # wazero 默认 startFunctions=["_start"]
实例化成功 closed=true
调用 add([2 3]) 失败: "module closed with exit_code(0)"

$ ./out/host.exe -wasm ./out/guest-exe.wasm -mode manual-start -fn add # 自己手动调 _start
手动调 _start -> err="module closed with exit_code(0)" closed=true
调用 add([2 3]) 失败: "module closed with exit_code(0)"
```

**这条路对「库形态」是死路**：`_start` 是命令入口，跑完就退出。

**(c) wazero 对 Go 产物的已知约束**

- `api.Module.ExportedFunction` 的文档就点名了这件事（wazero v1.12.0 `api/wasm.go:155`）：
  > The default wazero.ModuleConfig attempts to invoke `_start`, which in rare cases can close the module.
- wazero 自己的例子（`examples/basic/add.go:45`、`examples/allocation/tinygo/greet.go:47`）对 wasi 目标一律是
  `wazero.NewModuleConfig().WithStartFunctions("_initialize")`。**这是 wazero 官方对 reactor 形态的标准写法**，不是我们发明的。
- 关键坑：**`WithStartFunctions("_initialize")` 在模块没有这个导出时不会报错，静默跳过**。拿 exe 构建配 `_initialize`，得到的是同一个 `unreachable`：

```
$ ./out/host.exe -wasm ./out/guest-exe.wasm -mode initialize -fn add
调用 add([2 3]) 失败: "wasm error: unreachable\n\t.runtime.abort(i32) i32\n\t.runtime.notInitialized()\n\t.add(i32,i32) i32"
```

  反过来，拿 c-shared 构建配默认配置（找 `_start`，找不到）也一样静默跳过、一样 `unreachable`。
  → **宿主必须自己按导出表判定形态并显式跑对的 start，不能指望 wazero 报错。**

**成功的那一次：**

```
$ ./out/host.exe -wasm ./out/guest-cshared.wasm -mode initialize -fn add
导出函数: [_initialize add alloc boom call spin]
调用 add([2 3]) = [5]
```

### 1.3 根因（不是猜的，是源码里那三行）

Go 在每个 `//go:wasmexport` 包装函数**开头**插入运行时初始化检查（`$(go env GOROOT)/src/cmd/internal/obj/wasm/wasmobj.go:915-921`）：

```go
// Detect and error out if called before runtime initialization
// SP is 0 if not initialized
p = appendp(p, AGet, regAddr(REG_SP))
p = appendp(p, AI32Eqz)
p = appendp(p, AIf)
p = appendp(p, ACall, obj.Addr{... Sym: runtimeNotInitialized})
p = appendp(p, AEnd)
```

`runtime.notInitialized1` 就是那句提示（`src/runtime/sys_wasm.go:43-50`）：

```go
func notInitialized1() {
	writeErrStr("runtime: wasmexport function called before runtime initialization\n")
	if isarchive || islibrary {
		writeErrStr("\tcall _initialize first\n")   // c-shared / archive
	} else {
		writeErrStr("\tcall _start first\n")        // exe
	}
}
```

而 `_initialize` 由 `_rt0_wasm_wasip1_lib` 提供（`src/runtime/rt0_wasip1_wasm.s:18-20`），**只有 c-shared/archive 构建才链接它**。

所以那次的完整因果链是：

1. 用默认 `go build` 产出了 exe 形态（有 `_start`、没有 `_initialize`）；
2. `wasmhost.Load` 用默认 `ModuleConfig`（默认 startFunctions = `["_start"]`，见 `wasmhost.go:79-81`）—— 要么跑了 `_start` 把模块关掉，要么根本没跑；
3. 调 `add` 时 SP 还是 0 → `notInitialized` → `unreachable`；
4. Go 已经把原因写到 stderr 了，但 `wasmhost.go:81` 的 `WithStdout(nil).WithStderr(nil)` **把它扔了**，于是只剩一句 `wasm error: unreachable`。

**这里还有一件事比 bug 本身更值得记：两处注释的前提是错的。**

- `internal/wasmhost/wasmhost.go:77-78`：「不设 start 函数……设了反而会因"找不到入口"装载失败」
- `cmd/dshd/wasm.go:27-28`：「设了 `_start` 反而会让这类模块装不上」

实测（§1.2(c)）恰好相反：**wazero 对缺失的 start 函数是静默跳过，不报错**。c-shared 模块配默认配置（去找 `_start`）的输出是「实例化成功 closed=false」，然后在第一次调用时炸成 `unreachable`。后果是：那句注释让所有人**不去设 `_initialize`**，于是故障从「装载期一句明确报错」被推迟成「调用期一句 unreachable」；而它描述的失败模式在 wazero v1.12 上根本不存在。

另外顺带验到：`wasmhost.Load` 现在**没有装 WASI 导入**，所以真正的 Go 产物连实例化都过不去（用仓库里现成的 `dshd.exe` 跑，没改任何代码）：

```
$ ./dshd.exe wasm inspect --module tmp/abispike/out/guest-cshared.wasm
模块:   tmp/abispike/out/guest-cshared.wasm（4464916 字节）
魔数:   ok（\0asm）
sha256: efbd41ffadcfd7294ffeefe4ddfbc844cf4b57cb525a07754177268b09633967
dshd: 实例化 wasm 失败（tmp/abispike/out/guest-cshared.wasm）: module[wasi_snapshot_preview1] not instantiated
[exit=1]

$ ./dshd.exe wasm call --module tmp/abispike/out/guest-cshared.wasm --fn add --args 2,3
dshd: 实例化 wasm 失败（...）: module[wasi_snapshot_preview1] not instantiated
[exit=1]
```

（Go 与 TinyGo 的产物都导入 `wasi_snapshot_preview1`；关掉 WASI 导入时两者都是这一句。）

### 1.4 问题一的答案

| | 结论 |
|---|---|
| 能不能调？ | **能。** 前提两条：`-buildmode=c-shared` 产出 reactor；宿主 `WithStartFunctions("_initialize")`。 |
| 用默认 `go build` 行不行？ | **不行。** `_start` 语义是「跑完就退出」，`proc_exit(0)` 之后 `module closed with exit_code(0)`。 |
| 这是 wazero 的限制吗？ | **不是。** wazero 的官方例子就是这么接 wasi 目标的；Go 侧是「reactor 必须先 `_initialize`」的运行时约定。 |
| 还能不能继续用现在的 60 字节夹具？ | 能，但它只是 **reactor-without-runtime** 的特例（没有 `_initialize`，也就不需要初始化）。**别把「不需要初始化」当成一般规律。** |

---

## 2. 问题二：TinyGo 这条备选

### 2.1 本机安装：能装，但卡了两步

1. 本机**没有** TinyGo（`which tinygo` → not found）。
2. 下载 `tinygo0.44.0.windows-amd64.zip`（180,259,399 B）解压后 `tinygo version` 直接可用，自带的 Go 版本探测也过了：

```
$ tinygo version
tinygo version 0.44.0 windows/amd64 (using go version go1.27.3 and LLVM version 22.1.4)
```

3. **第一个卡点：`-target=wasi` 需要 `wasm-opt`，Windows 版 zip 里没带。**

```
$ tinygo build -target=wasi -o ../out/tinygo-guest.wasm .
error: could not find wasm-opt, set the WASMOPT environment variable to override
```

   `-opt=0` 绕不过去（照样找 wasm-opt）。需要额外装 Binaryen：`binaryen-version_133-x86_64-windows.tar.gz`（111,045,528 B），然后
   `export WASMOPT=$(cygpath -w /tmp/binaryen-version_133/bin/wasm-opt.exe)`。

4. **第二个卡点（更关键）：默认 `-target=wasi` 产物是 command 形态，导出根本不能从宿主调。**

```
$ ./out/host.exe -wasm ./out/tinygo-guest.wasm -mode noinit -fn add -v
导出函数: [_start add alloc ... spin]
panic: //go:wasmexport function called before runtime initialization
调用 add([2 3]) 失败: "wasm error: unreachable\n\tmain.runtime.runtimePanicAt(i32)\n\tmain.runtime.wasmExportCheckRun()\n\tmain.main.add#wasmexport(i32,i32) i32..."
```

   TinyGo 的提示比 Go 干脆（`panic: //go:wasmexport function called before runtime initialization`），但**同样要 reactor**：
   `tinygo build -buildmode=c-shared -target=wasi` 之后导出表变成 `[_initialize add ...]`，`add(2,3)=[5]`。

### 2.2 TinyGo 通不通？通，而且同一套 ABI 直接吃

```
$ ./out/host.exe -wasm ./out/tinygo-cshared.wasm -mode initialize -fn add
导出函数: [_initialize add alloc asyncify_get_state ... spin]
调用 add([2 3]) = [5]

$ ./out/host.exe -wasm ./out/tinygo-cshared.wasm -mode initialize -fn call -text 你好
ABI: 写入 17 字节到 143552；call 返回 ptr=154368 len=64；读回 = {"summary":"收到 你好","text":"echo:你好","isError":false}

$ ./out/host.exe -wasm ./out/tinygo-cshared.wasm -mode initialize -fn spin -timeout 300ms
spin: 超时 300ms，实际 315ms，err="module closed with context deadline exceeded"，IsClosed=true
```

**§3 的 ABI 草案，在 Go 和 TinyGo 两种产物上一字未改地跑通了。**

### 2.3 Go vs TinyGo：同一份客人源码，实测对比

| | Go `-buildmode=c-shared` | TinyGo `-buildmode=c-shared` |
|---|---|---|
| 产物大小（带 `encoding/json`） | 4,464,916 B（4.3 MiB） | 2,400,429 B（2.3 MiB；`-opt=z -no-debug` 后 **1,009,731 B**） |
| 产物大小（只有 `add`，无 json） | 4.4 MiB 起步（运行时全量链接） | **26,565 B** |
| 编译耗时 | 0.74–1.55 s | 0.34–0.67 s |
| 实例化 + `_initialize` | 1.5–3.8 ms | 0.5–1.5 ms |
| 每实例线性内存 | 3,670,016 B（3.5 MiB） | 393,216 B（384 KiB） |
| 一次成功调用 | 微秒级 | 微秒级（首次偶见 ~0.5 ms） |
| 额外工具链 | 无 | TinyGo 180 MB + Binaryen 111 MB |
| 标准库 | 全量 | 子集（`encoding/json` 可用，但会把体积从 26 KB 拉到 1 MB） |
| 装/用中的坑 | 必须 `-buildmode=c-shared` | 必须 `-buildmode=c-shared` **且**要有 `wasm-opt` |

> 顺带一个反面教材：TinyGo 的 26 KB 和 1 MB 之间差的就是 `encoding/json`。**如果 ABI 用 JSON，TinyGo 的体积优势会掉一大半**；要留住它，就得把信封换成手写的最小解析（这属于后续优化，本轮没做，见 §7「未验证」）。

---

## 3. 问题三：工具 ABI 草案

不管 §1 的结论如何都要写，现在它有实测支撑了。

### 3.1 形状：线性内存 + 两个导出，不引入任何调用约定库

```
alloc(len: i32) -> ptr: i32                  ; 客人划一块 len 字节，返回线性内存偏移
call(ptr: i32, len: i32) -> (ptr<<32)|len    ; 客人吃 args JSON、吐结果 JSON
```

约定（全部是 i32 / 线性内存，没有 WIT、没有 component model、没有 wasi-http、没有新的 IDL）：

- **输入**：宿主把一段 JSON 写进 `[ptr, ptr+len)`，然后 `call(ptr, len)`。
- **输出**：客人正常 return 一个 `uint64`，高 32 位是结果缓冲区偏移，低 32 位是长度；宿主按这个区间读回 JSON。
- **`_initialize` 不属于 ABI**：它是 Go/TinyGo 运行时的初始化入口，属于**构建形态**，不是我们的调用约定。别把它算成「第三个导出」——它由宿主的 `WithStartFunctions` 显式跑。
- 输入输出都是**客人分配**的（`alloc`），宿主只写进客人给的区间。
- **`len == 0` 是协议错**：客人必须永远回一个信封（哪怕是 `isError:true`）。空返回按宿主错误处理，别当成「空结果」——两者在排障时天差地别。
- 宿主自己的 `Memory` 句柄**不要缓存底层 `[]byte`**：客人一 `memory.grow` 就搬（实测见 §3.7）。

实际跑通的那一次（Go 产物，`text` 是中文，验证了 UTF-8 跨边界）：

```
$ ./out/host.exe -wasm ./out/guest-cshared.wasm -mode initialize -fn call -text 你好
ABI: 写入 17 字节到 2957312；call 返回 ptr=3047552 len=64；读回 = {"summary":"收到 你好","text":"echo:你好","isError":false}
```

### 3.2 信封

输入（宿主 → 客人）：

```json
{ "v": 1, "tool": "echo", "args": { /* 工具的 args 原样，不二次编码成字符串 */ } }
```

输出（客人 → 宿主）：

```json
{ "summary": "一行给人看", "text": "给模型看的正文", "isError": false }
```

- `summary` / `text` / `isError` 三个字段**必填**（就是任务里要求的 `{summary,text,isError}`），可选 `hint`（失败时给下一步）。
- `v` 是 ABI 版本。宿主只认 `1`；客人回的信封也带 `v`，对不上按**宿主错误**处理，不按工具失败。
- 为什么连 `tool` 一起传：现在一个模块一个工具，字段看起来多余；但把工具名放进去，将来「一个模块挂多个工具」不需要改 ABI，也不需要宿主猜。

### 3.3 失败必须分三层，wasm trap ≠ 工具失败

这是整个 ABI 最重要的一条。三层各有各的出口：

| 情况 | 客人侧表现 | 宿主看到 | 宿主产出 |
|---|---|---|---|
| **业务失败**（文件不存在、参数不合法、被拒绝） | 正常 `return`，信封里 `isError:true` | `Call` 返回 nil error，读到合法 JSON | `Result{Summary, Text, Hint}`, `err != nil` → v4 的 `isError=true`（`cmd/dshd/main.go:533` 就是按 `c.Err != nil` 编码的） |
| **客人崩溃**（panic / 越界 / 除零 / 断言） | trap，永远不返回 | `Call` 返回 `wasm error: unreachable\nwasm stack trace: ...` | **不是工具语义**：`Result{Hint:"插件内部错误…"}` + 错误；该实例标记为不可信 |
| **宿主协议错**（越界指针、长度超限、JSON 坏、版本不认识） | —（客人没做错什么） | 宿主自己读不出来 | 宿主错误 + 一行明确的 `Hint`；**不要**在同一实例上重试 |
| **超时/取消** | ctx 到期，客人被打断 | `module closed with context deadline exceeded` | `context.DeadlineExceeded` + 实例重建（见 §3.4） |

实测的三条原文：

```
# 业务失败：信封里 isError:true，没有 trap
ABI: 写入 11 字节到 2678832；call 返回 ptr=3063888 len=67；读回 = {"summary":"空输入","text":"args.text 是空的","isError":true}

# 客人 panic：trap，栈里能看见客人自己的函数名
调用 boom([]) 失败: "wasm error: unreachable\nwasm stack trace:\n\t.runtime.abort(i32) i32\n\t.runtime.fatalpanic(i32) i32\n\t.runtime.gopanic(i32) i32\n\t.main.boom(i32) i32\n\t.boom() i32"

# trap 之后同一个实例还能调别的导出（但不要信它的状态）
boom: trap 之后再调 add(2,3)：
调用 add([2 3]) = [5]
```

> 「trap 之后实例还能用」是实测出来的，**但不该拿它做设计**：Go 的 `fatalpanic` 之后运行时状态没人担保。正确做法是**trap 即弃实例**，下次调用重建（重建只要 2 ms / 0.5 ms，见 §3.5）。

### 3.4 超时 / 取消怎么切断

- 宿主侧靠 wazero 的 `WithCloseOnContextDone(true)`（`wasmhost.go:64` 已经有这一行）。每次调用把带 deadline 的 ctx 传给 `Call`；wazero 在解释/编译循环的回边检查 ctx，**真的**把执行打断，而不是「把结果丢掉继续转」。
- 实测（Go 产物，`spin()` 是空死循环）：

```
spin: 超时 300ms，实际 301ms，err="module closed with context deadline exceeded"，IsClosed=true
spin: 打断后再调 add(2,3)：
调用 add([2 3]) 失败: "module closed with context deadline exceeded"
```

- **代价必须写进设计：ctx 一到期，wazero 会把整个 module 关掉**（`IsClosed=true`）。所以超时不是「这次调用失败」，而是「这个实例没了」。宿主的处理是：**标记实例死亡 + 下次调用重建**。
- 同一条也适用于**用户取消**（`ctx` 被 cancel，错误是 `context.Canceled`，模块同样被关）。取消和超时在实现上是一条路径。
- 并发上限：一个实例同一时刻只能跑一个 `call`（`call` 是同步导出，重入会踩客人自己的全局状态）。宿主侧按实例加锁即可；要让多个工具并行，就给每个模块各自一个实例。

### 3.5 生命周期：实例怎么管

三种都试过，数字如下（Go 产物）：

| 策略 | 每次调用代价 | 风险 |
|---|---|---|
| 常驻一个实例 | ~0 | 客人状态跨调用泄漏；超时/崩溃后必须重建 |
| **每次调用新实例** | 1.5–3.8 ms + 3.5 MiB | 无状态泄漏；内存瞬时翻倍（并发时） |
| 常驻 + 死亡重建（**推荐**） | 平时 ~0，死了付一次 1.5–3.8 ms | 需要「实例已死」的状态机 |

编译只做一次（`rt.CompileModule`），这一步很贵，必须常驻：

```
编译耗时: 935.884ms          # 4.4 MB 的 Go 模块
  第 1 次: 实例化+_initialize=2.723ms 线性内存=3670016 字节(3 MiB) ...
  第 2 次: 实例化+_initialize=3.416ms 线性内存=3670016 字节(3 MiB) ...
  对照（不跑 _initialize）: 实例化=594µs 线性内存=3080192 字节(2 MiB)
```

TinyGo 同样策略下：编译 0.34 s、实例化 0.5–1.5 ms、384 KiB。

→ **推荐「编译一次 + 每模块一个常驻实例 + 死了重建」**。这样正常路径零成本，异常路径（超时/trap）付一次 2 ms。

### 3.6 映射到宿主的 `spi.Tool`

| `spi.Tool` | 从哪来（**不能**从客人来） |
|---|---|
| `Name()` / `Description()` | manifest（与 static 模块同样的审查与快照流程） |
| `Schema()` | manifest 里写死的 JSON **或**装载期向客人要一次（`call` 传 `{"v":1,"tool":"x","args":{"__describe":true}}`）。要走 R4 快照，建议先 manifest-pinned；客人给 schema 的那天再做 |
| `Caps()` | **只能 manifest**。理由：审批与沙箱靠 `Caps` 判定（`AGENTS.md` §2 / `spi/tool.go:35-44`），让客人自己声明等于让它自己给自己发许可证 |
| `Exec(ctx, args)` | `Invoke(ctx, args)` → 信封 → `Result{Summary, Text, Hint}`；`isError:true` 时 `err != nil` |

**隔离是这条路线最硬的好处**：客人只有线性内存 + `wasi_snapshot_preview1` 的最小面（`fd_write`/`random_get` 之类），**不挂任何 FS preopen、不传 args/env**，所以 `Caps{Writes:false, Network:false}` 是**被强制**的，不是靠自我介绍。这一点和 static 工具完全不是一回事。

### 3.7 线性内存的两个实测事实

```
mem: 初始大小 3670016 字节
mem: alloc(8 MiB) 返回 ptr=3670016；之后 Memory.Size()=12058624（同一个句柄，没重取）
mem: 往 ptr 写 "tail" -> true；读回 = "tail" (true)
```

1. `mod.Memory()` 返回的是**活句柄**：客人 `memory.grow` 之后同一个句柄的 `Size()`/`Read`/`Write` 立刻反映新大小，不用重取。
2. 但**宿主不要缓存 `mem` 背后的 `[]byte`**（`Read` 是拷贝，所以安全；自己 `UnsafeView` 才是坑）。旧指针在图里依然可读可写。

宿主侧必须设上限（建议）：args ≤ 1 MiB、结果 ≤ 1 MiB、单次调用 deadline 默认 10 s 可配。超限是**宿主协议错**（§3.3 第三行），不是工具失败。

---

## 4. 推荐路线

**宿主侧**：wazero v1.12 + §3 的 `alloc`/`call` 两个导出 + 线性内存 JSON 信封；编译一次、每模块一个常驻实例、死亡重建；`WithCloseOnContextDone(true)` 管超时与取消。

**客人侧**：默认 `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`。

为什么不直接选账面更好的 TinyGo：

- **今天就能用**：Go 那条路不需要给仓库加任何外部二进制，客人代码与宿主同语言、同审查标准、同 `go vet`；TinyGo 需要 TinyGo 180 MB + Binaryen 111 MB，而且 `-target=wasi` 不加 `-buildmode=c-shared` 就是**调不通的 command 形态**（§2.1 第二卡点），这个坑必须写进构建脚本才不会有人再踩一次。
- **收益现在不是瓶颈**：常驻实例下，3.5 MiB vs 384 KiB、4.4 MB vs 1 MB 只在「模块很多」或「要发小体积插件包」时才变成问题。
- **切换不需要改宿主**：ABI 与工具链无关，这两条今天都验过了。等体积/冷启动真的疼了，再把客人换成 TinyGo，宿主的 `internal/wasmhost` 一行不用动。

一句可执行的判据：**先按 Go 落地；当「wasm 模块数 × 4.4 MB」或「冷启动编译」超过可接受阈值时，换 TinyGo，宿主不动。**

---

## 5. 三条备选与代价（如果 §1/§2 都不成立，或不想走 wasm）

| 备选 | 代价 | 什么时候选它 |
|---|---|---|
| **A. 进程插件**（子进程 + stdio JSON，`BackendProcess` 已经留了常量） | 每工具一个进程：启动 10–50 ms、常驻内存 MB 级、要写握手/心跳/回收；好处是崩溃隔离彻底、任何语言都能写、不受 wasm 内存/指令限制 | 工具要跑重依赖（浏览器、Python 栈）、或需要真文件系统/网络 |
| **B. 只做 static（编译进二进制）** | 零装载、零隔离；每加一个工具都要重编宿主、走全量回归；第三方无法自带 | 工具是官方长期内置的（现在 30 个工具全是这样） |
| **C. 换运行时**（wasmtime-go / wasmer-go 都走 cgo；wazero 之外没有不引 cgo 的选择） | 引入 cgo → 交叉编译和静态链接全废（本仓 `AGENTS.md` §5 要 `-race` 与单文件 exe）；换运行时解决不了「Go 运行时初始化」这一类**客人侧**问题，而这次的问题正是客人侧的 | 只有当 wazero 的指令覆盖/性能真的不够时才考虑；本轮没发现这种迹象 |

> 注意：本轮**不是**「wasm 不行」，而是「wasm 行，但宿主少做了两件事（WASI 导入、跑 `_initialize`）」。所以备选 A/B/C 现在是**降级选项**，不是替代品。

---

## 6. 如果走这条路，下一步要改哪几个文件

按依赖顺序（前两个是宿主内核件，后面是装配与 CLI）：

| # | 文件 | 改什么 | 为什么必须改（对应哪条实测） |
|---|---|---|---|
| 1 | `internal/wasmhost/wasmhost.go` | `Load` 里：① 先 `wasi_snapshot_preview1.Instantiate(ctx, rt)`；② 按导出表判形态：有 `_initialize` → `WithStartFunctions("_initialize")`，只有 `_start` → **明确报错**「这是 command 构建，用 `-buildmode=c-shared` 重编」；③ 别再无脑 `WithStdout(nil).WithStderr(nil)`，把客人 stderr 收进一个 4 KiB 环形 buffer，trap 时拼进错误；④ 暴露 `Memory()` | §1.3 末尾的 `module[wasi_snapshot_preview1] not instantiated`；§1.3 那句被吃掉的 `call _initialize first`；§1.2(c)「配错 start 名 wazero 不报错」 |
| 2 | `internal/wasmhost/abi.go`（新） | `Envelope{v,tool,summary,text,isError,hint}`；`(*Module).Invoke(ctx, args)`：`alloc` → `mem.Write` → `call` → 读回 → 解析；三个上限常量；把 §3.3 四层错误各给一个可 `errors.Is` 的错误类型 | §3.1/§3.3/§3.7 全节 |
| 3 | `cmd/dshd/wasmload.go` | 删掉「provides 非空就报错」（现 133–139 行）；把现在 `Register: nil` 的壳（184–189 行）换成真的 `Register: func(s *kernel.Scope){ reg,_ := kernel.Inject(s, spi.ToolsKey); reg.Register(&wasmTool{...}) }`，并给模块加 `Requires: []string{"tools"}`；工具名/schema/caps 从 manifest `config` 读（`kernel.ModuleEntry.Config` 已存在，**kernel 不用改**） | 现状是「装载了但不提供任何能力」；`modules/tool/glob/glob.go:183-193` 是标准形状 |
| 4 | `internal/wasmhost/tool.go`（新，或并入 2） | `wasmTool` 实现 `spi.Tool`：`Name/Description/Schema/Caps` 取 manifest，`Exec` 调 `Invoke`，`isError` → `(Result{Hint}, err)`；**按 R2 只拿 `*kernel.Scope`，不碰 App/单例** | §3.6 |
| 5 | `cmd/dshd/wasm.go` | `inspect` 打「reactor / command」判定与 `_initialize` 有无；`call` 增加 ABI 模式（走 `alloc`+`call`），保留现在的裸函数调用当调试选项 | 今天 `dshd wasm inspect` 对 Go 产物只会吐 `module[wasi_snapshot_preview1] not instantiated` |
| 6 | `dsh.toml`（及 profile manifest） | 新 `[[module]] backend="wasm" path=... digest=... requires=["tools"]` + `[module.config]` 里的 `name/description/schema/caps/timeout_ms` | §3.6「Caps 只能 manifest」 |
| 7 | `internal/wasmhost/wasmhost_test.go` + `testdata/` | 现在只有一个手写的 60 字节夹具（**没有 `_initialize`**）。要覆盖 reactor，得再入一个带 `_initialize` 的小夹具：TinyGo 最小产物 **26 KB** 够用，别把 4.4 MB 的 Go 产物入库 | §1.4 最后一行；`scripts/build-wasm-fixture.py` 的头注释把「Go 产物需要 `_initialize`」写成了理由，这条判断现在被 §1 修正了一半 |

**明确不用改**：`internal/kernel/`（`implementedBackends` 已经含 wasm，`ModuleEntry.Config` 已够用）、`internal/spi/`（`Tool`/`ToolRegistry` 形状不变）。这符合 kernel 的红线 R1。

---

## 7. 未验证 / 已知缺口

写在前面：**下面这些没试过，别当结论用。**

1. **wazero 的 interpreter 引擎**（`NewRuntimeConfigInterpreter`）没试。全部实测都在默认 compiler（wazevo）上。
2. **`-race` 下的 wasmhost 路径**没跑（`bash scripts/race.sh` 需要在隔离副本里跑，本轮没做）。
3. **并发**：同一 runtime 同时实例化多个模块、多个实例并行调用，没做压力测试；§3.4 的「每实例加锁」是设计推断，不是实测。
4. **`-buildmode=c-archive`**（另一条 reactor 路）没试；`go help buildmode` 说 wasip1 上 archive 也能导，但没跑。
5. **长期运行的泄漏**：只做了 3–5 次实例化循环，没做千次级，也没量 RSS。Go 的 module 每实例 3.5 MiB，如果 close 不干净会很难看——**没验证**。
6. **攻击面**：没做恶意客人测试（读越界、无限增长内存、`memory.grow` 到上限）。wazero 应该挡住，但没打。
7. **信封换成非 JSON 的最小解析**没做（§2.3 提到的 TinyGo 体积优化）。
8. **TinyGo 的 `-target=wasm`（非 wasi）**没试；本轮只验 wasi。
9. `wasmhost.Load` 里 `WithCloseOnContextDone(true)` 对**实例化阶段**的影响没单独测；只在调用阶段验过。

---

## 附录 A：一键复现

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export GOCACHE=/tmp/gocache-abi
cd "D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/abispike"

# 1) 造产物（Go 两种构建）
(cd guest && GOOS=wasip1 GOARCH=wasm go build -o ../out/guest-exe.wasm . \
          && GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../out/guest-cshared.wasm .)

# 2) 宿主探针
cd ../.. && go build -o tmp/abispike/out/host.exe ./tmp/abispike/host && cd tmp/abispike

# 3) 全部实验（转录会与 out/evidence.txt 同形）
bash run-evidence.sh | tee out/evidence-$(date +%H%M).txt
```

TinyGo 那两支需要额外：

```bash
# 已下载解压在 /tmp：tinygo-0.44.0/（180 MB）与 binaryen-version_133/（111 MB）
export PATH="/c/Program Files/Go/bin:$PATH"
export WASMOPT="$(cygpath -w /tmp/binaryen-version_133/bin/wasm-opt.exe)"
cd tmp/abispike/tguest
/tmp/tinygo-0.44.0/tinygo/bin/tinygo.exe build -buildmode=c-shared -target=wasi -o ../out/tinygo-cshared.wasm .
```

---

## 8. 落地记录（2026-11-04）

> 这一节是**已经发生的**，不是计划。§1–§7 的结论一条没改，只是把其中"下一步要改哪几个文件"的前三步做掉了。
> 下面的输出都是本轮原样摘的。环境：`go1.27.3 windows/amd64`、`wazero v1.12.0`、`GOCACHE=/tmp/gocache-wtool`。

### 8.1 一句话结论

manifest 里写 `provides = ["tool"]` 的 wasm 模块，现在**真的在工具表里有一个工具**：模型看得见、`dshd tools` 打得出、`dshd exec` 调得动、`dshd modules graph` 有它一行。
没写这个声明的 wasm 模块，行为与落地前**逐字节一致**（工具表 sha256 都相同，见 §8.6 第 4 条）。

### 8.2 改了哪些文件

| 文件 | 改了什么 |
|---|---|
| `internal/wasmhost/wasmhost.go` | `Load`：① 装 `wasi_snapshot_preview1`（§1.3 末尾那个 `module[wasi_snapshot_preview1] not instantiated` 就是缺这一步）；② 按导出表判形态——有 `_initialize` 就是 reactor，用 `WithStartFunctions("_initialize")`；只有 `_start` 就明确报错"用 `-buildmode=c-shared` 重编"（配错 start 名 wazero 不报错，见 §1.2(c)）；③ 客人 stdout/stderr 收进 4 KiB 环形缓冲，trap 时拼进错误（§1.3 那句被吃掉的 `call _initialize first` 不再丢） |
| `internal/wasmhost/abi.go`（新） | `InvokeTool` / `ParseReply` / `Request` / `Reply` / 三个上限 / `ErrGuestTrap` / `ErrProtocol` / `TrapError` / `ProtocolError` / `SetToolName` / `SetTimeout` |
| `internal/wasmhost/abi_test.go`（新） | 8 条（§8.6） |
| `internal/wasmhost/testdata/toolplugin/main.go`（新） | Go 源码夹具：`//go:wasmexport alloc/call`，实现 ABI，返回 `{"summary":…,"text":…}` |
| `internal/wasmhost/testdata/toolplugin.wasm`（新，入库） | 上面那份用 `-buildmode=c-shared` 编出来的 reactor，**4,458,584 字节**（sha256 `35cde818…`，脚本连跑两次同值） |
| `cmd/dshd/wasmload.go` | `provides = ["tool"]` → 生成 `kernel.Module{Requires:["tools"], Register: 注册一个 spi.Tool}`；新增 `wasmTool` / `wasmToolSpec` / `hintForWasmError` / `parseWasmCaps`；`modules graph` 的 wasm 小节多一列 TOOL |
| `cmd/dshd/wasmload_test.go` | 新增 6 条、改写 1 条（原 `TestWasmProvidesRejected` 的语义变了） |
| `scripts/build-wasm-fixture.py` | 继续产出 60 字节纯 wasm；**新增**编 `toolplugin.wasm`（`GOOS=wasip1 GOARCH=wasm -buildmode=c-shared -trimpath`） |
| `docs/wasm-tool-abi.md` | 这一节 |

**没动**（本轮边界）：`internal/kernel/`、`internal/transport/`、`modules/`、`dsh.toml`、`PLAN.md`、`AGENTS.md`、`cmd/dshd/serve*.go`。

### 8.3 ABI 字段名（定稿）

两个导出（名字写死在宿主里，换客人编译器不用改宿主）：

```
alloc(n: i32) -> ptr: i32                 客人划 n 字节，返回线性内存偏移
call(ptr: i32, n: i32) -> (ptr<<32)|len   吃 args JSON，吐结果 JSON
```

**请求**（宿主 → 客人，JSON，写进客人 `alloc` 出来的区间）：

```json
{ "v": 1, "tool": "echo", "args": { /* 模型给的参数，原样嵌入，不二次编码成字符串 */ } }
```

**结果**（客人 → 宿主，JSON，通过返回值的 `ptr<<32|len` 指回来）：

```json
{ "v": 1, "summary": "一行给人看", "text": "给模型看的正文", "isError": false, "hint": "失败时给下一步" }
```

| 字段 | 必填 | 谁读 | 规矩 |
|---|---|---|---|
| `v` | **是** | 宿主 | 只认 `1`；对不上按**宿主协议错**处理，不按工具失败 |
| `tool` | 是（请求） | 客人 | 一个模块一个工具时看着多余，但将来一模块多工具不用改 ABI |
| `args` | 是（请求） | 客人 | 原样嵌入对象，不是字符串 |
| `summary` | **是** | 人（TUI/桌面） | 一行摘要 |
| `text` | **是** | 模型 | 已渲染好的正文，非空时投影只用它 |
| `isError` | **是** | 宿主 | **工具失败的唯一表达方式**（见下表） |
| `hint` | 否 | 模型 | 失败时的下一步；空则由宿主兜一条可行动的 |

上限与超时（超限 = 宿主协议错，不是工具失败）：args ≤ **1 MiB**、结果 ≤ **1 MiB**、单次调用默认 **10 s**（manifest `timeout_ms` 可改）。

**失败分四层，出口完全不同**（这是整个 ABI 最重要的一条，`abi.go` 就是按它写的）：

| 情况 | 客人侧 | `InvokeTool` 返回 | `Exec` 产出 |
|---|---|---|---|
| 业务失败（参数不对、文件不存在…） | 正常返回，信封 `isError:true` | `nil` error + 合法 JSON | `Result{Summary,Text,Hint}, err`（Hint 优先用客人的） |
| 客人崩溃（panic / 越界 / 断言） | trap，永不返回 | error，`errors.Is(err, ErrGuestTrap)` | `Result{Hint:"插件内部崩溃…不要原样重试"}, err` |
| 宿主协议错（指针越界、长度超限、坏 JSON、`v` 不认识、返回长度 0） | 客人没做错 | error，`errors.Is(err, ErrProtocol)` | `Result{Hint:"ABI 对不上…报给用户"}, err` |
| 超时 / 取消 | 执行被打断 | error，`errors.Is(err, context.DeadlineExceeded/Canceled)` | `Result{Hint:"缩小输入再试…"}, err` |

### 8.4 manifest 怎么写（这就是开关）

```toml
[[module]]
id      = "wasm-echo"
backend = "wasm"
path    = "echo.wasm"          # 相对**manifest 所在目录**解析，不是 cwd
digest  = "<sha256，64 位十六进制>"   # 可选，但建议写：内容被换要在启动时炸
provides = ["tool"]            # 开关。不写 = 老行为：装载但不提供任何能力
tool_name        = "echo"      # 必填：模型看到的工具名
tool_description = "把 text 原样回显"   # 必填：模型看到的「什么时候用它」
tool_schema      = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}'''
tool_caps        = []          # 可选：writes / executes / network / destructive，默认全 false
timeout_ms       = 2000        # 可选，默认 10000
```

几条定下来的语义：

- **`provides` 只认 `"tool"`**。写 `["compaction"]` 之类直接报错并点名是哪个能力——这一层做的是"一个模块 = 一个工具"，别的还没实现，写进去就是骗读 manifest 的人。
- **`"tool"`（单数）不是服务键 `"tools"`（复数）**。`tools` 是**别人提供**的工具目录，wasm 模块靠 `Requires: ["tools"]` 才能 `Inject` 进去注册；`tool` 是它要**贡献**的东西。`modules graph` 的 PROVIDES 列显示 `tool`、REQUIRES 列显示 `tools`，两列都在就是为了让人分清这件事（`provides` 的值由 `kernel.Apply` 原样复制到装配结果，装载层不另编一个）。
- **身份字段全在 manifest**（Name/Description/Schema/Caps）。不从客人那里问的理由见 §3.6：Caps 决定审批与沙箱，让客人自己声明等于让它自己给自己发许可证。
- **默认 `caps` 全 false 是沙箱强制的**，不是客人的自我介绍：宿主不给任何 FS preopen、不传 args/env、只挂 `wasi_snapshot_preview1` 的最小面。manifest 写更严的 caps（比如 `destructive`）只是让审批更早介入。
- 写错任何一项（缺 `tool_name`、schema 不是 JSON 对象、`tool_caps` 拼错）都在**装配期**失败并说清缺什么——这些错误在运行时的表现是"模型不会用这个工具"，从现象归因不到 manifest。

### 8.5 一个 wasm 插件作者要写什么

**两件事：两个导出 + 一个信封。** 没有 WIT、没有 component model、没有绑定库、不用学新语言——Go 客人就是普通 Go。

```go
package main

import (
	"encoding/json"
	"unsafe"
)

var keep [][]byte // 必须：宿主拿到的只是偏移，Go 看不见还有人在用这块内存

//go:wasmexport alloc
func alloc(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	b := make([]byte, n)
	keep = append(keep, b)
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

//go:wasmexport call
//go:nocheckptr
func call(ptr, n uint32) uint64 {
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), n)

	var req struct {
		V    int             `json:"v"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	_ = json.Unmarshal(in, &req)

	// ……你的逻辑：req.Args 里就是模型给的参数……
	out, _ := json.Marshal(map[string]any{
		"v": 1, "summary": "一行摘要", "text": "给模型看的正文", "isError": false,
	})
	buf := make([]byte, len(out))
	copy(buf, out)
	keep = append(keep, buf)
	return uint64(uint32(uintptr(unsafe.Pointer(&buf[0]))))<<32 | uint64(uint32(len(buf)))
}

func main() {} // reactor 里不会被执行，但 c-shared 要求有 main 包
```

构建（**两条都不能少**，见 §1）：

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o echo.wasm .
```

然后写 manifest（§8.4），`dshd tools --manifest …` 里就会出现 `echo`。

**三条不许做的事**（做了宿主会给出错误的方向）：

1. **不要用 trap 表达业务失败。** panic 是"插件崩了"，宿主会告诉模型"别原样重试"；参数不对请正常返回 `isError:true` + `hint`。
2. **不要返回空结果。** `len=0` 是协议错（宿主会当作 ABI 坏了），不是"工具说没事"。
3. **不要指望能碰外面。** 没有 FS preopen、没有网络、没有 args/env；Caps 只能由 manifest 声明。

参考实现就在 `internal/wasmhost/testdata/toolplugin/main.go`，测试用的 `toolplugin.wasm` 是它的产物。

### 8.6 验收（逐条，真实输出）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export GOCACHE=/tmp/gocache-wtool
$ go build ./... && go vet ./...
OK（无输出）

$ go test ./internal/wasmhost ./cmd/dshd -count=1
ok  	dshgo/internal/wasmhost	5.095s
ok  	dshgo/cmd/dshd	7.327s

$ python scripts/build-wasm-fixture.py
生成 internal\wasmhost\testdata\fixture.wasm 60 字节（手写纯 wasm：add / spin）
+ (cd internal\wasmhost\testdata\toolplugin && build -buildmode=c-shared -trimpath -o ...\toolplugin.wasm .)
生成 internal\wasmhost\testdata\toolplugin.wasm 4458584 字节（Go reactor：_initialize / alloc / call）

$ go build -o tmp/dshd.exe ./cmd/dshd
OK
```

**1. ABI 往返**（`internal/wasmhost`）：`TestInvokeToolRoundTrip` 给出 `{"text":"你好"}`，拿回合法 JSON，`summary`/`text` 都含"你好"。

```
--- PASS: TestInvokeToolRoundTrip (1.15s)
--- PASS: TestInvokeToolBusinessErrorIsNotTrap (0.93s)   # isError:true 时 InvokeTool 仍是 nil error
```

**2. trap ≠ 成功**（`TestInvokeToolTrapIsError`）：`{"boom":true}` → `errors.Is(err, ErrGuestTrap)`，返回的字节长度为 0。

```
--- PASS: TestInvokeToolTrapIsError (1.05s)
```

**3. `isError:true` → Exec 的 error + Hint**（`cmd/dshd`）

```
--- PASS: TestWasmToolBusinessErrorGivesHint (0.78s)
--- PASS: TestWasmToolTrapGivesHint (0.76s)
```

**4. 没有 `provides=["tool"]` 的 wasm 模块，工具表逐字节不变**（`TestWasmWithoutProvidesKeepsToolTable`）。真机上再验一遍：

```
$ sha256sum tmp/tools-a.json tmp/tools-b.json
0807348b2ff99e19f85cc7268962dfe60f15962d6bd52d95306f1f9a7e61baf2  tmp/tools-a.json   # 没有 wasm 模块
0807348b2ff99e19f85cc7268962dfe60f15962d6bd52d95306f1f9a7e61baf2  tmp/tools-b.json   # 有 wasm 模块，但没写 provides
$ diff tmp/tools-a.json tmp/tools-b.json && echo 逐字节相同
逐字节相同
```

**5. 真机：`modules graph` 能看到它**

```
$ ./tmp/dshd.exe modules graph --manifest tmp/wasm-tool-manifest.toml
# 激活顺序（由能力依赖决定，不是 manifest 行顺序）
MODULE                 BACKEND   PROVIDES                   REQUIRES
tools                  static    tools,toolrunner
tool-nowline           static                               tools
wasm-echo              wasm      tool                       tools

# wasm 后端（已装载 / 已校验；TOOL 列 = 注册进工具表的工具名，空表示不提供能力、只能 `dshd wasm call` 手动调）
MODULE                 BACKEND   DIGEST       EXPORTS            TOOL                   PATH
wasm-echo              wasm      35cde81846d2 _initialize,alloc,call echo                   D:\...\internal\wasmhost\testdata\toolplugin.wasm
```

**6. 真机：`tools` 里出现那个工具名**

```
$ ./tmp/dshd.exe tools --manifest tmp/wasm-tool-manifest.toml
[
  {
    "name": "echo",
    "description": "把 text 原样回显（wasm 夹具工具）",
    "parameters": { "additionalProperties": false, "properties": { "text": { "type": "string" } },
                    "required": ["text"], "type": "object" },
    "caps": {}
  },
  … 另外 1 个（now_line）
]
工具 2 个，600 字节
```

**7. 真机：真的调得动 / 三种结局**

```
$ ./tmp/dshd.exe exec --tool echo --args '{"text":"你好"}' --manifest tmp/wasm-tool-manifest.toml
{ "Summary": "收到 你好", "Data": null, "Text": "echo:你好", … "Hint": "", … }     [exit=0]

$ ./tmp/dshd.exe exec --tool echo --args '{}' --manifest tmp/wasm-tool-manifest.toml
执行失败: 空输入：args.text 是空的
提示: 传 {"text": "..."}：这个夹具工具只把 text 原样回显        [exit=1]

$ ./tmp/dshd.exe exec --tool echo --args '{"boom":true}' --manifest tmp/wasm-tool-manifest.toml
执行失败: wasm 工具 "echo" 执行失败: wasm 模块 "wasm-echo" 在 call 里 trap（插件内部崩溃，不是工具失败）: wasm error: unreachable
提示: 插件内部崩溃（wasm trap），不是你的参数错了：不要原样重试；换一种做法，或改用别的工具
```

trap 那一条还带回了客人的 stderr（就是 §1.3 里被丢掉的东西）：

```
客人 stderr: panic: toolplugin: 故意 panic，用来验证 trap 不等于工具失败
goroutine 1 [running]:
main.call(0x2d2000, 0x2a)
	dshgo/internal/wasmhost/testdata/toolplugin/main.go:99 +0x19
```

### 8.7 本轮没做 / 已知缺口

1. **实例不重建**：trap 或超时后 wazero 会把 module 关掉，之后这个模块不能用，直到重启宿主。§3.5 推荐的"死亡重建"没做（`CompileModule` 的结果还在手里，重建只要 2–3 ms，是下一步）。
2. **一个部署只能有一个 wasm 工具模块**：多个都写 `provides = ["tool"]` 会撞 kernel 的"能力被两个模块同时提供"。要放开得换标记（per-module 字段）或让 kernel 认识"能力种类"，**不用改 kernel 也能做**（装载层自己解析 manifest）。
3. **schema 不从客人取**：仍是 manifest-pinned（§6 第 3 行的建议没变）。
4. **`cmd/dshd/doctor.go` 的 wasm 文案过期**：`checkWasmModules` 无条件说"它们不提供工具，只能 `dshd wasm call` 手动调"，现在对 `provides=["tool"]` 的模块是错的。该文件不在本轮边界内，未改。
5. **并发 / `-race` 没跑**：`bash scripts/race.sh` 要在隔离副本里跑，本轮没做；`InvokeTool` 已按"每实例一把锁"串行化同一模块的调用（实测 §3.4 的设计推断，不是压测结论）。
6. **体积 4.4 MB**（Go 客人固有，见 §2.3）。要小就换 TinyGo，宿主一行不用改。
7. 上一轮 §7 里没验的那些（interpreter 引擎、c-archive、千次实例化泄漏、恶意客人）本轮同样没验。
