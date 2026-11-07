# AGENTS.md · DSH-GO

> Go 重写 DSH 宿主；上游基线 `0.2.2-rc.1`，当前版本读源码与 `CHANGELOG.md`。
> 原目标：低占用、高性能、可替换能力、还原 DSH 使用体验并接管真实工作。界面在独立 `../dsh-ui` 用 Go+Viva；Web 仅可关闭过渡件。

## 0. 每次开工按此顺序

0. **GO.1 专项必读（当前封版阻断）**：凡接手 W163/W167/W168/W170/W171/W175/W200，先读并执行 [GO.1 Lead 可执行任务表](docs/go1-lead-execution-task-table-2026-11-07.md)；先核 W200 真实现场，严格按其中轮次、依赖、单 PTY 锁和封版门推进，不重复派写渲染文件。W200 未明确交付前，`LIVE` 对其他人只读。该表是 GO.1 专项入口，不替代 `PLAN.md` §14.1。
1. **先核源码再领任务**：读 PLAN/MAINTENANCE、git status/log 与相关源码/测试；H 表是证据非派工单，先验证再回填，不回滚/重做。
2. **入口**：宿主 `PLAN.md` §14.1；界面 `UIPLAN.md` §3–§4 + `../dsh-ui/PLAN.md`。先 H14，再 H6/H13/跨仓验收；H8 可并行。
3. **Lead 只拆包/派单/验收**：≥2 个独立 READY 同轮外派，派后不 wait/poll，完成方通知；验收后更新任务表并派下一项。任务书写 owner/paths/rev/依赖/红测/验收/证据，同文件单写者，契约/壳串行。**子代理在自身作用域同样是局部 Lead**：可再派独立子任务，但路径只能是父任务子集，默认深度≤3、单父并发≤4；父级必须集成复验后再通知根 Lead，完整规则/正例/反例见 `$DSH_HOME/LEAD.md` §1.5。
3.1 **本工作区团队编制（Lead 必读，用户 2026-11-07 指定）**：派单前先读 [LEAD.md](LEAD.md) §2 账本与 §3 防重复检查。固定 5+1 角色：`lead`（只拆包/集成/验收）、`architect`（**`group/kimi-k3.1-pro`，只读**）、`backend`/`tui`/`qa`/`gui`（**`group/deepseek-v4.1-flash-1017`**）。**只有方案师用 Kimi，且不得写产品；执行层不得继承 Kimi。** 同一文件同一时刻只允许一个 active writer；派单必须先有 `task_id/owner/model/写作用域/依赖/验收/产出`，账本无行不派、同 scope 已有 IN_PROGRESS 不派、DONE 无产物不重派。本工作区模型分配覆盖 `$DSH_HOME/LEAD.md` §1.1 的默认限制。GUI 按用户 2026-11-07 指令解除“封版前暂停新增任务”，改为执行 UI 插件对齐（见 §0.5），宿主与 TUI 封版门不变。

3.2 **官方 0.2.2 同步是当前最高优先（用户 2026-11-07 指令，三条线缺一不可）**：接口、宿主插件、UI 插件三者都算“同步官方”，只做接口不算完成。
- **目标口径（用户 2026-11-07 明确）**：要的是**完整功能 + 后续重构 GUI 可用的稳定接口**，不是对接官方 Electron 壳。因此官方 153 个方法必须**逐条有确定结论**（真实现，或官方形状的结构化未实现 + 写清缺什么），禁止留空/假数据/静默成功；**M0（serve 官方前端 dist + index 注入）降为可选**，不再作为目标。
- **接口面**：唯一清单 [docs/official-desktop-api-parity.md](docs/official-desktop-api-parity.md)（官方 153 个 typert 方法、宿主 58、同名 8、缺 145）。L1 按官方 zod 契约实现参数与结果字段，落在现有 `POST /api/<method>` + 每会话 WS journal；**L2 的 `remote.mux` + typert Connection wire 兼容单列程序，未实现前不得声称与官方桌面端 wire 兼容**。既有 58 个方法不改名，官方同名走新增；缺真实支撑一律四字段未实现错误并登记，禁止假数据、静默成功、假流式。官方契约原件可从 `app.asar`（sha256 `5fa8a6b2…`）重建，镜像在 `/tmp/official-api-src`。
- **宿主插件面**：唯一清单 [docs/official-plugins-022-map.md](docs/official-plugins-022-map.md)（官方 base 97 项：DONE 37 / PARTIAL 32 / MISSING 28）。按能力逐项 PARTIAL→DONE、MISSING→DONE；平台型（40 sandbox runner、42 bash-sandbox、43 pwsh-sandbox、72 ptc-runtime）按官方平台开关与 R6 显式记录不做或后置，**不得用“生态欠账”一句话带过**。改映射表不算实现。
- **UI 插件面**：唯一清单 [docs/official-ui-plugins-parity.md](docs/official-ui-plugins-parity.md)（官方 60 个 `dsh-client-ui-*` + 5 个实验包；本仓 24 个 `ds*`）。P0 首屏与主交互 → P1 面板与 11 个 `ui-settings*`、`plugin-manager` → P2 生态/实验；新部件按官方包名落目录；官方无同名包的 `dstopbar`/`dsstatusbar` 必须有官方结构证据，否则 derived 标注或 HIDE。

3.3 **方案师持续产出（用户 2026-11-07 指令）**：`architect`（`group/kimi-k3.1-pro`，只读）必须**持续**维护两份文档，不得中断：
- **方案书** [docs/architect-official-sync-blueprint.md](docs/architect-official-sync-blueprint.md)：三条线的整体架构、分层边界（L1/L2）、依赖顺序、风险与反例、每轮必须给出的 GO/NO-GO。
- **任务书** [docs/architect-official-sync-taskbooks.md](docs/architect-official-sync-taskbooks.md)：可直接下发的任务书，逐条含 `task_id / owner / model / 写作用域 / 依赖 / 先红 / 验收 / 产出`，按轮次排序。
规矩：Lead 每次收到架构或缺口变化（新证据、新交付、新 BLOCKER）必须**当轮重派一次**方案师刷新两份文档；刷新写进两文档顶部的「本轮更新」小节（时间、触发原因、改了什么）。Kimi 只读，不得写产品代码，不得跑 PTY/compile；Lead 收到后必须核对 `file:line` 与账本，无产物视为未完成。12–15 分钟无 artifact 就按现有方案继续，不阻塞执行线。

4. **契约先行**：唯一 owner 先冻结 `docs/transport-contract.md`、双仓快照/golden，再并行实现；跨仓必经真实 UI apiclient，mockd/手写 JSON/ACP 各只证明自身层。
5. **分层验收**：代码 ≠ 单测 ≠ 宿主执行链 ≠ 跨仓联调 ≠ 产品完成；回填写 rev/配置/命令/退出码/断言/证据/未验项，不以历史 ✅ 替代。
6. **真实终验**：同一新 dshd + `../dsh-ui` + 临时 home，跑认证/list/load/发送/text/reasoning/tool/审批/取消/续历史/断线恢复，再做完整工作日、A/B 和整树性能。
7. **官方 UI 结构树先行、素材优先复用（用户 2026-11-05 拍板）**：**根本原则是 DSH 已是完整成熟产品，可见部分直接复用官方结构树+素材；我们只做看不见的部分重构（Go 宿主、Transport、运行时、状态、性能），不是重新设计一套 UI。** **本项目采用 DSH 原生结构树、素材和设计语言，实行最小自制约束，力求最原生 DSH 交互体验；缺官方证据或真实能力就 HIDE，不自造布局/文案/图标/交互。** 全会话唯一主库为 `$DSH_HOME/dsh-ui-official/`，入口 `$DSH_HOME/DSH-UI-official.md`；主库已有内容直接复用，不重复解构。先拆 `../dsh-ui/tmp/official/**` 与 `tmp/asar-css/**` 的组件层级、slot、CSS 几何、行为和素材，冻结 `../dsh-ui/docs/official-shell-tree.md`、`../dsh-ui/docs/official-settings-tree.md`、`../dsh-ui/docs/gui-structure-gap.md`，再按结构树一比一实现；禁止凭截图自造层级或把“功能存在”当“结构相同”。官方素材优先从 `../dsh-ui/assets/official/**` 与 `plugins/dsassets/**` 复用，记录 file:line、sha256、viewBox；只有官方确无素材时才 derived，并明确标注，不手画冒充官方。设置模态按官方 5 section / 11 general slot（全量 Host 可形成 12 行）/ 11 个 `ui-settings*` 包核对，不自造“目录与新会话默认”等顶级层级。W99/W100/W101/W102 已冻结结构证据，W103/W104/W106 按同一规则实现与验收。

## 1. 不可越过的边界

| 红线 | 规则 |
|---|---|
| R1 内核 | `internal/kernel` 不识 agent/tool/session/llm；`go list -deps` 不含业务包/modules |
| R2 SPI | 模块只拿 `*Scope`，不拿 `*App`/全局单例；禁止 `init()` 注册 |
| R3 工具 | 结构化有界输出；失败 `Result{Hint:"…"}, err`；不得空 Result 丢 Hint |
| R4 契约 | schema/快照变更显式审查；禁止为变绿删测试、放宽断言或盲更新 golden |
| R5 会话 | v4 append-only；滑窗/压缩/截断/Transport journal 都是派生投影，读路径不写盘 |
| R6 宿主 | 不嵌 JS 运行时、不 fork/vendor dsc、不用 mvdan 执行命令；不禁止独立 Viva |
| R7 跨线 | 只经 Transport+ViewSpec；宿主不 import UI，UI 插件不直读 v4 日志 |

宿主线只改 `cmd/dshd/`、`internal/`、`modules/`、`codecs/` 及宿主文档；界面实现/构建都在 `../dsh-ui`，本仓不建 TypeScript UI，不跑 npm。跨线需求只写 `docs/transport-contract.md`。

**沙盒不得冒名**：approval、工具白名单、FS路径边界、Windows Job Object/Unix进程组都不是完整沙盒。当前三档与一次性 `sandbox_permissions` 尚未生产接线；实现与验收严格按 [docs/sandbox-policy.md](docs/sandbox-policy.md)，core、生产策略、OS隔离分层写证据，未到的层级不得写DONE。

## 2. Tool / SPI 摘要

唯一代码定义是 `internal/spi/tool.go`：`Name / Description / Schema / Caps / Exec`。`Caps()` 必填 writes/executes/network/destructive，审批与沙箱不按工具名猜。所有注册错误必须返回，禁止吞错。新 SPI 同时交默认实现、替换样本、禁用 fail-loud 测试。

v4、提示装配与架构理由读 `PLAN.md`；P0 旧工具/“不做”清单不得用于删除后续能力。process/wasm 的背压、隔离、自包含与协议选择读 `PLAN.md` §3.1。

## 3. Transport canonical 摘要

完整且唯一详细定义在 `docs/transport-contract.md`，开工必须主动读它，不能只靠本入口：

- HTTP `POST /api/<method>` body 是**裸参数**，不包 `params`；成功 `result`，错误四字段；先 `/connect` 换 cookie，HTTP/WS 同认证。
- WS 为 **`/ws`**；控制帧 `t=sub/unsub/ping`，服务端 `{seq,type,data,time}`，type 只取白名单（v4 原词、turn done/error、approval、reset、pong）。
- **每会话一条独立 WS + 一个 Transport journal**；seq 在 journal 追加时分配，不能是 v4 seq、全局 turn seq或连接序号。
- WS 与 `session.history` 读同一 journal；history 返回原始 `frames`，不是抽过 text 的 messages。load 只作首屏投影。
- `journalEpoch` 随 load/history/sub 传递；不匹配用 `session/reset`/`stale_epoch` 强制重建基线。reasoning/tool/approval 断线后必须可恢复。
- H14 只验契约夹具和合成帧重放；真实生成归 H6，允许/拒绝真实执行归 H13，UI 产品消费归 U1/G2，避免循环门。

## 4. 共享仓与进程安全

- 改前重读；`FS_STALE_VERSION` 先合并。禁止 checkout/reset 覆盖他人、原地移动 `modules/` 或兄弟仓、按镜像名杀 `DSH GO.exe/app.exe`。
- `scripts/regress.sh` 有全局 taskkill，`check.sh` 原地移动 modules：隔离修复前禁在共享树运行；造红/变异只在独占副本。
- 测试用临时 home、`--port 0`、ready/token、无头窗口；只回收自己启动且核对归属的 PID。不得重启/关闭 DSH/magpie，不写 profiles/asar/原始会话，不抢现有 GUI 地址。
- 提交只 add 认领路径并查 cached diff；**两仓 push 都需用户当次确认。**
- **并行任务各自开本地分支**（`git worktree add ../<task>-<id> -b task/<id>`），不得多个 agent 共用一棵工作树或共用 main；Lead 验收全绿后显式 `git merge --no-ff task/<id>` 合入，**禁止 `git add -A`**。测试红的分支不进主线；beta/日常分支只留本地，稳定版满足铁律且 CI 通过后才推 GitHub。

## 5. 验证与交付

**Go 诊断工具、测试预设与红点分类见 [docs/go-toolchain.md](docs/go-toolchain.md)。先做目标包/headless 确定性复现，再上 PTY；模型时序抖动不派产品补丁。**

**Node/TS TUI（dsh-TUI/W170/W171/W167）的 hang/crash/React #300/ConPTY 检查单见 [docs/node-tui-debug.md](docs/node-tui-debug.md)；不要用 Go 工具硬套 Node 层。**

**可见缺陷优先于矩阵绿。** 交付/日志/报告出现 `依然存在`、`仍`、`未消除`、`known issue`、`边界`、`UNVERIFIED`、`legacy`、`临时`、`降级`、`超出本任务` 时，必须：
1. 先判断是产品缺陷、测试夹具、还是模型行为，不能一句“边界”带过；
2. 在 `LEAD.md` 建 `KNOWN-ISSUE` / `P1` / `BLOCKER` 行，写 owner、证据、是否阻塞 GO.1；
3. 在给用户的总结里显式写出，不能只埋在报告末尾；
4. `UNVERIFIED` 不是通过；主矩阵 `FAIL=0` 不能替代可见缺陷关闭；测试目标模型/命令不在 catalog 时先修夹具，不算产品通过。
5. 无未登记的可见缺陷，才允许把任务标 `DONE`；接受为 known issue 的，必须有用户/Lead 的明确决定行。

**TUI/宿主归属先对照再修。** 同一 TUI 版本、同一驱动在官方宿主复现 → 归 TUI/测试/模型，不进 Go 宿主；只有官方宿主绿、Go 宿主红 → 才是宿主范围。禁止把 TUI 回归、测试夹具过期或模型不遵循提示记成宿主缺陷；升级 TUI 前先确认版本是否在官方验证线内。

**提交前必须自证 HEAD 单独能编译**（工作树绿不算）：

```bash
git stash -u --keep-index -q && go build ./... ; git stash pop -q
```

自纠：`check.sh` 会 `mv modules`；**门禁一律在 `git archive` 副本跑**，共享树只跑定向 test/build/vet。

自纠：真机用过陈旧 `tmp/DSH GO.exe`，未先 `bin-check`，误判思考块未落盘。**真机第0步必须 `python scripts/bin-check.py`**。

自纠（W78 已实证）：Windows 真机/真实链除新鲜度外，还必须核对**二进制启动位置关联的进程完整性级别**。本机同一 dshd 从 C: 证据目录启动为 High `S-1-16-12288`，从 D: 共享工作区启动为 Medium `S-1-16-4096`；Medium 会在 MSYS `\BaseNamedObjects\msys-2.0S5-…` 报 `0xC0000022`、bash 以 `0xC0000142` 退出，并不是产品 jobs/Job Object 逻辑回归。真实模型、TUI、GUI、bash 与 real-chain 统一把**新构建二进制放到 C: 证据目录**运行，记录 `DSHD_ENV` 与 user/session/integrity；D: 红证据保留，不改 ACL，不用 D: Medium 二进制判产品。详见 `docs/msys-startup-acl.md`。

**写了不跑的探针等于没有。** 2026-11-04 一个代理交付了没跑过的真宿主探针；第一次跑就抓到“一次 prompt 变 2~3 轮”，而单测全绿。新探针必须贴本次汇总 `OK=n FAIL=m` 并接门禁；接不进要写谁跑、何时跑。

**`cmd | head` 会把退出码换成 head 的。** 2026-11-07 Lead 自查时踩过：`go build ./... | head -6; echo $?` 打印 `0`，而真实 `internal/llm` 是编译红（并行写者半写）。门禁命令一律 `> log 2>&1; echo "EXIT=$?"` 或 `set -o pipefail`，禁止把管道最后一个命令的状态当门禁结果。

**Windows `.syso` 目录树必须排在数据前**：顺序反了 build/run 全正常，只有 VersionInfo 全空。唯一判据：`(Get-Item x.exe).VersionInfo.FileDescription` 非空；已由 vsinfo 两条布局测试钉住。

三事故：漏 add untracked 实现；`git add` 带入别人半成品；测试留工作树。规则：按目录 add、先看 cached diff、只认自己路径；实现与测试同提交；收尾看 `git status`。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./...
go list -deps ./internal/kernel   # 不含 modules/ 与业务包
```

1. 真机前 `bin-check`；陈旧/分叉立即中止（案例见 `docs/verify-rules.md`）。
2. 无机器汇总=`FAIL`，0匹配不报绿；红先分代码/判据/等待窗。
3. 新守卫在 `guard-selftest.py` 加“弄坏必红”；各层不外推整链。
4. `-race` 未成功=未验证；有C工具链跑 `race.sh`；工具表真分词，二进制≤25MiB，ALL GREEN记SKIP/WARN。
5. 缺陷先红后修，只隔离旧实现造红；不破坏共享源码、不盲改golden/放宽断言。

## 6. 文档与维护

- 稳定规则在本文件与 PLAN；宿主顺序只在 `PLAN.md` §14.1，跨线契约/证据只在 `docs/transport-contract.md`。
- 决策先改 PLAN；代码冲突先判文档过期还是代码越约。委派附完整任务书，不假设对方读过全文。
- 版本：稳定版 `<上游版本>-go.<N>`，测试版 `<上游版本>-beta.<N>`；前缀升级后各自从 1 开始；仓库 LF；只 ack 完成项；定时唤醒不能操作 goal 就直接推进。
