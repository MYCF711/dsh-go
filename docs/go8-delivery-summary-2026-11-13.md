# DSH-GO 交付总结（2026-11-13）

> 目标（goals.json）：基于官方 DSH `0.2.2-rc.2` 推进 DSH-GO 宿主稳定版 → 宿主层完备后转入 GUI 线。
> 本文是**证据索引**：每条结论都指向可复跑的命令或可读的文件。

---

## 一、宿主线（已完成并发布）

| 判据 | 结果 | 证据 |
|---|---|---|
| 方法面 | **223/223** 逐条有生产落点 | `docs/evidence/official-method-coverage-0.2.2-rc.2.md`（机器生成） |
| base 插件面 | **99** 个，新增 `agent-team` / `tool-agent-team` 已实现 | `docs/evidence/official-base-plugins-0.2.2-rc.2.txt` |
| 参数总账 | **405** 条（`officialCoverage`） | `docs/parameter-register.json` |
| 四门禁 | build / vet / test（90 包）/ `-race`（90 包）**全绿** | `scripts/check.sh` ALL GREEN |
| 稳定版 | **`0.2.2-go.3`** | Tag + [Release](https://github.com/MYCF711/dsh-go/releases/tag/v0.2.2-go.3) + 7 附件 |

**rc.1 → rc.2 硬数字**：方法 193→**223**（+35/−5）· base 插件 97→**99** · 客户端 UI 插件 65→**66** · `hostProtocolVersion` **4**（未变）。

**本版新做的真实能力**：`sessionStats` 投影（8 字段）、`tokenUsage` 投影（四桶累计，真机 `outputTokens == decodeTokens == 114` 交叉自证）、`team/mode` 事件、409 结构化 `details.reason`（9 取值）、Office 真渲染、Forge GraphQL 审阅面、`retryPolicy` 可写、归档清理取消、terminal-panel 父子桥、CUA 9 方法。

---

## 二、GUI 线（结构树复刻 + 素材复用 + 最小自制）

| 项 | 结果 |
|---|---|
| 插件数 | **25** 个 `ds*` 插件 |
| 官方槽位 | **54 条 100% 有实现节点**（74 节点树，四种布局逐一断言） |
| 几何 | **145 行单一常量表**（aligned / derived / deviation 三态），绘制代码零魔法数 |
| 素材 | 官方 sprite 逐点转写（`goal-outline` / `gauge-outline` / `database-outline` 等，带 `file:line` + sha256 + viewBox）；**derived 项明确标注** |
| transcript | **19/20 节点真渲染**（`submitted-plan` 按官方零渲染） |
| composer | 官方 `conversation.composer` 座位**真 dispatch**（`main.composer` 0 次）+ 子槽 10/10 |
| 设置 | 5 section / 13 general item（含条件项 `desktop-update-channel`） |
| 真机 | 真 `dshd serve` + 真 apiclient → **83 条真实帧**渲染到画布；断线重连真 409 `stale_epoch` → 重建 72 帧 |

---

## 三、Viva 引擎线（v0.2.3 → **v0.6.0**，跨 11 个版本）

| 阶段 | 量 | 提交 |
|---|---|---|
| V-06-1 类型感知改名 | `flex`→`ui` 382 处 / `ui`→`gfx` 1737 处 / **188 文件** | `d826b18` |
| V-06-2 frame 方法 | **925 处 / 89 文件**，全树 0 残留 | `ab2b9db` |
| V-06-3 Page 迁移 | **90 文件**（build 4 + vet 20 全清） | `3555eb0` |
| V-06-4 控件语义 | **11 形态 / 323 可点元素 / 56 `.Clicked()`**，失去命中区 **0** | `123ade4` |
| V-06-5 视觉门禁 | 9 形态 / 46 区域，27 个 **0 像素差** | `ed547ba` |
| V-06-5b seeding | 连跑 3 次 **0 像素差** | `630065d` |

**两次跨阶段抓到的真缺陷**：① "先记偏移再恢复"把阅读位置覆盖成 0（变异验证：对调两行 → 红 `500≠120`）② `Popup` 翻面第一版首帧压住 trigger（既有测试当场抓到）。

**上游 issue**：
- [#184](https://github.com/vivaui/viva/issues/184) Windows 文本整形 512 悬崖 — **open**，已回 v0.6.0 源码级证据
- [#239](https://github.com/vivaui/viva/issues/239) `Editor.String()` 并发 — closed **not planned**（我们的 `pendingDraft` 兜底**就是**上游认可的正确用法）
- [#240](https://github.com/vivaui/viva/issues/240) `upgrading.md` 漏写 — closed **fixed**（随 v0.6.1；上游还提示了 `ReplaceBlockElements`→`ReplaceCellGlyphs`，本仓 **0 处**受影响）

---

## 四、门禁（最终态）

```
宿主  main=1adb2cb   build=0  vet=0  test=0(90包)  race=0(90包)  check.sh ALL GREEN
GUI   main=62e60d7   build=0  vet=0  test=0(35包)  race=0(35包)  guard-selftest OK=9 FAIL=0
                        parity-count / doc-links / scripts-index 全 PASS
```

---

## 五、剩余缺口（不阻塞，已登记）

1. **围栏代码块视觉覆盖**：只能由 `demo-tool` 形态覆盖；要全覆盖需先把消息流改**顶端对齐**
2. **`-shot` 注入不了鼠标**：三个"必须点开"的浮层（活动胶囊弹窗 / 子代理目录 / `dsopen`）无像素基线——命中区由 V-06-4 守
3. **CJK + 行内代码胶囊** ~1/5 概率整行水平抖 1px（上游文本测量；已从种子规避）
4. **宿主无 updater 通道** → `DesktopUpdateIndicator` 第 3 格产品态不出现
5. **`spike/*` 五个子模块**仍 pin viva v0.2.3（不在门禁内）
6. **GUI 旧三栏座位**不派发子槽
7. **目标条 / 系统主题跟随**不在视觉基线内

---

## 六、还需要用户拍板的

- **`TUI-GO.lnk` 桌面双击确认**（需可见桌面人工点一次）
- **下一个版本号**（GUI 侧要不要跟一个 `-go.2`）
