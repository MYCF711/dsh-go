# AGENTS.md · DSH-GO

> **本仓**：用 Go 重写 DeepSeek Harness（DSH）。上游基线 `0.2.1-rc.5`；当前版本以源码中的版本声明与 `CHANGELOG.md` 为准。
> 本文件只规定执行方式，不改写用户目标。方向以 `PLAN.md` §0/§1、`UIPLAN.md` 顶部用户原话与 §2.10、`../dsh-ui/PLAN.md` §8 为据；后续明确拍板优先于旧方案。

## 0. 当前执行入口（2026-11-03 校正）

**最终目标不是工具数量、门禁全绿或截图相似，而是低占用、高性能、还原 DSH 使用体验、能力可替换的原生产品，能接管真实工作。** 宿主用 Go；界面在独立 `dsh-ui` 仓用 Viva 原生；先根本依赖，再逐部件插件化。Web 仅是可以关闭的兼容过渡件，不得反向绑住原生主干。

1. 开工先读本节、`MAINTENANCE.md`、`docs/execution-review.md`，再读任务对应的 PLAN 与源码。历史报告、✅、注释都不是当前通过证据。
2. 宿主按 §12 最新证据继续：H1/H9/H10/H11 已有同版本测试或链式证据，H8 主路径已修但编码失败出口仍需闭环；下一步优先 **H6、H13 与跨仓消费**。界面不再等 H1–H7 全齐，按已存在契约接真宿主，未知值不显示、不编数据。
3. 修缺陷先写能在旧实现上失败的测试，再实现并复验。状态回填必须附对应版本、命令、退出码、真实断言和未验证项；不允许把「代码存在」写成「端到端完成」。
4. 产品级验收必须是**同一真宿主 + 原生界面**链路：认证、list/load、发送、流式 text/reasoning/tool、真实审批、取消、续历史、断线恢复；之后才是完整工作日、同任务 A/B、整进程树性能。mockd、ACP、静态插件样例各自只证明自身一层。
5. **共享仓安全**：改前重读，只改认领文件；不得用 `git checkout --`/reset 覆盖他人改动，不原地移动 `modules/` 或兄弟仓，不按镜像名杀 `dshd.exe`/`app.exe`。当前 `scripts/regress.sh` 有全局 taskkill，`scripts/check.sh` 有原地移动，隔离修复前不得在并行工作树运行。移除模块/变异只在自己拥有的隔离副本做，只回收自己启动且核对归属的 PID。
6. **指引送达不等于执行完成**：官方 DSH 的下一次请求可加载更新后的 AGENTS，不承诺正在执行的请求被中途改写。Go 版 `internal/prompt` 默认只取前 8000 字节，因此本入口必须短而准；委派按 `docs/WORKER.md` 给明确任务书，不假设对方已读全文。

## 两条线（先认清改哪条）

| | **宿主线（本仓）** | **界面线（独立仓）** |
|---|---|---|
| 计划 | `PLAN.md` | `UIPLAN.md` + `../dsh-ui/PLAN.md` |
| 代码 | `internal/` `modules/` `codecs/` `cmd/dshd/` | `../dsh-ui/`；本仓仅 `internal/transport/` 是宿主侧接口 |
| 技术 | Go | Go + Viva；`CGO_ENABLED=0` 必须可构建 |
| 当前 | 13 个 Transport method 已存在；H1/H9/H10/H11 有宿主侧新证据，H8 边角、H6/H13 与跨仓联调未闭环 | 原生插件与真宿主对接已存在，不是“尚未开工”；完整产品链路未验收 |
| 验收 | PLAN §0/§14；当前版本 build/vet/test、真实分词、资源与真实任务 | `../dsh-ui/AGENTS.md` 的真链路、插件替换和完整工作日验收 |

两线只经 **Transport** 握手：HTTP `/api`、WS 流和 `ViewSpec`。宿主不 import 界面实现，界面插件不直读会话日志。跨线字段/形状变更先更新两边契约与快照，再各改各的实现。

> 官方壳与协议调研见 `research/40-official-shell.md`、`research/42-frontend-inventory.md`。官方主界面是纯 Web，Electron 是宿主；官方 GUI 不使用 ACP 作为总线。它们是调研事实，不再意味着我们的主界面退回 WebView。

---

## 1. 七条红线

| # | 红线 | 验证 |
|---|---|---|
| **R1** | 内核零业务：`internal/kernel` 不认识 agent/tool/session/llm | `go list -deps ./internal/kernel` 不得含 `modules/` 或这些业务包 |
| **R2** | SPI 之外无通路：模块只拿 `*Scope`，不拿 `*App`/全局单例 | `modules/` 无 `kernel.App`、包级可变状态、`init()` 注册 |
| **R3** | 工具结果结构化、有界，错误带可执行 Hint | 单测 + AST 扫描 |
| **R4** | schema/契约稳定；变更必须显式改快照 | 工具：`cmd/dshd/testdata/tools.snapshot.json`；Transport：`internal/transport/testdata/api.snapshot.json` |
| **R5** | 会话日志 append-only；滑窗/压缩/截断都是派生投影 | 读路径不写盘；只有追加写可发布新帧 |
| **R6** | 宿主不引 JS 运行时，不 fork/vendor dsc，不用 mvdan 执行命令 | `go.mod` 无 JS 引擎；exec 走真实 shell；mvdan 仅静态分析 |
| **R7** | 界面不越界读日志，宿主不 import 界面实现 | 在各自仓检查依赖方向；跨线只走 Transport + ViewSpec |

R6 禁止的是把 V8/Node 塞进 `dshd`，**不禁止独立原生 Viva 界面**。可选 Web 兼容件若将来启用，也不得进入宿主进程或成为主干的硬依赖。dsc 只读参考，引用注明 `refs/dsc/<file>:<line>`。

---

## 2. 代码地图与写作用域

| 路径 | 职责 / 限制 |
|---|---|
| `cmd/dshd/` | 只做 manifest → kernel → 默认模块装配；禁止业务分支 |
| `internal/kernel/` | scope / registry / eventbus / assembler；改动须有 PLAN 理由 |
| `internal/spi/` | 接口、数据类型、服务键，零实现；新接口须同时有默认实现、替换样本和禁用 fail-loud 测试 |
| `internal/fsutil/` | 文件遍历、二进制判定；零业务 |
| `internal/session/` | 事件溯源、canonical 模型、会话列举；唯一允许直接读日志的包 |
| `internal/agent/` | turn/step 双循环；不内联压缩策略 |
| `internal/acp/` | ACP stdio JSON-RPC；不是 GUI 总线 |
| `internal/llm/` | OpenAI 兼容 SSE 客户端；当前只接 magpie |
| `internal/prompt/` | 确定性、可组合、有界的提示装配；新段须测空段消失与确定性 |
| `internal/compact/` | 压缩策略；写事件只走 `session.Compact` |
| `internal/approval/` | 会话×工具审批记忆；默认内存，落盘须显式开启且不写 v4 日志 |
| `modules/*` | 静态默认实现，一模块一目录一个 `Register(scope)` |
| `codecs/v4/` | v4 编解码，字节级对齐 §4 |
| `internal/transport/` | 宿主侧界面契约入口；只认 SPI；改字段/形状同步快照和界面消费端 |
| `internal/modes/` | 模式与工具可见性；内置只读，未知模式报错 |
| `docs/` | 设计与当前证据；决策变化同时改对应 PLAN |
| `research/` `evidence/` `refs/` | 调研与参考；不拿旧结论覆盖新拍板 |
| `../dsh-ui/` | 独立原生界面仓；遵守其 AGENTS，不在本仓新建 TypeScript `ui/` 或 `cmd/dshui/` |

同一文件只给一个写入者。公共 SPI/Transport/快照的改动串行合入；独立模块和独立插件目录才可并行。

---

## 3. 写工具的规矩

```go
type Tool interface {
    Name() string
    Description() string // 什么时候用，≤120 字（含中文标点）
    Schema() json.RawMessage
    Exec(ctx context.Context, args json.RawMessage) (Result, error)
}
```

1. `Data` 是结构化 JSON，键名 snake_case；`Summary` 只给人看。
2. 输出有界；超阈值由中间件外置 artifact + 指针，替换体本身也不超阈值。
3. 每个 error 带 Hint，明确下一步怎么改。
4. 失败返回 `(Result{}, err)`，不返回半截结果 + nil error。
5. 路径用正斜杠、相对 workspace 根；不把原生反斜杠喂给模型。
6. 写类工具必须如实声明 `Caps()`：writes/executes/network/destructive；审批与沙箱不按工具名猜。
7. 改前必须读，由 FS 中间件统一实现观测策略。
8. 中间件顺序固定：pre-execute → execute → post-execute → result。

---

## 4. 会话 v4：字节级清单

| # | 规则 |
|---|---|
| 1 | `projectKey`：`/ \ :` 各变一个 `-` 并折叠；安全字符原样；其余每个 UTF-16 码元 → 大写 `~XXXX`；去前导 `-`；空为 `root`；截 251；包成 `--<key>--` |
| 2 | `encodeSegment(id)`：`.`→`~002E`、`..`→`~002E~002E`，其余不安全码元（含 `~`）→`~XXXX`；空串报错 |
| 3 | `cwd` 缺失 → `<root>/_no-cwd`；文件 `session.v4.jsonl.zstd` |
| 4 | 第 1 帧只装 header 一行，恰好一行且以 `\n` 结尾；list/stat 只解首帧 |
| 5 | 每个 durable 批次一帧；批窗口 200 ms；fsync 后 resolve；失败回滚到原长度 |
| 6 | zstd 只开 checksum，不设 level；追加不重写已提交帧 |
| 7 | header 键白名单：`type,version,id,createdAt,isSeeded,delegationDepth` + 可选 `cwd,parentSession,origin,agentPreset`；多一个即非法 |
| 8 | 事件不在 62 个 type 词汇表 → 拒绝，除非信封 `ignorable:true` |
| 9 | 只有 5 类参与派生：system/developer/user/assistant message 与 tool/result，必须带 `surfaceOp` |
| 10 | `tool/call.arguments` 是 JSON 字符串；`tool/result` 是 `role:'tool'` + 顶层 `toolCallId/content/isError` |
| 11 | `request/header.header.system` 已退休；多出 `system` 键即拒绝 |
| 12 | 压缩只追加 `compaction/summary`（shadowedRange/shadowedSeqs），日志永不删；遮蔽仅发生在 surface 折叠 |

先写真实语料 golden test，再改编码器。格式改动后用 `scripts/verify-node-reads.mjs` 做 Node 侧复验；本机 Node 运行方式见 §8。状态断言优先写 Go 测试，不用进程脚本替代格式断言。

---

## 5. 工作流与交付门

**开工前**
1. 读 `PLAN.md` §0/§1/§14；界面任务读 `UIPLAN.md` 与 `../dsh-ui/AGENTS.md`。
2. 读对应 research 和当前源码，不凭记忆写协议。
3. 确认文件写作用域；重读最新文件再改，发现他人改动先合并。

**实现时**
- 会话、工具投影、提示装配必须 TDD：先 golden/一致性测试，再实现。
- bug 修复必须有旧代码上会红的测试；同时记录一个真实失败分支，但不得让执行者临时破坏共享源码来“造红”。
- 新 SPI 同时交默认实现、替换样本、禁用 fail-loud 测试。
- 新依赖在 `go.mod` 注释说明为什么标准库不够。

**交付前**
```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./...
# 有可用 C 工具链时另跑：go test ./... -race
go list -deps ./internal/kernel   # 不含 modules/ 与业务包
```
- 抽掉模块的可替换性判据不变，但只在隔离副本验证；不得在共享树执行 `mv modules …`。
- 工具表须用当前版本、同模型真实分词并记录工具数/配置/字节数/token；估算器只是日常代理。
- 二进制用 `-ldflags "-s -w"` 实测，目标 ≤25 MB。
- `scripts/check.sh` 的 `ALL GREEN` 不排除 SKIP/WARN，逐项记录；race 未跑成就写未验证。
- 当前 `check.sh`/`regress.sh` 有 §0 第 5 条所述共享环境副作用，隔离修复前只运行审查过的定向测试、build/vet 和无副作用文档检查。

压缩改动另跑 `scripts/acp-compact-semantics.mjs`；ACP 回归参考 `scripts/e2e-regression.mjs`，启动/清理也必须只碰测试自有进程。

---

## 6. 版本、提交、文档

- 版本：`<上游版本>-go.<序号>`；当前值不在手册重复硬编码。发布前更新 `CHANGELOG.md` 映射与移植/跳过理由。
- 一个提交只做一件事；提交前确认不包含其他执行者的文件。
- 改决策先改对应 PLAN 并写理由，禁止代码先行、文档追认。
- 文档引用必须真实存在；`scripts/doc-links.py` 只检查部分路径与 Python 语法，不证明内容真实，仍需人工/独立审读。
- 历史数字与当前结论分开写；没有本轮证据就标“历史记录/本轮未复验”。

---

## 7. 基线数字（历史测量，不冒充当前验收）

| 项 | 历史值 | 口径 |
|---|---:|---|
| 官方 DSH 整进程树 | 1,603.8 MB / 9 进程 / 2 窗口（2026-11-03） | 用户任务管理器截图 |
| 官方工具表 | 61 个 / 54,936 字符 / 12,209 token | `research/20` |
| dsh-go 工具表 | **42 个** / 31,630 字节；完整 6,048 / compact 4,149 token（2026-11-03） | 旧配置网关实测，不能证明当前 44 工具 |
| 当前手册记录的工具表估算 | 44 个 / 23,172 字节 / 估算 3,892 token | 代理值，非同模型实测 |
| dsh-go 宿主内存 | 整轮峰值 15.7 MB（2026-11-03） | `scripts/measure-mem.py` 历史记录 |
| dsh-go 冷启动 | 紧探针 p50 27.0–34.8 ms（2026-11-03） | `docs/cold-start.md`，含本机底噪口径 |
| dsh-go 二进制 | 8.4 MB，`-s -w`（2026-11-02） | 历史构建，当前需重测 |
| Viva 原生界面 | 插件化阶段记录约 86 MB、9.08 MB | `../dsh-ui/docs/progress.md`；不是整链当前验收 |

新测量必须写日期、版本、配置和进程树口径。单个估算/实测样本不能外推固定误差方向或换算公式。

---

## 8. 本机环境

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
# GOPROXY=https://goproxy.cn,direct
```

| 资源 | 路径 / 用法 |
|---|---|
| DSH asar | `D:/DeepSeek Harness/Desktop/resources/app.asar/dsh`；只读，经 `ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" script.mjs` 访问 |
| 存量会话 | `D:/DeepSeek Harness/home/sessions/**`；只读，作 golden 输入 |
| 模型网关 | `http://127.0.0.1:3425/v1`；不得关闭 |
| Martty | `C:/Users/Administrator/AppData/Roaming/npm/martty-cli.cmd`；用法见 `docs/martty.md`，实际可用性先查再报 |
| dsc | `refs/dsc/`；只读 |

仓库统一 LF。不得重启/关闭 DSH，不写 profiles/asar，不写旧工作区 `E:/Documents/deepseek-harness/`。测试服务使用临时 home 和 OS 分配端口，不抢现有 GUI 地址，不另起替代服务冒充验证。

---

## 9. 明确不做 / 不得缩减

宿主 P0 不做：JS 兼容桥、`!!js`、cordis 式热重载、多 provider、把默认实现全做成进程插件。team/workflow/schedule/goal/skill 的“不做”只指对应里程碑，不得删除后来已实际实现的能力或借旧表返工。

界面不做 Electron 主壳、账号/充值、埋点/崩溃上报、自动更新、侧栏嵌浏览器、完整 IDE、移动端。**不得再把“只抄 8 包、其余官方部件一律不做”当现行约束**：用户已要求按根本依赖与分层接口逐部件插件化、还原原生体验；具体优先级见 `../dsh-ui/PLAN.md` §8，第三方生态不能用静态 demo 冒充完成。

---

## 10. 主理鱼与唤醒回合

1. 先读 `MAINTENANCE.md` 和 `docs/execution-review.md`，判断属于宿主、Transport 还是独立界面仓。
2. 可复现 bug：失败测试 → 修复 → 定向与集成复验 → PR；复现不了就列出所需版本/步骤/原文，不猜着改。
3. 范围外请求说明所属里程碑，不顺手扩功能；界面路线已按 §2.10 拍板，不再拿旧 Q 表阻止原生开发。
4. 动代码前复核 PLAN；改决策先改 PLAN。
5. 交付逐项记录 PASS/FAIL/SKIP/WARN 和未验证项，不能只写 ALL GREEN。
6. 只对实际处理完毕的 issue/PR 执行 `watch_ack`；新踩的坑写入对应 docs，不重复污染本手册。

唤醒回合不重启 DSH、不写 profiles、不为让 CI 过而放宽门禁。定时唤醒中若工具不允许 goal 创建/恢复，就直接执行当前任务，不反复尝试。

---

## 11. Transport 与界面协作规程

1. 界面实现和构建都在 `../dsh-ui`，本仓不执行 npm 构建。
2. 契约变更先改 `internal/transport/testdata/api.snapshot.json` 及界面快照，再改代码。现有快照主要覆盖 method 与部分响应字段，**不能代替入参、完整 HTTP 信封、错误与 WS 语义测试**；缺什么就补明确契约测试。
3. 宿主监听 `--port 0`，客户端从 `ready {…}` 取地址；不写死端口。stdout 必须恰好一行 ready 握手，诊断只去 stderr。
4. launch token → `/connect` 换 cookie；HTTP 与 WS 使用同一认证语义。客户端先 Connect 成功，再 list/load/subscribe。
5. 事件沿用 v4 原始词汇，不造第二套真相。text/reasoning/tool 块都要投影并由界面消费。
6. 每个会话独立维护 seq/revision/index、流文本与订阅；断线按 history 补缺口，不整页 load，不跨会话串流；后台会话继续接收数据。
7. 审批必须有真实 Transport 请求/响应，原样回传 request id；本地气泡消失不等于审批完成。
8. R7 验证在本仓检查 `internal/transport` 不 import 界面实现，在界面仓检查插件不读 v4 日志。`cmd/mockd` 是过渡假宿主，不能作为真实数据源的永久替代。

界面最终验收是**自己用它干一天活**，不是截图、装配日志或 mock 测试全绿。

---

## 12. 界面线 → 宿主线的需求（唯一跨线通道）

界面需要宿主新增/修正的字段或语义，写入本表；不要只留在聊天。两边各自记录“提供端”和“消费端”，✅ 只表示所写那一层有对应证据，不自动代表整链完成。⏳/🟡 时界面显示未知或隐藏，不编数据。

| # | 状态（2026-11-03 复核） | 需求与当前证据 | 完成判据 |
|---|---|---|---|
| **H1** | 🟡 提供端已补齐（顶层合计），界面消费待验 | `LoadResult` 新增 `usageTotal{input,output,total,cacheRead}`（**全会话累计**）与 `contextTokens`（**最近一条**的 totalTokens，即当前上下文占用）。两者是两件事：累计一直涨，上下文会随压缩回落 | **2026-11-03 真机**：同一会话 `tailMessages=1` 与 `=50` 两次 load，`usageTotal` **完全一致**（`input 7409 / output 211 / total 7620`），`contextTokens=7620`；单测 `TestLoadSessionUsageTotalIndependentOfTail` 钉住「切页不改变合计」。**仍未验**：界面实际消费这两个数，以及未知时不补零的显示 |
| **H2** | 🟡 宿主侧两项已验，界面消费待验 | 消息与**流帧**都要带墙钟 `time`；缺时/非正间隔时界面显示未知 | **① 真实语料**（20 个文件 / 40,231 条事件）：`time<=0` **0 条**、时间回退 **0 处**。**② WS 流**：修前 `帧都带 time` **4/5**——`turn/done` 终局帧没有 time（`publishTurn` 直接构造 `ServerFrame` 漏了它）；补上后 **5/5**，且 `time 单调不减` ✅。单测 `TestServerFrameCarriesTime` 钉住帧里有 time。**仍未验**：界面拿到「缺时/非正间隔」时确实显示未知；且相邻 assistant 的差值**含等待与工具耗时**，只能当区间估算，不能当纯生成速率 |
| **H3** | ✅ 提供端实现有历史证据 | `messages[].view` 透传 ViewSpec；nil 时省略。界面按 kind 渲染，缺 view 回退工具名+摘要 | 本轮未重跑端到端；联调时核 card/table/plain 与无 view 分支 |
| **H4** | ✅ 提供端实现有历史证据 | 取最近一条含非空 model 的 `request/context`；不知道就省略，不拿默认值冒充 | 界面 LoadResult/顶栏实际接入，空值不画 |
| **H5** | ✅ 提供端字段存在 | 返回当前进程 `permission` policy（deny/ask/allow/auto），不是 mode/preset | 界面显示同一进程真实 policy，空值不画 |
| **H6** | 🟡 未闭环 | 子进程 done 后 follow 补读不等于 reasoning 已验证；需要含 reasoning 的真实流和界面消费证据 | text/reasoning/tool 块按序到达，结束帧不丢，界面实际显示 reasoning |
| **H7** | ✅ 两种 content 落法有单测 | 对固定、已完成且原始日志确含 assistant 的样本验证 `data.content` 与 `data.message.content`；失败/取消/在途可合法无 assistant | 不再使用“有 user 就必须有 assistant”的恒强判据；真实样本投影正确 |
| **H8** | 🟡 主路径已修，边角待闭环 | M243 已让任意成功类型包 result，errorBody 带 code/message/hint/retryable，并有 envelope 测试；`writeJSON` 的 encode_failure 字面量仍需补 hint 并加失败分支 | 覆盖 map/struct/list/page 与**包括序列化失败在内的全部错误出口**，统一四字段；跨仓客户端复验 |
| **H9** | ✅ 宿主侧契约测试通过；跨仓待验 | M243 已接 tailMessages；`envelope_test.go` 验证仅 id/tailMessages 时 limit=2，limit 与 tailMessages 冲突时 limit 优先 | 界面只发契约名复验；快照/文档明确别名优先级 |
| **H10** | 🟡 宿主侧同版本已验（5/5），跨仓待联调 | `session.load` 要能加载 `session.list` 返回的任何 id；此前只验过「首个 id 一次成功」 | **2026-11-03 `scripts/h10-chain.py`（同一新二进制 + 全新临时 home + 只回收自己启动的 PID）**：`create` 给 id ✅、`list` 含新会话 ✅、**list 里每个 id 用两种入参名（`sessionId` / `id`+`tailMessages`）都能 load** ✅、`prompt` 后能 load 到 assistant 消息 ✅、响应是 `{result:…}` 信封 ✅ —— 合计 **OK=5 FAIL=0**。**仍未验**：真实 home 全量 id 遍历（home 只读，改法是把项目目录**复制**到临时 home 再验）与界面实际消费 |
| **H11** | ✅ 2026-11-03（进程级已验） | policy 诊断只去 stderr；stdout 恰好一行 ready 握手 | **`scripts/h11-stdout.py` 捕获完整 stdout（不是 `head -1`——那看不到第二行）**：`stdout 恰好一行` ✅、`以 ready 开头` ✅、`之后是合法 JSON` ✅、`含 url 与 token` ✅ —— **OK=4 FAIL=0**。脚本只 `p.kill()` 自己启动的 PID，临时 home，不按镜像名杀 |
| **H12** | 🟡 提供端有，联合待验 | `contentWidth` 0=未知且省略；传值可透传，壳实际供宽度及界面消费未完成 | 不传时字段不出现，传 748 得 748；原生壳提供真实列宽，未知不编默认值 |
| **H13** | ⏳ 新增：真实审批契约 | 现有 13 method 未覆盖审批回传；界面审读时 Allow/Deny 仅清本地气泡 | Transport 定义审批请求/响应与 request id 原样回传；允许后工具执行、拒绝后不执行；取消/超时明确；界面不以气泡消失冒充完成 |

界面侧从 mockd 切真宿主需要改多少代码，以 H1–H13 和 `../dsh-ui/AGENTS.md` 的实际联调为准，**不再承诺“渲染一行不用改”**。
