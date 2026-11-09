# Viva 完善方案

> 日期：2026-11-09 04:0x（Asia/Shanghai）。对应 `AGENTS.md` §0 第 0 条「双线并行」。
> 定位：**我们不是 Viva 的维护者，是它最重的产品用户。** 完善它的方式不是接管开发，而是**把真实产品跑出来的问题变成上游的修复和测试**。
> 状态：**方案待拍板**，未派单。

---

## 0. 一句话

**Viva 缺的不是功能（我们 704 处 import、零 workaround），缺的是「真实负载 + Windows 平台」这两种场景的测试与文档。** 我们能给的恰好是这两样——**不是插件系统**（那属于应用层，见 §2）。

---

## 1. 现状（2026-11-09 实测）

### 1.1 我们的用量

| 维度 | 数字 |
|---|---|
| 直接 `import` Viva 的文件 | **234 个** |
| 总 import 处数（含子包） | **704 处** |
| `ui` | 238 |
| `flex` | 173 |
| `app` | 95 |
| `headless` | 62（全部在 `*_headless_test.go`） |
| `components` | 24 |
| `markdown` | 10 |
| `text` | 2 |
| 代码规模 | 46,227 行 Go 非测试 + 260 个测试文件 |
| 插件 | 24 个，完整 `pluginapi`（`Plugin`/`Context`/`Painter`/`Primitives`/`Panels`/`KeyHandler`…） |
| **行内 TODO/FIXME/HACK** | **0**（产品代码 grep 无命中） |
| **模块形态的 workaround** | **明显存在**：`internal/mdlite` 1,076 行接管 Clip/平移/滚动条、`shell_overlay` 436 行、`os_hotkeys` 244 行（`WH_KEYBOARD_LL` 低级钩子）、`frame_chrome` 585 行 |
| 依赖形态 | `v0.5.1`，**无 fork、无 vendor**，只有示例插件的 `replace` |

> ⚠️ **统计口径**：`dsh-ui/tmp` 里有 3.1G 历史副本，任何行数/import 统计必须 `--exclude-dir=tmp`。含 tmp 会变成 85,069 行 / 427 测试 / 268 import。

**关键结论（2026-11-09 T1 审计修正）**：~~零 workaround 说明 API 够用~~ **这个推论不成立**。行内注释确实干净，但**真正的 workaround 在模块形态上**——我们为了补 `flex` 的横向滚动、IME 组合态键盘、层合成顺序，各写了几百到上千行的自建层。**这些才是真缺口，也是能提上游的东西。**

### 1.2 上游状态

| 项 | 事实 | 对我们的含义 |
|---|---|---|
| 仓库年龄 | 2026-10-22 创建（**17 天**） | 极早期，API 会变 |
| 版本节奏 | 15 个版本 / 17 天 | 小 PR 才跟得上 |
| 贡献者 | `t8y2` 229 commits（**78%**），5 个外部贡献者 | bus factor = 1，但有社区 |
| 参考平台 | **macOS**（release notes 每版都写） | **Windows 是短板，也是我们的强项** |
| 许可 | Apache-2.0 | 可 fork、可改 |
| 工程实践 | `apidiff` 门禁、`SHA256SUMS`、六平台 CI、双语 notes | 值得信任，能提规范 PR |

---

## 2. 范围判据（**先看这节，再看任务**）

### 2.1 职责边界

上游 `CONTRIBUTING.md` 的边界原文：

> **Reusable capability** — windows, input, text, layout, components, rendering, accessibility, lifecycle, headless control — belongs in Viva.
> **Product behaviour** — a protocol, a storage schema, a workflow, a specific feature of one application — stays in the application.

**唯一判据：把 DSH 的名字去掉，这个改动还成立吗？**

| 自检 | 结论 |
|---|---|
| ✅ 成立（讲的是窗口/输入/文字/布局/渲染/无障碍/生命周期/headless） | 是框架能力，可以提 |
| ❌ 不成立（讲的是会话、审批、插件、Transport、DSH 的工作流） | 是产品行为，**不许提** |

**反面例子（我们自己的）**：`pluginapi` 里的 `Conversation`、`TurnEvent`、`AttachmentItem`、`OutgoingMessage` 全是 DSH 业务概念。**「插件系统」是产品架构，不是引擎能力**——即使写成文档，也是在把应用层设计强加给框架。

### 2.2 查重方法（**两次踩坑的教训**）

判断"上游有没有"必须**全仓搜索**，四个位置缺一不可：

```bash
M=$(go env GOMODCACHE)/github.com/vivaui/viva@<版本>
ls  $M/docs/                              # 1. 仓库文档（怎么构建框架）
ls  $M/website/content/guides/en/         # 2. 用户指南（怎么用框架）← 最容易漏
grep -rn "关键词" $M/headless/*.go        # 3. 包 godoc
grep -n "关键词" $M/README.md              # 4. README
```

**两次真实事故**：

| 日期 | 错在哪 | 代价 |
|---|---|---|
| 2026-11-09 | 只看 GitHub Release 页面，没核 `docs/releases/<version>.md` 原文，把 v0.2.4 的声明当成 v0.5.1 的 | issue #180 提错，已关闭 |
| 2026-11-09 | 只看 `docs/`，没看 `website/content/guides/en/`，误判 headless 和 Windows 构建是缺口 | V-03/V-04 作废，未提 |

**规则：`docs/` 和 `website/content/guides/` 是两套文档，任何"缺文档"的结论必须两层都查过才能下。**

---

## 3. 四个贡献方向（按 ROI 排序）

### P0 · 文档

**结论：上游两层文档已经相当完整，P0 只剩 1 项。**

| 任务 | 内容 | 状态 | 依据 |
|---|---|---|---|
| **V-01** | 跨版本升级指南（各版本 API 变化 + 迁移） | ✅ **已提** [PR #181](https://github.com/vivaui/viva/pull/181) | `docs/` 与 `website/content/guides/en/` 均无 upgrading 页 |
| ~~V-02~~ | ~~插件系统指南~~ | ❌ **越界删除** | `pluginapi` 是应用层架构，`CONTRIBUTING.md` 明确 product behaviour 留在应用 |
| ~~V-03~~ | ~~headless 用法~~ | ❌ **已有** | `guides/en/testing.md` 已讲 `-tags vivahead`、`VIVA_HEADLESS=1`、`RunCanvas`、行协议 |
| ~~V-04~~ | ~~Windows 构建~~ | ❌ **已有** | `guides/en/build-and-package.md` 已讲 `--target windows`、`-trimpath`、GUI subsystem、NSIS、签名 |

**P0 到此为止。** 想再找文档缺口，必须先跑 P1 的诊断——**没有真实问题，就没有可写的文档**。

### P1 · 诊断（✅ 2026-11-09 04:28 完成）

**团队 `viva-diagnosis` 四个任务全部完成**：

| 文件 | 内容 |
|---|---|
| [`tmp/viva-diag/static-report.md`](../tmp/viva-diag/static-report.md) | T1 静态审计（505 行） |
| [`tmp/viva-diag/bench-results.md`](../tmp/viva-diag/bench-results.md) | T2 性能基准（464 行 / 58 个 benchmark） |
| [`tmp/viva-diag/scenario-matrix.md`](../tmp/viva-diag/scenario-matrix.md) | T3 场景矩阵（275 行 / 19 组证据） |
| [`tmp/viva-diag/FINAL.md`](../tmp/viva-diag/FINAL.md) | T4 上游核查 + 汇总（564 行） |

### P2 · 可提清单（**8 条**：FINAL.md 的 U-01…U-07 + 本轮新挖的 U-08；草稿在 `$DSH_HOME/outbox/`）

| ID | 级别 | 问题 | 动作 | 状态 |
|---|---|---|---|---|
| **U-01** | P0 | `textcache` 512/代 + 纯两代轮换 → 每行 106× | issue | ✅ **[#184](https://github.com/vivaui/viva/issues/184)** |
| **U-02** | P0 | `ScrollState` 无法在布局前问「本帧会钉到底」→ 长会话首帧闪回顶部 | PR | ✅ **[#183](https://github.com/vivaui/viva/pull/183)** +28/−1 |
| **U-03** | P1 | 同一 layer 内文字恒在所有矩形之上 | issue | ✅ **[#185](https://github.com/vivaui/viva/issues/185)** |
| **U-04** | P2 | `CheckLayout` 两两重叠 O(n²) | issue | 草稿（**已核 `main@0c11a3d` 仍成立**：`layout.go:51/93/112`） |
| **U-05** | P2 | `MarkedSelection` 的「相对 Text」语义没进公开 godoc | PR | ✅ **[#182](https://github.com/vivaui/viva/pull/182)** +2 |
| **U-06** | P2 | `ScrollState.Wheel` 吞横向滚轮 | issue | 草稿（**已核仍成立**：`ui/scroll.go:119`） |
| **U-07** | P3 | headless 截图在 Windows/Linux 不含编辑器覆盖层 | 文档 PR（并入 U-05） | 草稿 |
| **U-08** | P2 | Windows 上 3 个测试恒红（ACL 继承 + MSYS 路径），任何人从红树起步 | issue | 草稿（**已核 `main@0c11a3d` 仍红**） |

### P2b · 必须撤回的 **10 条**（N-01…N-10，T4 逐条核过源码）

> ⚠️ **修正**：本会话早期只汇报了 N-01/N-02/N-03 三条，**实际 FINAL.md §3 有 10 条**。完整清单见 [`tmp/viva-diag/FINAL.md`](../tmp/viva-diag/FINAL.md) §3。其余七条：N-04（DirectWrite 324× 并入 U-01）、N-05（`estimateMsgHeight` 产品层）、N-06（Paint 线程已文档化）、N-07（`Frame.Bounds` 设计意见）、N-08（窗口旧帧残留已由 #159/#158 修）、N-09（`OnScroll==nil` 半对）、N-10（类型断言 72 处全在自家 DI）。

| 撤回项 | 真相 |
|---|---|
| T1 的 S-09（IME 组合期收不到键） | **设计如此**：`native.m:783` 明写输入法组合期间持有整个键盘；`text-input.md:114` 用户文档也写了 |
| T1 的 S-03（TextStyle 没有公开入口） | **前提错**：`ui/canvas.go:1184 Canvas.Label(value, bounds, TextStyle)` 就是公开入口 |
| T3 的 F5（headless Resize 不触发回调） | **误报**：`app/driver.go:238-253` 明确会调；T3 自己的 `result.json` 里 `frame_callbacks: 2` |

> 这三条证明了诊断团队的验收价值：**报告不能照抄，必须逐条开源码核**。2026-11-09 已因此避免重演 #180（提错 issue）。

### P3 · API 反馈（已并入 §P2 的可提清单）

704 处 import 里"用起来费劲"的地方已经收集完毕（见 T1 的 S-01/S-03 等）。**API 形状是上游的设计权**——我们只报现象 + 给可选的修法，不动手改设计。

---

## 4. 诊断方法（怎么找真问题）

**没有诊断就没有真问题。** 三种手段，按成本排序：

### 4.1 静态扫描（半天）

```bash
# 1. 我们写得最别扭的地方：连续 3 个以上链式调用、超过 120 字符的行
grep -rn --include="*.go" -E "\.\w+\([^)]*\)\.\w+\([^)]*\)\.\w+\([^)]*\)\.\w+\(" plugins/ | head -20

# 2. 我们重复实现的模式（说明上游缺抽象）
grep -rn --include="*.go" "func.*Row().*Width" plugins/ | wc -l
```

### 4.2 性能基准（1–2 天）

```bash
cd ../dsh-ui
go test -bench=. -benchmem -run=^$ ./plugins/dsconversation ./plugins/dscomposer
```

把「1000 条消息 + 24 个插件挂载的完整应用 + 4K DPI」跑成数字。**上游要的就是这种真实负载**——他们的示例只有几十个控件。

### 4.3 场景矩阵（2–3 天）

| 场景 | 为什么重要 |
|---|---|
| 1000 条消息滚动 | 我们的主场景，上游没测过 |
| 中文输入法（IME）组合输入 | Windows 独有，历史踩过坑 |
| 4K 高 DPI 缩放 | 上游只测了 macOS |
| 大量控件同时渲染 | 几百个 `flex.Box` 下的布局/绘制开销（**测框架，不测我们的插件调度**） |
| 断线重连时的 UI 状态 | 我们的 Transport 特有 |

---

## 5. 协作纪律

| 规则 | 理由 |
|---|---|
| **小 PR 优先**（< 300 行） | 上游 17 天 15 版本，大 PR 必过期 |
| **一次一件事** | 文档 PR 不夹带代码修复 |
| **先 issue 后 PR**（除非文档） | `CONTRIBUTING.md`：大改动先议 |
| **PR 分支永不强推** | `AGENTS.md` 铁律 8；评审记录必须连续 |
| **跟着上游节奏走** | 他们快，我们也要快；跟不上就等下一版 |
| **不发「设计意见」类 issue** | 除非有真实产品证据 |

**工具**：一律 `gh` CLI（本机 `github` 工具 POST 返回 422）。

---

## 6. 验收与退出

### 6.1 做到什么程度算完成

**不是「贡献了多少行」，而是这三条：**

1. **升级不再靠猜**：每次 `go get -u github.com/vivaui/viva` 后，`CGO_ENABLED=0 go build/vet/test` 全绿 = 完成。若有 breaking，`docs/upgrading.md` 里能查到。
2. **Windows 场景有上游测试兜底**：V-06 的用例被上游接受后，我们以后再升级，平台退化会被上游 CI 先抓到。
3. **框架层文档齐全**：headless / Windows 构建 / 跨版本升级三份合并后，下一个用 Viva 的人不用重踩我们的坑。

### 6.2 什么时候该停

| 信号 | 动作 |
|---|---|
| 上游 2 周无响应 | 转本地兜底 + 登记，不再提 |
| 我们的 PR 连续 2 个被拒 | 重新评估方向，别再硬提 |
| 上游开始做插件系统（若将来被认定为框架能力） | 我们只做**使用者反馈 + 测试**，不提交设计 |

---

## 7. 不做什么

- ❌ **不 fork**：除非上游明确停更（现在 17 天 15 版，离停更很远）
- ❌ **不改上游架构**：API 形状、包边界是他们的设计权
- ❌ **不贡献插件系统**：`pluginapi` 是**应用层**架构（`Conversation`/`TurnEvent`/`AttachmentItem` 全是 DSH 概念），上游 `CONTRIBUTING.md` 明确「product behaviour stays in the application」。**文档也不行**——文档同样是在推销应用层设计
- ❌ **不把 DSH 环境问题当成 Viva 问题**：本机 ACL 完整性级别、MinGW 路径、沙箱策略都是我们自己的环境，不是框架缺陷
- ❌ **不为了贡献而贡献**：主战场永远是产品本身
- ❌ **不凭印象提 issue**：2026-11-09 已因此提错一个（#180，把 v0.2.4 的声明当成 v0.5.1 的，已关闭）
- ❌ **不越过 `CONTRIBUTING.md` 的边界清单**：提交前必须过 §2 的「去掉 DSH 名字还成立吗」自检

---

## 8. 排期建议

| 周 | 任务 | 产出 |
|---|---|---|
| **W1** | **V-05 诊断**（静态扫描 + benchmark） | 热点数字 + 真实问题清单 |
| **W2** | **V-06 场景矩阵**（1000 消息 / IME / 4K DPI / 大量控件 / 断线重连） | 上游 issue（**有复现才提**） |
| **W3** | **V-07 按清单修复** | 小 PR（每条 < 300 行） |

**每周围绕 1–2 个 PR，不铺开。** 我们的主战场是产品，Viva 是顺手加固。

> **排期前提**：文档线（P0）已经走完——V-01 是唯一真缺口且已提。**在 V-05 跑出真实问题之前，不应再提任何新的 issue 或 PR。**

**每周围绕 1–2 个 PR，不铺开。** 我们的主战场是产品，Viva 是顺手加固。

---

## 9. 与主线的边界

| 谁 | 做什么 |
|---|---|
| **产品线**（dsh-ui / dshd） | 主战场，不受本方案影响 |
| **Viva 线** | 只做「产品踩到的坑」的上游化，**不主动找活** |
| **判据** | 一个 Viva 任务值不值得做，看它是否**直接降低我们升级的风险** |

**一句话收口：Viva 线是产品的保险，不是第二份工作。**
