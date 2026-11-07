# AUDIT-GUI-KNOWN-ISSUES · GUI 已知限制 / 未验 / 可见缺陷只读审计

> 任务 `AUDIT-GUI-KNOWN-ISSUES` · 模型 `group/deepseek-v4.1-flash-1017` · 2026-11-07 08:40–09:00 +08:00
> 基线：`../dsh-ui` 工作树 `feat/naming-fulltime`，HEAD `3925e84`。只读；本文件是唯一新增，未改 GUI/产品/任务表。
> 判据按 `AGENTS.md:63-68`「可见缺陷优先于矩阵绿」：`UNVERIFIED` 不是通过，`FAIL=0` 不能替代可见缺陷关闭。

## 0. 本次实跑（原样，未编辑）

```text
$ cd ../dsh-ui && CGO_ENABLED=0 go build ./...                       → BUILD_EXIT=0
$ CGO_ENABLED=0 go vet ./...                                         → VET_EXIT=0
$ CGO_ENABLED=0 go test ./...                                        → TEST_EXIT=1
--- FAIL: TestRealTopbarThirdPartyReplaceAndRestore (0.02s)
    plugin_lifecycle_test.go:203: uikernel: 槽授权失败 parent="shell.root" child="main.header" painter="ds-topbar/bar" path=shell.root -> main.header: 父槽未显式允许这个子槽
--- FAIL: TestRealBuiltinAssemblySatisfiesSlotAuthorization (0.00s)
    official_service_auth_test.go:82: 官方 shell.root painter = 3，期望 1
--- FAIL: TestUserImageBitmapRequiresRealBytesAndUsesBGRA (0.00s)
    attachments_test.go:126: bitmap 几何/身份错误：{Pix:[0 0 255 255 0 255 0 255] Width:2 Height:1 Stride:8 ID:5346950235639184572 Version:1}
--- FAIL: TestMessageAreaOfficialGeometryAtTwoWindowSizes (0.83s)
    message_area_headless_test.go:179: 长助手估高+官方上下 padding 与实画内容高不一致：estimate=285 actual=257 markdown=203
--- FAIL: TestW118OfficialChatGeometryAndStreamRealViva (0.42s)
    official_conversation_headless_test.go:209: ScrollArea 内容高=126 官方含上下16 padding=154
--- FAIL: TestReasoningExpandedBodyUsesRealMarkdown (0.00s)
    reasoning_test.go:158: 思考展开正文未走完整 Markdown：heading=true inline=true marker=true rawSignature=false rawMarkdown=true
--- FAIL: TestW120TwoSizeSessionSwitchHiddenRetentionHitsAndABEvidence (0.16s)
    official_rightbar_test.go:571: headless: no control labelled "退出全屏"
--- FAIL: TestV4AHeadlessResizeAndScreenshots (0.12s)
    visual_test.go:124: 全屏后轨迹没有进入全画面板：beforeX=1108.7568 afterX=1108.7568
FAIL  github.com/MYCF711/dsh-ui/cmd/app
FAIL  github.com/MYCF711/dsh-ui/internal/uikernel
FAIL  github.com/MYCF711/dsh-ui/plugins/dsconversation
FAIL  github.com/MYCF711/dsh-ui/plugins/dsrightbar

$ bash scripts/headcheck.sh test                                     → HEADCHECK_EXIT=0  (PASS 干净检出 test)
$ git status --porcelain | wc -l                                     → 316（其中 `??` 214）
```

**读法：HEAD 单独是绿的，红的是"已交付但未提交"的 316 文件工作树。** UIPLAN §4.1 所有 `DONE scoped` 都只活在这棵树里。

## 1. Q1 + Q2 + Q4 逐项账（只列有证据的；Q1=已完成但带限制未入表，Q2=声称全绿但缺退出码/截图/真实组件）

| # | 类型 | 文件:行 | 原文 | 建议状态 |
|---|---|---|---|---|
| 1 | Q1/Q2 | 本次实跑 | 上节 `TEST_EXIT=1`，8 用例红 / 4 包红；而 `UIPLAN.md:131-142` 逐行写「全仓全绿」「Lead全仓全绿」 | **KNOWN-ISSUE**（阻塞一切 GUI `DONE` 升级） |
| 2 | Q1 | `../dsh-ui` `git status --porcelain` | 316 行改动（214 `??`），HEAD 停在 `3925e84`；所有 scoped 补丁未提交 | **KNOWN-ISSUE** |
| 3 | Q2 | `../dsh-ui/docs/plugin-slot-auth.md:107` | `--- PASS: TestRealBuiltinAssemblySatisfiesSlotAuthorization (0.01s)` | **KNOWN-ISSUE**（该 PASS 已失效，实跑 `expects 1 / got 3`） |
| 4 | Q2 | `UIPLAN.md:165` | 「…全仓owner全绿，真实Win32/物理断链未验」与同行「未修复HEAD Panels=2红」自相矛盾；本次 `TestNoTwoPanelsShareOneSlotAndOrder` 实跑 `ok … 0.132s` | **P2**（该行已过期，需改写） |
| 5 | Q2 | `UIPLAN.md:158` vs `../dsh-ui/docs/progress.md:384` | 表写 `DONE implementation`；交付自述「未跑测试体/headless/真实 OS/全仓测试…不宣称完成」 | **KNOWN-ISSUE** |
| 6 | Q2 | `../dsh-ui/.github/workflows/ci.yml:73,77,81` | `build` / `vet` / `test` 是三条独立 step；`BUILD=0 VET=0` 不蕴含 `TEST=0` | **P1**（三绿口径必须分开写） |
| 7 | Q1 | `../dsh-ui/docs/official-parity-current.md:322-324` | `DONE scoped 26 / PARTIAL 10 / MISSING 16 / N/A 3`，UIPLAN §4.1 无对应行 | **P1**（GO.1 后按 U3 逐部件） |
| 8 | Q1 | `../dsh-ui/docs/official-parity-current.md:258-259` | `WASM 通道 MISSING`、`Web shim MISSING（且不得进主壳）` | **KNOWN-ISSUE**（明确不实现） |
| 9 | Q1 | `../dsh-ui/docs/progress.md:339` | 「仍然缺的：`dsschedule` 的真机截图（有数据没有图）；`dsconversation`/`dsterminal`/`dsquestions` 有真机日志没有截图」 | **P2** |
| 10 | Q1 | `../dsh-ui/docs/visual-audit.md:21,170` | `PASS=20 FAIL=0 SKIP=20 MISSING=0 → PASS`；「只覆盖 1× DPI、暗色主题、两档默认窗口尺寸」 | **KNOWN-ISSUE**（一半判定项无官方值） |
| 11 | Q1 | `DSH-GO/docs/attachments-review.md:158` | 「不证明 W82 可见 UI、真实模型多模态理解质量或完整工作日」 | **KNOWN-ISSUE** |
| 12 | Q1 | `DSH-GO/docs/completion-report-2026-11-05.md:150` vs `:105` | 「附件审查当前仍有 1 条真实 FAIL」 vs 「Lead 复跑 `OK=155 FAIL=0`」 | **P2**（同文档自相矛盾） |
| 13 | Q1 | `DSH-GO/docs/completion-report-2026-11-05.md:143-144` | 证据索引列 `docs/product-acceptance.md`、`docs/attachments-ui.md`，两文件在 DSH-GO 均不存在 | **P2**（断链） |
| 14 | Q1 | `../dsh-ui/docs/settings-gap-0.2.2.md:180` | 「未验证：真宿主、真实模型、OS 窗口与 OS 键盘、真实插件安装/更新、真实凭据、真实日程 mutation、语音设备」 | **P1**（GO.1 后 HOST-SETTINGS-SCHEMA） |
| 15 | Q1 | `../dsh-ui/docs/gui-structure-gap.md:222-224` | 「未启动可见 GUI、OS 窗口、OS 键盘、IME、真实鼠标，未做高DPI/浅色/其他尺寸实测」 | **KNOWN-ISSUE** |

## 2. Q3 · GO.1 暂停 GUI 后「只登记、不现在修」的遗留项

以下已在任务表有行，**不需要新派单**，只需在 `LEAD.md` §2 补 `KNOWN-ISSUE` 决定行：

1. **高 DPI**：`UIPLAN.md:147` GUI-HIGH-DPI-ACCEPTANCE「真实120/144显示器=0…保持UNVERIFIED」——本机无该硬件，非代码缺陷。
2. **真实 OS 键鼠/IME 前台单 owner**：`UIPLAN.md:150` GUI-RIGHTBAR-OS-ACCEPTANCE「BLOCKED · 用户要求全部结构实现后集中测试，已暂停OS输入」。
3. **真实 dshd 200+ 长会话**：`UIPLAN.md:149`「真实OS与真实宿主200+长会话未验」。
4. **跨进程持久化**：`UIPLAN.md:146`「split/float/drag等不画按钮…跨进程持久化未做」。
5. **16 个官方 MISSING 部件**（subagent/skill/workflow-run/loadOlder/workspace CRUD/directory-picker 等）：第 7 行，GO.1 后按 U3 排期。
6. **WASM / Web shim**：第 8 行，属"明确不做"而非缺陷。
7. **4 个插件缺真机截图**：第 9 行，只影响证据完整度，不影响功能。

## 3. 唯一需要现在处理的结论

**GUI 不能以「全仓全绿」签收。** 当前工作树 `go test ./...` = 1（8 红），HEAD 绿但未包含交付；按 `AGENTS.md:68`「无未登记的可见缺陷，才允许把任务标 `DONE`」，上表 1/2/3/5 必须先由 Lead 写入 `LEAD.md` §2 的 `KNOWN-ISSUE` 行，再决定是「回滚到 HEAD 口径」还是「先修红再提交」。本审计不修任何一项。
