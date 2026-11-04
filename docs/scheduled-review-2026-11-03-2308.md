# 定时方向与派单复核 · 2026-11-03 23:08

> 覆盖 22:08/23:08 两轮唤醒。基线：宿主 `4f7c885`、UI `bc79784`。未修改产品实现、未启动服务、未杀进程、未提交。本轮 Lead 只做只读复核、任务表和报告回写。

## 当前可确认的完成项

- 两仓当前工作树完整门禁：`go test ./...`、`go build ./...`、`go vet ./...` 均 **exit 0**（宿主含 M293–M295 工具新增；UI 含 bc79784 收进视觉文档）。
- 视觉 V1–V4B 的 DONE 保持不变；`bc79784` 已把五份视觉证据、A/B 设计（735 行）、PERF 设计（308 行）与 V1 后续改动纳入提交，不再是“未提交文档”。
- 实时 tool→轨迹+自动开右栏（`c60c6ba`，`tmp/tool-live.png`）、首启水位/epoch（`80a5754`，`tmp/replay-fix2.png`）、窄窗右栏消失（`7d43574`）已有真机证据。
- **A→B→A 连续流已闭环**：`eb7306d` 诊断后确认界面 8 条=宿主 8 条，四轮问答俱在（`tmp/aba3.png`；此前 6vs8 是对账时机误判，不是丢历史）。
- U2 的 Dispose 收口（`8b3f382`，先红后绿）、公开槽排序/过滤（`89e42cf`）已过测试；U0 的 history 409 重建有实现候选（`48f036b`）。

## 仍未闭环（不得写成全部完成）

- **U0/H14**：`session/reset` 局部 `since` 未归零；`session.history` 请求仍不带 `journalEpoch`；UI 快照仍写 `/api/mux`、旧 `kind`、history `messages`；无共享 `ws-golden`；宿主重启 cookie 无自动 `/connect`；409 重建无单测/换代真机。
- **W1/W3/W5/W7**：`wssession.go` FrameSub 重放仍是未提交工作树，replay→Subscribe 非原子、pendingReplay 顺序重/丢风险未测；serve follow 零水位重读、debug `Seq=1`/无 `time` 绕过 journal；`stale_epoch` 错误体仍缺 `retryable`；e2e 空 load 假阳性探针未修。
- **W4/U1-H13**：pending cancel 仍未实现；UI 自己回传审批尚未同版本复验；审批 reset 恢复未证。
- **U2**：完整授权树、普通部件禁用/替换、逐插件卸载与全副作用撤销、公开槽实画（`DrawSlot depth>8` 静默 return）未闭环；`Effect` 测试的 `cleaned` 断言仍须补。
- **U1-H6**：`Stream.Reset` 未清 `tools`，第二轮同 `Tool+Args` 可能复用旧结果；侧栏真实标题/时间/逐会话活动点未验。
- **U4P/PERF**：只完成设计；A/B 两臂一次未跑，PERF 十项均未实测。RACE 是 `bf7e888` 历史记录，不代表当前 `bc79784`/宿主工作树。
- `product-acceptance.md` 的整进程树 ✅、`#20 验证中`、`#24 掉帧` 口径仍与设计/实测状态冲突，需 UI Lead 分项纠正。

## 本轮实派（同轮、无重叠）

> **00:00 恢复记录**：U3A 代理在收尾前失败，但完整交付了实现、22 个测试、四张图和证据；Lead 复跑 `go test ./plugins/dssidebar`、vet、build、diff-check 均 exit 0，已按 DONE 验收（缺口照实保留）。W11 代理也在收尾前失败，留下两个隔离 worktree；Lead 接管后完成 A/B 两态验证：`bin-check`/`build`/`vet`/`test`/`h-suite --fast`/`race` 全部 exit 0，非模型探针 40/40，race 合计 OK=1 FAIL=0，证据见 `docs/h-suite-validation-2026-11-03.md`。当前 `83eaa92` 另跑 test/build/vet exit 0，但 race/h-suite 未复跑。PERF0 尚未见产出。

| ID | owner | 范围 | 交付 |
|---|---|---|---|
| **U3A** | `9cdf4a50` | `plugins/dssidebar/**` + 新证据 | 官方 48px 搜索结果卡，无 snippet 真值隐藏，两尺寸 ≤8px |
| **PERF0** | `f1dfd6e0` | `scripts/perf-gap.sh`、`docs/perf-zero-results.md` + tmp | G1/G6/G7/G8 实测，unavailable 不记 PASS |
| **W11** | `9dff3286` | 隔离副本 + 新 `docs/h-suite-validation-2026-11-03.md` | committed 与 `+wssession diff` 两态跑 bin-check/h-suite fast/race/build/vet/test |

PERF0 与 W11 的负载错峰要求已写入任务书；`scripts/regress.sh`、`scripts/check.sh` 禁止在共享树运行。三项目前均无产出，等 owner 主动通知后验收。

## 纪律

- Lead 直接实现记录仍存在（`66ec07d` 滚轮、`c4f1d63` 部分文档），属流程偏差；后续实现必须外派，Lead 直跑测试只算验收。
- 两仓任务表、报告均为工作树改动，不冒称已提交。
- 本轮未创建/恢复/完成 goal。

## 未验证

完整工作日、A/B 实际执行、PERF0 实测、共享 golden、History epoch、reset 局部水位、宿主重启自愈、pending cancel、stale_epoch 四字段、serve 去重/debug 统一、FrameSub 同版本复验、U2 完整生命周期、OS 原生输入/高 DPI、`Stream.Reset` 第二轮工具对账。