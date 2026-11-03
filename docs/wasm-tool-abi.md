# wasm 工具 ABI 技术验证（spike）

> **只想写插件？** 看 `docs/wasm-plugin-author-guide.md`（不含设计理由，只有"你要写什么"）。

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

> **更新（§10）**：这一节写的是**单工具时代**的规则。§10 起，一个模块可以声明多个工具（`[[module.tool]]` 数组），
> `provides` 里也可以出现多个 `tool:<工具名>`。**旧写法仍然有效、一个字不用改**，两种写法不许混写。

```toml
[[module]]
id      = "wasm-echo"
backend = "wasm"
path    = "echo.wasm"          # 相对**manifest 所在目录**解析，不是 cwd
digest  = "<sha256，64 位十六进制>"   # 可选，但建议写：内容被换要在启动时炸
provides = ["tool"]            # 开关。不写 = 老行为：装载但不提供任何能力
                               # 多个 wasm 工具模块共存时必须写 ["tool:<tool_name>"]，见 §9.2
tool_name        = "echo"      # 必填：模型看到的工具名
tool_description = "把 text 原样回显"   # 必填：模型看到的「什么时候用它」
tool_schema      = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}'''
tool_caps        = []          # 可选：writes / executes / network / destructive，默认全 false
timeout_ms       = 2000        # 可选，默认 10000
```

几条定下来的语义：

- **`provides` 只认两种写法：`["tool"]` 与 `["tool:<tool_name>"]`**（后者的来历见 §9.2）。写 `["compaction"]` 之类直接报错并点名是哪个能力——这一层做的是"一个模块 = 一个工具"，别的还没实现，写进去就是骗读 manifest 的人。`"tool"` 这种缩写**全署只能出现一次**；有两个以上 wasm 工具模块时，每个模块写自己的 `"tool:<tool_name>"`（冒号后必须与同一行的 `tool_name` 一致，装载层会校验）。
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

1. **实例不重建**：trap 或超时后 wazero 会把 module 关掉，之后这个模块不能用，直到重启宿主。§3.5 推荐的"死亡重建"没做（`CompileModule` 的结果还在手里，重建只要 2–3 ms，是下一步）。→ **已补：§9.1**。
2. **一个部署只能有一个 wasm 工具模块**：多个都写 `provides = ["tool"]` 会撞 kernel 的"能力被两个模块同时提供"。要放开得换标记（per-module 字段）或让 kernel 认识"能力种类"，**不用改 kernel 也能做**（装载层自己解析 manifest）。→ **已补：§9.2**（走的是"换能力键"那条）。
3. **schema 不从客人取**：仍是 manifest-pinned（§6 第 3 行的建议没变）。
4. **`cmd/dshd/doctor.go` 的 wasm 文案过期**：`checkWasmModules` 无条件说"它们不提供工具，只能 `dshd wasm call` 手动调"，现在对 `provides=["tool"]` 的模块是错的。该文件不在本轮边界内，未改。
5. **并发 / `-race` 没跑**：`bash scripts/race.sh` 要在隔离副本里跑，本轮没做；`InvokeTool` 已按"每实例一把锁"串行化同一模块的调用（实测 §3.4 的设计推断，不是压测结论）。
6. **体积 4.4 MB**（Go 客人固有，见 §2.3）。要小就换 TinyGo，宿主一行不用改。
7. 上一轮 §7 里没验的那些（interpreter 引擎、c-archive、千次实例化泄漏、恶意客人）本轮同样没验。

---

## 9. 补两个缺口：实例重建 + 多个 wasm 工具模块（2026-11-04 二轮）

> §8.7 里列的两条已知缺口，这一节是它们的落地记录。规矩没变：**`internal/kernel/` 一个字节没动**（红线），改动只落在 `internal/wasmhost/`、`cmd/dshd/wasmload.go`（含测试）与这份文档。
> 环境同 §8：`go1.27.3 windows/amd64`、`wazero v1.12.0`、`GOCACHE=/tmp/gocache-wasm2`。

### 9.1 缺口 1：实例死了要能重建

#### 9.1.1 先把"流传的说法"钉准（实测，不是转述）

§8.7 第 1 条写的是"trap 或超时后 wazero 会把 module 关掉"。实测只有**一半**对：

```
# 客人 panic（{"boom":true}）之后
trap 之后 IsClosed=false
trap 之后第二次调用: out={"v":1,"summary":"收到 hi",…} err=<nil> IsClosed=false

# 超时（纯 wasm 夹具 spin()，deadline 100ms）之后
超时之后 IsClosed=true
超时之后再来一次: err=调用 fixture.add 失败: module closed with context deadline exceeded
```

两句话：

- **超时/取消**：wazero 真的把实例关了（`WithCloseOnContextDone` 的语义，§3.4 已经写过）。
- **trap**：wazero **不关**实例，而且那个实例看起来还能继续调（§3.3 也记过这条）。但客人是从 `fatalpanic` 里出来的，Go 运行时状态没人担保——"看起来能用"正是最坏的一种形态。

所以"实例不可用"**不能只靠 `IsClosed()` 判定**：只看它的话，trap 这条路根本不触发重建，于是测试里的"trap 后再调一次"会碰巧成功，把 bug 藏起来。判死必须由宿主自己做。

#### 9.1.2 做法

`internal/wasmhost/wasmhost.go`：

| 决定 | 做法 | 理由 |
|---|---|---|
| 留什么 | `Module` 常驻 `wazero.CompiledModule` + 装载参数（`reactor` 是否要跑 `_initialize`、`tool`、`timeout`） | 编译 4.4 MB Go 产物 **~0.9 s**（§3.5），实例化只要 **2–3 ms**。贵的那步必须常驻，便宜的那步可以重来。装载参数是宿主侧状态，重建后必须原样生效（否则工具名/超时会跟着丢） |
| 什么时候判死 | ① 一次调用返回后 `instanceFailureReason(err)` 命中 trap / `DeadlineExceeded` / `Canceled`；② 下一次调用前 `mod.IsClosed()` 为真 | ① 覆盖"wazero 没关但实例已不可信"；② 覆盖"wazero 自己关掉了"（超时、客人 `proc_exit`）。两个信号合起来才完整 |
| 什么时候重建 | `InvokeTool` / `Call` 进入时 `ensureLiveLocked`：关掉旧实例 → `InstantiateModule(compiled)`（**再跑一遍 `_initialize`**）→ 换 `mod`/`log`、`gen++`、`rebuilds++` | Go 客人的运行时状态是每实例一份，不重跑 `_initialize` 一进导出函数就 `runtime.notInitialized`（unreachable，§1.3） |
| 要不要在同一次调用里重试 | **不要** | 客人 panic 之前可能已经改了自己一半的状态（`keep` 切片、业务全局量）。宿主替它重放一遍，等于把"一次副作用"变成"两次"。重建留给**下一次**调用——那一次是模型/用户明确发起的新调用，语义才干净 |
| 重建失败怎么说 | 错误里两句话都在：`实例已失效（<为什么死>），**自动重建也失败了**: <为什么失败>`；再下一次调用还会带"上次重建也失败过（…）" | 只写"重建失败"会把排查起点从"插件的哪个错误"挪到"宿主的哪一层"，那是帮倒忙 |
| `Close()` 之后 | 不重建，直接报"已卸载，不能再调用" | 卸载是终点。自动重建等于把卸载撤销：调用方以为资源还了，进程里又装了一个回来 |
| 并发 | 判死、重建、调用全在同一条 `m.mu` 里；`gen` 是实例代数，`Rebuilds()` 是重建次数 | 两个 goroutine 同时撞上死实例：先到的重建，后到的在锁外等、拿到的是重建好的实例，再顺手判一次 → **只重建一次**。若"判死"与"重建"分在两段锁里，就会各建一个实例，漏一个在 wazero store 里白占线性内存 |

重建时实例名带代数（`plugin#2`、`plugin#3`…）：trap 之后旧实例可能还挂在 store 里，同名再实例化会撞 `module already instantiated`——不处理的话"重建"本身就成了新的坑。

代价：

- 常驻 `CompiledModule`：每个工具模块多留一份编译产物。不这样做，每次重建要重编 ~0.9 s，那不叫自愈，叫"每次失败后卡一秒"。
- 重建瞬间线性内存短暂翻倍（旧实例先 `Close` 再建新的，不会累积；Go 产物 3.5 MiB 量级）。
- 判死偏保守：trap 之后实例"看起来还能用"也丢掉。这是 §3.3 已经写过的结论，不是新代价。
- 实测数字（本轮，同一台机器）：重建 + 一次调用 **2.7–3.0 ms**；热路径 **60 µs/次**（100 次平均）。

### 9.2 缺口 2：一个 manifest 里多个 wasm 工具模块

#### 9.2.1 为什么"保留 `tool` 但不让 kernel 判重"做不到

三条事实叠起来就是死路：

1. **判重按字符串**：`internal/kernel/assemble.go:64` —— 能力名 → 提供者，同名即硬错（`能力 %q 被两个模块同时提供`）。
2. **装载层写的 `Provides` 会被覆盖**：`internal/kernel/module.go:167` —— `if len(e.Provides) > 0 { mod.Provides = e.Provides }`。manifest 里的 provides 会被**原样复制**到装配结果上。
3. **没有 Apply 之后的钩子**：`loadMods` 是「`loadWasmModules`（装 + 造 `kernel.Module`）→ `LoadManifest` → `Apply` → `TopoSort`」，装载层的输出在 Apply 之前就交出去了，而 TopoSort 在 Apply 之后跑。

所以装载层把 `Provides` 设成什么都白费：只要两个 manifest 行都写 `provides = ["tool"]`，Apply 就把同一个字符串复制到两个模块上，TopoSort 必然报错。
**不改 kernel 的话，唯一能动的就是写进 manifest 的那个字符串。** 想让两个模块共存，它们的能力键本来就必须是两个不同的字符串。

#### 9.2.2 选择：能力键带上工具名

| manifest 写法 | 什么时候用 | 装配结果里的 PROVIDES |
|---|---|---|
| `provides = ["tool"]` | 全署只有一个 wasm 工具模块（**既有 manifest 一个字都不用改**） | `tool` |
| `provides = ["tool:<tool_name>"]` | 有两个以上 wasm 工具模块时，每个模块写自己的 | `tool:echo` / `tool:hello` |

> **更新（§10）**：同一行 `provides` 里现在可以写**多个** `tool:<工具名>`——那对应"一个模块挂多个工具"，
> 一个工具一个键。单数缩写 `"tool"` 仍然只允许配一个工具。见 §10.2。

为什么这条是对的、不是将就：

- 能力键是**部署级身份**（`internal/kernel/registry.go:30` 那句"一个能力全部署只有一个提供者"）。两个 wasm 工具模块贡献的本来就是两件不同的东西，"谁贡献了哪个工具"写进能力名之后，kernel 的判重**自然成立**——不需要内核认识 wasm，也不需要给它开特例。
- 冒号后面必须是同一行的 `tool_name`，装载层校验（不一致就在启动时炸）：`modules graph`（能力键）与模型看到的工具表（工具名）必须说的是同一件事。两处不一致的错在 manifest 里只是几个字符，运行期完全看不出来。
- `"tool"` 这种缩写**全署只能用一次**。两个模块都写它时，装载层在**装载任何 wasm 之前**就报错，并给出能照着改的文案（不是 kernel 那句只有症状的"能力被两个模块提供"）。多工具部署下这条错**必然**会发生，让人去 kernel 的报错里反推 provides 该写什么，等于把成本推给最不了解内核的人。

否决的备选：

- **让 `tool_name` 隐式当开关**（provides 留空，装载层自己造 `tool:<名字>` 键）：那样"能力从哪来"就有了两个来源（manifest + 装载层），`modules graph` 会显示 manifest 里根本没写的键；而这一层的既有规矩是"能力的唯一来源是 manifest，装载层不另编一个"（§8.4）。**省下的只是几行 TOML，弄丢的是一条判据。**
- **改 kernel 支持"能力种类"/per-module 字段**：那是红线段，本轮明令不许动；而且为了一个显示用的装饰性能力去改装配内核，收益与风险不成比例。

代价（必须写出来）：

- **多工具部署要动 manifest**：加第二个 wasm 工具时，得把第一个模块的 `provides` 从 `["tool"]` 改成 `["tool:<它的 tool_name>"]`。单工具部署不受影响。
- 两种写法并存，读 manifest 的人需要知道区别 —— 由本节与 §8.4 承担。
- 能力键与工具名的一致性靠**校验**而不是靠推导：用户仍能写错，但一定在启动时炸，而不是在运行期各说各话。

### 9.3 改了哪些文件

| 文件 | 改了什么 |
|---|---|
| `internal/wasmhost/wasmhost.go` | `Module` 常驻 `compiled` / `reactor` / `dead` / `deadReason` / `rebuildErr` / `gen` / `rebuilds`；新增 `RebuildTimeout`、`Rebuilds()`、`InstanceGen()`、`ensureLiveLocked`、`rebuildLocked`、`markDeadLocked`、`instanceFailureReason`、`instanceName`；`Call` 改成与 `InvokeTool` 同一条路（进入前重建、失败后判死）；`Exports`/`Close` 的字段读取收进锁里 |
| `internal/wasmhost/abi.go` | `InvokeTool` 进入前 `ensureLiveLocked`；失败后按 `instanceFailureReason` 判死（**不在同一次调用里重试**）；`wrapCallError`/文档注释改成"已判死，下次调用自动重建" |
| `internal/wasmhost/rebuild_test.go`（新） | 5 条（§9.4 第 1 组） |
| `cmd/dshd/wasmload.go` | `wasmToolCap` + `wasmToolCapPrefix`；`splitWasmProvides` 认两种写法；新增 `checkWasmToolCapKey` / `dupWasmToolCapErr`；装载循环拆成"先校验 manifest、再读 wasm"两遍；`hintForWasmError` 的超时文案改成"下次调用自动重建，不用重启宿主" |
| `cmd/dshd/wasmload_test.go` | 新增 4 条（§9.4 第 2 组） |
| `docs/wasm-tool-abi.md` | §8.4 的 provides 说明、§8.7 第 1/2 条的指向、这一节 |

**没动**：`internal/kernel/`、`internal/transport/`、`modules/`、`dsh.toml`、`cmd/dshd/serve*.go`、`cmd/dshtui/`、`PLAN.md`、`AGENTS.md`。`scripts/build-wasm-fixture.py` 与两个夹具**都没动**（这轮不需要新客人行为：trap 用夹具已有的 `{"boom":true}`）。

### 9.4 验收（逐条，真实输出）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export GOCACHE=/tmp/gocache-wasm2

$ go build ./... && go vet ./...
build OK / vet OK（无输出）

$ go test ./internal/wasmhost ./cmd/dshd -count=1
ok  	dshgo/internal/wasmhost	8.746s
ok  	dshgo/cmd/dshd	8.609s

$ go test ./... -count=1
exit=0，47 个包 ok，无 FAIL（最后几行是 tmp/ 下若干历史探针包的 `[no test files]`）

$ go build -o tmp/dshd.exe ./cmd/dshd
OK（18,844,160 字节）
```

**第 1 组：缺口 1（`internal/wasmhost`）**

```
--- PASS: TestInvokeToolRebuildsAfterTrap (0.87s)   # 先 trap（失败）→ 再正常调（成功），Rebuilds()==1
--- PASS: TestConcurrentCallsRebuildOnce (0.82s)    # 8 个 goroutine 撞死实例，Rebuilds()==1，8 个全成功
--- PASS: TestRebuildFailureSaysWhy (0.74s)         # 重建也失败 → 错误同时说清"为什么死"与"为什么重建失败"
--- PASS: TestCallTimeoutThenRebuild (0.15s)        # 超时把实例关掉（IsClosed=true）→ 下一次 add(2,3)=5
--- PASS: TestClosedModuleDoesNotRebuild (0.77s)    # Close 是终点，不重建
```

三条断言值得单独抄出来（它们是"真的重建了"的证据，不是"碰巧成功"）：

```
# trap 之后第二次调用成功，且中间重建过一次
Rebuilds()=1  InstanceGen()=2  ToolName()="echo"（工具名没因为重建丢）

# 8 个并发
Rebuilds()=1（onRebuild 里故意 sleep 30ms 撑开窗口：实现若在锁外重建，这里会是 2）

# 重建失败（先把 runtime 关掉再撞死实例）
wasm 模块 "toolplugin" 的实例已失效（上一次调用里客人 trap（插件崩溃）），**自动重建也失败了**: runtime closed with exit_code(0)。这个模块现在调不动——先按上面的原因修插件或环境再重启宿主；原样重试不会成功
```

**第 2 组：缺口 2（`cmd/dshd`，走 `loadMods → kernel.Assemble → spi.Tool.Exec` 的同一条生产路径）**

```
--- PASS: TestWasmTwoToolModulesCoexist (2.20s)          # 两个 wasm 工具模块共存，两个工具都在表里且都调得通
--- PASS: TestWasmTwoToolsWithPlainToolKeyFails (0.01s)  # 两个都写 ["tool"] → 装载前的可照着改的报错
--- PASS: TestWasmToolCapKeyMustMatchToolName (0.01s)    # tool:eho vs tool_name="echo" → 启动就炸
--- PASS: TestWasmToolRebuildsAfterTrapThroughExec (0.86s) # 同一个 spi.Tool 句柄：先炸一次、再成功一次
```

**第 3 组：真机（一个 manifest 两个 wasm 工具模块）**

`tmp/wasm-two-tools.toml`：两个 `[[module]]`，同一个 `toolplugin.wasm`，`provides = ["tool:echo"]` / `["tool:hello"]`。

```
$ ./tmp/dshd.exe modules graph --manifest tmp/wasm-two-tools.toml
# 激活顺序（由能力依赖决定，不是 manifest 行顺序）
MODULE                 BACKEND   PROVIDES                   REQUIRES
tools                  static    tools,toolrunner
wasm-echo              wasm      tool:echo                  tools
wasm-hello             wasm      tool:hello                 tools

# wasm 后端（已装载 / 已校验；TOOL 列 = 注册进工具表的工具名，空表示不提供能力、只能 `dshd wasm call` 手动调）
MODULE                 BACKEND   DIGEST       EXPORTS            TOOL                   PATH
wasm-echo              wasm      35cde81846d2 _initialize,alloc,call echo                   D:\…\internal\wasmhost\testdata\toolplugin.wasm
wasm-hello             wasm      35cde81846d2 _initialize,alloc,call hello                  D:\…\internal\wasmhost\testdata\toolplugin.wasm
```

```
$ ./tmp/dshd.exe tools --manifest tmp/wasm-two-tools.toml
[ { "name": "echo",  "description": "把 text 原样回显（wasm 夹具工具 · 一号）", … },
  { "name": "hello", "description": "把 text 原样回显（wasm 夹具工具 · 二号）", … } ]
```

```
$ ./tmp/dshd.exe exec --tool echo --args '{"text":"一号工具在跑"}' --manifest tmp/wasm-two-tools.toml
INFO 工具调用 tool=echo ms=1 ok=true args_bytes=29
{ "Summary": "收到 一号工具在跑", "Text": "echo:一号工具在跑", … }        [exit=0]

$ ./tmp/dshd.exe exec --tool hello --args '{"text":"二号工具在跑"}' --manifest tmp/wasm-two-tools.toml
INFO 工具调用 tool=hello ms=1 ok=true args_bytes=29
{ "Summary": "收到 二号工具在跑", "Text": "echo:二号工具在跑", … }       [exit=0]
```

两个都写 `provides = ["tool"]` 时的真机报错（这就是"多工具部署必然踩到"的那条，必须能照着改）：

```
$ ./tmp/dshd.exe tools --manifest <两个都写 ["tool"] 的 manifest>
dshd: 模块 "wasm-echo" 与 "wasm-hello" 都写了 provides = ["tool"]：kernel 只允许一个能力一个提供者，
所以 "tool" 这种缩写全署只能用一个。要同时挂多个 wasm 工具，请让每个模块写各自的能力键：
provides = ["tool:<tool_name>"]（例：provides = ["tool:echo"]，冒号后必须与同一行的 tool_name 一致）
——能力键就是"哪个模块提供了哪个工具"，两个模块贡献的不是同一个东西          [exit=1]
```

### 9.5 本轮没做 / 已知限制

1. **`-race` 没跑**：本机 `CGO_ENABLED=0`、没有 gcc，Go 的 race detector 需要 cgo。并发那条用例因此靠 `Rebuilds()==1` 的**计数断言**证明"只重建一次"，不是靠 race 检测器；也没做压测。
2. **真机演示"重建"的边界**：`dshd exec` 是"一次进程一次调用"，trap 与重建在同一个进程里，`exec` 只能演示 trap 报错正确；重建的现场证据是 `TestWasmToolRebuildsAfterTrapThroughExec`（同一条生产路径，只是不换进程）。要真机演示需要长驻进程（`serve`）里连调两次同一个工具，那要过模型/一次 turn，本轮不做。
3. **`cmd/dshd/doctor.go` 的 wasm 文案仍然过期**（不在本轮边界内）：它无条件说"wasm 模块不提供工具，只能 `dshd wasm call` 手动调"，现在不仅对 `provides=["tool"]` 的模块是错的，还多了 `tool:<tool_name>` 这种写法没提。
4. **ABI 没变**：仍是一模块一工具（`request.tool` 字段早就留好了，一个模块挂多工具那天不用改信封）。客人侧源码与产物一个字节没动。
5. §8.7 第 3 / 6 / 7 条（schema 仍 manifest-pinned、体积 4.4 MB、interpreter 引擎等）**不变**。

> §9.5 第 4 条在这里被兑现：`request.tool` 字段一直在，多工具那天确实**没有改信封**。见 §10。

---

## 10. 一个模块挂多个工具（2026-11-04 三轮）

> 起因：§8.7 第 2 条与 §9.2 解决的是**多个模块**共存，但一个模块仍然只提供一个工具。而请求信封里的 `tool` 字段从 §3.2 起就留着了（"将来「一个模块挂多个工具」不需要改 ABI"）。这一节就是那一天：**把它用起来**。
> 环境同上：`go1.27.3 windows/amd64`、`wazero v1.12.0`、`GOCACHE=/tmp/gocache-wtool2`。

### 10.1 一句话结论

manifest 的一个 `[[module]]` 现在可以声明**多个**工具（`[[module.tool]]` 数组）：每个工具注册成**独立**的 `spi.Tool`（各自的名字/描述/schema/caps/超时），调用时宿主把**当前**工具名写进请求信封的 `tool` 字段，客人按它分流。

- **ABI 一个字节没改**：`internal/wasmhost/` 这轮没有改动（`abi.go` / `wasmhost.go` 的 `git status` 是干净的）。
- **旧写法一个字不用改**：`tool_name` / `tool_description` / `tool_schema` / `tool_caps` 继续有效，仍是"一个模块一个工具"。
- **两种写法混用 = 启动即炸**，宿主不猜优先级。
- 能力键仍沿用 §9 的 `tool:<工具名>`：**一个工具一个键**，与声明逐个对不上就启动即炸。

### 10.2 manifest 新写法

```toml
[[module]]
id       = "wasm-text"
backend  = "wasm"
path     = "text.wasm"                       # 一个模块，一个 .wasm
provides = ["tool:echo", "tool:upper"]       # 一个工具一个键（§9.2 的写法，只是可以写多个）

[[module.tool]]
name        = "echo"                          # 必填：模型看到的工具名
description = "把 text 原样回显"                # 必填：模型看到的「什么时候用它」
schema      = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'''
caps        = []                              # 可选：writes / executes / network / destructive
timeout_ms  = 2000                            # 可选：这一项自己的 deadline（没写用模块级，再没写 10000）

[[module.tool]]
name        = "upper"
description = "把 text 里的 ASCII 字母转成大写"
schema      = '''{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}'''
timeout_ms  = 5000
```

同一个模块也可以只挂一个 `[[module.tool]]`（等价于旧写法，此时 `provides = ["tool"]` 也允许）。

| 写法 | 什么时候用 | `modules graph` 的 PROVIDES |
|---|---|---|
| 旧：`tool_name` / `tool_description` / `tool_schema` / `tool_caps` | 一个模块一个工具（**既有 manifest 全部属于这一类**） | `tool` 或 `tool:<工具名>`，一个键 |
| 新：`[[module.tool]]` × N | 一个模块 N 个工具 | `tool:<名1>,tool:<名2>,…`，N 个键 |

**能力键的三条硬规则**（都来自 §9.2 那条"kernel 按字符串判重、Apply 原样复制 provides"）：

1. 每个工具一个 `tool:<工具名>`；`provides` 里的键集合必须与声明的工具集合**逐个对齐**（少一个、多一个、写错一个都炸）。
2. `provides = ["tool"]` 是**单工具缩写**：配多个工具就炸，也不能与 `tool:<名>` 混写。
3. 声明了 `[[module.tool]]` 却没写任何工具键 → 炸（能力的唯一来源是 manifest，装载层不替它补键，§8.4）。

报错文案都是"能照着改"的，例如：

```
dshd: 模块 "wasm-text" 的 provides 与声明的工具对不上（这一行声明了 2 个工具：echo、upper）；
能力键 "tool:ghost" 没有对应的工具声明。正确写法：provides = ["tool:echo", "tool:upper"]
（冒号后与 [[module.tool]].name 逐个对齐）
```

混写（模块级旧字段 + `[[module.tool]]`）的报错：

```
dshd: 模块 "wasm-text" 同时写了单工具字段（tool_name / tool_description / tool_schema / tool_caps）
和 [[module.tool]] 数组：两种写法不能混用，宿主不会替你猜哪个优先。只有一个工具就删掉
[[module.tool]] 只留 tool_name 那几行；有多个工具就删掉 tool_* 那一组，每个工具写一节 [[module.tool]]   [exit=1]
```

> **TOML 位置语义的坑**：模块级字段必须写在第一节 `[[module.tool]]` **之前**，写在后面就属于那个子表了（go-toml 静默接受未知键，不会报错）。所以"混写"的现实形态是"模块级字段在前、数组在后"——这一形态由 `TestWasmMixedToolFormsRejected` 钉住。

### 10.3 路由怎么落的：一把锁，不是第二个模块

`call` 只有一个入口，客人怎么知道"这次我是谁"？答案就是信封里的 `tool` 字段。但 `abi.go` 里它是**模块级状态**：

```go
func (m *Module) SetToolName(name string)   // 写 m.tool
func (m *Module) InvokeTool(ctx, args)      // 把 m.tool 编进 Request{Tool: m.tool, …}
```

`InvokeTool` **没有 per-call 工具名参数**（协议本来就没有，这轮也不改协议）。所以装载层做了一个 `wasmToolRouter`：

| 决定 | 做法 | 理由 |
|---|---|---|
| 谁来写工具名 | 每次 `Exec` 都调 `SetToolName(t.name)`，且与 `InvokeTool` 在**同一把互斥锁**里 | 只设一次不行（下一个工具会串台）；设了不锁更不行（A 设完名、B 又设一遍，A 就会以 B 的身份跑起来——**结果对、身份错**，最难查的一种） |
| 这把锁会不会拖慢 | 不会：一个实例本来就只能跑一个 `call`（§3.4，同步导出不能重入），同一模块上的两个工具本来就得串行 | 锁只是把"串行"从"实例"扩到"写名字 + 调用"，二者本来同域 |
| 超时 | 模块级 `SetTimeout` 取**全部工具里最大的**那个当安全上限；每个工具在 `Exec` 里用自己的 `timeout_ms` 再收一次 `ctx` | 取最小会冤枉超时长的工具；`context.WithTimeout` 天然取更早的那个，于是**每个工具拿到的正是自己声明的 deadline** |
| 实例重建 | 不变（§9.1）：`compiled` 常驻、死了重建；重建后 router 里的模块句柄还是同一个 | 工具名/超时是宿主侧状态，不在客人里 |

**否决的备选：同一个 `.wasm` 文件 Load N 次**（每个工具一个 `wasmhost.Module`）。

- 那等于把"一个模块两个工具"退回成"两个模块"：N 份编译产物（4.4 MB 的 Go 产物每份 ~0.9 s 编译，§3.5）、N 个常驻实例（每实例 3.5 MiB）、N 次 `_initialize`；
- 而且**信封里的 `tool` 字段依然没用上**——每个模块各有一个名字，客人还是不需要看它。这一轮要证明的恰恰是"客人按 `tool` 分流，所以一个实例就够"。

### 10.4 客人侧：按 `tool` 分流

夹具（`internal/wasmhost/testdata/toolplugin/main.go`）现在有两个工具，**同一个 `.wasm`、同一份实例**：

```go
if req.Tool == "upper" {
    return writeReply(upperReply(args.Text))
}
return writeReply(echoReply(args.Text))   // echo 是默认分支
```

为什么 `echo` 要做默认分支：`internal/wasmhost` 里有些用例是**直接调 `Module.InvokeTool`** 而不设工具名的（这时 `m.tool` 是模块名 `toolplugin`），它们不该因为这一轮多了一个工具就换行为。所以夹具对"不是 upper"的一切（含空串、模块名）都走 echo——**这是夹具的兼容选择，不是 ABI 的规定**。

于是"路由有没有生效"这个问题的判据非常干净（直接把 ABI 调四次，不经过装配层）：

```
请求信封 tool="toolplugin" → Text="echo:Hi 你好"    ← 模块默认名，落回 echo
请求信封 tool="wasm-text"  → Text="echo:Hi 你好"
请求信封 tool="echo"       → Text="echo:Hi 你好"
请求信封 tool="upper"      → Text="upper:HI 你好"   ← 只有它换分支
```

**没有 `tool` 字段 / 字段填错，upper 会安静地返回 echo 的结果。** 所以工具层的用例不能只断言 `err == nil`（那会全绿），必须断言**输出不同**——见 §10.6 的 `TestWasmTwoToolsRouteByToolField`。

### 10.5 改了哪些文件

| 文件 | 改了什么 |
|---|---|
| `cmd/dshd/wasmload.go` | `wasmEntry.Tool []wasmToolDecl`（`[[module.tool]]`）；`newWasmToolSpecs`（两种写法二选一、混写报错、重名报错、逐项超时默认）；`hasLegacyToolFields`；`splitWasmProvides` 改为返回**多个**能力键；`checkWasmTools`（键集合 ↔ 工具集合逐个对齐，含 bare `tool` 规则与"同一个键写两次"）；`noWasmToolCapErr` / `capKeysOf` / `quoteJoin`；`wasmToolRegister(specs, router)` 一个 Register 注册全部工具；`wasmToolRouter` + `invokeTool`（写名字 + 调用原子化、per-tool ctx deadline）；`maxWasmToolTimeout`；`wasmTool` 改持 `router` 与自己的 `timeout`；`wasmLoaded.tool` → `tools []string`；`writeWasmSection` 的 TOOL 列改成逗号分隔 |
| `cmd/dshd/wasmload_test.go` | 新增 7 条（§10.6 第 2 组）；两处老断言跟着字段改名（`x.tool` → `x.tools`），**其余老用例一行没改** |
| `internal/wasmhost/testdata/toolplugin/main.go` | 新增 `upper` 工具（`upperReply` / `upperASCII`）与 `echoReply`，按 `req.Tool` 分流；`boom` / 空输入的语义不变 |
| `internal/wasmhost/testdata/toolplugin.wasm` | 重新生成：**4,459,916 字节**（旧 4,458,584，+1,332），sha256 `ec3391fa…`，脚本连跑两次同值 |
| `docs/wasm-tool-abi.md` | 这一节 |
| `docs/wasm-plugin-author-guide.md` | §2 的 `tool` 字段说明、§4 manifest 两种写法、新增 §5「一个模块写多个工具」、§6 排查里的 graph 样子 |

**没动**（本轮边界）：`internal/kernel/`、`internal/wasmhost/`（**ABI 与宿主内核件一个字节没改**）、`internal/transport/`、`modules/`、`dsh.toml`、`cmd/dshd/serve*.go`、`cmd/dshtui/`、`cmd/dshd/doctor.go`、`PLAN.md`、`AGENTS.md`、`scripts/build-wasm-fixture.py`（脚本本身这轮不需要改，只是重跑）。

### 10.6 验收（逐条，真实输出）

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
export GOCACHE=/tmp/gocache-wtool2

$ go build ./... && go vet ./...
BUILD+VET OK（无输出）

$ go test ./internal/wasmhost ./cmd/dshd -count=1
ok  	dshgo/internal/wasmhost	11.041s
ok  	dshgo/cmd/dshd	12.219s

$ go test ./... -count=1
go test exit=0；ok 行数 47；FAIL 行 0（grep 'FAIL\|panic:' 为空）

$ python scripts/build-wasm-fixture.py
生成 internal\wasmhost\testdata\fixture.wasm 60 字节（手写纯 wasm：add / spin）
+ (cd internal\wasmhost\testdata\toolplugin && build -buildmode=c-shared -trimpath -o …\toolplugin.wasm .)
生成 internal\wasmhost\testdata\toolplugin.wasm 4459916 字节（Go reactor：_initialize / alloc / call）
$ sha256sum internal/wasmhost/testdata/toolplugin.wasm        # 连跑两次同值
ec3391fa4cea70ae44ec9e18b1a6b64682e5864dd602fb02923b685e6101e36a

$ go build -o tmp/dshd.exe ./cmd/dshd
OK（18,869,760 字节）
```

**第 1 组：`internal/wasmhost` 全部老用例（ABI 没改，夹具改了）**

```
ok  	dshgo/internal/wasmhost	9.106s   # 13 条全绿，一行没改
```

（夹具对"不是 upper"的一切仍走 echo，所以 `TestInvokeToolRoundTrip` / `TestInvokeToolBusinessErrorIsNotTrap` / `TestInvokeToolTrapIsError` / 重建那 5 条都不受影响。）

**第 2 组：`cmd/dshd` 的全部 wasm 用例（22 条）**

```
--- PASS: TestWasmModuleAssemblesAndShowsInGraph (0.03s)
--- PASS: TestWasmDigestMismatchFails (0.01s)
--- PASS: TestWasmMissingFileFails (0.01s)
--- PASS: TestWasmNonWasmFileFails (0.01s)
--- PASS: TestWasmProvidesRejected (0.01s)
--- PASS: TestWasmToolProvidesOneTool (1.03s)
--- PASS: TestWasmToolBusinessErrorGivesHint (0.87s)
--- PASS: TestWasmToolTrapGivesHint (0.90s)
--- PASS: TestWasmWithoutProvidesKeepsToolTable (0.86s)
--- PASS: TestWasmDisabledSkipped (0.01s)
--- PASS: TestWasmToolManifestValidation (0.03s)
--- PASS: TestWasmTwoToolModulesCoexist (1.82s)
--- PASS: TestWasmTwoToolsWithPlainToolKeyFails (0.02s)
--- PASS: TestWasmToolCapKeyMustMatchToolName (0.02s)
--- PASS: TestWasmToolRebuildsAfterTrapThroughExec (0.82s)
--- PASS: TestWasmModuleServesTwoTools (0.84s)              ← 新：一个模块两个工具 → 表里两个名字
--- PASS: TestWasmTwoToolsRouteByToolField (0.80s)          ← 新：两个工具逻辑各自不同（tool 字段真被路由）
--- PASS: TestWasmLegacySingleToolFormStillWorks (0.80s)    ← 新：旧写法仍能用
--- PASS: TestWasmMixedToolFormsRejected (0.01s)            ← 新：混写 → 启动报错
--- PASS: TestWasmToolCapKeysMustMatchDeclaredTools (0.04s) ← 新：6 个子用例，能力键必须逐个对齐
--- PASS: TestWasmMultiToolDeclValidation (0.03s)           ← 新：5 个子用例，逐项必填 / 重名
--- PASS: TestWasmSingleItemArrayWithBareToolKey (0.84s)    ← 新：数组只写一项时 provides=["tool"] 仍成立
ok  	dshgo/cmd/dshd	9.862s
```

核心的四条各自钉的是什么（另外三条是校验表的用例：能力键对齐 6 例、逐项必填 5 例、数组单写成缩写 1 例；缺核心任一条，"一个模块多个工具"就不成立）：

| 用例 | 钉住的事实 |
|---|---|
| `TestWasmModuleServesTwoTools` | 装配结果里**只有 1 个** wasm 模块（不是把文件挂了两遍）却有 **2 个**能力键、**2 个**工具名；两个工具的描述没串台；两个都调得通（各自 `timeout_ms` 也不同）；`modules graph` 的 TOOL 列是 `echo,upper` |
| `TestWasmTwoToolsRouteByToolField` | **同一个模块**上 echo 返回 `echo:Hi 你好`、upper 返回 `upper:HI 你好`，且按 echo→upper→echo→upper 交叉调用。路由没生效的话 upper 会安静地返回 echo 的结果——所以断言的是**输出值**，不是"没报错" |
| `TestWasmLegacySingleToolFormStillWorks` | 旧 manifest 从解析一路走到 `Exec`：能力键仍是 `tool`、`tool_caps=["writes"]` 仍生效、模块级工具名仍是 `echo`（`dshd wasm call` 用） |
| `TestWasmMixedToolFormsRejected` | 模块级 `tool_name` + `[[module.tool]]` 同时出现 → 启动即炸；错误同时点出两种写法、模块 id 和"不能混用"；失败后不留已装载句柄 |

**第 3 组：真机（一个模块两个工具）**

`tmp/wasm-multi-tool.toml`：**一个** `[[module]]`、一个 `.wasm`、两个 `[[module.tool]]`。

```
$ ./tmp/dshd.exe modules graph --manifest tmp/wasm-multi-tool.toml
# 激活顺序（由能力依赖决定，不是 manifest 行顺序）
MODULE                 BACKEND   PROVIDES                   REQUIRES
tools                  static    tools,toolrunner
wasm-text              wasm      tool:echo,tool:upper       tools

# wasm 后端（已装载 / 已校验；TOOL 列 = 注册进工具表的工具名（多个用逗号分隔），…）
MODULE                 BACKEND   DIGEST       EXPORTS            TOOL                   PATH
wasm-text              wasm      ec3391fa4cea _initialize,alloc,call echo,upper             D:\…\internal\wasmhost\testdata\toolplugin.wasm
```

```
$ ./tmp/dshd.exe tools --manifest tmp/wasm-multi-tool.toml
[
  { "name": "echo",  "description": "把 text 原样回显（wasm 夹具工具 · echo）",             … },
  { "name": "upper", "description": "把 text 里的 ASCII 字母转成大写（wasm 夹具工具 · upper）", … }
]
```

```
$ ./tmp/dshd.exe exec --tool echo  --args '{"text":"Hi 你好"}' --manifest tmp/wasm-multi-tool.toml
  "Summary": "收到 Hi 你好",
  "Text": "echo:Hi 你好",

$ ./tmp/dshd.exe exec --tool upper --args '{"text":"Hi 你好"}' --manifest tmp/wasm-multi-tool.toml
  "Summary": "大写 HI 你好",
  "Text": "upper:HI 你好",
```

同一个 manifest 上跑 `dshd doctor` 也不再说它不提供工具（`wasm-text（已注册工具，模型可见；…）`）——`doctor.go` 本轮没改，它读的是 `Register != nil`。

**第 4 组：真机（旧写法一个字没改）**

`tmp/wasm-legacy-tool.toml`（只有 `tool_name` 那一组）：

```
$ ./tmp/dshd.exe modules graph --manifest tmp/wasm-legacy-tool.toml
wasm-echo              wasm      tool                       tools
wasm-echo              wasm      ec3391fa4cea _initialize,alloc,call echo

$ ./tmp/dshd.exe exec --tool echo --args '{"text":"旧写法"}' --manifest tmp/wasm-legacy-tool.toml
  "Summary": "收到 旧写法",
  "Text": "echo:旧写法",
```

**第 5 组：真机（混写 → 启动报错，exit=1）**

```
$ ./tmp/dshd.exe tools --manifest tmp/wasm-mixed-tool.toml ; echo exit=$?
dshd: 模块 "wasm-text" 同时写了单工具字段（tool_name / tool_description / tool_schema / tool_caps）
和 [[module.tool]] 数组：两种写法不能混用，宿主不会替你猜哪个优先。…
exit=1
```

### 10.7 旧写法为什么还安全

四条，从解析到运行期各一条：

1. **解析层结构没动**：`wasmEntry` 只**新增** `Tool` 字段，`tool_name` / `tool_description` / `tool_schema` / `tool_caps` / `timeout_ms` 原样保留。`newWasmToolSpecs` 在 `len(e.Tool)==0` 时把老字段打包成"唯一一项"，走的是**同一个** `newWasmToolSpec`（校验逻辑只有一份，不是两套实现各自漂移）。
2. **老报错文案还在**：缺名 / 缺描述 / 缺 schema / JSON 对象 / caps 拼错这几条错误里仍然带 `tool_name` / `tool_description` / `tool_schema` / `tool_caps` 字样——`TestWasmToolManifestValidation` 六个子用例**一行没改**仍然通过，它们钉的就是老写法的形状。
3. **能力键语义没变**：老写法仍然写 `provides = ["tool"]`（全署唯一，§9.2）或 `["tool:echo"]`。单工具模块 `checkWasmTools` 两种都放行，所以 §9 那批老 manifest 与 `TestWasmTwoToolModulesCoexist` 全部照旧。
4. **运行期是幂等的**：单工具模块装载时仍 `SetToolName(specs[0].name)`，`dshd wasm call` 这类不经过 `spi.Tool` 的入口拿到的默认工具名与以前一样；`Exec` 里的路由只是在同一把锁里**再写一遍同样的名字**。而"没写 provides 的 wasm 模块不改变工具表"这条硬要求仍由 `TestWasmWithoutProvidesKeepsToolTable` 的**逐字节比对**守着。

### 10.8 本轮没做 / 已知限制

1. **工具名跨模块的唯一性没在装载层提前查**：两个不同模块声明同一个工具名时，报错来自工具表的 `Register`（"wasm 工具 X 注册失败"），而不是 manifest 校验那一遍。仍是启动期失败，只是文案不如 provides 那几条具体。要提前查就得在 `loadWasmModules` 里维护全局工具名表——本轮没做（收益是一条更早的错，风险是多一处要跟注册表语义同步的状态）。
2. **`dshd wasm call` 仍用模块级默认工具名**（它是"手动调试单个模块"的入口，不经过 `spi.Tool`）。多工具模块里这个默认值是模块 id，不是某个工具名；要指定工具仍得走 `dshd exec --tool`。
3. **模块级 `timeout_ms` 取的是最大值**：`Exec` 会用自己的 `ctx` 收紧，所以走 `spi.Tool` 的每个工具都拿到自己声明的 deadline；不经过 `Exec` 的入口（上面第 2 条）拿到的是最大那个。
4. **宿主无法验证客人真的按 `tool` 分了流**：客人完全可以无视这个字段。判据只能是测试与夹具（`TestWasmTwoToolsRouteByToolField` 断言两个工具**输出不同**）。这一点写进作者手册了。
5. **`-race` 没跑**（同 §9.5 第 1 条）：`wasmToolRouter` 的并发正确性靠"同一把锁里写名字 + 调用"的设计与单测，不是 race 检测器；也没做压测。
6. **`cmd/dshd/doctor.go` 的 wasm 文案仍偏旧**（不在本轮边界）：它读 `Register != nil` 判"已注册工具"，对多工具模块是正确的；但文案里"多工具的模块用 `provides=["tool:<工具名>"]` 各占一个能力键"只说了跨模块的场景，没提 `[[module.tool]]`。
7. §8.7 / §9.5 里没验的那些（interpreter 引擎、c-archive、千次实例化泄漏、恶意客人、体积 4.4 MB）**不变**。
