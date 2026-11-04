# AGENTS.md · DSH-GO

> Go 重写 DSH 宿主；上游基线 `0.2.1-rc.5`，当前版本读源码与 `CHANGELOG.md`。
> 原目标：低占用、高性能、可替换能力、还原 DSH 使用体验并接管真实工作。界面在独立 `../dsh-ui` 用 Go+Viva；Web 仅可关闭过渡件。

## 0. 每次开工按此顺序

1. **先核源码再领任务**：读 PLAN/MAINTENANCE、git status/log 与相关源码/测试；H 表是证据非派工单，先验证再回填，不回滚/重做。
2. **入口**：宿主 `PLAN.md` §14.1；界面 `UIPLAN.md` §3–§4 + `../dsh-ui/PLAN.md`。先 H14，再 H6/H13/跨仓验收；H8 可并行。
3. **Lead 只拆包/派单/验收**：≥2 个独立 READY 同轮外派，派后不 wait/poll，完成方通知；验收后更新任务表并派下一项。任务书写 owner/paths/rev/依赖/红测/验收/证据，同文件单写者，契约/壳串行。
4. **契约先行**：唯一 owner 先冻结 `docs/transport-contract.md`、双仓快照/golden，再并行实现；跨仓必经真实 UI apiclient，mockd/手写 JSON/ACP 各只证明自身层。
5. **分层验收**：代码 ≠ 单测 ≠ 宿主执行链 ≠ 跨仓联调 ≠ 产品完成；回填写 rev/配置/命令/退出码/断言/证据/未验项，不以历史 ✅ 替代。
6. **真实终验**：同一新 dshd + `../dsh-ui` + 临时 home，跑认证/list/load/发送/text/reasoning/tool/审批/取消/续历史/断线恢复，再做完整工作日、A/B 和整树性能。

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

- 改前重读；`FS_STALE_VERSION` 先合并。禁止 checkout/reset 覆盖他人、原地移动 `modules/` 或兄弟仓、按镜像名杀 `dshd.exe/app.exe`。
- `scripts/regress.sh` 有全局 taskkill，`check.sh` 原地移动 modules：隔离修复前禁在共享树运行；造红/变异只在独占副本。
- 测试用临时 home、`--port 0`、ready 地址/token、无头窗口；只回收自己启动且核对归属的 PID。不得重启/关闭 DSH 或 magpie，不写 profiles/asar/原始会话，不抢现有 GUI 地址。
- commit/PR/push 由任务书显式授权；提交只 add 认领路径并检查 cached diff，不夹带他人改动。**两仓 push 都需用户当次确认。**

## 5. 验证与交付（硬规则）

**提交前必须自证 HEAD 单独能编译**（工作树绿不算）：

```bash
git stash -u --keep-index -q && go build ./... ; git stash pop -q
# 或 git archive HEAD | tar -x -C /tmp 后 build
```

2026-11-04 自纠：`check.sh` 会 `mv modules`，并行会话 build 会看不到 modules/。**门禁一律在 `git archive HEAD | tar -x -C /tmp/gate-iso` 副本跑**；共享树只跑定向 test/build/vet，不动别人。

2026-11-04 自纠（代价最大的一次）：真机验 ACP 是否落思考块时，我用的是 `tmp/dshd.exe`，
**没先跑 `python scripts/bin-check.py`**——那是个陈旧二进制，于是看到"思考块没落盘"，
接着花了很久去查网关、查客户端解析、查 `reasoning_effort` 有没有发出去（全都正常）。
换成新编的二进制，第一次就落出 `['reasoning','text']`。
规矩不是多写一句"记得验二进制"，而是**把它排在真机步骤第 0 位**：不加这一步，后面的结论全部不可信，
而且它会把"陈旧二进制"伪装成"产品缺陷"，让人去改对的东西。

2026-11-04 三事故：① `main.go` 已提交，untracked 的 `tool_merged.go` 漏 add；② `git add main.go` 带上别人未提交的半成品 `prepareLazy`；③ 实现已提交、钉它的测试还留在工作树。规则：并行提交按目录 add 并先 `git diff --cached --stat`，只认领自己真改的路径；实现与测试同一次提交；收尾 `git status` 检查未跟踪文件。

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build ./... && go vet ./...
go test ./...
go list -deps ./internal/kernel   # 不含 modules/ 与业务包
```

1. 真机前 `python scripts/bin-check.py`；陈旧/分叉立即中止，案例见 `docs/verify-rules.md`。
2. 无机器汇总=`FAIL`，0匹配不报绿；红先分代码/过期判据/等待窗。
3. 新守卫在 `scripts/guard-selftest.py` 加“弄坏必红”；各层不外推整链。
4. `-race` 未成功=未验证；有C工具链跑 `scripts/race.sh`；工具表真分词，二进制≤25MiB，ALL GREEN仍记SKIP/WARN。
5. 缺陷先红后修，只隔离旧实现造红；不破坏共享源码、不盲改golden/放宽断言。

## 6. 文档与维护

- 稳定规则在本文件与 PLAN；宿主顺序只在 `PLAN.md` §14.1，跨线契约/证据只在 `docs/transport-contract.md`。
- 决策先改 PLAN；代码冲突先判文档过期还是代码越约。委派附完整任务书，不假设对方读过全文。
- 版本 `<上游版本>-go.<N>`，N 为 Go 自有序号；仓库 LF；只 ack 完成项；定时唤醒不能操作 goal 就直接推进。
