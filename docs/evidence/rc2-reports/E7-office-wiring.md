# E7 · Office 面接线（officeKit 方法路由 + 三个 Office 工具 + 共享 provider）

- 任务板：`task-21`（owner `e7-officewire`，claim rev 2 → in_progress）
- 工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`（共享工作树；`cmd/dshd/serve.go`、
  `internal/transport/server.go` 只做最小 edit，改前逐次重读锚点）
- 基线：C4 已在同一工作树里实现 `internal/officekit/**`、`internal/officepdf`（适配层）、
  `internal/transport/handlers_office_kit_rc2.go`、`modules/tool/office/**`（报告
  `tmp/GO8-RC2/reports/C4-office-kit.md`）。**本任务只接线，没有重写任何 C4 实现。**

## 0. 一句话结论

Office 面已接进产品：`/api/officeKit.generation|render` 经真实 `server.ServeHTTP` 分派到
真引擎，**端到端拿到 21379 字节真 PDF**（`%PDF-1.7` + `%%EOF`）；旧的 `officeToPdf.*`
与新的 `officeKit.*` 共用**同一个** `officekit.Provider`（generation 一致）；
三个工具 `office_convert / office_recalculate / office_render` 已进 `builtinModules`
与产品 manifest 白名单。引擎缺失时两个方法仍 `501 + code=unavailable + capableWhen +
capability.tried`，四操作面（render/convert/recalculate/images）**没有假渲染**。

| 判据 | 结果 | 证据 |
|---|---|---|
| dispatch 级真 PDF（transport 包） | ✅ 21379 字节 | `tmp/GO8-RC2/e7-evidence/green-dispatch3.log`：`分派级真实 PDF：21379 字节` |
| 产品适配器 e2e（cmd/dshd 包） | ✅ 21379 字节 | `e7-officekit-wiring.pdf`（21379 B，`%PDF-1.7`） |
| **真实 `dshd serve` 进程** HTTP 级 | ✅ 21379 字节 | `e7-serve-real.pdf`（21379 B）；render 响应 28807 B（base64 信封） |
| 新旧两面同 provider | ✅ 两个 generation 完全相同 | `officeKit.generation` = `officeToPdf.generation` = `officekit-17442830daa9e04b`；两个面各出 21379 B PDF（`serve2-kit-render.pdf` / `serve2-legacy-render.pdf`） |
| 工具面真转换 | ✅ 21379 字节，落 `<home>/office-kit/outputs/…/probe.pdf` | `tool-office-convert.log`（`ok=true ms=591`） |
| 没引擎 | ✅ 501 + `unavailable`/`capableWhen`/`capability`/`tried`，无 `%PDF` | `TestOfficeKitWiringWithoutEngineReports501` 等 |

## 1. 四步接线（file:line）

### 步骤 1 · `internal/transport/server.go:579-586`（+7 行）

```go
	// officeKit 两方法（rc.2 取代 officeToPdf）：真实引擎在 internal/officekit。
	// 必须早于下面 officeToPdf 与 media 的旧分支：officeKit 是 rc.2 起的权威面，
	// 两者方法名不重叠，但顺序上仍让新面先认领（旧面只是兼容保留）。
	if IsOfficialOfficeKitMethod(name) {
		s.ServeOfficialOfficeKitAPI(w, r, name)
		return
	}
```

位置：`IsOfficialAccountMethod` 之后、`IsOfficialOfficePDFMethod`（旧 `officeToPdf.*`）之前，
符合「officeKit 优先于旧 officeToPdf 族」。

### 步骤 2 · `cmd/dshd/serve_office_kit.go`（新文件，218 行）

把测试里的 `realOfficeKitAdapter` / `toOfficeKitAPIError` 原样搬成产品代码，并补两件产品才需要的事：

| 行 | 内容 |
|---|---|
| `:32` | `type officeKitAPI struct`（provider + home + 会话 cwd 解析器） |
| `:47` | `newOfficeKitAPI(provider *officekit.Provider, home ...string) *officeKitAPI`（报告 §7.4 的 `newOfficeKitAPI(provider)` 形状仍然成立；`home` 可选） |
| `:56` | `withSessionCwd(...)` → serve 注入 `src.Cwd`，GUI 传的**工作区相对路径**才能解析 |
| `:63` | `newOfficePDFAPIWith(home, kit)` → `officepdf.Wrap(home, kit)`（旧面复用同一 provider） |
| `:69` | `Generation`：capability 逐字段映射（无引擎给真值 `officekit-unavailable`） |
| `:80` | `Render`：`absolutePath` → `provider.Render(ctx, abs, priority)`；错误走 `toOfficeKitAPIError` |
| `:102` | `absolutePath`：绝对路径原样 → 会话 cwd（`src.Cwd`）→ `<home>/workspaces/<scope>`；`..` 逃逸按 `invalid-request` 拒 |
| `:143` | `toOfficeKitAPIError`：unavailable→501 / invalid-*·unsupported·too-large·invalid-output·source-changed→400 / timeout→504 / busy→429 / 其余→502（C4 §4 表格逐行一致） |
| `:169-217` | 进程内共享 provider 槽：`shareOfficeKitProvider(home, p)` / `sharedOfficeKitProvider(home)` |

### 步骤 3 · `cmd/dshd/serve.go:865-875`（+12 行，替换 1 行）

```go
	// Office 面（rc.2 新权威面 officeKit.* + 兼容保留的 officeToPdf.*）共用**同一个**
	// provider：同一引擎、同一准入队列、同一 generation、同一产物仓。
	// provider 没有引擎也照样建（官方语义「It now registers without Kit」），
	// 具体操作才按 unavailable 失败 → Transport 出 501 + capableWhen。
	officeKitProvider := officekit.Open(officekit.Options{Home: *home,
		OutputDirectory: filepath.Join(*home, "office-kit", "outputs")})
	defer officeKitProvider.Close()
	// 登记为进程内共享实例：工具面（office_convert/recalculate/render）从这一份取，
	// 避免「Transport 一套引擎状态、工具又一套」（见 cmd/dshd/serve_office_kit.go）。
	shareOfficeKitProvider(*home, officeKitProvider)
	srv.AttachOfficialOfficeKitAPI(newOfficeKitAPI(officeKitProvider, *home).withSessionCwd(src.Cwd))
	srv.AttachOfficialOfficePDFAPI(newOfficePDFAPIWith(*home, officeKitProvider))
```

另加 import：`"dshgo/internal/officekit"`（`serve.go:24`）。

### 步骤 4 · `cmd/dshd/main.go:180`（+1 行模块 + 1 行 import）

```go
		// Office 三工具（office_convert / office_recalculate / office_render）。
		// provider 从共享槽取：serve 进程里就是 cmdServe 建的那一份，run/tools/exec
		// 里则由这里建一份 —— 关键是**同一进程只此一份**（同一引擎/同一准入队列/
		// 同一 generation），否则工具面与 Transport 面各排队一半、generation 打架。
		officetool.Module(sharedOfficeKitProvider(home)),
```

import：`officetool "dshgo/modules/tool/office"`（`main.go:42`）。

**为什么是「共享槽」而不是把 provider 作为参数透传**：`builtinModules` 的调用方是
`loadModsIn → loadMods/loadModsWithPluginsIn`，后者又被 `acp.go:1009`、`doctor.go:252`、
`cmdTools`、`cmdExec`、`cmdRun` 调用——其中 `acp.go` / `doctor.go` **不在本任务写作用域**。
共享槽只改 `main.go` 一行就能让所有入口拿到同一份 provider，且 serve 进程里
`cmdServe` 已经登记过，`builtinModules` 若在同一进程被调用也命中的是这一份。

> 关于「同一 provider 实例」的事实边界：默认 serve 路径下，每一轮 turn 是
> `loopRunner` 起的 `dshd run` 子进程跑的，工具在**子进程**里，Transport 面在
> **父进程**里——跨进程不可能共享同一个 Go 对象。本接线保证的是
> **每进程只有一份 provider**（父进程一份、每个 run 子进程各一份），配置同源
> （同样的 `Home`、同样的 `office-kit/outputs`、同样的引擎发现规则）。
> `DSHD_SERVE_MODEL_RUNNER=1` 的进程内 runner 路径下则真的是同一份。

## 2. 先红后绿

红（接线前，`tmp/GO8-RC2/e7-evidence/red-dispatch.log`）：

```
transport: /api/officeKit.generation status=501
--- FAIL: TestOfficeKitReachableThroughHTTP
    officeKit.generation 的 501 体缺 "unavailable"：
    {"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现",...}}
```

即：接线前 `officeKit.*` 落到通用「未实现表」（`not_implemented`、无 `capableWhen`），
**没有进入 officeKit handler**。

绿：加上 `server.go` 那条分派后同一条用例转 200/501 正确形状
（`green-dispatch3.log`，transport 包 `TestOfficeKit*` 9 条全 PASS）。

## 3. e2e 真 PDF 证据（四层链路，全部 21379 字节）

| 层 | 用例/命令 | 产物 |
|---|---|---|
| transport 包（真实 `ServeHTTP` → 适配器 → 引擎） | `TestOfficeKitDispatchEndToEndRealEngine` | 日志 `分派级真实 PDF：21379 字节` |
| cmd/dshd 包（产品适配器 + 会话 cwd 相对路径） | `TestOfficeKitWiringEndToEndRealEngine` | `tmp/GO8-RC2/e7-evidence/e7-officekit-wiring.pdf`（21379 B） |
| **真实 `dshd serve` 进程**（ready → `/connect` → POST） | `tmp/GO8-RC2/e7-evidence/serve-smoke.sh` | `e7-serve-real.pdf`（21379 B，`%PDF-1.7`…`%%EOF`，`bytes=21379`、`eof=true`、`missingFonts=[]`、`version=5d7385e7abc9d6ba-985`、`absolutePath` 正确） |
| 新旧两面共存 + 工具面 | `serve-smoke2.sh`、`dshd exec --tool office_convert` | `serve2-kit-render.pdf` / `serve2-legacy-render.pdf` 各 21379 B；工具输出 `bytes=21379` 真文件 |

`dshd exec --tool office_convert`（`tool-office-convert.log`）：

```
INFO 工具调用 tool=office_convert ms=591 ok=true args_bytes=146
"bytes": 21379   "file": "…/_e7home/office-kit/outputs/session-c3313ef4727d75ced5edfdc4/…/probe.pdf"
```

## 4. 引擎缺失时的能力边界（不许假渲染）

`officekit.Open` 没有引擎也返回可用对象（官方 rc.2「It now registers without Kit」）。此状态下：

- `officeKit.render` → **HTTP 501**，`code=unavailable`、`capableWhen`（说清设
  `DSHD_OFFICE_KIT_ENGINE` 指向引擎包根）、`capability{tried:[…]}`，响应体**没有**
  `%PDF` / base64 PDF；
- `officeKit.generation` → 200 + 真值 `officekit-unavailable`（不编造可用的 generation）；
- 参数错误优先于能力错误：文件不存在 → 400 `invalid-request`（不是 501）；
- 工具面（`office_render/convert/recalculate`）在无引擎时按 `officekit.Error` 返回结构化
  失败与「怎么修」提示，C4 的 `TestProviderConstructsWithoutEngine` /
  `TestOperationsFailStructuredWhenUnavailable` 四个操作面全覆盖；
- 分派层「没挂实现」（transport 未 attach）仍是 501 + `unavailable` + `capableWhen` 字段。

用例：`TestOfficeKitWiringWithoutEngineReports501`（产品适配器 + 真 HTTP）、
`TestOfficeKitReachableThroughHTTP`（dispatch + 无引擎真适配器）、
C4 的 `TestOfficeKitEndToEndNoEngine`。

## 5. serve.go 到底加了哪几行

| 位置 | 行 | 说明 |
|---|---|---|
| import 区 `serve.go:24` | +1 | `"dshgo/internal/officekit"` |
| `cmdServe` 内 `serve.go:865-875` | +12 / −1 | provider 创建 + `defer Close` + 登记共享槽 + `AttachOfficialOfficeKitAPI` + `AttachOfficialOfficePDFAPI(newOfficePDFAPIWith(...))`（替换原来的 `newOfficePDFAPI(*home)`） |

`newOfficePDFAPI(home)` 保留未删（`cmd/dshd/serve_office_pdf.go` 未改，本任务写作用域外）。

## 6. 超出声明写作用域的两处（已在最终答复里点名）

1. **`dsh.toml` +3 行**（`id = "tool-office"`）：manifest 是**模块白名单**，
   不加这一行 `builtinModules` 里的 `tool-office` 会被静默跳过（启动日志
   `WARN manifest 是白名单，这个模块没列出来，已跳过 module=tool-office`），
   模型永远看不到三个 Office 工具——接线的目的就落空了。仓库既有做法一致
   （M306/M312/M318 每个新工具模块都往 `dsh.toml` 加白名单行）。
2. **`cmd/dshd/snapshot_test.go` +6 行**（`TestWriteToolsDeclareCaps` 的 `want` 表）：
   该测试要求「每个进工具表的工具都必须显式认领 caps」，新工具不认领会直接红。按
   `modules/tool/office/office.go:105-112` 的真实 `Caps()` 认领：
   `office_render{Executes}`、`office_convert/office_recalculate{Writes,Executes}`。

工具表快照已按 R4 流程更新并**逐条 diff**：`tools.snapshot.json` 只多三个工具
（110 行新增、**0 行删除**，工具数 61→64），没有其它变化。

## 7. 已知问题 / 未做项（登记，不隐藏）

1. **工作树里另外两条测试是红的，与本任务无关**：`TestSettingsDescribeOfficialWireEnvelope`、
   `TestSettingsDescribeOfficialWireShape`（`cmd/dshd/serve_settings_api_test.go`）。
   原因是另一并行写者正在改 `internal/settingsform`（新增 `provider_schema.go`、
   `llm` 命名空间行，mtime 06:22/06:33），`settings.describe` 多出一行 `ns:"llm"`，
   而这两条用例仍只有 model/permission 两行。**office 面不碰 settings**。
   全量 `go test ./cmd/dshd` = 178.4s，仅上述 2 条 FAIL，其余全绿。
2. **`tool-actioncards` 也不在 `dsh.toml` 白名单**（启动日志同样 WARN 跳过）——本次**没动**，
   属 actioncards 那条线的收口范围。
3. **相对路径解析基准**：`officeKit.render` 的 `path` 是相对时，按「会话 cwd（`src.Cwd`）→
   `<home>/workspaces/<scope>`」解析；跨会话传别的会话 id 时以那条会话的 cwd 为准
   （与官方 workspaceFileScope 语义一致）。`..` 逃逸一律 `invalid-request`。
4. **`officeToPdf.*` 未删除**（官方 rc.2 已改名）：本任务遵循 C4 §8.5 的「保留做兼容」，
   两面共用同一 provider；要删是独立决策（删 `handlers_office_pdf_official.go` 注册 +
   `serve_office_pdf.go` 即可）。
5. **跨进程**：见 §1 步骤 4 末尾的事实边界——父/子进程各一份 provider 是现有
   子进程架构决定的，不是本接线引入的缺陷。

## 8. 改动文件

新增：
- `cmd/dshd/serve_office_kit.go`（产品适配器 + 错误码表 + 共享 provider 槽）
- `cmd/dshd/serve_office_kit_test.go`（5 条：共享单例、路径解析、工具注册、真引擎 e2e、无引擎 501）
- `internal/transport/handlers_office_kit_dispatch_test.go`（2 条：真实 `ServeHTTP` 分派 + 分派级真 PDF）

修改：
- `internal/transport/server.go`（+7：officeKit 分派）
- `cmd/dshd/serve.go`（+12/−1：provider + 两次 attach；+1 import）
- `cmd/dshd/main.go`（+1 行模块 + 1 行 import：`officetool.Module(sharedOfficeKitProvider(home))`）
- `cmd/dshd/snapshot_test.go`（+6：三条 caps 认领）
- `cmd/dshd/testdata/tools.snapshot.json`（R4 快照，+3 个工具，0 删除）
- `dsh.toml`（+3：`tool-office` 白名单）

**没有改**：`internal/officekit/**`（未发现真 bug）、`internal/officepdf/**`、
`modules/tool/office/**`、AGENTS/CHANGELOG/docs。

## 9. 验证命令与结果

```
go build ./cmd/dshd/ ./internal/transport/          → OK
go build ./...                                      → OK
go vet ./cmd/dshd/ ./internal/transport/            → OK
go test ./internal/transport/ -count=1              → ok 6.625s（全量，含 9 条 officeKit 用例）
go test ./cmd/dshd/ -count=1                        → 178.4s，仅 2 条 settings 用例红（他人并行改动，见 §7.1）
go test ./cmd/dshd -run TestToolTableSnapshot -update → 快照 +3 工具、0 删除（逐条 diff 见 §6）
go test ./cmd/dshd -run 'TestTool…|TestWrite…|TestEveryTool…|TestParity…' → 全绿
```

证据目录：`tmp/GO8-RC2/e7-evidence/`（红/绿日志、4 份 21379 B PDF、serve 日志与脚本）。
