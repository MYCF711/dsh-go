# 定时方向与派单复核 · 2026-11-04 00:08

> 覆盖 23:08 后的恢复与增量。基线：宿主 `83eaa92`、UI `bc79784`。未修改产品实现、未启动共享树服务、未杀进程、未提交。本轮只做只读复核、Lead 验收和任务表/报告回写。

## 当前门禁

- 宿主 `83eaa92`：`go test ./...`、`go build ./...`、`go vet ./...` 均 **exit 0**（`/tmp/current-host-{test,build,vet}.log`）。
- UI `bc79784`：`go test ./...`、`go build ./...`、`go vet ./...` 均 **exit 0**。
- 两仓工作树仍有未提交改动：宿主 `internal/transport/wssession.go` 与文档；UI `plugins/dssidebar/sidebar.go`、U3A 新测试/四张图/证据文档。全绿不等于 committed 或整产品完成。

## 本轮完成/恢复

### U3A 搜索结果卡 → DONE

- 原 owner `9cdf4a50` 在收尾前失败，但成品完整：`sidebar.go` 改动、`search_result_test.go`、四张 PNG、`docs/visual-search-result.md`。
- Lead 独立复跑 `go test ./plugins/dssidebar -count=1`、`go vet`、`go build ./...`、`git diff --check` 均 **exit 0**；22 个顶层测试。
- 几何：48×268 结果卡、节距 50、内两行 20/17、左缘 28，实测偏差 0；V2 行为未回退。
- 缺口：圆角 12 vs 官方 16（4px，受 `ListRow` 契约限制）、snippet 无真值故隐藏、真宿主/OS 输入/高 DPI 未验。

### W11 隔离验证 → DONE（限 4f7c885 两态）

- 原 owner `9dff3286` 失败，留下两个隔离 worktree；Lead 接管完成。
- A=`4f7c885` committed；B=`4f7c885 + wssession patch`。
- 两态 `bin-check`、`build`、`vet`、`test`、`h-suite --fast`、`race` 全部 **exit 0**。
- h-suite fast 非模型探针 **40/40**；6 条真实模型/真实审批/UI-e2e 按设计 SKIP，不记通过。
- race 合计 OK=1 FAIL=0；只证明无数据竞争，不证明 replay→Subscribe 原子性或 pendingReplay 顺序。
- 当前 `83eaa92` 只补了 test/build/vet；race/h-suite 未在该 rev 复跑，不能外推。
- 证据：[h-suite-validation-2026-11-03.md](h-suite-validation-2026-11-03.md)。

### A→B→A 与刷新覆盖

- `eb7306d` 诊断后确认 A→B→A 对账为界面 8 条=宿主 8 条，四轮问答俱在（`tmp/aba3.png`）；此前 6vs8 是对账时机误判。U1-H6 的该子项已关闭，其余链路缺口保持。

## 仍未闭环

- **U0/H14**：reset 局部 `since`、History 请求 `journalEpoch`、UI 快照旧 `/api/mux`/`kind`/messages、共享 ws-golden、宿主重启 cookie；409 重建有候选实现但无单测/换代真机。
- **W1/W3/W5/W7**：FrameSub 重放仍是未提交 worktree，且 replay→Subscribe 非原子；serve 零水位重读、debug 绕过 journal；`stale_epoch` 缺 `retryable`；e2e 空 load 假阳性探针未修。
- **W4/U1-H13**：pending cancel、UI 自己回传审批同版本复验、审批 reset 恢复均未完成。
- **U2**：完整授权树、普通禁用/替换、逐插件卸载与全副作用撤销、公开槽实画；`DrawSlot depth>8` 静默 return。
- **U1-H6**：`Stream.Reset` 未清 `tools`，第二轮同 `Tool+Args` 可能复用旧结果；侧栏真实标题/时间/逐会话活动点未验。
- **PERF0 已完成（00:22 追加）**：G1/G6/G8 有实测数；**G7 判 FAIL**（ref×sw 9/9 差 7.3%，直接打开中栏纯空白）；脏 wssession patch 导致 turn 137,246 次重连/120s 超时，干净 4f7c885 14s 正常。详见下节。
- **U4P/PERF**：仅设计；A/B 与 10 项性能均未执行。RACE 是 `bf7e888` 历史记录，不是当前版本证明。

## PERF0 完成追加（00:22）

- 产出：`scripts/perf-gap.sh`（1637 行）、`docs/perf-zero-results.md`（445 行）、`tmp/perf-gap/{summary.json,meta.json,g7-matrix.json,...}`；`bash -n` exit 0，真实执行 3 次均 exit 0。
- **G1**：界面静置 Dedicated 5.1 MB（3 轮 3.1–7.1）、Local 24.3、Shared 19.2；纹理单项 unavailable。
- **G6**：纯文本 3/3，turn 中位 30.2 s，峰值 WS 97.5 / priv 139.0；结束后静置 93.2–102.9，不回落；TTFT unavailable。
- **G7 判 FAIL**：ref×ref 0–60 px、sw×sw 0、ref×sw 9/9 差 73,364–73,424 px（7.3%），全在中栏；直接打开时中栏唯一色=1（纯背景），切回后 185 色。**exit 0 只代表程序跑完，不表示判据通过**。
- **G8**：serve 空载 8.7/45.1；一轮整树 281.9–304.2 MB WS；界面+整树 169.7–176.3 MB WS / 243.2–246.6 MB priv。
- **高优先级跨项发现**：脏工作树 `wssession.go`（+30 行重放 patch）令 turn 进 137,246 次重连、120 s 超时；干净 `4f7c885` 同参数 14.0 s、2 次重连。该 patch 在修好前不得用于真机性能/h-suite 结论。
- PID 复用曾把 Armoury 进程算进宿主整树；已加创建时间剪枝但正式轮未撞上，剪枝有效性未实证。
- G7 空白中栏与重连风暴已分别写入 U1-H6、W1/W7；PERF0 本身 DONE，但 G7 不通过。

## 任务表动作

- W6/R0：更新到 00:08 复核，证据为本报告。
- W11：DONE，两态结果、当前 HEAD 未外推、原 owner 失败均写明。
- U3A：DONE，Lead 独立复验并保留缺口。
- PERF0：保持 IN_PROGRESS，无产出不签 DONE。
- U0/U2/U1-H6/U1-H13/W7 保持 IN_PROGRESS，不重复派单。

## 纪律

- `66ec07d` 滚轮修复为 Lead 直接实现，属流程偏差；后续实现必须外派，Lead 跑测试只算验收。
- 本轮未创建/恢复/完成 goal；未在共享树运行 h-suite/race/regress/check。
- 两个子代理失败后，未把“失败”写成“完成”；U3A/W11 均按实际成品和 Lead 验收签收。

## 未验证

当前 `83eaa92` 的 race/h-suite、真实 h14/h13/ui-e2e、共享 golden、History epoch、reset 局部水位、宿主重启自愈、pending cancel、stale_epoch 四字段、FrameSub 同版本、U2 完整生命周期、`Stream.Reset` 工具对账、PERF0 全部实测、A/B 实际执行、OS 原生输入/高 DPI。
