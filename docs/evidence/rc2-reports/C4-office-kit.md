# C4 · officeKit 取代 officeToPdf + 三个 Office 工具（真原生引擎）

- 任务板：`task-10`（owner `c4-office`）
- 分支/工作树：`.agents/worktrees/dsh-bdb66fbf-95ba`，版本 `0.2.2-rc.2-gobeta.1`（未改版本号）
- 结论：**真实现**。本机找到官方 rc.2 原生引擎并**真实完成转换/重算/光栅渲染**，不是 501。
- 测试：`internal/officekit`、`internal/officepdf`、`internal/transport`、`modules/tool/office` 四包
  `go test -count=1 -v` = **664 passed / 0 failed / 0 skipped**（含全部真实引擎用例）。

## 0. 一句话结论与硬证据

| 判据 | 结果 |
|---|---|
| 引擎发现 | ✅ 自动发现官方解包引擎：`…\Desktop-0.2.2-rc.2\resources\app.asar.unpacked\dsh\node_modules\@deepseek-ai\libreoffice-kit-win32-x64`，`prebuilds.json` version **0.2.3**、engine.kind `native`、platform `win32-x64` |
| 真转换（docx→PDF） | ✅ `tmp/GO8-RC2/c4probe/evidence/probe-from-kit.pdf`，**21379 字节**，头 `%PDF-1.7`，耗时 **544 ms** |
| 真渲染（docx→PNG） | ✅ `tmp/GO8-RC2/c4probe/evidence/probe-page-1.png`，**18743 字节**，`816×1056`（Letter@96DPI，与 Writer 页面几何一致） |
| 真转换（xlsx→CSV） | ✅ 36 字节，内容 `value,doubled\n2,4\n3,6\n4,8\n5,10`（公式被引擎重算后导出） |
| 真重算（xlsx→xlsx） | ✅ 6636 字节，ZIP 头 `PK\x03\x04` |
| 端到端（HTTP→适配器→引擎） | ✅ `TestOfficeKitEndToEndRealEngine`：`POST /api/officeKit.render` 拿到 **21379 字节真 PDF**（base64 解出 `%PDF-`），同链路无引擎时 501 + capableWhen |
| 无引擎时 | ✅ 结构化 **501 / code=unavailable / capableWhen**，四个操作面全部覆盖（render/convert/recalculate/images），绝不返回空 PDF 或空 data |

引擎探针原始输出：`tmp/GO8-RC2/c4probe/evidence/engine-probe.log`
（`capability: available=true generation=officekit-… engineVersion=0.2.3 source=auto:D:\DeepSeek Harness\Desktop-0.2.2-rc.2\…`）。

## 1. 官方依据（file:line）

契约与文档：
- `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2127-2166` —— `officeKit/generation`（参数：无，结果 `{"type":"string"}`）、`officeKit/render`（`workspaceFileScope`,`path`,`priority:foreground|background` → `RenderedDocumentBytes`）。
- `tmp/GO8-RC2/official-rc2-docs/.../docs/tool-catalog.md:743-846` —— `office_convert` / `office_recalculate` / `office_render` 的模型面 schema（本实现逐字对齐）。
- `.../docs/config-catalog.md:2614-2642` —— `office-kit` 行配置：`maxConcurrentConversions` / `maxBackgroundConversions` / `outputDirectory` / `timeoutMs` / `maxOutputBytes` / `maxImageResolution`。
- `.../docs/upgrade-guide/v0.2.2-alpha.3/office-kit-package/guide.md` —— `officeToPdf*` → `officeKit*` 改名表；**「It now registers without Kit」**（provider 照常注册，用到才报 `unavailable`）。
- `.../docs/upgrade-guide/v0.2.2-alpha.3/office-operation-tools/guide.md` —— 三个 skill 换成三个工具；`outputDirectory`/`outputRetentionMs`/`maxOutputStoreBytes` 三个新配置；工具在宿主进程内用原生引擎解析不可信文档（不在 shell 沙盒里）。

官方实现（读它决定参数、环境、校验、并发）：
- `dsh-office-kit/src/index.ts:127-176` OfficeKit 生命周期（generation/slots/queue/outputs）；`:185-250` convert/renderImages/convertTo/recalculate；`:278-332` `render()`（read → stat → convert → `RenderedDocumentBytes`）；`:334-344` 「Kit 缺失时每次重新定位，装好即生效」；`:369-409` `runOperation`（私有临时目录 + 错误分类 + finally 清理）；`:412-433` `runEngine`；`:444-481` `renderPng`（越界路径拒绝 + 总字节上限）。
- `dsh-office-kit/src/types.ts:1-175` 全部类型；`:157-165` **OfficeKitErrorCode** 十一个码（本仓 `Code*` 逐字移植）。
- `dsh-office-kit/src/output.ts:16-69` 产物读取：普通文件、字节上限、`%PDF-x.y`+`%%EOF`、PNG 签名。
- `dsh-office-kit/src/outputs.ts:34-60` 产物仓目录命名（owner 哈希）与保留/淘汰。
- `dsh-office-kit/src/queue.ts:105-…` 准入队列（并发/排队/后台配额，排队可取消）。
- `dsh-office-kit/src/locate.ts:86-93` `.asar → .asar.unpacked` 映射（我们的自动发现同一条规则）。
- `libreoffice-kit/lib/cli.js:714-726` `NATIVE_ENVIRONMENT_KEYS`；`:754-775` `nativeEnvironment`；`:776-806` `prepareNativeEngine`（Windows ≥260 字符走 junction+复制）；`:845-862` `prepareNativeInput`（0.2.x stdin 字节 / 0.1.x `--input-path`）；`:866-905` 工作进程参数；`:929-950` stdout 64 KiB 上限与 `ok!==true` 判失败；`:1086-1100` `--operation render-images` 参数；`:1148-1265` 光栅协议（`P …` / `DONE` / `tile-N.rgba`）；`:1561-1700` 引擎版本与 `readEngine`（prebuilds.json 校验）。
- `libreoffice-kit/lib/cli.js:74-345` 光栅几何全套（parseCellRange/spans/dimension/sheetRangeRectangle/unmirrorRtlTile/splitRasterRectangle/tileDimensions/rgbaPixels/writerPages/selectedPages）——本仓 `internal/officekit/geometry.go` 是逐函数移植。
- `dsh-tool-office/src/index.ts:84-118` 参数对象；`:142-219` 三个工具的注册与执行；`:290-297` `officeFailure`（错误 → 模型可执行提示）；`:315-371` render；`:390-427` convertFile。
- `dsh-tool-office/src/formats.ts:22-144` 族/格式矩阵与四类参数校验；`src/content.ts:74-101` 模型可见措辞。
- 引擎包自身：`libreoffice-kit-win32-x64/prebuilds.json`（`schemaVersion:1`,`version:0.2.3`,`platform:win32-x64`,`status:built`,`engine:{kind:native,executable:bin/dsoffice.exe,programDirectory:program/program}`）、`bin/dsoffice.exe` 185,909,744 字节。

## 2. 引擎发现策略（三级，绝不假渲染）

代码：`internal/officekit/engine.go`。

1. **显式配置（最高优先）**
   - 设置项 `officekit.Options.EnginePath`（宿主侧接线给值）；
   - 环境变量 `DSHD_OFFICE_KIT_ENGINE`；
   - 兼容旧名字：`internal/officepdf` 还认 `DSHD_OFFICE_RENDERER`，但**仅当它指向引擎包根或 `dsoffice.exe`**（明确不再走 soffice/Word COM）。
   - 四种写法都认：包根、`node_modules`、`@deepseek-ai` 目录、`bin/dsoffice.exe` 本身。
2. **自动发现**（`defaultSearchRoots`）：环境变量 `DSHD_OFFICE_KIT_ROOTS` → 平台已知安装位置（`%LOCALAPPDATA%\Programs\*\resources\app.asar.unpacked\dsh\node_modules`、`%PROGRAMFILES%`、`%LOCALAPPDATA%\DeepSeek Harness\…`）→ 工作目录祖先（≤10 层）及其同级应用目录 `*/resources/app.asar.unpacked/dsh/node_modules`。
   - **带 3 秒总预算**并按候选逐个计时；**不进系统临时目录 / go-build 目录**。
   - 为什么：首版不带预算时，从 `go test` 临时可执行目录往上扫会把 Windows `%TEMP%`（成千上万 go-build 目录）拖进 Glob，**实测卡死 >190 s**；收窄后全量发现 22–35 ms。
   - 多引擎并存时的选择规则：①与**本进程同属一个应用目录**的引擎优先（官方语义：用宿主自带那份）；②否则**版本最高**。
     - 真机踩点：本机同时存在 `Desktop`（引擎 `0.1.5`，`bin/libreoffice-kit.exe`，旧 kit 布局）与 `Desktop-0.2.2-rc.2`（`0.2.3`，`bin/dsoffice.exe`）。按字典序或"先到先用"会挑到 **0.1.5**（它只支持 `--input-path` 旧传输），所以加了版本比较。
3. **都没有 → 结构化 501**
   - `officekit.Error{Code:"unavailable", Message, CapableWhen}`；
   - `Capability()` 返回 `{available:false, generation:"officekit-unavailable", tried:[…], capableWhen:"设置 DSHD_OFFICE_KIT_ENGINE 指向 LibreOfficeKit 包根（含 prebuilds.json，内有 bin/dsoffice.exe 与 program/program）…"}`；
   - Transport 面 `officeKit.*` 出 **HTTP 501 + code=unavailable + capableWhen + capability**（`capability` 含 tried/engineRoot/executable/programDirectory，UI 能直接给出动作）。

`prebuilds.json` 校验（`engine.go:loadEngine`）五条，任一不过即拒：`schemaVersion==1`、`status=="built"`、`platform==TargetPlatform()`、`engine.kind=="native"`、`executable` 是文件且 `programDirectory` 是目录、两者都在包根内（拒绝 `..` 越界）。与官方的唯一差异：**不钉死包版本**（官方 `ENGINE_VERSIONS[target]` 钉 0.2.3），改为记录版本并按"版本最高"选，便于升级引擎包。

## 3. 引擎调用（参数/环境/超时/取消/清理）

代码：`internal/officekit/convert.go`（转换）、`images.go`（光栅）、`proc_windows.go` / `proc_other.go`（进程树）。

- **转换/重算**：`dsoffice.exe --program-directory <LO program> [--input-bytes N --input-format ext | --input-path file] --output-path <out> --profile-directory <私有 profile> --max-output-bytes N --max-image-resolution N --format <pdf|docx|odt|txt|xlsx|ods|csv|pptx|odp> --recalculate <true|false> [--sheet <名>]`，stdout 一行 JSON（`{"ok":true,"missingFonts":[…],"pageCount"?:N}`）。
- **传输顺序**：先官方 0.2.x 的 stdin 字节（`--input-bytes/--input-format`）；引擎回报「Unknown worker argument」时退回 0.1.x 的 `--input-path`（两代引擎都能用；本机 0.2.3 走 stdin）。实测：0.2.3 也接受 `--input-path`，但我们以官方现行传输为主。
- **光栅**：`--operation render-images …` + 流式行协议：`ready` → 每条 `P <序号> <part> <宽> <高> <x> <y> <twips宽> <twips高>` → `paint{path:tile-N.rgba}` → `DONE` → `{"kind":"done"}`。像素是预乘 alpha 裸 RGBA（`tileMode 1` 为 BGRA），PNG 编码用 `image/png`（官方是手写编码器，PNG 无损故等价）。
- **环境**：逐字复刻 `nativeEnvironment` —— 只透传 `PATH/SystemRoot/SYSTEMROOT/WINDIR/COMSPEC/PATHEXT/LANG/LC_ALL/TZ/OFFICE_*DIAGNOSTICS`，其余全部指向私有 profile（`HOME/USERPROFILE/TMPDIR/TMP/TEMP`），并设 `SAL_DISABLE_PRINTERLIST=1`、`SAL_DISABLE_DEFAULTPRINTER=1`（避免打印机发现卡住）。非 Windows 追加 `LD_LIBRARY_PATH=<program>`。
- **可杀 / 超时 / 取消 / 清理**：
  - 每次操作 `os.MkdirTemp("", "dsh-office-kit-*")`，`defer os.RemoveAll`；profile 在 scratch 内；
  - `context` 截止（默认 60 s，官方 `timeoutMs`）+ 监听 ctx → 杀**整棵进程树**（Windows Job Object `KILL_ON_JOB_CLOSE`，与 `internal/worktree/proc_windows.go` 同机制；非 Windows 进程组 SIGKILL）；
  - stdout/stderr 各 64 KiB 上限（官方同数），产物读取再核 `maxOutputBytes`；
  - PDF 校验头 `%PDF-<d>.<d>` + 尾 `%%EOF`，否则 `invalid-output`；
  - Windows 长路径（≥260）时把包根 junction 到短临时目录并复制可执行文件（官方 `prepareNativeEngine`），建不出来就**明确失败**并给 `capableWhen`，不带病跑。
- **并发/排队**：`maxConcurrentConversions=2`、`maxQueuedJobs=8`、`maxBackgroundConversions=1`（官方默认值），排队中的调用可被 ctx 取消，队列满给 `busy`。`generation` = 引擎内容哈希（换引擎即变）。
- **产物仓**：`<home>/office-kit/outputs/session-<sha256(owner)[:24]>/<uuid>/<name>`，24 h 保留 + 512 MiB 上限（官方 `outputDirectory`/`outputRetentionMs`/`maxOutputStoreBytes` 默认）。

## 4. `officeKit.generation` / `officeKit.render`（Transport 面）

代码：`internal/transport/handlers_office_kit_rc2.go`（新）。

- `officeKit.generation`：不接受参数（带参数 = 400），返回 `{"result": "<generation>"}`；无引擎时返回真值 `"officekit-unavailable"`（不编造）。
- `officeKit.render`：参数严格为 `workspaceFileScopeId`（兼容 `agentId`/`sessionId` 两种旧写法）、`path`、`priority∈{foreground,background}`；未知字段 400；结果字段与官方 `RenderedDocumentBytes` 一致：`missingFonts,generation,offset,data(base64),eof,absolutePath,version,bytes?`。
- 错误映射：`unavailable→501`（+`capableWhen`+`capability`）、`invalid-request|invalid-document|unsupported-format|input-too-large|output-too-large|invalid-output|source-changed→400`、`timeout→504`、`busy→429`、`failed→502`。
- 旧面 `officeToPdf.*` 保留（`handlers_office_pdf_official.go`）：错误体新增 `capableWhen` 兄弟字段；`renderer_unavailable` 仍映射 503，**不改变** `cmd/dshd/serve_office_pdf.go` 的现有映射。

## 5. 三个工具（`modules/tool/office/`）

模型面 schema **逐字**对齐 `tool-catalog.md:743-846`（`TestSchemasMatchOfficialCatalog` 固化 required/enum/description）：

```json
office_convert     {"input","format"(enum 9: pdf,docx,odt,txt,xlsx,ods,csv,pptx,odp),"sheet"}  required: input,format
office_recalculate {"input","format"(enum 2: xlsx,ods)}                                        required: input,format
office_render      {"input","pages"[int],"sheet","range","dpi"[int]}                            required: input
```

schema 表达不了的规则按官方 `formats.ts` 全部前置校验（都先于引擎）：
- 输入族 ↔ 可转格式矩阵（text→pdf/docx/odt/txt、spreadsheet→pdf/xlsx/ods/csv、presentation→pdf/pptx/odp）；
- `office_convert` 不接受 pdf 输入；`office_recalculate` 只接受 xls/xlsx/ods；
- 工作表不接受 `pages`；非工作表不接受 `sheet`/`range`；`range` 必须带 `sheet`；`dpi∈[24,600]`；
- 未知参数键明确报错并指出属于哪个工具（`pages applies only to office_render`）；
- 文件必须经 `spi.FS.Resolve` 授权读取（工作区约束不变）。

`Caps()` 显式声明：`office_render{Executes}`；`office_convert`/`office_recalculate` `{Writes,Executes}`（产物写宿主私有产物仓，不写工作区）。
结果形状：`{operation,path,file,format,bytes,pageCount?,missingFonts}`（convert/recalculate）；`{operation:"render",path,pageCount,dpi,images[{page?,sheet?,range?,attachmentId,mediaType,bytes,width,height}],missingFonts}` + `spi.Result.Images`（PNG base64，宿主会把它挪进用户消息，否则模型看不到）；文本措辞与官方 `content.ts` 一致。

## 6. 先红后绿（测试点）

红（实现前，已留证）：
1. `internal/officepdf` 新测试对旧实现**编译红**（旧 Provider 没有 `Engine()/Tried()/CapableWhen()/CodeOf()`）——`go test ./internal/officepdf` 直接 build failed。
2. 旧实现行为实测（`red_test.go`，跑完删除）：`Renderer="word-com"`，Word COM 转同一个 docx 要 **16.76 s**、产物 23669 字节，且 generation 是 `officepdf-…`——即"能转但走的不是官方引擎，也没有 engine/convert/recalculate/images 任何一项"。
3. 发现器首版**性能红**：从临时目录往上扫 `%TEMP%`，`go test` 卡 >190 s（超时被杀）→ 收窄候选根 + 3 s 预算后 22–35 ms。
4. `spans`/`sheetRangeRectangle` 首个测试用例写错（把 flags 行程写成 `0:5 1:10`、把 144 DPI 期望成等比放大）——测试先红，按官方 JS 语义校正后才绿（取整是 `floor(size*scale/15)`，不是等比）。

绿（当前）：四包 **664 passed / 0 failed / 0 skipped**，其中真引擎用例：
`TestRealRenderDocxToPDF`、`TestRealConvertXlsxToCSV`、`TestRealRecalculateWorkbook`、`TestRealRenderImagesDocx`、`TestRealRenderImagesXlsxSheet`、`TestRealRenderCancelKillsEngine`（5 ms 取消 → `timeout` 码，进程树被杀）、`TestConvertRealDocxToPDF`、`TestConvertRealXlsxToCSV`、`TestRecalculateRealWorkbook`、`TestRenderRealDocxProducesImages`；
端到端用例：`TestOfficeKitEndToEndRealEngine`（HTTP 入口 → 适配器 → 真引擎 → 21379 字节 PDF）、`TestOfficeKitEndToEndNoEngine`（同链路 501 + capableWhen + capability + 无 PDF 数据）；
无引擎用例：`TestProviderConstructsWithoutEngine`、`TestOperationsFailStructuredWhenUnavailable`（render/convert/recalculate/images 四个面）、`TestUnavailableEngineIsActionable`、`TestOfficeKitWithoutAPIReports501`、`TestOfficeKitUnavailableCarriesCapability`；
发现器用例：`TestDiscoverValidEngine`、`TestDiscoverExplicitForms`（四种写法）、`TestDiscoverRejectsBrokenManifests`（6 种坏清单）、`TestDiscoverPrefersNewestEngine`、`TestCompareVersions`、`TestDiscoverReportsTriedPaths`。

## 7. 接线行（Lead 集成；我未改这些文件）

### 7.1 `internal/transport/server.go`（在 officeToPdf 早分支旁，约 568-572 行之后）

```go
	// officeKit 两方法（rc.2 取代 officeToPdf）：真实引擎在 internal/officekit。
	if IsOfficialOfficeKitMethod(name) {
		s.ServeOfficialOfficeKitAPI(w, r, name)
		return
	}
```

### 7.2 `cmd/dshd/serve.go`（约 842 行 `AttachOfficialOfficePDFAPI` 处）

```go
	officeKitProvider := officekit.Open(officekit.Options{
		Home:            *home,
		OutputDirectory: filepath.Join(*home, "office-kit", "outputs"),
	})
	defer officeKitProvider.Close()
	srv.AttachOfficialOfficeKitAPI(newOfficeKitAPI(officeKitProvider))
	// 旧的 officeToPdf 面复用同一 provider（同一引擎/同一队列/同一 generation）
	srv.AttachOfficialOfficePDFAPI(newOfficePDFAPIWith(*home, officeKitProvider))
```
新增 import：`"dshgo/internal/officekit"`。

### 7.3 `cmd/dshd/serve_office_pdf.go`（新增一个构造函数，保留旧 `newOfficePDFAPI` 不动）

```go
func newOfficePDFAPIWith(home string, kit *officekit.Provider) *officePDFAPI {
	return &officePDFAPI{provider: officepdf.Wrap(home, kit)}
}
```
（`officepdf.Wrap` 已实现；`officepdf.Open(home)` 仍单独可用。）

### 7.4 新增 `cmd/dshd/serve_office_kit.go`（约 60 行；这里是唯一需要写映射的地方）

> **这段适配器已经按同形写法在 `internal/transport/handlers_office_kit_rc2_test.go` 里跑通**：
> `realOfficeKitAdapter` + `toOfficeKitAPIError` 就是建议的文件内容，两个端到端测试
> `TestOfficeKitEndToEndRealEngine` / `TestOfficeKitEndToEndNoEngine` 证明了「有引擎→真 PDF、没引擎→501」。
> Lead 可以直接把这两个类型搬到 `cmd/dshd/serve_office_kit.go`。

```go
type officeKitAPI struct{ provider *officekit.Provider }

func newOfficeKitAPI(provider *officekit.Provider) *officeKitAPI { return &officeKitAPI{provider: provider} }

func (a *officeKitAPI) Generation(context.Context) (string, transport.OfficialOfficeKitCapability, error) {
	capability := a.provider.Capability()
	return capability.Generation, transport.OfficialOfficeKitCapability{
		Available: capability.Available, Generation: capability.Generation,
		EngineVersion: capability.EngineVersion, EngineRoot: capability.EngineRoot,
		Executable: capability.Executable, ProgramDirectory: capability.ProgramDirectory,
		Source: capability.Source, Tried: capability.Tried, CapableWhen: capability.CapableWhen,
	}, nil
}

func (a *officeKitAPI) Render(ctx context.Context, scope, path, priority string) (transport.OfficialOfficePDFResult, error) {
	doc, err := a.provider.Render(ctx, path, priority) // 路径解析/授权在调用方或 provider
	if err != nil {
		return transport.OfficialOfficePDFResult{}, toOfficeKitAPIError(err)
	}
	return transport.OfficialOfficePDFResult{MissingFonts: nonNil(doc.MissingFonts), Generation: doc.Generation,
		Offset: doc.Offset, Data: doc.Data, Eof: doc.Eof, AbsolutePath: doc.AbsolutePath,
		Version: doc.Version, Bytes: doc.Bytes}, nil
}

// toOfficeKitAPIError 按 §4 的表把 officekit.Code 映射成 transport.OfficeKitAPIError（501/400/504/429/502）。
```
（`scope` 用于把相对路径解析成工作区绝对路径：直接复用 `internal/officepdf` 的 `resolve` 语义，或先 `filepath.Join(home,"workspaces",scope,path)`。）

### 7.5 `cmd/dshd/main.go`（`builtinModules`，约 135 行 `read.Module()` 附近）

```go
	officetool.Module(officeKitProvider), // 需要把 7.2 的 provider 传进 builtinModules
```
import：`officetool "dshgo/modules/tool/office"`。
若不想改 `builtinModules` 签名，可在 `serve.go` 里单独装配：`kernel.Module{…Register: func(s *kernel.Scope) error { … registry.Register(&office.Tool{…}) }}`——
但**推荐**把 provider 作为参数传进 `builtinModules(wd, home, maxChars, sub, officeKitProvider)`，因为工具与 Transport 必须共用同一份引擎/队列。

## 8. 未实现 / 偏差（登记，不隐藏）

1. **`office_render` 的 `attachmentId` 是本仓生成的稳定句柄**（`office-image-<序号>-<宽>x<高>`）：本仓当前没有官方那套 attachment 服务，图片经 `spi.Result.Images` 作为图片块送给模型。官方是 `attachments.saveImages` 返回的引用。行为等价（模型能看到图），引用形态不同。
2. **字体替换表未实现**：官方会写 `registrymodifications.xcu` 做缺失字体替换并回报 `missingFonts`；本仓直接用引擎默认字体回退，`missingFonts` 取引擎回报（当前引擎对 OOXML 返回空数组）。不假报"没有缺失字体"之外的东西。
3. **`maxArchiveEntries` / `maxUncompressedBytes` / font 相关上限（`maxFontFiles` 等）未实现**：官方在建 converter 时限制 OOXML 解压条目与字体索引；本仓依赖引擎自身限制 + 输入字节上限（50 MiB）+ 输出上限。属"少一层防御"，不是假实现。
4. **PNG 编码器实现不同**：官方手写 PNG 编码器，本仓用 `image/png`（无损，像素内容等价）。
5. **`officeToPdf.*` 仍在**（未删旧方法）：官方 rc.2 已改名，本仓保留旧面做兼容（旧客户端不断线），新面 `officeKit.*` 是权威；两者共用同一 provider。若 Lead 决定彻底删除旧面，删除 `internal/transport/handlers_office_pdf_official.go(+_test)` 的注册与 `cmd/dshd/serve_office_pdf.go` 即可。
6. **`DSHD_OFFICE_RENDERER` 的 soffice/Word COM 分支已移除**：能力不降（官方引擎覆盖 doc/docx/xls/xlsx/ppt/pptx + odt/ods/odp 输入，Word COM 只会出 PDF），但 `.rtf`/`.odt` 里 **`.rtf` 不再接受**——官方 rc.2 的 officeKit 也只认 OOXML，这是对齐而非缩水。
7. **`modules/tools/mcp.go` 在本工作树是红的**（另一并行写者半写：`cannot use "error" ... as mcp.Status`），因此 `go build ./...` 整体不绿；**与本任务无关**，我只验自己四包（build/vet/test 全绿）。`go list -deps ./internal/kernel` 不含 `internal/officekit` / `modules/tool/office`（R1 未被破坏）。
8. **真实渲染只在 Windows 验证**（本机）。非 Windows 走进程组兜底，明确 OUT_OF_SUPPORT，不作为发布判据。

## 9. 改动文件

新增：
- `internal/officekit/errors.go`（官方错误码移植 + capableWhen）
- `internal/officekit/engine.go`（引擎发现/清单校验/多引擎选择）
- `internal/officekit/provider.go`（provider、准入队列、generation、产物仓）
- `internal/officekit/convert.go`（工作进程调用、环境白名单、超时/取消、输出校验、族/格式矩阵）
- `internal/officekit/geometry.go`（官方光栅几何逐函数移植）
- `internal/officekit/images.go`（render-images 流式协议 + PNG 编码）
- `internal/officekit/proc_windows.go`、`proc_other.go`（进程树）
- `internal/officekit/{engine,provider,geometry,real_engine}_test.go`、`internal/officekit/testdata/{probe.docx,book.xlsx}`
- `internal/transport/handlers_office_kit_rc2.go(+_test.go)`
- `modules/tool/office/{formats.go,office.go,office_test.go}`

修改：
- `internal/officepdf/provider.go`（旧渲染器 → officekit 适配层，保留对外形状与错误码）
- `internal/officepdf/provider_test.go`
- `internal/transport/handlers_office_pdf_official.go(+_test.go)`（错误体加 capableWhen）

证据：
- `tmp/GO8-RC2/c4probe/evidence/`（`engine-probe.log`、`probe-from-kit.pdf` 21379 B、`probe-page-1.png` 18743 B、`home/office-kit/outputs/**` 下的 book.csv / book.xlsx）
